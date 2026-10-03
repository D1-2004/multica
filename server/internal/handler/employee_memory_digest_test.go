package handler

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/employeeentry"
	"github.com/multica-ai/multica/server/internal/modelregistry"
	"github.com/multica-ai/multica/server/internal/scene"
	"github.com/multica-ai/multica/server/internal/service/employeeloop"
	"github.com/multica-ai/multica/server/internal/service/employeememory/digest"
	"github.com/multica-ai/multica/server/internal/util"
	openai "github.com/openai/openai-go/v3"
)

func cleanupEmployeeDigest(t *testing.T, agentID string) {
	t.Cleanup(func() {
		for _, table := range []string{"employee_scene_ledger", "employee_scene_digest_run", "employee_scene_digest_state"} {
			if _, err := testPool.Exec(context.Background(), `DELETE FROM `+table+` WHERE agent_id=$1`, agentID); err != nil {
				t.Error(err)
			}
		}
	})
}

func TestEmployeeDigestFenceFailsClosed(t *testing.T) {
	f, _, _ := employeeFixture(t)
	ctx := context.Background()
	agent := parseUUID(f.agentID)
	row, err := scene.Resolve(ctx, f.h.Queries, scene.Owner{WorkspaceID: parseUUID(testWorkspaceID), AgentID: agent}, scene.DingTalkConversation("digest-org", scene.KindGroup, "cid-"+uuid.NewString()), scene.Observation{KindStated: true})
	if err != nil {
		t.Fatal(err)
	}
	key := digest.SceneKey{WorkspaceID: testWorkspaceID, AgentID: f.agentID, TenantOrgID: "digest-org", SceneID: util.UUIDToString(row.ID)}
	fence := func(k digest.SceneKey) string {
		t.Helper()
		tx, err := testPool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		reason, err := employeeDigestFence(ctx, tx, k)
		if err != nil {
			t.Fatal(err)
		}
		return reason
	}
	if got := fence(key); got != "" {
		t.Fatalf("live employee group scene fenced: %s", got)
	}
	stale := key
	stale.TenantOrgID = "other-org"
	missing := key
	missing.SceneID = uuid.NewString()
	if fence(stale) != "tenant_revoked" || fence(missing) != "scene_missing" {
		t.Fatal("tenant and scene fences must fail closed")
	}
	if _, err = testPool.Exec(ctx, `UPDATE agent SET coordination_mode='coordinator' WHERE id=$1`, f.agentID); err != nil {
		t.Fatal(err)
	}
	if got := fence(key); got != "agent_not_employee" {
		t.Fatalf("coordinator-mode agent: %q", got)
	}
}

type digestTestAttempt struct {
	ref     modelregistry.Ref
	request openai.ChatCompletionNewParams
}

func (a *digestTestAttempt) Ref() modelregistry.Ref       { return a.ref }
func (a *digestTestAttempt) ConfigurationRevision() int64 { return 7 }
func (a *digestTestAttempt) Chat(_ context.Context, request openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
	a.request = request
	return &openai.ChatCompletion{ID: "ok"}, nil
}

type digestTestRoutes struct {
	plan     modelregistry.CoordinatorPlan
	attempt  *digestTestAttempt
	prepared []int
}

func (r *digestTestRoutes) CoordinatorPlan(context.Context) (modelregistry.CoordinatorPlan, error) {
	return r.plan, nil
}
func (r *digestTestRoutes) PrepareCoordinatorAttempt(_ context.Context, _ modelregistry.CoordinatorPlan, index int) (modelregistry.CoordinatorAttempt, error) {
	r.prepared = append(r.prepared, index)
	if index == 0 {
		return nil, modelregistry.ErrCandidateUnavailable
	}
	r.attempt.ref = r.plan.Candidates[index]
	return r.attempt, nil
}

func TestEmployeeDigestModelUsesSharedCoordinatorChain(t *testing.T) {
	routes := &digestTestRoutes{plan: modelregistry.CoordinatorPlan{Version: 1, Revision: 3, Candidates: []modelregistry.Ref{{Provider: "a", Model: "disabled"}, {Provider: "dashscope", Model: "qwen3.7-plus"}, {Provider: "b", Model: "never"}}, RequestProfile: modelregistry.EmployeeFastRequestProfile}, attempt: &digestTestAttempt{}}
	out, ref, err := employeeDigestModel{routes: routes}.Chat(context.Background(), openai.ChatCompletionNewParams{Messages: []openai.ChatCompletionMessageParamUnion{openai.UserMessage("x")}})
	if err != nil || out == nil || ref != routes.plan.Candidates[1].String() {
		t.Fatalf("out=%v ref=%s err=%v", out, ref, err)
	}
	if len(routes.prepared) != 2 || string(routes.attempt.request.Model) != "qwen3.7-plus" {
		t.Fatalf("prepared=%v model=%s", routes.prepared, routes.attempt.request.Model)
	}
	extra := routes.attempt.request.ExtraFields()
	if extra["tool_choice"] != "required" || extra["enable_thinking"] != false {
		t.Fatalf("request profile: %v", extra)
	}
	routes.plan.Candidates = routes.plan.Candidates[:1]
	if _, _, err = (employeeDigestModel{routes: routes}).Chat(context.Background(), openai.ChatCompletionNewParams{}); err == nil || !errors.Is(err, modelregistry.ErrCandidateUnavailable) {
		t.Fatalf("an empty usable chain must fail: %v", err)
	}
}

func TestEmployeeWakeLedgerEntryUsesHostFactsOnly(t *testing.T) {
	job := employeeentry.Job{ID: uuid.NewString(), Kind: employeeentry.KindMessage, Scope: employeeentry.Scope{WorkspaceID: uuid.NewString(), AgentID: uuid.NewString(), TenantOrgID: "org", SceneID: uuid.NewString()}}
	saved := employeeSavedOutcome{Failure: "context deadline exceeded: provider said secret-token-123", Outcome: employeeloop.Outcome{
		Decision: employeeloop.Decision{Kind: employeeloop.Dispatched, Reply: "已安排后台处理"},
		Entries: []employeeloop.SessionEntry{{Message: &openai.ChatCompletionMessage{ToolCalls: []openai.ChatCompletionMessageToolCallUnion{
			{ID: "c1", Function: openai.ChatCompletionMessageFunctionToolCallFunction{Name: "dispatch_task"}},
			{ID: "c2", Function: openai.ChatCompletionMessageFunctionToolCallFunction{Name: "memory_capture"}},
		}}}},
		ToolOutcomes: []employeeloop.ToolOutcome{{NativeToolCallID: "c1", ToolName: "dispatch_task", Result: employeeloop.ToolResult{Receipt: "task-1"}}},
		Receipts:     []employeeloop.Receipt{{NativeToolCallID: "c1", ToolName: "dispatch_task", ID: "task-1"}},
	}}
	requests := []digest.LedgerRequest{{RequesterRef: "dingtalk:org:uid:u1", SpeakerName: "Director", MessageID: "m1", Text: "生成 a.csv"}}
	entry := employeeWakeLedgerEntry(job, requests, "", saved, []string{"action-1"})
	b := entry.Body
	if entry.Kind != digest.LedgerWake || entry.SourceID != job.ID || b.Outcome != "dispatched" || b.Failure != "model_timeout" || b.Reply != "已安排后台处理" {
		t.Fatalf("entry = %+v", entry)
	}
	if len(b.Tools) != 2 || !b.Tools[0].OK || b.Tools[0].Receipt != "task-1" || b.Tools[1].OK {
		t.Fatalf("tools = %+v (an unexecuted call is not ok)", b.Tools)
	}
	if len(b.TaskRefs) != 1 || b.TaskRefs[0] != "dispatch_task:task-1" || b.NoDurableTrace || len(b.SendActionIDs) != 1 {
		t.Fatalf("refs = %+v", b)
	}
	quiet := employeeWakeLedgerEntry(job, nil, "execution_follow_up", employeeSavedOutcome{Outcome: employeeloop.Outcome{Decision: employeeloop.Decision{Kind: employeeloop.Quiet, Reply: "draft"}}}, nil)
	if !quiet.Body.NoDurableTrace || quiet.Body.Reply != "" || quiet.Body.WakeKind != "execution_follow_up" {
		t.Fatalf("a quiet wake records no durable trace: %+v", quiet.Body)
	}
	if strings.Contains(b.Failure, "secret") {
		t.Fatal("raw provider text must not enter the ledger")
	}
}

func TestEmployeeSceneTaskLedgerOncePerTerminalState(t *testing.T) {
	f, _, _ := employeeFixture(t)
	cleanupEmployeeDigest(t, f.agentID)
	ctx := context.Background()
	taskID, sceneID := uuid.NewString(), uuid.NewString()
	if _, err := testPool.Exec(ctx, `INSERT INTO employee_task(id,workspace_id,agent_id,tenant_org_id,scope_kind,scene_id,owner_loop,dispatch_mode,requester_ref,definition,state,source_namespace,source_key,create_payload)
VALUES($1,$2,$3,'org-ledger','scene',$4,'employee','direct','dingtalk:org-ledger:uid:u1','{"goal":"生成 a.csv，恰好 3 行"}','succeeded','test',$5,'{}')`, taskID, testWorkspaceID, f.agentID, sceneID, "k-"+taskID); err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(ctx, `INSERT INTO employee_task_run(id,workspace_id,agent_id,tenant_org_id,task_id,queue_task_id,goal_revision,input_seq,state,result,finished_at) VALUES($1,$2,$3,'org-ledger',$4,$5,1,1,'succeeded','a.csv 已生成，3 行',now())`, uuid.NewString(), testWorkspaceID, f.agentID, taskID, uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := f.h.ReconcileEmployeeSceneTaskLedger(ctx, 100); err != nil {
			t.Fatal(err)
		}
	}
	key := digest.SceneKey{WorkspaceID: testWorkspaceID, AgentID: f.agentID, TenantOrgID: "org-ledger", SceneID: sceneID}
	entries, err := digest.ListLedger(ctx, testPool, key, time.Time{}, 10)
	if err != nil || len(entries) != 1 {
		t.Fatalf("entries = %+v err=%v", entries, err)
	}
	b := entries[0].Body
	if entries[0].Kind != digest.LedgerTaskTerminal || b.TaskID != taskID || b.Goal != "生成 a.csv，恰好 3 行" || b.TaskState != "succeeded" || b.RunState != "succeeded" || b.RunResult != "a.csv 已生成，3 行" {
		t.Fatalf("task terminal entry = %+v", entries[0])
	}
}
