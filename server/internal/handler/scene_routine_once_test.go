package handler

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/contextcap"
	"github.com/multica-ai/multica/server/internal/service/dingtalkresponse"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestOnceTimeRequiresOffsetAndPreservesInstant(t *testing.T) {
	now := time.Date(2026, 10, 4, 6, 18, 0, 0, time.UTC)
	at, _, err := normalizeRoutineOnce("2026-10-04T14:33:06.107+08:00", "Asia/Shanghai", now)
	if err != nil || at != "2026-10-04T06:33:06.107Z" {
		t.Fatalf("lost reminder instant: %s %v", at, err)
	}
	for _, raw := range []string{"2026-10-04T14:33:06", "2026-10-04T06:17:00Z", "0 9 * * *"} {
		if _, _, err := normalizeRoutineOnce(raw, "", now); err == nil {
			t.Fatalf("accepted invalid/past one-shot: %s", raw)
		}
	}
}

func TestOnceSceneOutboxKeepsOriginalDestinationAndSingleMessage(t *testing.T) {
	x := newEmployeeRoutineHandlerFixture(t, "fixture")
	f, a := x.f, x.a
	ctx := context.Background()
	created := createGroupRoutine(t, f, a, sceneRoutineInput{Title: "Once delivery", Instructions: "Remind me to review the report.", Trigger: sceneRoutineTrigger{Kind: "once", RunAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)}})
	// Advance the persisted clock boundary without holding the creating run open.
	due := time.Now().Add(-time.Minute).UTC().Truncate(time.Microsecond)
	if _, err := testPool.Exec(ctx, `UPDATE autopilot_trigger SET run_at=$2,next_run_at=$2 WHERE id=$1::uuid`, created.Routine.Trigger.ID, due); err != nil {
		t.Fatal(err)
	}
	ap, err := f.h.Queries.GetAutopilot(ctx, parseUUID(created.Routine.AutopilotID))
	if err != nil {
		t.Fatal(err)
	}
	run, err := f.h.AutopilotService.DispatchAutopilotForPlan(ctx, ap, parseUUID(created.Routine.Trigger.ID), "schedule", nil, due)
	if err != nil || run == nil || !run.TaskID.Valid {
		t.Fatalf("one-shot not admitted: %+v %v", run, err)
	}
	if n := x.count(t, `SELECT count(*) FROM response_action WHERE id=$1`, x.noticeID(run.ID, dingtalkresponse.RoutineNoticeStart)); n != 0 {
		t.Fatal("one-shot reminder emitted start-message noise")
	}
	queue, err := f.h.Queries.GetAgentTask(ctx, run.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	result, _ := json.Marshal(protocol.TaskCompletedPayload{Output: "冬翔，到时间了，请查看刚才的报告。"})
	if err := f.h.RoutineTaskFinished(ctx, nil, queue, "completed", result, ""); err != nil {
		t.Fatal(err)
	}
	if err := f.h.RoutineTaskFinished(ctx, nil, queue, "completed", result, ""); err != nil {
		t.Fatal(err)
	}
	var input []byte
	if err := testPool.QueryRow(ctx, `SELECT input FROM response_action WHERE id=$1`, x.noticeID(run.ID, dingtalkresponse.RoutineNoticeEnd)).Scan(&input); err != nil {
		t.Fatal(err)
	}
	var delivered dingtalkresponse.ActionInput
	if err := json.Unmarshal(input, &delivered); err != nil {
		t.Fatal(err)
	}
	routine := mustRoutine(t, f, a, created.Routine.ID)
	want, err := f.h.routineNoticeInput(ctx, f.h.Queries, routine, "冬翔，到时间了，请查看刚才的报告。")
	if err != nil || delivered.Text != want.Text || delivered.ConversationID != want.ConversationID || delivered.DWSOrgID != want.DWSOrgID || delivered.SenderOpenDingTalkID != want.SenderOpenDingTalkID {
		t.Fatalf("one-shot changed destination or output: %+v want %+v %v", delivered, want, err)
	}
}

// Exercise the scene API contract on a real local database: rescheduling must
// not invalidate creation replay, and cancel/consume must never rearm it.
func TestOnceSceneRescheduleReplayAndTerminalGuards(t *testing.T) {
	x := newEmployeeRoutineHandlerFixture(t, "fixture")
	f, a := x.f, x.a
	ctx := context.Background()
	in := sceneRoutineInput{Title: "Once reminder", Instructions: "Remind here once.", Trigger: sceneRoutineTrigger{Kind: "once", RunAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)}}
	created := createGroupRoutine(t, f, a, in)
	routine := mustRoutine(t, f, a, created.Routine.ID)
	newAt := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Microsecond).Format(time.RFC3339Nano)
	updated, err := f.h.updateSceneRoutine(ctx, a, routine, routineMember(), sceneRoutinePatch{RunAt: &newAt})
	if err != nil || updated.Routine.Trigger.RunAt != newAt || updated.Routine.SceneID != routine.SceneID {
		t.Fatalf("reschedule failed or changed scene: %+v %v", updated, err)
	}
	replay := createGroupRoutine(t, f, a, in)
	if replay.Routine.ID != created.Routine.ID || replay.Routine.Trigger.RunAt != newAt || !replay.Updated {
		t.Fatalf("original creation replay duplicated/rearmed rescheduled reminder: %+v", replay)
	}
	if _, err := f.h.runSceneRoutine(ctx, routine, routineMember()); routineCode(err) != "once_run_managed" {
		t.Fatalf("one-shot manually reran: %v", err)
	}
	if err := f.h.deleteSceneRoutine(ctx, a, routine, routineMember()); err != nil {
		t.Fatal(err)
	}
	if err := f.h.deleteSceneRoutine(ctx, a, routine, routineMember()); err != nil {
		t.Fatal("cancellation replay", err)
	}
	sc, _ := f.h.sceneRoutineScene(ctx, a, routine.SceneID)
	if _, err := f.h.createSceneRoutine(ctx, a, sc, routineMember(), sceneRoutineCounterpart{}, in); routineCode(err) != "once_cancelled" {
		t.Fatalf("cancelled reminder creation replay reactivated: %v", err)
	}
	// Model a resource created before its due time whose response was lost.
	// Its exact time is now past, but replay must still return the resource.
	in.Title = "Already due reminder"
	in.Trigger.RunAt = time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)
	created = createGroupRoutine(t, f, a, in)
	routine = mustRoutine(t, f, a, created.Routine.ID)
	past := time.Now().Add(-time.Hour).UTC().Truncate(time.Microsecond)
	in.Trigger.RunAt = past.Format(time.RFC3339Nano)
	if _, err := testPool.Exec(ctx, `UPDATE autopilot_trigger SET run_at=$2,next_run_at=NULL,last_fired_at=now() WHERE id=$1::uuid`, created.Routine.Trigger.ID, past); err != nil {
		t.Fatal(err)
	}
	if err := contextcap.SetRoutineDedupeKey(ctx, testPool, routine.ID, routineDedupeKey(in.Title, in.Trigger)); err != nil {
		t.Fatal(err)
	}
	replay = createGroupRoutine(t, f, a, in)
	if replay.Routine.ID != routine.ID || !replay.Routine.Trigger.Consumed || replay.Routine.Enabled {
		t.Fatalf("lost-response replay failed after due: %+v", replay)
	}
	enable := true
	if _, err := f.h.updateSceneRoutine(ctx, a, routine, routineMember(), sceneRoutinePatch{Enabled: &enable}); routineCode(err) != "once_consumed" {
		t.Fatalf("consumed reminder rearmed: %v", err)
	}
	if err := f.h.deleteSceneRoutine(ctx, a, routine, routineMember()); routineCode(err) != "once_consumed" {
		t.Fatalf("already admitted reminder falsely cancelled: %v", err)
	}
}
