package handler

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/dwsclient"
	"github.com/multica-ai/multica/server/internal/service/dingtalkresponse"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func fileOnlyNotice(f *dingTalkResponseFixture, m *employeeTestModel) {
	f.command.Event.Data.Messages[0].Text = "生成文件并发给我。文件发出后不用再发总结。"
	m.completionNotice = map[string]any{"mode": "if_not_delivered", "require_delivery": "file", "instruction_quote": "文件发出后不用再发总结"}
}

type fileNoticeProvider struct {
	sent chan dingtalkresponse.ActionInput
	employeeNoticeProvider
	deliveryCID   string
	file          bool
	verifyErr     error
	waitForCancel bool
	verifies      int
	onVerify      func()
}

func (p *fileNoticeProvider) Send(ctx context.Context, in dingtalkresponse.ActionInput, key string) (dwsclient.SendResult, error) {
	if p.sent != nil {
		p.sent <- in
	}
	return p.employeeNoticeProvider.Send(ctx, in, key)
}
func (p *fileNoticeProvider) Query(_ context.Context, in dingtalkresponse.ActionInput, _ string) (dwsclient.SendStatus, error) {
	cid := in.ConversationID
	if p.deliveryCID != "" {
		cid = p.deliveryCID
	}
	return dwsclient.SendStatus{State: "delivered", OpenConversationID: cid, OpenMessageID: "notice-provider-message"}, nil
}
func (p *fileNoticeProvider) VerifyMessageFile(ctx context.Context, _ dingtalkresponse.ActionInput, _ string) (bool, error) {
	p.verifies++
	if p.waitForCancel {
		<-ctx.Done()
		return false, ctx.Err()
	}
	if p.onVerify != nil {
		p.onVerify()
	}
	return p.file, p.verifyErr
}
func noticeReceipt(t *testing.T, f employeeNoticeFixture, p *fileNoticeProvider, state, cid string) {
	t.Helper()
	ctx := context.Background()
	svc := dingtalkresponse.NewService(testPool, p, nil)
	f.h.DingTalkResponses = svc
	in := f.sandboxInput()
	in.TaskID = f.queueID
	in.IssueID = ""
	in.ConversationID = f.command.Event.Data.Conversation.OpenConversationID
	receipt := protocol.DingTalkSendReceipt{ClientActionID: uuid.NewString(), State: state, PayloadHash: strings.Repeat("b", 64), OpenConversationID: cid}
	if state == "delivered" || state == "provider_accepted" {
		receipt.State = "delivered"
		receipt.OpenTaskID = "native-send"
		receipt.OpenMessageID = "client-claimed-id"
	}
	if err := svc.RecordSandboxReceipt(ctx, in, receipt); err != nil {
		t.Fatal(err)
	}
	if state != "delivered" {
		return
	}
	// Only the fake provider query may establish delivery. The client's delivered
	// flag above is persisted as provider_accepted until this worker runs.
	if _, err := testPool.Exec(ctx, `DELETE FROM response_action WHERE agent_id=$1::uuid`, f.agentID); err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	go svc.Run(runCtx)
	defer func() {
		cancel()
		if !svc.WaitWithTimeout(ctx, 5*time.Second) {
			t.Error("receipt verifier did not stop")
		}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var got string
		err := testPool.QueryRow(ctx, `SELECT state FROM sandbox_send_receipt WHERE task_id=$1::uuid AND client_action_id=$2`, f.queueID, receipt.ClientActionID).Scan(&got)
		if err != nil {
			t.Fatal(err)
		}
		if got == "delivered" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("receipt not verified", got)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
func TestEmployeeFileNoticeOnlySuppressesVerifiedFile(t *testing.T) {
	for _, tc := range []struct {
		name, state                string
		file, defaultPolicy        bool
		wantNotice, wantSuppressed bool
	}{
		{"verified-file", "delivered", true, false, true, true},
		{"delivered-text-ack", "delivered", false, false, true, false},
		{"client-accepted-is-not-delivery", "provider_accepted", true, false, false, false},
		{"pending", "pending", true, false, false, false},
		{"unknown", "unknown", true, false, true, false},
		{"summary-requested", "delivered", true, true, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var options []func(*dingTalkResponseFixture, *employeeTestModel)
			if !tc.defaultPolicy {
				options = append(options, fileOnlyNotice)
			}
			f := employeeNoticeDatabase(t, "succeeded", false, false, options...)
			p := &fileNoticeProvider{file: tc.file}
			noticeReceipt(t, f, p, tc.state, f.command.Event.Data.Conversation.OpenConversationID)
			if _, err := f.h.ReconcileEmployeeRunNotices(context.Background(), 100); err != nil {
				t.Fatal(err)
			}
			var n int
			if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM employee_run_notice WHERE run_id=$1::uuid`, f.runID).Scan(&n); err != nil {
				t.Fatal(err)
			}
			if (n == 1) != tc.wantNotice {
				t.Fatal("unexpected notice count", n)
			}
			if n == 0 {
				return
			}
			var state, reason, body string
			var action bool
			if err := testPool.QueryRow(context.Background(), `SELECT state,reason,body,action_id IS NOT NULL FROM employee_run_notice WHERE run_id=$1::uuid`, f.runID).Scan(&state, &reason, &body, &action); err != nil {
				t.Fatal(err)
			}
			if tc.wantSuppressed {
				if state != "suppressed" || reason != "native_file_delivered" || body != "" || action {
					t.Fatal(state, reason, body, action)
				}
			} else if state != "enqueued" || !action {
				t.Fatal(state, reason, action)
			}
			if tc.defaultPolicy && (!strings.Contains(body, "真实已存结果") || p.verifies != 0) {
				t.Fatal("ordinary result forwarding changed", body, p.verifies)
			}
			if tc.state == "unknown" && !strings.Contains(body, "尚未确认") {
				t.Fatal("unknown delivery claimed success", body)
			}
		})
	}
}
func TestEmployeeFileNoticeFailureAndCancellationRemainVisible(t *testing.T) {
	for _, state := range []string{"failed", "cancelled"} {
		t.Run(state, func(t *testing.T) {
			f := employeeNoticeDatabase(t, state, false, false, fileOnlyNotice)
			p := &fileNoticeProvider{file: true}
			noticeReceipt(t, f, p, "delivered", f.command.Event.Data.Conversation.OpenConversationID)
			if _, err := f.h.ReconcileEmployeeRunNotices(context.Background(), 100); err != nil {
				t.Fatal(err)
			}
			var got string
			if err := testPool.QueryRow(context.Background(), `SELECT state FROM employee_run_notice WHERE run_id=$1::uuid`, f.runID).Scan(&got); err != nil || got != "enqueued" {
				t.Fatal(got, err)
			}
			if p.verifies != 0 {
				t.Fatal("failure delivery depended on file verification")
			}
		})
	}
}
func TestEmployeeFileNoticeLateDeliveryFencesBeforeSend(t *testing.T) {
	f := employeeNoticeDatabase(t, "succeeded", false, false, fileOnlyNotice)
	if _, err := f.h.ReconcileEmployeeRunNotices(context.Background(), 100); err != nil {
		t.Fatal(err)
	}
	var in dingtalkresponse.ActionInput
	if err := testPool.QueryRow(context.Background(), `SELECT a.input FROM response_action a JOIN employee_run_notice n ON n.action_id=a.id WHERE n.run_id=$1::uuid`, f.runID).Scan(&in); err != nil {
		t.Fatal(err)
	}
	p := &fileNoticeProvider{file: true}
	noticeReceipt(t, f, p, "delivered", f.command.Event.Data.Conversation.OpenConversationID)
	err := f.h.BeforeEmployeeRunNoticeSend(context.Background(), in)
	var suppressed *dingtalkresponse.SuppressSendError
	if !errors.As(err, &suppressed) || suppressed.Reason != "native_file_delivered" {
		t.Fatal("late native file did not fence summary", err)
	}
}

func TestEmployeeFileNoticeKeepsOtherTargetAndDuplicateReceiptsSeparate(t *testing.T) {
	for _, other := range []bool{false, true} {
		t.Run(map[bool]string{false: "shim-sdk-duplicate", true: "other-conversation"}[other], func(t *testing.T) {
			f := employeeNoticeDatabase(t, "succeeded", false, false, fileOnlyNotice)
			p := &fileNoticeProvider{file: true}
			cid := f.command.Event.Data.Conversation.OpenConversationID
			if other {
				cid = "foreign-conversation"
				p.deliveryCID = cid
			}
			noticeReceipt(t, f, p, "delivered", cid)
			if !other {
				noticeReceipt(t, f, p, "pending", cid)
			}
			if _, err := f.h.ReconcileEmployeeRunNotices(context.Background(), 100); err != nil {
				t.Fatal(err)
			}
			var got string
			if err := testPool.QueryRow(context.Background(), `SELECT state FROM employee_run_notice WHERE run_id=$1::uuid`, f.runID).Scan(&got); err != nil {
				t.Fatal(err)
			}
			if other && (got != "enqueued" || p.verifies != 0) || !other && got != "suppressed" {
				t.Fatal(got, p.verifies)
			}
		})
	}
}
func TestEmployeeFileNoticeVerificationReleasesPoolAndRechecksAuthority(t *testing.T) {
	f := employeeNoticeDatabase(t, "succeeded", false, false, fileOnlyNotice)
	p := &fileNoticeProvider{file: true}
	noticeReceipt(t, f, p, "delivered", f.command.Event.Data.Conversation.OpenConversationID)
	config := testPool.Config().Copy()
	config.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	h := *f.h
	h.DB, h.TxStarter, h.Queries = pool, pool, db.New(pool)
	p.onVerify = func() {
		var n int
		if err := pool.QueryRow(context.Background(), `SELECT 1`).Scan(&n); err != nil {
			t.Error(err)
		}
		revokeEmployeeNoticePrincipal(t)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err = h.ReconcileEmployeeRunNotices(ctx, 100); err != nil {
		t.Fatal(err)
	}
	var reason string
	if err = testPool.QueryRow(context.Background(), `SELECT reason FROM employee_run_notice WHERE run_id=$1::uuid`, f.runID).Scan(&reason); err != nil || reason != "principal_revoked" {
		t.Fatal("lookup bypassed later revocation", reason, err)
	}
}
func TestEmployeeFileNoticeRolloutGateBlocksEnqueueAndSend(t *testing.T) {
	f := employeeNoticeDatabase(t, "succeeded", false, false, fileOnlyNotice)
	old := errors.New("live replica lacks employee-loop:4")
	f.h.EmployeeSceneWorker.ReplicaReady = func(context.Context) error { return old }
	if _, err := f.h.ReconcileEmployeeRunNotices(context.Background(), 100); !errors.Is(err, old) {
		t.Fatal(err)
	}
	f.h.EmployeeSceneWorker.ReplicaReady = func(context.Context) error { return nil }
	if _, err := f.h.ReconcileEmployeeRunNotices(context.Background(), 100); err != nil {
		t.Fatal(err)
	}
	var in dingtalkresponse.ActionInput
	if err := testPool.QueryRow(context.Background(), `SELECT a.input FROM response_action a JOIN employee_run_notice n ON n.action_id=a.id WHERE n.run_id=$1::uuid`, f.runID).Scan(&in); err != nil {
		t.Fatal(err)
	}
	f.h.EmployeeSceneWorker.ReplicaReady = func(context.Context) error { return old }
	if err := f.h.BeforeEmployeeRunNoticeSend(context.Background(), in); !errors.Is(err, old) {
		t.Fatal(err)
	}
}

func TestEmployeeFileNoticeMixedReceiptsWaitUntilFileIsSettled(t *testing.T) {
	for _, first := range []string{"delivered", "unknown"} {
		t.Run(first+"-plus-pending", func(t *testing.T) {
			f := employeeNoticeDatabase(t, "succeeded", false, false, fileOnlyNotice)
			p := &fileNoticeProvider{file: false}
			cid := f.command.Event.Data.Conversation.OpenConversationID
			noticeReceipt(t, f, p, first, cid)
			noticeReceipt(t, f, p, "pending", cid)
			if _, err := f.h.ReconcileEmployeeRunNotices(context.Background(), 100); err != nil {
				t.Fatal(err)
			}
			var n int
			if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM employee_run_notice WHERE run_id=$1::uuid`, f.runID).Scan(&n); err != nil || n != 0 {
				t.Fatal("pending file was hidden behind another receipt", n, err)
			}
		})
	}
}
func TestEmployeeFileNoticeLateUncertaintyRefreshesOnlyUnsubmittedText(t *testing.T) {
	for _, state := range []string{"unknown", "failed"} {
		t.Run(state, func(t *testing.T) {
			f := employeeNoticeDatabase(t, "succeeded", false, false, fileOnlyNotice)
			if _, err := f.h.ReconcileEmployeeRunNotices(context.Background(), 100); err != nil {
				t.Fatal(err)
			}
			var in dingtalkresponse.ActionInput
			if err := testPool.QueryRow(context.Background(), `SELECT a.input FROM response_action a JOIN employee_run_notice n ON n.action_id=a.id WHERE n.run_id=$1::uuid`, f.runID).Scan(&in); err != nil {
				t.Fatal(err)
			}
			p := &fileNoticeProvider{}
			noticeReceipt(t, f, p, "unknown", f.command.Event.Data.Conversation.OpenConversationID)
			if state == "failed" {
				if _, err := testPool.Exec(context.Background(), `UPDATE sandbox_send_receipt SET state='failed',error_code='provider_failed' WHERE task_id=$1::uuid`, f.queueID); err != nil {
					t.Fatal(err)
				}
			}
			if err := f.h.BeforeEmployeeRunNoticeSend(context.Background(), in); err == nil {
				t.Fatal("stale successful text was permitted after delivery failure")
			}
			var current dingtalkresponse.ActionInput
			var body string
			if err := testPool.QueryRow(context.Background(), `SELECT a.input,n.body FROM response_action a JOIN employee_run_notice n ON n.action_id=a.id WHERE n.run_id=$1::uuid`, f.runID).Scan(&current, &body); err != nil {
				t.Fatal(err)
			}
			if body == in.Text || current.Text != body || !strings.Contains(body, "任务返回内容（不作为送达确认）：\n> 真实已存结果") {
				t.Fatal("both durable bodies must change before retry", body)
			}
			if state == "unknown" && !strings.Contains(body, "尚未确认") || state == "failed" && !strings.Contains(body, "发送失败") {
				t.Fatal(body)
			}
			if err := f.h.BeforeEmployeeRunNoticeSend(context.Background(), in); err == nil {
				t.Fatal("stale worker did not reload modified text")
			} else {
				var suppressed *dingtalkresponse.SuppressSendError
				if errors.As(err, &suppressed) {
					t.Fatal("stale worker cancelled a refreshed pending action", err)
				}
			}
			if err := f.h.BeforeEmployeeRunNoticeSend(context.Background(), current); err != nil {
				t.Fatal("reloaded safe notice cannot proceed", err)
			}
		})
	}
}
func TestEmployeeFileNoticeProviderReadFailureDoesNotStarveNextRun(t *testing.T) {
	first := employeeNoticeDatabase(t, "succeeded", false, false, fileOnlyNotice)
	p := &fileNoticeProvider{verifyErr: errors.New("provider read unavailable")}
	noticeReceipt(t, first, p, "delivered", first.command.Event.Data.Conversation.OpenConversationID)
	if _, err := testPool.Exec(context.Background(), `UPDATE agent_runtime SET daemon_id=$2 WHERE id=(SELECT runtime_id FROM agent WHERE id=$1::uuid)`, first.agentID, "notice-first-"+uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	second := employeeNoticeDatabase(t, "failed", false, false)
	if _, err := first.h.ReconcileEmployeeRunNotices(context.Background(), 100); err != nil {
		t.Fatal("one file read blocked every later run", err)
	}
	var firstBody, secondBody string
	if err := testPool.QueryRow(context.Background(), `SELECT body FROM employee_run_notice WHERE run_id=$1::uuid`, first.runID).Scan(&firstBody); err != nil {
		t.Fatal(err)
	}
	if err := testPool.QueryRow(context.Background(), `SELECT body FROM employee_run_notice WHERE run_id=$1::uuid`, second.runID).Scan(&secondBody); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(firstBody, "尚未确认") || !strings.Contains(secondBody, "真实失败原因") {
		t.Fatal(firstBody, secondBody)
	}
}

func TestEmployeeFileNoticeWorkerReloadsChangedTextWithoutResending(t *testing.T) {
	for _, submitted := range []bool{false, true} {
		t.Run(map[bool]string{false: "pending-reloads", true: "accepted-only-queries"}[submitted], func(t *testing.T) {
			f := employeeNoticeDatabase(t, "succeeded", false, false, fileOnlyNotice)
			setNoticeExecutionOutput(t, f, noticeFileReadError)
			if _, err := f.h.ReconcileEmployeeRunNotices(context.Background(), 100); err != nil {
				t.Fatal(err)
			}
			var actionID, oldBody string
			if err := testPool.QueryRow(context.Background(), `SELECT action_id,body FROM employee_run_notice WHERE run_id=$1::uuid`, f.runID).Scan(&actionID, &oldBody); err != nil {
				t.Fatal(err)
			}
			p := &fileNoticeProvider{sent: make(chan dingtalkresponse.ActionInput, 3)}
			noticeReceipt(t, f, p, "unknown", f.command.Event.Data.Conversation.OpenConversationID)
			if _, err := testPool.Exec(context.Background(), `DELETE FROM response_action WHERE agent_id=$1::uuid AND id<>$2`, f.agentID, actionID); err != nil {
				t.Fatal(err)
			}
			if submitted {
				if _, err := testPool.Exec(context.Background(), `UPDATE response_action SET state='provider_accepted',provider_task_id='already-submitted' WHERE id=$1`, actionID); err != nil {
					t.Fatal(err)
				}
			}
			svc := dingtalkresponse.NewService(testPool, p, nil)
			svc.BeforeSend = f.h.BeforeEmployeeRunNoticeSend
			f.h.DingTalkResponses = svc
			ctx, cancel := context.WithCancel(context.Background())
			go svc.Run(ctx)
			defer func() {
				cancel()
				if !svc.WaitWithTimeout(context.Background(), 5*time.Second) {
					t.Error("worker did not stop")
				}
			}()
			waitEmployeeNoticeAction(t, actionID, "delivered", 10*time.Second)
			if submitted {
				if p.sends.Load() != 0 {
					t.Fatal("accepted action was submitted again")
				}
				var body string
				if err := testPool.QueryRow(context.Background(), `SELECT input->>'text' FROM response_action WHERE id=$1`, actionID).Scan(&body); err != nil || body != oldBody {
					t.Fatal("submitted input was rewritten", body, err)
				}
				return
			}
			if p.sends.Load() != 1 {
				t.Fatal("new notice was not sent exactly once", p.sends.Load())
			}
			select {
			case sent := <-p.sent:
				if sent.Text == oldBody || !strings.Contains(sent.Text, "尚未确认") || !strings.Contains(sent.Text, "任务返回内容（不作为送达确认）") || !strings.Contains(sent.Text, "cannot read: no such file or directory") {
					t.Fatal("worker sent stale text", sent.Text)
				}
			default:
				t.Fatal("send not captured")
			}
		})
	}
}

func TestEmployeeFileNoticeBlockingProviderLeavesDeadlineForCommit(t *testing.T) {
	first := employeeNoticeDatabase(t, "succeeded", false, false, fileOnlyNotice)
	p := &fileNoticeProvider{waitForCancel: true}
	noticeReceipt(t, first, p, "delivered", first.command.Event.Data.Conversation.OpenConversationID)
	if _, err := testPool.Exec(context.Background(), `UPDATE agent_runtime SET daemon_id=$2 WHERE id=(SELECT runtime_id FROM agent WHERE id=$1::uuid)`, first.agentID, "notice-blocking-"+uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	second := employeeNoticeDatabase(t, "failed", false, false)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	started := time.Now()
	if _, err := first.h.ReconcileEmployeeRunNotices(ctx, 100); err != nil {
		t.Fatal("blocking provider exhausted production notice context", err)
	}
	if ctx.Err() != nil || time.Since(started) >= 8*time.Second {
		t.Fatal("no SQL commit budget remained", ctx.Err(), time.Since(started))
	}
	var body string
	if err := testPool.QueryRow(context.Background(), `SELECT body FROM employee_run_notice WHERE run_id=$1::uuid`, first.runID).Scan(&body); err != nil || !strings.Contains(body, "尚未确认") {
		t.Fatal(body, err)
	}
	if err := testPool.QueryRow(context.Background(), `SELECT body FROM employee_run_notice WHERE run_id=$1::uuid`, second.runID).Scan(&body); err != nil || !strings.Contains(body, "真实失败原因") {
		t.Fatal("later Run starved", body, err)
	}
}

func TestEmployeeDefaultNoticePreservesCommittedLegacyQueueRecovery(t *testing.T) {
	f := employeeNoticeDatabase(t, "succeeded", false, false)
	ctx := context.Background()
	var raw []byte
	if err := testPool.QueryRow(ctx, `SELECT context FROM agent_task_queue WHERE id=$1::uuid`, f.queueID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var legacy map[string]any
	if err := json.Unmarshal(raw, &legacy); err != nil {
		t.Fatal(err)
	}
	// Reproduce the v3 queue bytes that committed before the v4 policy existed.
	strip := func(value map[string]any) {
		delete(value, employeeCompletionNoticeContextKey)
		if prompt, ok := value["direct_task_prompt"].(string); ok {
			value["direct_task_prompt"] = strings.ReplaceAll(prompt, "- COMPLETION NOTICE POLICY (Host verified): always\n", "")
		}
	}
	strip(legacy)
	if input, ok := legacy["employee_direct_input"].(map[string]any); ok {
		strip(input)
	}
	oldContext, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = testPool.Exec(ctx, `UPDATE agent_task_queue SET context=$2::jsonb WHERE id=$1::uuid`, f.queueID, oldContext); err != nil {
		t.Fatal(err)
	}
	// Preserve a genuinely old frozen tool schema and its matching recorded
	// request. Recovery must not rebuild the request from the new live schema.
	var snapshotRaw, journalRaw []byte
	if err = testPool.QueryRow(ctx, `SELECT input_snapshot,model_journal FROM employee_scene_job WHERE id=$1::uuid`, f.jobID).Scan(&snapshotRaw, &journalRaw); err != nil {
		t.Fatal(err)
	}
	var removeNewSchema func(any)
	removeNewSchema = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			delete(x, "completion_notice_policy")
			for _, item := range x {
				removeNewSchema(item)
			}
		case []any:
			for _, item := range x {
				removeNewSchema(item)
			}
		}
	}
	oldJSON := func(raw []byte) []byte {
		var v any
		if err := json.Unmarshal(raw, &v); err != nil {
			t.Fatal(err)
		}
		removeNewSchema(v)
		out, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	if _, err = testPool.Exec(ctx, `UPDATE employee_scene_job SET input_snapshot=$2::jsonb,model_journal=$3::jsonb WHERE id=$1::uuid`, f.jobID, oldJSON(snapshotRaw), oldJSON(journalRaw)); err != nil {
		t.Fatal(err)
	}
	// The model reply survived; only its Host tool checkpoint/outcome was lost.
	if _, err = testPool.Exec(ctx, `UPDATE employee_scene_job SET state='pending',outcome=NULL,tool_journal='{}',available_at=now() WHERE id=$1::uuid`, f.jobID); err != nil {
		t.Fatal(err)
	}
	if worked, err := f.h.EmployeeSceneWorker.ProcessNext(ctx); err != nil || !worked {
		t.Fatal(worked, err)
	}
	var saved employeeSavedOutcome
	var state string
	if err = testPool.QueryRow(ctx, `SELECT state,outcome FROM employee_scene_job WHERE id=$1::uuid`, f.jobID).Scan(&state, &saved); err != nil {
		t.Fatal(err)
	}
	if state != "completed" || saved.Failure != "" || saved.Outcome.Kind != "dispatched" {
		t.Fatal("legacy admitted task was changed by default policy", state, saved.Failure, saved.Outcome.Kind)
	}
	var queues, runs int
	var same bool
	if err = testPool.QueryRow(ctx, `SELECT (SELECT count(*) FROM agent_task_queue WHERE agent_id=$1::uuid AND context->>'type'='employee_direct'),(SELECT count(*) FROM employee_task_run WHERE agent_id=$1::uuid),(SELECT context=$3::jsonb FROM agent_task_queue WHERE id=$2::uuid)`, f.agentID, f.queueID, oldContext).Scan(&queues, &runs, &same); err != nil {
		t.Fatal(err)
	}
	if queues != 1 || runs != 1 || !same || f.model.calls != 1 {
		t.Fatal("recovery changed legacy execution or repeated reasoning", queues, runs, same, f.model.calls)
	}
}
