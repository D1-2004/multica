package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/service/employeememory"
)

func employeeResetFixture(t *testing.T) (employeeLearningFixture, employeememory.Scope) {
	t.Helper()
	return employeeResetFixtureIn(t, "group")
}

// employeeResetFixtureIn seeds shared and private memory in a conversation.
// A 1:1 reset clears the shared layer; a group reset clears only the sender's
// own records (decision D3), see TestEmployeeMemoryV2GroupResetClearsOnlySendersItems.
func employeeResetFixtureIn(t *testing.T, conversation string) (employeeLearningFixture, employeememory.Scope) {
	t.Helper()
	f := newEmployeeLearningFixtureInConversation(t, conversation)
	ctx := context.Background()
	if _, err := f.response.h.ReconcileEmployeeLearnings(ctx, 100); err != nil {
		t.Fatal(err)
	}
	shared := f.privateScope()
	shared.Kind = employeememory.ScopeScene
	shared.PrincipalID = ""
	bob := f.privateScope()
	bob.PrincipalID = "dingtalk:456:open_id:bob"
	for i, scope := range []employeememory.Scope{shared, bob} {
		key := []string{"shared-preference", "bob-preference"}[i]
		if _, err := f.response.h.EmployeeMemory.Record(ctx, scope, employeememory.LearningRecord{Type: employeememory.LearningTypePreference, Key: key, Insight: key, Confidence: 8}, employeememory.TrustedEvidence{SourceID: key, EvidenceID: key, ActorID: "verified-human", HumanStated: true}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := testPool.Exec(ctx, `INSERT INTO agent_scene_memory(scene_id,workspace_id,agent_id,memory_text) VALUES($1,$2,$3,'Coordinator memory stays')`, f.scope.Scene.SceneID, f.scope.WorkspaceID, f.scope.AgentID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_scene_memory WHERE agent_id=$1`, f.scope.AgentID)
	})
	f.model.dispatch = false
	return f, bob
}
func submitEmployeeReset(t *testing.T, f employeeLearningFixture, messages []DispatchMessage) string {
	t.Helper()
	f.response.command.Event.Data.Messages = messages
	response := employeeHTTP(t, f.response, f.dc, uuid.NewString())
	if response.Code != http.StatusAccepted {
		t.Fatal(response.Code, response.Body.String())
	}
	var receipt string
	if err := testPool.QueryRow(context.Background(), `SELECT receipt_id::text FROM employee_event_consumption WHERE agent_id=$1 ORDER BY created_at DESC LIMIT 1`, f.scope.AgentID).Scan(&receipt); err != nil {
		t.Fatal(err)
	}
	return receipt
}
func TestEmployeeSceneResetClearsOnlySharedAndOwnMemoryWithoutModel(t *testing.T) {
	f, bob := employeeResetFixtureIn(t, "single")
	ctx := context.Background()
	calls := f.model.calls
	receipt := submitEmployeeReset(t, f, []DispatchMessage{{OpenMsgID: "reset-one", Text: "/reset-memory"}})
	if _, err := f.response.h.EmployeeSceneWorker.ProcessNext(ctx); err != nil {
		t.Fatal(err)
	}
	shared := f.privateScope()
	shared.Kind = employeememory.ScopeScene
	shared.PrincipalID = ""
	for _, scope := range []employeememory.Scope{shared, f.privateScope()} {
		brief, err := f.response.h.EmployeeMemory.Brief(ctx, scope, "", 8)
		if err != nil || brief != "" {
			t.Fatalf("reset left memory: %q %v", brief, err)
		}
	}
	if brief, err := f.response.h.EmployeeMemory.Brief(ctx, bob, "", 8); err != nil || !strings.Contains(brief, "bob-preference") {
		t.Fatalf("another requester's memory changed: %q %v", brief, err)
	}
	var coordinator string
	if err := testPool.QueryRow(ctx, `SELECT memory_text FROM agent_scene_memory WHERE scene_id=$1`, f.scope.Scene.SceneID).Scan(&coordinator); err != nil || coordinator != "Coordinator memory stays" {
		t.Fatal(coordinator, err)
	}
	if f.model.calls != calls {
		t.Fatalf("slash called model: %d -> %d", calls, f.model.calls)
	}
	// A crash after the reset journal but before outcome must not clear new memory.
	if _, err := f.response.h.EmployeeMemory.Record(ctx, f.privateScope(), employeememory.LearningRecord{Type: employeememory.LearningTypePreference, Key: "after-reset", Insight: "new preference after reset", Confidence: 8}, employeememory.TrustedEvidence{SourceID: "after-reset", EvidenceID: "after-reset", ActorID: "verified-human", HumanStated: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(ctx, `UPDATE employee_scene_job SET state='pending',outcome=NULL,lease_token=NULL,lease_until=NULL WHERE id=(SELECT job_id FROM employee_event_consumption WHERE receipt_id=$1)`, receipt); err != nil {
		t.Fatal(err)
	}
	if _, err := f.response.h.EmployeeSceneWorker.ProcessNext(ctx); err != nil {
		t.Fatal(err)
	}
	if brief, err := f.response.h.EmployeeMemory.Brief(ctx, f.privateScope(), "", 8); err != nil || !strings.Contains(brief, "new preference after reset") {
		t.Fatalf("replay cleared new memory: %q %v", brief, err)
	}
}
func TestEmployeeSceneResetDoesNotDiscardOtherRequesterWork(t *testing.T) {
	f, _ := employeeResetFixture(t)
	ctx := context.Background()
	calls := f.model.calls
	receipt := submitEmployeeReset(t, f, []DispatchMessage{{OpenMsgID: "alice-reset", Text: "/reset-memory", SenderOpenDingTalkID: "requester-open-id"}, {OpenMsgID: "bob-work", Text: "Analyze this new feedback", SenderOpenDingTalkID: "bob"}})
	f.model.dispatch = true
	f.model.sourceRef = receipt + "/bob-work"
	if _, err := f.response.h.EmployeeSceneWorker.ProcessNext(ctx); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	var owner string
	if err := testPool.QueryRow(ctx, `SELECT input_snapshot FROM employee_scene_job WHERE id=(SELECT job_id FROM employee_event_consumption WHERE receipt_id=$1)`, receipt).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var input employeeSavedInput
	if err := json.Unmarshal(raw, &input); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(input.Input.CurrentWindow, "/reset-memory") || !strings.Contains(input.Input.CurrentWindow, "bob-work") {
		t.Fatalf("wrong remaining window: %s", input.Input.CurrentWindow)
	}
	if err := testPool.QueryRow(ctx, `SELECT requester_ref FROM employee_task WHERE agent_id=$1 AND id<>$2`, f.scope.AgentID, f.taskID).Scan(&owner); err != nil || owner != "dingtalk:456:open_id:bob" {
		t.Fatalf("Bob work lost or misowned: %s %v", owner, err)
	}
	if strings.Contains(input.Input.Memory, "bob-preference") {
		t.Fatal("removing reset message exposed private memory in a mixed original window")
	}
	if f.model.calls != calls+1 {
		t.Fatalf("remaining work calls=%d want %d", f.model.calls, calls+1)
	}
}

func TestEmployeeSceneResetOversizedRemainderKeepsResetReceipt(t *testing.T) {
	f, _ := employeeResetFixture(t)
	ctx := context.Background()
	calls := f.model.calls
	receipt := submitEmployeeReset(t, f, []DispatchMessage{{OpenMsgID: "alice-reset", Text: "/reset-memory", SenderOpenDingTalkID: "requester-open-id"}, {OpenMsgID: "bob-large", Text: strings.Repeat("x", 300<<10), SenderOpenDingTalkID: "bob"}})
	if _, err := f.response.h.EmployeeSceneWorker.ProcessNext(ctx); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err := testPool.QueryRow(ctx, `SELECT outcome FROM employee_scene_job WHERE id=(SELECT job_id FROM employee_event_consumption WHERE receipt_id=$1)`, receipt).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "已清理") || !strings.Contains(string(raw), "过长") || f.model.calls != calls {
		t.Fatalf("partial Host fact lost: %s calls=%d", raw, f.model.calls)
	}
}

func TestEmployeeSceneResetUnknownRequesterDoesNotClearMemory(t *testing.T) {
	f, _ := employeeResetFixture(t)
	ctx := context.Background()
	receipt := submitEmployeeReset(t, f, []DispatchMessage{{OpenMsgID: "unknown-reset", Text: "/reset-memory"}, {OpenMsgID: "bob-chat", Text: "Hello", SenderOpenDingTalkID: "bob"}})
	if _, err := f.response.h.EmployeeSceneWorker.ProcessNext(ctx); err != nil {
		t.Fatal(err)
	}
	shared := f.privateScope()
	shared.Kind = employeememory.ScopeScene
	shared.PrincipalID = ""
	if brief, err := f.response.h.EmployeeMemory.Brief(ctx, shared, "", 8); err != nil || !strings.Contains(brief, "shared-preference") {
		t.Fatalf("unknown actor cleared scene: %q %v", brief, err)
	}
	var raw []byte
	if err := testPool.QueryRow(ctx, `SELECT outcome FROM employee_scene_job WHERE id=(SELECT job_id FROM employee_event_consumption WHERE receipt_id=$1)`, receipt).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "无法确认") {
		t.Fatalf("unknown actor received false success: %s", raw)
	}
}
func TestEmployeeSceneResetAndOtherReceiptEachGetDurableReply(t *testing.T) {
	f, _ := employeeResetFixture(t)
	ctx := context.Background()
	resetReceipt := submitEmployeeReset(t, f, []DispatchMessage{{OpenMsgID: "alice-reset", Text: "/reset-memory"}})
	if _, err := testPool.Exec(ctx, `UPDATE employee_scene_job SET created_at=now() WHERE id=(SELECT job_id FROM employee_event_consumption WHERE receipt_id=$1)`, resetReceipt); err != nil {
		t.Fatal(err)
	}
	workReceipt := submitEmployeeReset(t, f, []DispatchMessage{{OpenMsgID: "bob-work", Text: "Analyze this feedback", SenderOpenDingTalkID: "bob"}})
	var jobs int
	if err := testPool.QueryRow(ctx, `SELECT count(DISTINCT job_id) FROM employee_event_consumption WHERE receipt_id IN($1,$2)`, resetReceipt, workReceipt).Scan(&jobs); err != nil || jobs != 1 {
		t.Fatalf("fixture did not collect two receipts: %d %v", jobs, err)
	}
	f.model.dispatch = true
	f.model.sourceRef = workReceipt + "/bob-work"
	if _, err := f.response.h.EmployeeSceneWorker.ProcessNext(ctx); err != nil {
		t.Fatal(err)
	}
	var replies []string
	rows, err := testPool.Query(ctx, `SELECT input->>'text' FROM response_action WHERE agent_id=$1`, f.scope.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var reply string
		if err = rows.Scan(&reply); err != nil {
			t.Fatal(err)
		}
		replies = append(replies, reply)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(replies) != 3 {
		t.Fatalf("reset and work delivery collided: %#v", replies)
	}
	found := false
	for _, reply := range replies {
		found = found || strings.Contains(reply, "已清理")
	}
	if !found {
		t.Fatal("reset delivery missing", replies)
	}
}
func TestEmployeeResetParserRequiresStandaloneCommand(t *testing.T) {
	for _, tc := range []struct {
		text string
		want bool
	}{{"/reset-memory", true}, {"@Employee /reset-memory", true}, {"/reset-memory then analyze feedback", false}, {"Please /reset-memory", false}, {"/reset", false}} {
		if got := isEmployeeResetMemory(DispatchMessage{Text: tc.text}); got != tc.want {
			t.Fatalf("%q = %v", tc.text, got)
		}
	}
	if isEmployeeResetMemory(DispatchMessage{Text: "/reset-memory", ReferencedMessage: &DispatchReferencedMessage{}}) {
		t.Fatal("quoted command was executed")
	}
}
