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
	ActionID string `json:"action_id,omitempty"`
	// EmployeeMessageJobID is set by the foreground Host, never an authority claim.
	// BeforeSend verifies it against the exact completed message job and scope.
	EmployeeMessageJobID string `json:"employee_message_job_id,omitempty"`
	WorkspaceID          string `json:"workspace_id"`
	AgentID              string `json:"agent_id"`
	TaskID               string `json:"task_id,omitempty"`
	IssueID              string `json:"issue_id,omitempty"`
	RequestID            string `json:"request_id"`
	DWSUID               string `json:"dws_uid"`
	DWSOrgID             string `json:"dws_org_id"`
	// SceneID is the Agent work scene the response goes to
	// (docs/agent-scene.md); ConversationID is that scene's external
	// conversation id as the scene directory records it.
	SceneID              string `json:"scene_id,omitempty"`
	ConversationID       string `json:"conversation_id"`
	SenderOpenDingTalkID string `json:"sender_open_dingtalk_id"`
	IsGroup              bool   `json:"is_group"`
	ShowAITag            bool   `json:"show_ai_tag"`
	ReplyToOpenMsgID     string `json:"reply_to_open_msg_id,omitempty"`
	CallbackURL          string `json:"callback_url"`
	CallbackTarget       string `json:"callback_target"`
	Text                 string `json:"text,omitempty"`
	CloseState           string `json:"close_state,omitempty"`
	// Set only by EnqueueCoordinatorWait, never by a caller-supplied send flag.
	CoordinatorWaitJobID string `json:"coordinator_wait_job_id,omitempty"`
	// RoutineRunID is set only by EnqueueRoutineNotice: a Host notice of a
	// scene routine run (start or end), sent into the routine's scene with
	// no dispatch to close and no Router callback.
	RoutineRunID string `json:"routine_run_id,omitempty"`
	// SceneNoticeID is set only by EnqueueSceneNotice: a Host notice that a
	// conversation changed its scene's configuration, sent into that scene
	// with no dispatch to close and no Router callback.
	SceneNoticeID string `json:"scene_notice_id,omitempty"`
	// InvitationActionID is set only by EnqueueInvitationNotice: a cross-scene
	// collection invitation whose response action id is the invitation's own
	// delivery action id. A 1:1 invitation to a person without a known
	// conversation leaves ConversationID empty and adopts the conversation
	// the provider reports on delivery.
	InvitationActionID string `json:"invitation_action_id,omitempty"`
	// EmployeeRunNoticeID is Host provenance retained even if workspace teardown
	// removes the notice row while a worker already holds the action payload.
	EmployeeRunNoticeID string `json:"employee_run_notice_id,omitempty"`
	// DWSEnvironment pins the DWS gateway ("production" or "staging") the
	// send goes through. Empty keeps the provider's configured gateway; native
	// subscriptions set "production", where their events come from.
	DWSEnvironment string `json:"dws_environment,omitempty"`
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
	// BeforeSend is an optional Host authority fence immediately before a new
	// provider submission. Configure before Run; ordinary actions may return nil.
	BeforeSend func(context.Context, ActionInput) error
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
	if in.CoordinatorWaitJobID != "" {
		return "", errors.New("coordinator wait progress requires its Host enqueue path")
	}
	return s.enqueue(ctx, tx, in)
}

// EnqueueCoordinatorWait records non-terminal Host progress. An empty callback
// also makes an older worker fail callback validation before HTTP; it cannot
// settle the original dispatch while a rolling deployment is in progress.
func (s *Service) EnqueueCoordinatorWait(ctx context.Context, tx DBTX, in ActionInput, jobID string) (string, error) {
	if _, err := uuid.Parse(jobID); err != nil {
		return "", errors.New("coordinator wait job id is invalid")
	}
	in.CoordinatorWaitJobID = jobID
	in.RequestID = "coordinator-wait:" + jobID
	in.ActionID = ""
	in.TaskID, in.IssueID, in.CallbackURL, in.CloseState = "", "", "", ""
	return s.enqueue(ctx, tx, in)
}

// Routine notice phases (EnqueueRoutineNotice).
const (
	RoutineNoticeStart = "start"
	RoutineNoticeEnd   = "end"
)

// routineNoticeTarget fills CallbackTarget for routine notices, which have
// no Router callback to receipt.
const routineNoticeTarget = "scene-routine"

// RoutineNoticeRequestID is the idempotency key of a routine run's start or
// end notice.
func RoutineNoticeRequestID(runID, phase string) string {
	return "routine:" + runID + ":" + phase
}

// EnqueueRoutineNotice records the start or end notice of a scene routine run
// (docs/context-capabilities.md §9). The request id is derived from the run
// and phase, so a retried dispatch or terminal transition enqueues the same
// action once. There is no dispatch to close and no Router callback: the
// worker sends it and keeps its delivery state without a receipt.
func (s *Service) EnqueueRoutineNotice(ctx context.Context, tx DBTX, in ActionInput, runID, phase string) (string, error) {
	if _, err := uuid.Parse(runID); err != nil {
		return "", errors.New("routine run id is invalid")
	}
	if phase != RoutineNoticeStart && phase != RoutineNoticeEnd {
		return "", errors.New("routine notice phase is invalid")
	}
	in.RoutineRunID = runID
	in.RequestID = RoutineNoticeRequestID(runID, phase)
	in.ActionID = ""
	in.CoordinatorWaitJobID = ""
	// No task or issue id: a notice is not a task's reply, so nothing that
	// reads a task's responses (delivery evidence, close states) sees it.
	in.TaskID, in.IssueID, in.CallbackURL, in.CloseState, in.ReplyToOpenMsgID = "", "", "", "", ""
	in.CallbackTarget = routineNoticeTarget
	return s.enqueue(ctx, tx, in)
}

// EnqueueSceneNotice records a Host notice into a scene (a configuration
// change made from a conversation). noticeID makes it idempotent; like a
// routine notice it closes no dispatch and has no Router callback.
func (s *Service) EnqueueSceneNotice(ctx context.Context, tx DBTX, in ActionInput, noticeID string) (string, error) {
	if _, err := uuid.Parse(noticeID); err != nil {
		return "", errors.New("scene notice id is invalid")
	}
	in.SceneNoticeID = noticeID
	in.RoutineRunID = ""
	in.RequestID = "scene-notice:" + noticeID
	in.ActionID = ""
	in.CoordinatorWaitJobID = ""
	in.TaskID, in.IssueID, in.CallbackURL, in.CloseState, in.ReplyToOpenMsgID = "", "", "", "", ""
	in.CallbackTarget = routineNoticeTarget
	return s.enqueue(ctx, tx, in)
}

// invitationNoticeNamespace derives the scene notice id of an invitation from
// its delivery action id, so older workers treat the action as a notice (no
// Router callback) and a replay maps to the same row.
var invitationNoticeNamespace = uuid.MustParse("3f6d1c2a-6a0e-4c2e-9e57-2f6a1d8b9c41")

// EnqueueInvitationNotice records the send of one collection invitation. The
// response action id is actionID itself (taskinput.DeliveryActionID), so the
// invitation, its history fact and any later reminder name the same frozen
// provider address. It closes no dispatch and has no Router callback.
func (s *Service) EnqueueInvitationNotice(ctx context.Context, tx DBTX, in ActionInput, actionID string) (string, error) {
	actionID = strings.TrimSpace(actionID)
	if actionID == "" || len(actionID) > 128 || strings.ContainsAny(actionID, " \t\r\n") {
		return "", errors.New("invitation action id is invalid")
	}
	in.InvitationActionID = actionID
	in.SceneNoticeID = uuid.NewSHA1(invitationNoticeNamespace, []byte(actionID)).String()
	in.RoutineRunID = ""
	in.RequestID = "invitation:" + actionID
	in.ActionID = actionID
	in.CoordinatorWaitJobID = ""
	in.TaskID, in.IssueID, in.CallbackURL, in.CloseState, in.ReplyToOpenMsgID = "", "", "", "", ""
	in.CallbackTarget = routineNoticeTarget
	return s.enqueue(ctx, tx, in)
}

func (s *Service) enqueue(ctx context.Context, tx DBTX, in ActionInput) (string, error) {
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
	if !validDWSEnvironment(in.DWSEnvironment) {
		return errors.New("response DWS environment is invalid")
	}
	if in.CallbackTarget == "" {
		return errors.New("response callback target is required")
	}
	return nil
}

func validateInput(in ActionInput) error {
	if in.EmployeeRunNoticeID != "" {
		if _, err := uuid.Parse(in.EmployeeRunNoticeID); err != nil {
			return errors.New("employee run notice id is invalid")
		}
	}
	switch {
	case in.CoordinatorWaitJobID != "":
		if _, err := uuid.Parse(in.CoordinatorWaitJobID); err != nil {
			return errors.New("coordinator wait job id is invalid")
		}
		if in.CallbackURL != "" || in.TaskID != "" || in.IssueID != "" || in.CloseState != "" || in.Text == "" {
			return errors.New("coordinator wait cannot close a dispatch or claim a task")
		}
	case in.RoutineRunID != "" || in.SceneNoticeID != "":
		if in.RoutineRunID != "" && in.SceneNoticeID != "" {
			return errors.New("a notice is a routine notice or a scene notice, not both")
		}
		if _, err := uuid.Parse(in.RoutineRunID + in.SceneNoticeID); err != nil {
			return errors.New("notice id is invalid")
		}
		if in.CallbackURL != "" || in.TaskID != "" || in.IssueID != "" || in.CloseState != "" || in.ReplyToOpenMsgID != "" || in.Text == "" {
			return errors.New("routine notice cannot close a dispatch, claim a task or quote a message")
		}
	default:
		if _, err := parseCallback(in.CallbackURL, true); err != nil {
			return err
		}
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
	if in.InvitationActionID != "" && (in.ActionID != in.InvitationActionID || in.SceneNoticeID == "") {
		return errors.New("invitation notice identity is inconsistent")
	}
	// Only an invitation may reach a person by DM before their conversation
	// is known; the provider reports the conversation on delivery.
	pendingConversation := in.InvitationActionID != "" && !in.IsGroup && in.ConversationID == ""
	if in.DWSUID == "" || in.DWSOrgID == "" || (in.ConversationID == "" && !pendingConversation) || (!in.IsGroup && in.SenderOpenDingTalkID == "") {
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

func validDWSEnvironment(environment string) bool {
	return environment == "" || environment == "production" || environment == "staging"
}
