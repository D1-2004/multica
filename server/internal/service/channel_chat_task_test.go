package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/runtimeapps"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type durableChannelTaskFixture struct {
	pool        *pgxpool.Pool
	userID      pgtype.UUID
	workspaceID pgtype.UUID
	runtimeID   pgtype.UUID
	agentID     pgtype.UUID
	sessionID   pgtype.UUID
}

func newDurableChannelTaskFixture(t *testing.T) durableChannelTaskFixture {
	t.Helper()
	ctx := context.Background()
	pool := newTaskClaimRacePool(t)

	// The integration database used by service tests may predate the migration
	// under test. Install the two intended indexes idempotently so the generated
	// ON CONFLICT target has the same contract as production.
	if _, err := pool.Exec(ctx, `
		CREATE UNIQUE INDEX IF NOT EXISTS uq_agent_task_queue_deferred_chat_session
		ON agent_task_queue (chat_session_id)
		WHERE status = 'deferred' AND chat_session_id IS NOT NULL
	`); err != nil {
		t.Fatalf("ensure deferred chat unique index: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		CREATE INDEX IF NOT EXISTS idx_agent_task_queue_channel_dispatch_pending
		ON agent_task_queue (status, fire_at, id)
		WHERE chat_session_id IS NOT NULL
		  AND fire_at IS NOT NULL
		  AND status IN ('deferred', 'queued')
	`); err != nil {
		t.Fatalf("ensure channel dispatch index: %v", err)
	}

	suffix := time.Now().UnixNano()
	var f durableChannelTaskFixture
	f.pool = pool
	if err := pool.QueryRow(ctx, `
		INSERT INTO "user" (name, email)
		VALUES ($1, $2)
		RETURNING id
	`, "Durable Channel User", fmt.Sprintf("durable-channel-%d@multica.test", suffix)).Scan(&f.userID); err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO workspace (name, slug, description, issue_prefix)
		VALUES ($1, $2, '', 'DCT')
		RETURNING id
	`, "Durable Channel Workspace", fmt.Sprintf("durable-channel-%d", suffix)).Scan(&f.workspaceID); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO member (workspace_id, user_id, role)
		VALUES ($1, $2, 'owner')
	`, f.workspaceID, f.userID); err != nil {
		t.Fatalf("create member: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO agent_runtime (
			workspace_id, daemon_id, name, runtime_mode, provider, status,
			device_info, metadata, last_seen_at, visibility, owner_id
		)
		VALUES ($1, $2, 'Durable Channel Runtime', 'cloud', 'durable_channel_test',
		        'online', 'test', '{}'::jsonb, now(), 'private', $3)
		RETURNING id
	`, f.workspaceID, fmt.Sprintf("durable-channel-daemon-%d", suffix), f.userID).Scan(&f.runtimeID); err != nil {
		t.Fatalf("create runtime: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO agent (
			workspace_id, name, description, runtime_mode, runtime_config,
			runtime_id, visibility, max_concurrent_tasks, owner_id
		)
		VALUES ($1, 'Durable Channel Agent', '', 'cloud', '{}'::jsonb,
		        $2, 'private', 5, $3)
		RETURNING id
	`, f.workspaceID, f.runtimeID, f.userID).Scan(&f.agentID); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO chat_session (workspace_id, agent_id, creator_id, title)
		VALUES ($1, $2, $3, 'Durable channel test')
		RETURNING id
	`, f.workspaceID, f.agentID, f.userID).Scan(&f.sessionID); err != nil {
		t.Fatalf("create chat session: %v", err)
	}

	t.Cleanup(func() {
		cleanupCtx := context.Background()
		pool.Exec(cleanupCtx, `DELETE FROM agent_task_queue WHERE agent_id = $1`, f.agentID)
		pool.Exec(cleanupCtx, `DELETE FROM issue WHERE workspace_id = $1`, f.workspaceID)
		pool.Exec(cleanupCtx, `DELETE FROM chat_session WHERE id = $1`, f.sessionID)
		pool.Exec(cleanupCtx, `DELETE FROM agent WHERE id = $1`, f.agentID)
		pool.Exec(cleanupCtx, `DELETE FROM agent_runtime WHERE id = $1`, f.runtimeID)
		pool.Exec(cleanupCtx, `DELETE FROM member WHERE workspace_id = $1 AND user_id = $2`, f.workspaceID, f.userID)
		pool.Exec(cleanupCtx, `DELETE FROM workspace WHERE id = $1`, f.workspaceID)
		pool.Exec(cleanupCtx, `DELETE FROM "user" WHERE id = $1`, f.userID)
	})
	return f
}

func (f durableChannelTaskFixture) session(t *testing.T) db.ChatSession {
	t.Helper()
	session, err := db.New(f.pool).GetChatSession(context.Background(), f.sessionID)
	if err != nil {
		t.Fatalf("load chat session: %v", err)
	}
	return session
}

func memberChatTaskIdentity(userID pgtype.UUID) ChatTaskIdentity {
	return ChatTaskIdentity{PrincipalUserID: userID, InitiatorUserID: userID}
}

func preparedUpsertParams(sessionID pgtype.UUID, p PreparedChannelChatTask) db.UpsertDeferredChannelChatTaskParams {
	return db.UpsertDeferredChannelChatTaskParams{
		ID:                   p.ID,
		AgentID:              p.AgentID,
		RuntimeID:            p.RuntimeID,
		ChatSessionID:        sessionID,
		InitiatorUserID:      p.InitiatorUserID,
		OriginatorUserID:     p.OriginatorUserID,
		ForceFreshSession:    pgtype.Bool{Bool: p.ForceFreshSession, Valid: true},
		RuntimeMcpOverlay:    p.RuntimeMCPOverlay,
		RuntimeConnectedApps: p.RuntimeConnectedApps,
		TaskContext:          p.TaskContext,
		DebounceSeconds:      p.DebounceSeconds,
	}
}

func TestPrepareChannelChatTaskPreservesRuntimeEnvelope(t *testing.T) {
	f := newDurableChannelTaskFixture(t)
	q := db.New(f.pool)
	builder := &stubOverlayBuilder{
		resp: json.RawMessage(`{"mcpServers":{"composio":{"type":"http","url":"https://mcp.example/session"}}}`),
		apps: []runtimeapps.ConnectedApp{{
			Provider:    "composio",
			ServerName:  "composio",
			ToolkitSlug: "notion",
			ToolkitName: "Notion",
		}},
	}
	svc := NewTaskService(q, f.pool, nil, events.New())
	svc.Composio = builder
	svc.FeatureFlags = composioMCPAppsTestFlags(true)
	taskContext := []byte(`{"agent_identity_context_token":"test-context","agent_identity_context_token_expires_at":4102444800000}`)

	prepared, err := svc.PrepareChannelChatTask(context.Background(), f.session(t), memberChatTaskIdentity(f.userID), true, taskContext)
	if err != nil {
		t.Fatalf("prepare channel task: %v", err)
	}
	if !prepared.ID.Valid {
		t.Fatal("prepared task id must be valid")
	}
	if prepared.AgentID != f.agentID || prepared.RuntimeID != f.runtimeID {
		t.Fatalf("prepared routing = agent %s runtime %s, want %s/%s",
			util.UUIDToString(prepared.AgentID), util.UUIDToString(prepared.RuntimeID),
			util.UUIDToString(f.agentID), util.UUIDToString(f.runtimeID))
	}
	if prepared.InitiatorUserID != f.userID || prepared.OriginatorUserID != f.userID {
		t.Fatal("prepared task must preserve the triggering user as initiator and originator")
	}
	if !prepared.ForceFreshSession {
		t.Fatal("force-fresh flag was not preserved")
	}
	if prepared.DebounceSeconds != ChannelChatDebounceWindow.Seconds() {
		t.Fatalf("debounce seconds = %v, want %v", prepared.DebounceSeconds, ChannelChatDebounceWindow.Seconds())
	}
	if string(prepared.RuntimeMCPOverlay) != string(builder.resp) {
		t.Fatalf("runtime MCP overlay mismatch: got %s", prepared.RuntimeMCPOverlay)
	}
	if len(prepared.RuntimeConnectedApps) == 0 {
		t.Fatal("runtime connected-app metadata was not preserved")
	}
	taskContext[0] = 'x'
	if string(prepared.TaskContext) != `{"agent_identity_context_token":"test-context","agent_identity_context_token_expires_at":4102444800000}` {
		t.Fatal("prepared task context must not alias the caller's buffer")
	}

	if _, err := f.pool.Exec(context.Background(), `UPDATE agent SET archived_at = now() WHERE id = $1`, f.agentID); err != nil {
		t.Fatalf("archive agent: %v", err)
	}
	if _, err := svc.PrepareChannelChatTask(context.Background(), f.session(t), memberChatTaskIdentity(f.userID), false, nil); !errors.Is(err, ErrChatTaskAgentArchived) {
		t.Fatalf("archived prepare error = %v, want ErrChatTaskAgentArchived", err)
	}
	if _, err := f.pool.Exec(context.Background(), `UPDATE agent SET archived_at = NULL, runtime_id = NULL WHERE id = $1`, f.agentID); err != nil {
		t.Fatalf("clear agent runtime: %v", err)
	}
	if _, err := svc.PrepareChannelChatTask(context.Background(), f.session(t), memberChatTaskIdentity(f.userID), false, nil); !errors.Is(err, ErrChatTaskAgentNoRuntime) {
		t.Fatalf("runtime-less prepare error = %v, want ErrChatTaskAgentNoRuntime", err)
	}
}

func TestPrepareChannelChatTaskSeparatesPrincipalFromInitiator(t *testing.T) {
	f := newDurableChannelTaskFixture(t)
	builder := &stubOverlayBuilder{resp: json.RawMessage(`{"mcpServers":{}}`)}
	svc := NewTaskService(db.New(f.pool), f.pool, nil, events.New())
	svc.Composio = builder
	svc.FeatureFlags = composioMCPAppsTestFlags(true)

	prepared, err := svc.PrepareChannelChatTask(context.Background(), f.session(t), ChatTaskIdentity{
		PrincipalUserID: f.userID,
	}, false, []byte(`{"dingtalk_conversation_initiator":{"display_name":"当前对话者"}}`))
	if err != nil {
		t.Fatalf("prepare channel task: %v", err)
	}
	if prepared.InitiatorUserID.Valid {
		t.Fatalf("initiator = %v, want invalid for unbound sender", prepared.InitiatorUserID)
	}
	if prepared.OriginatorUserID != f.userID {
		t.Fatalf("originator principal = %v, want %v", prepared.OriginatorUserID, f.userID)
	}
	if builder.lastUser != f.userID {
		t.Fatalf("overlay principal = %v, want %v", builder.lastUser, f.userID)
	}
}

func TestUpsertDeferredChannelChatTaskConcurrentCoalesces(t *testing.T) {
	f := newDurableChannelTaskFixture(t)
	q := db.New(f.pool)
	svc := NewTaskService(q, f.pool, nil, events.New())
	session := f.session(t)

	const workers = 20
	prepared := make([]PreparedChannelChatTask, workers)
	for i := range workers {
		p, err := svc.PrepareChannelChatTask(context.Background(), session, memberChatTaskIdentity(f.userID), i == workers-1, []byte(fmt.Sprintf(`{"seq":%d}`, i)))
		if err != nil {
			t.Fatalf("prepare %d: %v", i, err)
		}
		prepared[i] = p
	}

	start := make(chan struct{})
	ids := make(chan string, workers)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := range workers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			task, err := q.UpsertDeferredChannelChatTask(context.Background(), preparedUpsertParams(f.sessionID, prepared[i]))
			if err != nil {
				errs <- err
				return
			}
			ids <- util.UUIDToString(task.ID)
		}(i)
	}
	close(start)
	wg.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent upsert: %v", err)
		}
	}

	unique := map[string]struct{}{}
	for id := range ids {
		unique[id] = struct{}{}
	}
	if len(unique) != 1 {
		t.Fatalf("concurrent upserts returned %d task ids, want 1: %v", len(unique), unique)
	}
	var taskID pgtype.UUID
	var count int
	if err := f.pool.QueryRow(context.Background(), `
		SELECT count(*)
		FROM agent_task_queue
		WHERE chat_session_id = $1 AND status = 'deferred'
	`, f.sessionID).Scan(&count); err != nil {
		t.Fatalf("count coalesced tasks: %v", err)
	}
	if count != 1 {
		t.Fatalf("deferred task count = %d, want 1", count)
	}
	if err := f.pool.QueryRow(context.Background(), `
		SELECT id
		FROM agent_task_queue
		WHERE chat_session_id = $1 AND status = 'deferred'
	`, f.sessionID).Scan(&taskID); err != nil {
		t.Fatalf("load coalesced task id: %v", err)
	}
	task, err := q.GetAgentTask(context.Background(), taskID)
	if err != nil {
		t.Fatalf("get coalesced task: %v", err)
	}
	if task.ChatInputTaskID != task.ID {
		t.Fatalf("chat input owner = %s, want task id %s", util.UUIDToString(task.ChatInputTaskID), util.UUIDToString(task.ID))
	}
	if !task.ForceFreshSession {
		t.Fatal("force-fresh OR was lost during concurrent coalescing")
	}
}

type safeWakeupRecorder struct {
	mu    sync.Mutex
	calls []string
	ch    chan struct{}
}

func (r *safeWakeupRecorder) NotifyTaskAvailable(runtimeID, taskID string) {
	r.mu.Lock()
	r.calls = append(r.calls, runtimeID+"/"+taskID)
	r.mu.Unlock()
	if r.ch != nil {
		select {
		case r.ch <- struct{}{}:
		default:
		}
	}
}

func (r *safeWakeupRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

func seedDueDeferredChannelTask(t *testing.T, f durableChannelTaskFixture) db.AgentTaskQueue {
	t.Helper()
	svc := NewTaskService(db.New(f.pool), f.pool, nil, events.New())
	prepared, err := svc.PrepareChannelChatTask(context.Background(), f.session(t), memberChatTaskIdentity(f.userID), false, []byte(`{"trace":"durable"}`))
	if err != nil {
		t.Fatalf("prepare due task: %v", err)
	}
	task, err := db.New(f.pool).UpsertDeferredChannelChatTask(context.Background(), preparedUpsertParams(f.sessionID, prepared))
	if err != nil {
		t.Fatalf("upsert due task: %v", err)
	}
	if _, err := f.pool.Exec(context.Background(), `UPDATE agent_task_queue SET fire_at = now() - interval '1 second' WHERE id = $1`, task.ID); err != nil {
		t.Fatalf("make task due: %v", err)
	}
	task.FireAt = pgtype.Timestamptz{Time: time.Now().Add(-time.Second), Valid: true}
	return task
}

func TestPromoteAndNotifyDueChannelTasksConcurrentAndRetry(t *testing.T) {
	f := newDurableChannelTaskFixture(t)
	task := seedDueDeferredChannelTask(t, f)
	recorderA := &safeWakeupRecorder{}
	recorderB := &safeWakeupRecorder{}
	svcA := NewTaskService(db.New(f.pool), f.pool, nil, events.New(), recorderA)
	svcB := NewTaskService(db.New(f.pool), f.pool, nil, events.New(), recorderB)

	start := make(chan struct{})
	counts := make(chan int, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, svc := range []*TaskService{svcA, svcB} {
		wg.Add(1)
		go func(svc *TaskService) {
			defer wg.Done()
			<-start
			count, err := svc.PromoteAndNotifyDueChannelTasks(context.Background())
			counts <- count
			errs <- err
		}(svc)
	}
	close(start)
	wg.Wait()
	close(counts)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent promoter: %v", err)
		}
	}
	totalPromoted := 0
	for count := range counts {
		totalPromoted += count
	}
	if totalPromoted != 1 {
		t.Fatalf("promoted count across replicas = %d, want 1", totalPromoted)
	}
	if got := recorderA.count() + recorderB.count(); got != 1 {
		t.Fatalf("notification count across replicas = %d, want 1", got)
	}

	queued, err := db.New(f.pool).GetAgentTask(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("load queued task: %v", err)
	}
	if queued.Status != "queued" {
		t.Fatalf("task status = %q, want queued", queued.Status)
	}
	if time.Until(queued.FireAt.Time) < 10*time.Second {
		t.Fatalf("next notification was not durably deferred: fire_at=%s", queued.FireAt.Time)
	}

	if _, err := svcA.PromoteAndNotifyDueChannelTasks(context.Background()); err != nil {
		t.Fatalf("immediate promoter retry: %v", err)
	}
	if got := recorderA.count() + recorderB.count(); got != 1 {
		t.Fatalf("immediate retry emitted %d total notifications, want 1", got)
	}

	// A live FC/E2B launch lease owns the side effect even when next_notify_at
	// is due; another replica must not submit a second runtime launch.
	if _, err := f.pool.Exec(context.Background(), `
		UPDATE agent_task_queue
		SET fire_at = now() - interval '1 second',
		    runtime_launch_lease_token = gen_random_uuid(),
		    runtime_launch_lease_expires_at = now() + interval '1 minute'
		WHERE id = $1
	`, task.ID); err != nil {
		t.Fatalf("install active runtime launch lease: %v", err)
	}
	if _, err := svcA.PromoteAndNotifyDueChannelTasks(context.Background()); err != nil {
		t.Fatalf("promoter with active launch lease: %v", err)
	}
	if got := recorderA.count() + recorderB.count(); got != 1 {
		t.Fatalf("active launch lease allowed another notification: total=%d", got)
	}

	// Model a worker crash after it claimed the notification but before its
	// asynchronous wake/launch completed: when next_notify_at becomes due, the
	// same durable task is retried without creating another task row.
	if _, err := f.pool.Exec(context.Background(), `
		UPDATE agent_task_queue
		SET fire_at = now() - interval '1 second',
		    runtime_launch_lease_token = NULL,
		    runtime_launch_lease_expires_at = NULL
		WHERE id = $1
	`, task.ID); err != nil {
		t.Fatalf("make notification retry due: %v", err)
	}
	if _, err := svcA.PromoteAndNotifyDueChannelTasks(context.Background()); err != nil {
		t.Fatalf("durable notification retry: %v", err)
	}
	if got := recorderA.count() + recorderB.count(); got != 2 {
		t.Fatalf("notification count after durable retry = %d, want 2", got)
	}
	var taskRows int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM agent_task_queue WHERE chat_session_id = $1`, f.sessionID).Scan(&taskRows); err != nil {
		t.Fatalf("count channel tasks: %v", err)
	}
	if taskRows != 1 {
		t.Fatalf("notification retry created %d task rows, want 1", taskRows)
	}

	if _, err := f.pool.Exec(context.Background(), `
		UPDATE agent_task_queue
		SET status = 'dispatched', fire_at = now() - interval '1 second'
		WHERE id = $1
	`, task.ID); err != nil {
		t.Fatalf("dispatch task: %v", err)
	}
	if _, err := svcA.PromoteAndNotifyDueChannelTasks(context.Background()); err != nil {
		t.Fatalf("post-dispatch promoter: %v", err)
	}
	if got := recorderA.count() + recorderB.count(); got != 2 {
		t.Fatalf("dispatched task was notified again: total=%d", got)
	}
}

func TestRunDeferredChannelTaskPromoterRunsImmediatelyAndStops(t *testing.T) {
	f := newDurableChannelTaskFixture(t)
	seedDueDeferredChannelTask(t, f)
	recorder := &safeWakeupRecorder{ch: make(chan struct{}, 1)}
	svc := NewTaskService(db.New(f.pool), f.pool, nil, events.New(), recorder)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		svc.RunDeferredChannelTaskPromoter(ctx)
	}()

	select {
	case <-recorder.ch:
		// Immediate run succeeded without waiting for the one-second ticker.
	case <-time.After(750 * time.Millisecond):
		t.Fatal("promoter did not process a due task immediately on start")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("promoter did not stop promptly after context cancellation")
	}
}

func TestDeferredIssueTasksDoNotConflictWithChannelUniqueIndex(t *testing.T) {
	f := newDurableChannelTaskFixture(t)
	ctx := context.Background()
	var issueID pgtype.UUID
	if err := f.pool.QueryRow(ctx, `
		INSERT INTO issue (
			workspace_id, title, status, priority, creator_id, creator_type,
			number, position
		)
		VALUES ($1, 'Deferred issue isolation', 'in_progress', 'none', $2,
		        'member', 990001, 0)
		RETURNING id
	`, f.workspaceID, f.userID).Scan(&issueID); err != nil {
		t.Fatalf("create issue: %v", err)
	}
	for i := 0; i < 2; i++ {
		if _, err := f.pool.Exec(ctx, `
			INSERT INTO agent_task_queue (
				id, agent_id, runtime_id, issue_id, status, priority, fire_at
			)
			VALUES ($1, $2, $3, $4, 'deferred', 0, now() + interval '1 day')
		`, pgtype.UUID{Bytes: [16]byte(uuid.New()), Valid: true}, f.agentID, f.runtimeID, issueID); err != nil {
			t.Fatalf("create deferred issue task %d: %v", i, err)
		}
	}

	svc := NewTaskService(db.New(f.pool), f.pool, nil, events.New())
	prepared, err := svc.PrepareChannelChatTask(ctx, f.session(t), memberChatTaskIdentity(f.userID), false, nil)
	if err != nil {
		t.Fatalf("prepare channel task: %v", err)
	}
	if _, err := db.New(f.pool).UpsertDeferredChannelChatTask(ctx, preparedUpsertParams(f.sessionID, prepared)); err != nil {
		t.Fatalf("upsert deferred channel task alongside issue tasks: %v", err)
	}
	var issueDeferred, chatDeferred int
	if err := f.pool.QueryRow(ctx, `
		SELECT
			count(*) FILTER (WHERE issue_id = $1),
			count(*) FILTER (WHERE chat_session_id = $2)
		FROM agent_task_queue
		WHERE status = 'deferred' AND agent_id = $3
	`, issueID, f.sessionID, f.agentID).Scan(&issueDeferred, &chatDeferred); err != nil {
		t.Fatalf("count isolated deferred tasks: %v", err)
	}
	if issueDeferred != 2 || chatDeferred != 1 {
		t.Fatalf("deferred task counts issue/chat = %d/%d, want 2/1", issueDeferred, chatDeferred)
	}
}
