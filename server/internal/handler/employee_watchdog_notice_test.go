package handler

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/employeetask"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/service/dingtalkresponse"
)

type employeeWatchdogHandlerFixture struct {
	employeeNoticeFixture
	scope employeetask.Scope
	task  employeetask.Task
	ref   service.EmployeeWatchdogNoticeRef
	start time.Time
}

// employeeWatchdogHandlerDatabase dispatches a real Employee Direct Run through
// the foreground (fake model) and leaves its queue claimed and running.
func employeeWatchdogHandlerDatabase(t *testing.T, router bool) employeeWatchdogHandlerFixture {
	t.Helper()
	f := employeeNoticeDatabase(t, "running", router, false)
	ctx := context.Background()
	t.Cleanup(func() {
		for _, q := range []string{`DELETE FROM employee_watchdog_notice WHERE agent_id=$1::uuid`, `DELETE FROM employee_watchdog_episode WHERE agent_id=$1::uuid`, `DELETE FROM employee_watchdog_cursor WHERE agent_id=$1::uuid`, `DELETE FROM response_action WHERE agent_id=$1::uuid`, `DELETE FROM employee_host_notice WHERE agent_id=$1::uuid`} {
			if _, err := testPool.Exec(ctx, q, f.agentID); err != nil {
				t.Error(err)
			}
		}
	})
	out := employeeWatchdogHandlerFixture{employeeNoticeFixture: f}
	var taskID string
	if err := testPool.QueryRow(ctx, `SELECT t.id::text,t.workspace_id::text,t.agent_id::text,t.tenant_org_id,t.scene_id::text,date_trunc('second',now()) FROM employee_task t JOIN employee_task_run r ON r.task_id=t.id WHERE r.id=$1::uuid`, f.runID).
		Scan(&taskID, &out.scope.WorkspaceID, &out.scope.AgentID, &out.scope.TenantOrgID, &out.scope.Scene.SceneID, &out.start); err != nil {
		t.Fatal(err)
	}
	out.scope.Kind = employeetask.ScopeScene
	task, err := employeetask.NewStore(testPool).Get(ctx, out.scope, taskID)
	if err != nil {
		t.Fatal(err)
	}
	out.task = task
	if _, err := testPool.Exec(ctx, `UPDATE agent_task_queue SET started_at=$2 WHERE id=$1::uuid`, f.queueID, out.start); err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(ctx, `INSERT INTO employee_watchdog_cursor(workspace_id,agent_id,enabled_at) VALUES($1::uuid,$2::uuid,$3)`, out.scope.WorkspaceID, out.scope.AgentID, out.start.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	out.ref = service.EmployeeWatchdogNoticeRef{Scope: out.scope, TaskID: task.ID, RunID: f.runID, QueueTaskID: f.queueID, RequesterRef: task.RequesterRef, Kind: service.EmployeeWatchdogExecutionRunning}
	return out
}

func (f employeeWatchdogHandlerFixture) resolve(t *testing.T) (service.EmployeeWatchdogTarget, error) {
	t.Helper()
	ctx := context.Background()
	tx, err := testPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	return f.h.ResolveEmployeeWatchdogTarget(ctx, tx, f.ref)
}

func TestEmployeeWatchdogNoticeTargetIsTheAcceptedSource(t *testing.T) {
	for _, router := range []bool{false, true} {
		name := "native"
		if router {
			name = "router"
		}
		t.Run(name, func(t *testing.T) {
			f := employeeWatchdogHandlerDatabase(t, router)
			target, err := f.resolve(t)
			if err != nil {
				t.Fatal(err)
			}
			in := target.Input
			if in.WorkspaceID != f.scope.WorkspaceID || in.AgentID != f.scope.AgentID || in.SceneID != f.scope.Scene.SceneID || in.DWSUID != "123" || in.DWSOrgID != f.scope.TenantOrgID ||
				in.ConversationID != f.command.Event.Data.Conversation.OpenConversationID || in.SenderOpenDingTalkID == "" {
				t.Fatalf("anchor is not the accepted source: %+v", in)
			}
			if in.TaskID != "" || in.CallbackURL != "" || in.RequestID != "" || in.ReplyToOpenMsgID != "" || in.EmployeeRunNoticeID != "" || in.CloseState != "" {
				t.Fatalf("watchdog target must not close a dispatch or claim a Run reply: %+v", in)
			}
			// The notice joins the requester's scene dialogue: the source
			// job's admission principal and the source message's receipt.
			var principal string
			if err := testPool.QueryRow(context.Background(), `SELECT principal_id::text FROM employee_scene_job WHERE id=$1::uuid`, f.jobID).Scan(&principal); err != nil {
				t.Fatal(err)
			}
			if target.HistoryPrincipalID != principal || target.OriginReceiptID == "" {
				t.Fatalf("history binding: %+v (principal %s)", target, principal)
			}
			// A goal without an active Run is addressed through its Task
			// origin, which reaches the same conversation and principal.
			origin := f
			origin.ref.RunID, origin.ref.QueueTaskID = "", ""
			byOrigin, err := origin.resolve(t)
			if err != nil {
				t.Fatal(err)
			}
			if byOrigin.Input.ConversationID != in.ConversationID || byOrigin.Input.DWSUID != in.DWSUID || byOrigin.Input.SceneID != in.SceneID || byOrigin.Input.IsGroup != in.IsGroup ||
				byOrigin.HistoryPrincipalID != principal || byOrigin.OriginReceiptID != target.OriginReceiptID {
				t.Fatalf("origin anchor %+v differs from the run anchor %+v", byOrigin, target)
			}
		})
	}
}

func TestEmployeeWatchdogNoticeTargetHoldsRevokedAnchor(t *testing.T) {
	for _, c := range []struct {
		name, statement, reason string
	}{
		{"agent_archived", `UPDATE agent SET archived_at=now() WHERE id=$1::uuid`, "agent_archived"},
		{"identity_removed", `DELETE FROM agent_dingtalk_identity WHERE agent_id=$1::uuid`, "identity_removed"},
		{"endpoint_revoked", `DELETE FROM agent_dispatch_endpoint WHERE agent_id=$1::uuid`, "endpoint_revoked"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := employeeWatchdogHandlerDatabase(t, false)
			if _, err := testPool.Exec(context.Background(), c.statement, f.agentID); err != nil {
				t.Fatal(err)
			}
			_, err := f.resolve(t)
			var hold *service.EmployeeWatchdogHold
			if !errors.As(err, &hold) || hold.Reason != c.reason {
				t.Fatalf("revoked anchor err=%v, want hold %s", err, c.reason)
			}
		})
	}
	t.Run("half_execution_ref", func(t *testing.T) {
		f := employeeWatchdogHandlerDatabase(t, false)
		f.ref.QueueTaskID = ""
		_, err := f.resolve(t)
		var hold *service.EmployeeWatchdogHold
		if !errors.As(err, &hold) || hold.Reason != "delivery_anchor_unavailable" {
			t.Fatal(err)
		}
	})
}

// The full chain on the real foreground fixture: one scene notice into the
// accepted conversation, sent once through the chained fence, with no new
// model call, Task, Run or queue work.
func TestEmployeeWatchdogNoticeDeliversOnceToTheAcceptedConversation(t *testing.T) {
	f := employeeWatchdogHandlerDatabase(t, false)
	ctx := context.Background()
	if _, err := testPool.Exec(ctx, `DELETE FROM response_action WHERE agent_id=$1::uuid`, f.agentID); err != nil {
		t.Fatal(err)
	}
	counts := func() [3]int {
		var c [3]int
		for i, q := range []string{`SELECT count(*) FROM employee_task_run WHERE agent_id=$1::uuid`, `SELECT count(*) FROM agent_task_queue WHERE agent_id=$1::uuid`, `SELECT count(*) FROM employee_scene_job WHERE agent_id=$1::uuid`} {
			if err := testPool.QueryRow(ctx, q, f.agentID).Scan(&c[i]); err != nil {
				t.Fatal(err)
			}
		}
		return c
	}
	before, calls := counts(), f.model.calls
	var clock atomic.Int64
	clock.Store(f.start.Add(16 * time.Minute).UnixNano())
	w := &service.EmployeeWatchdog{DB: testPool, Config: service.DefaultEmployeeWatchdogConfig(), Targets: f.h, Outbox: f.h.DingTalkResponses, Ready: func(context.Context) error { return nil }, Now: func() time.Time { return time.Unix(0, clock.Load()) }}
	got, err := w.Scan(ctx, 100)
	if err != nil || got.Enqueued != 1 {
		t.Fatal(got, err)
	}
	var noticeID, actionID, body string
	if err := testPool.QueryRow(ctx, `SELECT id::text,action_id,body FROM employee_watchdog_notice WHERE task_id=$1::uuid AND state='enqueued'`, f.task.ID).Scan(&noticeID, &actionID, &body); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "还在执行") {
		t.Fatal(body)
	}
	provider := &employeeNoticeProvider{}
	svc := dingtalkresponse.NewService(testPool, provider, nil)
	svc.BeforeSend = f.h.BeforeEmployeeResponseSend(w)
	runCtx, cancel := context.WithCancel(ctx)
	go svc.Run(runCtx)
	defer func() {
		cancel()
		if !svc.WaitWithTimeout(context.Background(), 5*time.Second) {
			t.Error("response worker did not stop")
		}
	}()
	waitEmployeeNoticeAction(t, actionID, "delivered", 15*time.Second)
	if provider.sends.Load() != 1 {
		t.Fatal("watchdog notice sends", provider.sends.Load())
	}
	clock.Store(f.start.Add(30 * time.Minute).UnixNano())
	if _, err := w.Scan(ctx, 100); err != nil {
		t.Fatal(err)
	}
	if after := counts(); after != before || f.model.calls != calls {
		t.Fatalf("notice created work or a generation: before=%v after=%v calls %d->%d", before, after, calls, f.model.calls)
	}
	var conversation, scene string
	if err := testPool.QueryRow(ctx, `SELECT input->>'conversation_id',input->>'scene_notice_id' FROM response_action WHERE id=$1`, actionID).Scan(&conversation, &scene); err != nil {
		t.Fatal(err)
	}
	if conversation != f.command.Event.Data.Conversation.OpenConversationID || scene != noticeID {
		t.Fatal(conversation, scene)
	}
	// The delivered notice is history of the requester's scene dialogue.
	var kind, source string
	if err := testPool.QueryRow(ctx, `SELECT h.source_kind,h.source_id FROM employee_host_notice h JOIN employee_scene_job j ON j.id=$2::uuid AND j.principal_id=h.principal_id AND j.scene_id=h.scene_id WHERE h.action_id=$1`, actionID, f.jobID).Scan(&kind, &source); err != nil || kind != "watchdog" || source != noticeID {
		t.Fatal("stall notice is not in the requester's scene history", kind, source, err)
	}
}
