package handler

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

var errAgentDispatchAcceptanceConflict = errors.New("idempotency key conflicts with another dispatch")
var errAgentDispatchAcceptancePending = errors.New("dispatch acceptance is still pending")

type bufferedDispatchResponse struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func newBufferedDispatchResponse() *bufferedDispatchResponse {
	return &bufferedDispatchResponse{header: make(http.Header)}
}

func (w *bufferedDispatchResponse) Header() http.Header {
	return w.header
}

func (w *bufferedDispatchResponse) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}

func (w *bufferedDispatchResponse) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.body.Write(body)
}

func (w *bufferedDispatchResponse) Status() int {
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}

func (w *bufferedDispatchResponse) Forward(target http.ResponseWriter) {
	for key, values := range w.header {
		for _, value := range values {
			target.Header().Add(key, value)
		}
	}
	target.WriteHeader(w.Status())
	_, _ = target.Write(w.body.Bytes())
}

func dispatchRequestFingerprint(command DispatchCommand, idempotencyKey string) string {
	payload := struct {
		SchemaVersion  string                     `json:"schemaVersion"`
		AgentID        string                     `json:"agentId,omitempty"`
		Continuation   *AgentDispatchContinuation `json:"continuation"`
		Source         DispatchSource             `json:"source"`
		Event          DispatchEvent              `json:"event"`
		Surface        DispatchSurface            `json:"surface"`
		Outbound       DispatchOutbound           `json:"outbound"`
		CallbackURL    string                     `json:"callbackUrl"`
		CallbackTarget string                     `json:"callbackTarget"`
		IdempotencyKey string                     `json:"idempotencyKey"`
		EndpointID     string                     `json:"endpointId"`
	}{
		SchemaVersion:  command.SchemaVersion,
		AgentID:        command.AgentID,
		Continuation:   command.Continuation,
		Source:         command.Source,
		Event:          command.Event,
		Surface:        command.Surface,
		Outbound:       command.Outbound,
		IdempotencyKey: idempotencyKey,
		EndpointID:     command.DispatchEndpointID,
	}
	if command.CompletionCallback != nil {
		payload.CallbackURL = command.CompletionCallback.URL
		payload.CallbackTarget = command.CompletionCallback.Target
	}
	raw, _ := json.Marshal(payload)
	digest := sha256.Sum256(raw)
	return fmt.Sprintf("sha256:%x", digest)
}

func (h *Handler) claimAgentDispatchAcceptance(
	ctx context.Context,
	command DispatchCommand,
	dispatchContext agentDispatchContext,
	idempotencyKey string,
) (db.AgentDispatchAcceptance, bool, error) {
	fingerprint := dispatchRequestFingerprint(command, idempotencyKey)
	params := db.ClaimAgentDispatchAcceptanceParams{
		EndpointID:         dispatchContext.EndpointNamespaceID,
		AgentID:            dispatchContext.AgentID,
		TargetIdentity:     command.CompletionCallback.Target,
		IdempotencyKey:     idempotencyKey,
		RequestFingerprint: fingerprint,
	}
	acceptance, err := h.Queries.ClaimAgentDispatchAcceptance(ctx, params)
	if err == nil {
		return acceptance, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return db.AgentDispatchAcceptance{}, false, err
	}
	existing, err := h.Queries.GetAgentDispatchAcceptance(
		ctx,
		db.GetAgentDispatchAcceptanceParams{
			EndpointID:     dispatchContext.EndpointNamespaceID,
			IdempotencyKey: idempotencyKey,
		},
	)
	if err != nil {
		return db.AgentDispatchAcceptance{}, false, err
	}
	if existing.RequestFingerprint != fingerprint ||
		existing.AgentID != dispatchContext.AgentID ||
		existing.TargetIdentity != command.CompletionCallback.Target {
		return db.AgentDispatchAcceptance{}, false, errAgentDispatchAcceptanceConflict
	}
	if existing.Status == "accepted" {
		return existing, true, nil
	}
	return db.AgentDispatchAcceptance{}, false, errAgentDispatchAcceptancePending
}

func (h *Handler) completeAgentDispatchAcceptance(
	ctx context.Context,
	acceptance db.AgentDispatchAcceptance,
	response *bufferedDispatchResponse,
) error {
	var responseTask struct {
		TaskID string `json:"taskId"`
	}
	_ = json.Unmarshal(response.body.Bytes(), &responseTask)
	rootTaskID := pgtype.UUID{}
	if strings.TrimSpace(responseTask.TaskID) != "" {
		rootTaskID = parseUUID(responseTask.TaskID)
	}
	_, err := h.Queries.CompleteAgentDispatchAcceptance(
		ctx,
		db.CompleteAgentDispatchAcceptanceParams{
			ResponseStatus: pgtype.Int4{Int32: int32(response.Status()), Valid: true},
			ResponseContentType: pgtype.Text{
				String: response.header.Get("Content-Type"),
				Valid:  response.header.Get("Content-Type") != "",
			},
			ResponseBody: append([]byte{}, response.body.Bytes()...),
			RootTaskID:   rootTaskID,
			ID:           acceptance.ID,
			LeaseToken:   acceptance.LeaseToken,
		},
	)
	return err
}

func (h *Handler) recoverAgentDispatchAcceptance(
	ctx context.Context,
	command DispatchCommand,
	dispatchContext agentDispatchContext,
	acceptance db.AgentDispatchAcceptance,
) (*bufferedDispatchResponse, bool, error) {
	rootTask, err := h.Queries.GetAgentDispatchRootTaskByAcceptance(
		ctx,
		db.GetAgentDispatchRootTaskByAcceptanceParams{
			AgentID:        dispatchContext.AgentID,
			IdempotencyKey: acceptance.IdempotencyKey,
			EndpointID:     command.DispatchEndpointID,
			CallbackUrl:    command.CompletionCallback.URL,
			TargetIdentity: command.CompletionCallback.Target,
		},
	)
	if err == nil {
		response := newBufferedDispatchResponse()
		switch {
		case rootTask.ChatSessionID.Valid:
			writeJSON(response, http.StatusAccepted, AgentChatDispatchResponse{
				Continuation: AgentDispatchContinuation{
					Kind:          "chat",
					ChatSessionID: uuidToString(rootTask.ChatSessionID),
				},
				TaskID: uuidToString(rootTask.ID),
			})
		case rootTask.IssueID.Valid:
			issue, issueErr := h.Queries.GetIssueInWorkspace(
				ctx,
				db.GetIssueInWorkspaceParams{
					ID:          rootTask.IssueID,
					WorkspaceID: dispatchContext.WorkspaceID,
				},
			)
			if issueErr != nil {
				return nil, false, issueErr
			}
			recovered := AgentDispatchResponse{
				Continuation: AgentDispatchContinuation{
					Kind:    "issue",
					IssueID: uuidToString(issue.ID),
				},
				TaskID: uuidToString(rootTask.ID),
			}
			if rootTask.TriggerCommentID.Valid {
				recovered.CommentID = uuidToString(rootTask.TriggerCommentID)
			} else {
				recovered.IssueIdentifier = h.getIssuePrefix(ctx, dispatchContext.WorkspaceID) +
					"-" + formatIssueNumber(issue.Number)
			}
			writeJSON(response, http.StatusCreated, recovered)
		default:
			return nil, false, errors.New("recovered dispatch task has no supported surface")
		}
		return response, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, false, err
	}

	dispatchTaskID, ok := completionCallbackDispatchTaskID(command.CompletionCallback.URL)
	if !ok {
		return nil, false, errors.New("invalid completion callback")
	}
	completion, err := h.Queries.GetTaskCompletionByRequestID(
		ctx,
		"multica-terminal:sync:"+dispatchTaskID,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if completion.RootTaskID.Valid ||
		completion.TerminalTaskID.Valid ||
		completion.AgentID != dispatchContext.AgentID ||
		completion.CallbackUrl != command.CompletionCallback.URL ||
		completion.TargetIdentity != command.CompletionCallback.Target {
		return nil, false, nil
	}
	response := newBufferedDispatchResponse()
	response.WriteHeader(http.StatusAccepted)
	return response, true, nil
}

func completionCallbackDispatchTaskID(callbackURL string) (string, bool) {
	const prefix = "/api/v1/dispatch-tasks/"
	const suffix = "/execution-result"
	if !strings.HasPrefix(callbackURL, prefix) || !strings.HasSuffix(callbackURL, suffix) {
		return "", false
	}
	dispatchTaskID := strings.TrimSuffix(strings.TrimPrefix(callbackURL, prefix), suffix)
	return dispatchTaskID, dispatchTaskID != ""
}

func (h *Handler) releaseAgentDispatchAcceptance(
	ctx context.Context,
	acceptance db.AgentDispatchAcceptance,
) {
	_, _ = h.Queries.ReleaseAgentDispatchAcceptance(
		ctx,
		db.ReleaseAgentDispatchAcceptanceParams{
			ID:         acceptance.ID,
			LeaseToken: acceptance.LeaseToken,
		},
	)
}

func writeAgentDispatchAcceptanceReplay(
	w http.ResponseWriter,
	acceptance db.AgentDispatchAcceptance,
) {
	if acceptance.ResponseContentType.Valid {
		w.Header().Set("Content-Type", acceptance.ResponseContentType.String)
	}
	w.Header().Set("Content-Length", fmt.Sprint(len(acceptance.ResponseBody)))
	w.WriteHeader(int(acceptance.ResponseStatus.Int32))
	_, _ = w.Write(acceptance.ResponseBody)
}
