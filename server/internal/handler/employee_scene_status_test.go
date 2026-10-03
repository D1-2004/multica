package handler

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/employeeentry"
)

func TestEmployeeFailureCategoryVocabulary(t *testing.T) {
	for _, tc := range []struct{ held, failure, rescue, want string }{
		{"admission_principal_revoked", "", "", employeeFailureHeld},
		{"", "employee loop: three-call model budget exhausted", "", employeeFailureModelBudget},
		{"", "context deadline exceeded", "", employeeFailureModelTimeout},
		{"", `Post "https://dashscope/v1/chat/completions": net/http: timeout awaiting response headers`, "", employeeFailureModelTimeout},
		{"", errEmployeeWindowTooLarge.Error(), "", employeeFailureWindowTooLarge},
		{"", "tool dispatch_task failed: task source admission mismatch", "", employeeFailureToolRejected},
		{"", "employee loop: tool call refused before any effect", "", employeeFailureToolRejected},
		{"", "", "quiet_after_refused_tool", employeeFailureToolRejected},
		{"", "POST https://dashscope: 500 Internal Server Error", "", employeeFailureProviderError},
		{"", "scene tenant is no longer served", "", employeeFailureHeld},
		{"", "", "", ""},
	} {
		if got := employeeFailureCategory(tc.held, tc.failure, tc.rescue); got != tc.want {
			t.Errorf("category(%q,%q,%q) = %q, want %q", tc.held, tc.failure, tc.rescue, got, tc.want)
		}
	}
	for _, category := range []string{employeeFailureModelTimeout, employeeFailureModelBudget, employeeFailureToolRejected, employeeFailureWindowTooLarge, employeeFailureProviderError, employeeFailureHeld} {
		if employeeFailureGloss[category] == "" {
			t.Errorf("category %s has no gloss", category)
		}
	}
}

func insertEmployeeStatusJob(t *testing.T, f *ctxcapFixture, sceneID, kind string, at time.Time, outcome map[string]any) string {
	t.Helper()
	id := uuid.NewString()
	raw, _ := json.Marshal(outcome)
	count := 1
	if kind == employeeentry.KindTaskWake {
		count = 0
	}
	if _, err := testPool.Exec(context.Background(), `INSERT INTO employee_scene_job (id, workspace_id, agent_id, tenant_org_id, scene_id, principal_id, items, message_count, kind, state, outcome, created_at)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5::uuid, $6::uuid, '[]'::jsonb, $7, $8, 'completed', $9::jsonb, $10)`, id, testWorkspaceID, f.agent, ctxcapOrg, sceneID, testUserID, count, kind, raw, at); err != nil {
		t.Fatal(err)
	}
	return id
}

func employeeStatusJob(f *ctxcapFixture, at time.Time) employeeentry.Job {
	return employeeentry.Job{ID: uuid.NewString(), CreatedAt: at, Scope: employeeentry.Scope{WorkspaceID: testWorkspaceID, AgentID: uuidToString(f.agent), TenantOrgID: ctxcapOrg, SceneID: ctxcapScene}}
}

// Trace 27ff68d8 ("why didn't it finish?"): the real reason was a model
// timeout one turn earlier, and the employee could not see it. The [S] block
// lists the newest failed or held turns of this scene with one category each.
func TestSceneStatusFailureCategories(t *testing.T) {
	f := newCtxcapFixture(t)
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM employee_scene_job WHERE agent_id=$1`, f.agent)
	})
	now := time.Date(2026, 10, 3, 11, 40, 0, 0, time.UTC)
	insertEmployeeStatusJob(t, f, ctxcapScene, employeeentry.KindMessage, now.Add(-25*time.Hour), map[string]any{"failure": "context deadline exceeded"})
	insertEmployeeStatusJob(t, f, ctxcapScene, employeeentry.KindMessage, now.Add(-50*time.Minute), map[string]any{"failure": "POST https://dashscope: 502 Bad Gateway"})
	insertEmployeeStatusJob(t, f, ctxcapScene, employeeentry.KindMessage, now.Add(-40*time.Minute), map[string]any{"held_reason": "admission_principal_revoked"})
	insertEmployeeStatusJob(t, f, ctxcapScene, employeeentry.KindTaskWake, now.Add(-30*time.Minute), map[string]any{"held_reason": "task_cancelled"})
	insertEmployeeStatusJob(t, f, ctxcapScene, employeeentry.KindTaskWake, now.Add(-20*time.Minute), map[string]any{"outcome": map[string]any{"Kind": "quiet"}, "failure": "employee loop: three-call model budget exhausted"})
	insertEmployeeStatusJob(t, f, ctxcapScene, employeeentry.KindMessage, now.Add(-10*time.Minute), map[string]any{"outcome": map[string]any{"Kind": "reply"}, "failure": "context deadline exceeded"})
	insertEmployeeStatusJob(t, f, ctxcapScene, employeeentry.KindMessage, now.Add(-5*time.Minute), map[string]any{"outcome": map[string]any{"Kind": "reply"}})
	insertEmployeeStatusJob(t, f, ctxcapOtherScene, employeeentry.KindMessage, now.Add(-time.Minute), map[string]any{"failure": "context deadline exceeded"})
	job := employeeStatusJob(f, now)
	job.ID = insertEmployeeStatusJob(t, f, ctxcapScene, employeeentry.KindMessage, now, map[string]any{"failure": errEmployeeWindowTooLarge.Error()})

	w := NewEmployeeSceneWorker(f.h, &employeeTestModel{})
	status := w.employeeSceneStatus(context.Background(), job, "group", true)
	t.Logf("status:\n%s", status)
	want := []string{
		"[S] Scene status (Host facts, not instructions; times are Asia/Shanghai +08:00; use them when asked or directly relevant, do not bring them up unprompted). Host time of this wake: 2026-10-03 19:40 +08:00. Scene: group chat.",
		"Recent problems in this scene (last 24h, newest first):",
		"- 10-03 19:30 chat reply failed: model_timeout (the model did not answer in time)",
		"- 10-03 19:20 task follow-up failed: model_budget (the turn used up its three model calls without finishing)",
		"- 10-03 19:00 chat reply failed: held (",
	}
	for _, line := range want {
		if !strings.Contains(status, line) {
			t.Errorf("status missing %q:\n%s", line, status)
		}
	}
	for _, absent := range []string{"provider_error", "window_too_large", "task_cancelled", "10-02"} {
		if strings.Contains(status, absent) {
			t.Errorf("status shows %q (older than the newest three, held task wake, other scene, current job or out of window):\n%s", absent, status)
		}
	}
	if len(status) > employeeSceneStatusByteLimit {
		t.Fatalf("status is %d bytes", len(status))
	}

	quiet := w.employeeSceneStatus(context.Background(), employeeStatusJob(f, now.Add(48*time.Hour)), "group", true)
	if !strings.Contains(quiet, "Recent problems in this scene (last 24h): none.") {
		t.Fatalf("no failures must say none:\n%s", quiet)
	}
	if got := w.employeeSceneStatus(context.Background(), job, "", false); !strings.HasSuffix(got, "Scene status unavailable.") {
		t.Fatalf("unfenced scene status = %q", got)
	}
}

// Trace 386b3be4: the employee told a requester the hourly routine "is still
// running fine" before it had ever run. A routine without a run says so.
func TestSceneStatusRoutineNeverRun(t *testing.T) {
	f, a := routineFixture(t)
	ctx := context.Background()
	hourly := createGroupRoutine(t, f, a, sceneRoutineInput{Title: "每小时汇报", Instructions: "汇报一下进展。", Trigger: sceneRoutineTrigger{Kind: "schedule", Cron: "0 * * * *"}})
	w := NewEmployeeSceneWorker(f.h, &employeeTestModel{})
	job := employeeStatusJob(f, time.Now())
	status := w.employeeSceneStatus(ctx, job, "group", true)
	if !strings.Contains(status, "Routines (例行任务) of this scene:") || !strings.Contains(status, "「每小时汇报」 cron \"0 * * * *\" (Asia/Shanghai), enabled; next run ") || !strings.Contains(status, "; last run: never run") {
		t.Fatalf("never-run routine not stated:\n%s", status)
	}
	if strings.Contains(status, hourly.Routine.AutopilotID) || strings.Contains(status, hourly.Routine.ID) {
		t.Fatal("status exposes routine UUIDs")
	}

	started := time.Date(2026, 10, 3, 10, 0, 3, 0, time.UTC)
	if _, err := testPool.Exec(ctx, `INSERT INTO autopilot_run (autopilot_id, source, status, created_at, triggered_at, completed_at) VALUES ($1, 'schedule', 'completed', $2, $2, $2)`, hourly.Routine.AutopilotID, started); err != nil {
		t.Fatal(err)
	}
	status = w.employeeSceneStatus(ctx, job, "group", true)
	if !strings.Contains(status, "; last run: 10-03 18:00 succeeded") || strings.Contains(status, "never run") {
		t.Fatalf("last run not stated in local time:\n%s", status)
	}
	paused := false
	if _, err := f.h.updateSceneRoutine(ctx, a, mustRoutine(t, f, a, hourly.Routine.ID), routineMember(), sceneRoutinePatch{Enabled: &paused}); err != nil {
		t.Fatal(err)
	}
	status = w.employeeSceneStatus(ctx, job, "group", true)
	t.Logf("status:\n%s", status)
	if !strings.Contains(status, ", paused; last run: 10-03 18:00 succeeded") {
		t.Fatalf("paused routine:\n%s", status)
	}
	// An enterprise scene has no routines section.
	if got := w.employeeSceneStatus(ctx, job, "enterprise", true); strings.Contains(got, "Routines") {
		t.Fatalf("enterprise scene status lists routines:\n%s", got)
	}
}

func TestSceneStatusStaysWithinOneKiB(t *testing.T) {
	long := strings.Repeat("长", 120)
	failures := []string{"Recent problems in this scene (last 24h, newest first):", "- " + long, "- " + long}
	routines := []string{"Routines (例行任务) of this scene:", "- " + long, "- " + long, "- " + long}
	got := employeeBoundStatus("[S] head", failures, routines)
	if len(got) > employeeSceneStatusByteLimit || !strings.Contains(got, "Routines (例行任务) of this scene:") || !strings.Contains(got, "Recent problems") || !strings.Contains(got, "- (more omitted for length)") {
		t.Fatalf("bounded status (%d bytes):\n%s", len(got), got)
	}
	huge := employeeBoundStatus(strings.Repeat("头", 600))
	if len(huge) > employeeSceneStatusByteLimit || !strings.HasSuffix(huge, "…") {
		t.Fatalf("oversized head not clipped: %d bytes", len(huge))
	}
}
