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
	historyLookback    = 14 * 24 * time.Hour
	historyReadTimeout = 40 * time.Second
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

func (r *DWSRangeReader) Read(ctx context.Context, row db.SceneMemory) (HistoryPage, error) {
	if r == nil || r.queries == nil || r.issuer == nil {
		return HistoryPage{}, errors.New("scene memory DWS reader is not configured")
	}
	readCtx, cancel := context.WithTimeout(ctx, historyReadTimeout)
	defer cancel()
	identity, err := r.queries.GetAgentDingTalkIdentity(readCtx, db.GetAgentDingTalkIdentityParams{
		WorkspaceID: row.WorkspaceID, AgentID: row.AgentID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return HistoryPage{}, &FlushError{
				Code: ErrorRouteInactive,
				Err:  fmt.Errorf("DWS identity is not bound"),
			}
		}
		return HistoryPage{}, fmt.Errorf("resolve DWS identity: %w", err)
	}
	if strings.TrimSpace(identity.OrgID) != strings.TrimSpace(row.OrgID) {
		return HistoryPage{}, &FlushError{
			Code: ErrorRouteInactive,
			Err:  fmt.Errorf("DWS identity org does not match scene"),
		}
	}
	mint := func(ctx context.Context, id dwsclient.Identity) (dwsclient.Credential, error) {
		runID := "scene-memory-dws-" + uuid.NewString()
		issued, err := r.issuer.CreateContext(ctx, agentidentityhsf.CreateContextRequest{
			RequestID: runID, TaskID: runID,
			AgentID:     id.AgentID,
			RuntimeType: "SERVER", RuntimeID: runID,
			Reason: "Multica scene memory DingTalk history",
			Source: map[string]string{"app": "dt-fde-multica", "identity_source": "scene_memory_dws"},
			UID:    id.UID, OrgID: id.OrgID, TTLSeconds: 120,
		})
		if err != nil {
			return dwsclient.Credential{}, fmt.Errorf("issue DWS history identity: %w", err)
		}
		return r.redeem.Redeem(ctx, issued.ContextToken)
	}
	// The SDK transport reuses the identity's shared token and mints only
	// without one; the dws CLI exchanges a credential for this read.
	reader := dwsclient.Identity{AgentID: util.UUIDToString(row.AgentID), UID: identity.DwsUid, OrgID: identity.OrgID}
	dir, cleanup, shared, err := dwsclient.Shared{CLI: r.cli}.Open(readCtx, reader, mint)
	if shared {
		if err != nil {
			return HistoryPage{}, err
		}
		defer cleanup()
	} else {
		credential, err := dwsclient.Shared{CLI: r.cli}.Mint(readCtx, reader, mint)
		if err != nil {
			return HistoryPage{}, err
		}
		dir, err = os.MkdirTemp("", "multica-scene-memory-dws-")
		if err != nil {
			return HistoryPage{}, errors.New("create isolated DWS history directory")
		}
		defer func() { _ = os.RemoveAll(dir) }()
		if err := os.Chmod(dir, 0o700); err != nil {
			return HistoryPage{}, errors.New("secure isolated DWS history directory")
		}
		if err := r.cli.Exchange(readCtx, dir, credential); err != nil {
			return HistoryPage{}, err
		}
	}
	// A claim commits one DWS page. Its exact continuation is committed with
	// the memory, so a page limit or a restart never loses newer evidence.
	limit := flushBatchEvents
	if row.SceneKind == KindGroup {
		limit = 30
	}
	bootstrap := false
	if flags, flagErr := r.queries.GetAgentSceneMemoryFlags(readCtx, row.AgentID); flagErr == nil {
		bootstrap = flags.BootstrapEnabled
	}
	after := historyStartAfter(row, bootstrap, time.Now().UTC())
	raw, err := r.cli.List(readCtx, dir, dwsclient.ListRequest{
		ConversationID: row.SceneKey,
		Before:         after,
		Direction:      "newer",
		Limit:          limit,
	})
	if err != nil {
		return HistoryPage{}, err
	}
	page, err := parseDWSPage(raw, identity.DwsUid, identity.AccountDisplayName)
	if err != nil {
		return HistoryPage{}, err
	}
	if !page.PaginationKnown {
		return HistoryPage{}, &FlushError{Code: ErrorIncomplete, Err: errors.New("DWS history page is missing pagination metadata")}
	}
	if page.HasMore && !page.NextCursor.After(after) {
		return HistoryPage{}, &FlushError{Code: ErrorIncomplete, Err: errors.New("DWS history continuation did not advance")}
	}
	return page, nil
}

func historyStartAfter(row db.SceneMemory, bootstrap bool, now time.Time) time.Time {
	if progress, ok := restoredHistoryProgress(row); ok {
		return progress.After
	}
	// Native message times have second precision; include the entire first
	// second, including a late trigger behind the monotonic source cursor.
	return HistoryLookback(row, bootstrap, now).Truncate(time.Second).Add(-time.Second)
}

func deadlineRemaining(ctx context.Context) (time.Duration, bool) {
	if ctx == nil {
		return 0, false
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		return 0, false
	}
	return time.Until(deadline), true
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
		if row.PendingFromAt.Valid && !row.PendingFromAt.Time.IsZero() {
			at := row.PendingFromAt.Time.UTC()
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
	if row.PendingFromAt.Valid && !row.PendingFromAt.Time.IsZero() {
		at := row.PendingFromAt.Time.UTC()
		if need.IsZero() || at.Before(need) {
			need = at
		}
	}
	// A bootstrap page can contain only non-text messages, leaving the source
	// cursor empty. A new revision must still visit the remaining old range.
	if progress, ok := storedHistoryProgress(row); ok && (need.IsZero() || progress.After.Before(need)) {
		need = progress.After
	}
	return need
}

// HistoryPage is one forward DWS page, including transport continuation and
// evidence from messages with no textual content.
type HistoryPage struct {
	Events          []HistoryEvent
	EvidenceIDs     []string
	RawCount        int
	Oldest          time.Time
	NextCursor      time.Time
	HasMore         bool
	PaginationKnown bool
	// SelfNames is this digital employee's bound display name (and aliases),
	// even when the current page has no [self] events. Flush uses it to
	// strip leftover self-citations from earlier revisions.
	SelfNames []string
}

func parseDWSEvents(raw []byte) ([]HistoryEvent, error) {
	page, err := parseDWSPage(raw, "", "")
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
	SenderType    string `json:"senderType"`
}

func parseDWSPage(raw []byte, agentUID, agentDisplayName string) (HistoryPage, error) {
	var payload struct {
		Success    bool             `json:"success"`
		ErrorCode  string           `json:"errorCode"`
		ErrorMsg   string           `json:"errorMsg"`
		Messages   []dwsListMessage `json:"messages"`
		Result     json.RawMessage  `json:"result"`
		HasMore    *bool            `json:"hasMore"`
		NextCursor json.RawMessage  `json:"nextCursor"`
	}
	if json.Unmarshal(raw, &payload) != nil {
		return HistoryPage{}, errors.New("decode DWS conversation history response")
	}
	messages := payload.Messages
	if len(payload.Result) > 0 && payload.Result[0] == '{' {
		var nested struct {
			Messages   []dwsListMessage `json:"messages"`
			HasMore    *bool            `json:"hasMore"`
			NextCursor json.RawMessage  `json:"nextCursor"`
		}
		if json.Unmarshal(payload.Result, &nested) == nil {
			if len(messages) == 0 {
				messages = nested.Messages
			}
			if payload.HasMore == nil {
				payload.HasMore = nested.HasMore
				payload.NextCursor = nested.NextCursor
			}
		}
	}
	if !payload.Success && len(messages) == 0 {
		return HistoryPage{}, dwsclient.HistoryRejected(payload.ErrorCode, payload.ErrorMsg)
	}
	page := HistoryPage{
		RawCount:        len(messages),
		PaginationKnown: payload.HasMore != nil,
		SelfNames:       agentNameAliases(agentDisplayName),
	}
	if payload.HasMore != nil {
		page.HasMore = *payload.HasMore
	}
	if len(payload.NextCursor) > 0 {
		var milliseconds json.Number
		if json.Unmarshal(payload.NextCursor, &milliseconds) == nil {
			if value, err := milliseconds.Int64(); err == nil && value > 0 {
				page.NextCursor = time.UnixMilli(value).UTC()
			}
		}
	}
	for _, message := range messages {
		occurred := parseDWSTime(message.CreateTime)
		if page.Oldest.IsZero() || occurred.Before(page.Oldest) {
			page.Oldest = occurred
		}
		evidenceID := strings.TrimSpace(message.OpenMessageID)
		if evidenceID == "" {
			evidenceID = strings.TrimSpace(message.MessageID)
		}
		if evidenceID != "" {
			page.EvidenceIDs = append(page.EvidenceIDs, evidenceID)
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
		self := messageIsSelf(message.IsSelf, message.Self, agentUID, agentDisplayName, message.SenderID, message.SenderOpenID, speaker)
		page.Events = append(page.Events, HistoryEvent{
			EvidenceID: evidenceID,
			OccurredAt: occurred,
			Speaker:    speaker,
			Content:    content,
			Self:       self,
			NonHuman:   !self && senderIsDigitalEmployee(message.SenderType),
		})
	}
	return page, nil
}

func senderIsDigitalEmployee(senderType string) bool {
	switch strings.ToLower(strings.TrimSpace(senderType)) {
	case "bot", "robot", "digital_employee", "digitalemployee", "ai", "assistant":
		return true
	default:
		return false
	}
}

func messageIsSelf(flag *bool, self bool, agentUID, agentDisplayName, senderID, senderOpenID, senderName string) bool {
	if flag != nil {
		return *flag
	}
	if self {
		return true
	}
	agentUID = strings.TrimSpace(agentUID)
	if agentUID != "" && (strings.TrimSpace(senderID) == agentUID || strings.TrimSpace(senderOpenID) == agentUID) {
		return true
	}
	return namesReferToSameAgent(senderName, agentDisplayName)
}

// namesReferToSameAgent treats "菲迪" and "菲迪-FDE教练" as the same digital employee.
func namesReferToSameAgent(speaker, display string) bool {
	speaker = strings.TrimSpace(speaker)
	display = strings.TrimSpace(display)
	if speaker == "" || display == "" {
		return false
	}
	if strings.EqualFold(speaker, display) {
		return true
	}
	for _, alias := range agentNameAliases(display) {
		if strings.EqualFold(speaker, alias) {
			return true
		}
	}
	for _, alias := range agentNameAliases(speaker) {
		if strings.EqualFold(display, alias) {
			return true
		}
	}
	return false
}

func agentNameAliases(name string) []string {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	out := []string{name}
	cut := name
	for _, sep := range []string{"-", "－", "—", "–", "（", "(", " "} {
		if i := strings.Index(cut, sep); i > 0 {
			cut = strings.TrimSpace(cut[:i])
			break
		}
	}
	if cut != "" && !strings.EqualFold(cut, name) {
		out = append(out, cut)
	}
	return out
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
