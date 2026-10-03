package handler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/dwsclient"
	"github.com/multica-ai/multica/server/internal/integrations/agentmessagerouter"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dws"
	dwsevents "github.com/multica-ai/multica/server/pkg/dws/events"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// Native DWS inbound: the server acts as its own Agent Message Router for
// execution identities with native subscription on. One IM event becomes the
// DispatchCommand v2 a Router digital-employee delivery would carry and is
// accepted in-process by handleAgentDispatchV2, so acceptance, collect
// windows, the Coordinator job, tasks and task_finished all run unchanged.
// Its callbacks name a Multica-owned dispatch task (dwsn-…) bound to
// agentmessagerouter.NativeTargetIdentity; replies are always managed
// responses sent through the production DWS gateway as the identity.

// nativeDWSEnvironment is the DWS gateway of native subscriptions: their
// events come from production DWS whatever this deployment is, so their
// replies go back through it.
const nativeDWSEnvironment = "production"

// nativeDispatchContextKey marks the in-process native dispatch. Only it may
// use the native dispatch task namespace; a wire delivery may not.
type nativeDispatchContextKey struct{}

func withNativeDispatch(ctx context.Context) context.Context {
	return context.WithValue(ctx, nativeDispatchContextKey{}, true)
}

func isNativeDispatch(ctx context.Context) bool {
	native, _ := ctx.Value(nativeDispatchContextKey{}).(bool)
	return native
}

// dispatchCommandClaimsNativeNamespace reports whether any callback of a
// command names a native dispatch task, however malformed.
func dispatchCommandClaimsNativeNamespace(c DispatchCommand) bool {
	callbacks := append([]DispatchCompletionCallback{}, c.ExtraCompletionCallbacks...)
	if c.CompletionCallback != nil {
		callbacks = append(callbacks, *c.CompletionCallback)
	}
	const prefix = "/api/v1/dispatch-tasks/" + agentmessagerouter.NativeDispatchTaskPrefix
	for _, callback := range callbacks {
		for _, raw := range []string{callback.URL, callback.UpdateURL, callback.ResponseURL, callback.TelemetryURL} {
			if strings.HasPrefix(strings.TrimSpace(raw), prefix) {
				return true
			}
		}
	}
	return false
}

// completionTargetFor is the completion target of a callback: native
// dispatch tasks complete to the native target, everything else to the
// Router. The target is not persisted with a Coordinator job, so every
// restore derives it again from the callback URL.
func completionTargetFor(callbackURL, routerTarget string) string {
	if agentmessagerouter.IsNativeDispatchCallback(callbackURL) {
		return agentmessagerouter.NativeTargetIdentity()
	}
	return strings.TrimSpace(routerTarget)
}

// isNativeDispatchCommand reports whether a command came from a native
// subscription (its primary callback is a native dispatch task).
func isNativeDispatchCommand(c DispatchCommand) bool {
	return c.CompletionCallback != nil && agentmessagerouter.IsNativeDispatchCallback(c.CompletionCallback.URL)
}

// commandDWSEnvironment is the DWS environment a command's managed replies
// are pinned to; empty keeps the deployment's configured gateway.
func commandDWSEnvironment(c DispatchCommand) string {
	if isNativeDispatchCommand(c) {
		return nativeDWSEnvironment
	}
	return ""
}

func (h *Handler) notifyTaskExecutionUpdates() {
	agentmessagerouter.CompletionNotifiers{h.TaskCompletionWorker, h.NativeCompletionWorker}.NotifyTaskExecutionUpdate()
}

// DispatchEndpointEnsurer creates, or returns, an agent's dispatch endpoint.
// *agentmessagerouter.DispatchEndpointService satisfies it.
type DispatchEndpointEnsurer interface {
	Ensure(ctx context.Context, workspaceID, agentID, actorUserID pgtype.UUID) (agentmessagerouter.DispatchEndpoint, error)
}

// nativeEligibilityStore reads what decides whether an agent's native
// dispatches can carry a managed response policy; h.Queries in production.
type nativeEligibilityStore interface {
	GetAgentDingTalkResponsePolicy(context.Context, pgtype.UUID) (db.GetAgentDingTalkResponsePolicyRow, error)
	GetAgentRuntimeForWorkspace(context.Context, db.GetAgentRuntimeForWorkspaceParams) (db.AgentRuntime, error)
}

// nativeManagedResponsePolicy is the managed response policy an agent's
// native dispatches carry, or the reason the agent has none. There is no
// Router to own a legacy reply, so native dispatches exist only in managed
// mode: the same conditions the Router policy sync uses for
// multica_coordinator (Coordinator and DingTalk response on, a versioned
// policy, a runtime with the managed DWS wrapper).
func nativeManagedResponsePolicy(ctx context.Context, store nativeEligibilityStore, agent db.Agent) (protocol.DingTalkResponsePolicy, string, error) {
	row, err := store.GetAgentDingTalkResponsePolicy(ctx, agent.ID)
	if err != nil {
		return protocol.DingTalkResponsePolicy{}, "", err
	}
	switch {
	case !row.InboundCoordinator:
		return protocol.DingTalkResponsePolicy{}, "inbound_coordinator_off", nil
	case !row.DingtalkResponseEnabled:
		return protocol.DingTalkResponsePolicy{}, "dingtalk_response_off", nil
	case row.DingtalkResponsePolicyRevision < 1:
		return protocol.DingTalkResponsePolicy{}, "response_policy_unversioned", nil
	case !agent.RuntimeID.Valid:
		return protocol.DingTalkResponsePolicy{}, "runtime_missing", nil
	}
	runtime, err := store.GetAgentRuntimeForWorkspace(ctx, db.GetAgentRuntimeForWorkspaceParams{ID: agent.RuntimeID, WorkspaceID: agent.WorkspaceID})
	if errors.Is(err, pgx.ErrNoRows) {
		return protocol.DingTalkResponsePolicy{}, "runtime_missing", nil
	}
	if err != nil {
		return protocol.DingTalkResponsePolicy{}, "", err
	}
	if !service.RuntimeSupportsDWSMessagePolicy(runtime) {
		return protocol.DingTalkResponsePolicy{}, "runtime_lacks_dws_message_policy", nil
	}
	return protocol.DingTalkResponsePolicy{
		Version: protocol.DingTalkResponsePolicyVersion, Mode: protocol.DingTalkResponseModeCoordinator,
		Revision: row.DingtalkResponsePolicyRevision, ShowAITag: row.DingtalkShowAiTag,
	}, "", nil
}

// nativeDispatchStore is what accepting a native event reads; h.Queries in
// production.
type nativeDispatchStore interface {
	nativeEligibilityStore
	nativeOwnershipStore
	GetAgent(context.Context, pgtype.UUID) (db.Agent, error)
	GetAgentDispatchEndpointForDelivery(context.Context, db.GetAgentDispatchEndpointForDeliveryParams) (db.AgentDispatchEndpoint, error)
	IsAgentOwnDingTalkMessage(context.Context, db.IsAgentOwnDingTalkMessageParams) (bool, error)
	IsRecentOwnReplyEcho(context.Context, db.IsRecentOwnReplyEchoParams) (bool, error)
	SetDWSNativeSelfOpenDingTalkID(context.Context, db.SetDWSNativeSelfOpenDingTalkIDParams) error
}

func (h *Handler) nativeDispatch() nativeDispatchStore {
	if h.dwsNativeDispatch != nil {
		return h.dwsNativeDispatch
	}
	if h.Queries != nil {
		return h.Queries
	}
	return nil
}

// nativeSkip is a trusted reason to acknowledge a native event without
// dispatching it; redelivery would not change the outcome.
type nativeSkip string

func (s nativeSkip) Error() string { return "native event skipped: " + string(s) }

// nativeMessageInput is one native IM message and the facts the server
// resolved for it.
type nativeMessageInput struct {
	AgentID  string
	UID      string
	OrgID    string
	EventKey string
	Message  *dwsevents.MessageEvent
	Policy   protocol.DingTalkResponsePolicy
	// QuotedOwn: the quoted message is one this agent sent (its provider
	// receipt named it), so its author is the employee itself.
	QuotedOwn bool
	// ConversationTitle is a group's title as the identity sees it
	// (nativeConversationTitle); the event itself carries none.
	ConversationTitle string
	// SenderStaffID is the sender's staffId as the identity's org knows them
	// (nativeSenderStaffID), "" when it could not be proved.
	SenderStaffID string
}

// buildNativeDispatchCommand turns one native IM message into the Dispatch
// Command v2 a Router digital-employee delivery of it would be. It is a pure
// function of its input, so a redelivered event yields the same command and
// the acceptance fingerprint replays instead of conflicting.
//
// The event carries no sender uid, staffId, mention list, conversation
// title or attachments. The sender is identified by openDingTalkId (no uid
// is invented); that id is relative to the receiving account and the same
// in its 1:1 chats and groups. The sender's staffId is the one the server
// proved for that id (in.SenderStaffID), so the trigger person has the key
// a Router delivery gives them (contextcap.TriggerPersonKey); without one
// the openDingTalkId keys them. A group's title is the one the server read
// for it (in.ConversationTitle). A group event
// exists only because this account was @-mentioned
// (user_im_message_receive_at), which is recorded as a trusted mention of the
// receiving uid; other mentions in the same line stay unknown.
func buildNativeDispatchCommand(in nativeMessageInput) (DispatchCommand, error) {
	m := in.Message
	if m == nil {
		return DispatchCommand{}, nativeSkip("not_a_message")
	}
	conversationID := strings.TrimSpace(m.ConversationID)
	messageID := strings.TrimSpace(m.MessageID)
	if conversationID == "" || messageID == "" {
		return DispatchCommand{}, nativeSkip("missing_message_reference")
	}
	if strings.TrimSpace(m.Content) == "" {
		// Media and other contentless messages: the event carries no
		// attachment, so there is nothing trustworthy to hand over.
		return DispatchCommand{}, nativeSkip("empty_content")
	}
	senderOpenID := strings.TrimSpace(m.SenderOpenDingTalkID)
	conversationType := "single"
	conversationTitle := ""
	mentions := []DispatchMention{}
	switch in.EventKey {
	case dws.EventIMAt:
		conversationType = "group"
		conversationTitle = strings.TrimSpace(in.ConversationTitle)
		mentions = []DispatchMention{{UID: in.UID}}
	case dws.EventIMAllSingleChats:
		if senderOpenID == "" {
			return DispatchCommand{}, nativeSkip("missing_sender")
		}
	case dws.EventIMAllGroups:
		// A proactive wake the Host gate admitted for an unaddressed group
		// line (employee_proactive_wake.go): a known empty mention list, so
		// the line never counts as addressing this account.
		if senderOpenID == "" {
			return DispatchCommand{}, nativeSkip("missing_sender")
		}
		conversationType = "group"
		conversationTitle = strings.TrimSpace(in.ConversationTitle)
	default:
		return DispatchCommand{}, nativeSkip("unsupported_event_key")
	}
	senderName := nativeDisplayName(m.Sender)
	senderStaffID := ""
	if senderOpenID != "" {
		senderStaffID = strings.TrimSpace(in.SenderStaffID)
	}
	occurredAt := nativeMessageSentAt(m)
	message := DispatchMessage{
		Mentions:             mentions,
		OpenMsgID:            messageID,
		OccurredAt:           occurredAt,
		Text:                 m.Content,
		SenderDisplayName:    senderName,
		SenderOpenDingTalkID: senderOpenID,
		SenderStaffID:        senderStaffID,
	}
	if quoted := m.QuotedMessage; quoted != nil &&
		(strings.TrimSpace(quoted.MessageID) != "" || strings.TrimSpace(quoted.Content) != "") {
		ref := &DispatchReferencedMessage{OpenMsgID: strings.TrimSpace(quoted.MessageID), Text: quoted.Content}
		// The quoted author is stated only when proven: the employee's own
		// sent message, or the current sender quoting themselves. Anything
		// else stays unknown rather than being called another person.
		switch {
		case in.QuotedOwn:
			ref.SenderUID = in.UID
		case senderOpenID != "" && strings.TrimSpace(quoted.SenderOpenDingTalkID) == senderOpenID:
			ref.SenderUID = senderOpenID
		}
		message.ReferencedMessage = ref
	}
	base := "/api/v1/dispatch-tasks/" + agentmessagerouter.NativeDispatchTaskID(in.AgentID, in.OrgID, conversationID, messageID)
	policy := in.Policy
	return DispatchCommand{
		SchemaVersion: "2.0",
		AgentID:       in.AgentID,
		Source:        DispatchSource{Platform: "dingtalk", Type: "digital_employee"},
		Event: DispatchEvent{Domain: "channel", Type: "message.created", Data: DispatchEventData{
			Conversation: DispatchConversation{OpenConversationID: conversationID, Type: conversationType, Title: conversationTitle},
			Sender:       DispatchSender{DisplayName: senderName, OpenDingTalkID: senderOpenID, SenderOpenDingTalkID: senderOpenID, StaffID: senderStaffID},
			Messages:     []DispatchMessage{message},
		}},
		Surface:          DispatchSurface{Type: protocol.DispatchSurfaceTypeAuto},
		Outbound:         DispatchOutbound{Mode: protocol.DispatchOutboundModeDWS, ReplyTo: protocol.DispatchReplyToLatestMessage},
		ResponsePolicy:   &policy,
		ExternalIdentity: AgentDispatchExternalIdentity{DWS: &AgentDispatchDWSIdentity{UID: in.UID, OrgID: in.OrgID}},
		CompletionCallback: &DispatchCompletionCallback{
			URL:         base + "/execution-result",
			UpdateURL:   base + "/execution-update",
			ResponseURL: base + "/response-receipt",
		},
	}, nil
}

// nativeDisplayName drops the literal "null" DingTalk sends for an omitted
// sender name.
func nativeDisplayName(raw string) string {
	name := strings.TrimSpace(raw)
	if strings.EqualFold(name, "null") {
		return ""
	}
	return name
}

// nativeDispatchIdempotencyKey keys acceptance by the DingTalk message, not
// the event id, which can fall back to a per-delivery frame id.
func nativeDispatchIdempotencyKey(orgID, conversationID, messageID string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{orgID, conversationID, messageID}, "\x00")))
	return "dws-native:v1:" + hex.EncodeToString(sum[:])
}

// acceptNativeMessage processes a native event iff this agent's native
// subscription owns the account (nativeAccountOwner), drops the account's
// own output, then hands the message to the Router pipeline in-process. A
// nil error acknowledges the event; an error leaves it unacknowledged so it
// is delivered again.
func (h *Handler) acceptNativeMessage(ctx context.Context, id dwsclient.Identity, ev dwsevents.Event, m *dwsevents.MessageEvent) error {
	skip := func(reason string) error {
		slog.Info("DWS native event skipped", "event", "dws_native_event_skipped",
			"agent_id", id.AgentID, "event_key", ev.Key, "event_id", ev.ID, "reason", reason)
		return nil
	}
	store := h.nativeDispatch()
	if store == nil {
		return skip("store_unavailable")
	}
	agentID, err := util.ParseUUID(id.AgentID)
	if err != nil {
		return skip("invalid_agent")
	}
	// Ownership is the single rule both paths apply: the account's native row
	// exists, its agent is not archived and still holds that identity.
	owner, owned, err := nativeAccountOwner(ctx, store, id.UID, id.OrgID)
	if err != nil {
		return fmt.Errorf("resolve native account owner: %w", err)
	}
	if !owned {
		return skip("not_owned")
	}
	if owner.AgentID != agentID {
		// The stream learns a new owner only at its next sweep: the account
		// moved to another agent, which handles the message now.
		slog.Info("DWS native event follows the account's current owner", "event", "dws_native_event_owner_moved",
			"stream_agent_id", id.AgentID, "owner_agent_id", util.UUIDToString(owner.AgentID), "event_id", ev.ID)
		agentID = owner.AgentID
		id.AgentID = util.UUIDToString(owner.AgentID)
	}
	agent, err := store.GetAgent(ctx, agentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return skip("agent_missing")
	}
	if err != nil {
		return fmt.Errorf("load native agent: %w", err)
	}
	conversationID := strings.TrimSpace(m.ConversationID)
	senderOpenID := strings.TrimSpace(m.SenderOpenDingTalkID)
	// DWS delivers the account's own messages too; the uid-based self checks
	// cannot see an openDingTalkId sender, so three guards stand in for them.
	isOwn := func(messageID string) (bool, error) {
		return store.IsAgentOwnDingTalkMessage(ctx, db.IsAgentOwnDingTalkMessageParams{
			MessageID: strings.TrimSpace(messageID), WorkspaceID: agent.WorkspaceID, AgentID: agent.ID,
		})
	}
	own, err := isOwn(m.MessageID)
	if err != nil {
		return fmt.Errorf("check own message: %w", err)
	}
	if own {
		h.learnNativeSelfOpenID(ctx, store, owner, id, senderOpenID)
		return skip("own_message")
	}
	if owner.SelfOpenDingtalkID != "" && senderOpenID == owner.SelfOpenDingtalkID {
		return skip("self_sender")
	}
	// Echo window, only while the account's own openDingTalkId is not yet
	// learned (afterwards self_sender is exact) and only in single chats: a
	// group event exists only when someone @-mentions the account, which its
	// own replies never do. A person repeating the reply in a group is never
	// dropped.
	if content := strings.TrimSpace(m.Content); content != "" && conversationID != "" &&
		owner.SelfOpenDingtalkID == "" && ev.Key == dws.EventIMAllSingleChats {
		echo, err := store.IsRecentOwnReplyEcho(ctx, db.IsRecentOwnReplyEchoParams{
			WorkspaceID: agent.WorkspaceID, AgentID: agent.ID, ConversationID: conversationID, Content: content,
		})
		if err != nil {
			return fmt.Errorf("check own reply echo: %w", err)
		}
		if echo {
			return skip("echo_of_own_reply")
		}
	}
	// A backlog DWS replays after a reconnect or rebind is acknowledged
	// unanswered: the asker has moved on, and each old message would draw
	// its own replies.
	sentAt := nativeMessageSentAt(m)
	if reason := nativeStaleReason(sentAt, owner.EnabledAt.Time, nativeClock()); reason != "" {
		slog.Warn("DWS native event skipped: message too old to answer", "event", "dws_native_event_skipped",
			"agent_id", id.AgentID, "event_key", ev.Key, "event_id", ev.ID, "reason", reason,
			"sent_at", time.UnixMilli(sentAt).UTC().Format(time.RFC3339),
			"subscribed_at", owner.EnabledAt.Time.UTC().Format(time.RFC3339))
		return nil
	}
	policy, reason, err := nativeManagedResponsePolicy(ctx, store, agent)
	if err != nil {
		return fmt.Errorf("load native response policy: %w", err)
	}
	if reason != "" {
		slog.Warn("DWS native event not dispatched: managed response unavailable", "event", "dws_native_event_skipped",
			"agent_id", id.AgentID, "event_key", ev.Key, "event_id", ev.ID, "reason", "response_not_managed", "detail", reason)
		return nil
	}
	quotedOwn := false
	if quoted := m.QuotedMessage; quoted != nil && strings.TrimSpace(quoted.MessageID) != "" {
		quotedOwn, err = isOwn(quoted.MessageID)
		if err != nil {
			return fmt.Errorf("check quoted message: %w", err)
		}
		quotedSender := strings.TrimSpace(quoted.SenderOpenDingTalkID)
		if quotedOwn {
			// A quote of a proven own message names the account's openDingTalkId.
			h.learnNativeSelfOpenID(ctx, store, owner, id, quotedSender)
		} else if owner.SelfOpenDingtalkID != "" && quotedSender == owner.SelfOpenDingtalkID {
			quotedOwn = true
		}
	}
	// The title and the sender's staffId are looked up only for a message
	// buildNativeDispatchCommand will dispatch.
	conversationTitle, groupConversationID, senderStaffID := "", "", ""
	if strings.TrimSpace(m.Content) != "" && conversationID != "" && strings.TrimSpace(m.MessageID) != "" {
		if ev.Key == dws.EventIMAt {
			groupConversationID = conversationID
			conversationTitle = h.nativeConversationTitle(ctx, id, conversationID)
		}
		senderStaffID = h.nativeSenderStaffID(ctx, id, m, groupConversationID)
	}
	command, err := buildNativeDispatchCommand(nativeMessageInput{
		AgentID: util.UUIDToString(agent.ID), UID: id.UID, OrgID: id.OrgID,
		EventKey: ev.Key, Message: m, Policy: policy, QuotedOwn: quotedOwn,
		ConversationTitle: conversationTitle,
		SenderStaffID:     senderStaffID,
	})
	var skipped nativeSkip
	if errors.As(err, &skipped) {
		return skip(string(skipped))
	}
	if err != nil {
		return err
	}
	// Per sender: a loop repeats one sender, while a person typing in bursts
	// or several people @-ing the account in a busy group do not trip it.
	if !nativeDispatchLoops.admit(id.AgentID+"\x00"+conversationID+"\x00"+senderOpenID, strings.TrimSpace(m.MessageID), time.Now()) {
		slog.Warn("DWS native dispatches look like a reply loop", "event", "dws_native_loop_suspected",
			"agent_id", id.AgentID, "event_key", ev.Key, "event_id", ev.ID,
			"limit", nativeLoopLimit, "window_s", int(nativeLoopWindow.Seconds()))
		return nil
	}
	endpoint, err := store.GetAgentDispatchEndpointForDelivery(ctx, db.GetAgentDispatchEndpointForDeliveryParams{AgentID: agent.ID, WorkspaceID: agent.WorkspaceID})
	if errors.Is(err, pgx.ErrNoRows) {
		slog.Warn("DWS native event not dispatched: no dispatch endpoint", "event", "dws_native_event_skipped",
			"agent_id", id.AgentID, "event_key", ev.Key, "event_id", ev.ID, "reason", "dispatch_endpoint_missing")
		return nil
	}
	if err != nil {
		return fmt.Errorf("load dispatch endpoint: %w", err)
	}
	dispatchContext := agentDispatchContext{
		EndpointID:          endpoint.EndpointID,
		EndpointNamespaceID: endpoint.ID,
		UserID:              endpoint.ActorUserID,
		WorkspaceID:         agent.WorkspaceID,
		AgentID:             agent.ID,
	}
	key := nativeDispatchIdempotencyKey(id.OrgID, command.Event.Data.Conversation.OpenConversationID, command.Event.Data.Messages[0].OpenMsgID)
	status, body, err := h.submitNativeDispatch(withNativeEvent(ctx, ev), command, dispatchContext, key)
	if err != nil {
		return err
	}
	switch {
	case status >= http.StatusOK && status < http.StatusMultipleChoices:
		slog.Info("DWS native event dispatched", "event", "dws_native_event_dispatched",
			"agent_id", id.AgentID, "event_key", ev.Key, "event_id", ev.ID, "status", status,
			"conversation_type", command.Event.Data.Conversation.Type)
		return nil
	case status == http.StatusConflict && strings.Contains(body, errAgentDispatchAcceptancePending.Error()),
		status >= http.StatusInternalServerError:
		// Another delivery is mid-acceptance or the pipeline is unavailable:
		// leave the event unacknowledged so it comes again and replays.
		return fmt.Errorf("native dispatch deferred with HTTP %d", status)
	default:
		slog.Warn("DWS native event rejected by the dispatch pipeline", "event", "dws_native_event_rejected",
			"agent_id", id.AgentID, "event_key", ev.Key, "event_id", ev.ID, "status", status, "body", clipRunes(body, 300))
		return nil
	}
}

// learnNativeSelfOpenID records the account's own openDingTalkId, proven by
// one of its own messages, the first time it is seen. Failure only costs the
// next guard a lookup, so it is logged, not returned.
func (h *Handler) learnNativeSelfOpenID(ctx context.Context, store nativeDispatchStore, owner db.GetDWSNativeAccountOwnerRow, id dwsclient.Identity, openID string) {
	if owner.SelfOpenDingtalkID != "" || openID == "" || strings.EqualFold(openID, "null") {
		return
	}
	if err := store.SetDWSNativeSelfOpenDingTalkID(ctx, db.SetDWSNativeSelfOpenDingTalkIDParams{
		SelfOpenDingtalkID: openID, AgentID: owner.AgentID, OrgID: id.OrgID, DwsUid: id.UID,
	}); err != nil {
		slog.Warn("DWS native self openDingTalkId not recorded", "event", "dws_native_self_open_id_failed",
			"agent_id", id.AgentID, "error", err)
		return
	}
	slog.Info("DWS native self openDingTalkId learned", "event", "dws_native_self_open_id_learned", "agent_id", id.AgentID)
}

// nativeSubmitTimeout bounds one in-process acceptance. The submission runs
// detached from the event stream's context: a stream handover cancelling it
// mid-acceptance would leave the acceptance pending until its lease expires.
const nativeSubmitTimeout = 60 * time.Second

// submitNativeDispatch runs the Router webhook pipeline in-process, marked
// as native so it may use the native dispatch task namespace.
func (h *Handler) submitNativeDispatch(ctx context.Context, command DispatchCommand, dispatchContext agentDispatchContext, idempotencyKey string) (int, string, error) {
	raw, err := json.Marshal(AgentDispatchV2Request{
		SchemaVersion:      command.SchemaVersion,
		AgentID:            command.AgentID,
		Continuation:       command.Continuation,
		Source:             command.Source,
		Event:              command.Event,
		Surface:            command.Surface,
		Outbound:           command.Outbound,
		Control:            command.Control,
		ContextPrompt:      command.ContextPrompt,
		ResponsePolicy:     command.ResponsePolicy,
		ExternalIdentity:   command.ExternalIdentity,
		CompletionCallback: command.CompletionCallback,
	})
	if err != nil {
		return 0, "", fmt.Errorf("encode native dispatch: %w", err)
	}
	submitCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), nativeSubmitTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(withNativeDispatch(submitCtx), http.MethodPost, "/internal/dws-native-dispatch", nil)
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("Idempotency-Key", idempotencyKey)
	response := newBufferedDispatchResponse()
	h.handleAgentDispatchV2(response, req, raw, dispatchContext)
	return response.Status(), response.body.String(), nil
}

// nativeSubscriptionPreconditions checks what enabling native subscription
// needs beyond a bound identity and no message route: managed responses, an
// account no other agent streams, and a dispatch endpoint. It writes the
// error response and returns false when one is missing.
func (h *Handler) nativeSubscriptionPreconditions(
	w http.ResponseWriter,
	r *http.Request,
	store dwsNativeSubscriptionStore,
	agent db.Agent,
	identity db.AgentDingtalkIdentity,
	actorID pgtype.UUID,
) bool {
	ctx := r.Context()
	if !h.nativeSourceRunning() {
		writeDingTalkAccountBindingAPIError(w, http.StatusConflict, "native_subscription_unavailable",
			"the native subscription event source is not running on this deployment")
		return false
	}
	_, reason, err := nativeManagedResponsePolicy(ctx, store, agent)
	if err != nil {
		writeDingTalkAccountBindingAPIError(w, http.StatusInternalServerError, "native_subscription_failed", "failed to load the response policy")
		return false
	}
	if reason != "" {
		writeDingTalkAccountBindingAPIError(w, http.StatusConflict, "native_subscription_requires_managed_response",
			"native subscription needs the inbound Coordinator, DingTalk response and a runtime with the managed DWS wrapper ("+reason+")")
		return false
	}
	rows, err := store.ListActiveDWSNativeSubscriptions(ctx)
	if err != nil {
		writeDingTalkAccountBindingAPIError(w, http.StatusInternalServerError, "native_subscription_failed", "failed to load native subscriptions")
		return false
	}
	for _, row := range rows {
		// One event stream per DingTalk account: a second agent on the same
		// account would never receive its events.
		if row.AgentID != agent.ID && row.DwsUid == identity.DwsUid && row.OrgID == identity.OrgID {
			writeDingTalkAccountBindingAPIError(w, http.StatusConflict, "native_subscription_account_in_use",
				"another agent already receives this DingTalk account's messages through native subscription")
			return false
		}
	}
	if h.DispatchEndpoints == nil {
		writeDingTalkAccountBindingAPIError(w, http.StatusServiceUnavailable, "native_subscription_unavailable", "agent dispatch endpoints are not configured")
		return false
	}
	if _, err := h.DispatchEndpoints.Ensure(ctx, agent.WorkspaceID, agent.ID, actorID); err != nil {
		slog.Warn("native subscription dispatch endpoint unavailable", "event", "dws_native_subscription_endpoint_failed",
			"agent_id", util.UUIDToString(agent.ID), "error", err)
		writeDingTalkAccountBindingAPIError(w, http.StatusServiceUnavailable, "native_subscription_unavailable", "failed to prepare the agent dispatch endpoint")
		return false
	}
	return true
}
