package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	a2aintegration "github.com/multica-ai/multica/server/internal/integrations/a2a"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestA2ARequestBoundQueuedIdentityRequiresMatchingLiveRequest(t *testing.T) {
	t.Parallel()
	now := time.Now()
	lease := pgtype.Timestamptz{Time: now.Add(time.Minute), Valid: true}
	taskContext := []byte(a2aTaskDEAPDWSContextJSON)

	if got := a2AQueuedExternalIdentityDispositionFor(
		context.Background(), taskContext, "request-b", lease, now,
	); got != a2aQueuedIdentityWait {
		t.Fatalf("live lease without request context disposition = %v, want wait", got)
	}

	ctx := a2aintegration.WithInvocationIdentity(
		context.Background(),
		a2aintegration.InvocationIdentity{DEAPDWSToken: "request-b-token"},
	)
	ctx, holder := withA2ARequestBoundTurnClaimHolder(ctx)
	holder.claim = &a2aRequestBoundTurnClaim{requestFingerprint: "request-b"}
	if got := a2AQueuedExternalIdentityDispositionFor(
		ctx, taskContext, "request-b", lease, now,
	); got != a2aQueuedIdentityReady {
		t.Fatalf("matching live request disposition = %v, want ready", got)
	}
	if got := a2AQueuedExternalIdentityDispositionFor(
		ctx, taskContext, "request-a", lease, now,
	); got != a2aQueuedIdentityWait {
		t.Fatalf("mismatched live request disposition = %v, want wait", got)
	}

	expired := pgtype.Timestamptz{Time: now.Add(-time.Second), Valid: true}
	if got := a2AQueuedExternalIdentityDispositionFor(
		ctx, taskContext, "request-a", expired, now,
	); got != a2aQueuedIdentityAuthRequired {
		t.Fatalf("expired mismatched request disposition = %v, want auth required", got)
	}
}

type requestBoundQueueController struct {
	calls    int
	task     db.AgentTaskQueue
	identity a2aintegration.InvocationIdentity
}

func (c *requestBoundQueueController) NotifyA2ATaskEnqueued(ctx context.Context, task db.AgentTaskQueue) {
	c.calls++
	c.task = task
	c.identity, _ = a2aintegration.InvocationIdentityFromContext(ctx)
}

func (*requestBoundQueueController) CancelTask(context.Context, pgtype.UUID) (*db.AgentTaskQueue, error) {
	return nil, errors.New("unexpected cancel")
}

func TestA2ARequestBoundTurnQueuesUntilItsOwnRequestPromotesIt(t *testing.T) {
	f := newDurableChannelTaskFixture(t)
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, `
		ALTER TABLE a2a_task_turn
		ADD COLUMN IF NOT EXISTS request_bound_lease_expires_at TIMESTAMPTZ;
		ALTER TABLE a2a_task_turn
		DROP CONSTRAINT IF EXISTS a2a_task_turn_control_signal_check;
		ALTER TABLE a2a_task_turn
		ADD CONSTRAINT a2a_task_turn_control_signal_check
		CHECK (control_signal IS NULL OR control_signal IN ('input_required', 'auth_required', 'request_bound'))
	`); err != nil {
		t.Fatalf("ensure request-bound queue schema: %v", err)
	}
	queries := db.New(f.pool)

	first, err := queries.CreateA2AChatTask(ctx, db.CreateA2AChatTaskParams{
		AgentID:       f.agentID,
		RuntimeID:     f.runtimeID,
		ChatSessionID: f.sessionID,
		TaskContext:   newA2ATaskContext(),
	})
	if err != nil {
		t.Fatalf("create first A2A task: %v", err)
	}
	secondToken := "second-request-token-must-not-persist"
	second, err := queries.CreateA2AChatTask(ctx, db.CreateA2AChatTaskParams{
		AgentID:       f.agentID,
		RuntimeID:     f.runtimeID,
		ChatSessionID: f.sessionID,
		TaskContext: newA2ATaskContext(a2aintegration.InvocationIdentity{
			DEAPDWSToken: secondToken,
		}),
	})
	if err != nil {
		t.Fatalf("create second A2A task: %v", err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE agent_task_queue SET status = 'running' WHERE id = $1`, first.ID); err != nil {
		t.Fatalf("start first A2A task: %v", err)
	}

	firstInput, err := queries.CreateChatMessage(ctx, db.CreateChatMessageParams{
		ChatSessionID: f.sessionID,
		Role:          "user",
		Content:       "first",
		TaskID:        first.ID,
	})
	if err != nil {
		t.Fatalf("create first input: %v", err)
	}
	secondInput, err := queries.CreateChatMessage(ctx, db.CreateChatMessageParams{
		ChatSessionID: f.sessionID,
		Role:          "user",
		Content:       "second",
		TaskID:        second.ID,
	})
	if err != nil {
		t.Fatalf("create second input: %v", err)
	}

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	var endpointID, clientID, contextID, bindingID pgtype.UUID
	if err := f.pool.QueryRow(ctx, `
		INSERT INTO agent_a2a_endpoint (
			workspace_id, agent_id, public_agent_id, enabled,
			delegated_by_user_id, card_name
		)
		VALUES ($1, $2, $3, TRUE, $4, 'request-bound queue')
		RETURNING id
	`, f.workspaceID, f.agentID, "request_bound_agent_"+suffix, f.userID).Scan(&endpointID); err != nil {
		t.Fatalf("create endpoint: %v", err)
	}
	if err := f.pool.QueryRow(ctx, `
		INSERT INTO a2a_client (endpoint_id, name, scopes, created_by, updated_by)
		VALUES ($1, 'request-bound queue', ARRAY['send','read']::text[], $2, $2)
		RETURNING id
	`, endpointID, f.userID).Scan(&clientID); err != nil {
		t.Fatalf("create client: %v", err)
	}
	if err := f.pool.QueryRow(ctx, `
		INSERT INTO a2a_context (endpoint_id, client_id, public_context_id, chat_session_id)
		VALUES ($1, $2, $3, $4)
		RETURNING id
	`, endpointID, clientID, "ctx_request_bound_"+suffix, f.sessionID).Scan(&contextID); err != nil {
		t.Fatalf("create context: %v", err)
	}
	firstFingerprint := strings.Repeat("a", 64)
	secondFingerprint := strings.Repeat("b", 64)
	publicTaskID := "tsk_request_bound_" + suffix
	if err := f.pool.QueryRow(ctx, `
		INSERT INTO a2a_task_binding (
			endpoint_id, client_id, context_id, public_task_id, message_id,
			request_fingerprint, artifact_id, root_local_task_id,
			input_chat_message_id, public_state
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, 'TASK_STATE_WORKING')
		RETURNING id
	`, endpointID, clientID, contextID, publicTaskID, "message-first-"+suffix,
		firstFingerprint, "art_request_bound_"+suffix, first.ID, firstInput.ID).Scan(&bindingID); err != nil {
		t.Fatalf("create binding: %v", err)
	}
	if _, err := f.pool.Exec(ctx, `
		INSERT INTO a2a_task_turn (
			binding_id, endpoint_id, client_id, sequence, message_id,
			request_fingerprint, local_task_id, input_chat_message_id, input_parts
		)
		VALUES
			($1, $2, $3, 1, $4, $5, $6, $7, '[]'::jsonb),
			($1, $2, $3, 2, $8, $9, $10, $11, '[]'::jsonb)
	`, bindingID, endpointID, clientID,
		"message-first-"+suffix, firstFingerprint, first.ID, firstInput.ID,
		"message-second-"+suffix, secondFingerprint, second.ID, secondInput.ID,
	); err != nil {
		t.Fatalf("create turns: %v", err)
	}
	if _, err := queries.SetA2ARequestBoundTurnLease(ctx, db.SetA2ARequestBoundTurnLeaseParams{
		LeaseExpiresAt:     pgtype.Timestamptz{Time: time.Now().Add(time.Minute), Valid: true},
		LocalTaskID:        second.ID,
		RequestFingerprint: secondFingerprint,
	}); err != nil {
		t.Fatalf("set second request lease: %v", err)
	}

	if promoted, err := queries.PromoteDueDeferredTasksForRuntime(ctx, f.runtimeID); err != nil {
		t.Fatalf("daemon promotion while first task runs: %v", err)
	} else if len(promoted) != 0 {
		t.Fatalf("daemon promoted %d request-bound tasks while predecessor ran", len(promoted))
	}
	if _, err := f.pool.Exec(ctx, `
		UPDATE agent_task_queue SET status = 'completed', completed_at = now() WHERE id = $1
	`, first.ID); err != nil {
		t.Fatalf("complete first A2A task: %v", err)
	}
	if promoted, err := queries.PromoteDueDeferredTasksForRuntime(ctx, f.runtimeID); err != nil {
		t.Fatalf("daemon promotion after predecessor completed: %v", err)
	} else if len(promoted) != 0 {
		t.Fatalf("daemon promoted %d request-bound tasks without the live token", len(promoted))
	}

	recorder := &requestBoundQueueController{}
	svc := NewA2AService(queries, f.pool, recorder, nil)
	wrongCtx := a2aintegration.WithInvocationIdentity(ctx, a2aintegration.InvocationIdentity{
		DEAPDWSToken: "first-request-token",
	})
	wrongCtx, wrongHolder := withA2ARequestBoundTurnClaimHolder(wrongCtx)
	wrongHolder.claim = &a2aRequestBoundTurnClaim{
		requestFingerprint: firstFingerprint,
		localTaskID:        first.ID,
	}
	svc.notifyNextA2ATask(wrongCtx, f.sessionID)
	var status string
	if err := f.pool.QueryRow(ctx, `SELECT status FROM agent_task_queue WHERE id = $1`, second.ID).Scan(&status); err != nil {
		t.Fatalf("load waiting second task: %v", err)
	}
	if status != "deferred" || recorder.calls != 0 {
		t.Fatalf("mismatched request promoted task: status=%s calls=%d", status, recorder.calls)
	}

	requestCtx := a2aintegration.WithInvocationIdentity(ctx, a2aintegration.InvocationIdentity{
		DEAPDWSToken: secondToken,
	})
	requestCtx, holder := withA2ARequestBoundTurnClaimHolder(requestCtx)
	holder.claim = &a2aRequestBoundTurnClaim{
		requestFingerprint: secondFingerprint,
		localTaskID:        second.ID,
		chatSessionID:      f.sessionID,
	}
	svc.notifyNextA2ATask(requestCtx, f.sessionID)
	if err := f.pool.QueryRow(ctx, `SELECT status FROM agent_task_queue WHERE id = $1`, second.ID).Scan(&status); err != nil {
		t.Fatalf("load promoted second task: %v", err)
	}
	if status != "queued" || recorder.calls != 1 || recorder.identity.DEAPDWSToken != secondToken {
		t.Fatalf("matching request promotion: status=%s calls=%d identity=%q", status, recorder.calls, recorder.identity.DEAPDWSToken)
	}
	if strings.Contains(string(second.Context), secondToken) {
		t.Fatal("request-scoped DWS token leaked into the durable task context")
	}
}
