package handler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/multica-ai/multica/server/internal/dwsclient"
	"github.com/multica-ai/multica/server/internal/employeeentry"
	"github.com/multica-ai/multica/server/internal/employeeloopconfig"
	"github.com/multica-ai/multica/server/internal/scene"
	"github.com/multica-ai/multica/server/internal/service/employeememory"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dws"
	dwsevents "github.com/multica-ai/multica/server/pkg/dws/events"
)

// Proactive wake gate (DS-09, GawkBot "observe all, wake selectively").
//
// An unaddressed human question stored from the group observation becomes a
// pending candidate. After employeeProactiveWait the Host decides it with no
// model call; it admits one normal unaddressed employee wake only when all
// hold: the scene is still the agent's and runs the Employee loop, nobody
// @-addressed the line, no other person spoke after it, the employee was not
// told to stay quiet in the scene (until told it is over), the question
// shares at least two retrieval units with the agent's duty text or the
// scene's recent topics, and the scene had fewer than
// employeeProactivePerHour proactive wakes in the last hour. The wake goes
// through the native dispatch pipeline with an empty mention list, so the
// Loop sees an unaddressed line (Quiet is a correct outcome) and cannot
// dispatch a Task from it.

const (
	employeeProactivePerHour  = 6
	employeeProactiveExpiry   = 10 * time.Minute
	employeeProactiveBatch    = 20
	employeeProactiveTick     = 5 * time.Second
	employeeTopicLookback     = 6 * time.Hour
	employeeTopicLines        = 30
	employeePurgeTick         = 10 * time.Minute
	employeePurgeBatch        = 500
	employeePurgeBatchesPerGo = 20
)

// EmployeeSceneMessageWorker runs the proactive gate and the transcript
// retention purge on every replica; row locks keep them exclusive.
type EmployeeSceneMessageWorker struct {
	handler *Handler
	now     func() time.Time
	// admit admits the wake; tests replace it.
	admit func(context.Context, employeeentry.ProactiveCandidate, db.AgentScene) (string, bool, error)
}

func NewEmployeeSceneMessageWorker(h *Handler) *EmployeeSceneMessageWorker {
	w := &EmployeeSceneMessageWorker{handler: h, now: time.Now}
	w.admit = h.admitEmployeeProactiveWake
	return w
}

func (w *EmployeeSceneMessageWorker) Run(ctx context.Context) {
	proactive := time.NewTicker(employeeProactiveTick)
	purge := time.NewTicker(employeePurgeTick)
	defer proactive.Stop()
	defer purge.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-proactive.C:
			if _, err := w.RunProactiveOnce(ctx); err != nil && ctx.Err() == nil {
				slog.Warn("employee proactive gate failed", "event", "employee_proactive_gate_failed", "error", err)
			}
		case <-purge.C:
			if _, err := w.PurgeOnce(ctx); err != nil && ctx.Err() == nil {
				slog.Warn("employee group transcript purge failed", "event", "employee_scene_message_purge_failed", "error", err)
			}
		}
	}
}

// PurgeOnce deletes transcript rows past retention in bounded batches.
func (w *EmployeeSceneMessageWorker) PurgeOnce(ctx context.Context) (int, error) {
	database, ok := employeeEntryDB(w.handler)
	if !ok {
		return 0, nil
	}
	cutoff := w.now().Add(-employeeentry.SceneMessageRetention)
	total := 0
	for i := 0; i < employeePurgeBatchesPerGo; i++ {
		n, receipts, err := employeeentry.PurgeSceneMessages(ctx, database, cutoff, employeePurgeBatch)
		if err != nil {
			return total, err
		}
		total += n
		if n > 0 {
			slog.Info("employee group transcript purged", "event", "employee_scene_message_purged", "rows", n, "receipts", receipts)
		}
		if n < employeePurgeBatch {
			break
		}
	}
	return total, nil
}

// RunProactiveOnce decides the due candidates this replica could lock.
func (w *EmployeeSceneMessageWorker) RunProactiveOnce(ctx context.Context) (int, error) {
	h := w.handler
	database, ok := employeeEntryDB(h)
	if !ok || !h.employeeMemoryObserveReady(ctx) {
		return 0, nil
	}
	now := w.now().UTC()
	tx, err := database.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	candidates, err := employeeentry.ClaimDueProactive(ctx, tx, now, employeeProactiveBatch)
	if err != nil || len(candidates) == 0 {
		return 0, err
	}
	decided := 0
	for _, c := range candidates {
		admit, reason, registered, err := h.decideEmployeeProactive(ctx, database, c, now)
		if err == nil && admit {
			var retry bool
			reason, retry, err = w.admit(ctx, c, registered)
			if err == nil && retry {
				err = errors.New(reason)
			}
		}
		state := employeeentry.ProactiveSkipped
		if err == nil && reason == "admitted" {
			state = employeeentry.ProactiveAdmitted
		}
		if err != nil {
			if now.Sub(c.DueAt) < employeeProactiveExpiry {
				// Retryable: stays pending for the next tick.
				slog.Warn("employee proactive candidate deferred", "event", "employee_proactive_deferred",
					"agent_id", c.Key.AgentID, "scene_id", c.Key.SceneID, "error", err)
				continue
			}
			reason = "expired"
		}
		if err := employeeentry.DecideProactive(ctx, tx, c.Key, c.Message.ProviderMessageID, state, reason, now); err != nil {
			return decided, err
		}
		decided++
		slog.Info("employee proactive candidate decided", "event", "employee_proactive_decided", "workspace_id", c.Key.WorkspaceID,
			"agent_id", c.Key.AgentID, "scene_id", c.Key.SceneID, "state", state, "reason", reason,
			"waited_ms", now.Sub(c.Message.SentAt).Milliseconds())
	}
	return decided, tx.Commit(ctx)
}

// decideEmployeeProactive applies the deterministic gate. An error means the
// facts could not be read; the candidate is retried until it expires.
func (h *Handler) decideEmployeeProactive(ctx context.Context, q employeeentry.SceneMessageDB, c employeeentry.ProactiveCandidate, now time.Time) (bool, string, db.AgentScene, error) {
	if c.Withdrawn {
		return false, "withdrawn", db.AgentScene{}, nil
	}
	ws, wsErr := util.ParseUUID(c.Key.WorkspaceID)
	agentID, agentErr := util.ParseUUID(c.Key.AgentID)
	if wsErr != nil || agentErr != nil || h.Queries == nil {
		return false, "invalid_scope", db.AgentScene{}, nil
	}
	owner := scene.Owner{WorkspaceID: ws, AgentID: agentID}
	registered, err := fencedScene(ctx, h.Queries, &scene.Ref{SceneID: c.Key.SceneID}, owner, c.Key.TenantOrgID)
	if err != nil {
		if errors.Is(err, scene.ErrNotFound) || errors.Is(err, scene.ErrStaleTenant) || errors.Is(err, scene.ErrUnresolved) {
			return false, "scene_inactive", db.AgentScene{}, nil
		}
		return false, "", db.AgentScene{}, err
	}
	if registered.SceneKind != scene.KindGroup {
		return false, "not_group", registered, nil
	}
	config, err := employeeloopconfig.Load(ctx, q, ws, agentID)
	if err != nil {
		return false, "", registered, err
	}
	if !config.Enabled || config.Mode != employeeloopconfig.Employee {
		return false, "not_employee_mode", registered, nil
	}
	admitted, err := employeeentry.MessageAdmitted(ctx, q, c.Key, c.Message.ProviderMessageID, c.Message.SentAt.Add(-employeeProactiveExpiry))
	if err != nil {
		return false, "", registered, err
	}
	if admitted {
		return false, "addressed", registered, nil
	}
	replies, err := employeeentry.HumanRepliesAfter(ctx, q, c.Key, c.Message, now)
	if err != nil {
		return false, "", registered, err
	}
	if replies > 0 {
		return false, "answered", registered, nil
	}
	participation, err := employeeReadParticipation(ctx, q, employeeentry.Scope{WorkspaceID: c.Key.WorkspaceID, AgentID: c.Key.AgentID, TenantOrgID: c.Key.TenantOrgID, SceneID: c.Key.SceneID})
	if err != nil {
		return false, "", registered, err
	}
	if participation.Mode == "quiet" {
		return false, "quiet", registered, nil
	}

	agent, err := h.Queries.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{ID: agentID, WorkspaceID: ws})
	if err != nil {
		return false, "", registered, err
	}
	duty, _ := employeeRoleInstructions(agent)
	corpus := []string{duty}
	addressed, err := employeeentry.RecentAdmittedTexts(ctx, q, c.Key, now.Add(-employeeTopicLookback), 100)
	if err != nil {
		return false, "", registered, err
	}
	for _, text := range addressed {
		corpus = append(corpus, text.Text)
	}
	topics, err := employeeentry.SceneMessagesBefore(ctx, q, c.Key, c.Message.SentAt, c.Message.ProviderMessageID, employeeTopicLines)
	if err != nil {
		return false, "", registered, err
	}
	for _, line := range topics {
		if c.Message.SentAt.Sub(line.SentAt) <= employeeTopicLookback {
			corpus = append(corpus, line.Body)
		}
	}
	if len(employeememory.RankTexts(c.Message.Body, corpus, 1)) == 0 {
		return false, "outside_duty", registered, nil
	}
	recent, err := employeeentry.ProactiveAdmittedSince(ctx, q, c.Key, now.Add(-time.Hour))
	if err != nil {
		return false, "", registered, err
	}
	if recent >= employeeProactivePerHour {
		return false, "rate_limited", registered, nil
	}
	return true, "", registered, nil
}

func employeeProactiveIdempotencyKey(org, conversationID, messageID string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{org, conversationID, messageID}, "\x00")))
	return "dws-native-proactive:v1:" + hex.EncodeToString(sum[:])
}

// admitEmployeeProactiveWake submits the stored question as a native group
// message without mentions through the in-process dispatch pipeline. The
// idempotency key and command are deterministic, so a retry after a crash
// replays the same admission. It returns the outcome ("admitted" or a skip
// reason) and whether a failure is worth retrying.
func (h *Handler) admitEmployeeProactiveWake(ctx context.Context, c employeeentry.ProactiveCandidate, registered db.AgentScene) (string, bool, error) {
	store := h.nativeDispatch()
	subscriptions := h.nativeSubscriptions()
	if store == nil || subscriptions == nil {
		return "store_unavailable", false, nil
	}
	rows, err := subscriptions.ListActiveDWSNativeSubscriptions(ctx)
	if err != nil {
		return "", true, err
	}
	var identity dwsclient.Identity
	for _, row := range rows {
		if row.AgentID == registered.AgentID && row.WorkspaceID == registered.WorkspaceID && row.OrgID == c.Key.TenantOrgID {
			identity = dwsclient.Identity{AgentID: c.Key.AgentID, UID: row.DwsUid, OrgID: row.OrgID}
		}
	}
	if identity.UID == "" {
		return "native_identity_missing", false, nil
	}
	agent, err := store.GetAgent(ctx, registered.AgentID)
	if err != nil {
		return "", true, err
	}
	policy, reason, err := nativeManagedResponsePolicy(ctx, store, agent)
	if err != nil {
		return "", true, err
	}
	if reason != "" {
		return "response_not_managed", false, nil
	}
	endpoint, err := store.GetAgentDispatchEndpointForDelivery(ctx, db.GetAgentDispatchEndpointForDeliveryParams{AgentID: agent.ID, WorkspaceID: agent.WorkspaceID})
	if errors.Is(err, pgx.ErrNoRows) {
		return "dispatch_endpoint_missing", false, nil
	}
	if err != nil {
		return "", true, err
	}
	conversationID := registered.ExternalSceneID
	senderOpenID := strings.TrimPrefix(c.Message.SenderRef, "dingtalk:"+c.Key.TenantOrgID+":open_id:")
	if senderOpenID == c.Message.SenderRef || senderOpenID == "" {
		return "sender_unknown", false, nil
	}
	message := &dwsevents.MessageEvent{MessageID: c.Message.ProviderMessageID, ConversationID: conversationID,
		Sender: c.Message.SenderName, SenderOpenDingTalkID: senderOpenID, Content: c.Message.Body}
	message.EventTime = c.Message.SentAt.UnixMilli()
	if c.Message.QuotedMessageID != "" {
		message.QuotedMessage = &dwsevents.MessageContext{MessageID: c.Message.QuotedMessageID}
	}
	command, err := buildNativeDispatchCommand(nativeMessageInput{
		AgentID: c.Key.AgentID, UID: identity.UID, OrgID: identity.OrgID, EventKey: dws.EventIMAllGroups, Message: message, Policy: policy,
		ConversationTitle: h.nativeConversationTitle(ctx, identity, conversationID),
		SenderStaffID:     h.nativeSenderStaffID(ctx, identity, message, conversationID),
	})
	var skipped nativeSkip
	if errors.As(err, &skipped) {
		return string(skipped), false, nil
	}
	if err != nil {
		return "", false, err
	}
	dispatchContext := agentDispatchContext{EndpointID: endpoint.EndpointID, EndpointNamespaceID: endpoint.ID, UserID: endpoint.ActorUserID,
		WorkspaceID: agent.WorkspaceID, AgentID: agent.ID}
	status, body, err := h.submitNativeDispatch(ctx, command, dispatchContext, employeeProactiveIdempotencyKey(identity.OrgID, conversationID, c.Message.ProviderMessageID))
	if err != nil {
		return "", true, err
	}
	switch {
	case status >= http.StatusOK && status < http.StatusMultipleChoices:
		return "admitted", false, nil
	case status == http.StatusConflict && strings.Contains(body, errAgentDispatchAcceptancePending.Error()), status >= http.StatusInternalServerError:
		return "dispatch_deferred", true, nil
	default:
		slog.Warn("employee proactive wake rejected by the dispatch pipeline", "event", "employee_proactive_rejected",
			"agent_id", c.Key.AgentID, "scene_id", c.Key.SceneID, "status", status, "body", clipRunes(body, 300))
		return "dispatch_rejected", false, nil
	}
}

// employeeUnaddressedWindowNote is the Host fact for a group window in which
// no line @-mentions the employee (a proactive wake, or an unaddressed line
// of a proactive conversation). It is empty for 1:1 windows, addressed
// windows and legacy lines whose mention list is unknown.
func employeeUnaddressedWindowNote(envelopes []employeeDispatchEnvelope) string {
	if len(envelopes) == 0 || employeeWindowAddressed(envelopes) {
		return ""
	}
	for _, env := range envelopes {
		if !strings.EqualFold(env.Command.Event.Data.Conversation.Type, "group") {
			return ""
		}
		for _, message := range env.Command.Event.Data.Messages {
			if message.Mentions == nil {
				return ""
			}
		}
	}
	return "Unaddressed group window (Host fact): nobody @-mentioned you in these group lines; the Host woke you only because the latest one looks like an unanswered question within your duty. " +
		"Reply once and briefly only if it is yours to answer and the material you have answers it correctly; otherwise stay Quiet, which is a correct outcome. Do not ask whether you should answer, and do not start or dispatch work from it."
}
