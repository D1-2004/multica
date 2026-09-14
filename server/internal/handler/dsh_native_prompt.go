package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/chattrace"
	"github.com/multica-ai/multica/server/internal/dshhost"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type dshNativePromptReceipt = service.DSHNativePromptReceipt
type dshNativePromptSubmit = service.DSHNativePromptSubmit

// Native capabilities authenticate the human independently of ordinary JWT
// middleware. Neither a user ID nor a platform session ID is accepted from the
// client. Registration resolves the platform scope from the native identity.
func dshNativePromptHandler(w http.ResponseWriter, r *http.Request, manager dshhost.NativeAccessManager, submit dshNativePromptSubmit) {
	dshNativeResponseHeaders(w)
	var input struct {
		WorkspaceID string `json:"workspace_id"`
		AgentID     string `json:"agent_id"`
		Generation  int64  `json:"generation"`
		SandboxID   string `json:"sandbox_id"`
		SessionID   string `json:"session_id"`
		RequestID   string `json:"request_id"`
		Workdir     string `json:"workdir"`
		Content     string `json:"content"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2*1024*1024))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || !errors.Is(decoder.Decode(&struct{}{}), io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid DSH native prompt")
		return
	}
	workspace, ok := parseUUIDOrBadRequest(w, input.WorkspaceID, "workspace_id")
	if !ok {
		return
	}
	agent, ok := parseUUIDOrBadRequest(w, input.AgentID, "agent_id")
	if !ok {
		return
	}
	request, ok := parseUUIDOrBadRequest(w, input.RequestID, "request_id")
	if !ok {
		return
	}
	chatInput := service.DSHNativeChatInput{SessionID: input.SessionID, RequestID: uuid.UUID(request.Bytes), Workdir: input.Workdir}
	if chatInput.RequestID.String() != input.RequestID || chatInput.Validate(input.Content) != nil {
		writeError(w, http.StatusBadRequest, "invalid DSH native prompt")
		return
	}
	headers := r.Header.Values("Authorization")
	if len(headers) != 1 || !strings.HasPrefix(headers[0], "Bearer ") || r.URL.RawQuery != "" {
		writeError(w, http.StatusUnauthorized, "DSH native access denied")
		return
	}
	host := dshhost.Host{Key: dshhost.Key{WorkspaceID: uuid.UUID(workspace.Bytes), AgentID: uuid.UUID(agent.Bytes)}, Generation: input.Generation, SandboxID: input.SandboxID}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	authCtx, authCancel := context.WithTimeout(ctx, 5*time.Second)
	access, err := manager.Authorize(authCtx, strings.TrimPrefix(headers[0], "Bearer "), host)
	authCancel()
	if err != nil {
		writeError(w, http.StatusUnauthorized, "DSH native access denied")
		return
	}
	if submit == nil {
		writeError(w, http.StatusServiceUnavailable, "DSH native prompt admission is unavailable")
		return
	}
	receipt, err := submit(ctx, access, chatInput, input.Content)
	if err != nil {
		switch {
		case errors.Is(err, dshhost.ErrNativeAccessDenied):
			writeError(w, http.StatusForbidden, "DSH native invocation is no longer authorized")
		case errors.Is(err, service.ErrDSHNativeInput):
			writeError(w, http.StatusBadRequest, "invalid DSH native prompt")
		case errors.Is(err, dshhost.ErrChanged), errors.Is(err, service.ErrChatSessionArchived):
			writeError(w, http.StatusConflict, "DSH native session or request conflicts with an existing admission")
		default:
			writeError(w, http.StatusServiceUnavailable, "DSH native admission is unconfirmed; retry with the same request identity")
		}
		return
	}
	status := http.StatusCreated
	if receipt.Replayed {
		status = http.StatusOK
	}
	writeJSON(w, status, receipt)
}

func (h *Handler) dshNativeInvoke(ctx context.Context, q *db.Queries, agent db.Agent, userID pgtype.UUID) error {
	if q == nil || !canInvokeAgentWithStore(ctx, q, agent, "member", uuidToString(userID), uuidToString(userID), uuidToString(agent.WorkspaceID)) {
		return dshhost.ErrNativeAccessDenied
	}
	return nil
}

func (h *Handler) submitDSHNativePrompt(ctx context.Context, access dshhost.NativeAccess, input service.DSHNativeChatInput, content string) (dshNativePromptReceipt, error) {
	var out dshNativePromptReceipt
	if h.Queries == nil || h.TaskService == nil {
		return out, errors.New("DSH task admission unavailable")
	}
	agent, err := h.Queries.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{
		ID: pgtype.UUID{Bytes: access.AgentID, Valid: true}, WorkspaceID: pgtype.UUID{Bytes: access.WorkspaceID, Valid: true},
	})
	if err != nil {
		return out, dshhost.ErrNativeAccessDenied
	}
	registered, err := h.TaskService.RegisterDSHNativeChatSession(ctx, agent, access, input.SessionID, input.Workdir, h.dshNativeInvoke)
	if err != nil {
		return out, err
	}
	trace := chattrace.New("dsh_native_prompt")
	result, err := h.TaskService.SendDSHNativeChatMessage(ctx, registered.Session, agent, access, input, content, trace, h.dshNativeInvoke)
	if err != nil {
		return out, err
	}
	if !result.Replayed {
		h.publishChat(protocol.EventChatMessage, access.WorkspaceID.String(), "member", access.UserID.String(), uuidToString(registered.Session.ID), protocol.ChatMessagePayload{
			ChatSessionID: uuidToString(registered.Session.ID), MessageID: uuidToString(result.Message.ID), Role: "user", Content: result.Message.Content,
			TaskID: uuidToString(result.Task.ID), CreatedAt: timestampToString(result.Message.CreatedAt), TraceID: trace.TraceID,
		})
	}
	return dshNativePromptReceipt{SessionID: input.SessionID, RequestID: input.RequestID, ChatSessionID: registered.Session.ID, TaskID: result.Task.ID, MessageID: result.Message.ID, Queued: result.Queued, Replayed: result.Replayed}, nil
}

func (h *Handler) SubmitDSHNativePrompt(w http.ResponseWriter, r *http.Request) {
	dshNativePromptHandler(w, r, h.dshNativeAccessManager(), h.submitDSHNativePrompt)
}
