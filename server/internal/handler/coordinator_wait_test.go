package handler

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/service/dingtalkresponse"
	"github.com/multica-ai/multica/server/internal/service/inboundcoord"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type coordinatorWaitFixture struct {
	coordinatorPlanFixture
	pool *pgxpool.Pool
}

func waitResponsePool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()
	schema := pgx.Identifier{"wait_progress_" + strings.ReplaceAll(uuid.NewString(), "-", "")}.Sanitize()
	if _, err := testPool.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	cfg, err := pgxpool.ParseConfig(testPool.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		if _, err := testPool.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Error(err)
		}
	})
	for _, name := range []string{"9144_response_action", "9145_response_action_id", "9146_response_action_due", "9147_response_action_scope", "9148_response_route_callback"} {
		raw, err := os.ReadFile(filepath.Join("..", "..", "migrations", name+".up.sql"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err = pool.Exec(ctx, string(raw)); err != nil {
			t.Fatal(err)
		}
	}
	return pool
}

func waitPlanFixture(t *testing.T, managed bool) coordinatorWaitFixture {
	t.Helper()
	f := coordinatorWaitFixture{coordinatorPlanFixture: newCoordinatorPlanFixture(t, "继续查询结果"), pool: waitResponsePool(t)}
	f.h.Queries, f.h.TxStarter = db.New(f.pool), f.pool
	session, err := f.h.Queries.CreateChatSession(context.Background(), db.CreateChatSessionParams{WorkspaceID: f.dc.WorkspaceID, AgentID: f.agent.ID, CreatorID: f.dc.UserID, Title: "Coordinator waiting fixture"})
	if err != nil {
		t.Fatal(err)
	}
	f.job.ChatSessionID = session.ID
	if _, err := testPool.Exec(context.Background(), `UPDATE inbound_coordinator_job SET chat_session_id=$2 WHERE id=$1`, f.job.ID, session.ID); err != nil {
		t.Fatal(err)
	}
	f.h.Bus = testHandler.Bus
	f.h.TaskCompletionTargetIdentity = testRouterTargetIdentity
	f.h.DingTalkResponses = dingtalkresponse.NewService(f.pool, nil, nil)
	f.command.CompletionCallback = &DispatchCompletionCallback{URL: "/api/v1/dispatch-tasks/" + f.baseKey + "/execution-result", ResponseURL: "/api/v1/dispatch-tasks/" + f.baseKey + "/response-receipt", Target: testRouterTargetIdentity}
	f.command.Outbound = DispatchOutbound{Mode: protocol.DispatchOutboundModeDWS}
	f.command.ExternalIdentity = AgentDispatchExternalIdentity{DWS: &AgentDispatchDWSIdentity{UID: "wait-fixture", OrgID: "org"}}
	if managed {
		f.command.ResponsePolicy = &protocol.DingTalkResponsePolicy{Version: 1, Mode: protocol.DingTalkResponseModeCoordinator, Revision: 1}
	}
	raw, _ := json.Marshal(f.command)
	if _, err := testPool.Exec(context.Background(), `UPDATE inbound_coordinator_job SET command=$2 WHERE id=$1`, f.job.ID, raw); err != nil {
		t.Fatal(err)
	}
	if managed {
		if err := f.h.registerDingTalkResponseRoute(context.Background(), f.pool, f.command, f.dc); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM response_action WHERE agent_id=$1`, f.agent.ID)
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM response_route WHERE agent_id=$1`, f.agent.ID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM chat_message WHERE chat_session_id=$1`, f.job.ChatSessionID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM chat_session WHERE id=$1`, f.job.ChatSessionID)
	})
	return f
}
func waitCounts(t *testing.T, f coordinatorWaitFixture) (int, int, int) {
	t.Helper()
	var notices, terminals, outbound int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FILTER(WHERE message_kind='message'),count(*) FILTER(WHERE message_kind='coordinator') FROM chat_message WHERE chat_session_id=$1`, f.job.ChatSessionID).Scan(&notices, &terminals); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM response_action WHERE agent_id=$1`, f.agent.ID).Scan(&outbound); err != nil {
		t.Fatal(err)
	}
	return notices, terminals, outbound
}
func TestCoordinatorWaitNoticeIsOnceAndDoesNotSettlePlan(t *testing.T) {
	ctx := context.Background()
	f := waitPlanFixture(t, true)
	plan := f.plan(t)
	worker := NewInboundCoordinatorJobWorker(f.h)
	for _, reason := range []string{"recalled issue already has a pending agent task", "scene already has two in-flight matters", "recalled issue already has a pending agent task"} {
		f.job.LeaseToken = parseUUID(uuid.NewString())
		if _, err := testPool.Exec(ctx, `UPDATE inbound_coordinator_job SET status='running',lease_token=$2,lease_expires_at=now()+interval '1 minute' WHERE id=$1`, f.job.ID, f.job.LeaseToken); err != nil {
			t.Fatal(err)
		}
		if err := worker.park(ctx, f.job, 5*time.Second, reason); err != nil {
			t.Fatal(err)
		}
	}
	notices, terminals, outbound := waitCounts(t, f)
	if notices != 1 || terminals != 0 || outbound != 1 {
		t.Fatalf("duplicate or terminal wait feedback: notices=%d terminals=%d outbound=%d", notices, terminals, outbound)
	}
	job, stored := f.stored(t)
	if job.Status != "pending" || len(stored.CompletedActionKeys) != 0 || stored.UserText != plan.UserText {
		t.Fatal("wait feedback changed the executable plan or marked work complete")
	}
	var count int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM task_completion_outbox WHERE agent_id=$1`, f.agent.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal("wait feedback settled original callback")
	}
	if err := f.h.persistCoordinatorJobChat(ctx, job, plan); err != nil {
		t.Fatal(err)
	}
	notices, terminals, _ = waitCounts(t, f)
	if notices != 1 || terminals != 1 {
		t.Fatal("waiting notice hid the eventual coordinator result")
	}
}
func TestCoordinatorWaitRequiresSavedWorkAndHonorsManagedReplyPolicy(t *testing.T) {
	for _, managed := range []bool{false, true} {
		t.Run(map[bool]string{false: "local_only", true: "managed"}[managed], func(t *testing.T) {
			f := waitPlanFixture(t, managed)
			if err := f.h.persistCoordinatorWait(context.Background(), f.job, "issue_busy"); err != nil {
				t.Fatal(err)
			}
			n, _, a := waitCounts(t, f)
			if n != 0 || a != 0 {
				t.Fatal("unjudged message caused waiting feedback")
			}
			f.plan(t)
			if err := f.h.persistCoordinatorWait(context.Background(), f.job, "issue_busy"); err != nil {
				t.Fatal(err)
			}
			n, _, a = waitCounts(t, f)
			if n != 1 || a != map[bool]int{false: 0, true: 1}[managed] {
				t.Fatal("waiting feedback ignored frozen response policy")
			}
		})
	}
}
func TestCoordinatorWaitFailureDoesNotBlockPark(t *testing.T) {
	f := waitPlanFixture(t, true)
	f.plan(t)
	if _, err := f.pool.Exec(context.Background(), `CREATE FUNCTION reject_wait_action() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test outbox failure'; END $$; CREATE TRIGGER reject_wait_action BEFORE INSERT ON response_action FOR EACH ROW EXECUTE FUNCTION reject_wait_action()`); err != nil {
		t.Fatal(err)
	}
	if err := NewInboundCoordinatorJobWorker(f.h).park(context.Background(), f.job, time.Second, "issue_busy"); err != nil {
		t.Fatal(err)
	}
	n, _, a := waitCounts(t, f)
	if n != 0 || a != 0 {
		t.Fatal("failed notice did not roll back atomically")
	}
	job, _ := f.stored(t)
	if job.Status != "pending" {
		t.Fatal("feedback failure prevented existing wait/recovery")
	}
}
func TestCoordinatorWaitTextDoesNotClaimAllWorkIsUnstartedAfterPartialCommit(t *testing.T) {
	d := &inboundcoord.Decision{Action: inboundcoord.ActionIssue, Items: []inboundcoord.WindowItem{{ActionKey: "a"}, {ActionKey: "b"}}, CompletedActionKeys: []string{"a"}}
	text := coordinatorWaitText(d, "issue_busy")
	if !strings.Contains(text, "剩余部分") || strings.Contains(text, "已入队") || strings.Contains(text, "正在执行") {
		t.Fatal(text)
	}
	d.CompletedActionKeys = []string{"a", "b"}
	if coordinatorWaitText(d, "issue_busy") != "" {
		t.Fatal("fully committed work emitted waiting notice")
	}
}

func TestCoordinatorWaitWithoutTrustedSendIdentityIsLocalOnly(t *testing.T) {
	f := waitPlanFixture(t, true)
	f.plan(t)
	if _, err := f.pool.Exec(context.Background(), `UPDATE response_route SET input=jsonb_set(input,'{dws_uid}','""') WHERE agent_id=$1`, f.agent.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.h.persistCoordinatorWait(context.Background(), f.job, "issue_busy"); err != nil {
		t.Fatal(err)
	}
	notices, _, outbound := waitCounts(t, f)
	if notices != 1 || outbound != 0 {
		t.Fatal("missing send identity must retain visible local waiting state only")
	}
}
