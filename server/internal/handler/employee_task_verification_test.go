package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/dwsclient"
	"github.com/multica-ai/multica/server/internal/employeetask"
	"github.com/multica-ai/multica/server/internal/employeeverification"
	"github.com/multica-ai/multica/server/internal/scene"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/service/dingtalkresponse"
	"github.com/multica-ai/multica/server/internal/service/employeememory"
)

func ensureEmployeeVerificationSchema(t *testing.T) {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("..", "..", "migrations", "997*.up.sql"))
	if err != nil || len(files) == 0 {
		t.Fatalf("verification migrations: %v %v", files, err)
	}
	attempts, err := filepath.Glob(filepath.Join("..", "..", "migrations", "987[01]_*.up.sql"))
	if err != nil || len(attempts) != 2 {
		t.Fatalf("verification attempt migrations: %v %v", attempts, err)
	}
	files = append(files, attempts...)
	sort.Strings(files)
	for _, path := range files {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = testPool.Exec(context.Background(), string(raw)); err != nil {
			t.Fatalf("%s: %v", filepath.Base(path), err)
		}
	}
}

// The Host adapter reads the sealed Storage object of a real uploaded Run
// artifact, verifies it deterministically and distills exactly once into the
// configured automation scope, never into the creator's private namespace.
func TestEmployeeVerificationReadsSealedRunArtifactAndDistillsOnce(t *testing.T) {
	f := newEmployeeArtifactFixture(t)
	ensureEmployeeVerificationSchema(t)
	ctx := context.Background()
	t.Cleanup(func() {
		for _, table := range []string{"employee_task_verified_distill", "employee_task_verification", "employee_task_verification_spec", "employee_learning", "employee_memory_state"} {
			_, _ = testPool.Exec(context.Background(), `DELETE FROM `+table+` WHERE workspace_id=$1::uuid`, f.scope.WorkspaceID)
		}
	})
	previousMemory := testHandler.EmployeeMemory
	testHandler.EmployeeMemory = employeememory.NewStore(testPool)
	t.Cleanup(func() { testHandler.EmployeeMemory = previousMemory })

	if w := f.upload(t, "report.csv", []byte("区域,金额\n华东,12\n华北,8\n")); w.Code != http.StatusOK {
		t.Fatalf("upload %d %s", w.Code, w.Body.String())
	}
	store := employeetask.NewStore(testPool)
	if _, _, err := store.RecordResult(ctx, f.scope, f.task.ID, employeetask.ResultParams{Source: employeetask.Source{Namespace: "queue_terminal", Key: f.access.owned}, RunID: f.run.ID, State: employeetask.StateSucceeded, Result: "PASS"}); err != nil {
		t.Fatal(err)
	}
	if _, err := employeeverification.NewStore(testPool).SetSpec(ctx, f.scope, f.task.ID, employeeverification.SetSpecParams{Origin: employeeverification.OriginHostFixture, SourceRef: "fixture:" + uuid.NewString(), AuthorRef: "host:fixture", LearningScope: employeeverification.LearningScopeScene,
		Checks: []employeeverification.Check{{Kind: employeeverification.KindArtifactContents, File: "report.csv", DataRows: intPointer(2), Columns: []string{"区域", "金额"}}}}); err != nil {
		t.Fatal(err)
	}

	// A swapped Storage object fails to open; nothing is recorded.
	f.storage.mu.Lock()
	saved := map[string][]byte{}
	for key, data := range f.storage.files {
		saved[key] = data
		f.storage.files[key] = append([]byte("x"), data[1:]...)
	}
	f.storage.mu.Unlock()
	if _, err := testHandler.VerifyEmployeeRun(ctx, f.scope, f.task.ID, f.run.ID); !errors.Is(err, errEmployeeArtifactInvalid) {
		t.Fatalf("tampered object verified: %v", err)
	}
	f.storage.mu.Lock()
	for key, data := range saved {
		f.storage.files[key] = data
	}
	f.storage.mu.Unlock()
	var records int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM employee_task_verification WHERE workspace_id=$1::uuid`, f.scope.WorkspaceID).Scan(&records); err != nil || records != 0 {
		t.Fatalf("records after failed read=%d %v", records, err)
	}

	// The durable trigger finds the Run without any notification.
	if n, err := testHandler.ReconcileEmployeeVerifications(ctx, 100); err != nil || n < 1 {
		t.Fatalf("pending verification n=%d %v", n, err)
	}
	result, err := testHandler.VerifyEmployeeRun(ctx, f.scope, f.task.ID, f.run.ID)
	if err != nil || result.Gate.Status != employeeverification.GatePassed || !result.Intent {
		t.Fatalf("verify %+v %v", result, err)
	}
	for range 2 {
		if _, err = testHandler.ReconcileEmployeeVerifiedDistill(ctx, 100); err != nil {
			t.Fatal(err)
		}
	}
	memoryScope := employeememory.Scope{WorkspaceID: parseUUID(f.scope.WorkspaceID), AgentID: parseUUID(f.scope.AgentID), TenantOrgID: f.scope.TenantOrgID, Scene: f.scope.Scene, Kind: employeememory.ScopeScene}
	rows, err := testHandler.EmployeeMemory.Search(ctx, memoryScope, "", 10)
	if err != nil || len(rows) != 1 || !rows[0].Trusted || rows[0].Source != employeememory.LearningSourceExecution || rows[0].ExecutionID != f.run.ID {
		t.Fatalf("scene learning %+v %v", rows, err)
	}
	private := memoryScope
	private.Kind, private.PrincipalID = employeememory.ScopePrivate, f.task.RequesterRef
	if rows, err = testHandler.EmployeeMemory.Search(ctx, private, "", 10); err != nil || len(rows) != 0 {
		t.Fatalf("automation learning reached the creator's private namespace: %+v %v", rows, err)
	}
}

func intPointer(v int) *int { return &v }

// G1.1: a Run that delivered sum.txt through the sandbox's native DingTalk send
// is verified from the bytes the Host downloads as the agent, bound to the
// admitted dispatch's identity and gateway, and distilled once.
func TestEmployeeVerificationReadsSandboxDeliveredFileAsAgent(t *testing.T) {
	r := newEmployeeResourceTest(t, resourceMessage("msg-sum-request", "ELMEM1 完成标准：sum.txt 的内容应为 5050"))
	ensureEmployeeVerificationSchema(t)
	ctx := context.Background()
	h := r.f.h
	h.EmployeeMemory = employeememory.NewStore(testPool)
	h.EmployeeSceneWorker.ResourceProvider = r.dws
	scope := employeetask.Scope{WorkspaceID: r.job.Scope.WorkspaceID, AgentID: r.job.Scope.AgentID, TenantOrgID: r.job.Scope.TenantOrgID, Kind: employeetask.ScopeScene, Scene: scene.Ref{SceneID: r.job.Scope.SceneID}}
	queueID := uuid.NewString()
	t.Cleanup(func() {
		for _, table := range []string{"employee_task_verified_distill", "employee_task_verification", "employee_task_verification_attempt", "employee_task_verification_spec", "employee_learning", "employee_memory_state"} {
			_, _ = testPool.Exec(context.Background(), `DELETE FROM `+table+` WHERE workspace_id=$1::uuid`, scope.WorkspaceID)
		}
		_, _ = testPool.Exec(context.Background(), `DELETE FROM sandbox_send_receipt WHERE task_id=$1::uuid`, queueID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id=$1::uuid`, queueID)
	})
	requester := "dingtalk:" + scope.TenantOrgID + ":open_id:" + resourceRequester
	store := employeetask.NewStore(testPool)
	task, err := store.Create(ctx, employeetask.CreateParams{Scope: scope, OwnerLoop: employeetask.LoopEmployee, DispatchMode: employeetask.DispatchDirect, RequesterRef: requester,
		Definition: employeetask.Definition{Goal: "计算 1 到 100 的和"}, Source: employeetask.Source{Namespace: "employee_scene", Key: uuid.NewString()}, Input: "sum"})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]string{"type": service.DirectTaskContextType, "workspace_id": scope.WorkspaceID, "employee_task_id": task.ID, "direct_task_prompt": "sum", "direct_principal_id": r.job.PrincipalID, "employee_job_id": r.job.ID})
	if _, err = testPool.Exec(ctx, `INSERT INTO agent_task_queue(id,agent_id,runtime_id,status,context,trigger_evidence_kind,trigger_evidence_ref_id) SELECT $1::uuid,a.id,a.runtime_id,'completed',$2,'employee_task',$3::uuid FROM agent a WHERE a.id=$4::uuid`, queueID, raw, task.ID, scope.AgentID); err != nil {
		t.Fatal(err)
	}
	run, err := store.StartRun(ctx, scope, task.ID, employeetask.StartRunParams{Source: employeetask.Source{Namespace: "test", Key: uuid.NewString()}, QueueTaskID: queueID, ExpectedVersion: task.Version})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.RecordResult(ctx, scope, task.ID, employeetask.ResultParams{Source: employeetask.Source{Namespace: "queue_terminal", Key: queueID}, RunID: run.ID, State: employeetask.StateSucceeded, Result: "已发送 sum.txt，delivered"}); err != nil {
		t.Fatal(err)
	}
	if _, err = employeeverification.NewStore(testPool).SetSpec(ctx, scope, task.ID, employeeverification.SetSpecParams{Origin: employeeverification.OriginHumanCue, SourceRef: "employee-message:" + r.receipt + "/msg-sum-request", AuthorRef: requester,
		Checks: employeeverification.DeriveFromHumanText("完成标准：sum.txt 的内容应为 5050")}); err != nil {
		t.Fatal(err)
	}
	if _, err = testPool.Exec(ctx, `INSERT INTO sandbox_send_receipt(id,workspace_id,agent_id,task_id,client_action_id,input,payload_hash,target_conversation_id,observed_state,state,provider_conversation_id,provider_message_id)
VALUES($1,$2::uuid,$3::uuid,$4::uuid,'client',jsonb_build_object('dws_uid','123','dws_org_id',$5::text),'hash',$6,'delivered','delivered',$6,'msg-sum-file')`,
		"sandbox-"+queueID, scope.WorkspaceID, scope.AgentID, queueID, scope.TenantOrgID, r.conversation); err != nil {
		t.Fatal(err)
	}
	r.dws.messages["msg-sum-file"] = providerMessage(r.conversation, "msg-sum-file", "agent-open-id", "", fileRes("FILE-SUM"))
	r.dws.files["FILE-SUM"] = dwsclient.MessageFile{Name: "sum.txt", Data: []byte("5050\n")}

	if n, err := h.ReconcileEmployeeVerifications(ctx, 100); err != nil || n < 1 {
		t.Fatalf("pending verification n=%d %v", n, err)
	}
	gate, err := employeeverification.GateTx(ctx, testPool, scope, task.ID, run.ID)
	if err != nil || gate.Status != employeeverification.GatePassed || !gate.Correct {
		t.Fatalf("gate %+v %v", gate, err)
	}
	r.dws.mu.Lock()
	inputs := append([]dingtalkresponse.ActionInput(nil), r.dws.inputs...)
	r.dws.mu.Unlock()
	if len(inputs) == 0 {
		t.Fatal("no provider read")
	}
	for _, in := range inputs {
		if in.DWSUID != "123" || in.ConversationID != r.conversation || in.TaskID != queueID || in.DWSOrgID != scope.TenantOrgID || in.DWSEnvironment != commandDWSEnvironment(r.envs[0].Command) {
			t.Fatalf("provider read not bound to the agent and dispatch: %+v", in)
		}
	}
	if n, err := h.ReconcileEmployeeVerifiedDistill(ctx, 100); err != nil || n != 1 {
		t.Fatalf("distill n=%d %v", n, err)
	}
	private := employeememory.Scope{WorkspaceID: parseUUID(scope.WorkspaceID), AgentID: parseUUID(scope.AgentID), TenantOrgID: scope.TenantOrgID, Scene: scope.Scene, Kind: employeememory.ScopePrivate, PrincipalID: requester}
	rows, err := h.EmployeeMemory.Search(ctx, private, "", 10)
	if err != nil || len(rows) != 1 || rows[0].ExecutionID != run.ID || !rows[0].Trusted {
		t.Fatalf("verified learning %+v %v", rows, err)
	}
}
