package employeeverification

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/employeetask"
)

const agentDWSUID = "507523443"

// fakeFiles plays D1's provider read: the exact delivered message's own file
// resources, downloaded as the agent identity.
type fakeFiles struct {
	mu     sync.Mutex
	files  map[string][]DeliveredFile
	errs   map[string]error
	calls  []DeliveredMessage
	queues []string
	onRead func(DeliveredMessage)
}

func newFakeFiles() *fakeFiles {
	return &fakeFiles{files: map[string][]DeliveredFile{}, errs: map[string]error{}}
}

func (f *fakeFiles) ReadDeliveredFiles(_ context.Context, _ employeetask.Scope, _, _, queueTaskID string, m DeliveredMessage, _ int64) ([]DeliveredFile, error) {
	if f.onRead != nil {
		f.onRead(m)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, m)
	f.queues = append(f.queues, queueTaskID)
	if err := f.errs[m.MessageID]; err != nil {
		return nil, err
	}
	out := make([]DeliveredFile, 0, len(f.files[m.MessageID]))
	for _, file := range f.files[m.MessageID] {
		file.Data = append([]byte(nil), file.Data...)
		out = append(out, file)
	}
	return out, nil
}

func (f *fakeFiles) called() []DeliveredMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]DeliveredMessage(nil), f.calls...)
}

func (fx *fixture) deliveredVerifier(files *fakeFiles) *Verifier {
	return &Verifier{DB: fx.pool, Evidence: PGEvidence{Reader: fx.read, Files: files}}
}

// receipt records one sandbox send of the Run's queue execution.
func (fx *fixture) receipt(t *testing.T, run employeetask.Run, state, conversation, messageID, org string, nextAttempt bool) string {
	t.Helper()
	id := "sandbox-" + uuid.NewString()
	next := "NULL"
	if nextAttempt {
		next = "now()"
	}
	if _, err := fx.pool.Exec(context.Background(), `INSERT INTO sandbox_send_receipt(id,workspace_id,agent_id,task_id,client_action_id,input,payload_hash,target_conversation_id,observed_state,state,provider_conversation_id,provider_message_id,next_attempt_at)
VALUES($1,$2::uuid,$3::uuid,$4::uuid,$5,jsonb_build_object('dws_uid',$6::text,'dws_org_id',$7::text),'hash',$8,'delivered',$9,$10,$11,`+next+`)`,
		id, fx.scope.WorkspaceID, fx.scope.AgentID, run.QueueTaskID, "client-"+id, agentDWSUID, org, conversation, state, map[bool]string{true: conversation, false: ""}[state == "delivered"], messageID); err != nil {
		t.Fatal(err)
	}
	return id
}

func sumTask(t *testing.T, f *fixture) (employeetask.Task, employeetask.Run) {
	t.Helper()
	task := f.task(t, humanTaskSource, alice, "计算 1 到 100 的和并写进 sum.txt")
	checks := DeriveFromHumanText("ELMEM1 请用 Python 实际计算 1 到 100 的和，把结果写进文件 sum.txt，并把 sum.txt 发给我。完成标准：sum.txt 的内容应为 5050")
	if len(checks) != 1 {
		t.Fatalf("checks %+v", checks)
	}
	f.spec(t, task, OriginHumanCue, alice, checks)
	return task, f.run(t, task, employeetask.StateSucceeded, "已实际用 Python 计算 = 5050，文件已发送 delivered")
}

// MEM-01 on the real runtime: the sandbox sends sum.txt natively; the Host
// reads the delivered bytes as the agent and verifies them.
func TestVerificationReadsDeliveredFileBytes(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	task, run := sumTask(t, f)
	files := newFakeFiles()
	files.files["msgFile"] = []DeliveredFile{{FileID: "F1", Name: "sum.txt", Data: []byte("5050")}}
	f.receipt(t, run, "delivered", f.cid, "msgFile", "org-a", false)
	f.receipt(t, run, "delivered", f.cid, "msgText", "org-a", false) // the text reply: no file resources
	result, err := f.deliveredVerifier(files).VerifyRun(ctx, f.scope, task.ID, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Gate.Status != GatePassed || !result.Gate.Correct || !result.Intent || len(result.Records) != 1 {
		t.Fatalf("delivered file %+v", result)
	}
	r := result.Records[0]
	if r.EvidenceRef != "dws-message-file:msgFile/F1" || r.EvidenceSHA256 != sha256Hex([]byte("5050")) || r.Outcome != OutcomePassed {
		t.Fatalf("record %+v", r)
	}
	calls := files.called()
	if len(calls) != 2 || calls[0].DWSUID != agentDWSUID || calls[0].ConversationID != f.cid {
		t.Fatalf("provider reads %+v", calls)
	}
	for _, q := range files.queues {
		if q != run.QueueTaskID {
			t.Fatalf("read bound to queue %s, want %s", q, run.QueueTaskID)
		}
	}
	outcomes, err := f.store.ProcessVerifiedDistill(ctx, 10, DistillOptions{Memory: f.memory})
	if err != nil || len(outcomes) != 1 || outcomes[0].State != "captured" {
		t.Fatalf("distill %+v %v", outcomes, err)
	}
	rows, err := f.memory.Search(ctx, f.privateScope(alice), "", 10)
	if err != nil || len(rows) != 1 || !strings.Contains(rows[0].Insight, "sum.txt equals 5050") || rows[0].ExecutionID != run.ID {
		t.Fatalf("verified learning %+v %v", rows, err)
	}
}

func TestVerificationDeliveredFileWrongContentAndForeignTargets(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	files := newFakeFiles()
	files.files["msgWrong"] = []DeliveredFile{{FileID: "F2", Name: "sum.txt", Data: []byte("5049\n")}}
	task, run := sumTask(t, f)
	f.receipt(t, run, "delivered", f.cid, "msgWrong", "org-a", false)
	result, err := f.deliveredVerifier(files).VerifyRun(ctx, f.scope, task.ID, run.ID)
	if err != nil || result.Gate.Status != GateFailed || result.Intent || !strings.Contains(result.Gate.Failed[0].Detail, "is 5049, expected 5050") {
		t.Fatalf("wrong delivered content %+v %v", result, err)
	}

	// Correct bytes sent elsewhere, by another Run's queue, or under another
	// tenant are not this Run's delivery and are never read.
	files.files["msgElsewhere"] = []DeliveredFile{{FileID: "F3", Name: "sum.txt", Data: []byte("5050")}}
	files.files["msgOtherOrg"] = []DeliveredFile{{FileID: "F4", Name: "sum.txt", Data: []byte("5050")}}
	files.files["msgOtherRun"] = []DeliveredFile{{FileID: "F5", Name: "sum.txt", Data: []byte("5050")}}
	task2, run2 := sumTask(t, f)
	f.receipt(t, run2, "delivered", "cid-another-chat", "msgElsewhere", "org-a", false)
	f.receipt(t, run2, "delivered", f.cid, "msgOtherOrg", "org-b", false)
	f.receipt(t, run, "delivered", f.cid, "msgOtherRun", "org-a", false)
	before := len(files.called())
	result, err = f.deliveredVerifier(files).VerifyRun(ctx, f.scope, task2.ID, run2.ID)
	if err != nil || result.Gate.Status != GateFailed || result.Intent || result.Records[0].EvidenceRef != "run-artifacts:"+run2.ID {
		t.Fatalf("foreign deliveries counted %+v %v", result, err)
	}
	if after := len(files.called()); after != before {
		t.Fatalf("foreign deliveries were read: %d -> %d", before, after)
	}
}

func TestVerificationWaitsForUnconfirmedDelivery(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	files := newFakeFiles()
	files.files["msgLate"] = []DeliveredFile{{FileID: "F6", Name: "sum.txt", Data: []byte("5050")}}
	task, run := sumTask(t, f)
	receipt := f.receipt(t, run, "provider_accepted", f.cid, "", "org-a", true)
	if _, err := f.deliveredVerifier(files).VerifyRun(ctx, f.scope, task.ID, run.ID); !errors.Is(err, ErrEvidencePending) {
		t.Fatalf("judged before delivery confirmed: %v", err)
	}
	outcomes, err := f.deliveredVerifier(files).ProcessPending(ctx, 10)
	if err != nil || len(outcomes) != 1 || !errors.Is(outcomes[0].Err, ErrEvidencePending) {
		t.Fatalf("pending pass %+v %v", outcomes, err)
	}
	if n := f.count(t, `SELECT count(*) FROM employee_task_verification`) + f.count(t, `SELECT count(*) FROM employee_task_verification_attempt`); n != 0 {
		t.Fatalf("pending delivery wrote %d rows", n)
	}
	// An unknown receipt still being reconciled also waits.
	if _, err = f.pool.Exec(ctx, `UPDATE sandbox_send_receipt SET state='unknown' WHERE id=$1`, receipt); err != nil {
		t.Fatal(err)
	}
	if _, err = f.deliveredVerifier(files).VerifyRun(ctx, f.scope, task.ID, run.ID); !errors.Is(err, ErrEvidencePending) {
		t.Fatalf("reconciling unknown judged: %v", err)
	}
	if _, err = f.pool.Exec(ctx, `UPDATE sandbox_send_receipt SET state='delivered',provider_conversation_id=$2,provider_message_id='msgLate',next_attempt_at=NULL WHERE id=$1`, receipt, f.cid); err != nil {
		t.Fatal(err)
	}
	outcomes, err = f.deliveredVerifier(files).ProcessPending(ctx, 10)
	if err != nil || len(outcomes) != 1 || outcomes[0].Err != nil || outcomes[0].Gate != GatePassed || !outcomes[0].Intent {
		t.Fatalf("after confirmation %+v %v", outcomes, err)
	}
	if again, err := f.deliveredVerifier(files).ProcessPending(ctx, 10); err != nil || len(again) != 0 {
		t.Fatalf("rediscovered %+v %v", again, err)
	}
}

func TestVerificationUnreadableDeliveredFileIsUnknownNotFailed(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	files := newFakeFiles()
	files.files["msgSealed"] = []DeliveredFile{{Unreadable: "identity_changed"}}
	task, run := sumTask(t, f)
	f.receipt(t, run, "delivered", f.cid, "msgSealed", "org-a", false)
	result, err := f.deliveredVerifier(files).VerifyRun(ctx, f.scope, task.ID, run.ID)
	if err != nil || result.Gate.Status != GatePending || result.Intent || result.Records[0].Outcome != OutcomeUnknown || result.Records[0].EvidenceRef != "run-artifacts:"+run.ID+"/delivered" {
		t.Fatalf("unreadable delivery %+v %v", result, err)
	}
	// A transient provider failure writes nothing and is retried.
	task2, run2 := sumTask(t, f)
	f.receipt(t, run2, "delivered", f.cid, "msgFlaky", "org-a", false)
	files.errs["msgFlaky"] = errors.New("provider timeout")
	before := f.count(t, `SELECT count(*) FROM employee_task_verification`)
	if _, err = f.deliveredVerifier(files).VerifyRun(ctx, f.scope, task2.ID, run2.ID); err == nil {
		t.Fatal("transient provider error was judged")
	}
	if after := f.count(t, `SELECT count(*) FROM employee_task_verification`); after != before {
		t.Fatalf("transient error wrote records %d -> %d", before, after)
	}
}

func TestVerificationDeliveredReceiptSetChangeDiscards(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	files := newFakeFiles()
	files.files["msgA"] = []DeliveredFile{{FileID: "FA", Name: "sum.txt", Data: []byte("5050")}}
	task, run := sumTask(t, f)
	f.receipt(t, run, "delivered", f.cid, "msgA", "org-a", false)
	added := false
	files.onRead = func(DeliveredMessage) {
		if !added {
			added = true
			f.receipt(t, run, "delivered", f.cid, "msgB", "org-a", false)
		}
	}
	if _, err := f.deliveredVerifier(files).VerifyRun(ctx, f.scope, task.ID, run.ID); !errors.Is(err, ErrEvidence) {
		t.Fatalf("receipt set changed under the check: %v", err)
	}
	if n := f.count(t, `SELECT count(*) FROM employee_task_verification`); n != 0 {
		t.Fatalf("discarded result wrote %d records", n)
	}
	files.onRead = nil
	var wg sync.WaitGroup
	errs := make(chan error, 6)
	for range 6 {
		wg.Go(func() {
			_, err := f.deliveredVerifier(files).VerifyRun(ctx, f.scope, task.ID, run.ID)
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if n := f.count(t, `SELECT count(*) FROM employee_task_verification WHERE run_id=$1::uuid`, run.ID); n != 1 {
		t.Fatalf("records=%d", n)
	}
	if n := f.count(t, `SELECT count(*) FROM employee_task_verified_distill WHERE run_id=$1::uuid`, run.ID); n != 1 {
		t.Fatalf("intents=%d", n)
	}
}

// hostOnlyEvidence is a replica binary that cannot read provider deliveries.
type hostOnlyEvidence struct{ p PGEvidence }

func (h hostOnlyEvidence) RunArtifacts(ctx context.Context, q Querier, scope employeetask.Scope, taskID, runID string) ([]Artifact, error) {
	return h.p.RunArtifacts(ctx, q, scope, taskID, runID)
}
func (h hostOnlyEvidence) ReadArtifact(ctx context.Context, a Artifact) ([]byte, error) {
	return h.p.ReadArtifact(ctx, a)
}
func (h hostOnlyEvidence) RunDeliveries(ctx context.Context, q Querier, scope employeetask.Scope, taskID, runID string) ([]Delivery, error) {
	return h.p.RunDeliveries(ctx, q, scope, taskID, runID)
}

// Rolling deploy: an older replica judged the Run without deliveries. The
// newer generation rediscovers it once and its real evidence supersedes the
// older "absent" verdict, which stays for audit.
func TestVerificationOlderReplicaAbsenceYieldsToDeliveredEvidence(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	files := newFakeFiles()
	files.files["msgSum"] = []DeliveredFile{{FileID: "FS", Name: "sum.txt", Data: []byte("5050")}}
	task, run := sumTask(t, f)
	f.receipt(t, run, "delivered", f.cid, "msgSum", "org-a", false)
	old := &Verifier{DB: f.pool, Evidence: hostOnlyEvidence{p: PGEvidence{Reader: f.read}}}
	result, err := old.VerifyRun(ctx, f.scope, task.ID, run.ID)
	if err != nil || result.Gate.Status != GateFailed {
		t.Fatalf("older replica %+v %v", result, err)
	}
	// The older binary never wrote attempt rows.
	if _, err = f.pool.Exec(ctx, `DELETE FROM employee_task_verification_attempt WHERE run_id=$1::uuid`, run.ID); err != nil {
		t.Fatal(err)
	}
	outcomes, err := f.deliveredVerifier(files).ProcessPending(ctx, 10)
	if err != nil || len(outcomes) != 1 || outcomes[0].Err != nil || outcomes[0].Gate != GatePassed || !outcomes[0].Intent {
		t.Fatalf("newer generation %+v %v", outcomes, err)
	}
	gate, err := GateTx(ctx, f.pool, f.scope, task.ID, run.ID)
	if err != nil || gate.Status != GatePassed || len(gate.Failed) != 0 {
		t.Fatalf("gate %+v %v", gate, err)
	}
	if n := f.count(t, `SELECT count(*) FROM employee_task_verification WHERE run_id=$1::uuid AND evidence_ref LIKE 'run-artifacts:%'`, run.ID); n != 1 {
		t.Fatalf("audit absence record=%d", n)
	}
	// A later absent verdict from an old replica does not undo the pass.
	if _, err = old.VerifyRun(ctx, f.scope, task.ID, run.ID); err != nil {
		t.Fatal(err)
	}
	if gate, err = GateTx(ctx, f.pool, f.scope, task.ID, run.ID); err != nil || gate.Status != GatePassed {
		t.Fatalf("old replica undid the pass: %+v %v", gate, err)
	}
	if again, err := f.deliveredVerifier(files).ProcessPending(ctx, 10); err != nil || len(again) != 0 {
		t.Fatalf("rediscovered %+v %v", again, err)
	}
}
