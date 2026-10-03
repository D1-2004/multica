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

const reportCriteria = "验收标准：report.csv 共 3 行数据，report.csv 的列为 姓名、部门、状态；report.csv 里要包含「研发部」"

func reportChecks(t *testing.T) []Check {
	t.Helper()
	checks := DeriveFromHumanText(reportCriteria)
	if len(checks) != 3 {
		t.Fatalf("derived %d checks from the requester's criteria: %+v", len(checks), checks)
	}
	return checks
}

func TestVerificationRejectsAssistantClaimAndMismatchedEvidence(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	task := f.task(t, humanTaskSource, alice, "整理本周人员状态表")
	f.spec(t, task, OriginHumanCue, alice, reportChecks(t))

	// The assistant claims success and the queue completed (exit 0), but no
	// artifact exists: the claim is not evidence.
	claimed := f.run(t, task, employeetask.StateSucceeded, "PASS: report.csv 已生成，共 3 行数据，所有检查通过。")
	result, err := f.verifier().VerifyRun(ctx, f.scope, task.ID, claimed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Gate.Status != GateFailed || result.Intent || len(result.Records) != 3 {
		t.Fatalf("assistant claim verified: %+v", result)
	}
	for _, r := range result.Records {
		if r.Outcome != OutcomeFailed || !strings.Contains(r.Detail, "produced no artifact named report.csv") || r.EvidenceRef != "run-artifacts:"+claimed.ID {
			t.Fatalf("claim record %+v", r)
		}
	}

	// Correct bytes bound to a different Task, to the right Run but a forged
	// queue, or to an older goal revision are not this Run's evidence.
	other := f.task(t, humanTaskSource, alice, "另一件事")
	otherRun := f.run(t, other, employeetask.StateSucceeded, "done")
	f.artifact(t, other.ID, otherRun, "report.csv", []byte(reportCSV))
	mismatched := f.run(t, task, employeetask.StateSucceeded, "PASS again")
	f.artifactBound(t, task.ID, mismatched.ID, uuid.NewString(), mismatched.GoalRevision, "report.csv", []byte(reportCSV))
	f.artifactBound(t, task.ID, mismatched.ID, mismatched.QueueTaskID, mismatched.GoalRevision+7, "report.csv", []byte(reportCSV))
	result, err = f.verifier().VerifyRun(ctx, f.scope, task.ID, mismatched.ID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Gate.Status != GateFailed || result.Intent {
		t.Fatalf("mismatched evidence verified: %+v", result.Gate)
	}
	for _, r := range result.Records {
		if r.Outcome != OutcomeFailed || !strings.HasPrefix(r.EvidenceRef, "run-artifacts:") {
			t.Fatalf("mismatched record %+v", r)
		}
	}
	if n := f.count(t, `SELECT count(*) FROM employee_task_verified_distill`); n != 0 {
		t.Fatalf("intents=%d", n)
	}
	feedback, err := f.store.Feedback(ctx, f.scope, task.ID, 8)
	if err != nil || len(feedback) != 3 || feedback[0].RunID != mismatched.ID {
		t.Fatalf("feedback %+v %v", feedback, err)
	}
	block := FormatFeedback(feedback)
	if !strings.Contains(block, "FAILED artifact_contents") || !strings.Contains(block, "report.csv") || !strings.Contains(block, "not model claims") {
		t.Fatalf("feedback block %q", block)
	}
}

func TestVerificationCurrentGoalAndArtifactContents(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	task := f.task(t, humanTaskSource, alice, "整理本周人员状态表")
	f.spec(t, task, OriginHumanCue, alice, reportChecks(t))

	short := f.run(t, task, employeetask.StateSucceeded, "done")
	f.artifact(t, task.ID, short, "report.csv", []byte("姓名,部门,状态\n张三,研发部,已完成\n李四,产品部,已完成\n"))
	result, err := f.verifier().VerifyRun(ctx, f.scope, task.ID, short.ID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Gate.Status != GateFailed || result.Intent || len(result.Gate.Failed) != 1 || !strings.Contains(result.Gate.Failed[0].Detail, "has 2 data rows, expected 3") {
		t.Fatalf("short table: %+v", result.Gate)
	}

	good := f.run(t, task, employeetask.StateSucceeded, "done")
	f.artifact(t, task.ID, good, "report.csv", []byte("\xef\xbb\xbf"+strings.ReplaceAll(reportCSV, "\n", "\r\n")))
	result, err = f.verifier().VerifyRun(ctx, f.scope, task.ID, good.ID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Gate.Status != GatePassed || !result.Gate.Correct || !result.Intent {
		t.Fatalf("correct artifact: %+v", result)
	}
	gate, err := GateTx(ctx, f.pool, f.scope, task.ID, good.ID)
	if err != nil || gate.Status != GatePassed {
		t.Fatalf("gate %+v %v", gate, err)
	}
	if feedback, err := f.store.Feedback(ctx, f.scope, task.ID, 8); err != nil || len(feedback) != 0 {
		t.Fatalf("latest run passed but feedback %+v %v", feedback, err)
	}

	// A verified human correction moves the goal; the old Run is no longer
	// the current goal's evidence.
	current, err := f.tasks.Get(ctx, f.scope, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = f.tasks.AppendInput(ctx, f.scope, task.ID, employeetask.InputParams{Source: employeetask.Source{Namespace: "test", Key: uuid.NewString()}, ActorRef: alice, Body: "改成按部门分组",
		Correction: &employeetask.Definition{Goal: "按部门分组整理人员状态表"}, ExpectedVersion: current.Version}); err != nil {
		t.Fatal(err)
	}
	before := f.count(t, `SELECT count(*) FROM employee_task_verification`)
	if _, err = f.verifier().VerifyRun(ctx, f.scope, task.ID, good.ID); !errors.Is(err, ErrStaleGoal) {
		t.Fatalf("old goal run verified: %v", err)
	}
	if after := f.count(t, `SELECT count(*) FROM employee_task_verification`); after != before {
		t.Fatalf("stale goal wrote records: %d -> %d", before, after)
	}
}

func TestVerificationDoesNotTreatDeliveredAsCorrect(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	delivery := []Check{{Kind: KindDeliveryReceipt, Delivery: "origin_reply"}}

	onlyDelivered := f.task(t, humanTaskSource, alice, "把结果发给我")
	f.spec(t, onlyDelivered, OriginHostFixture, "host:fixture", delivery)
	run := f.run(t, onlyDelivered, employeetask.StateSucceeded, "已发送")
	f.delivery(t, onlyDelivered, run, "delivered", "msg-1")
	result, err := f.verifier().VerifyRun(ctx, f.scope, onlyDelivered.ID, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Gate.Status != GatePassed || result.Gate.Correct || result.Intent || result.Records[0].Outcome != OutcomePassed {
		t.Fatalf("delivery became correctness: %+v", result)
	}

	wrongButDelivered := f.task(t, humanTaskSource, alice, "整理并发送")
	f.spec(t, wrongButDelivered, OriginHostFixture, "host:fixture", append([]Check{{Kind: KindArtifactContents, File: "summary.md", Contains: []string{"合计"}}}, delivery...))
	run = f.run(t, wrongButDelivered, employeetask.StateSucceeded, "已发送 summary.md")
	f.artifact(t, wrongButDelivered.ID, run, "summary.md", []byte("小计 12"))
	f.delivery(t, wrongButDelivered, run, "delivered", "msg-2")
	if result, err = f.verifier().VerifyRun(ctx, f.scope, wrongButDelivered.ID, run.ID); err != nil || result.Gate.Status != GateFailed || result.Intent {
		t.Fatalf("delivered wrong file: %+v %v", result, err)
	}

	pending := f.task(t, humanTaskSource, alice, "发送中")
	f.spec(t, pending, OriginHostFixture, "host:fixture", append([]Check{{Kind: KindArtifactContents, File: "summary.md"}}, delivery...))
	run = f.run(t, pending, employeetask.StateSucceeded, "sent")
	f.artifact(t, pending.ID, run, "summary.md", []byte("合计 12"))
	f.delivery(t, pending, run, "provider_accepted", "")
	if result, err = f.verifier().VerifyRun(ctx, f.scope, pending.ID, run.ID); err != nil || result.Gate.Status != GatePending || result.Intent {
		t.Fatalf("accepted is not a receipt: %+v %v", result, err)
	}
	if n := f.count(t, `SELECT count(*) FROM employee_task_verified_distill`); n != 0 {
		t.Fatalf("intents=%d", n)
	}
}

func TestVerificationTwoWorkersSameEvidenceOneRecord(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	task := f.task(t, humanTaskSource, alice, "整理本周人员状态表")
	f.spec(t, task, OriginHumanCue, alice, reportChecks(t))
	run := f.run(t, task, employeetask.StateSucceeded, "done")
	f.artifact(t, task.ID, run, "report.csv", []byte(reportCSV))
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Go(func() {
			result, err := f.verifier().VerifyRun(ctx, f.scope, task.ID, run.ID)
			if err == nil && (!result.Intent || result.Gate.Status != GatePassed) {
				err = errors.New("worker did not converge on the passing result")
			}
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
	if n := f.count(t, `SELECT count(*) FROM employee_task_verification WHERE run_id=$1::uuid`, run.ID); n != 3 {
		t.Fatalf("records=%d, want one per check", n)
	}
	if n := f.count(t, `SELECT count(*) FROM employee_task_verified_distill WHERE run_id=$1::uuid`, run.ID); n != 1 {
		t.Fatalf("intents=%d", n)
	}
}

func TestVerificationCheckerVersionAndSameIDConflict(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	task := f.task(t, humanTaskSource, alice, "整理本周人员状态表")
	f.spec(t, task, OriginHumanCue, alice, reportChecks(t))
	run := f.run(t, task, employeetask.StateSucceeded, "done")
	attachment := f.artifact(t, task.ID, run, "report.csv", []byte(reportCSV))
	if _, err := f.verifier().VerifyRun(ctx, f.scope, task.ID, run.ID); err != nil {
		t.Fatal(err)
	}
	newer := f.verifier()
	newer.CheckerVersion = "employee-verification/v2"
	if _, err := newer.VerifyRun(ctx, f.scope, task.ID, run.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("different checker version overwrote or duplicated: %v", err)
	}
	// Same evidence identity, different content: retained result wins.
	tampered := []byte(strings.Replace(reportCSV, "王五", "赵六", 1))
	if _, err := f.pool.Exec(ctx, `UPDATE employee_task_artifact SET sha256=$2 WHERE attachment_id=$1::uuid`, attachment, sha256Hex(tampered)); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.objects[attachment] = tampered
	f.mu.Unlock()
	if _, err := f.verifier().VerifyRun(ctx, f.scope, task.ID, run.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed bytes under the same evidence id: %v", err)
	}
	var versions []string
	rows, err := f.pool.Query(ctx, `SELECT DISTINCT checker_version FROM employee_task_verification WHERE run_id=$1::uuid`, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var v string
		_ = rows.Scan(&v)
		versions = append(versions, v)
	}
	rows.Close()
	if len(versions) != 1 || versions[0] != CheckerVersion || f.count(t, `SELECT count(*) FROM employee_task_verification WHERE run_id=$1::uuid`, run.ID) != 3 {
		t.Fatalf("retained records changed: %v", versions)
	}
}

func TestVerificationFencesTenantRequesterAndCancelledTask(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	task := f.task(t, humanTaskSource, alice, "整理本周人员状态表")
	if _, err := f.store.SetSpec(ctx, f.scope, task.ID, SetSpecParams{Origin: OriginHumanCue, SourceRef: "msg:mallory", AuthorRef: mallory, Checks: reportChecks(t)}); !errors.Is(err, ErrNotRequester) {
		t.Fatalf("another participant's words became the contract: %v", err)
	}
	if _, err := f.store.SetSpec(ctx, f.scope, task.ID, SetSpecParams{Origin: OriginAutomation, SourceRef: "routine:1", AuthorRef: "routine:1", Checks: reportChecks(t)}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("automation config governed a human task: %v", err)
	}
	proposed, err := f.store.SetSpec(ctx, f.scope, task.ID, SetSpecParams{Origin: OriginModelProposed, SourceRef: "model:criteria", AuthorRef: "model", Checks: DeriveFromModelCriteria([]string{"report.csv 共 3 行数据"})})
	if err != nil || proposed.State != SpecProposed {
		t.Fatalf("proposal %+v %v", proposed, err)
	}
	run := f.run(t, task, employeetask.StateSucceeded, "done")
	f.artifact(t, task.ID, run, "report.csv", []byte(reportCSV))
	if result, err := f.verifier().VerifyRun(ctx, f.scope, task.ID, run.ID); err != nil || result.Gate.Status != GateNone {
		t.Fatalf("an unconfirmed model proposal gated work: %+v %v", result, err)
	}
	if _, err = f.store.ConfirmSpec(ctx, f.scope, task.ID, ConfirmParams{ExpectedRevision: proposed.Revision, ConfirmerRef: mallory, SourceRef: "msg:confirm-mallory"}); !errors.Is(err, ErrNotRequester) {
		t.Fatalf("non-requester confirmed: %v", err)
	}
	confirmed, err := f.store.ConfirmSpec(ctx, f.scope, task.ID, ConfirmParams{ExpectedRevision: proposed.Revision, ConfirmerRef: alice, SourceRef: "msg:confirm-alice"})
	if err != nil || confirmed.State != SpecActive || confirmed.Revision != 2 {
		t.Fatalf("confirm %+v %v", confirmed, err)
	}

	// Tenant fence: the scene directory no longer maps this tenant.
	if _, err = f.pool.Exec(ctx, `UPDATE agent_scene SET tenant_org_id='org-b' WHERE id=$1::uuid`, f.scope.Scene.SceneID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.verifier().VerifyRun(ctx, f.scope, task.ID, run.ID); !errors.Is(err, ErrStaleTenant) {
		t.Fatalf("stale tenant verified: %v", err)
	}
	if _, err = f.pool.Exec(ctx, `UPDATE agent_scene SET tenant_org_id='org-a' WHERE id=$1::uuid`, f.scope.Scene.SceneID); err != nil {
		t.Fatal(err)
	}
	// A cancelled Task is never verified, even with correct bytes.
	if _, err = f.pool.Exec(ctx, `UPDATE employee_task SET state='cancelled' WHERE id=$1::uuid`, task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.verifier().VerifyRun(ctx, f.scope, task.ID, run.ID); !errors.Is(err, ErrTaskCancelled) {
		t.Fatalf("cancelled task verified: %v", err)
	}
	if n := f.count(t, `SELECT count(*) FROM employee_task_verification`); n != 0 {
		t.Fatalf("fenced verification wrote %d records", n)
	}
	// A different scope cannot read or write this Task's contract.
	wrong := f.scope
	wrong.TenantOrgID = "org-b"
	if _, err = f.store.Spec(ctx, wrong, task.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-tenant spec read: %v", err)
	}
}

func TestVerificationSpecChangeWhileCheckingConflicts(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	task := f.task(t, humanTaskSource, alice, "整理本周人员状态表")
	first := f.spec(t, task, OriginHumanCue, alice, reportChecks(t))
	run := f.run(t, task, employeetask.StateSucceeded, "done")
	f.artifact(t, task.ID, run, "report.csv", []byte(reportCSV))
	changed := false
	f.onRead = func(Artifact) {
		if changed {
			return
		}
		changed = true
		// The requester changes the criteria while the check is running.
		if _, err := f.store.SetSpec(ctx, f.scope, task.ID, SetSpecParams{Origin: OriginHumanCue, SourceRef: "msg:new-criteria", AuthorRef: alice, ExpectedRevision: first.Revision,
			Checks: DeriveFromHumanText("完成标准：report.csv 共 4 行数据")}); err != nil {
			t.Error(err)
		}
	}
	if _, err := f.verifier().VerifyRun(ctx, f.scope, task.ID, run.ID); !errors.Is(err, ErrSpecChanged) {
		t.Fatalf("result against a replaced spec was kept: %v", err)
	}
	if n := f.count(t, `SELECT count(*) FROM employee_task_verification`); n != 0 {
		t.Fatalf("discarded result wrote %d records", n)
	}
	f.onRead = nil
	result, err := f.verifier().VerifyRun(ctx, f.scope, task.ID, run.ID)
	if err != nil || result.Gate.Status != GateFailed || result.Gate.SpecRevision != 2 {
		t.Fatalf("new spec result %+v %v", result.Gate, err)
	}

	// New evidence appearing during the check is also a discard.
	other := f.task(t, humanTaskSource, alice, "另一份")
	f.spec(t, other, OriginHumanCue, alice, reportChecks(t))
	otherRun := f.run(t, other, employeetask.StateSucceeded, "done")
	f.artifact(t, other.ID, otherRun, "report.csv", []byte(reportCSV))
	added := false
	f.onRead = func(Artifact) {
		if !added {
			added = true
			f.artifact(t, other.ID, otherRun, "report.csv", []byte("姓名,部门,状态\n"))
		}
	}
	if _, err = f.verifier().VerifyRun(ctx, f.scope, other.ID, otherRun.ID); !errors.Is(err, ErrEvidence) {
		t.Fatalf("evidence changed under the check: %v", err)
	}
}

func TestVerificationUnknownKindFailsClosed(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	task := f.task(t, humanTaskSource, alice, "检查网址")
	// A newer binary wrote a kind this binary does not know.
	if _, err := f.pool.Exec(ctx, `INSERT INTO employee_task_verification_spec(workspace_id,agent_id,tenant_org_id,task_id,revision,origin,state,source_ref,author_ref,checks,spec_digest)
VALUES($1::uuid,$2::uuid,$3,$4::uuid,1,'host_fixture','active','future','host','[{"id":"chk_000000000000000000000001","kind":"url","url":"https://example.com"}]',$5)`,
		f.scope.WorkspaceID, f.scope.AgentID, f.scope.TenantOrgID, task.ID, sha256Hex([]byte("future"))); err != nil {
		t.Fatal(err)
	}
	run := f.run(t, task, employeetask.StateSucceeded, "URL works, PASS")
	result, err := f.verifier().VerifyRun(ctx, f.scope, task.ID, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Gate.Status != GateFailed || len(result.Records) != 1 || result.Records[0].Outcome != OutcomeFailed || result.Records[0].CheckKind != "url" || result.Intent {
		t.Fatalf("unknown kind did not fail closed: %+v", result)
	}
	if _, err = f.store.SetSpec(ctx, f.scope, task.ID, SetSpecParams{Origin: OriginHostFixture, SourceRef: "x", AuthorRef: "host", ExpectedRevision: 1, Checks: []Check{{Kind: "url", File: "a.txt"}}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown kind accepted on write: %v", err)
	}
}

func TestOrdinaryRunsProduceZeroVerified(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	plain := f.task(t, humanTaskSource, alice, "随便聊聊")
	run := f.run(t, plain, employeetask.StateSucceeded, "PASS, verified, all checks green")
	f.artifact(t, plain.ID, run, "report.csv", []byte(reportCSV))
	if result, err := f.verifier().VerifyRun(ctx, f.scope, plain.ID, run.ID); err != nil || result.Gate.Status != GateNone || len(result.Records) != 0 {
		t.Fatalf("ordinary success verified: %+v %v", result, err)
	}
	for state, want := range map[employeetask.State]error{employeetask.StateFailed: ErrNotVerifiable, employeetask.StateCancelled: ErrTaskCancelled} {
		task := f.task(t, humanTaskSource, alice, "会失败的事")
		f.spec(t, task, OriginHumanCue, alice, reportChecks(t))
		run := f.run(t, task, state, "PASS")
		f.artifact(t, task.ID, run, "report.csv", []byte(reportCSV))
		if _, err := f.verifier().VerifyRun(ctx, f.scope, task.ID, run.ID); !errors.Is(err, want) {
			t.Fatalf("%s run verified: %v", state, err)
		}
	}
	outcomes, err := f.store.ProcessVerifiedDistill(ctx, 100, DistillOptions{Memory: f.memory})
	if err != nil || len(outcomes) != 0 {
		t.Fatalf("ordinary runs distilled: %+v %v", outcomes, err)
	}
	if n := f.count(t, `SELECT count(*) FROM employee_learning WHERE record->>'source'='execution'`); n != 0 {
		t.Fatalf("verified learnings=%d", n)
	}
	if n := f.count(t, `SELECT count(*) FROM employee_task_verification`); n != 0 {
		t.Fatalf("records=%d", n)
	}
}

func TestSetSpecReplayAndConflict(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	task := f.task(t, humanTaskSource, alice, "整理本周人员状态表")
	p := SetSpecParams{Origin: OriginHumanCue, SourceRef: "employee-message:r1/m1", AuthorRef: alice, Checks: reportChecks(t)}
	first, err := f.store.SetSpec(ctx, f.scope, task.ID, p)
	if err != nil || first.Revision != 1 {
		t.Fatalf("%+v %v", first, err)
	}
	replay, err := f.store.SetSpec(ctx, f.scope, task.ID, p)
	if err != nil || replay.Revision != 1 || replay.Digest != first.Digest {
		t.Fatalf("replay %+v %v", replay, err)
	}
	p.Checks = DeriveFromHumanText("完成标准：report.csv 共 9 行数据")
	if _, err = f.store.SetSpec(ctx, f.scope, task.ID, p); !errors.Is(err, ErrConflict) {
		t.Fatalf("same source, different content: %v", err)
	}
	p.SourceRef = "employee-message:r2/m2"
	if _, err = f.store.SetSpec(ctx, f.scope, task.ID, p); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale expected revision: %v", err)
	}
	p.ExpectedRevision = 1
	second, err := f.store.SetSpec(ctx, f.scope, task.ID, p)
	if err != nil || second.Revision != 2 {
		t.Fatalf("%+v %v", second, err)
	}
	if _, err = f.store.SetSpec(ctx, f.scope, task.ID, SetSpecParams{Origin: OriginModelProposed, SourceRef: "model:x", AuthorRef: "model", ExpectedRevision: 2, Checks: p.Checks[:1]}); !errors.Is(err, ErrConflict) {
		t.Fatalf("model proposal replaced an active contract: %v", err)
	}
	// The first source still replays its own historical revision.
	if again, err := f.store.SetSpec(ctx, f.scope, task.ID, SetSpecParams{Origin: OriginHumanCue, SourceRef: "employee-message:r1/m1", AuthorRef: alice, Checks: reportChecks(t)}); err != nil || again.Revision != 1 {
		t.Fatalf("historical replay %+v %v", again, err)
	}
	current, err := f.store.Spec(ctx, f.scope, task.ID)
	if err != nil || current.Revision != 2 {
		t.Fatalf("current %+v %v", current, err)
	}
}
