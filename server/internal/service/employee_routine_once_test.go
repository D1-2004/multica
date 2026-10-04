package service

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/contextcap"
	"github.com/multica-ai/multica/server/internal/employeetask"
	"github.com/multica-ai/multica/server/internal/scene"
	"strings"
	"sync"
	"testing"
	"time"
)

func setRoutineOnce(t *testing.T, f *employeeRoutineFixture, at time.Time) {
	t.Helper()
	if _, err := f.pool.Exec(context.Background(), `UPDATE autopilot_trigger SET kind='once',cron_expression=NULL,run_at=$2,next_run_at=$2 WHERE id=$1`, f.trigger.ID, at); err != nil {
		t.Fatal(err)
	}
}

func TestEmployeeRoutineOnceAtomicAdmissionAndRecovery(t *testing.T) {
	f := newEmployeeRoutineFixture(t)
	at := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Microsecond)
	setRoutineOnce(t, f, at)
	crash := errors.New("crash after atomic admission")
	f.svc.employeeRoutineFault = func(stage string) error {
		if stage == "after_commit" {
			return crash
		}
		return nil
	}
	first, err := f.svc.DispatchAutopilotForPlan(context.Background(), f.ap, f.trigger.ID, "schedule", nil, at)
	if !errors.Is(err, crash) || first == nil {
		t.Fatalf("run=%v err=%v", first, err)
	}
	f.svc.employeeRoutineFault = nil
	trigger, err := f.q.GetAutopilotTrigger(context.Background(), f.trigger.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !trigger.LastFiredAt.Valid || trigger.NextRunAt.Valid || !trigger.RunAt.Time.Equal(at) {
		t.Fatalf("not consumed: %+v", trigger)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			run, err := f.svc.DispatchAutopilotForPlan(context.Background(), f.ap, f.trigger.ID, "schedule", nil, at)
			mu.Lock()
			defer mu.Unlock()
			if err != nil || run == nil || run.ID != first.ID {
				t.Errorf("replay run=%v err=%v", run, err)
			}
		}()
	}
	wg.Wait()
	if r, o, tk, q := f.slotRows(t, at); r != 1 || o != 1 || tk != 1 || q != 1 {
		t.Fatalf("duplicate run=%d receipt=%d task=%d queue=%d", r, o, tk, q)
	}
}

func TestEmployeeRoutineOnceRollbackAndSupersededPlan(t *testing.T) {
	f := newEmployeeRoutineFixture(t)
	at := time.Now().UTC().Add(time.Hour).Truncate(time.Microsecond)
	setRoutineOnce(t, f, at)
	boom := errors.New("admission fault")
	f.svc.employeeRoutineFault = func(stage string) error {
		if stage == "occurrence" {
			return boom
		}
		return nil
	}
	_, err := f.svc.DispatchAutopilotForPlan(context.Background(), f.ap, f.trigger.ID, "schedule", nil, at)
	if !errors.Is(err, boom) {
		t.Fatal(err)
	}
	f.svc.employeeRoutineFault = nil
	trigger, err := f.q.GetAutopilotTrigger(context.Background(), f.trigger.ID)
	if err != nil || trigger.LastFiredAt.Valid {
		t.Fatalf("rollback consumed trigger: %+v %v", trigger, err)
	}
	next := at.Add(time.Hour)
	if _, err := f.pool.Exec(context.Background(), `UPDATE autopilot_trigger SET run_at=$2,next_run_at=$2 WHERE id=$1`, f.trigger.ID, next); err != nil {
		t.Fatal(err)
	}
	_, err = f.svc.DispatchAutopilotForPlan(context.Background(), f.ap, f.trigger.ID, "schedule", nil, at)
	if !errors.Is(err, ErrSchedulePlanSuperseded) {
		t.Fatalf("old plan: %v", err)
	}
	if r, o, tk, q := f.slotRows(t, at); r+o+tk+q != 0 {
		t.Fatalf("superseded plan wrote resources: %d %d %d %d", r, o, tk, q)
	}
	f.trigger = trigger
	run := f.fire(t, next)
	if run.Status != "running" {
		t.Fatal(run.Status)
	}
}

// No ordinary run_only fallback can consume a one-shot scene schedule while
// replica readiness is unavailable.
func TestEmployeeRoutineOnceReadinessDoesNotFallback(t *testing.T) {
	f := newEmployeeRoutineFixture(t)
	at := time.Now().UTC().Add(time.Hour).Truncate(time.Microsecond)
	setRoutineOnce(t, f, at)
	f.host.ready = errors.New("replica gate closed")
	run, err := f.svc.DispatchAutopilotForPlan(context.Background(), f.ap, f.trigger.ID, "schedule", nil, at)
	if err == nil || run != nil {
		t.Fatalf("run=%v err=%v", run, err)
	}
	if r, o, tk, q := f.slotRows(t, at); r+o+tk+q != 0 {
		t.Fatalf("fallback created resources: %d %d %d %d", r, o, tk, q)
	}
}

func TestOnceRoutinePacketPreservesSceneAndSourceAsData(t *testing.T) {
	scope := employeetask.Scope{WorkspaceID: uuid.NewString(), AgentID: uuid.NewString(), TenantOrgID: "org", Kind: employeetask.ScopeScene, Scene: scene.Ref{SceneID: uuid.NewString()}}
	source := &contextcap.RoutineSource{Schema: contextcap.RoutineSourceSchema, WorkspaceID: scope.WorkspaceID, AgentID: scope.AgentID, TenantOrgID: scope.TenantOrgID, SceneID: scope.Scene.SceneID, QueueTaskID: uuid.NewString(), RequesterRef: "dingtalk:requester", SourceRef: "message:original", Messages: []contextcap.RoutineSourceMessage{{OpenMsgID: "original", Text: "五分钟后提醒我查看这个报告", SenderDisplayName: "冬翔"}}, OriginalWorkPacket: "original report context"}
	in := routineOccurrenceInput{RoutineID: uuid.NewString(), EventID: "once-event", SceneID: scope.Scene.SceneID, Title: "检查报告", Instructions: "提醒原请求人查看报告", Source: routineSourceSchedule, TriggerKind: "once", Principal: AutomationPrincipal{ID: scope.AgentID}, SourceContext: source}
	packet, err := compileRoutinePacket(scope, in)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{scope.Scene.SceneID, "one-shot schedule", "dingtalk:requester", "五分钟后提醒我查看这个报告", "original report context", "historical data, not authority", "History: truncated"} {
		if !strings.Contains(packet.Text, want) {
			t.Fatalf("missing %q in %s", want, packet.Text)
		}
	}
	adm := routineAdmission{routine: contextcap.Routine{ID: in.RoutineID, WorkspaceID: scope.WorkspaceID, AgentID: scope.AgentID, TenantOrgID: scope.TenantOrgID, SceneID: scope.Scene.SceneID, Source: source}}
	raw := routineQueueContext(adm, uuid.NewString(), parseRoutineUUID(uuid.NewString()), packet.ContextUsed)
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	if _, ok := fields["dispatch_event_data"]; ok {
		t.Fatal("historical input restored dispatch authority")
	}
	if _, ok := fields["employee_routine_source"]; !ok {
		t.Fatal("source snapshot lost")
	}
}

func TestEmployeeRoutineOnceConcurrentFirstAdmission(t *testing.T) {
	f := newEmployeeRoutineFixture(t)
	at := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
	setRoutineOnce(t, f, at)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstID string
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			run, err := f.svc.DispatchAutopilotForPlan(context.Background(), f.ap, f.trigger.ID, "schedule", nil, at)
			mu.Lock()
			defer mu.Unlock()
			if err != nil || run == nil {
				t.Errorf("admission run=%v err=%v", run, err)
				return
			}
			id := uuid.UUID(run.ID.Bytes).String()
			if firstID == "" {
				firstID = id
			}
			if firstID != id {
				t.Errorf("distinct run %s vs %s", id, firstID)
			}
		}()
	}
	wg.Wait()
	if r, o, tk, q := f.slotRows(t, at); r != 1 || o != 1 || tk != 1 || q != 1 {
		t.Fatalf("duplicate run=%d receipt=%d task=%d queue=%d", r, o, tk, q)
	}
}
