package handler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/multica-ai/multica/server/internal/dwsclient"
	"github.com/multica-ai/multica/server/internal/employeeentry"
	"github.com/multica-ai/multica/server/internal/employeeloopconfig"
	"github.com/multica-ai/multica/server/internal/eventrouter"
	"github.com/multica-ai/multica/server/internal/scene"
	"github.com/multica-ai/multica/server/internal/service/inboundcoord"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dws"
	dwsevents "github.com/multica-ai/multica/server/pkg/dws/events"
)

// Native group observation: user_im_message_receive_group_all delivers every
// group message the employee account can see. Each human line of a group the
// agent already has a scene for is admitted as an eventrouter Observation
// (never a user message) and stored in the group transcript in the receipt's
// transaction. An observation never registers a scene, never wakes the Loop
// and calls no model; an unaddressed question may only become a proactive
// wake candidate that the Host gate decides later.

const (
	employeeObservationSource  = "dws-native-group/"
	employeeObservationSchema  = "dws.native.event/1"
	employeeObservationVersion = "employee-memory-observe/1"
	// employeeProactiveWait is how long an unaddressed question waits for a
	// colleague's answer before the Host gate decides.
	employeeProactiveWait = 75 * time.Second
	// employeeProactiveFreshness: an older question (a replayed backlog) is
	// stored but never becomes a candidate.
	employeeProactiveFreshness = 5 * time.Minute
)

// HandleDWSNativeGroupObservation is the all-group-messages consumer. Like
// HandleDWSNativeEvent, an undecodable or skipped event is acknowledged and
// a storage failure is returned so the event is delivered again.
func (h *Handler) HandleDWSNativeGroupObservation(ctx context.Context, id dwsclient.Identity, line []byte) error {
	ev, message, err := decodeNativeEvent(line)
	if err != nil {
		slog.Warn("DWS native observation not decodable", "event", "dws_native_observation_undecodable",
			"agent_id", id.AgentID, "event_key", ev.Key, "event_id", ev.ID, "error", err)
		return nil
	}
	reason, err := h.observeNativeGroupMessage(ctx, id, ev, message, nativeClock())
	if err != nil {
		return err
	}
	slog.Info("DWS native group message observed", "event", "dws_native_observation", "agent_id", id.AgentID,
		"event_id", ev.ID, "outcome", reason)
	return nil
}

// observeNativeGroupMessage returns the outcome ("stored", "duplicate" or a
// skip reason) or a retryable error.
func (h *Handler) observeNativeGroupMessage(ctx context.Context, id dwsclient.Identity, ev dwsevents.Event, m *dwsevents.MessageEvent, now time.Time) (string, error) {
	if ev.Key != dws.EventIMAllGroups || m == nil {
		return "unsupported_event_key", nil
	}
	if !h.employeeMemoryObserveReady(ctx) {
		return "replicas_not_ready", nil
	}
	store := h.nativeDispatch()
	database, ok := employeeEntryDB(h)
	if store == nil || !ok || h.Queries == nil || h.TxStarter == nil {
		return "store_unavailable", nil
	}
	conversationID := strings.TrimSpace(m.ConversationID)
	messageID := strings.TrimSpace(m.MessageID)
	content := strings.TrimSpace(m.Content)
	senderOpenID := strings.TrimSpace(m.SenderOpenDingTalkID)
	switch {
	case conversationID == "" || messageID == "":
		return "missing_message_reference", nil
	case content == "":
		return "empty_content", nil
	case senderOpenID == "" || strings.EqualFold(senderOpenID, "null"):
		// System notices carry no sender.
		return "no_sender", nil
	case employeePlaceholderText.MatchString(content) || employeeCardSummary(content):
		// Cards (digital employee intros, robot cards) and media summaries.
		return "card_or_media", nil
	}
	owner, owned, err := nativeAccountOwner(ctx, store, id.UID, id.OrgID)
	if err != nil {
		return "", fmt.Errorf("resolve native account owner: %w", err)
	}
	if !owned {
		return "not_owned", nil
	}
	agent, err := store.GetAgent(ctx, owner.AgentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "agent_missing", nil
	}
	if err != nil {
		return "", fmt.Errorf("load native agent: %w", err)
	}
	if owner.SelfOpenDingtalkID != "" && senderOpenID == owner.SelfOpenDingtalkID {
		return "self_sender", nil
	}
	own, err := store.IsAgentOwnDingTalkMessage(ctx, db.IsAgentOwnDingTalkMessageParams{MessageID: messageID, WorkspaceID: agent.WorkspaceID, AgentID: agent.ID})
	if err != nil {
		return "", fmt.Errorf("check own message: %w", err)
	}
	if own {
		h.learnNativeSelfOpenID(ctx, store, owner, id, senderOpenID)
		return "own_message", nil
	}
	config, err := employeeloopconfig.Load(ctx, database, agent.WorkspaceID, agent.ID)
	if err != nil {
		return "", fmt.Errorf("load work owner configuration: %w", err)
	}
	if !config.Enabled || config.Mode != employeeloopconfig.Employee {
		return "not_employee_mode", nil
	}
	sceneOwner := scene.Owner{WorkspaceID: agent.WorkspaceID, AgentID: agent.ID}
	org, err := agentTenantOrg(ctx, h.Queries, sceneOwner, id.OrgID)
	if reason := eventrouter.UnmappedReason(err); reason != "" {
		return reason, nil
	}
	if err != nil {
		return "", fmt.Errorf("resolve tenant: %w", err)
	}
	locator := scene.DingTalkConversation(org, scene.KindGroup, conversationID)
	registered, err := scene.Lookup(ctx, h.Queries, sceneOwner, locator)
	switch {
	case errors.Is(err, scene.ErrNotFound):
		// Never mint a scene from an observation: a group nobody addressed
		// the employee in stays unknown, with no receipt.
		return "no_scene", nil
	case eventrouter.UnmappedReason(err) != "":
		return eventrouter.UnmappedReason(err), nil
	case err != nil:
		return "", fmt.Errorf("look up scene: %w", err)
	}
	endpoint, err := store.GetAgentDispatchEndpointForDelivery(ctx, db.GetAgentDispatchEndpointForDeliveryParams{AgentID: agent.ID, WorkspaceID: agent.WorkspaceID})
	if errors.Is(err, pgx.ErrNoRows) {
		return "dispatch_endpoint_missing", nil
	}
	if err != nil {
		return "", fmt.Errorf("load dispatch endpoint: %w", err)
	}
	payload, err := json.Marshal(ev)
	if err != nil {
		return "", err
	}
	sentAt := time.UnixMilli(nativeMessageSentAt(m)).UTC()
	if nativeMessageSentAt(m) <= 0 {
		sentAt = now.UTC()
	}
	eventID := strings.TrimSpace(ev.ID)
	if eventID == "" {
		sum := sha256.Sum256([]byte(strings.Join([]string{org, conversationID, messageID}, "\x00")))
		eventID = "message:" + hex.EncodeToString(sum[:])
	}
	event := eventrouter.Event{Version: eventrouter.Version, ID: eventID, Source: employeeObservationSource + uuidToString(endpoint.ID),
		Type: ev.Key, Category: eventrouter.Observation, OccurredAt: sentAt, PayloadSchema: employeeObservationSchema, Payload: payload}
	row := employeeentry.SceneMessageRow{SceneMessageInput: employeeentry.SceneMessageInput{
		ProviderMessageID: messageID, SentAt: sentAt, SenderClass: employeeentry.SceneSenderHuman,
		SenderRef: "dingtalk:" + org + ":open_id:" + senderOpenID, SenderName: nativeDisplayName(m.Sender),
		Body: inboundcoord.RedactConfigLinks(content)}}
	if quoted := m.QuotedMessage; quoted != nil {
		row.QuotedMessageID = strings.TrimSpace(quoted.MessageID)
	}
	if employeeProactiveCandidate(content, sentAt, now) {
		due := sentAt.Add(employeeProactiveWait)
		if due.Before(now) {
			due = now
		}
		row.ProactiveDueAt = due
	}
	stored := false
	_, replay, err := eventrouter.AdmitWithHook(ctx, h.TxStarter, event, eventrouter.Host{
		Owner: sceneOwner, PrincipalID: endpoint.ActorUserID, TenantOrgID: org, Locator: locator,
		Observation: scene.Observation{KindStated: true}, Route: eventrouter.Unified, ConfigVersion: employeeObservationVersion,
		Fingerprint: eventPayloadFingerprint(employeeObservationVersion, payload), ExistingSceneOnly: true,
	}, func(ctx context.Context, tx pgx.Tx, receipt db.SceneEventReceipt) error {
		if !receipt.SceneID.Valid || receipt.SceneID != registered.ID {
			return nil
		}
		row.ReceiptID = uuidToString(receipt.ID)
		key := employeeentry.Scope{WorkspaceID: uuidToString(agent.WorkspaceID), AgentID: uuidToString(agent.ID), TenantOrgID: org, SceneID: uuidToString(registered.ID)}
		stored = true
		return h.insertEmployeeSceneMessagesTx(ctx, tx, key, employeeentry.SceneMessageSourceNativeGroup, []employeeentry.SceneMessageRow{row})
	})
	switch {
	case errors.Is(err, eventrouter.ErrConflict), errors.Is(err, scene.ErrStaleTenant), errors.Is(err, eventrouter.ErrInvalidEvent):
		return "receipt_" + strings.ReplaceAll(err.Error(), " ", "_"), nil
	case err != nil:
		return "", fmt.Errorf("admit observation: %w", err)
	case replay:
		return "duplicate", nil
	case !stored:
		return "scene_unmapped", nil
	}
	return "stored", nil
}

// employeeCardSummary recognises the localized summary of a card message
// ("[互动卡片] …", "[卡片]…"), which digital employees post as intros.
func employeeCardSummary(content string) bool {
	return strings.HasPrefix(content, "[") && strings.Contains(content[:min(len(content), 48)], "卡片")
}

var (
	// employeeQuestionMarkers make a line question-like.
	employeeQuestionMarkers = []string{"?", "？", "请问", "谁", "哪", "多少", "吗", "几", "什么", "怎么", "如何", "是否", "有没有", "能不能", "可不可以", "为什么", "啥"}
	// employeeChitChatMarkers are small talk the gate never wakes on.
	employeeChitChatMarkers = []string{"吃饭", "午饭", "晚饭", "早饭", "中午吃", "晚上吃", "吃什么", "外卖", "奶茶", "咖啡", "天气", "下雨", "周末", "放假", "下班", "摸鱼", "哈哈", "游戏", "电影", "八卦"}
	employeeMentionPattern  = regexp.MustCompile(`@\S`)
)

// employeeProactiveCandidate is the cheap ingest-time filter: a fresh,
// question-like line that is not small talk and does not @-mention anyone
// (an @-mention of the employee is handled by the @ path; of someone else,
// is not the employee's to answer).
func employeeProactiveCandidate(content string, sentAt, now time.Time) bool {
	if now.Sub(sentAt) > employeeProactiveFreshness || employeeMentionPattern.MatchString(content) {
		return false
	}
	for _, marker := range employeeChitChatMarkers {
		if strings.Contains(content, marker) {
			return false
		}
	}
	for _, marker := range employeeQuestionMarkers {
		if strings.Contains(content, marker) {
			return true
		}
	}
	return false
}
