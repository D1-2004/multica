package handler

import (
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/service/employeeloop"
)

// DS-01 (R1003): "send-side log: sent (200). Did they receive it?" was
// dispatched as a 20-call background check. Evidence in hand is answered
// directly; real outside work is still dispatched, never refused
// (docs/plans/2026-10-03/employee-foreground-boundary.md).
func TestForegroundBoundaryAnswersEvidenceInHand(t *testing.T) {
	f := newCtxcapFixture(t)
	input, err := employeeCapabilityInput(t, f, []DispatchMessage{{OpenMsgID: "ds01", SenderUID: "alice", Text: "发送侧日志：P579 09:00 已发出（200）。对方收到了吗？"}})
	if err != nil {
		t.Fatal(err)
	}
	prompt := employeeloop.BuildPrompt(input.Config.Persona)
	for _, rule := range []string{
		"Answer directly, without dispatching, when what the answer needs is already here",
		"facts, logs, numbers or observations the requester gave you",
		"Judging or explaining evidence in hand is your own work, not a lookup",
		"say exactly what it shows and what it cannot confirm",
		"offer a background check instead of starting one unless the requester asked you to check",
		"messages outside this window and the recent conversation",
		// The approved contract still holds: real outside work is dispatched.
		"When a request needs such work, dispatch it; never answer that you cannot do it",
		"Only a task result can show that something is impossible",
	} {
		if !strings.Contains(prompt, rule) {
			t.Errorf("foreground boundary missing %q", rule)
		}
	}
}

// Foreground scene_config_get routine times are Asia/Shanghai with an
// explicit offset, like every other Host time the model reads.
func TestEmployeeSceneConfigRoutineTimesAreLocal(t *testing.T) {
	next, completed := "2026-10-03T12:00:00Z", "2026-10-03T11:00:05Z"
	view := sceneRoutineView{CreatedAt: "2026-10-03T03:00:00Z", UpdatedAt: "not a time", Trigger: sceneRoutineTriggerView{NextRunAt: &next, NextRuns: []string{"2026-10-03T12:00:00Z", "2026-10-03T13:00:00Z"}}, LastRun: &sceneRoutineRunView{CreatedAt: "2026-10-03T11:00:00Z", CompletedAt: &completed}}
	original := *view.LastRun
	employeeLocalizeRoutineView(&view)
	if view.CreatedAt != "2026-10-03T11:00:00+08:00" || view.UpdatedAt != "not a time" || *view.Trigger.NextRunAt != "2026-10-03T20:00:00+08:00" ||
		view.Trigger.NextRuns[1] != "2026-10-03T21:00:00+08:00" || view.LastRun.CreatedAt != "2026-10-03T19:00:00+08:00" || *view.LastRun.CompletedAt != "2026-10-03T19:00:05+08:00" {
		t.Fatalf("localized view = %+v / %+v", view, view.Trigger)
	}
	if next != "2026-10-03T12:00:00Z" || original.CreatedAt != "2026-10-03T11:00:00Z" {
		t.Fatal("localization mutated the shared executor values")
	}
}
