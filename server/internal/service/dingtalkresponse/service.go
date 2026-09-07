// Package dingtalkresponse owns durable platform message sends and lifecycle
// receipts. An execution callback is never itself evidence of message delivery.
package dingtalkresponse

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// ActionInput is a frozen dispatch snapshot. Credential material must never be
// included here: this value is persisted before any external operation.
type ActionInput struct {
	ActionID             string `json:"action_id,omitempty"`
	WorkspaceID          string `json:"workspace_id"`
	AgentID              string `json:"agent_id"`
	TaskID               string `json:"task_id,omitempty"`
	IssueID              string `json:"issue_id,omitempty"`
	RequestID            string `json:"request_id"`
	DWSUID               string `json:"dws_uid"`
	DWSOrgID             string `json:"dws_org_id"`
	ConversationID       string `json:"conversation_id"`
	SenderOpenDingTalkID string `json:"sender_open_dingtalk_id"`
	IsGroup              bool   `json:"is_group"`
	ShowAITag            bool   `json:"show_ai_tag"`
	CallbackURL          string `json:"callback_url"`
	CallbackTarget       string `json:"callback_target"`
	Text                 string `json:"text,omitempty"`
	CloseState           string `json:"close_state,omitempty"`
}

type Route struct {
	CallbackURL string
	Input       ActionInput
}

type ReceiptSender interface {
	SendResponseReceipt(context.Context, string, string, protocol.DingTalkResponseReceipt) error
}

// DBTX is implemented by pgx.Tx, pgxpool.Pool and the generated query DBTX.
type DBTX interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type Service struct {
	// OnSandboxDelivered must be configured before Run. Implementations must
	// tolerate repetition after a crash between their commit and our ack.
	OnSandboxDelivered func(context.Context, ActionInput, string, string) error
	pool               *pgxpool.Pool
	provider           Provider
	receipts           ReceiptSender
	wake               chan struct{}
	sandboxWake        chan struct{}
	started            sync.Once
	done               chan struct{}
}

func NewService(pool *pgxpool.Pool, provider Provider, receipts ReceiptSender) *Service {
	return &Service{pool: pool, provider: provider, receipts: receipts, wake: make(chan struct{}, 1), sandboxWake: make(chan struct{}, 1), done: make(chan struct{})}
}

func (s *Service) RegisterRoute(ctx context.Context, tx DBTX, route Route) error {
	if tx == nil {
		return errors.New("response route database is required")
	}
	if err := validateRoute(route); err != nil {
		return err
	}
	raw, err := json.Marshal(route.Input)
	if err != nil {
		return err
	}
	var matches bool
	err = tx.QueryRow(ctx, `INSERT INTO response_route (callback_url,workspace_id,agent_id,input)
		VALUES ($1,$2,$3,$4) ON CONFLICT (callback_url) DO UPDATE SET callback_url=EXCLUDED.callback_url
		RETURNING workspace_id=$2 AND agent_id=$3 AND input=$4::jsonb`,
		route.CallbackURL, route.Input.WorkspaceID, route.Input.AgentID, raw).Scan(&matches)
	if err != nil {
		return fmt.Errorf("persist response route: %w", err)
	}
	if !matches {
		return errors.New("response route conflicts with frozen dispatch")
	}
	return nil
}

func (s *Service) FindRoute(ctx context.Context, callbackURL string) (*Route, error) {
	if s == nil || s.pool == nil {
		return nil, errors.New("response service is not configured")
	}
	var raw []byte
	err := s.pool.QueryRow(ctx, `SELECT input FROM response_route WHERE callback_url=$1`, callbackURL).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	route := &Route{CallbackURL: callbackURL}
	if err := json.Unmarshal(raw, &route.Input); err != nil {
		return nil, fmt.Errorf("decode frozen response route: %w", err)
	}
	return route, nil
}

// Enqueue is transaction-friendly. Call Notify only after the caller commits.
// Reusing an action ID with different contents is rejected, never overwritten.
func (s *Service) Enqueue(ctx context.Context, tx DBTX, in ActionInput) (string, error) {
	if tx == nil {
		return "", errors.New("response action database is required")
	}
	if err := validateInput(in); err != nil {
		return "", err
	}
	kind := "message.send"
	if in.Text == "" {
		kind = "reaction.clear"
	}
	if in.ActionID == "" {
		in.ActionID = StableActionID(in.WorkspaceID, in.AgentID, in.RequestID, kind)
	}
	raw, err := json.Marshal(in)
	if err != nil {
		return "", err
	}
	var matches bool
	err = tx.QueryRow(ctx, `INSERT INTO response_action (id,workspace_id,agent_id,request_id,task_id,issue_id,kind,input)
		VALUES ($1,$2,$3,$4,NULLIF($5,'')::uuid,NULLIF($6,'')::uuid,$7,$8)
		ON CONFLICT(id) DO UPDATE SET id=EXCLUDED.id
		RETURNING workspace_id=$2 AND agent_id=$3 AND input=$8::jsonb`,
		in.ActionID, in.WorkspaceID, in.AgentID, in.RequestID, in.TaskID, in.IssueID, kind, raw).Scan(&matches)
	if err != nil {
		return "", fmt.Errorf("persist response action: %w", err)
	}
	if !matches {
		return "", errors.New("response action conflicts with immutable idempotency key")
	}
	return in.ActionID, nil
}

func StableActionID(workspaceID, agentID, requestID, kind string) string {
	hash := sha256.Sum256([]byte(workspaceID + "\x00" + agentID + "\x00" + requestID + "\x00" + kind))
	return "response-" + hex.EncodeToString(hash[:])
}

func (s *Service) Submit(ctx context.Context, in ActionInput) (string, error) {
	if s == nil || s.pool == nil {
		return "", errors.New("response service is not configured")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	id, err := s.Enqueue(ctx, tx, in)
	if err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	s.Notify()
	return id, nil
}

// Notify is an optimization. Every replica also polls the durable outbox.
func (s *Service) Notify() {
	if s == nil {
		return
	}
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Service) Run(ctx context.Context) {
	if s == nil {
		return
	}
	s.started.Do(func() {
		defer close(s.done)
		if s.pool == nil {
			return
		}
		// Independent workers keep an identity exchange or receipt retry from
		// blocking another conversation's first reply.
		var workers sync.WaitGroup
		for range 4 {
			workers.Add(1)
			go func() { defer workers.Done(); s.runWorker(ctx, false) }()
		}
		for range 2 {
			workers.Add(1)
			go func() { defer workers.Done(); s.runWorker(ctx, true) }()
		}
		workers.Wait()
	})
}

func (s *Service) notifySandbox() {
	select {
	case s.sandboxWake <- struct{}{}:
	default:
	}
}

func (s *Service) runWorker(ctx context.Context, sandbox bool) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	work, wake := s.processOne, s.wake
	if sandbox {
		work, wake = s.processSandboxOne, s.sandboxWake
	}
	for ctx.Err() == nil {
		worked, err := work(ctx)
		if err != nil && ctx.Err() == nil {
			slog.Warn("response action worker failed", "error", err)
		}
		if worked && err == nil {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-wake:
		case <-ticker.C:
		}
	}
}

func (s *Service) WaitWithTimeout(ctx context.Context, timeout time.Duration) bool {
	if s == nil {
		return true
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-s.done:
		return true
	case <-ctx.Done():
		return false
	case <-timer.C:
		return false
	}
}

func validateRoute(route Route) error {
	completion, err := parseCallback(route.CallbackURL, false)
	if err != nil {
		return err
	}
	receipt, err := parseCallback(route.Input.CallbackURL, true)
	if err != nil {
		return err
	}
	if completion.task != receipt.task || (completion.origin != "" && receipt.origin != "" && completion.origin != receipt.origin) {
		return errors.New("response callbacks must target the same Router task and origin")
	}
	return validateScope(route.Input)
}

func validateScope(in ActionInput) error {
	for _, id := range []string{in.WorkspaceID, in.AgentID} {
		if _, err := uuid.Parse(id); err != nil {
			return errors.New("response scope UUID is invalid")
		}
	}
	if in.CallbackTarget == "" {
		return errors.New("response callback target is required")
	}
	return nil
}

func validateInput(in ActionInput) error {
	if _, err := parseCallback(in.CallbackURL, true); err != nil {
		return err
	}
	if err := validateScope(in); err != nil {
		return err
	}
	if strings.TrimSpace(in.RequestID) == "" {
		return errors.New("response request id is required")
	}
	for _, id := range []string{in.TaskID, in.IssueID} {
		if id != "" {
			if _, err := uuid.Parse(id); err != nil {
				return errors.New("response task or issue UUID is invalid")
			}
		}
	}
	if in.Text == "" {
		switch in.CloseState {
		case "silent", "failed", "cancelled", "unknown":
			return nil
		}
		return errors.New("response close state is invalid")
	}
	if strings.TrimSpace(in.Text) == "" || in.CloseState != "" {
		return errors.New("response send content is invalid")
	}
	if in.DWSUID == "" || in.DWSOrgID == "" || in.ConversationID == "" || (!in.IsGroup && in.SenderOpenDingTalkID == "") {
		return errors.New("response send identity or target is incomplete")
	}
	return nil
}

var responseCallbackPattern = regexp.MustCompile(`^/api/v1/dispatch-tasks/([A-Za-z0-9_-]{1,128})/(execution-result|execution-update|response-receipt)$`)

type parsedCallback struct{ task, origin string }

func parseCallback(raw string, isReceipt bool) (parsedCallback, error) {
	parsed, err := url.Parse(raw)
	if err != nil || raw == "" || parsed.Opaque != "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.User != nil || parsed.Fragment != "" || parsed.RawPath != "" || parsed.EscapedPath() != parsed.Path {
		return parsedCallback{}, errors.New("response callback URL is invalid")
	}
	if (parsed.Scheme == "" && parsed.Host != "") || (parsed.Scheme != "" && (parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https"))) {
		return parsedCallback{}, errors.New("response callback origin is invalid")
	}
	match := responseCallbackPattern.FindStringSubmatch(parsed.Path)
	if len(match) != 3 || (match[2] == "response-receipt") != isReceipt {
		return parsedCallback{}, errors.New("response callback path is invalid")
	}
	origin := ""
	if parsed.Host != "" {
		origin = parsed.Scheme + "://" + parsed.Host
	}
	return parsedCallback{task: match[1], origin: origin}, nil
}
