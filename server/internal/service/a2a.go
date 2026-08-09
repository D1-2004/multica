package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	a2aintegration "github.com/multica-ai/multica/server/internal/integrations/a2a"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	a2aTextMIMEType  = "text/plain"
	a2aIDRandomBytes = 24
)

// A2AService is the application boundary for inbound A2A requests. Protocol
// identifiers are deliberately separate from local database UUIDs.
type A2AService struct {
	Queries     *db.Queries
	TxStarter   A2ATxStarter
	TaskService A2ATaskNotifier
}

var _ a2aintegration.Port = (*A2AService)(nil)

// A2ATxStarter keeps the protocol service independent from the concrete pool.
type A2ATxStarter interface {
	Begin(context.Context) (pgx.Tx, error)
}

// A2ATaskNotifier is the post-commit wakeup seam implemented by TaskService.
type A2ATaskNotifier interface {
	NotifyTaskEnqueued(context.Context, db.AgentTaskQueue)
}

func NewA2AService(queries *db.Queries, txStarter A2ATxStarter, taskService A2ATaskNotifier) *A2AService {
	return &A2AService{
		Queries:     queries,
		TxStarter:   txStarter,
		TaskService: taskService,
	}
}

type a2aPrincipalIDs struct {
	WorkspaceID  pgtype.UUID
	AgentID      pgtype.UUID
	EndpointID   pgtype.UUID
	ClientID     pgtype.UUID
	CredentialID pgtype.UUID
}

type validatedA2ASend struct {
	MessageID   string
	Content     string
	Fingerprint string
}

// SendMessage accepts one new, asynchronous text task. Context continuation,
// streaming, files, and synchronous waiting are intentionally outside this
// first inbound slice.
func (s *A2AService) SendMessage(ctx context.Context, request *a2a.SendMessageRequest) (a2a.SendMessageResult, error) {
	if s == nil || s.Queries == nil || s.TxStarter == nil || s.TaskService == nil {
		return nil, a2a.NewError(a2a.ErrInternalError, "A2A service is not configured")
	}

	principal, principalIDs, err := a2aPrincipalFromContext(ctx)
	if err != nil {
		return nil, err
	}
	validated, err := validateA2ASendRequest(request)
	if err != nil {
		return nil, err
	}

	endpoint, err := s.Queries.GetPublishedAgentA2AEndpointByPublicID(ctx, principal.PublicAgentID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, a2a.NewError(a2a.ErrUnauthorized, "A2A endpoint is not available")
		}
		slog.Error("load published A2A endpoint failed", "public_agent_id", principal.PublicAgentID, "error", err)
		return nil, a2a.NewError(a2a.ErrInternalError, "unable to load A2A endpoint")
	}
	if !sameUUID(endpoint.ID, principalIDs.EndpointID) ||
		!sameUUID(endpoint.WorkspaceID, principalIDs.WorkspaceID) ||
		!sameUUID(endpoint.AgentID, principalIDs.AgentID) {
		return nil, a2a.NewError(a2a.ErrUnauthorized, "A2A principal does not match the endpoint")
	}

	if replay, found, err := s.replayA2AMessage(ctx, principalIDs, validated); err != nil {
		return nil, err
	} else if found {
		return replay, nil
	}

	publicContextID, err := newA2APublicID("ctx_")
	if err != nil {
		return nil, a2a.NewError(a2a.ErrInternalError, "unable to allocate A2A context")
	}
	publicTaskID, err := newA2APublicID("tsk_")
	if err != nil {
		return nil, a2a.NewError(a2a.ErrInternalError, "unable to allocate A2A task")
	}
	artifactID, err := newA2APublicID("art_")
	if err != nil {
		return nil, a2a.NewError(a2a.ErrInternalError, "unable to allocate A2A artifact")
	}

	var queuedTask db.AgentTaskQueue
	tx, err := s.TxStarter.Begin(ctx)
	if err != nil {
		slog.Error("begin A2A send transaction failed", "client_id", principal.ClientID, "error", err)
		return nil, a2a.NewError(a2a.ErrInternalError, "unable to accept A2A message")
	}
	defer tx.Rollback(ctx)
	qtx := s.Queries.WithTx(tx)

	// This lock is the SendMessage admission linearization point. It rechecks
	// every mutable authorization edge inside the same transaction that creates
	// the local task, so endpoint/client disable, credential revoke, owner
	// transfer, archive, and runtime removal cannot race a stale preflight read.
	admission, err := qtx.LockAgentA2ASendAdmission(ctx, db.LockAgentA2ASendAdmissionParams{
		AgentID:       principalIDs.AgentID,
		WorkspaceID:   principalIDs.WorkspaceID,
		EndpointID:    principalIDs.EndpointID,
		PublicAgentID: principal.PublicAgentID,
		ClientID:      principalIDs.ClientID,
		CredentialID:  principalIDs.CredentialID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		_ = tx.Rollback(ctx)
		return nil, a2a.NewError(a2a.ErrUnauthorized, "A2A endpoint no longer accepts messages")
	}
	if err != nil {
		return s.finishA2ASendError(ctx, tx, principalIDs, validated, err, "lock admission")
	}

	// Re-check inside the transaction so a committed replay never allocates a
	// second local chat task. The unique key remains the final race arbiter for
	// two transactions that both observe no row here.
	claim, claimErr := qtx.GetA2AMessageClaimForClient(ctx, db.GetA2AMessageClaimForClientParams{
		EndpointID: principalIDs.EndpointID,
		ClientID:   principalIDs.ClientID,
		MessageID:  validated.MessageID,
	})
	if claimErr == nil {
		_ = tx.Rollback(ctx)
		return s.replayA2AClaim(ctx, principalIDs, validated.Fingerprint, claim)
	}
	if !errors.Is(claimErr, pgx.ErrNoRows) {
		_ = tx.Rollback(ctx)
		slog.Error("check A2A message claim failed", "client_id", principal.ClientID, "message_id", validated.MessageID, "error", claimErr)
		return nil, a2a.NewError(a2a.ErrInternalError, "unable to accept A2A message")
	}

	session, err := qtx.CreateChatSession(ctx, db.CreateChatSessionParams{
		WorkspaceID:  admission.WorkspaceID,
		AgentID:      admission.AgentID,
		CreatorID:    admission.DelegatedByUserID,
		Title:        admission.CardName,
		IsAgentIntro: false,
	})
	if err != nil {
		return s.finishA2ASendError(ctx, tx, principalIDs, validated, err, "create chat session")
	}
	a2aContext, err := qtx.CreateA2AContext(ctx, db.CreateA2AContextParams{
		PublicContextID: publicContextID,
		ChatSessionID:   session.ID,
		ClientID:        principalIDs.ClientID,
		EndpointID:      principalIDs.EndpointID,
	})
	if err != nil {
		return s.finishA2ASendError(ctx, tx, principalIDs, validated, err, "create context")
	}
	queuedTask, err = qtx.CreateA2AChatTask(ctx, db.CreateA2AChatTaskParams{
		AgentID:       admission.AgentID,
		RuntimeID:     admission.AgentRuntimeID,
		ChatSessionID: session.ID,
		TaskContext:   newA2ATaskContext(),
	})
	if err != nil {
		return s.finishA2ASendError(ctx, tx, principalIDs, validated, err, "create task")
	}
	queuedTask, err = qtx.SetChatTaskInputOwnerSelf(ctx, queuedTask.ID)
	if err != nil {
		return s.finishA2ASendError(ctx, tx, principalIDs, validated, err, "set task input owner")
	}
	inputMessage, err := qtx.CreateChatMessage(ctx, db.CreateChatMessageParams{
		ChatSessionID: session.ID,
		Role:          "user",
		Content:       validated.Content,
		TaskID:        queuedTask.ID,
	})
	if err != nil {
		return s.finishA2ASendError(ctx, tx, principalIDs, validated, err, "create input message")
	}
	_, err = qtx.CreateA2ATaskBinding(ctx, db.CreateA2ATaskBindingParams{
		PublicTaskID:       publicTaskID,
		MessageID:          validated.MessageID,
		RequestFingerprint: validated.Fingerprint,
		ArtifactID:         artifactID,
		CredentialID:       principalIDs.CredentialID,
		RootLocalTaskID:    queuedTask.ID,
		InputChatMessageID: inputMessage.ID,
		ContextID:          a2aContext.ID,
		EndpointID:         principalIDs.EndpointID,
		ClientID:           principalIDs.ClientID,
	})
	if err != nil {
		return s.finishA2ASendError(ctx, tx, principalIDs, validated, err, "create task binding")
	}

	if err := tx.Commit(ctx); err != nil {
		return s.finishA2ASendError(ctx, tx, principalIDs, validated, err, "commit task")
	}

	// A daemon may only observe the task after every A2A identity row commits.
	s.TaskService.NotifyTaskEnqueued(ctx, queuedTask)
	return s.GetTask(ctx, &a2a.GetTaskRequest{ID: a2a.TaskID(publicTaskID)})
}

// GetTask returns only stable A2A identifiers and protocol-safe output. Local
// task, workspace, session, runtime, and filesystem identifiers never enter
// the projection.
func (s *A2AService) GetTask(ctx context.Context, request *a2a.GetTaskRequest) (*a2a.Task, error) {
	if s == nil || s.Queries == nil {
		return nil, a2a.NewError(a2a.ErrInternalError, "A2A service is not configured")
	}
	_, principalIDs, err := a2aPrincipalFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if request == nil || strings.TrimSpace(string(request.ID)) == "" {
		return nil, a2a.NewError(a2a.ErrInvalidParams, "task id is required")
	}
	if request.HistoryLength != nil && *request.HistoryLength < 0 {
		return nil, a2a.NewError(a2a.ErrInvalidParams, "history length must not be negative")
	}

	projection, err := s.Queries.GetA2ATaskProjectionForClient(ctx, db.GetA2ATaskProjectionForClientParams{
		EndpointID:   principalIDs.EndpointID,
		ClientID:     principalIDs.ClientID,
		PublicTaskID: string(request.ID),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, a2a.ErrTaskNotFound
		}
		slog.Error("load A2A task projection failed", "public_task_id", request.ID, "error", err)
		return nil, a2a.NewError(a2a.ErrInternalError, "unable to load A2A task")
	}
	return projectA2ATask(projection)
}

func (s *A2AService) replayA2AMessage(ctx context.Context, principal a2aPrincipalIDs, request validatedA2ASend) (*a2a.Task, bool, error) {
	claim, err := s.Queries.GetA2AMessageClaimForClient(ctx, db.GetA2AMessageClaimForClientParams{
		EndpointID: principal.EndpointID,
		ClientID:   principal.ClientID,
		MessageID:  request.MessageID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		slog.Error("load A2A replay claim failed", "message_id", request.MessageID, "error", err)
		return nil, false, a2a.NewError(a2a.ErrInternalError, "unable to check A2A message replay")
	}
	task, err := s.replayA2AClaim(ctx, principal, request.Fingerprint, claim)
	return task, true, err
}

func (s *A2AService) replayA2AClaim(ctx context.Context, principal a2aPrincipalIDs, fingerprint string, claim db.A2aTaskBinding) (*a2a.Task, error) {
	if claim.RequestFingerprint != fingerprint {
		return nil, a2a.NewError(a2a.ErrInvalidParams, "message id conflicts with a different A2A request")
	}
	projection, err := s.Queries.GetA2ATaskProjectionForClient(ctx, db.GetA2ATaskProjectionForClientParams{
		EndpointID:   principal.EndpointID,
		ClientID:     principal.ClientID,
		PublicTaskID: claim.PublicTaskID,
	})
	if err != nil {
		slog.Error("load replayed A2A task failed", "public_task_id", claim.PublicTaskID, "error", err)
		return nil, a2a.NewError(a2a.ErrInternalError, "unable to load replayed A2A task")
	}
	return projectA2ATask(projection)
}

func (s *A2AService) finishA2ASendError(
	ctx context.Context,
	tx pgx.Tx,
	principal a2aPrincipalIDs,
	request validatedA2ASend,
	cause error,
	stage string,
) (a2a.SendMessageResult, error) {
	_ = tx.Rollback(ctx)
	if isA2AUniqueViolation(cause) {
		claim, err := s.Queries.GetA2AMessageClaimForClient(ctx, db.GetA2AMessageClaimForClientParams{
			EndpointID: principal.EndpointID,
			ClientID:   principal.ClientID,
			MessageID:  request.MessageID,
		})
		if err == nil {
			if claim.RequestFingerprint != request.Fingerprint {
				return nil, a2a.NewError(a2a.ErrInvalidParams, "message id conflicts with a different A2A request")
			}
			return s.replayA2AClaim(ctx, principal, request.Fingerprint, claim)
		}
	}
	slog.Error("accept A2A message failed", "stage", stage, "message_id", request.MessageID, "error", cause)
	return nil, a2a.NewError(a2a.ErrInternalError, "unable to accept A2A message")
}

func validateA2ASendRequest(request *a2a.SendMessageRequest) (validatedA2ASend, error) {
	if request == nil || request.Message == nil {
		return validatedA2ASend{}, a2a.NewError(a2a.ErrInvalidParams, "message is required")
	}
	if request.Config == nil || !request.Config.ReturnImmediately {
		return validatedA2ASend{}, a2a.NewError(a2a.ErrUnsupportedOperation, "only returnImmediately=true is supported")
	}
	if request.Config.PushConfig != nil {
		return validatedA2ASend{}, a2a.ErrPushNotificationNotSupported
	}
	if len(request.Config.AcceptedOutputModes) > 0 && !containsString(request.Config.AcceptedOutputModes, a2aTextMIMEType) {
		return validatedA2ASend{}, a2a.ErrUnsupportedContentType
	}

	message := request.Message
	if message.Role != a2a.MessageRoleUser {
		return validatedA2ASend{}, a2a.NewError(a2a.ErrInvalidParams, "message role must be user")
	}
	messageID := message.ID
	if strings.TrimSpace(messageID) == "" || strings.TrimSpace(messageID) != messageID || utf8.RuneCountInString(messageID) > 256 {
		return validatedA2ASend{}, a2a.NewError(a2a.ErrInvalidParams, "message id must be 1 to 256 non-whitespace characters")
	}
	if message.ContextID != "" || message.TaskID != "" || len(message.ReferenceTasks) > 0 {
		return validatedA2ASend{}, a2a.NewError(a2a.ErrUnsupportedOperation, "context and task continuation are not supported")
	}
	if len(message.Parts) == 0 {
		return validatedA2ASend{}, a2a.ErrUnsupportedContentType
	}

	texts := make([]string, 0, len(message.Parts))
	for _, part := range message.Parts {
		if part == nil {
			return validatedA2ASend{}, a2a.ErrUnsupportedContentType
		}
		text, ok := part.Content.(a2a.Text)
		if !ok || (part.MediaType != "" && part.MediaType != a2aTextMIMEType) || part.Filename != "" {
			return validatedA2ASend{}, a2a.ErrUnsupportedContentType
		}
		texts = append(texts, string(text))
	}
	content := strings.Join(texts, "\n")
	if strings.TrimSpace(content) == "" {
		return validatedA2ASend{}, a2a.NewError(a2a.ErrInvalidParams, "message text must not be empty")
	}
	fingerprint, err := fingerprintA2ASendRequest(request)
	if err != nil {
		return validatedA2ASend{}, a2a.NewError(a2a.ErrInvalidParams, "message cannot be fingerprinted")
	}
	return validatedA2ASend{MessageID: messageID, Content: content, Fingerprint: fingerprint}, nil
}

func fingerprintA2ASendRequest(request *a2a.SendMessageRequest) (string, error) {
	encoded, err := json.Marshal(request)
	if err != nil {
		return "", fmt.Errorf("encode A2A send request: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func projectA2ATask(row db.GetA2ATaskProjectionForClientRow) (*a2a.Task, error) {
	state, err := projectA2ATaskState(row.TaskStatus, row.CancelRequestedAt.Valid)
	if err != nil {
		return nil, a2a.NewError(a2a.ErrInternalError, "A2A task has an invalid state")
	}
	task := &a2a.Task{
		ID:        a2a.TaskID(row.PublicTaskID),
		ContextID: row.PublicContextID,
		Status: a2a.TaskStatus{
			State:     state,
			Timestamp: a2aProjectionTimestamp(row, state),
		},
	}
	if state != a2a.TaskStateCompleted {
		return task, nil
	}

	task.Artifacts = []*a2a.Artifact{
		{
			ID:   a2a.ArtifactID(row.ArtifactID),
			Name: "result",
			// The task queue result is daemon-authored execution metadata and can
			// contain local task IDs, workdirs, or unredacted model output. The
			// assistant chat outcome is the existing redaction boundary written by
			// TaskService, so it is the only safe source for the public Artifact.
			Parts: a2a.ContentParts{a2a.NewTextPart(row.AssistantResultText)},
		},
	}
	return task, nil
}

func projectA2ATaskState(localStatus string, cancelRequested bool) (a2a.TaskState, error) {
	if cancelRequested {
		return a2a.TaskStateCanceled, nil
	}
	switch localStatus {
	case "queued", "deferred":
		return a2a.TaskStateSubmitted, nil
	case "dispatched", "running", "waiting_local_directory":
		return a2a.TaskStateWorking, nil
	case "completed":
		return a2a.TaskStateCompleted, nil
	case "failed":
		return a2a.TaskStateFailed, nil
	case "cancelled":
		return a2a.TaskStateCanceled, nil
	default:
		return a2a.TaskStateUnspecified, fmt.Errorf("unknown local task status %q", localStatus)
	}
}

func a2aProjectionTimestamp(row db.GetA2ATaskProjectionForClientRow, state a2a.TaskState) *time.Time {
	candidates := []pgtype.Timestamptz{row.TaskCreatedAt, row.BindingCreatedAt}
	switch state {
	case a2a.TaskStateWorking:
		candidates = []pgtype.Timestamptz{row.TaskStartedAt, row.TaskDispatchedAt, row.TaskCreatedAt, row.BindingUpdatedAt}
	case a2a.TaskStateCompleted, a2a.TaskStateFailed, a2a.TaskStateCanceled:
		candidates = []pgtype.Timestamptz{row.TaskCompletedAt, row.BindingUpdatedAt, row.TaskStartedAt, row.TaskCreatedAt}
	}
	for _, candidate := range candidates {
		if candidate.Valid {
			timestamp := candidate.Time
			return &timestamp
		}
	}
	return nil
}

func a2aPrincipalFromContext(ctx context.Context) (a2aintegration.Principal, a2aPrincipalIDs, error) {
	principal, ok := a2aintegration.PrincipalFromContext(ctx)
	if !ok {
		return a2aintegration.Principal{}, a2aPrincipalIDs{}, a2a.ErrUnauthenticated
	}
	parse := func(raw string) (pgtype.UUID, error) {
		var id pgtype.UUID
		if err := id.Scan(strings.TrimSpace(raw)); err != nil || !id.Valid {
			return pgtype.UUID{}, a2a.ErrUnauthenticated
		}
		return id, nil
	}
	workspaceID, err := parse(principal.WorkspaceID)
	if err != nil {
		return a2aintegration.Principal{}, a2aPrincipalIDs{}, err
	}
	agentID, err := parse(principal.AgentID)
	if err != nil {
		return a2aintegration.Principal{}, a2aPrincipalIDs{}, err
	}
	endpointID, err := parse(principal.EndpointID)
	if err != nil {
		return a2aintegration.Principal{}, a2aPrincipalIDs{}, err
	}
	clientID, err := parse(principal.ClientID)
	if err != nil {
		return a2aintegration.Principal{}, a2aPrincipalIDs{}, err
	}
	credentialID, err := parse(principal.CredentialID)
	if err != nil || strings.TrimSpace(principal.PublicAgentID) == "" {
		return a2aintegration.Principal{}, a2aPrincipalIDs{}, a2a.ErrUnauthenticated
	}
	return principal, a2aPrincipalIDs{
		WorkspaceID:  workspaceID,
		AgentID:      agentID,
		EndpointID:   endpointID,
		ClientID:     clientID,
		CredentialID: credentialID,
	}, nil
}

func newA2APublicID(prefix string) (string, error) {
	random := make([]byte, a2aIDRandomBytes)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("read random A2A id: %w", err)
	}
	return prefix + base64.RawURLEncoding.EncodeToString(random), nil
}

func sameUUID(left, right pgtype.UUID) bool {
	return left.Valid && right.Valid && left.Bytes == right.Bytes
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func isA2AUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
