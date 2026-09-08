package engine

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/integrations/channel"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/service/inboundcoord"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type channelPlanFixture struct {
	durableSessionFixture
	router   *Router
	inst     ResolvedInstallation
	identity ResolvedIdentity
	msg      channel.InboundMessage
}

func newChannelPlanFixture(t *testing.T) channelPlanFixture {
	t.Helper()
	if os.Getenv("DATABASE_URL") == "" {
		t.Skip("set DATABASE_URL to an isolated migrated test database")
	}
	pool := sessionPersistenceTestDB(t)
	base := seedSessionPersistenceFixture(t, pool)
	q := db.New(pool)
	session, err := q.GetChatSession(context.Background(), base.sessionID)
	if err != nil {
		t.Fatal(err)
	}
	agent, err := q.GetAgent(context.Background(), session.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	f := durableSessionFixture{pool: pool, userID: base.userID, workspaceID: base.workspaceID, sessionID: base.sessionID, agentID: agent.ID, runtimeID: agent.RuntimeID, installationID: base.sessionID, chatID: uuidString(base.sessionID)}
	if _, err := pool.Exec(context.Background(), `UPDATE agent_runtime SET status='online', last_seen_at=now() WHERE id=$1`, agent.RuntimeID); err != nil {
		t.Fatal(err)
	}
	bus := events.New()
	tasks := &service.TaskService{Queries: q, TxStarter: f.pool, Bus: bus}
	r := NewRouter(service.NewIssueService(q, f.pool, bus, nil, tasks), tasks, q, RouterConfig{Logger: discardLogger()})
	t.Cleanup(func() {
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM comment WHERE workspace_id=$1`, f.workspaceID)
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE agent_id=$1`, f.agentID)
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM issue WHERE workspace_id=$1`, f.workspaceID)
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM chat_message WHERE chat_session_id=$1`, f.sessionID)
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM chat_session WHERE id=$1`, f.sessionID)
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM agent WHERE id=$1`, f.agentID)
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM agent_runtime WHERE id=$1`, f.runtimeID)
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM member WHERE workspace_id=$1`, f.workspaceID)
	})
	return channelPlanFixture{durableSessionFixture: f, router: r, inst: ResolvedInstallation{ID: f.installationID, WorkspaceID: f.workspaceID, AgentID: f.agentID, InstallerUserID: f.userID, Active: true}, identity: ResolvedIdentity{PrincipalUserID: f.userID, InitiatorUserID: f.userID}, msg: channel.InboundMessage{MessageID: "window-1", Text: "推进已有工作并安排新的需求", Source: channel.Source{ChannelType: "dingtalk", ChatID: f.chatID, ChatType: channel.ChatTypeP2P, SenderID: "speaker-1"}}}
}
func (f channelPlanFixture) existing(t *testing.T, n int) db.Issue {
	t.Helper()
	var id pgtype.UUID
	if err := f.pool.QueryRow(context.Background(), `INSERT INTO issue(workspace_id,number,title,status,priority,assignee_type,assignee_id,creator_type,creator_id) VALUES($1,$2,'existing work','todo','none','agent',$3,'member',$4) RETURNING id`, f.workspaceID, 100+n, f.agentID, f.userID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	issue, err := db.New(f.pool).GetIssue(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return issue
}
func newChannelWorkPlan(items ...inboundcoord.WindowItem) inboundcoord.Decision {
	return inboundcoord.Decision{Action: inboundcoord.ActionIssue, PlanVersion: "window-plan-v1", UserText: "我来处理这两件事。", Items: items}
}
func newChannelWorkItem(key string) inboundcoord.WindowItem {
	return inboundcoord.WindowItem{ActionKey: key, SourceRefs: []string{"u1"}, Basis: "new_request", Delegator: "speaker-1", Purpose: "安排下周项目的需求评审", LookInto: "确认评审时间与参与人", Intent: "安排评审"}
}
func continuedChannelWorkItem(key string, issue db.Issue) inboundcoord.WindowItem {
	item := newChannelWorkItem(key)
	item.IssueID = uuidString(issue.ID)
	item.Content = "speaker-1：补充资料已经准备好，请继续。"
	item.Basis = "substantive_input"
	return item
}
func (f channelPlanFixture) run(ctx context.Context, d *inboundcoord.Decision) error {
	return f.router.materializeChannelCoordinatorPlan(ctx, f.inst, f.identity, "dingtalk_chat", f.msg, []byte(`{"completion_callback":{"url":"https://callback.invalid/update","target":"speaker-1"},"dispatch_idempotency_key":"outer-original-key"}`), d)
}
func (f channelPlanFixture) counts(t *testing.T, issues, comments, tasks int) {
	t.Helper()
	var a, b, c int
	if err := f.pool.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM issue WHERE workspace_id=$1),(SELECT count(*) FROM comment WHERE workspace_id=$1),(SELECT count(*) FROM agent_task_queue WHERE agent_id=$2)`, f.workspaceID, f.agentID).Scan(&a, &b, &c); err != nil {
		t.Fatal(err)
	}
	if a != issues || b != comments || c != tasks {
		t.Fatalf("issues/comments/tasks=%d/%d/%d want %d/%d/%d", a, b, c, issues, comments, tasks)
	}
}
func TestChannelCoordinatorPlanMaterializesAllKindsAndIsolatesCallbacks(t *testing.T) {
	for _, kind := range []string{"two_new", "mixed", "two_continuations"} {
		t.Run(kind, func(t *testing.T) {
			f := newChannelPlanFixture(t)
			items := []inboundcoord.WindowItem{newChannelWorkItem("a"), newChannelWorkItem("b")}
			existing := 0
			if kind != "two_new" {
				existing++
				items[0] = continuedChannelWorkItem("a", f.existing(t, 1))
			}
			if kind == "two_continuations" {
				existing++
				items[1] = continuedChannelWorkItem("b", f.existing(t, 2))
			}
			d := newChannelWorkPlan(items...)
			var eventsSeen atomic.Int32
			f.router.issues.(*service.IssueService).Bus.SubscribeAll(func(e events.Event) { eventsSeen.Add(1); f.counts(t, 2, existing, 2) })
			if err := f.run(context.Background(), &d); err != nil {
				t.Fatal(err)
			}
			f.counts(t, 2, existing, 2)
			if len(d.IssueResults) != 2 || len(d.CompletedActionKeys) != 2 || eventsSeen.Load() == 0 {
				t.Fatalf("incomplete outcomes: %#v events=%d", d.IssueResults, eventsSeen.Load())
			}
			for i, result := range d.IssueResults {
				if (items[i].IssueID != "") != (result.Action == "issue_commented") {
					t.Fatalf("wrong create/continue result: %#v", result)
				}
				task, err := db.New(f.pool).GetAgentTask(context.Background(), util.MustParseUUID(result.TaskID))
				if err != nil {
					t.Fatal(err)
				}
				var fields map[string]json.RawMessage
				if err := json.Unmarshal(task.Context, &fields); err != nil {
					t.Fatal(err)
				}
				if _, present := fields["completion_callback"]; present {
					t.Fatal("independent issue task inherited inbound completion callback")
				}
				if _, _, ok := inboundcoord.WrapupCallback(task.Context); !ok {
					t.Fatal("independent task lost its wrapup target")
				}
				if result.Action == "issue_commented" && (!task.TriggerCommentID.Valid || result.CommentID == "") {
					t.Fatal("continuation has no real comment receipt")
				}
			}
		})
	}
}
func TestChannelCoordinatorPlanFailureRollsBackWholeWindow(t *testing.T) {
	f := newChannelPlanFixture(t)
	bad := newChannelWorkItem("b")
	bad.IssueID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	bad.Content = "continue"
	d := newChannelWorkPlan(newChannelWorkItem("a"), bad)
	var eventsSeen atomic.Int32
	f.router.issues.(*service.IssueService).Bus.SubscribeAll(func(events.Event) { eventsSeen.Add(1) })
	if err := f.run(context.Background(), &d); err == nil {
		t.Fatal("missing second target accepted")
	}
	f.counts(t, 0, 0, 0)
	if eventsSeen.Load() != 0 || len(d.CompletedActionKeys) != 0 || len(d.IssueResults) != 0 {
		t.Fatal("rollback leaked outcomes or product events")
	}
	d.Items[1] = newChannelWorkItem("b")
	if err := f.run(context.Background(), &d); err != nil {
		t.Fatal(err)
	}
	f.counts(t, 2, 0, 2)
}
func TestChannelCoordinatorPlanConcurrentReplayAndCommittedResponseLoss(t *testing.T) {
	f := newChannelPlanFixture(t)
	ctx := context.Background()
	original := newChannelWorkPlan(newChannelWorkItem("a"), newChannelWorkItem("b"))
	tasks := f.router.coordinatorTaskService()
	tasks.TxStarter = &lostAckTxStarter{pool: f.pool}
	failed := original
	if err := f.run(ctx, &failed); err == nil {
		t.Fatal("expected injected lost commit response")
	}
	f.counts(t, 2, 0, 2)
	tasks.TxStarter = f.pool
	// Recovery keys are attached to ingress, so a fresh model response with
	// different action keys still recovers the original durable work.
	restored, found, err := f.router.restoreChannelCoordinatorPlan(ctx, f.inst, f.msg)
	if err != nil || !found {
		t.Fatalf("restore after lost commit: found=%v err=%v", found, err)
	}
	const workers = 5
	var wg sync.WaitGroup
	results := make(chan inboundcoord.Decision, workers)
	errs := make(chan error, workers)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d := newChannelWorkPlan(newChannelWorkItem("different-a"), newChannelWorkItem("different-b"))
			errs <- f.run(ctx, &d)
			results <- d
		}()
	}
	wg.Wait()
	close(errs)
	close(results)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	for d := range results {
		if d.IssueResults[0].TaskID != restored.IssueResults[0].TaskID || d.IssueResults[1].TaskID != restored.IssueResults[1].TaskID {
			t.Fatal("replay duplicated accepted work")
		}
	}
	f.counts(t, 2, 0, 2)
}
func TestChannelCoordinatorDeferredDoesNotRunLegacyChat(t *testing.T) {
	h := newHarness(t)
	h.media.noMedia = true
	set := h.router.sets[channel.TypeFeishu]
	h.router.Register("dingtalk", set)
	h.router.SetInboundCoordinator(&inboundcoord.Coordinator{})
	msg := p2pMessage(t)
	msg.Source.ChannelType = "dingtalk"
	_, err := h.router.HandleResult(context.Background(), msg)
	if !errors.Is(err, service.ErrIssueDispatchPending) {
		t.Fatalf("expected retryable deferred result: %v", err)
	}
	if h.tasks.wasCalled() || h.tasks.wasPrepared() || h.issues.called || h.binder.appendCalls != 0 || h.dedup.releases() != 1 {
		t.Fatal("deferred decision entered legacy execution or was consumed")
	}
}
func TestChannelCoordinatorMediaBypassesDecisionBeforeEffects(t *testing.T) {
	h := newHarness(t)
	set := h.router.sets[channel.TypeFeishu]
	h.router.Register("dingtalk", set)
	h.router.SetInboundCoordinator(&inboundcoord.Coordinator{})
	h.media.noMedia = false
	msg := p2pMessage(t)
	msg.Source.ChannelType = "dingtalk"
	_, err := h.router.HandleResult(context.Background(), msg)
	if err != nil {
		t.Fatal(err)
	}
	if h.issues.called || !h.tasks.wasCalled() {
		t.Fatal("media should keep the established attachment/chat path")
	}
}

func TestChannelCoordinatorNativeAppendFailureRecoversPlanBeforeDecision(t *testing.T) {
	f := newChannelPlanFixture(t)
	binder := &fakeBinder{ensureID: f.sessionID, appendErr: errors.New("append unavailable"), appendResult: AppendResult{MessageID: f.sessionID, DedupMarked: true}}
	dedup := &fakeDedup{token: f.sessionID}
	f.router.Register("dingtalk", ResolverSet{Installation: &fakeInstaller{inst: f.inst}, Identity: &fakeIdentity{id: f.identity}, Dedup: dedup, Session: binder, Audit: &fakeAuditor{}, TaskContext: &fakeTaskContext{value: []byte(`{"completion_callback":{"url":"https://callback.invalid/update","target":"speaker-1"}}`)}, OriginType: "dingtalk_chat"})
	f.router.SetInboundCoordinator(&inboundcoord.Coordinator{})
	original := newChannelWorkPlan(newChannelWorkItem("a"), newChannelWorkItem("b"))
	ctx := inboundcoord.ContextWithPlanCheckpoint(context.Background(), &original, nil)
	if _, err := f.router.HandleResultWithOptions(ctx, f.msg, HandleOptions{SuppressServerOutbound: true}); err == nil {
		t.Fatal("expected append failure")
	}
	f.counts(t, 2, 0, 2)
	if dedup.releases() != 1 {
		t.Fatal("append failure should release dedup for retry")
	}
	binder.appendErr = nil
	// No saved context and no model: the durable native receipt must resolve
	// this retry before Decide would return ActionDeferred.
	var observed inboundcoord.Decision
	ctx = inboundcoord.WithDecisionObserver(context.Background(), func(d inboundcoord.Decision) { observed = d })
	result, err := f.router.HandleResultWithOptions(ctx, f.msg, HandleOptions{SuppressServerOutbound: true})
	if err != nil {
		t.Fatal(err)
	}
	if !result.CoordinatorIssue || result.runScheduled || !result.TaskID.Valid || len(observed.IssueResults) != 2 {
		t.Fatalf("incomplete native result: %#v outcomes=%d", result, len(observed.IssueResults))
	}
	f.counts(t, 2, 0, 2)
	if _, err := f.pool.Exec(context.Background(), `UPDATE agent_task_queue SET status='completed', completed_at=now() WHERE agent_id=$1`, f.agentID); err != nil {
		t.Fatal(err)
	}
	replay := newChannelWorkPlan(newChannelWorkItem("changed"))
	if err := f.run(context.Background(), &replay); err != nil {
		t.Fatal(err)
	}
	f.counts(t, 2, 0, 2)
	if len(replay.IssueResults) != 2 || replay.IssueResults[0].TaskID != observed.IssueResults[0].TaskID {
		t.Fatal("completed receipt was not restored")
	}
}

func TestChannelCoordinatorPlanKeepsWorkBeyondFirstBatch(t *testing.T) {
	f := newChannelPlanFixture(t)
	d := newChannelWorkPlan(newChannelWorkItem("a"), newChannelWorkItem("b"), newChannelWorkItem("c"))
	if err := f.run(context.Background(), &d); err != nil {
		t.Fatal(err)
	}
	f.counts(t, 3, 0, 3)
	if len(d.IssueResults) != 3 {
		t.Fatal("third request was dropped")
	}
}

func TestChannelCoordinatorUnknownActionCannotFallThroughToChat(t *testing.T) {
	h := newHarness(t)
	h.media.noMedia = true
	set := h.router.sets[channel.TypeFeishu]
	h.router.Register("dingtalk", set)
	h.router.SetInboundCoordinator(&inboundcoord.Coordinator{})
	msg := p2pMessage(t)
	msg.Source.ChannelType = "dingtalk"
	invalid := inboundcoord.Decision{Action: "unknown", PlanVersion: "window-plan-v1"}
	ctx := inboundcoord.ContextWithPlanCheckpoint(context.Background(), &invalid, nil)
	if _, err := h.router.HandleResult(ctx, msg); err == nil {
		t.Fatal("unknown action was accepted")
	}
	if h.tasks.wasCalled() || h.tasks.wasPrepared() || h.issues.called || h.binder.appendCalls != 0 {
		t.Fatal("unknown action fell through into chat")
	}
}
