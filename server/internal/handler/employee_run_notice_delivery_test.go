package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/dwsclient"
	"github.com/multica-ai/multica/server/internal/service/dingtalkresponse"
)

func TestEmployeeRunNoticeConcurrentFirstAdmission(t *testing.T) {
	f := employeeNoticeDatabase(t, "succeeded", false, false)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Go(func() {
			restarted := *f.h
			_, err := restarted.ReconcileEmployeeRunNotices(context.Background(), 100)
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
	var notices, actions int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*),count(a.id) FROM employee_run_notice n JOIN response_action a ON a.id=n.action_id WHERE n.run_id=$1::uuid`, f.runID).Scan(&notices, &actions); err != nil || notices != 1 || actions != 1 {
		t.Fatal(notices, actions, err)
	}
	if f.model.calls != 1 {
		t.Fatal("delivery repeated computation", f.model.calls)
	}
}

func revokeEmployeeNoticePrincipal(t *testing.T) {
	t.Helper()
	member, err := testHandler.getWorkspaceMember(context.Background(), testUserID, testWorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = testPool.Exec(context.Background(), `DELETE FROM member WHERE id=$1`, member.ID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `INSERT INTO member(id,workspace_id,user_id,role) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, member.ID, member.WorkspaceID, member.UserID, member.Role)
	})
}

func TestEmployeeRunNoticeRevocationAndCorruptBindingsSuppressBeforeEnqueue(t *testing.T) {
	for _, change := range []string{"tenant", "principal", "invoke", "identity", "requester", "job_scene", "source", "queue_principal", "queue_scene", "scene_kind", "endpoint", "archive"} {
		t.Run(change, func(t *testing.T) {
			f := employeeNoticeDatabase(t, "succeeded", false, false)
			ctx := context.Background()
			var err error
			switch change {
			case "tenant":
				_, err = testPool.Exec(ctx, `UPDATE agent_dingtalk_identity SET org_id='revoked-org' WHERE agent_id=$1::uuid`, f.agentID)
			case "principal":
				revokeEmployeeNoticePrincipal(t)
			case "invoke":
				_, err = testPool.Exec(ctx, `UPDATE agent SET permission_mode='private',owner_id=NULL WHERE id=$1::uuid`, f.agentID)
			case "identity":
				_, err = testPool.Exec(ctx, `UPDATE agent_dingtalk_identity SET dws_uid='another-employee' WHERE agent_id=$1::uuid`, f.agentID)
			case "requester":
				_, err = testPool.Exec(ctx, `UPDATE employee_task SET requester_ref='another-requester' WHERE agent_id=$1::uuid`, f.agentID)
			case "job_scene":
				_, err = testPool.Exec(ctx, `UPDATE employee_scene_job SET scene_id=$2::uuid WHERE id=$1::uuid`, f.jobID, uuid.NewString())
			case "source":
				_, err = testPool.Exec(ctx, `UPDATE agent_task_queue SET context=jsonb_set(context,'{employee_source_ref}','"another-receipt/message"') WHERE id=$1::uuid`, f.queueID)
			case "queue_principal":
				_, err = testPool.Exec(ctx, `UPDATE agent_task_queue SET context=jsonb_set(context,'{direct_principal_id}',to_jsonb($2::text)) WHERE id=$1::uuid`, f.queueID, uuid.NewString())
			case "queue_scene":
				_, err = testPool.Exec(ctx, `UPDATE agent_task_queue SET context=jsonb_set(context,'{agent_scene,scene_id}',to_jsonb($2::text)) WHERE id=$1::uuid`, f.queueID, uuid.NewString())
			case "scene_kind":
				_, err = testPool.Exec(ctx, `UPDATE agent_scene SET scene_kind='enterprise' WHERE agent_id=$1::uuid`, f.agentID)
			case "endpoint":
				_, err = testPool.Exec(ctx, `DELETE FROM agent_dispatch_endpoint WHERE agent_id=$1::uuid`, f.agentID)
			case "archive":
				_, err = testPool.Exec(ctx, `UPDATE agent SET archived_at=now() WHERE id=$1::uuid`, f.agentID)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.h.ReconcileEmployeeRunNotices(ctx, 100); err != nil {
				t.Fatal(err)
			}
			var state, reason, body string
			var hasAction bool
			if err = testPool.QueryRow(ctx, `SELECT state,reason,body,action_id IS NOT NULL FROM employee_run_notice WHERE run_id=$1::uuid`, f.runID).Scan(&state, &reason, &body, &hasAction); err != nil {
				t.Fatal(err)
			}
			if state != "suppressed" || reason == "" || body != "" || hasAction {
				t.Fatal(state, reason, body, hasAction)
			}
		})
	}
}

func TestEmployeeRunNoticeOutboxAndNoticeCommitAtomically(t *testing.T) {
	for _, table := range []string{"response_action", "employee_run_notice"} {
		t.Run(table, func(t *testing.T) {
			f := employeeNoticeDatabase(t, "succeeded", false, false)
			ctx := context.Background()
			name := "notice_reject_" + strings.ReplaceAll(uuid.NewString(), "-", "")
			ddl := fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.agent_id='%s'::uuid THEN RAISE EXCEPTION 'injected notice failure'; END IF; RETURN NEW; END $$`, name, f.agentID)
			if _, err := testPool.Exec(ctx, ddl); err != nil {
				t.Fatal(err)
			}
			if _, err := testPool.Exec(ctx, fmt.Sprintf(`CREATE TRIGGER %s BEFORE INSERT ON %s FOR EACH ROW EXECUTE FUNCTION %s()`, name, table, name)); err != nil {
				t.Fatal(err)
			}
			cleanup := func() {
				_, _ = testPool.Exec(ctx, fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON %s`, name, table))
				_, _ = testPool.Exec(ctx, fmt.Sprintf(`DROP FUNCTION IF EXISTS %s()`, name))
			}
			t.Cleanup(cleanup)
			if _, err := f.h.ReconcileEmployeeRunNotices(ctx, 100); err == nil {
				t.Fatal("outbox transaction failure ignored")
			}
			var notices, actions int
			if err := testPool.QueryRow(ctx, `SELECT (SELECT count(*) FROM employee_run_notice WHERE run_id=$1::uuid),(SELECT count(*) FROM response_action WHERE input->>'scene_notice_id'=$1::text)`, f.runID).Scan(&notices, &actions); err != nil || notices != 0 || actions != 0 {
				t.Fatal(notices, actions, err)
			}
			cleanup()
			if _, err := f.h.ReconcileEmployeeRunNotices(ctx, 100); err != nil {
				t.Fatal(err)
			}
			if f.model.calls != 1 {
				t.Fatal("retry reran model", f.model.calls)
			}
		})
	}
}

type employeeNoticeProvider struct {
	sends, queries atomic.Int32
	unknown        bool
}

func (p *employeeNoticeProvider) Send(_ context.Context, _ dingtalkresponse.ActionInput, _ string) (dwsclient.SendResult, error) {
	p.sends.Add(1)
	if p.unknown {
		return dwsclient.SendResult{OpenTaskID: "notice-provider-task"}, errors.New("fake lost acknowledgement")
	}
	return dwsclient.SendResult{OpenTaskID: "notice-provider-task"}, nil
}
func (p *employeeNoticeProvider) Query(_ context.Context, in dingtalkresponse.ActionInput, _ string) (dwsclient.SendStatus, error) {
	p.queries.Add(1)
	return dwsclient.SendStatus{State: "delivered", OpenConversationID: in.ConversationID, OpenMessageID: "notice-provider-message"}, nil
}

func startEmployeeNoticeDelivery(t *testing.T, f employeeNoticeFixture, p *employeeNoticeProvider) string {
	t.Helper()
	var actionID string
	if err := testPool.QueryRow(context.Background(), `SELECT action_id FROM employee_run_notice WHERE run_id=$1::uuid`, f.runID).Scan(&actionID); err != nil {
		t.Fatal(err)
	}
	// The initial foreground acknowledgement is covered by the SceneEntry tests.
	if _, err := testPool.Exec(context.Background(), `DELETE FROM response_action WHERE agent_id=$1::uuid AND id<>$2`, f.agentID, actionID); err != nil {
		t.Fatal(err)
	}
	svc := dingtalkresponse.NewService(testPool, p, nil)
	svc.BeforeSend = f.h.BeforeEmployeeRunNoticeSend
	ctx, cancel := context.WithCancel(context.Background())
	go svc.Run(ctx)
	t.Cleanup(func() {
		cancel()
		if !svc.WaitWithTimeout(context.Background(), 5*time.Second) {
			t.Error("response worker did not stop")
		}
	})
	f.h.DingTalkResponses = svc
	return actionID
}

func waitEmployeeNoticeAction(t *testing.T, id, want string, timeout ...time.Duration) {
	t.Helper()
	budget := 5 * time.Second
	if len(timeout) > 0 {
		budget = timeout[0]
	}
	deadline := time.Now().Add(budget)
	for {
		var state string
		if err := testPool.QueryRow(context.Background(), `SELECT state FROM response_action WHERE id=$1`, id).Scan(&state); err != nil {
			t.Fatal(err)
		}
		if state == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("action state=%s want=%s", state, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestEmployeeRunNoticeQueuedThenRevokedNeverCallsProvider(t *testing.T) {
	for _, revoke := range []string{"tenant", "principal", "invoke", "audience"} {
		t.Run(revoke, func(t *testing.T) {
			f := employeeNoticeDatabase(t, "succeeded", false, false)
			if _, err := f.h.ReconcileEmployeeRunNotices(context.Background(), 100); err != nil {
				t.Fatal(err)
			}
			switch revoke {
			case "principal":
				revokeEmployeeNoticePrincipal(t)
			case "tenant":
				if _, err := testPool.Exec(context.Background(), `UPDATE agent_dingtalk_identity SET org_id='revoked-org' WHERE agent_id=$1::uuid`, f.agentID); err != nil {
					t.Fatal(err)
				}
			case "invoke":
				if _, err := testPool.Exec(context.Background(), `UPDATE agent SET permission_mode='private',owner_id=NULL WHERE id=$1::uuid`, f.agentID); err != nil {
					t.Fatal(err)
				}
			case "audience":
				if _, err := testPool.Exec(context.Background(), `UPDATE response_action SET input=jsonb_set(input,'{is_group}','false') WHERE id=(SELECT action_id FROM employee_run_notice WHERE run_id=$1::uuid)`, f.runID); err != nil {
					t.Fatal(err)
				}
			}
			provider := &employeeNoticeProvider{}
			actionID := startEmployeeNoticeDelivery(t, f, provider)
			waitEmployeeNoticeAction(t, actionID, "cancelled")
			if provider.sends.Load() != 0 {
				t.Fatal("revoked notice reached provider", provider.sends.Load())
			}
			var state string
			if err := testPool.QueryRow(context.Background(), `SELECT state FROM employee_run_notice WHERE run_id=$1::uuid`, f.runID).Scan(&state); err != nil || state != "suppressed" {
				t.Fatal(state, err)
			}
		})
	}
}

func TestEmployeeRunNoticeUnknownQueriesOriginalSendWithoutRecomputing(t *testing.T) {
	f := employeeNoticeDatabase(t, "succeeded", false, false)
	if _, err := f.h.ReconcileEmployeeRunNotices(context.Background(), 100); err != nil {
		t.Fatal(err)
	}
	provider := &employeeNoticeProvider{unknown: true}
	actionID := startEmployeeNoticeDelivery(t, f, provider)
	waitEmployeeNoticeAction(t, actionID, "unknown")
	if _, err := testPool.Exec(context.Background(), `UPDATE agent_dingtalk_identity SET org_id='revoked-org' WHERE agent_id=$1::uuid`, f.agentID); err != nil {
		t.Fatal(err)
	}
	restarted := *f.h
	if n, err := restarted.ReconcileEmployeeRunNotices(context.Background(), 100); err != nil || n != 0 {
		t.Fatal(n, err)
	}
	if _, err := testPool.Exec(context.Background(), `UPDATE response_action SET next_attempt_at=now() WHERE id=$1`, actionID); err != nil {
		t.Fatal(err)
	}
	f.h.DingTalkResponses.Notify()
	waitEmployeeNoticeAction(t, actionID, "delivered")
	if provider.sends.Load() != 1 || provider.queries.Load() < 1 || f.model.calls != 1 {
		t.Fatal(provider.sends.Load(), provider.queries.Load(), f.model.calls)
	}
	var actual string
	if err := testPool.QueryRow(context.Background(), `SELECT provider_message_id FROM response_action WHERE id=$1`, actionID).Scan(&actual); err != nil || actual != "notice-provider-message" {
		t.Fatal(actual, err)
	}
}

func TestEmployeeRunNoticeHookIgnoresUnrelatedActions(t *testing.T) {
	f := employeeNoticeDatabase(t, "succeeded", false, false)
	if err := f.h.BeforeEmployeeRunNoticeSend(context.Background(), dingtalkresponse.ActionInput{ActionID: "unrelated-coordinator-action"}); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err := testPool.QueryRow(context.Background(), `SELECT input FROM response_action WHERE agent_id=$1::uuid LIMIT 1`, f.agentID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var original dingtalkresponse.ActionInput
	if err := json.Unmarshal(raw, &original); err != nil {
		t.Fatal(err)
	}
	if err := f.h.BeforeEmployeeRunNoticeSend(context.Background(), original); err != nil {
		t.Fatal("foreground/ordinary notice behavior changed", err)
	}
}

func TestEmployeeRunNoticeDeletedBindingNeverBecomesOrdinarySend(t *testing.T) {
	f := employeeNoticeDatabase(t, "succeeded", false, false)
	if _, err := f.h.ReconcileEmployeeRunNotices(context.Background(), 100); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err := testPool.QueryRow(context.Background(), `SELECT a.input FROM response_action a JOIN employee_run_notice n ON n.action_id=a.id WHERE n.run_id=$1::uuid`, f.runID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var in dingtalkresponse.ActionInput
	if err := json.Unmarshal(raw, &in); err != nil {
		t.Fatal(err)
	}
	// A response worker may already hold this payload when teardown commits.
	if _, err := testPool.Exec(context.Background(), `DELETE FROM employee_run_notice WHERE run_id=$1::uuid`, f.runID); err != nil {
		t.Fatal(err)
	}
	var suppressed *dingtalkresponse.SuppressSendError
	if err := f.h.BeforeEmployeeRunNoticeSend(context.Background(), in); !errors.As(err, &suppressed) {
		t.Fatal("deleted notice became an ordinary unguarded action", err)
	}
}

func TestEmployeeRunNoticeRevokedRouterStopsWithoutInventedReceipt(t *testing.T) {
	f := employeeNoticeDatabase(t, "succeeded", true, false)
	if _, err := f.h.ReconcileEmployeeRunNotices(context.Background(), 100); err != nil {
		t.Fatal(err)
	}
	f.h.TaskCompletionTargetIdentity = "router-target:v1:sha256:" + strings.Repeat("c", 64)
	provider := &employeeNoticeProvider{}
	actionID := startEmployeeNoticeDelivery(t, f, provider)
	waitEmployeeNoticeAction(t, actionID, "cancelled")
	var noRetry bool
	var receipt string
	if err := testPool.QueryRow(context.Background(), `SELECT next_attempt_at IS NULL,receipt_state FROM response_action WHERE id=$1`, actionID).Scan(&noRetry, &receipt); err != nil {
		t.Fatal(err)
	}
	if !noRetry || receipt != "" || provider.sends.Load() != 0 {
		t.Fatal("revocation retried or invented Router acknowledgement", noRetry, receipt, provider.sends.Load())
	}
}
