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
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	a2aintegration "github.com/multica-ai/multica/server/internal/integrations/a2a"
	"github.com/multica-ai/multica/server/internal/storage"
	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/internal/util/secretbox"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	a2aTextMIMEType         = "text/plain"
	a2aIDRandomBytes        = 24
	a2aBlockingPollInterval = 250 * time.Millisecond
)

// A2AService is the application boundary for inbound A2A requests. Protocol
// identifiers are deliberately separate from local database UUIDs.
type A2AService struct {
	Queries      *db.Queries
	TxStarter    A2ATxStarter
	TaskService  A2ATaskController
	Storage      storage.Storage
	HTTPClient   *http.Client
	PushSecrets  *secretbox.Box
	PushNotifier A2APushNotifier
}

var _ a2aintegration.Port = (*A2AService)(nil)

// A2ATxStarter keeps the protocol service independent from the concrete pool.
type A2ATxStarter interface {
	Begin(context.Context) (pgx.Tx, error)
}

// A2ATaskNotifier is the post-commit wakeup seam implemented by TaskService.
type A2ATaskNotifier interface {
	NotifyA2ATaskEnqueued(context.Context, db.AgentTaskQueue)
}

// A2ATaskController is the narrow task lifecycle surface needed by public A2A
// task management. It does not expose the ordinary Multica task API.
type A2ATaskController interface {
	A2ATaskNotifier
	CancelTask(context.Context, pgtype.UUID) (*db.AgentTaskQueue, error)
}

// A2APushNotifier wakes the durable push worker after an event is committed.
type A2APushNotifier interface {
	NotifyA2APush()
}

func NewA2AService(queries *db.Queries, txStarter A2ATxStarter, taskService A2ATaskController, store storage.Storage) *A2AService {
	return &A2AService{
		Queries:     queries,
		TxStarter:   txStarter,
		TaskService: taskService,
		Storage:     store,
		HTTPClient:  newA2AOutboundHTTPClient(30 * time.Second),
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
	MessageID           string
	Fingerprint         string
	ReturnImmediately   bool
	ContextID           string
	TaskID              string
	Parts               a2a.ContentParts
	MessageExtensions   []string
	Metadata            map[string]any
	RequestMetadata     map[string]any
	ReferenceTaskIDs    []string
	AcceptedOutputModes []string
	HistoryLength       int
	PushConfig          *a2a.PushConfig
	Identity            a2aintegration.InvocationIdentity
	IdentityExpired     bool
}

// SendMessage appends one durable turn. A missing task id creates a public
// task; a non-terminal task id queues another local execution in the same Chat
// Session. PostgreSQL serializes concurrent turns and is the sole source of
// public state, history, artifacts, and event order.
func (s *A2AService) SendMessage(ctx context.Context, request *a2a.SendMessageRequest) (a2a.SendMessageResult, error) {
	if s == nil || s.Queries == nil || s.TxStarter == nil || s.TaskService == nil {
		return nil, a2a.NewError(a2a.ErrInternalError, "A2A service is not configured")
	}

	principal, principalIDs, err := a2aPrincipalFromContext(ctx)
	if err != nil {
		return nil, err
	}
	validated, err := validateA2ASendRequest(ctx, request, time.Now())
	if err != nil {
		return nil, err
	}
	ctx, requestBoundClaim := ensureA2ARequestBoundTurnClaim(ctx, validated)
	if requestBoundClaim != nil && !validated.ReturnImmediately {
		defer s.releaseA2ARequestBoundTurn(requestBoundClaim)
	}

	endpoint, err := s.Queries.GetPublishedAgentA2AEndpointByPublicID(ctx, db.GetPublishedAgentA2AEndpointByPublicIDParams{
		PublicAgentID:         principal.PublicAgentID,
		AllowDisabledEndpoint: principal.AllowDisabledEndpoint,
	})
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
		return s.completeA2ASend(ctx, validated, replay)
	}

	ingestID, err := newA2APublicID("ing_")
	if err != nil {
		return nil, a2a.NewError(a2a.ErrInternalError, "unable to allocate A2A input storage")
	}
	// Every contender for the same messageId receives a distinct staging
	// prefix. A transaction that loses the idempotency race can then remove only
	// its own objects without deleting the committed winner's input.
	objectPrefix := "a2a/inputs/" + uuidStringOrOpaque(ingestID)
	materializedParts, err := materializeA2AParts(ctx, s.Storage, s.HTTPClient, objectPrefix, validated.Parts)
	if err != nil {
		return nil, err
	}
	committed := false
	defer func() {
		if !committed {
			deleteMaterializedA2AParts(s.Storage, materializedParts)
		}
	}()
	storedParts, err := encodeStoredA2AParts(materializedParts)
	if err != nil {
		return nil, a2a.NewError(a2a.ErrInternalError, "unable to encode A2A parts")
	}
	messageMetadata, err := json.Marshal(validated.Metadata)
	if err != nil {
		return nil, a2a.NewError(a2a.ErrInvalidParams, "message metadata is not valid JSON")
	}
	content := renderA2AInputForAgent(materializedParts)

	var queuedTask db.AgentTaskQueue
	var publicTaskID string
	var publicContextID string
	var binding db.A2aTaskBinding
	var a2aContext db.A2aContext
	tx, err := s.TxStarter.Begin(ctx)
	if err != nil {
		slog.Error("begin A2A send transaction failed", "client_id", principal.ClientID, "error", err)
		return nil, a2a.NewError(a2a.ErrInternalError, "unable to accept A2A message")
	}
	defer tx.Rollback(ctx)
	qtx := s.Queries.WithTx(tx)

	// This lock is the SendMessage admission linearization point. It rechecks
	// every mutable authorization edge inside the same transaction that creates
	// the local task, so client disable, credential revoke, owner transfer,
	// archive, and runtime removal cannot race a stale preflight read. A2A also
	// requires endpoint publication; authenticated MCP links intentionally do not.
	admission, err := qtx.LockAgentA2ASendAdmission(ctx, db.LockAgentA2ASendAdmissionParams{
		AgentID:               principalIDs.AgentID,
		WorkspaceID:           principalIDs.WorkspaceID,
		EndpointID:            principalIDs.EndpointID,
		PublicAgentID:         principal.PublicAgentID,
		ClientID:              principalIDs.ClientID,
		CredentialID:          principalIDs.CredentialID,
		AllowDisabledEndpoint: principal.AllowDisabledEndpoint,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		_ = tx.Rollback(ctx)
		return nil, a2a.NewError(a2a.ErrUnauthorized, "A2A endpoint no longer accepts messages")
	}
	if err != nil {
		return s.finishA2ASendError(ctx, tx, principalIDs, validated, err, "lock admission")
	}

	isNewTask := validated.TaskID == ""
	limits, err := qtx.CheckA2AClientSendLimits(ctx, db.CheckA2AClientSendLimitsParams{
		ClientID:   principalIDs.ClientID,
		EndpointID: principalIDs.EndpointID,
		IsNewTask:  isNewTask,
	})
	if err != nil {
		return s.finishA2ASendError(ctx, tx, principalIDs, validated, err, "check client limits")
	}
	if !limits.RateAllowed.Valid || !limits.RateAllowed.Bool {
		_ = tx.Rollback(ctx)
		return nil, a2a.NewError(a2a.ErrServerError, "A2A client rate limit exceeded")
	}
	if !limits.ConcurrencyAllowed.Valid || !limits.ConcurrencyAllowed.Bool {
		_ = tx.Rollback(ctx)
		return nil, a2a.NewError(a2a.ErrServerError, "A2A client concurrent task limit exceeded")
	}

	// Re-check inside the transaction so a committed replay never allocates a
	// second local chat task. The unique key remains the final race arbiter for
	// two transactions that both observe no row here.
	claim, claimErr := qtx.GetA2AMessageTurnClaimForClient(ctx, db.GetA2AMessageTurnClaimForClientParams{
		EndpointID: principalIDs.EndpointID,
		ClientID:   principalIDs.ClientID,
		MessageID:  validated.MessageID,
	})
	if claimErr == nil {
		_ = tx.Rollback(ctx)
		if claim.RequestFingerprint != validated.Fingerprint {
			return nil, a2a.NewError(a2a.ErrInvalidParams, "message id conflicts with a different A2A request")
		}
		if requestBoundClaim != nil {
			requestBoundClaim.localTaskID = claim.LocalTaskID
			s.maintainA2ARequestBoundTurn(ctx)
		}
		task, replayErr := s.GetTask(ctx, &a2a.GetTaskRequest{ID: a2a.TaskID(claim.PublicTaskID), HistoryLength: a2aSendHistoryLength(request)})
		if replayErr != nil {
			return nil, replayErr
		}
		return s.completeA2ASend(ctx, validated, task)
	}
	if !errors.Is(claimErr, pgx.ErrNoRows) {
		_ = tx.Rollback(ctx)
		slog.Error("check A2A message claim failed", "client_id", principal.ClientID, "message_id", validated.MessageID, "error", claimErr)
		return nil, a2a.NewError(a2a.ErrInternalError, "unable to accept A2A message")
	}

	if validated.TaskID != "" {
		binding, err = qtx.LockA2ATaskBindingForClient(ctx, db.LockA2ATaskBindingForClientParams{
			EndpointID:   principalIDs.EndpointID,
			ClientID:     principalIDs.ClientID,
			PublicTaskID: validated.TaskID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			_ = tx.Rollback(ctx)
			return nil, a2a.ErrTaskNotFound
		}
		if err != nil {
			return s.finishA2ASendError(ctx, tx, principalIDs, validated, err, "lock task")
		}
		if a2a.TaskState(binding.PublicState).Terminal() {
			_ = tx.Rollback(ctx)
			return nil, a2a.NewError(a2a.ErrInvalidParams, "terminal A2A task cannot be reopened")
		}
		publicProjection, loadErr := qtx.GetA2APublicTaskForClient(ctx, db.GetA2APublicTaskForClientParams{
			EndpointID:   principalIDs.EndpointID,
			ClientID:     principalIDs.ClientID,
			PublicTaskID: validated.TaskID,
		})
		if loadErr != nil {
			return s.finishA2ASendError(ctx, tx, principalIDs, validated, loadErr, "load task context")
		}
		publicTaskID = binding.PublicTaskID
		publicContextID = publicProjection.PublicContextID
		a2aContext = db.A2aContext{ID: binding.ContextID, EndpointID: binding.EndpointID, ClientID: binding.ClientID, PublicContextID: publicProjection.PublicContextID, ChatSessionID: publicProjection.ChatSessionID}
		if validated.ContextID != "" && validated.ContextID != publicContextID {
			_ = tx.Rollback(ctx)
			return nil, a2a.NewError(a2a.ErrInvalidParams, "taskId and contextId do not match")
		}
		if binding.PublicState == string(a2a.TaskStateAuthRequired) && validated.Identity.ContextToken == "" && validated.Identity.DEAPDWSToken == "" && !validated.IdentityExpired {
			_ = tx.Rollback(ctx)
			return nil, a2a.NewError(a2a.ErrInvalidParams, "a valid external identity is required to resume this task")
		}
	} else {
		publicTaskID, err = newA2APublicID("tsk_")
		if err != nil {
			return s.finishA2ASendError(ctx, tx, principalIDs, validated, err, "allocate public task")
		}
		if validated.ContextID != "" {
			a2aContext, err = qtx.LockA2AContextForClient(ctx, db.LockA2AContextForClientParams{
				EndpointID:      principalIDs.EndpointID,
				ClientID:        principalIDs.ClientID,
				PublicContextID: validated.ContextID,
			})
			if errors.Is(err, pgx.ErrNoRows) {
				_ = tx.Rollback(ctx)
				return nil, a2a.ErrTaskNotFound
			}
			if err != nil {
				return s.finishA2ASendError(ctx, tx, principalIDs, validated, err, "lock context")
			}
			publicContextID = a2aContext.PublicContextID
		} else {
			publicContextID, err = newA2APublicID("ctx_")
			if err != nil {
				return s.finishA2ASendError(ctx, tx, principalIDs, validated, err, "allocate context")
			}
			session, createErr := s.resolveChatSessionForNewA2AContext(ctx, qtx, admission, validated)
			if createErr != nil {
				return s.finishA2ASendError(ctx, tx, principalIDs, validated, createErr, "create chat session")
			}
			a2aContext, err = qtx.CreateA2AContext(ctx, db.CreateA2AContextParams{
				PublicContextID: publicContextID,
				ChatSessionID:   session.ID,
				ClientID:        principalIDs.ClientID,
				EndpointID:      principalIDs.EndpointID,
			})
			if err != nil {
				return s.finishA2ASendError(ctx, tx, principalIDs, validated, err, "create context")
			}
		}
	}
	if len(validated.ReferenceTaskIDs) > 0 {
		visible, referenceErr := qtx.CountVisibleA2AReferenceTasks(ctx, db.CountVisibleA2AReferenceTasksParams{
			EndpointID:    principalIDs.EndpointID,
			ClientID:      principalIDs.ClientID,
			PublicTaskIds: validated.ReferenceTaskIDs,
		})
		if referenceErr != nil {
			return s.finishA2ASendError(ctx, tx, principalIDs, validated, referenceErr, "validate references")
		}
		if visible != int64(len(validated.ReferenceTaskIDs)) {
			_ = tx.Rollback(ctx)
			return nil, a2a.NewError(a2a.ErrInvalidParams, "referenceTaskIds contain an unavailable task")
		}
	}
	taskContext := newA2ATaskContext(validated.Identity)
	if !isNewTask && a2a.TaskState(binding.PublicState) == a2a.TaskStateAuthRequired && validated.Identity.ContextToken != "" {
		if _, resumeErr := qtx.ResumeDeferredA2AAuthTurns(ctx, db.ResumeDeferredA2AAuthTurnsParams{
			TaskContext: taskContext,
			BindingID:   binding.ID,
		}); resumeErr != nil {
			return s.finishA2ASendError(ctx, tx, principalIDs, validated, resumeErr, "resume authenticated turns")
		}
		if resumeErr := qtx.ClearDeferredA2AAuthTurnSignals(ctx, binding.ID); resumeErr != nil {
			return s.finishA2ASendError(ctx, tx, principalIDs, validated, resumeErr, "clear resumed authentication signals")
		}
	}
	if validated.IdentityExpired {
		queuedTask, err = qtx.CreatePausedA2AChatTask(ctx, db.CreatePausedA2AChatTaskParams{
			AgentID:       admission.AgentID,
			RuntimeID:     admission.AgentRuntimeID,
			ChatSessionID: a2aContext.ChatSessionID,
			TaskContext:   taskContext,
		})
	} else {
		queuedTask, err = qtx.CreateA2AChatTask(ctx, db.CreateA2AChatTaskParams{
			AgentID:       admission.AgentID,
			RuntimeID:     admission.AgentRuntimeID,
			ChatSessionID: a2aContext.ChatSessionID,
			TaskContext:   taskContext,
		})
	}
	if err != nil {
		return s.finishA2ASendError(ctx, tx, principalIDs, validated, err, "create task")
	}
	queuedTask, err = qtx.SetChatTaskInputOwnerSelf(ctx, queuedTask.ID)
	if err != nil {
		return s.finishA2ASendError(ctx, tx, principalIDs, validated, err, "set task input owner")
	}
	inputMessage, err := qtx.CreateChatMessage(ctx, db.CreateChatMessageParams{
		ChatSessionID: a2aContext.ChatSessionID,
		Role:          "user",
		Content:       content,
		TaskID:        queuedTask.ID,
	})
	if err != nil {
		return s.finishA2ASendError(ctx, tx, principalIDs, validated, err, "create input message")
	}
	attachmentIDs, err := createA2AInputAttachments(ctx, qtx, principalIDs, a2aContext.ChatSessionID, queuedTask.ID, materializedParts)
	if err != nil {
		return s.finishA2ASendError(ctx, tx, principalIDs, validated, err, "create input attachments")
	}
	if len(attachmentIDs) > 0 {
		linked, linkErr := qtx.LinkAttachmentsToChatMessage(ctx, db.LinkAttachmentsToChatMessageParams{
			ChatMessageID: inputMessage.ID,
			ChatSessionID: a2aContext.ChatSessionID,
			WorkspaceID:   principalIDs.WorkspaceID,
			UploaderType:  "agent",
			UploaderID:    principalIDs.AgentID,
			AttachmentIds: attachmentIDs,
		})
		if linkErr != nil || len(linked) != len(attachmentIDs) {
			if linkErr == nil {
				linkErr = errors.New("not all A2A input attachments were linked")
			}
			return s.finishA2ASendError(ctx, tx, principalIDs, validated, linkErr, "link input attachments")
		}
	}
	if isNewTask {
		artifactID, allocateErr := newA2APublicID("art_")
		if allocateErr != nil {
			return s.finishA2ASendError(ctx, tx, principalIDs, validated, allocateErr, "allocate artifact")
		}
		binding, err = qtx.CreateA2ATaskBinding(ctx, db.CreateA2ATaskBindingParams{
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
	}
	turn, err := qtx.CreateA2ATaskTurn(ctx, db.CreateA2ATaskTurnParams{
		MessageID:           validated.MessageID,
		RequestFingerprint:  validated.Fingerprint,
		InputParts:          storedParts,
		MessageExtensions:   validated.MessageExtensions,
		MessageMetadata:     messageMetadata,
		ReferenceTaskIds:    validated.ReferenceTaskIDs,
		AcceptedOutputModes: validated.AcceptedOutputModes,
		CredentialID:        principalIDs.CredentialID,
		LocalTaskID:         queuedTask.ID,
		InputChatMessageID:  inputMessage.ID,
		BindingID:           binding.ID,
		EndpointID:          principalIDs.EndpointID,
		ClientID:            principalIDs.ClientID,
	})
	if err != nil {
		return s.finishA2ASendError(ctx, tx, principalIDs, validated, err, "create task turn")
	}
	if requestBoundClaim != nil {
		leaseNow := time.Now()
		leased, leaseErr := qtx.SetA2ARequestBoundTurnLease(ctx, db.SetA2ARequestBoundTurnLeaseParams{
			LeaseExpiresAt:     pgtype.Timestamptz{Time: leaseNow.Add(a2aRequestBoundTurnLeaseDuration), Valid: true},
			LocalTaskID:        queuedTask.ID,
			RequestFingerprint: validated.Fingerprint,
		})
		if leaseErr != nil {
			return s.finishA2ASendError(ctx, tx, principalIDs, validated, leaseErr, "set request-bound turn lease")
		}
		requestBoundClaim.localTaskID = leased.LocalTaskID
		requestBoundClaim.chatSessionID = leased.ChatSessionID
		requestBoundClaim.nextLeaseRenewal = leaseNow.Add(a2aRequestBoundTurnLeaseRenewal)
	}
	publicState := a2a.TaskStateSubmitted
	if !isNewTask && a2a.TaskState(binding.PublicState) == a2a.TaskStateWorking {
		publicState = a2a.TaskStateWorking
	}
	statusMessage := []byte(nil)
	if validated.IdentityExpired {
		controlMessage := a2a.NewMessageForTask(a2a.MessageRoleAgent, &a2a.Task{ID: a2a.TaskID(publicTaskID), ContextID: publicContextID}, a2a.NewTextPart("A fresh external ContextToken is required to continue."))
		statusMessage, _ = json.Marshal(controlMessage)
		if _, err = qtx.SetA2ATurnControlSignal(ctx, db.SetA2ATurnControlSignalParams{
			ControlSignal:  pgtype.Text{String: "auth_required", Valid: true},
			ControlPayload: statusMessage,
			LocalTaskID:    queuedTask.ID,
		}); err != nil {
			return s.finishA2ASendError(ctx, tx, principalIDs, validated, err, "pause task for authentication")
		}
		publicState = a2a.TaskStateAuthRequired
	}
	updatedBinding, err := qtx.UpdateA2ATaskPublicState(ctx, db.UpdateA2ATaskPublicStateParams{
		PublicState:   string(publicState),
		StatusMessage: statusMessage,
		BindingID:     binding.ID,
	})
	if err == nil {
		binding = updatedBinding
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return s.finishA2ASendError(ctx, tx, principalIDs, validated, err, "update public task state")
	}
	event, err := appendA2AStatusEvent(ctx, qtx, binding, publicContextID, publicState, statusMessage, "turn:"+fmt.Sprint(turn.Sequence)+":"+strings.ToLower(publicState.String()))
	if err != nil {
		return s.finishA2ASendError(ctx, tx, principalIDs, validated, err, "append task event")
	}
	if validated.PushConfig != nil {
		if _, err = s.createTaskPushConfigInTx(ctx, qtx, principalIDs, binding, validated.PushConfig); err != nil {
			_ = tx.Rollback(ctx)
			return nil, err
		}
	}
	if err = qtx.EnqueueA2APushDeliveriesForEvent(ctx, event.ID); err != nil {
		return s.finishA2ASendError(ctx, tx, principalIDs, validated, err, "enqueue push event")
	}

	if err := tx.Commit(ctx); err != nil {
		return s.finishA2ASendError(ctx, tx, principalIDs, validated, err, "commit task")
	}
	committed = true

	// A daemon may only observe a task after every A2A identity row commits.
	// Context turns share a Chat Session, so wake only the oldest runnable turn.
	if requestBoundClaim != nil {
		s.maintainA2ARequestBoundTurn(ctx)
	} else if !validated.IdentityExpired {
		s.notifyNextA2ATask(ctx, a2aContext.ChatSessionID)
	}
	if s.PushNotifier != nil {
		s.PushNotifier.NotifyA2APush()
	}
	task, err := s.GetTask(ctx, &a2a.GetTaskRequest{ID: a2a.TaskID(publicTaskID), HistoryLength: a2aSendHistoryLength(request)})
	if err != nil {
		return nil, err
	}
	return s.completeA2ASend(ctx, validated, task)
}

func (s *A2AService) notifyNextA2ATask(ctx context.Context, chatSessionID pgtype.UUID) {
	if s == nil || s.Queries == nil || s.TxStarter == nil || s.TaskService == nil || !chatSessionID.Valid {
		return
	}
	tx, err := s.TxStarter.Begin(ctx)
	if err != nil {
		slog.Warn("begin next A2A turn transaction failed", "chat_session_id", chatSessionID, "error", err)
		return
	}
	defer tx.Rollback(ctx)
	qtx := s.Queries.WithTx(tx)
	candidate, err := qtx.LockNextDeferredA2ATurnForChatSession(ctx, chatSessionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return
	}
	if err != nil {
		slog.Warn("lock next A2A turn failed", "chat_session_id", chatSessionID, "error", err)
		return
	}
	if candidate.ControlSignal.Valid && candidate.ControlSignal.String != a2aRequestBoundControlSignal {
		if err = tx.Commit(ctx); err != nil {
			slog.Warn("commit blocked A2A turn check failed", "chat_session_id", chatSessionID, "error", err)
		}
		return
	}
	currentPublicState := a2a.TaskState(candidate.PublicState)
	if currentPublicState == a2a.TaskStateInputRequired || currentPublicState == a2a.TaskStateAuthRequired {
		// Input queued before the Agent entered a waiting state does not by itself
		// satisfy the later request. A new same-task SendMessage is the explicit
		// resume signal; it moves the public Task back to SUBMITTED before calling
		// this scheduler. AUTH_REQUIRED additionally applies the fresh token to
		// every pending FIFO row before that transition.
		if err = tx.Commit(ctx); err != nil {
			slog.Warn("commit waiting A2A turn check failed", "chat_session_id", chatSessionID, "error", err)
		}
		return
	}
	identityDisposition := a2AQueuedExternalIdentityDispositionFor(
		ctx,
		candidate.TaskContext,
		candidate.RequestFingerprint,
		candidate.RequestBoundLeaseExpiresAt,
		time.Now(),
	)
	if identityDisposition == a2aQueuedIdentityWait {
		if err = tx.Commit(ctx); err != nil {
			slog.Warn("commit request-bound A2A turn wait failed", "chat_session_id", chatSessionID, "error", err)
		}
		return
	}
	if identityDisposition == a2aQueuedIdentityAuthRequired {
		prompt := "A fresh external ContextToken is required to continue."
		if requiresA2ADEAPDWSToken(candidate.TaskContext) {
			prompt = "A fresh request-scoped X-DWS-Token is required to continue."
		}
		message := &a2a.Message{
			ID:        stableA2AAgentMessageID(candidate.PublicTaskID, candidate.TurnSequence),
			Role:      a2a.MessageRoleAgent,
			TaskID:    a2a.TaskID(candidate.PublicTaskID),
			ContextID: candidate.PublicContextID,
			Parts:     a2a.ContentParts{a2a.NewTextPart(prompt)},
			Metadata:  map[string]any{},
		}
		statusMessage, marshalErr := json.Marshal(message)
		if marshalErr != nil {
			slog.Warn("encode expired A2A identity prompt failed", "chat_session_id", chatSessionID, "error", marshalErr)
			return
		}
		if _, err = qtx.SetA2ATurnControlSignal(ctx, db.SetA2ATurnControlSignalParams{
			ControlSignal:  pgtype.Text{String: "auth_required", Valid: true},
			ControlPayload: statusMessage,
			LocalTaskID:    candidate.LocalTaskID,
		}); err != nil {
			slog.Warn("pause expired A2A identity turn failed", "chat_session_id", chatSessionID, "error", err)
			return
		}
		if err = qtx.ClearDeferredA2ALocalTaskIdentityContext(ctx, candidate.LocalTaskID); err != nil {
			slog.Warn("clear expired A2A identity failed", "chat_session_id", chatSessionID, "error", err)
			return
		}
		binding, updateErr := qtx.UpdateA2ATaskPublicState(ctx, db.UpdateA2ATaskPublicStateParams{
			PublicState:   a2a.TaskStateAuthRequired.String(),
			StatusMessage: statusMessage,
			BindingID:     candidate.BindingID,
		})
		if updateErr != nil {
			slog.Warn("publish expired A2A identity state failed", "chat_session_id", chatSessionID, "error", updateErr)
			return
		}
		dedupeKey := "turn:" + strconv.Itoa(int(candidate.TurnSequence)) + ":auth_required_expired"
		event, appendErr := appendA2AStatusEvent(
			ctx, qtx, binding, candidate.PublicContextID, a2a.TaskStateAuthRequired, statusMessage, dedupeKey,
		)
		if appendErr != nil {
			slog.Warn("append expired A2A identity event failed", "chat_session_id", chatSessionID, "error", appendErr)
			return
		}
		if err = qtx.EnqueueA2APushDeliveriesForEvent(ctx, event.ID); err != nil {
			slog.Warn("enqueue expired A2A identity push failed", "chat_session_id", chatSessionID, "error", err)
			return
		}
		if err = tx.Commit(ctx); err != nil {
			slog.Warn("commit expired A2A identity state failed", "chat_session_id", chatSessionID, "error", err)
			return
		}
		if s.PushNotifier != nil {
			s.PushNotifier.NotifyA2APush()
		}
		return
	}
	if candidate.ControlSignal.Valid && candidate.ControlSignal.String == a2aRequestBoundControlSignal {
		if _, err = qtx.ClearA2ARequestBoundTurnSignal(ctx, db.ClearA2ARequestBoundTurnSignalParams{
			LocalTaskID:        candidate.LocalTaskID,
			RequestFingerprint: candidate.RequestFingerprint,
		}); err != nil {
			slog.Warn("clear request-bound A2A turn signal failed", "chat_session_id", chatSessionID, "error", err)
			return
		}
	}
	next, err := qtx.PromoteNextRunnableA2ATaskForChatSession(ctx, chatSessionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return
	}
	if err != nil {
		slog.Warn("promote next runnable A2A turn failed", "chat_session_id", chatSessionID, "error", err)
		return
	}
	if err = tx.Commit(ctx); err != nil {
		slog.Warn("commit next runnable A2A turn failed", "chat_session_id", chatSessionID, "error", err)
		return
	}
	if claim := a2aRequestBoundTurnClaimFromContext(ctx); claim != nil && sameUUID(next.ID, claim.localTaskID) {
		claim.promoted = true
	}
	s.TaskService.NotifyA2ATaskEnqueued(ctx, next)
}

func (s *A2AService) resolveChatSessionForNewA2AContext(
	ctx context.Context,
	qtx *db.Queries,
	admission db.LockAgentA2ASendAdmissionRow,
	validated validatedA2ASend,
) (db.ChatSession, error) {
	if strings.TrimSpace(validated.Identity.DEAPDWSToken) != "" {
		conversationID := deapA2AOpenConversationID(validated.RequestMetadata, validated.Metadata)
		if conversationID != "" {
			if session, ok := lookupDingTalkConversationChatSession(
				ctx,
				qtx,
				admission.WorkspaceID,
				admission.AgentID,
				conversationID,
			); ok {
				slog.Info(
					"reused DingTalk chat session for DEAP A2A turn",
					"agent_id", util.UUIDToString(admission.AgentID),
					"chat_session_id", util.UUIDToString(session.ID),
				)
				return session, nil
			}
		}
	}
	return qtx.CreateChatSession(ctx, db.CreateChatSessionParams{
		WorkspaceID:  admission.WorkspaceID,
		AgentID:      admission.AgentID,
		CreatorID:    admission.DelegatedByUserID,
		Title:        admission.CardName,
		IsAgentIntro: false,
	})
}

func lookupDingTalkConversationChatSession(
	ctx context.Context,
	qtx *db.Queries,
	workspaceID pgtype.UUID,
	agentID pgtype.UUID,
	conversationID string,
) (db.ChatSession, bool) {
	installation, err := qtx.GetActiveDingTalkBotInstallationByAgent(ctx, db.GetActiveDingTalkBotInstallationByAgentParams{
		WorkspaceID: workspaceID,
		AgentID:     agentID,
	})
	if err != nil {
		return db.ChatSession{}, false
	}
	binding, err := qtx.GetChannelChatSessionBinding(ctx, db.GetChannelChatSessionBindingParams{
		InstallationID: installation.ID,
		ChannelChatID:  conversationID,
	})
	if err != nil {
		return db.ChatSession{}, false
	}
	session, err := qtx.GetChatSessionInWorkspace(ctx, db.GetChatSessionInWorkspaceParams{
		ID:          binding.ChatSessionID,
		WorkspaceID: workspaceID,
	})
	if err != nil || !sameUUID(session.AgentID, agentID) {
		return db.ChatSession{}, false
	}
	return session, true
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
	if request == nil || strings.TrimSpace(string(request.ID)) == "" ||
		strings.TrimSpace(string(request.ID)) != string(request.ID) || len(request.ID) > 128 {
		return nil, a2a.NewError(a2a.ErrInvalidParams, "task id is required")
	}
	if request.HistoryLength != nil && (*request.HistoryLength < 0 || *request.HistoryLength > 100) {
		return nil, a2a.NewError(a2a.ErrInvalidParams, "historyLength must be between 0 and 100")
	}
	projection, err := s.Queries.GetA2APublicTaskForClient(ctx, db.GetA2APublicTaskForClientParams{
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
	projection, err = s.reconcileA2ATaskState(ctx, projection)
	if err != nil {
		slog.Error("reconcile A2A task state failed", "public_task_id", request.ID, "error", err)
		return nil, a2a.NewError(a2a.ErrInternalError, "unable to reconcile A2A task")
	}
	historyLength := 50
	if request.HistoryLength != nil {
		historyLength = *request.HistoryLength
	}
	return s.projectA2APublicTask(ctx, projection, historyLength, true)
}

func (s *A2AService) replayA2AMessage(ctx context.Context, principal a2aPrincipalIDs, request validatedA2ASend) (*a2a.Task, bool, error) {
	claim, err := s.Queries.GetA2AMessageTurnClaimForClient(ctx, db.GetA2AMessageTurnClaimForClientParams{
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
	if claim.RequestFingerprint != request.Fingerprint {
		return nil, true, a2a.NewError(a2a.ErrInvalidParams, "message id conflicts with a different A2A request")
	}
	if requestBoundClaim := a2aRequestBoundTurnClaimFromContext(ctx); requestBoundClaim != nil {
		requestBoundClaim.localTaskID = claim.LocalTaskID
		s.maintainA2ARequestBoundTurn(ctx)
	}
	historyLength := request.HistoryLength
	task, err := s.GetTask(ctx, &a2a.GetTaskRequest{ID: a2a.TaskID(claim.PublicTaskID), HistoryLength: &historyLength})
	return task, true, err
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
		claim, err := s.Queries.GetA2AMessageTurnClaimForClient(ctx, db.GetA2AMessageTurnClaimForClientParams{
			EndpointID: principal.EndpointID,
			ClientID:   principal.ClientID,
			MessageID:  request.MessageID,
		})
		if err == nil {
			if claim.RequestFingerprint != request.Fingerprint {
				return nil, a2a.NewError(a2a.ErrInvalidParams, "message id conflicts with a different A2A request")
			}
			if requestBoundClaim := a2aRequestBoundTurnClaimFromContext(ctx); requestBoundClaim != nil {
				requestBoundClaim.localTaskID = claim.LocalTaskID
				s.maintainA2ARequestBoundTurn(ctx)
			}
			historyLength := request.HistoryLength
			task, replayErr := s.GetTask(ctx, &a2a.GetTaskRequest{ID: a2a.TaskID(claim.PublicTaskID), HistoryLength: &historyLength})
			if replayErr != nil {
				return nil, replayErr
			}
			return s.completeA2ASend(ctx, request, task)
		}
	}
	slog.Error("accept A2A message failed", "stage", stage, "message_id", request.MessageID, "error", cause)
	return nil, a2a.NewError(a2a.ErrInternalError, "unable to accept A2A message")
}

func validateA2ASendRequest(ctx context.Context, request *a2a.SendMessageRequest, now time.Time) (validatedA2ASend, error) {
	if request == nil || request.Message == nil {
		return validatedA2ASend{}, a2a.NewError(a2a.ErrInvalidParams, "message is required")
	}
	if strings.TrimSpace(request.Tenant) != "" {
		return validatedA2ASend{}, a2a.NewError(a2a.ErrInvalidParams, "tenant is not supported by this endpoint")
	}
	returnImmediately := request.Config != nil && request.Config.ReturnImmediately
	// SendMessage responses are minimal by default: the submitted user message
	// must not be repeated alongside the answer artifact. Callers that need
	// inline history can request it explicitly; GetTask keeps its own history
	// default for task inspection.
	historyLength := 0
	var pushConfig *a2a.PushConfig
	acceptedOutputModes := []string{}
	if request.Config != nil {
		pushConfig = request.Config.PushConfig
		if request.Config.HistoryLength != nil {
			historyLength = *request.Config.HistoryLength
		}
	}
	if historyLength < 0 || historyLength > 100 {
		return validatedA2ASend{}, a2a.NewError(a2a.ErrInvalidParams, "historyLength must be between 0 and 100")
	}
	if request.Config != nil {
		if len(request.Config.AcceptedOutputModes) > 32 {
			return validatedA2ASend{}, a2a.NewError(a2a.ErrInvalidParams, "acceptedOutputModes supports at most 32 media types")
		}
		seenOutputModes := make(map[string]struct{}, len(request.Config.AcceptedOutputModes))
		for _, rawMode := range request.Config.AcceptedOutputModes {
			mode := strings.TrimSpace(rawMode)
			parsed, parameters, err := mime.ParseMediaType(mode)
			if err != nil || len(parameters) != 0 || mode != rawMode {
				return validatedA2ASend{}, a2a.NewError(a2a.ErrInvalidParams, "acceptedOutputModes contains an invalid media type")
			}
			mode = strings.ToLower(parsed)
			if _, duplicate := seenOutputModes[mode]; duplicate {
				return validatedA2ASend{}, a2a.NewError(a2a.ErrInvalidParams, "acceptedOutputModes must not contain duplicates")
			}
			seenOutputModes[mode] = struct{}{}
			acceptedOutputModes = append(acceptedOutputModes, mode)
		}
	}
	message := request.Message
	if message.Role != a2a.MessageRoleUser {
		return validatedA2ASend{}, a2a.NewError(a2a.ErrInvalidParams, "message role must be user")
	}
	messageID := message.ID
	if strings.TrimSpace(messageID) == "" || strings.TrimSpace(messageID) != messageID || utf8.RuneCountInString(messageID) > 256 {
		return validatedA2ASend{}, a2a.NewError(a2a.ErrInvalidParams, "message id must be 1 to 256 non-whitespace characters")
	}
	contextID := strings.TrimSpace(message.ContextID)
	taskID := strings.TrimSpace(string(message.TaskID))
	if contextID != message.ContextID || utf8.RuneCountInString(contextID) > 256 || taskID != string(message.TaskID) || utf8.RuneCountInString(taskID) > 128 {
		return validatedA2ASend{}, a2a.NewError(a2a.ErrInvalidParams, "taskId or contextId is invalid")
	}
	if len(message.ReferenceTasks) > 10 {
		return validatedA2ASend{}, a2a.NewError(a2a.ErrInvalidParams, "referenceTaskIds supports at most 10 tasks")
	}
	references := make([]string, 0, len(message.ReferenceTasks))
	seenReferences := make(map[string]struct{}, len(message.ReferenceTasks))
	for _, reference := range message.ReferenceTasks {
		value := strings.TrimSpace(string(reference))
		if value == "" || value != string(reference) || utf8.RuneCountInString(value) > 128 {
			return validatedA2ASend{}, a2a.NewError(a2a.ErrInvalidParams, "referenceTaskIds contains an invalid task id")
		}
		if _, duplicate := seenReferences[value]; duplicate {
			return validatedA2ASend{}, a2a.NewError(a2a.ErrInvalidParams, "referenceTaskIds must not contain duplicates")
		}
		seenReferences[value] = struct{}{}
		references = append(references, value)
	}
	if err := validateA2AParts(message.Parts); err != nil {
		return validatedA2ASend{}, err
	}
	messageExtensions, err := normalizeA2AExtensions(message.Extensions, "message extensions")
	if err != nil {
		return validatedA2ASend{}, a2a.NewError(a2a.ErrInvalidParams, err.Error())
	}
	if _, err := json.Marshal(message.Metadata); err != nil {
		return validatedA2ASend{}, a2a.NewError(a2a.ErrInvalidParams, "message metadata is not valid JSON")
	}
	identity, _ := a2aintegration.InvocationIdentityFromContext(ctx)
	if identity.ContextToken != "" && identity.DEAPDWSToken != "" {
		return validatedA2ASend{}, a2a.NewError(a2a.ErrInvalidParams, "external identity headers are ambiguous")
	}
	if identity.DEAPDWSToken != "" && returnImmediately && !identity.RequestBound {
		return validatedA2ASend{}, a2a.NewError(a2a.ErrInvalidParams, "X-DWS-Token requires blocking or streaming execution")
	}
	fingerprint, err := fingerprintA2ASendRequest(request, identity)
	if err != nil {
		return validatedA2ASend{}, a2a.NewError(a2a.ErrInvalidParams, "message cannot be fingerprinted")
	}
	identityExpired := false
	if identity.ContextToken != "" {
		expiresAt := time.UnixMilli(identity.ExpiresAtUnixMS)
		switch {
		case !expiresAt.After(now):
			identityExpired = true
			identity.ContextToken = ""
		case expiresAt.Before(now.Add(time.Minute)):
			return validatedA2ASend{}, a2a.NewError(a2a.ErrInvalidParams, "external ContextToken must remain valid for at least 60 seconds")
		}
	}
	return validatedA2ASend{
		MessageID:           messageID,
		Fingerprint:         fingerprint,
		ReturnImmediately:   returnImmediately,
		ContextID:           contextID,
		TaskID:              taskID,
		Parts:               message.Parts,
		MessageExtensions:   messageExtensions,
		Metadata:            message.Metadata,
		RequestMetadata:     request.Metadata,
		ReferenceTaskIDs:    references,
		AcceptedOutputModes: acceptedOutputModes,
		HistoryLength:       historyLength,
		PushConfig:          pushConfig,
		Identity:            identity,
		IdentityExpired:     identityExpired,
	}, nil
}

func a2aSendHistoryLength(request *a2a.SendMessageRequest) *int {
	historyLength := 0
	if request != nil && request.Config != nil && request.Config.HistoryLength != nil {
		historyLength = *request.Config.HistoryLength
	}
	return &historyLength
}

func (s *A2AService) completeA2ASend(ctx context.Context, request validatedA2ASend, task *a2a.Task) (a2a.SendMessageResult, error) {
	if request.ReturnImmediately || a2aSendWaitComplete(task.Status.State) {
		return task, nil
	}
	terminal, err := waitForA2ATerminalTask(ctx, task, a2aBlockingPollInterval, func(loadCtx context.Context, taskID a2a.TaskID) (*a2a.Task, error) {
		s.maintainA2ARequestBoundTurn(loadCtx)
		historyLength := request.HistoryLength
		return s.GetTask(loadCtx, &a2a.GetTaskRequest{ID: taskID, HistoryLength: &historyLength})
	})
	if err != nil {
		return nil, err
	}
	return terminal, nil
}

func waitForA2ATerminalTask(
	ctx context.Context,
	task *a2a.Task,
	pollInterval time.Duration,
	load func(context.Context, a2a.TaskID) (*a2a.Task, error),
) (*a2a.Task, error) {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			current, err := load(ctx, task.ID)
			if err != nil {
				if contextErr := ctx.Err(); contextErr != nil {
					return nil, contextErr
				}
				return nil, err
			}
			if a2aSendWaitComplete(current.Status.State) {
				return current, nil
			}
		}
	}
}

func a2aSendWaitComplete(state a2a.TaskState) bool {
	return state.Terminal() || state == a2a.TaskStateInputRequired || state == a2a.TaskStateAuthRequired
}

func fingerprintA2ASendRequest(request *a2a.SendMessageRequest, identity a2aintegration.InvocationIdentity) (string, error) {
	var contextTokenDigest string
	if identity.ContextToken != "" {
		sum := sha256.Sum256([]byte(identity.ContextToken))
		contextTokenDigest = hex.EncodeToString(sum[:])
	}
	var dwsTokenDigest string
	if identity.DEAPDWSToken != "" {
		sum := sha256.Sum256([]byte(identity.DEAPDWSToken))
		dwsTokenDigest = hex.EncodeToString(sum[:])
	}
	envelope := struct {
		Request                    *a2a.SendMessageRequest `json:"request"`
		IdentityExtensionDeclared  bool                    `json:"identityExtensionDeclared"`
		IdentityTokenSHA256        string                  `json:"identityTokenSha256,omitempty"`
		DEAPDWSTokenSHA256         string                  `json:"deapDwsTokenSha256,omitempty"`
		IdentityExpiresAtUnixMilli int64                   `json:"identityExpiresAtUnixMilli,omitempty"`
	}{
		Request:                    request,
		IdentityExtensionDeclared:  identity.ExtensionDeclared,
		IdentityTokenSHA256:        contextTokenDigest,
		DEAPDWSTokenSHA256:         dwsTokenDigest,
		IdentityExpiresAtUnixMilli: identity.ExpiresAtUnixMS,
	}
	encoded, err := json.Marshal(envelope)
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

func uuidStringOrOpaque(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:16])
}

func deleteMaterializedA2AParts(store storage.Storage, parts []materializedA2APart) {
	if store == nil {
		return
	}
	keys := make([]string, 0, len(parts))
	for _, part := range parts {
		if part.Stored.ObjectKey != "" {
			keys = append(keys, part.Stored.ObjectKey)
		}
	}
	store.DeleteKeys(context.Background(), keys)
}

func createA2AInputAttachments(
	ctx context.Context,
	queries *db.Queries,
	principal a2aPrincipalIDs,
	chatSessionID pgtype.UUID,
	localTaskID pgtype.UUID,
	parts []materializedA2APart,
) ([]pgtype.UUID, error) {
	ids := make([]pgtype.UUID, 0)
	for _, part := range parts {
		if part.Stored.Kind != "object" {
			continue
		}
		id, err := uuid.NewV7()
		if err != nil {
			return nil, fmt.Errorf("allocate A2A attachment id: %w", err)
		}
		attachmentID := pgtype.UUID{Bytes: id, Valid: true}
		if _, err = queries.CreateAttachment(ctx, db.CreateAttachmentParams{
			ID:            attachmentID,
			WorkspaceID:   principal.WorkspaceID,
			UploaderType:  "agent",
			UploaderID:    principal.AgentID,
			Filename:      part.Stored.Filename,
			Url:           part.ObjectURL,
			ContentType:   part.Stored.MediaType,
			SizeBytes:     part.Stored.SizeBytes,
			ChatSessionID: chatSessionID,
			TaskID:        localTaskID,
		}); err != nil {
			return nil, fmt.Errorf("create A2A attachment: %w", err)
		}
		ids = append(ids, attachmentID)
	}
	return ids, nil
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
