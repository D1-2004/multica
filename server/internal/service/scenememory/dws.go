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

	"github.com/multica-ai/multica/server/internal/dwsclient"
	"github.com/multica-ai/multica/server/internal/integrations/agentidentityhsf"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type identityLookup interface {
	GetAgentDingTalkIdentity(ctx context.Context, arg db.GetAgentDingTalkIdentityParams) (db.AgentDingtalkIdentity, error)
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
		return nil, fmt.Errorf("resolve DWS identity: %w", err)
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
	lookback := time.Now().UTC().Add(-historyLookback)
	if row.SourceCursorAt.Valid && !row.SourceCursorAt.Time.IsZero() {
		lookback = row.SourceCursorAt.Time.UTC()
	}
	before := time.Now().Add(time.Minute)
	seen := make(map[string]struct{})
	out := make([]HistoryEvent, 0, limit)
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
		pageEvents, err := parseDWSEvents(raw)
		if err != nil {
			return nil, err
		}
		if len(pageEvents) == 0 {
			break
		}
		added := 0
		oldest := before
		for _, event := range pageEvents {
			if event.EvidenceID != "" {
				if _, ok := seen[event.EvidenceID]; ok {
					continue
				}
				seen[event.EvidenceID] = struct{}{}
			}
			out = append(out, event)
			added++
			if event.OccurredAt.Before(oldest) {
				oldest = event.OccurredAt
			}
		}
		if added == 0 || !oldest.After(lookback) || len(pageEvents) < limit {
			break
		}
		before = oldest
	}
	return out, nil
}

func parseDWSEvents(raw []byte) ([]HistoryEvent, error) {
	var payload struct {
		Success   bool   `json:"success"`
		ErrorCode string `json:"errorCode"`
		Result    struct {
			Messages []struct {
				Content       string `json:"content"`
				CreateTime    string `json:"createTime"`
				OpenMessageID string `json:"openMessageId"`
				Sender        string `json:"sender"`
			} `json:"messages"`
		} `json:"result"`
	}
	if json.Unmarshal(raw, &payload) != nil {
		return nil, errors.New("decode DWS conversation history response")
	}
	if !payload.Success {
		return nil, fmt.Errorf("DWS conversation history query rejected: %s", dwsclient.SafeCode(payload.ErrorCode))
	}
	out := make([]HistoryEvent, 0, len(payload.Result.Messages))
	for _, message := range payload.Result.Messages {
		content := strings.TrimSpace(message.Content)
		if content == "" {
			continue
		}
		occurred := parseDWSTime(message.CreateTime)
		speaker := strings.Join(strings.Fields(message.Sender), " ")
		if speaker == "" {
			speaker = "dingtalk"
		}
		out = append(out, HistoryEvent{
			EvidenceID: strings.TrimSpace(message.OpenMessageID),
			OccurredAt: occurred,
			Speaker:    speaker,
			Content:    content,
		})
	}
	return out, nil
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
