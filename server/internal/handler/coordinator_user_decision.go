package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/multica-ai/multica/server/internal/dwsclient"
	"github.com/multica-ai/multica/server/internal/service/inboundcoord"
	"github.com/multica-ai/multica/server/internal/service/userdecision"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type userDecisionScopeKey struct{}
type userDecisionJobKey struct{}

func decisionScope(ctx context.Context) *userdecision.Request {
	r, _ := ctx.Value(userDecisionScopeKey{}).(*userdecision.Request)
	return r
}

// The trusted worker supplies the job and lease; neither can come from the wire.
func (h *Handler) guardUnavailableUserDecision(w http.ResponseWriter, r *http.Request, c DispatchCommand, dc agentDispatchContext) bool {
	if c.Event.Domain != "channel" || c.Event.Type != "message.created" || c.TaskFinishedTaskID != "" || c.Source.Type != "digital_employee" {
		return false
	}
	if _, ok := inboundcoord.RestoredPlan(r.Context()); ok {
		return false
	}
	policy, err := h.Queries.GetAgentDingTalkResponsePolicy(r.Context(), dc.AgentID)
	if err != nil {
		writeError(w, 503, "could not verify coordinator decision policy")
		return true
	}
	if !policy.InboundCoordinator || !policy.InboundCoordinatorUserDecision {
		return false
	}
	reject := func(reason, text string) bool {
		d := rejectedUserDecision(c, reason, text)
		inboundcoord.RecordDecision(r.Context(), d)
		if !writeDispatchCoordinatorTerminal(w, r.Context(), h, c, dc, d) {
			writeError(w, 503, text)
		}
		return true
	}
	if h.UserDecisions == nil {
		return reject("user_decision_service_unavailable", "用户选择服务暂不可用，本次未执行。")
	}
	job, ok := r.Context().Value(userDecisionJobKey{}).(db.InboundCoordinatorJob)
	if !ok {
		return reject("user_decision_queue_unavailable", "本次请求尚未进入用户选择队列，未执行。")
	}
	uid, org := dispatchCoordinatorDWSIdentity(c)
	req := userdecision.Request{ID: uuidToString(job.ID), JobID: uuidToString(job.ID), JobLease: uuidToString(job.LeaseToken), AgentID: uuidToString(dc.AgentID), WorkspaceID: uuidToString(dc.WorkspaceID), Environment: h.UserDecisions.Store.Environment, ConversationID: dispatchConversationID(c), SenderUID: uid, SenderOrgID: org}
	session, err := h.UserDecisions.Transport.Open(r.Context(), req)
	if err != nil {
		return reject("user_decision_sender_unavailable", "暂时无法验证员工的发卡身份，本次未执行。")
	}
	defer session.Close()
	origin := dispatchOriginOpenMsgID(c)
	if origin == "" && len(c.Event.Data.Messages) > 0 {
		origin = c.Event.Data.Messages[0].OpenMsgID
	}
	req.CorpID, req.InitiatorID, err = session.Verify(r.Context(), req.ConversationID, origin)
	if err != nil {
		reason, explanation := userDecisionIdentityRejection(err)
		slog.Warn("coordinator user decision identity rejected", "event", "user_decision_identity_rejected", "agent_id", req.AgentID, "decision_id", req.ID, "reason", reason)
		return reject(reason, explanation)
	}
	if !singleDecisionAuthor(c) {
		return reject("user_decision_multiple_initiators", "这批消息来自不同发起人，无法合并选择，本次未执行。")
	}
	*r = *r.WithContext(context.WithValue(r.Context(), userDecisionScopeKey{}, &req))
	return false
}
func singleDecisionAuthor(c DispatchCommand) bool {
	expected := firstNonEmpty(c.Event.Data.Sender.UID, c.Event.Data.Sender.OpenDingTalkID, c.Event.Data.Sender.SenderOpenDingTalkID)
	if expected == "" {
		return false
	}
	for _, m := range c.Event.Data.Messages {
		if id := firstNonEmpty(m.SenderUID, m.SenderOpenDingTalkID, expected); id != expected {
			return false
		}
	}
	return true
}
func sameDecisionAuthor(a, b DispatchCommand) bool {
	first := firstNonEmpty(a.Event.Data.Sender.UID, a.Event.Data.Sender.OpenDingTalkID, a.Event.Data.Sender.SenderOpenDingTalkID)
	return first != "" && first == firstNonEmpty(b.Event.Data.Sender.UID, b.Event.Data.Sender.OpenDingTalkID, b.Event.Data.Sender.SenderOpenDingTalkID)
}
func (h *Handler) persistUserDecision(w http.ResponseWriter, r *http.Request, d inboundcoord.Decision) bool {
	if d.Action != inboundcoord.ActionAwaitUser {
		return false
	}
	scope := decisionScope(r.Context())
	if scope == nil || d.UserDecision == nil || h.UserDecisions == nil {
		writeError(w, 503, "user decision scope unavailable; no work dispatched")
		return true
	}
	d.UserDecision.DecisionID = scope.ID
	snapshot, err := json.Marshal(d.UserDecision)
	if err != nil {
		writeError(w, 500, "could not freeze decision")
		return true
	}
	scope.Snapshot = snapshot
	scope.Proposal = d.UserDecision.Proposal
	if _, err = h.UserDecisions.Store.Prepare(r.Context(), *scope); err != nil {
		writeError(w, 503, "could not persist decision; no work dispatched")
		return true
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "awaiting_user_decision", "decision_id": scope.ID})
	return true
}
func unavailableUserDecision(c DispatchCommand) inboundcoord.Decision {
	return rejectedUserDecision(c, "user_decision_service_unavailable", "用户选择服务暂不可用，本次未执行。")
}

func rejectedUserDecision(c DispatchCommand, reason, text string) inboundcoord.Decision {
	if c.ProactiveConversation && !dispatchMentionsEmployee(c) {
		return inboundcoord.Decision{Action: inboundcoord.ActionSilence, Reason: "user_decision_not_addressed"}
	}
	return inboundcoord.Decision{Action: inboundcoord.ActionReply, UserText: text, Reason: reason}
}

func decisionRequestID(ctx context.Context) string {
	if scope := decisionScope(ctx); scope != nil {
		return scope.ID
	}
	return ""
}

func userDecisionIdentityRejection(err error) (string, string) {
	var identityErr *dwsclient.DecisionIdentityError
	if errors.As(err, &identityErr) {
		switch identityErr.Code {
		case "user_decision_sender_profile_lookup_failed", "user_decision_sender_profile_invalid", "user_decision_initiator_lookup_failed":
			return identityErr.Code, "暂时无法核验会话或原消息发起人，本次未执行，请稍后重试。"
		}
	}
	return "user_decision_identity_verification_failed", "当前会话或发起人身份未通过校验，本次未执行。"
}
