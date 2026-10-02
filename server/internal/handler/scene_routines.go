package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/contextcap"
	"github.com/multica-ai/multica/server/internal/scene"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/service/dingtalkresponse"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
	"github.com/multica-ai/multica/server/pkg/redact"
)

// Scene routines (例行任务, docs/context-capabilities.md §9) are cron or
// webhook autopilots bound to one Agent work scene, a group or a 1:1 chat.
// The autopilot carries schedule, trigger and run history; context_scope_routine
// binds it to the scene. Every run carries the scene (agent_scene plus the
// frozen scene_routine binding) so the claim applies that scene's
// configuration, and the Host posts a start and an end notice into the scene.
// The configure pages and the config-qwen-tag-scene MCP share the functions
// below; none of them writes an HTTP response.

const (
	sceneRoutineDefaultTimezone = "Asia/Shanghai"
	// sceneRoutineMinInterval is the shortest allowed gap between two runs.
	sceneRoutineMinInterval   = 15 * time.Minute
	sceneRoutineMaxTitle      = 120
	sceneRoutineMaxPrompt     = 8000
	sceneRoutineNoticeMax     = 2800
	sceneRoutinePreviewCount  = 3
	sceneRoutineIntervalProbe = 12
	sceneRoutineTriggerCron   = "schedule"
	sceneRoutineTriggerHook   = "webhook"
)

// sceneRoutineRunNote ends a routine run's instructions at claim: the Host
// already posts the start and end notices into the scene.
const sceneRoutineRunNote = "（这是场域例行任务的一次运行：把结果作为最终输出交回即可，不要自己往群聊或单聊里发消息；系统会在场域里发开始和结束消息，并附上你的最终输出。）"

// sceneRoutineError is a refusal with its HTTP status and error code; the
// MCP tools report the same code.
type sceneRoutineError struct {
	Status  int
	Code    string
	Message string
}

func (e *sceneRoutineError) Error() string { return e.Code + ": " + e.Message }

func routineRefusal(status int, code, message string) error {
	return &sceneRoutineError{Status: status, Code: code, Message: message}
}

func routineInvalid(message string) error {
	return routineRefusal(http.StatusBadRequest, "invalid_routine", message)
}

// sceneRoutineTrigger is the trigger of a routine: a cron schedule in a
// timezone or a webhook.
type sceneRoutineTrigger struct {
	Kind     string `json:"kind"`
	Cron     string `json:"cron,omitempty"`
	Timezone string `json:"timezone,omitempty"`
}

// sceneRoutineInput creates a routine.
type sceneRoutineInput struct {
	Title        string              `json:"title"`
	Instructions string              `json:"instructions"`
	Trigger      sceneRoutineTrigger `json:"trigger"`
}

// sceneRoutinePatch edits a routine; nil fields stay. The trigger kind never
// changes (delete and recreate instead); a schedule may change its cron and
// timezone.
type sceneRoutinePatch struct {
	Title        *string `json:"title"`
	Instructions *string `json:"instructions"`
	Enabled      *bool   `json:"enabled"`
	Cron         *string `json:"cron"`
	Timezone     *string `json:"timezone"`
}

// sceneRoutineActor is who writes a routine: a configure-page member, or the
// agent from a task in the scene (the config-qwen-tag-scene MCP).
type sceneRoutineActor struct {
	Type    string // contextcap.RoutineCreatedByMember | RoutineCreatedByAgent
	UserID  pgtype.UUID
	AgentID pgtype.UUID
	TaskID  pgtype.UUID
}

func (a sceneRoutineActor) id() pgtype.UUID {
	if a.Type == contextcap.RoutineCreatedByAgent {
		return a.AgentID
	}
	return a.UserID
}

// memberUserID is the member a manual run is attributed to; an agent actor
// has none.
func (a sceneRoutineActor) memberUserID() pgtype.UUID {
	if a.Type == contextcap.RoutineCreatedByMember {
		return a.UserID
	}
	return pgtype.UUID{}
}

// sceneRoutineCounterpart is the 1:1 chat counterpart a dm routine sends to,
// frozen at creation.
type sceneRoutineCounterpart struct {
	OpenDingTalkID string
}

type sceneRoutineTriggerView struct {
	ID               string   `json:"id"`
	Kind             string   `json:"kind"`
	Cron             string   `json:"cron,omitempty"`
	Timezone         string   `json:"timezone,omitempty"`
	NextRunAt        *string  `json:"next_run_at"`
	NextRuns         []string `json:"next_runs"`
	WebhookURLMasked string   `json:"webhook_url_masked,omitempty"`
	// WebhookURL is the full URL, returned only when the token was just
	// minted (create, rotate).
	WebhookURL string `json:"webhook_url,omitempty"`
}

type sceneRoutineRunView struct {
	ID            string  `json:"id"`
	Status        string  `json:"status"`
	Source        string  `json:"source"`
	FailureReason string  `json:"failure_reason,omitempty"`
	CreatedAt     string  `json:"created_at"`
	CompletedAt   *string `json:"completed_at"`
}

type sceneRoutineView struct {
	ID            string                  `json:"id"`
	SceneID       string                  `json:"scene_id"`
	SceneKind     string                  `json:"scene_kind"`
	AutopilotID   string                  `json:"autopilot_id"`
	Title         string                  `json:"title"`
	Instructions  string                  `json:"instructions"`
	Enabled       bool                    `json:"enabled"`
	PauseReason   string                  `json:"pause_reason,omitempty"`
	Trigger       sceneRoutineTriggerView `json:"trigger"`
	LastRun       *sceneRoutineRunView    `json:"last_run"`
	CreatedByType string                  `json:"created_by_type"`
	CreatedAt     string                  `json:"created_at"`
	UpdatedAt     string                  `json:"updated_at"`
}

// sceneRoutineResult is a write's outcome. Updated is true when a create
// matched an existing routine of the scene (same purpose and schedule) and
// updated it instead; TellTheHuman is what the agent should relay.
type sceneRoutineResult struct {
	Routine      sceneRoutineView `json:"routine"`
	Updated      bool             `json:"updated"`
	TellTheHuman string           `json:"tell_the_human,omitempty"`
}

// normalizeRoutineText trims and bounds a title or instruction.
func normalizeRoutineText(field, value string, max int) (string, error) {
	value = strings.TrimSpace(value)
	switch {
	case value == "":
		return "", routineInvalid(field + " is required")
	case utf8.RuneCountInString(value) > max:
		return "", routineInvalid(fmt.Sprintf("%s is longer than %d characters", field, max))
	case strings.ContainsRune(value, 0):
		return "", routineInvalid(field + " is invalid")
	}
	return value, nil
}

// normalizeRoutineSchedule validates a cron expression and timezone (default
// Asia/Shanghai) and enforces the minimum interval over the next runs.
func normalizeRoutineSchedule(cron, timezone string) (string, string, error) {
	cron = strings.Join(strings.Fields(cron), " ")
	timezone = strings.TrimSpace(timezone)
	if timezone == "" {
		timezone = sceneRoutineDefaultTimezone
	}
	if cron == "" {
		return "", "", routineInvalid("cron is required for a schedule")
	}
	if err := service.ValidateTimezone(timezone); err != nil {
		return "", "", routineInvalid(err.Error())
	}
	runs, err := service.NextOccurrencesAfterUTC(cron, timezone, time.Now(), sceneRoutineIntervalProbe)
	if err != nil {
		return "", "", routineInvalid("invalid cron expression: " + err.Error())
	}
	if len(runs) == 0 {
		return "", "", routineInvalid("the cron expression never runs")
	}
	for i := 1; i < len(runs); i++ {
		if runs[i].Sub(runs[i-1]) < sceneRoutineMinInterval {
			return "", "", routineInvalid("a routine runs at most every 15 minutes")
		}
	}
	return cron, timezone, nil
}

// normalizeRoutineInput validates a create request.
func normalizeRoutineInput(in sceneRoutineInput) (sceneRoutineInput, error) {
	var err error
	if in.Title, err = normalizeRoutineText("title", in.Title, sceneRoutineMaxTitle); err != nil {
		return in, err
	}
	if in.Instructions, err = normalizeRoutineText("instructions", in.Instructions, sceneRoutineMaxPrompt); err != nil {
		return in, err
	}
	switch in.Trigger.Kind = strings.TrimSpace(in.Trigger.Kind); in.Trigger.Kind {
	case sceneRoutineTriggerCron:
		in.Trigger.Cron, in.Trigger.Timezone, err = normalizeRoutineSchedule(in.Trigger.Cron, in.Trigger.Timezone)
		if err != nil {
			return in, err
		}
	case sceneRoutineTriggerHook:
		if strings.TrimSpace(in.Trigger.Cron) != "" || strings.TrimSpace(in.Trigger.Timezone) != "" {
			return in, routineInvalid("cron and timezone are only valid for a schedule")
		}
		in.Trigger.Cron, in.Trigger.Timezone = "", ""
	default:
		return in, routineInvalid("trigger.kind must be schedule or webhook")
	}
	return in, nil
}

func routineDedupeKey(title string, trigger sceneRoutineTrigger) string {
	return contextcap.RoutineDedupeKey(title, trigger.Kind, trigger.Cron, trigger.Timezone)
}

// sceneRoutineScene loads one of the agent's scenes in a.OrgID that takes
// routines: a group or a 1:1 chat of the agent's current tenant org.
func (h *Handler) sceneRoutineScene(ctx context.Context, a contextCapAgent, sceneID string) (db.AgentScene, error) {
	id, err := scene.ParseID(sceneID)
	if err != nil {
		return db.AgentScene{}, routineInvalid("scene_id is invalid")
	}
	sc, err := scene.Get(ctx, h.Queries, scene.Owner{WorkspaceID: parseUUID(a.WorkspaceID), AgentID: parseUUID(a.ID)}, id)
	if errors.Is(err, scene.ErrNotFound) || (err == nil && sc.TenantOrgID != a.OrgID) {
		return db.AgentScene{}, routineRefusal(http.StatusNotFound, "scene_not_found", "scene not found")
	}
	if err != nil {
		return db.AgentScene{}, err
	}
	if sc.SceneKind != scene.KindGroup && sc.SceneKind != scene.KindDM {
		return db.AgentScene{}, routineRefusal(http.StatusConflict, "scene_kind_without_routines", "only group and 1:1 chat scenes take routines")
	}
	return sc, nil
}

// routineIdentity is the agent's DingTalk identity that posts the notices of
// a routine in org: the agent must be bound in that org.
func (h *Handler) routineIdentity(ctx context.Context, q *db.Queries, workspaceID, agentID pgtype.UUID, orgID string) (db.AgentDingtalkIdentity, error) {
	identity, err := q.GetAgentDingTalkIdentity(ctx, db.GetAgentDingTalkIdentityParams{WorkspaceID: workspaceID, AgentID: agentID})
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && (identity.OrgID != orgID || strings.TrimSpace(identity.DwsUid) == "")) {
		return db.AgentDingtalkIdentity{}, routineRefusal(http.StatusConflict, "routine_requires_dingtalk_identity",
			"the agent has no DingTalk identity in this scene's org, so it cannot post a routine's results there")
	}
	return identity, err
}

// sceneRoutineDMCounterpart finds who a configure-page routine of a dm scene
// sends to: the sender of the scene's newest trusted inbound Coordinator job
// (the server-written dispatch sender). A task in the 1:1 chat passes its own
// sender instead. Only delivery uses it: a routine never runs with anyone's
// personal layer.
func (h *Handler) sceneRoutineDMCounterpart(ctx context.Context, a contextCapAgent, sceneID string) (sceneRoutineCounterpart, error) {
	var cp sceneRoutineCounterpart
	err := h.DB.QueryRow(ctx, `SELECT
			BTRIM(COALESCE(NULLIF(command #>> '{event,data,sender,openDingTalkId}', ''), command #>> '{event,data,sender,senderOpenDingTalkId}', ''))
		FROM inbound_coordinator_job
		WHERE agent_id = $2::uuid AND workspace_id = $1::uuid AND command #>> '{agent_scene,scene_id}' = $3
		ORDER BY created_at DESC, id DESC
		LIMIT 1`, a.WorkspaceID, a.ID, sceneID).Scan(&cp.OpenDingTalkID)
	if errors.Is(err, pgx.ErrNoRows) {
		err = nil
	}
	if err != nil {
		return cp, err
	}
	if cp.OpenDingTalkID == "" {
		return cp, routineRefusal(http.StatusConflict, "dm_target_unknown",
			"this 1:1 chat has no message from its counterpart yet; send the agent a message there first")
	}
	return cp, nil
}

// createSceneRoutine creates a routine in sc, or updates the routine of sc
// that has the same purpose and schedule (it never re-enables a paused one
// or moves it to another scene). cp is the 1:1 counterpart of a dm scene.
func (h *Handler) createSceneRoutine(ctx context.Context, a contextCapAgent, sc db.AgentScene, actor sceneRoutineActor, cp sceneRoutineCounterpart, in sceneRoutineInput) (sceneRoutineResult, error) {
	in, err := normalizeRoutineInput(in)
	if err != nil {
		return sceneRoutineResult{}, err
	}
	sceneID := util.UUIDToString(sc.ID)
	key := routineDedupeKey(in.Title, in.Trigger)
	if existing, err := contextcap.GetRoutineByDedupe(ctx, h.DB, sceneID, key); err == nil {
		result, err := h.refreshDuplicateRoutine(ctx, a, existing, actor, in)
		if !isRoutineGone(err) {
			return result, err
		}
		if err := h.deleteSceneRoutine(ctx, a, existing, actor); err != nil {
			return sceneRoutineResult{}, err
		}
	} else if !errors.Is(err, contextcap.ErrNotFound) {
		return sceneRoutineResult{}, err
	}
	if _, err := h.routineIdentity(ctx, h.Queries, sc.WorkspaceID, sc.AgentID, sc.TenantOrgID); err != nil {
		return sceneRoutineResult{}, err
	}
	if sc.SceneKind == scene.KindDM && cp.OpenDingTalkID == "" {
		return sceneRoutineResult{}, routineRefusal(http.StatusConflict, "dm_target_unknown", "the 1:1 chat counterpart is unknown")
	}
	if sc.SceneKind != scene.KindDM {
		cp = sceneRoutineCounterpart{}
	}

	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return sceneRoutineResult{}, err
	}
	defer tx.Rollback(ctx)
	qtx := h.Queries.WithTx(tx)
	agent, err := qtx.LockAgentForAutopilotAssignment(ctx, db.LockAgentForAutopilotAssignmentParams{ID: sc.AgentID, WorkspaceID: sc.WorkspaceID})
	if err != nil {
		return sceneRoutineResult{}, fmt.Errorf("lock agent: %w", err)
	}
	if !agent.RuntimeID.Valid {
		return sceneRoutineResult{}, routineRefusal(http.StatusConflict, "agent_runtime_required", "the agent has no runtime to run routines on")
	}
	ap, err := qtx.CreateAutopilot(ctx, db.CreateAutopilotParams{
		WorkspaceID:   sc.WorkspaceID,
		Title:         in.Title,
		Description:   pgtype.Text{String: in.Instructions, Valid: true},
		AssigneeType:  "agent",
		AssigneeID:    sc.AgentID,
		Status:        "active",
		ExecutionMode: "run_only",
		CreatedByType: actor.Type,
		CreatedByID:   actor.id(),
	})
	if err != nil {
		return sceneRoutineResult{}, fmt.Errorf("create autopilot: %w", err)
	}
	if err := service.RecordAutopilotRuleVersion(ctx, qtx, ap, actor.Type, actor.id()); err != nil {
		return sceneRoutineResult{}, err
	}
	trigger, webhookURL, err := h.createRoutineTrigger(ctx, qtx, ap, actor, in.Trigger)
	if err != nil {
		return sceneRoutineResult{}, err
	}
	routine, err := contextcap.InsertRoutine(ctx, tx, contextcap.Routine{
		WorkspaceID: a.WorkspaceID, AgentID: a.ID, SceneID: sceneID, TenantOrgID: sc.TenantOrgID, SceneKind: sc.SceneKind,
		AutopilotID: util.UUIDToString(ap.ID), DeliveryOpenDingTalkID: cp.OpenDingTalkID,
		DedupeKey: key, CreatedByType: actor.Type, CreatedByID: util.UUIDToString(actor.id()),
		CreatedTaskID: util.UUIDToString(actor.TaskID),
	})
	if errors.Is(err, contextcap.ErrRoutineDuplicate) {
		// A concurrent create of the same routine won; update that one.
		tx.Rollback(ctx)
		existing, err := contextcap.GetRoutineByDedupe(ctx, h.DB, sceneID, key)
		if err != nil {
			return sceneRoutineResult{}, err
		}
		return h.refreshDuplicateRoutine(ctx, a, existing, actor, in)
	}
	if err != nil {
		return sceneRoutineResult{}, fmt.Errorf("insert routine: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return sceneRoutineResult{}, err
	}
	h.publish(protocol.EventAutopilotCreated, a.WorkspaceID, actor.Type, util.UUIDToString(actor.id()), map[string]any{"autopilot": autopilotToResponse(ap, nil)})
	slog.InfoContext(ctx, "scene routine created", "routine_id", routine.ID, "autopilot_id", routine.AutopilotID,
		"scene_id", sceneID, "scene_kind", sc.SceneKind, "trigger", in.Trigger.Kind, "actor_type", actor.Type,
		"task_id", util.UUIDToString(actor.TaskID))
	view, err := h.sceneRoutineView(ctx, routine, &ap, &trigger)
	if err != nil {
		return sceneRoutineResult{}, err
	}
	view.Trigger.WebhookURL = webhookURL
	return sceneRoutineResult{Routine: view, TellTheHuman: routineCreatedMessage(view)}, nil
}

// createRoutineTrigger creates the routine's single trigger and returns the
// full webhook URL for a webhook (shown once).
func (h *Handler) createRoutineTrigger(ctx context.Context, qtx *db.Queries, ap db.Autopilot, actor sceneRoutineActor, t sceneRoutineTrigger) (db.AutopilotTrigger, string, error) {
	params := db.CreateAutopilotTriggerParams{
		AutopilotID:     ap.ID,
		Kind:            t.Kind,
		Enabled:         true,
		PublishedByType: pgtype.Text{String: actor.Type, Valid: true},
		PublishedByID:   actor.id(),
	}
	if t.Kind == sceneRoutineTriggerCron {
		next, err := service.ComputeNextRun(t.Cron, t.Timezone)
		if err != nil {
			return db.AutopilotTrigger{}, "", routineInvalid(err.Error())
		}
		params.CronExpression = pgtype.Text{String: t.Cron, Valid: true}
		params.Timezone = pgtype.Text{String: t.Timezone, Valid: true}
		params.NextRunAt = pgtype.Timestamptz{Time: next, Valid: true}
	} else {
		token, err := generateWebhookToken()
		if err != nil {
			return db.AutopilotTrigger{}, "", err
		}
		params.WebhookToken = pgtype.Text{String: token, Valid: true}
		params.Provider = pgtype.Text{String: "generic", Valid: true}
	}
	trigger, err := qtx.CreateAutopilotTrigger(ctx, params)
	if err != nil {
		return db.AutopilotTrigger{}, "", fmt.Errorf("create trigger: %w", err)
	}
	return trigger, h.routineWebhookURL(trigger), nil
}

// refreshDuplicateRoutine applies a re-registration of an existing routine:
// title and instructions are refreshed; enabled state, scene and trigger stay.
func (h *Handler) refreshDuplicateRoutine(ctx context.Context, a contextCapAgent, routine contextcap.Routine, actor sceneRoutineActor, in sceneRoutineInput) (sceneRoutineResult, error) {
	result, err := h.updateSceneRoutine(ctx, a, routine, actor, sceneRoutinePatch{Title: &in.Title, Instructions: &in.Instructions})
	if err != nil {
		return sceneRoutineResult{}, err
	}
	result.Updated = true
	result.TellTheHuman = fmt.Sprintf("这个场域已经有相同的例行任务「%s」，已更新它的说明，没有新建；它的启停状态保持不变（%s）。",
		result.Routine.Title, routineEnabledLabel(result.Routine.Enabled))
	return result, nil
}

// updateSceneRoutine edits a routine. Enabling, disabling, a new schedule or
// new instructions republish the autopilot rule with the actor.
func (h *Handler) updateSceneRoutine(ctx context.Context, a contextCapAgent, routine contextcap.Routine, actor sceneRoutineActor, patch sceneRoutinePatch) (sceneRoutineResult, error) {
	ap, trigger, err := h.loadRoutineAutopilot(ctx, routine)
	if err != nil {
		return sceneRoutineResult{}, err
	}
	title, instructions := ap.Title, ap.Description.String
	if patch.Title != nil {
		if title, err = normalizeRoutineText("title", *patch.Title, sceneRoutineMaxTitle); err != nil {
			return sceneRoutineResult{}, err
		}
	}
	if patch.Instructions != nil {
		if instructions, err = normalizeRoutineText("instructions", *patch.Instructions, sceneRoutineMaxPrompt); err != nil {
			return sceneRoutineResult{}, err
		}
	}
	schedule := sceneRoutineTrigger{Kind: trigger.Kind, Cron: trigger.CronExpression.String, Timezone: trigger.Timezone.String}
	scheduleChanged := false
	if patch.Cron != nil || patch.Timezone != nil {
		if trigger.Kind != sceneRoutineTriggerCron {
			return sceneRoutineResult{}, routineInvalid("only a schedule routine has a cron and timezone")
		}
		if patch.Cron != nil {
			schedule.Cron = *patch.Cron
		}
		if patch.Timezone != nil {
			schedule.Timezone = *patch.Timezone
		}
		if schedule.Cron, schedule.Timezone, err = normalizeRoutineSchedule(schedule.Cron, schedule.Timezone); err != nil {
			return sceneRoutineResult{}, err
		}
		scheduleChanged = schedule.Cron != trigger.CronExpression.String || schedule.Timezone != trigger.Timezone.String
	}
	status := ap.Status
	if patch.Enabled != nil {
		status = "paused"
		if *patch.Enabled {
			status = "active"
		}
	}
	if status == "active" && ap.Status != "active" {
		if _, err := h.routineIdentity(ctx, h.Queries, ap.WorkspaceID, ap.AssigneeID, routine.TenantOrgID); err != nil {
			return sceneRoutineResult{}, err
		}
	}

	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return sceneRoutineResult{}, err
	}
	defer tx.Rollback(ctx)
	qtx := h.Queries.WithTx(tx)
	params := db.UpdateAutopilotParams{ID: ap.ID, Title: pgtype.Text{String: title, Valid: true}, Description: pgtype.Text{String: instructions, Valid: true}}
	if status != ap.Status {
		params.Status = pgtype.Text{String: status, Valid: true}
	}
	updated, err := qtx.UpdateAutopilot(ctx, params)
	if err != nil {
		return sceneRoutineResult{}, fmt.Errorf("update autopilot: %w", err)
	}
	if scheduleChanged {
		next, err := service.ComputeNextRun(schedule.Cron, schedule.Timezone)
		if err != nil {
			return sceneRoutineResult{}, routineInvalid(err.Error())
		}
		if trigger, err = qtx.UpdateAutopilotTrigger(ctx, db.UpdateAutopilotTriggerParams{
			ID:             trigger.ID,
			CronExpression: pgtype.Text{String: schedule.Cron, Valid: true},
			Timezone:       pgtype.Text{String: schedule.Timezone, Valid: true},
			NextRunAt:      pgtype.Timestamptz{Time: next, Valid: true},
		}); err != nil {
			return sceneRoutineResult{}, fmt.Errorf("update trigger: %w", err)
		}
		if err := qtx.SetAutopilotTriggerPublisher(ctx, db.SetAutopilotTriggerPublisherParams{
			ID: trigger.ID, PublishedByType: pgtype.Text{String: actor.Type, Valid: true}, PublishedByID: actor.id(),
		}); err != nil {
			return sceneRoutineResult{}, fmt.Errorf("stamp trigger publisher: %w", err)
		}
	}
	if status != ap.Status || scheduleChanged || instructions != ap.Description.String {
		if err := service.RecordAutopilotRuleVersion(ctx, qtx, updated, actor.Type, actor.id()); err != nil {
			return sceneRoutineResult{}, err
		}
	}
	if key := routineDedupeKey(title, schedule); key != routine.DedupeKey {
		if err := contextcap.SetRoutineDedupeKey(ctx, tx, routine.ID, key); err != nil {
			if errors.Is(err, contextcap.ErrRoutineDuplicate) {
				return sceneRoutineResult{}, routineRefusal(http.StatusConflict, "routine_duplicate", "this scene already has a routine with the same purpose and schedule")
			}
			return sceneRoutineResult{}, err
		}
		routine.DedupeKey = key
	}
	if err := tx.Commit(ctx); err != nil {
		return sceneRoutineResult{}, err
	}
	h.publish(protocol.EventAutopilotUpdated, a.WorkspaceID, actor.Type, util.UUIDToString(actor.id()), map[string]any{"autopilot": autopilotToResponse(updated, nil)})
	view, err := h.sceneRoutineView(ctx, routine, &updated, &trigger)
	if err != nil {
		return sceneRoutineResult{}, err
	}
	return sceneRoutineResult{Routine: view}, nil
}

// deleteSceneRoutine archives the routine's autopilot (its runs stay as
// history) and removes the routine.
func (h *Handler) deleteSceneRoutine(ctx context.Context, a contextCapAgent, routine contextcap.Routine, actor sceneRoutineActor) error {
	ap, _, err := h.loadRoutineAutopilot(ctx, routine)
	if err != nil && !isRoutineGone(err) {
		return err
	}
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	qtx := h.Queries.WithTx(tx)
	if ap.ID.Valid && ap.Status != "archived" {
		if err := qtx.ArchiveAutopilot(ctx, ap.ID); err != nil {
			return fmt.Errorf("archive autopilot: %w", err)
		}
		ap.Status = "archived"
		if err := service.RecordAutopilotRuleVersion(ctx, qtx, ap, actor.Type, actor.id()); err != nil {
			return err
		}
	}
	if err := contextcap.DeleteRoutine(ctx, tx, routine.ID); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	h.publish(protocol.EventAutopilotDeleted, a.WorkspaceID, actor.Type, util.UUIDToString(actor.id()), map[string]any{"autopilot_id": routine.AutopilotID})
	slog.InfoContext(ctx, "scene routine deleted", "routine_id", routine.ID, "autopilot_id", routine.AutopilotID,
		"scene_id", routine.SceneID, "actor_type", actor.Type, "task_id", util.UUIDToString(actor.TaskID))
	return nil
}

// runSceneRoutine runs a routine now (a manual run, attributed to the member
// when a member asked).
func (h *Handler) runSceneRoutine(ctx context.Context, routine contextcap.Routine, actor sceneRoutineActor) (*sceneRoutineRunView, error) {
	ap, _, err := h.loadRoutineAutopilot(ctx, routine)
	if err != nil {
		return nil, err
	}
	if ap.Status != "active" {
		return nil, routineRefusal(http.StatusConflict, "routine_paused", "enable the routine before running it")
	}
	run, _, err := h.AutopilotService.DispatchAutopilotManual(ctx, ap, pgtype.UUID{}, nil, actor.memberUserID())
	if err != nil {
		return nil, err
	}
	if run == nil {
		return nil, nil
	}
	return routineRunView(*run), nil
}

// rotateSceneRoutineWebhook mints a new webhook token; the old URL stops
// working. It returns the routine with the new full URL (shown once).
func (h *Handler) rotateSceneRoutineWebhook(ctx context.Context, routine contextcap.Routine, actor sceneRoutineActor) (sceneRoutineView, error) {
	ap, trigger, err := h.loadRoutineAutopilot(ctx, routine)
	if err != nil {
		return sceneRoutineView{}, err
	}
	if trigger.Kind != sceneRoutineTriggerHook {
		return sceneRoutineView{}, routineInvalid("only a webhook routine has a URL to rotate")
	}
	token, err := generateWebhookToken()
	if err != nil {
		return sceneRoutineView{}, err
	}
	rotated, err := h.Queries.RotateAutopilotTriggerWebhookToken(ctx, db.RotateAutopilotTriggerWebhookTokenParams{
		ID: trigger.ID, WebhookToken: pgtype.Text{String: token, Valid: true},
	})
	if err != nil {
		return sceneRoutineView{}, fmt.Errorf("rotate webhook token: %w", err)
	}
	slog.InfoContext(ctx, "scene routine webhook rotated", "routine_id", routine.ID, "actor_type", actor.Type,
		"task_id", util.UUIDToString(actor.TaskID))
	view, err := h.sceneRoutineView(ctx, routine, &ap, &rotated)
	if err != nil {
		return sceneRoutineView{}, err
	}
	view.Trigger.WebhookURL = h.routineWebhookURL(rotated)
	return view, nil
}

// listSceneRoutines lists the routines of one scene.
func (h *Handler) listSceneRoutines(ctx context.Context, a contextCapAgent, sceneID string) ([]sceneRoutineView, error) {
	routines, err := contextcap.ListSceneRoutines(ctx, h.DB, a.WorkspaceID, a.ID, sceneID)
	if err != nil {
		return nil, err
	}
	views := make([]sceneRoutineView, 0, len(routines))
	for _, routine := range routines {
		view, err := h.sceneRoutineView(ctx, routine, nil, nil)
		if isRoutineGone(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		views = append(views, view)
	}
	return views, nil
}

// loadSceneRoutine loads a routine of the agent in a.OrgID.
func (h *Handler) loadSceneRoutine(ctx context.Context, a contextCapAgent, id string) (contextcap.Routine, error) {
	routine, err := contextcap.GetRoutine(ctx, h.DB, a.WorkspaceID, a.ID, id)
	if errors.Is(err, contextcap.ErrNotFound) || errors.Is(err, contextcap.ErrInvalidInput) || (err == nil && routine.TenantOrgID != a.OrgID) {
		return contextcap.Routine{}, routineRefusal(http.StatusNotFound, "routine_not_found", "routine not found")
	}
	return routine, err
}

// errRoutineGone: the routine's autopilot was archived or lost its trigger
// outside the scene API (an old replica during a rollout). The routine is
// still deletable; create drops it and starts over.
var errRoutineGone error = &sceneRoutineError{Status: http.StatusConflict, Code: "routine_gone",
	Message: "this routine's autopilot was removed outside the scene configuration; delete the routine and create it again"}

func isRoutineGone(err error) bool { return errors.Is(err, errRoutineGone) }

// loadRoutineAutopilot loads the autopilot and its single trigger.
func (h *Handler) loadRoutineAutopilot(ctx context.Context, routine contextcap.Routine) (db.Autopilot, db.AutopilotTrigger, error) {
	ap, err := h.Queries.GetAutopilotInWorkspace(ctx, db.GetAutopilotInWorkspaceParams{ID: parseUUID(routine.AutopilotID), WorkspaceID: parseUUID(routine.WorkspaceID)})
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && ap.Status == "archived") {
		return db.Autopilot{}, db.AutopilotTrigger{}, errRoutineGone
	}
	if err != nil {
		return db.Autopilot{}, db.AutopilotTrigger{}, err
	}
	triggers, err := h.Queries.ListAutopilotTriggers(ctx, ap.ID)
	if err != nil {
		return db.Autopilot{}, db.AutopilotTrigger{}, err
	}
	for _, trigger := range triggers {
		if trigger.Kind == sceneRoutineTriggerCron || trigger.Kind == sceneRoutineTriggerHook {
			return ap, trigger, nil
		}
	}
	return ap, db.AutopilotTrigger{}, errRoutineGone
}

func (h *Handler) sceneRoutineView(ctx context.Context, routine contextcap.Routine, ap *db.Autopilot, trigger *db.AutopilotTrigger) (sceneRoutineView, error) {
	if ap == nil || trigger == nil {
		loadedAP, loadedTrigger, err := h.loadRoutineAutopilot(ctx, routine)
		if err != nil {
			return sceneRoutineView{}, err
		}
		ap, trigger = &loadedAP, &loadedTrigger
	}
	view := sceneRoutineView{
		ID: routine.ID, SceneID: routine.SceneID, SceneKind: routine.SceneKind, AutopilotID: routine.AutopilotID,
		Title: ap.Title, Instructions: ap.Description.String, Enabled: ap.Status == "active",
		PauseReason: ap.PauseReason.String, CreatedByType: routine.CreatedByType,
		CreatedAt: routine.CreatedAt.UTC().Format(time.RFC3339), UpdatedAt: timestampToString(ap.UpdatedAt),
		Trigger: sceneRoutineTriggerView{ID: util.UUIDToString(trigger.ID), Kind: trigger.Kind, NextRuns: []string{}},
	}
	if trigger.Kind == sceneRoutineTriggerCron {
		view.Trigger.Cron, view.Trigger.Timezone = trigger.CronExpression.String, trigger.Timezone.String
		if view.Enabled {
			view.Trigger.NextRunAt = timestampToPtr(trigger.NextRunAt)
			if runs, err := service.NextOccurrencesAfterUTC(view.Trigger.Cron, view.Trigger.Timezone, time.Now(), sceneRoutinePreviewCount); err == nil {
				for _, run := range runs {
					view.Trigger.NextRuns = append(view.Trigger.NextRuns, run.Format(time.RFC3339))
				}
			}
		}
	} else {
		view.Trigger.WebhookURLMasked = h.routineWebhookURLMasked(*trigger)
	}
	runs, err := h.Queries.ListAutopilotRuns(ctx, db.ListAutopilotRunsParams{AutopilotID: ap.ID, Limit: 1})
	if err != nil {
		return sceneRoutineView{}, err
	}
	if len(runs) > 0 {
		view.LastRun = routineRunView(runs[0])
	}
	return view, nil
}

// sceneRoutineRunHistoryLimit is how many runs a routine's history lists.
const sceneRoutineRunHistoryLimit = 30

// listSceneRoutineRuns lists a routine's newest runs (status and times
// only, no run detail).
func (h *Handler) listSceneRoutineRuns(ctx context.Context, routine contextcap.Routine) ([]sceneRoutineRunView, error) {
	ap, _, err := h.loadRoutineAutopilot(ctx, routine)
	if err != nil {
		return nil, err
	}
	runs, err := h.Queries.ListAutopilotRuns(ctx, db.ListAutopilotRunsParams{AutopilotID: ap.ID, Limit: sceneRoutineRunHistoryLimit})
	if err != nil {
		return nil, err
	}
	out := make([]sceneRoutineRunView, 0, len(runs))
	for _, run := range runs {
		out = append(out, *routineRunView(run))
	}
	return out, nil
}

func routineRunView(run db.AutopilotRun) *sceneRoutineRunView {
	return &sceneRoutineRunView{
		ID: util.UUIDToString(run.ID), Status: run.Status, Source: run.Source,
		FailureReason: run.FailureReason.String, CreatedAt: timestampToString(run.CreatedAt),
		CompletedAt: timestampToPtr(run.CompletedAt),
	}
}

func (h *Handler) routineWebhookURL(trigger db.AutopilotTrigger) string {
	if trigger.Kind != sceneRoutineTriggerHook || !trigger.WebhookToken.Valid || trigger.WebhookToken.String == "" {
		return ""
	}
	return h.currentConfig().PublicURL + webhookPathForToken(trigger.WebhookToken.String)
}

// routineWebhookURLMasked shows the URL with only the token's last four
// characters, enough to tell two URLs apart without exposing the secret.
func (h *Handler) routineWebhookURLMasked(trigger db.AutopilotTrigger) string {
	full := h.routineWebhookURL(trigger)
	token := trigger.WebhookToken.String
	if full == "" || len(token) < 8 {
		return ""
	}
	return strings.TrimSuffix(full, token) + token[:4] + "…" + token[len(token)-4:]
}

func routineEnabledLabel(enabled bool) string {
	if enabled {
		return "运行中"
	}
	return "已暂停"
}

func routineCreatedMessage(view sceneRoutineView) string {
	if view.Trigger.Kind == sceneRoutineTriggerHook {
		return fmt.Sprintf("已在这个场域创建例行任务「%s」，由 Webhook 触发。完整的 Webhook 地址不会发在会话里：请智能体管理员在配置页的「例行任务」里点「重新生成 Webhook 地址」获取并复制。", view.Title)
	}
	next := "暂无"
	if len(view.Trigger.NextRuns) > 0 {
		if at, err := time.Parse(time.RFC3339, view.Trigger.NextRuns[0]); err == nil {
			next = routineLocalTime(at, view.Trigger.Timezone)
		}
	}
	return fmt.Sprintf("已在这个场域创建例行任务「%s」，按「%s」（%s）运行，下次运行：%s。每次开始和结束都会在这里发一条消息。",
		view.Title, view.Trigger.Cron, view.Trigger.Timezone, next)
}

func routineLocalTime(at time.Time, timezone string) string {
	if loc, err := time.LoadLocation(timezone); err == nil {
		at = at.In(loc)
	}
	return at.Format("2006-01-02 15:04")
}

// ── Run context and notices (service.SceneRoutines) ─────────────────────────

var _ service.SceneRoutines = (*Handler)(nil)

// sceneRoutineContext is the scene_routine task context key: the binding a
// run carries next to agent_scene.
type sceneRoutineContext struct {
	RoutineID   string `json:"routine_id"`
	TenantOrgID string `json:"tenant_org_id"`
	Kind        string `json:"kind"`
	Title       string `json:"title"`
}

// RoutineRuntimeContext implements service.SceneRoutines: the scene a run of
// ap carries, after checking the scene is still the agent's in an org the
// agent is bound to. It never falls back to another scene.
func (h *Handler) RoutineRuntimeContext(ctx context.Context, ap db.Autopilot) ([]byte, error) {
	routine, err := contextcap.GetRoutineByAutopilot(ctx, h.DB, util.UUIDToString(ap.ID))
	if errors.Is(err, contextcap.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := h.routineSceneUsable(ctx, routine); err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{
		protocol.AgentSceneContextKey: scene.Ref{SceneID: routine.SceneID},
		protocol.SceneRoutineContextKey: sceneRoutineContext{
			RoutineID: routine.ID, TenantOrgID: routine.TenantOrgID, Kind: routine.SceneKind, Title: ap.Title,
		},
	})
}

// routineSceneUsable is the use-time fence of a routine's scene: it still
// exists for the agent and the agent is still bound in its org.
func (h *Handler) routineSceneUsable(ctx context.Context, routine contextcap.Routine) error {
	owner := scene.Owner{WorkspaceID: parseUUID(routine.WorkspaceID), AgentID: parseUUID(routine.AgentID)}
	sc, err := scene.Get(ctx, h.Queries, owner, parseUUID(routine.SceneID))
	if errors.Is(err, scene.ErrNotFound) {
		return fmt.Errorf("%w: the routine's scene is gone", service.ErrSceneRoutineUnusable)
	}
	if err != nil {
		return err
	}
	identity, err := h.Queries.GetAgentDingTalkIdentity(ctx, db.GetAgentDingTalkIdentityParams{WorkspaceID: owner.WorkspaceID, AgentID: owner.AgentID})
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: the agent has no DingTalk identity", service.ErrSceneRoutineUnusable)
	}
	if err != nil {
		return err
	}
	if err := scene.CheckTenant(sc, identity.OrgID); err != nil || sc.TenantOrgID != routine.TenantOrgID {
		return fmt.Errorf("%w: the agent is no longer bound in the routine's org", service.ErrSceneRoutineUnusable)
	}
	return nil
}

// RoutineTaskQueued implements service.SceneRoutines: the start notice.
func (h *Handler) RoutineTaskQueued(ctx context.Context, ap db.Autopilot, run db.AutopilotRun, task db.AgentTaskQueue) {
	if h.DingTalkResponses == nil || h.TxStarter == nil {
		return
	}
	routine, err := contextcap.GetRoutineByAutopilot(ctx, h.DB, util.UUIDToString(ap.ID))
	if err != nil {
		if !errors.Is(err, contextcap.ErrNotFound) {
			slog.WarnContext(ctx, "scene routine start notice: routine lookup failed", "run_id", util.UUIDToString(run.ID), "error", err)
		}
		return
	}
	in, err := h.routineNoticeInput(ctx, h.Queries, routine, routineStartText(ap.Title, run, h.routineTimezone(ctx, run)))
	if err != nil {
		slog.WarnContext(ctx, "scene routine start notice skipped", "run_id", util.UUIDToString(run.ID), "routine_id", routine.ID, "error", err)
		return
	}
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		slog.WarnContext(ctx, "scene routine start notice: begin failed", "run_id", util.UUIDToString(run.ID), "error", err)
		return
	}
	defer tx.Rollback(ctx)
	if _, err := h.DingTalkResponses.EnqueueRoutineNotice(ctx, tx, in, util.UUIDToString(run.ID), dingtalkresponse.RoutineNoticeStart); err != nil {
		slog.WarnContext(ctx, "scene routine start notice: enqueue failed", "run_id", util.UUIDToString(run.ID), "error", err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		slog.WarnContext(ctx, "scene routine start notice: commit failed", "run_id", util.UUIDToString(run.ID), "error", err)
		return
	}
	h.DingTalkResponses.Notify()
}

// RoutineTaskFinished implements service.SceneRoutines: the end notice,
// enqueued in the task's terminal transaction under a savepoint, so a failed
// notice never aborts the terminal transition. Only a failure to open or
// release the savepoint is returned; an unusable scene or identity skips the
// notice.
func (h *Handler) RoutineTaskFinished(ctx context.Context, tx pgx.Tx, task db.AgentTaskQueue, status string, result []byte, errMessage string) error {
	if h.DingTalkResponses == nil || !task.AutopilotRunID.Valid {
		return nil
	}
	if tx == nil {
		if err := h.enqueueRoutineEndNotice(ctx, h.Queries, h.DB, task, status, result, errMessage); err != nil {
			slog.WarnContext(ctx, "scene routine end notice failed", "task_id", util.UUIDToString(task.ID), "error", err)
		}
		return nil
	}
	savepoint, err := tx.Begin(ctx)
	if err != nil {
		return err
	}
	if err := h.enqueueRoutineEndNotice(ctx, h.Queries.WithTx(savepoint), savepoint, task, status, result, errMessage); err != nil {
		slog.WarnContext(ctx, "scene routine end notice failed", "task_id", util.UUIDToString(task.ID), "error", err)
		return savepoint.Rollback(ctx)
	}
	return savepoint.Commit(ctx)
}

// RoutineTaskSettled implements service.SceneRoutines: the end notice of a
// run whose task ended outside a completion transaction (cancel, the
// stale-task sweeper, a runtime that failed to start). It runs after the
// terminal transition committed, so a notice enqueued there is already
// visible and is left alone.
func (h *Handler) RoutineTaskSettled(ctx context.Context, task db.AgentTaskQueue) {
	if h.DingTalkResponses == nil || h.DB == nil || !task.AutopilotRunID.Valid {
		return
	}
	switch task.Status {
	case "completed", "failed", "cancelled":
	default:
		return
	}
	runID := util.UUIDToString(task.AutopilotRunID)
	run, err := h.Queries.GetAutopilotRun(ctx, task.AutopilotRunID)
	if err != nil {
		return
	}
	routine, err := contextcap.GetRoutineByAutopilot(ctx, h.DB, util.UUIDToString(run.AutopilotID))
	if err != nil {
		return
	}
	var settled bool
	err = h.DB.QueryRow(ctx, `SELECT
			EXISTS (SELECT 1 FROM agent_task_queue WHERE autopilot_run_id = $1::uuid AND status IN ('queued', 'dispatched', 'running'))
			OR EXISTS (SELECT 1 FROM response_action WHERE id = $2)`,
		runID, dingtalkresponse.StableActionID(routine.WorkspaceID, routine.AgentID,
			dingtalkresponse.RoutineNoticeRequestID(runID, dingtalkresponse.RoutineNoticeEnd), "message.send")).Scan(&settled)
	if err != nil {
		slog.WarnContext(ctx, "scene routine end notice: settle check failed", "run_id", runID, "error", err)
		return
	}
	if settled {
		return
	}
	if err := h.enqueueRoutineEndNotice(ctx, h.Queries, h.DB, task, task.Status, task.Result, task.Error.String); err != nil {
		slog.WarnContext(ctx, "scene routine end notice failed", "run_id", runID, "error", err)
	}
}

// enqueueRoutineEndNotice enqueues the end notice of the routine run of task
// through q/exec (a transaction or the pool).
func (h *Handler) enqueueRoutineEndNotice(ctx context.Context, q *db.Queries, exec contextcap.DBTX, task db.AgentTaskQueue, status string, result []byte, errMessage string) error {
	run, err := q.GetAutopilotRun(ctx, task.AutopilotRunID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	routine, err := contextcap.GetRoutineByAutopilot(ctx, exec, util.UUIDToString(run.AutopilotID))
	if errors.Is(err, contextcap.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	title := routineContextTitle(task.Context)
	in, err := h.routineNoticeInput(ctx, q, routine, routineEndText(title, task, status, result, errMessage))
	if err != nil {
		slog.WarnContext(ctx, "scene routine end notice skipped", "run_id", util.UUIDToString(run.ID), "routine_id", routine.ID, "error", err)
		return nil
	}
	if _, err := h.DingTalkResponses.EnqueueRoutineNotice(ctx, exec, in, util.UUIDToString(run.ID), dingtalkresponse.RoutineNoticeEnd); err != nil {
		return fmt.Errorf("enqueue: %w", err)
	}
	h.DingTalkResponses.Notify()
	return nil
}

// routineNoticeInput addresses a notice to the routine's scene as the agent's
// DingTalk identity: the scene's conversation read back from the directory,
// no @ in a group, the frozen counterpart in a 1:1 chat.
func (h *Handler) routineNoticeInput(ctx context.Context, q *db.Queries, routine contextcap.Routine, text string) (dingtalkresponse.ActionInput, error) {
	owner := scene.Owner{WorkspaceID: parseUUID(routine.WorkspaceID), AgentID: parseUUID(routine.AgentID)}
	sc, err := scene.Get(ctx, q, owner, parseUUID(routine.SceneID))
	if err != nil {
		return dingtalkresponse.ActionInput{}, err
	}
	identity, err := h.routineIdentity(ctx, q, owner.WorkspaceID, owner.AgentID, routine.TenantOrgID)
	if err != nil {
		return dingtalkresponse.ActionInput{}, err
	}
	if err := scene.CheckTenant(sc, identity.OrgID); err != nil {
		return dingtalkresponse.ActionInput{}, err
	}
	in := dingtalkresponse.ActionInput{
		WorkspaceID: routine.WorkspaceID, AgentID: routine.AgentID, DWSUID: identity.DwsUid, DWSOrgID: identity.OrgID,
		SceneID: routine.SceneID, ConversationID: sc.ExternalSceneID, IsGroup: sc.SceneKind == scene.KindGroup,
		Text: text, DWSEnvironment: h.routineDWSEnvironment(ctx, q, owner, identity),
	}
	if !in.IsGroup {
		in.SenderOpenDingTalkID = routine.DeliveryOpenDingTalkID
	}
	if policy, err := q.GetAgentDingTalkResponsePolicy(ctx, owner.AgentID); err == nil {
		in.ShowAITag = policy.DingtalkShowAiTag
	}
	return in, nil
}

// routineDWSEnvironment pins the production gateway for an agent whose
// DingTalk account comes from a native subscription in the same org, as its
// managed replies do.
func (h *Handler) routineDWSEnvironment(ctx context.Context, q *db.Queries, owner scene.Owner, identity db.AgentDingtalkIdentity) string {
	sub, err := q.GetAgentDWSNativeSubscription(ctx, db.GetAgentDWSNativeSubscriptionParams{WorkspaceID: owner.WorkspaceID, AgentID: owner.AgentID})
	if err == nil && sub.OrgID == identity.OrgID {
		return nativeDWSEnvironment
	}
	return ""
}

func (h *Handler) routineTimezone(ctx context.Context, run db.AutopilotRun) string {
	if run.TriggerID.Valid {
		if trigger, err := h.Queries.GetAutopilotTrigger(ctx, run.TriggerID); err == nil && trigger.Timezone.Valid {
			return trigger.Timezone.String
		}
	}
	return sceneRoutineDefaultTimezone
}

func routineContextTitle(raw []byte) string {
	var envelope struct {
		Routine sceneRoutineContext `json:"scene_routine"`
	}
	if json.Unmarshal(raw, &envelope) == nil && strings.TrimSpace(envelope.Routine.Title) != "" {
		return strings.TrimSpace(envelope.Routine.Title)
	}
	return "例行任务"
}

// routineStartText is the start notice: name and what started it, never the
// routine's prompt.
func routineStartText(title string, run db.AutopilotRun, timezone string) string {
	source := "手动运行"
	switch run.Source {
	case "schedule":
		source = "定时触发"
		if run.PlannedAt.Valid {
			source += " · 计划 " + routineLocalTime(run.PlannedAt.Time, timezone)
		}
	case "webhook":
		source = "Webhook 触发"
	}
	return fmt.Sprintf("开始执行例行任务「%s」（%s）", title, source)
}

// routineEndText is the end notice: the outcome, how long it took and the
// clipped final output.
func routineEndText(title string, task db.AgentTaskQueue, status string, result []byte, errMessage string) string {
	elapsed := ""
	if task.StartedAt.Valid {
		elapsed = "，用时 " + routineDuration(time.Since(task.StartedAt.Time))
	}
	switch status {
	case "completed":
		var payload protocol.TaskCompletedPayload
		output := ""
		if json.Unmarshal(result, &payload) == nil {
			output = strings.TrimSpace(redact.Text(util.UnescapeBackslashEscapes(payload.Output)))
		}
		if output == "" {
			return fmt.Sprintf("例行任务「%s」已完成%s，没有输出内容。", title, elapsed)
		}
		if runes := []rune(output); len(runes) > sceneRoutineNoticeMax {
			output = string(runes[:sceneRoutineNoticeMax]) + "\n\n（内容过长已截断，完整结果见例行任务的运行记录）"
		}
		return fmt.Sprintf("例行任务「%s」已完成%s：\n\n%s", title, elapsed, output)
	case "cancelled", "canceled":
		return fmt.Sprintf("例行任务「%s」已取消。", title)
	default:
		reason := strings.TrimSpace(redact.Text(errMessage))
		if reason == "" {
			reason = "执行失败"
		}
		if runes := []rune(reason); len(runes) > 300 {
			reason = string(runes[:300]) + "…"
		}
		return fmt.Sprintf("例行任务「%s」没有完成%s：%s", title, elapsed, reason)
	}
}

func routineDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%d 秒", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%d 分 %d 秒", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%d 小时 %d 分", int(d.Hours()), int(d.Minutes())%60)
}
