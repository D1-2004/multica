package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/chattrace"
	"github.com/multica-ai/multica/server/internal/dshhost"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// DSHNativePromptReceipt is a durable task admission, not Host execution completion.
type DSHNativePromptReceipt struct {
	SessionID     string      `json:"session_id"`
	RequestID     uuid.UUID   `json:"request_id"`
	ChatSessionID pgtype.UUID `json:"chat_session_id"`
	TaskID        pgtype.UUID `json:"task_id"`
	MessageID     pgtype.UUID `json:"message_id"`
	Queued        bool        `json:"queued"`
	Replayed      bool        `json:"replayed"`
}
type DSHNativePromptSubmit func(context.Context, dshhost.NativeAccess, DSHNativeChatInput, string) (DSHNativePromptReceipt, error)

// DSHNativeChatInput carries the exact native identity. The gateway must create
// the Session in this canonical directory before asking to admit its prompt.
// Model, tools and credentials are resolved by the platform task dispatcher.
type DSHNativeChatInput struct {
	SessionID string
	RequestID uuid.UUID
	Workdir   string
	Prompt    *protocol.DSHNativePrompt
}

var ErrDSHNativeInput = errors.New("invalid DSH native input")
var ErrDSHNativeSteerBusy = errors.New("native steering requires active task input admission")

// DSHNativeInvokeCheck must apply the normal human invocation policy using the
// supplied queries, including when they belong to the admission transaction.
// Opening a management grant is not permission to invoke a private employee.
type DSHNativeInvokeCheck func(context.Context, *db.Queries, db.Agent, pgtype.UUID) error

type dshNativeChatAdmission struct {
	access    dshhost.NativeAccess
	input     DSHNativeChatInput
	invoke    DSHNativeInvokeCheck
	runtimeID pgtype.UUID
}

func (a dshNativeChatAdmission) validateEmployee(agent db.Agent, userID pgtype.UUID) error {
	if a.invoke == nil || a.access.ID == uuid.Nil || a.access.Kind != "session" ||
		a.access.Generation < 1 || a.access.SandboxID == "" ||
		a.access.WorkspaceID == uuid.Nil || a.access.AgentID == uuid.Nil || a.access.UserID == uuid.Nil ||
		!userID.Valid || uuid.UUID(userID.Bytes) != a.access.UserID ||
		!agent.WorkspaceID.Valid || uuid.UUID(agent.WorkspaceID.Bytes) != a.access.WorkspaceID ||
		!agent.ID.Valid || uuid.UUID(agent.ID.Bytes) != a.access.AgentID || agent.ArchivedAt.Valid ||
		agent.RuntimeMode != "cloud" || !agent.RuntimeID.Valid || agent.RuntimeID != a.runtimeID {
		return dshhost.ErrNativeAccessDenied
	}
	return nil
}

func (a dshNativeChatAdmission) validateIdentity(session db.ChatSession, agent db.Agent, userID pgtype.UUID) error {
	if err := a.validateEmployee(agent, userID); err != nil {
		return err
	}
	if !session.ID.Valid || session.ID.Bytes == [16]byte{} ||
		session.CreatorID != userID || session.AgentID != agent.ID || session.WorkspaceID != agent.WorkspaceID ||
		(session.RuntimeID.Valid && session.RuntimeID != agent.RuntimeID) {
		return dshhost.ErrNativeAccessDenied
	}
	return nil
}

func validateDSHNativeSession(sessionID, workdir string) error {
	if !dshhost.ValidSessionID(sessionID) || workdir != dshhost.MountPath+"/workspaces/"+sessionID {
		return ErrDSHNativeInput
	}
	return nil
}

// Validate rejects malformed or ambiguous native input before any registration.
func (input DSHNativeChatInput) Validate(content string) error {
	if err := validateDSHNativeSession(input.SessionID, input.Workdir); err != nil {
		return err
	}
	if input.Prompt != nil {
		if input.Prompt.Validate() != nil || input.Prompt.SessionID != input.SessionID || input.Prompt.RequestID != input.RequestID.String() || content != input.Prompt.DisplayText() {
			return ErrDSHNativeInput
		}
		return nil
	}
	if input.RequestID == uuid.Nil ||
		!utf8.ValidString(content) || strings.ContainsRune(content, 0) || strings.TrimSpace(content) == "" || len(content) > 256*1024 {
		return ErrDSHNativeInput
	}
	return nil
}

// SendDSHNativeChatMessage atomically persists a human prompt, its platform
// task, and the native request binding. The session must already be registered
// to this human. The caller obtains access through NativeAccessManager; this
// method rechecks the durable grant and Host inside the write transaction.
func (s *TaskService) SendDSHNativeChatMessage(ctx context.Context, session db.ChatSession, agent db.Agent, access dshhost.NativeAccess, input DSHNativeChatInput, content string, trace chattrace.Trace, invoke DSHNativeInvokeCheck) (*DirectChatSendResult, error) {
	if s == nil || s.Queries == nil || s.TxStarter == nil {
		return nil, errors.New("DSH native chat requires a task transaction")
	}
	if input.Prompt != nil {
		copy, err := input.Prompt.Clone()
		if err != nil {
			return nil, ErrDSHNativeInput
		}
		input.Prompt = copy
	}
	userID := pgtype.UUID{Bytes: access.UserID, Valid: true}
	admission := &dshNativeChatAdmission{access: access, input: input, invoke: invoke, runtimeID: agent.RuntimeID}
	if err := admission.validateIdentity(session, agent, userID); err != nil {
		return nil, err
	}
	if err := input.Validate(content); err != nil {
		return nil, err
	}
	// Avoid resolving any connected-app overlay for a disallowed caller.
	if err := invoke(ctx, s.Queries, agent, userID); err != nil {
		return nil, dshhost.ErrNativeAccessDenied
	}
	return s.sendDirectChatMessage(ctx, session, agent, userID, content, nil, "user", userID, nil, trace, admission)
}

func (a dshNativeChatAdmission) lockAdmission(ctx context.Context, tx pgx.Tx) error {
	if tx == nil {
		return errors.New("DSH native chat requires a task transaction")
	}
	// Match Host startup and template cutover: Runtime, then employee, before
	// domain row locks. Transaction-scoped locks release on every error path.
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock_shared($1,$2)", fcE2BRuntimeLockClass, fcE2BRuntimeLockKey(a.runtimeID)); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1,$2)", dshEmployeeLockClass,
		dshEmployeeLockKey(pgtype.UUID{Bytes: a.access.WorkspaceID, Valid: true}, pgtype.UUID{Bytes: a.access.AgentID, Valid: true}))
	return err
}

func (a dshNativeChatAdmission) checkLocked(ctx context.Context, tx pgx.Tx, q *db.Queries, session db.ChatSession, agent db.Agent, userID pgtype.UUID) error {
	if tx == nil {
		return errors.New("DSH native chat requires a task transaction")
	}
	// Revalidate rows read after the session and employee locks, not the caller's snapshot.
	if err := a.validateIdentity(session, agent, userID); err != nil {
		return err
	}
	return a.checkGrantLocked(ctx, tx, q, agent, userID)
}

func (a dshNativeChatAdmission) checkGrantLocked(ctx context.Context, tx pgx.Tx, q *db.Queries, agent db.Agent, userID pgtype.UUID) error {
	if err := a.validateEmployee(agent, userID); err != nil {
		return err
	}
	var id pgtype.UUID
	if err := tx.QueryRow(ctx, `SELECT id FROM member WHERE workspace_id=$1 AND user_id=$2 FOR SHARE`, agent.WorkspaceID, userID).Scan(&id); err != nil {
		return dshhost.ErrNativeAccessDenied
	}
	// Hold the exact Host and grant until commit. Retirement, revocation and
	// membership deletion cannot pass an admission that already holds these locks.
	if err := tx.QueryRow(ctx, `SELECT h.agent_id FROM dsh_employee_host h
 JOIN dsh_native_access g ON g.workspace_id=h.workspace_id AND g.agent_id=h.agent_id
 WHERE h.workspace_id=$1 AND h.agent_id=$2 AND h.generation=$3 AND h.sandbox_id=$4 AND h.state='running'
 AND g.id=$5 AND g.user_id=$6 AND g.kind='session' AND g.expires_at>clock_timestamp()
 AND g.generation=h.generation AND g.sandbox_id=h.sandbox_id FOR SHARE OF h,g`,
		a.access.WorkspaceID, a.access.AgentID, a.access.Generation, a.access.SandboxID, a.access.ID, userID).Scan(&id); err != nil {
		return dshhost.ErrNativeAccessDenied
	}
	if err := tx.QueryRow(ctx, `SELECT id FROM agent_runtime WHERE id=$1 AND workspace_id=$2 FOR SHARE`, agent.RuntimeID, agent.WorkspaceID).Scan(&id); err != nil {
		return dshhost.ErrNativeAccessDenied
	}
	runtime, err := q.GetAgentRuntime(ctx, agent.RuntimeID)
	if err != nil || runtime.Provider != "dsh" || !IsFCE2BRuntime(runtime) {
		return dshhost.ErrNativeAccessDenied
	}
	if err := a.invoke(ctx, q, agent, userID); err != nil {
		return dshhost.ErrNativeAccessDenied
	}
	return nil
}

func (a dshNativeChatAdmission) replay(ctx context.Context, tx pgx.Tx, q *db.Queries, session db.ChatSession, agent db.Agent, userID pgtype.UUID, content string) (*DirectChatSendResult, error) {
	var taskID pgtype.UUID
	var sessionID string
	err := tx.QueryRow(ctx, `SELECT task_id,session_id FROM dsh_task_binding
 WHERE workspace_id=$1 AND agent_id=$2 AND request_id=$3`, a.access.WorkspaceID, a.access.AgentID, a.input.RequestID).Scan(&taskID, &sessionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if sessionID != a.input.SessionID {
		return nil, dshhost.ErrChanged
	}
	task, err := q.GetAgentTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if task.AgentID != session.AgentID || task.ChatSessionID != session.ID || task.InitiatorUserID != userID || task.RuntimeID != agent.RuntimeID || task.ChatInputTaskID != task.ID {
		return nil, dshhost.ErrChanged
	}
	var messageID pgtype.UUID
	var count int64
	err = tx.QueryRow(ctx, `SELECT id,count(*) OVER () FROM chat_message
 WHERE chat_session_id=$1 AND task_id=$2 AND role='user' AND message_kind='message'`, session.ID, task.ID).Scan(&messageID, &count)
	if err != nil || count != 1 {
		return nil, dshhost.ErrChanged
	}
	message, err := q.GetChatMessage(ctx, messageID)
	if err != nil {
		return nil, err
	}
	if message.Content != content || !sameDSHNativeSource(message.SourcePayload, a.input.Prompt) {
		return nil, dshhost.ErrChanged
	}
	var queued bool
	if task.Status == "queued" || task.Status == "deferred" {
		err = tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM agent_task_queue
 WHERE chat_session_id=$1 AND id<>$2 AND (created_at,id)<($3,$2)
 AND status IN ('queued','dispatched','running','waiting_local_directory','deferred')
 AND regenerate_quick_actions_for IS NULL)`, session.ID, task.ID, task.CreatedAt).Scan(&queued)
		if err != nil {
			return nil, err
		}
	}
	// An existing result is never re-published or launched, even if the original
	// response was lost. Durable queue recovery owns any unclaimed task.
	return &DirectChatSendResult{Task: task, Message: message, Replayed: true, Queued: queued}, nil
}

func (a dshNativeChatAdmission) bind(ctx context.Context, tx pgx.Tx, session db.ChatSession, task db.AgentTaskQueue) error {
	_, err := (dshhost.PostgresStore{DB: tx}).AdoptNativeExecution(ctx,
		dshhost.SessionScope{Key: a.access.Key, Kind: "chat", ID: uuid.UUID(session.ID.Bytes)},
		uuid.UUID(task.ID.Bytes), a.input.SessionID, a.input.RequestID)
	return err
}

// The complete native input shares the immutable user-message transaction.
// Transcript text is only a display summary, never reconstructed for execution.
func dshNativeSource(prompt *protocol.DSHNativePrompt) ([]byte, error) {
	if prompt == nil {
		return nil, nil
	}
	if err := prompt.Validate(); err != nil {
		return nil, ErrDSHNativeInput
	}
	return json.Marshal(map[string]any{"dsh_native_prompt": prompt})
}
func sameDSHNativeSource(raw []byte, prompt *protocol.DSHNativePrompt) bool {
	var stored map[string]json.RawMessage
	if len(raw) > 0 && json.Unmarshal(raw, &stored) != nil {
		return false
	}
	value, found := stored["dsh_native_prompt"]
	if prompt == nil {
		return !found
	}
	if !found {
		return false
	}
	parsed, err := protocol.DecodeDSHNativePrompt(value)
	if err != nil {
		return false
	}
	left, leftErr := json.Marshal(parsed)
	right, rightErr := json.Marshal(prompt)
	return leftErr == nil && rightErr == nil && bytes.Equal(left, right)
}
