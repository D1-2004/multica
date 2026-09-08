package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/service/dingtalkresponse"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func (h *Handler) RecordDingTalkSendReceipt(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-Actor-Source") != "task_token" {
		writeError(w, http.StatusForbidden, "DingTalk send receipts require a task token")
		return
	}
	taskID := chi.URLParam(r, "taskID")
	if taskID != r.Header.Get("X-Task-ID") {
		writeError(w, http.StatusForbidden, "DingTalk send receipt task does not match token")
		return
	}
	id, ok := parseUUIDOrBadRequest(w, taskID, "taskID")
	if !ok {
		return
	}
	task, err := h.Queries.GetAgentTask(r.Context(), id)
	if err != nil || uuidToString(task.AgentID) != r.Header.Get("X-Agent-ID") {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}
	agent, err := h.Queries.GetAgent(r.Context(), task.AgentID)
	if err != nil || uuidToString(agent.WorkspaceID) != r.Header.Get("X-Workspace-ID") {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}
	stored, present := parsePersistedDispatchContext(task.Context)
	if !present || !managedDingTalkResponse(DispatchCommand{
		ResponsePolicy: stored.ResponsePolicy, Source: stored.Source, Outbound: stored.Outbound,
		Event: DispatchEvent{Domain: stored.Domain, Type: stored.Type}, Control: stored.Control,
	}) {
		writeError(w, http.StatusForbidden, "task does not have a managed DingTalk response policy")
		return
	}
	if h.DingTalkResponses == nil {
		writeError(w, http.StatusServiceUnavailable, "DingTalk response service is unavailable")
		return
	}
	var receipt protocol.DingTalkSendReceipt
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32*1024))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&receipt) != nil || !validDingTalkSendReceipt(receipt) {
		writeError(w, http.StatusBadRequest, "invalid DingTalk send receipt")
		return
	}
	in := dingtalkresponse.ActionInput{
		WorkspaceID: uuidToString(agent.WorkspaceID), AgentID: uuidToString(task.AgentID),
		TaskID: taskID, IssueID: uuidToString(task.IssueID),
		ConversationID:       stored.EventData.Conversation.OpenConversationID,
		SenderOpenDingTalkID: firstNonEmpty(stored.EventData.Sender.OpenDingTalkID, stored.EventData.Sender.SenderOpenDingTalkID),
		IsGroup:              strings.EqualFold(stored.EventData.Conversation.Type, "group"),
	}
	if stored.ExternalIdentity != nil && stored.ExternalIdentity.DWS != nil {
		in.DWSUID, in.DWSOrgID = stored.ExternalIdentity.DWS.UID, stored.ExternalIdentity.DWS.OrgID
	}
	if in.DWSUID == "" || in.DWSOrgID == "" {
		identity, err := h.Queries.GetAgentDingTalkIdentity(r.Context(), db.GetAgentDingTalkIdentityParams{WorkspaceID: agent.WorkspaceID, AgentID: task.AgentID})
		if err != nil {
			writeError(w, http.StatusConflict, "task DingTalk identity is unavailable")
			return
		}
		in.DWSUID, in.DWSOrgID = identity.DwsUid, identity.OrgID
	}
	if err := h.DingTalkResponses.RecordSandboxReceipt(r.Context(), in, receipt); err != nil {
		slog.Warn("DingTalk send receipt persistence failed", "task_id", taskID, "error", err)
		writeError(w, http.StatusConflict, "DingTalk send receipt could not be persisted")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"accepted": true})
}

func validDingTalkSendReceipt(r protocol.DingTalkSendReceipt) bool {
	if r.ClientActionID == "" || len(r.ClientActionID) > 128 || len(r.PayloadHash) != 64 {
		return false
	}
	for _, c := range r.PayloadHash {
		if !(c >= '0' && c <= '9') && !(c >= 'a' && c <= 'f') {
			return false
		}
	}
	switch r.State {
	case "pending", "accepted", "delivered", "failed", "unknown":
		return true
	default:
		return false
	}
}
