package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/contextcap"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/service/dingtalkresponse"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// routineFixture is a ctxcap fixture whose handler can create, dispatch and
// notify scene routines.
func routineFixture(t *testing.T) (*ctxcapFixture, contextCapAgent) {
	t.Helper()
	f := newCtxcapFixture(t)
	f.h.Bus = events.New()
	f.h.DingTalkResponses = dingtalkresponse.NewService(testPool, nil, nil)
	f.h.AutopilotService = service.NewAutopilotService(f.h.Queries, testPool, f.h.Bus, testHandler.TaskService)
	f.h.AutopilotService.SceneRoutines = f.h
	agentID := uuidToString(f.agent)
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = testPool.Exec(bg, `DELETE FROM response_action WHERE agent_id = $1`, agentID)
		_, _ = testPool.Exec(bg, `DELETE FROM context_scope_routine WHERE agent_id = $1`, agentID)
		_, _ = testPool.Exec(bg, `DELETE FROM agent_task_queue WHERE agent_id = $1`, agentID)
		_, _ = testPool.Exec(bg, `DELETE FROM autopilot_run WHERE autopilot_id IN (SELECT id FROM autopilot WHERE assignee_id = $1)`, agentID)
		_, _ = testPool.Exec(bg, `DELETE FROM autopilot_trigger WHERE autopilot_id IN (SELECT id FROM autopilot WHERE assignee_id = $1)`, agentID)
		_, _ = testPool.Exec(bg, `DELETE FROM autopilot_rule_version WHERE autopilot_id IN (SELECT id FROM autopilot WHERE assignee_id = $1)`, agentID)
		_, _ = testPool.Exec(bg, `DELETE FROM autopilot WHERE assignee_id = $1`, agentID)
	})
	return f, contextCapAgent{ID: agentID, WorkspaceID: testWorkspaceID, OrgID: ctxcapOrg, IdentityOrgID: ctxcapOrg}
}

func routineMember() sceneRoutineActor {
	return memberRoutineActor(testUserID)
}

func routineCode(err error) string {
	var refusal *sceneRoutineError
	if errors.As(err, &refusal) {
		return refusal.Code
	}
	return ""
}

func createGroupRoutine(t *testing.T, f *ctxcapFixture, a contextCapAgent, in sceneRoutineInput) sceneRoutineResult {
	t.Helper()
	ctx := context.Background()
	sc, err := f.h.sceneRoutineScene(ctx, a, ctxcapScene)
	if err != nil {
		t.Fatalf("scene: %v", err)
	}
	result, err := f.h.createSceneRoutine(ctx, a, sc, routineMember(), sceneRoutineCounterpart{}, in)
	if err != nil {
		t.Fatalf("create routine: %v", err)
	}
	return result
}

// A schedule routine is a run_only autopilot of the agent with one trigger,
// bound to its scene; the timezone defaults to Asia/Shanghai and the view
// previews the next runs. Re-registering the same purpose and schedule
// updates it and never re-enables a paused routine.
func TestSceneRoutineCreateDedupeKeepsPause(t *testing.T) {
	f, a := routineFixture(t)
	ctx := context.Background()

	created := createGroupRoutine(t, f, a, sceneRoutineInput{
		Title: "Daily standup reminder", Instructions: "Remind the group about standup.",
		Trigger: sceneRoutineTrigger{Kind: "schedule", Cron: "0  9 * * 1-5"},
	})
	view := created.Routine
	if created.Updated || view.SceneID != ctxcapScene || view.SceneKind != "group" || !view.Enabled {
		t.Fatalf("created = %+v", created)
	}
	if view.Trigger.Kind != "schedule" || view.Trigger.Cron != "0 9 * * 1-5" || view.Trigger.Timezone != "Asia/Shanghai" || len(view.Trigger.NextRuns) != 3 {
		t.Fatalf("trigger = %+v", view.Trigger)
	}
	ap, err := f.h.Queries.GetAutopilot(ctx, parseUUID(view.AutopilotID))
	if err != nil {
		t.Fatal(err)
	}
	if ap.ExecutionMode != "run_only" || uuidToString(ap.AssigneeID) != a.ID || ap.Description.String != "Remind the group about standup." {
		t.Fatalf("autopilot = %+v", ap)
	}

	paused := false
	if _, err := f.h.updateSceneRoutine(ctx, a, mustRoutine(t, f, a, view.ID), routineMember(), sceneRoutinePatch{Enabled: &paused}); err != nil {
		t.Fatal(err)
	}
	again := createGroupRoutine(t, f, a, sceneRoutineInput{
		Title: "reminder: daily STANDUP", Instructions: "Remind everyone, with the agenda.",
		Trigger: sceneRoutineTrigger{Kind: "schedule", Cron: "0 9 * * 1-5", Timezone: "Asia/Shanghai"},
	})
	if !again.Updated || again.Routine.ID != view.ID || again.Routine.Enabled || again.Routine.Instructions != "Remind everyone, with the agenda." || again.TellTheHuman == "" {
		t.Fatalf("re-registration = %+v", again)
	}
	other := createGroupRoutine(t, f, a, sceneRoutineInput{
		Title: "Daily standup reminder", Instructions: "Weekend edition.",
		Trigger: sceneRoutineTrigger{Kind: "schedule", Cron: "0 10 * * 6"},
	})
	if other.Updated || other.Routine.ID == view.ID {
		t.Fatalf("another schedule must be another routine: %+v", other)
	}
	routines, err := f.h.listSceneRoutines(ctx, a, ctxcapScene)
	if err != nil || len(routines) != 2 {
		t.Fatalf("list = %d %v", len(routines), err)
	}

	sc, _ := f.h.sceneRoutineScene(ctx, a, ctxcapScene)
	for name, in := range map[string]sceneRoutineInput{
		"too frequent":  {Title: "x", Instructions: "y", Trigger: sceneRoutineTrigger{Kind: "schedule", Cron: "*/5 * * * *"}},
		"bad cron":      {Title: "x", Instructions: "y", Trigger: sceneRoutineTrigger{Kind: "schedule", Cron: "not cron"}},
		"bad timezone":  {Title: "x", Instructions: "y", Trigger: sceneRoutineTrigger{Kind: "schedule", Cron: "0 9 * * *", Timezone: "Mars/Base"}},
		"no title":      {Instructions: "y", Trigger: sceneRoutineTrigger{Kind: "webhook"}},
		"unknown kind":  {Title: "x", Instructions: "y", Trigger: sceneRoutineTrigger{Kind: "api"}},
		"webhook w/ tz": {Title: "x", Instructions: "y", Trigger: sceneRoutineTrigger{Kind: "webhook", Timezone: "UTC"}},
	} {
		if _, err := f.h.createSceneRoutine(ctx, a, sc, routineMember(), sceneRoutineCounterpart{}, in); routineCode(err) != "invalid_routine" {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func mustRoutine(t *testing.T, f *ctxcapFixture, a contextCapAgent, id string) contextcap.Routine {
	t.Helper()
	routine, err := f.h.loadSceneRoutine(context.Background(), a, id)
	if err != nil {
		t.Fatal(err)
	}
	return routine
}

// A webhook routine shows its full URL once, a masked one afterwards, and
// rotating mints a new URL.
func TestSceneRoutineWebhookURLShownOnceAndRotates(t *testing.T) {
	f, a := routineFixture(t)
	ctx := context.Background()
	created := createGroupRoutine(t, f, a, sceneRoutineInput{
		Title: "Deploy finished", Instructions: "Summarize the deploy payload.", Trigger: sceneRoutineTrigger{Kind: "webhook"},
	})
	full := created.Routine.Trigger.WebhookURL
	if !strings.HasPrefix(full, "https://multica.example/api/webhooks/autopilots/awt_") || created.Routine.Trigger.WebhookURLMasked == "" ||
		strings.Contains(created.Routine.Trigger.WebhookURLMasked, strings.TrimPrefix(full, "https://multica.example/api/webhooks/autopilots/")) {
		t.Fatalf("webhook trigger = %+v", created.Routine.Trigger)
	}
	routines, err := f.h.listSceneRoutines(ctx, a, ctxcapScene)
	if err != nil || len(routines) != 1 || routines[0].Trigger.WebhookURL != "" {
		t.Fatalf("listed webhook must be masked: %+v %v", routines, err)
	}
	rotated, err := f.h.rotateSceneRoutineWebhook(ctx, mustRoutine(t, f, a, created.Routine.ID), routineMember())
	if err != nil || rotated.Trigger.WebhookURL == "" || rotated.Trigger.WebhookURL == full {
		t.Fatalf("rotate = %+v %v", rotated.Trigger, err)
	}
	if _, err := f.h.updateSceneRoutine(ctx, a, mustRoutine(t, f, a, created.Routine.ID), routineMember(), sceneRoutinePatch{Cron: ptr("0 9 * * *")}); routineCode(err) != "invalid_routine" {
		t.Fatalf("a webhook has no cron: %v", err)
	}
}

// A run carries the routine's scene and frozen binding; the claim resolves
// it to the scene layer in the routine's org, never a person for a group.
// A routine whose org the agent left is unusable and runs nowhere.
func TestSceneRoutineRunCarriesItsScene(t *testing.T) {
	f, a := routineFixture(t)
	ctx := context.Background()
	created := createGroupRoutine(t, f, a, sceneRoutineInput{
		Title: "Weekly digest", Instructions: "Digest the week.", Trigger: sceneRoutineTrigger{Kind: "schedule", Cron: "0 18 * * 5"},
	})
	ap, err := f.h.Queries.GetAutopilot(ctx, parseUUID(created.Routine.AutopilotID))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := f.h.RoutineRuntimeContext(ctx, ap)
	if err != nil || !service.IsSceneRoutineContext(raw) {
		t.Fatalf("runtime context = %s %v", raw, err)
	}
	var payload map[string]json.RawMessage
	_ = json.Unmarshal(raw, &payload)
	if !strings.Contains(string(payload[protocol.AgentSceneContextKey]), ctxcapScene) {
		t.Fatalf("agent_scene = %s", payload[protocol.AgentSceneContextKey])
	}
	task := f.task(t, raw)
	scope, skip := f.h.resolveTaskContextScope(ctx, f.ws, task)
	if skip != "" || scope.SceneID != ctxcapScene || scope.OrgID != ctxcapOrg || scope.PersonKey != "" {
		t.Fatalf("scope = %+v skip %q", scope, skip)
	}

	other, err := f.h.Queries.GetAutopilot(ctx, parseUUID(created.Routine.AutopilotID))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(ctx, `UPDATE agent_dingtalk_identity SET org_id = 'org-elsewhere' WHERE agent_id = $1`, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.h.RoutineRuntimeContext(ctx, other); !errors.Is(err, service.ErrSceneRoutineUnusable) {
		t.Fatalf("rebound agent: err = %v", err)
	}
	run, _, err := f.h.AutopilotService.DispatchAutopilotManual(ctx, other, pgtype.UUID{}, nil, parseUUID(testUserID))
	if err != nil || run == nil || run.Status != "skipped" || !strings.Contains(run.FailureReason.String, "no longer bound") {
		t.Fatalf("run of an unusable routine = %+v %v", run, err)
	}
}

// The Host notices go to the routine's scene: no callback, no @ in a group,
// one action per run and phase; the end notice carries the clipped output.
func TestSceneRoutineNoticesAreIdempotentSceneSends(t *testing.T) {
	f, a := routineFixture(t)
	ctx := context.Background()
	created := createGroupRoutine(t, f, a, sceneRoutineInput{
		Title: "Morning brief", Instructions: "Brief the group.", Trigger: sceneRoutineTrigger{Kind: "schedule", Cron: "30 8 * * *"},
	})
	ap, err := f.h.Queries.GetAutopilot(ctx, parseUUID(created.Routine.AutopilotID))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := f.h.RoutineRuntimeContext(ctx, ap)
	if err != nil {
		t.Fatal(err)
	}
	run, err := f.h.Queries.CreateAutopilotRun(ctx, db.CreateAutopilotRunParams{AutopilotID: ap.ID, Source: "schedule", Status: "running", RuntimeContext: raw})
	if err != nil {
		t.Fatal(err)
	}
	task := f.task(t, raw)
	task.AutopilotRunID = run.ID

	f.h.RoutineTaskQueued(ctx, ap, run, task)
	f.h.RoutineTaskQueued(ctx, ap, run, task)
	result, _ := json.Marshal(protocol.TaskCompletedPayload{Output: "Three items today."})
	for i := 0; i < 2; i++ {
		if err := f.h.RoutineTaskFinished(ctx, nil, task, "completed", result, ""); err != nil {
			t.Fatal(err)
		}
	}

	rows, err := testPool.Query(ctx, `SELECT request_id, input FROM response_action WHERE agent_id = $1 ORDER BY request_id`, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []dingtalkresponse.ActionInput
	for rows.Next() {
		var requestID string
		var in dingtalkresponse.ActionInput
		var input []byte
		if err := rows.Scan(&requestID, &input); err != nil {
			t.Fatal(err)
		}
		_ = json.Unmarshal(input, &in)
		got = append(got, in)
	}
	runID := uuidToString(run.ID)
	if len(got) != 2 || got[0].RequestID != "routine:"+runID+":end" || got[1].RequestID != "routine:"+runID+":start" {
		t.Fatalf("notices = %+v", got)
	}
	for _, in := range got {
		if in.CallbackURL != "" || in.TaskID != "" || in.SceneID != ctxcapScene || in.ConversationID != ctxcapSceneCID ||
			!in.IsGroup || in.SenderOpenDingTalkID != "" || in.RoutineRunID != runID || in.DWSOrgID != ctxcapOrg {
			t.Fatalf("notice input = %+v", in)
		}
	}
	if !strings.Contains(got[1].Text, "开始执行例行任务「Morning brief」") || strings.Contains(got[1].Text, "Brief the group.") {
		t.Fatalf("start text = %q", got[1].Text)
	}
	if !strings.Contains(got[0].Text, "已完成") || !strings.Contains(got[0].Text, "Three items today.") {
		t.Fatalf("end text = %q", got[0].Text)
	}
}

// A dm routine needs the counterpart it sends to; with it the notices go to
// that person in the 1:1 chat.
func TestSceneRoutineDMNeedsItsCounterpart(t *testing.T) {
	f, a := routineFixture(t)
	f.registerDirectScene(t)
	ctx := context.Background()
	sc, err := f.h.sceneRoutineScene(ctx, a, ctxcapDirectScene)
	if err != nil {
		t.Fatal(err)
	}
	in := sceneRoutineInput{Title: "Nightly check", Instructions: "Check my calendar.", Trigger: sceneRoutineTrigger{Kind: "schedule", Cron: "0 21 * * *"}}
	if _, err := f.h.createSceneRoutine(ctx, a, sc, routineMember(), sceneRoutineCounterpart{}, in); routineCode(err) != "dm_target_unknown" {
		t.Fatalf("no counterpart: %v", err)
	}
	if _, err := f.h.sceneRoutineDMCounterpart(ctx, a, ctxcapDirectScene); routineCode(err) != "dm_target_unknown" {
		t.Fatalf("no inbound message yet: %v", err)
	}
	created, err := f.h.createSceneRoutine(ctx, a, sc, routineMember(), sceneRoutineCounterpart{OpenDingTalkID: "$:LWCP_v1:$alice", StaffID: ctxcapStaff}, in)
	if err != nil || created.Routine.SceneKind != "dm" {
		t.Fatalf("dm routine = %+v %v", created, err)
	}
	routine := mustRoutine(t, f, a, created.Routine.ID)
	notice, err := f.h.routineNoticeInput(ctx, f.h.Queries, routine, "hello")
	if err != nil || notice.IsGroup || notice.SenderOpenDingTalkID != "$:LWCP_v1:$alice" || notice.ConversationID != ctxcapCoordinatorDirect {
		t.Fatalf("dm notice = %+v %v", notice, err)
	}
	ap, _ := f.h.Queries.GetAutopilot(ctx, parseUUID(created.Routine.AutopilotID))
	raw, err := f.h.RoutineRuntimeContext(ctx, ap)
	if err != nil {
		t.Fatal(err)
	}
	if scope := contextcap.ScopeFromTaskContext(raw); scope.SceneID != ctxcapDirectScene || scope.PersonKey != ctxcapStaff {
		t.Fatalf("dm scope = %+v", scope)
	}
}

// A scene routine's autopilot is changed from the scene, not the autopilot
// page; deleting the routine archives its autopilot.
func TestSceneRoutineAutopilotIsManagedByTheScene(t *testing.T) {
	f, a := routineFixture(t)
	ctx := context.Background()
	created := createGroupRoutine(t, f, a, sceneRoutineInput{
		Title: "Managed", Instructions: "x", Trigger: sceneRoutineTrigger{Kind: "schedule", Cron: "0 7 * * *"},
	})
	ap, err := f.h.Queries.GetAutopilot(ctx, parseUUID(created.Routine.AutopilotID))
	if err != nil {
		t.Fatal(err)
	}
	req := newRequest(http.MethodPatch, "/api/autopilots/"+created.Routine.AutopilotID, nil)
	w := httptest.NewRecorder()
	if testHandler.requireAutopilotWrite(w, req, ap, testWorkspaceID) || w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "managed_by_scene") {
		t.Fatalf("autopilot write guard = %d %s", w.Code, w.Body.String())
	}
	if err := f.h.deleteSceneRoutine(ctx, a, mustRoutine(t, f, a, created.Routine.ID), routineMember()); err != nil {
		t.Fatal(err)
	}
	if archived, _ := f.h.Queries.GetAutopilot(ctx, ap.ID); archived.Status != "archived" {
		t.Fatalf("autopilot status = %s", archived.Status)
	}
	if _, err := contextcap.GetRoutineByAutopilot(ctx, testPool, created.Routine.AutopilotID); !errors.Is(err, contextcap.ErrNotFound) {
		t.Fatalf("routine row: %v", err)
	}
}

// The admin routes: managers create and list a scene's routines; a plain
// member is refused; routines exist on scene nodes only.
func TestSceneRoutineAdminRoutes(t *testing.T) {
	f, _ := routineFixture(t)
	router := chi.NewRouter()
	router.Route("/api/agents/{id}/tenants", func(r chi.Router) {
		r.Use(RequireHumanActor)
		r.Get("/{orgId}/context/{scopeType}/{scopeKey}/routines", f.h.ListAgentContextRoutines)
		r.Post("/{orgId}/context/{scopeType}/{scopeKey}/routines", f.h.CreateAgentContextRoutine)
		r.Patch("/{orgId}/context/{scopeType}/{scopeKey}/routines/{routineId}", f.h.UpdateAgentContextRoutine)
	})
	agentID := uuidToString(f.agent)
	base := ctxNodePath(agentID, ctxcapOrg, contextcap.ScopeScene, ctxcapScene) + "/routines"
	body := map[string]any{"title": "Admin routine", "instructions": "Do it.", "trigger": map[string]any{"kind": "schedule", "cron": "0 12 * * *"}}

	w := scenesAs(t, router, "", http.MethodPost, base, body)
	ctxcapExpectStatus(t, w, http.StatusCreated, "create")
	var created sceneRoutineResult
	ctxcapDecode(t, w, &created)
	w = scenesAs(t, router, "", http.MethodPost, base, body)
	ctxcapExpectStatus(t, w, http.StatusOK, "re-register")

	member := createPermissionTestMember(t, "routine-member-"+uuid.NewString()[:8]+"@example.test")
	t.Cleanup(func() { _, _ = testPool.Exec(context.Background(), `DELETE FROM member WHERE user_id = $1`, member) })
	ctxcapExpectStatus(t, scenesAs(t, router, member, http.MethodPost, base, body), http.StatusForbidden, "plain member create")
	ctxcapExpectStatus(t, scenesAs(t, router, member, http.MethodPatch, base+"/"+created.Routine.ID, map[string]any{"enabled": false}), http.StatusForbidden, "plain member pause")

	w = scenesAs(t, router, "", http.MethodGet, base, nil)
	ctxcapExpectStatus(t, w, http.StatusOK, "list")
	var listed struct {
		Routines []sceneRoutineView `json:"routines"`
	}
	ctxcapDecode(t, w, &listed)
	if len(listed.Routines) != 1 || listed.Routines[0].ID != created.Routine.ID {
		t.Fatalf("listed = %+v", listed)
	}
	orgPath := ctxNodePath(agentID, ctxcapOrg, contextcap.ScopeOrg, ctxcapOrg) + "/routines"
	ctxcapExpectStatus(t, scenesAs(t, router, "", http.MethodPost, orgPath, body), http.StatusBadRequest, "org node")
}
