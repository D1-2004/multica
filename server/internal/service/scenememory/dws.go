package scenememory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/dwsclient"
	"github.com/multica-ai/multica/server/internal/integrations/agentidentityhsf"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type identityLookup interface {
	GetAgentDingTalkIdentity(ctx context.Context, arg db.GetAgentDingTalkIdentityParams) (db.AgentDingtalkIdentity, error)
	GetAgentSceneMemoryFlags(ctx context.Context, id pgtype.UUID) (db.AgentSceneMemoryFlags, error)
}

type DWSRangeConfig struct {
	Queries         identityLookup
	AgentIdentity   *agentidentityhsf.Client
	BaseURL         string
	BaseURLProvider func() string
	ClientSecret    string
	CLIPath         string
	HTTPClient      *http.Client
}

type DWSRangeReader struct {
	queries identityLookup
	issuer  *agentidentityhsf.Client
	redeem  dwsclient.Redeemer
	cli     dwsclient.CLI
}

const (
	maxHistoryPages = 8
	historyLookback = 14 * 24 * time.Hour
)

func NewDWSRangeReader(cfg DWSRangeConfig) *DWSRangeReader {
	client := cfg.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	path := strings.TrimSpace(cfg.CLIPath)
	if path == "" {
		path = "dws"
	}
	return &DWSRangeReader{
		queries: cfg.Queries,
		issuer:  cfg.AgentIdentity,
		redeem: dwsclient.Redeemer{
			BaseURL:         cfg.BaseURL,
			BaseURLProvider: cfg.BaseURLProvider,
			Client:          client,
			UserAgent:       "dt-fde-multica/scene-memory",
		},
		cli: dwsclient.CLI{Path: path, ClientSecret: strings.TrimSpace(cfg.ClientSecret)},
	}
}

func (r *DWSRangeReader) Read(ctx context.Context, row db.SceneMemory) ([]HistoryEvent, error) {
	if r == nil || r.queries == nil || r.issuer == nil {
		return nil, errors.New("scene memory DWS reader is not configured")
	}
	identity, err := r.queries.GetAgentDingTalkIdentity(ctx, db.GetAgentDingTalkIdentityParams{
		WorkspaceID: row.WorkspaceID, AgentID: row.AgentID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, &FlushError{
				Code: ErrorRouteInactive,
				Err:  fmt.Errorf("DWS identity is not bound"),
			}
		}
		return nil, fmt.Errorf("resolve DWS identity: %w", err)
	}
	if strings.TrimSpace(identity.OrgID) != strings.TrimSpace(row.OrgID) {
		return nil, &FlushError{
			Code: ErrorRouteInactive,
			Err:  fmt.Errorf("DWS identity org does not match scene"),
		}
	}
	runID := "scene-memory-dws-" + uuid.NewString()
	issued, err := r.issuer.CreateContext(ctx, agentidentityhsf.CreateContextRequest{
		RequestID: runID, TaskID: runID,
		AgentID:     util.UUIDToString(row.AgentID),
		RuntimeType: "SERVER", RuntimeID: runID,
		Reason: "Multica scene memory DingTalk history",
		Source: map[string]string{"app": "dt-fde-multica", "identity_source": "scene_memory_dws"},
		UID:    identity.DwsUid, OrgID: identity.OrgID, TTLSeconds: 120,
	})
	if err != nil {
		return nil, fmt.Errorf("issue DWS history identity: %w", err)
	}
	credential, err := r.redeem.Redeem(ctx, issued.ContextToken)
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "multica-scene-memory-dws-")
	if err != nil {
		return nil, errors.New("create isolated DWS history directory")
	}
	defer func() { _ = os.RemoveAll(dir) }()
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, errors.New("secure isolated DWS history directory")
	}
	if err := r.cli.Exchange(ctx, dir, credential); err != nil {
		return nil, err
	}
	limit := 30
	if row.SceneKind == KindGroup {
		limit = 60
	}
	now := time.Now().UTC()
	bootstrap := false
	if flags, flagErr := r.queries.GetAgentSceneMemoryFlags(ctx, row.AgentID); flagErr == nil {
		bootstrap = flags.BootstrapEnabled
	}
	lookback := HistoryLookback(row, bootstrap, now)
	before := now.Add(time.Minute)
	seen := make(map[string]struct{})
	out := make([]HistoryEvent, 0, limit)
	oldest := before
	hitPageCap := false
	for page := 0; page < maxHistoryPages; page++ {
		raw, err := r.cli.List(ctx, dir, dwsclient.ListRequest{
			ConversationID: row.SceneKey,
			Before:         before,
			Direction:      "older",
			Limit:          limit,
		})
		if err != nil {
			return nil, err
		}
		parsed, err := parseDWSPage(raw, identity.DwsUid)
		if err != nil {
			return nil, err
		}
		if parsed.RawCount == 0 {
			break
		}
		for _, event := range parsed.Events {
			if event.EvidenceID != "" {
				if _, ok := seen[event.EvidenceID]; ok {
					continue
				}
				seen[event.EvidenceID] = struct{}{}
			}
			out = append(out, event)
		}
		if !parsed.Oldest.IsZero() && parsed.Oldest.Before(oldest) {
			oldest = parsed.Oldest
		}
		if parsed.RawCount < limit || !oldest.After(lookback) {
			break
		}
		if page == maxHistoryPages-1 {
			hitPageCap = true
			break
		}
		before = oldest
	}
	if historyHasGap(row, oldest, hitPageCap) {
		return nil, &FlushError{
			Code: ErrorIncomplete,
			Err:  fmt.Errorf("history page cap left a gap behind the cursor"),
		}
	}
	return filterAfterLookback(out, lookback), nil
}

func filterAfterLookback(events []HistoryEvent, lookback time.Time) []HistoryEvent {
	if lookback.IsZero() {
		return events
	}
	out := make([]HistoryEvent, 0, len(events))
	for _, event := range events {
		if event.OccurredAt.Before(lookback) {
			continue
		}
		out = append(out, event)
	}
	return out
}

func HistoryLookback(row db.SceneMemory, bootstrap bool, now time.Time) time.Time {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if bootstrap && !row.BootstrappedAt.Valid {
		lookback := now.UTC().Add(-historyLookback)
		if row.LastTriggerAt.Valid && !row.LastTriggerAt.Time.IsZero() {
			at := row.LastTriggerAt.Time.UTC()
			if at.Before(lookback) {
				lookback = at
			}
		}
		return lookback
	}
	if need := historyNeedReach(row); !need.IsZero() {
		return need
	}
	if row.LeaseTargetThroughAt.Valid && !row.LeaseTargetThroughAt.Time.IsZero() {
		return row.LeaseTargetThroughAt.Time.UTC().Add(-time.Minute)
	}
	return now.UTC().Add(-time.Hour)
}

func historyNeedReach(row db.SceneMemory) time.Time {
	var need time.Time
	if row.SourceCursorAt.Valid && !row.SourceCursorAt.Time.IsZero() {
		need = row.SourceCursorAt.Time.UTC()
	}
	if row.LastTriggerAt.Valid && !row.LastTriggerAt.Time.IsZero() {
		at := row.LastTriggerAt.Time.UTC()
		if need.IsZero() || at.Before(need) {
			need = at
		}
	}
	return need
}

func historyHasGap(row db.SceneMemory, oldest time.Time, hitPageCap bool) bool {
	if !hitPageCap {
		return false
	}
	need := historyNeedReach(row)
	if need.IsZero() {
		return false
	}
	return oldest.After(need)
}

type dwsPage struct {
	Events   []HistoryEvent
	RawCount int
	Oldest   time.Time
}

func parseDWSEvents(raw []byte) ([]HistoryEvent, error) {
	page, err := parseDWSPage(raw, "")
	if err != nil {
		return nil, err
	}
	return page.Events, nil
}

type dwsListMessage struct {
	Content       string `json:"content"`
	Text          string `json:"text"`
	CreateTime    string `json:"createTime"`
	OpenMessageID string `json:"openMessageId"`
	MessageID     string `json:"messageId"`
	Sender        string `json:"sender"`
	SenderID      string `json:"senderId"`
	SenderOpenID  string `json:"senderOpenId"`
	IsSelf        *bool  `json:"isSelf"`
	Self          bool   `json:"self"`
}

func parseDWSPage(raw []byte, agentUID string) (dwsPage, error) {
	var payload struct {
		Success   bool             `json:"success"`
		ErrorCode string           `json:"errorCode"`
		Messages  []dwsListMessage `json:"messages"`
		Result    json.RawMessage  `json:"result"`
	}
	if json.Unmarshal(raw, &payload) != nil {
		return dwsPage{}, errors.New("decode DWS conversation history response")
	}
	messages := payload.Messages
	if len(messages) == 0 && len(payload.Result) > 0 && payload.Result[0] == '{' {
		var nested struct {
			Messages []dwsListMessage `json:"messages"`
		}
		if json.Unmarshal(payload.Result, &nested) == nil {
			messages = nested.Messages
		}
	}
	if !payload.Success && len(messages) == 0 {
		return dwsPage{}, fmt.Errorf("DWS conversation history query rejected: %s", dwsclient.SafeCode(payload.ErrorCode))
	}
	page := dwsPage{RawCount: len(messages)}
	for _, message := range messages {
		occurred := parseDWSTime(message.CreateTime)
		if page.Oldest.IsZero() || occurred.Before(page.Oldest) {
			page.Oldest = occurred
		}
		content := strings.TrimSpace(message.Content)
		if content == "" {
			content = strings.TrimSpace(message.Text)
		}
		if content == "" {
			continue
		}
		content = redactSecrets(content)
		speaker := strings.Join(strings.Fields(message.Sender), " ")
		if speaker == "" {
			speaker = "dingtalk"
		}
		evidenceID := strings.TrimSpace(message.OpenMessageID)
		if evidenceID == "" {
			evidenceID = strings.TrimSpace(message.MessageID)
		}
		page.Events = append(page.Events, HistoryEvent{
			EvidenceID: evidenceID,
			OccurredAt: occurred,
			Speaker:    speaker,
			Content:    content,
			Self:       messageIsSelf(message.IsSelf, message.Self, agentUID, message.SenderID, message.SenderOpenID),
		})
	}
	return page, nil
}

func messageIsSelf(flag *bool, self bool, agentUID, senderID, senderOpenID string) bool {
	if flag != nil {
		return *flag
	}
	if self {
		return true
	}
	agentUID = strings.TrimSpace(agentUID)
	if agentUID == "" {
		return false
	}
	return strings.TrimSpace(senderID) == agentUID || strings.TrimSpace(senderOpenID) == agentUID
}

func parseDWSTime(raw string) time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Now().UTC()
	}
	layouts := []string{
		time.RFC3339,
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05",
	}
	loc := time.FixedZone("Asia/Shanghai", 8*60*60)
	for _, layout := range layouts {
		if t, err := time.ParseInLocation(layout, raw, loc); err == nil {
			return t.UTC()
		}
		if t, err := time.Parse(layout, raw); err == nil {
			return t.UTC()
		}
	}
	return time.Now().UTC()
}
