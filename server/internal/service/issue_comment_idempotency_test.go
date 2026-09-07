package service

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type issueFollowUpFixture struct {
	pool   *pgxpool.Pool
	svc    *IssueCommentService
	params IssueCommentCreateParams
}

func newIssueFollowUpFixture(t *testing.T) issueFollowUpFixture {
	t.Helper()
	if os.Getenv("DATABASE_URL") == "" {
		t.Skip("set DATABASE_URL to an isolated migrated test database")
	}
	pool := newResolveOriginatorPool(t)
	workspaceID, userID, agentID, issueID := seedAttributionFixture(t, pool)
	q := db.New(pool)
	issue, err := q.GetIssue(context.Background(), util.MustParseUUID(issueID))
	if err != nil {
		t.Fatal(err)
	}
	agent, err := q.GetAgent(context.Background(), util.MustParseUUID(agentID))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		for _, statement := range []string{
			`DELETE FROM attachment WHERE issue_id = $1`,
			`DELETE FROM agent_task_queue WHERE issue_id = $1`,
			`DELETE FROM comment WHERE issue_id = $1`,
			`DELETE FROM issue WHERE id = $1`,
		} {
			if _, err := pool.Exec(ctx, statement, issueID); err != nil {
				t.Errorf("cleanup follow-up fixture: %v", err)
			}
		}
		for _, item := range []struct {
			query string
			arg   any
		}{
			{`DELETE FROM agent WHERE id = $1`, agentID},
			{`DELETE FROM agent_runtime WHERE id = $1`, agent.RuntimeID},
			{`DELETE FROM member WHERE workspace_id = $1`, workspaceID},
		} {
			if _, err := pool.Exec(ctx, item.query, item.arg); err != nil {
				t.Errorf("cleanup follow-up identity: %v", err)
			}
		}
	})
	bus := events.New()
	tasks := &TaskService{Queries: q, TxStarter: pool, Bus: bus}
	return issueFollowUpFixture{
		pool: pool, svc: NewIssueCommentService(q, bus, tasks),
		params: IssueCommentCreateParams{
			Issue: issue, AuthorID: util.MustParseUUID(userID), Content: "须莫：可以，周五三点没问题。",
			IdempotencyKey: "window-1:item-message-1", AgentIdentityContextToken: "task-scoped-token",
			DispatchContext: []byte(`{"dispatch_idempotency_key":"outer-acceptance-key","dispatch_source":"digital_employee"}`),
		},
	}
}

func (f issueFollowUpFixture) assertCounts(t *testing.T, comments, tasks int) {
	t.Helper()
	var actualComments, actualTasks int
	if err := f.pool.QueryRow(context.Background(), `SELECT
		(SELECT count(*) FROM comment WHERE issue_id = $1),
		(SELECT count(*) FROM agent_task_queue WHERE issue_id = $1)`, f.params.Issue.ID).Scan(&actualComments, &actualTasks); err != nil {
		t.Fatal(err)
	}
	if actualComments != comments || actualTasks != tasks {
		t.Fatalf("comments/tasks=%d/%d, want %d/%d", actualComments, actualTasks, comments, tasks)
	}
}

func TestIssueFollowUpIdempotencyConcurrentAndCompletedReplay(t *testing.T) {
	f := newIssueFollowUpFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var commentEvents atomic.Int32
	f.svc.Bus.Subscribe(protocol.EventCommentCreated, func(event events.Event) {
		commentEvents.Add(1)
		// This uses another pool connection: a pre-commit event would see no
		// committed pair and fail even though the creating transaction sees it.
		var count int
		if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM agent_task_queue WHERE issue_id = $1`, f.params.Issue.ID).Scan(&count); err != nil || count != 1 {
			t.Errorf("comment event fired before its task committed: count=%d err=%v", count, err)
		}
	})
	const workers = 6
	start := make(chan struct{})
	results := make(chan IssueCommentCreateResult, workers)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			result, err := f.svc.CreateExternalFollowUp(ctx, f.params, IssueCommentCreateOpts{})
			results <- result
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var first IssueCommentCreateResult
	for result := range results {
		if !first.Task.ID.Valid {
			first = result
		}
		if result.Task.ID != first.Task.ID || result.Comment.ID != first.Comment.ID {
			t.Fatalf("one operation produced different receipts: first=%s/%s got=%s/%s", util.UUIDToString(first.Comment.ID), util.UUIDToString(first.Task.ID), util.UUIDToString(result.Comment.ID), util.UUIDToString(result.Task.ID))
		}
	}
	f.assertCounts(t, 1, 1)
	if commentEvents.Load() != 1 {
		t.Fatalf("duplicate comment events: %d", commentEvents.Load())
	}
	var private map[string]any
	if err := json.Unmarshal(first.Task.Context, &private); err != nil {
		t.Fatal(err)
	}
	if private["dispatch_idempotency_key"] != "outer-acceptance-key" || private[issueFollowUpKeyField] != f.params.IdempotencyKey || private["agent_identity_context_token"] != "task-scoped-token" {
		t.Fatalf("normal dispatch/identity context was not preserved: fields=%v", map[string]any{"dispatch": private["dispatch_idempotency_key"], "follow_up": private[issueFollowUpKeyField]})
	}
	if first.Task.TriggerCommentID != first.Comment.ID || first.Task.AccountableUserID != f.params.AuthorID || first.Task.OriginatorUserID != f.params.AuthorID {
		t.Fatalf("normal comment attribution was bypassed: task=%s", util.UUIDToString(first.Task.ID))
	}
	if _, err := f.pool.Exec(ctx, `UPDATE agent_task_queue SET status = 'completed', completed_at = now() WHERE id = $1`, first.Task.ID); err != nil {
		t.Fatal(err)
	}
	replayed, err := f.svc.CreateExternalFollowUp(ctx, f.params, IssueCommentCreateOpts{})
	if err != nil || replayed.Task.ID != first.Task.ID || replayed.Comment.ID != first.Comment.ID || replayed.Task.Status != "completed" {
		t.Fatalf("completed task retry was not recovered: result=%s status=%q err=%v", util.UUIDToString(replayed.Task.ID), replayed.Task.Status, err)
	}
	f.assertCounts(t, 1, 1)
}

func TestIssueFollowUpIdempotencyRejectsChangedContentAndBusyNewOperation(t *testing.T) {
	f := newIssueFollowUpFixture(t)
	ctx := context.Background()
	first, err := f.svc.CreateExternalFollowUp(ctx, f.params, IssueCommentCreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	changed := f.params
	changed.Content = "不是三点，改为四点。"
	if _, err := f.svc.CreateExternalFollowUp(ctx, changed, IssueCommentCreateOpts{}); !errors.Is(err, ErrIssueFollowUpIdempotencyConflict) {
		t.Fatalf("changed payload reused an accepted operation: %v", err)
	}
	changed.IdempotencyKey = "window-2:item-message-2"
	if _, err := f.svc.CreateExternalFollowUp(ctx, changed, IssueCommentCreateOpts{}); !errors.Is(err, ErrIssueDispatchPending) {
		t.Fatalf("different operation must wait for existing task: %v", err)
	}
	f.assertCounts(t, 1, 1)
	if _, err := f.pool.Exec(ctx, `UPDATE agent_task_queue SET status = 'completed', completed_at = now() WHERE id = $1`, first.Task.ID); err != nil {
		t.Fatal(err)
	}
	second, err := f.svc.CreateExternalFollowUp(ctx, changed, IssueCommentCreateOpts{})
	if err != nil || second.Task.ID == first.Task.ID || second.Comment.ID == first.Comment.ID {
		t.Fatalf("new authorized input must create its own pair after capacity frees: err=%v", err)
	}
	f.assertCounts(t, 2, 2)
}

func TestIssueFollowUpIdempotencyRollsBackCommentAndAttachmentOnEnqueueFailure(t *testing.T) {
	f := newIssueFollowUpFixture(t)
	ctx := context.Background()
	agent, err := f.svc.Queries.GetAgent(ctx, f.params.Issue.AssigneeID)
	if err != nil {
		t.Fatal(err)
	}
	var attachmentID pgtype.UUID
	if err := f.pool.QueryRow(ctx, `INSERT INTO attachment (workspace_id, issue_id, uploader_type, uploader_id, filename, url, content_type, size_bytes)
		VALUES ($1, $2, 'member', $3, 'reply.txt', 'https://example.test/reply.txt', 'text/plain', 12) RETURNING id`, f.params.Issue.WorkspaceID, f.params.Issue.ID, f.params.AuthorID).Scan(&attachmentID); err != nil {
		t.Fatal(err)
	}
	f.params.AttachmentIDs = []pgtype.UUID{attachmentID}
	var eventsCount atomic.Int32
	f.svc.Bus.SubscribeAll(func(event events.Event) { eventsCount.Add(1) })
	if _, err := f.pool.Exec(ctx, `UPDATE agent SET runtime_id = NULL WHERE id = $1`, agent.ID); err != nil {
		t.Fatal(err)
	}
	result, err := f.svc.CreateExternalFollowUp(ctx, f.params, IssueCommentCreateOpts{})
	if err == nil || !strings.Contains(err.Error(), "runtime") || result.Comment.ID.Valid || result.Task.ID.Valid {
		t.Fatalf("enqueue failure must return no partially accepted result: result=%s/%s err=%v", util.UUIDToString(result.Comment.ID), util.UUIDToString(result.Task.ID), err)
	}
	f.assertCounts(t, 0, 0)
	var commentID pgtype.UUID
	if err := f.pool.QueryRow(ctx, `SELECT comment_id FROM attachment WHERE id = $1`, attachmentID).Scan(&commentID); err != nil || commentID.Valid {
		t.Fatalf("attachment binding escaped rollback: comment=%s err=%v", util.UUIDToString(commentID), err)
	}
	if eventsCount.Load() != 0 {
		t.Fatalf("failed transaction emitted %d events", eventsCount.Load())
	}
	if _, err := f.pool.Exec(ctx, `UPDATE agent SET runtime_id = $2 WHERE id = $1`, agent.ID, agent.RuntimeID); err != nil {
		t.Fatal(err)
	}
	result, err = f.svc.CreateExternalFollowUp(ctx, f.params, IssueCommentCreateOpts{})
	if err != nil || len(result.Attachments) != 1 || result.Attachments[0].CommentID != result.Comment.ID {
		t.Fatalf("same operation must be recoverable after rollback: attachments=%d err=%v", len(result.Attachments), err)
	}
	f.assertCounts(t, 1, 1)
}

type lostCommitAcknowledgementStarter struct {
	pool *pgxpool.Pool
	lost atomic.Bool
}

func (s *lostCommitAcknowledgementStarter) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return &lostCommitAcknowledgementTx{Tx: tx, starter: s}, nil
}

type lostCommitAcknowledgementTx struct {
	pgx.Tx
	starter *lostCommitAcknowledgementStarter
}

func (tx *lostCommitAcknowledgementTx) Commit(ctx context.Context) error {
	if err := tx.Tx.Commit(ctx); err != nil {
		return err
	}
	if tx.starter.lost.CompareAndSwap(false, true) {
		return errors.New("simulated lost commit acknowledgement")
	}
	return nil
}

func TestIssueFollowUpIdempotencyRecoversCommittedTransactionAfterLostAcknowledgement(t *testing.T) {
	f := newIssueFollowUpFixture(t)
	f.svc.TaskService.TxStarter = &lostCommitAcknowledgementStarter{pool: f.pool}
	ctx := context.Background()
	if _, err := f.svc.CreateExternalFollowUp(ctx, f.params, IssueCommentCreateOpts{}); err == nil || !strings.Contains(err.Error(), "lost commit acknowledgement") {
		t.Fatalf("fault injection did not exercise uncertain commit: %v", err)
	}
	f.assertCounts(t, 1, 1)
	result, err := f.svc.CreateExternalFollowUp(ctx, f.params, IssueCommentCreateOpts{})
	if err != nil || !result.Comment.ID.Valid || !result.Task.ID.Valid {
		t.Fatalf("retry did not recover the committed receipt: err=%v", err)
	}
	f.assertCounts(t, 1, 1)
}
