package service

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/dshhost"
	"github.com/multica-ai/multica/server/internal/dshschedule"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Reuses the explicitly opted-in, migrated real preproduction fixture. It never
// launches a sandbox: these tests prove application transactions and queue input,
// while actual model/native UI delivery remains a separate acceptance gate.
func TestDSHScheduleDatabaseFreshAdmissionAcrossReplicas(t *testing.T) {
	for _, scopeKind := range []string{"chat", "issue", "task"} {
		t.Run(scopeKind, func(t *testing.T) {
			s, a, session, agent, pool, _ := dshScheduleDatabaseFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			t.Cleanup(func() {
				for _, table := range []string{"dsh_schedule_occurrence", "dsh_schedule"} {
					if _, err := pool.Exec(context.Background(), "DELETE FROM "+table+" WHERE agent_id=$1", agent.ID); err != nil {
						t.Error(err)
					}
				}
			})
			sourceID := uuid.New()
			if _, err := pool.Exec(ctx, `INSERT INTO agent_task_queue(id,agent_id,runtime_id,status,originator_user_id,chat_session_id) VALUES($1,$2,$3,'completed',$4,$5)`, sourceID, agent.ID, agent.RuntimeID, session.CreatorID, pgtype.UUID{Bytes: session.ID.Bytes, Valid: scopeKind == "chat"}); err != nil {
				t.Fatal(err)
			}
			scope := dshhost.SessionScope{Key: a.access.Key, Kind: scopeKind, ID: sourceID}
			if scopeKind == "issue" {
				issueID := uuid.New()
				if _, err := pool.Exec(ctx, `INSERT INTO issue(id,workspace_id,title,creator_type,creator_id) VALUES($1,$2,'DSH reminder fixture','member',$3)`, issueID, agent.WorkspaceID, session.CreatorID); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if _, err := pool.Exec(context.Background(), `DELETE FROM issue WHERE id=$1`, issueID); err != nil {
						t.Error(err)
					}
				})
				if _, err := pool.Exec(ctx, `UPDATE agent_task_queue SET issue_id=$1 WHERE id=$2`, issueID, sourceID); err != nil {
					t.Fatal(err)
				}
				scope.ID = issueID
			}
			if scopeKind == "chat" {
				scope.ID = uuid.UUID(session.ID.Bytes)
			}
			record := dshschedule.Record{Key: dshschedule.Key{WorkspaceID: a.access.WorkspaceID, AgentID: a.access.AgentID, SessionID: a.input.SessionID, ScheduleID: "schedule-1"}, SourceTaskID: sourceID, Prompt: "present this reminder"}
			if err := pool.QueryRow(ctx, `SELECT id FROM member WHERE workspace_id=$1 AND user_id=$2`, agent.WorkspaceID, session.CreatorID).Scan(&record.OwnerMemberID); err != nil {
				t.Fatal(err)
			}
			if err := pool.QueryRow(ctx, `SELECT date_trunc('milliseconds',clock_timestamp())-interval '1 hour'`).Scan(&record.FirstDue); err != nil {
				t.Fatal(err)
			}
			tx, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(ctx)
			if _, err := (dshhost.PostgresStore{DB: tx}).AdoptNativeExecution(ctx, scope, sourceID, record.SessionID, uuid.New()); err != nil {
				t.Fatal(err)
			}
			record.EverySeconds = 300
			if _, err := (dshschedule.Store{Tx: tx}).Register(ctx, record); err != nil {
				t.Fatal(err)
			}
			sibling := record
			sibling.ScheduleID = "schedule-2"
			sibling.EverySeconds = 600
			if _, err := (dshschedule.Store{Tx: tx}).Register(ctx, sibling); err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			// The first authority read permits the overlay preflight, but a revoked
			// write-time check must roll back task, input and occurrence together.
			var calls atomic.Int32
			denyAtWrite := func(context.Context, *db.Queries, db.Agent, pgtype.UUID) error {
				if calls.Add(1) == 2 {
					return ErrDSHAccessDenied
				}
				return nil
			}
			if _, err := s.DispatchDSHSchedule(ctx, record.Key, denyAtWrite); !errors.Is(err, ErrDSHAccessDenied) {
				t.Fatal("revoked permission admitted", err)
			}
			var count int
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM dsh_schedule_occurrence WHERE agent_id=$1`, agent.ID).Scan(&count); err != nil || count != 0 {
				t.Fatal("denied admission wrote receipt", err)
			}
			// Two independent pools simulate replicas. No task credential survives from
			// the completed parent, and only one winner may commit a fresh input batch.
			second, err := pgxpool.NewWithConfig(ctx, pool.Config().Copy())
			if err != nil {
				t.Fatal(err)
			}
			defer second.Close()
			other := &TaskService{Queries: db.New(second), TxStarter: second, Bus: s.Bus}
			type outcome struct {
				receipt dshschedule.Receipt
				err     error
			}
			out := make(chan outcome, 4)
			var wg sync.WaitGroup
			for i := range 4 {
				wg.Go(func() {
					receipt, err := []*TaskService{s, other}[i%2].DispatchDSHSchedule(ctx, record.Key, a.invoke)
					out <- outcome{receipt, err}
				})
			}
			wg.Wait()
			close(out)
			winners := 0
			for result := range out {
				if errors.Is(result.err, dshschedule.ErrNotDue) {
					continue
				}
				if result.err != nil {
					t.Fatal(result.err)
				}
				winners++
				if len(result.receipt.Reminders) != 2 {
					t.Fatal("application split complete batch")
				}
				task, err := s.Queries.GetAgentTask(ctx, pgtype.UUID{Bytes: result.receipt.TaskID, Valid: true})
				if err != nil {
					t.Fatal(err)
				}
				if task.ID.Bytes == sourceID || task.OriginatorUserID.Valid || task.InitiatorUserID.Valid || task.AccountableUserID != session.CreatorID || task.Status != "queued" {
					t.Fatal("not a fresh automatic task")
				}
				execution, err := dshschedule.LoadExecution(ctx, pool, scope.Key, result.receipt.TaskID)
				if err != nil || !ScheduleExecutionMatches(task, execution) || execution.SessionScope != scope {
					t.Fatal("original scope not preserved", err)
				}
				if scopeKind == "chat" {
					var text string
					if err := pool.QueryRow(ctx, `SELECT content FROM chat_message WHERE task_id=$1`, task.ID).Scan(&text); err != nil || text != result.receipt.Framing() {
						t.Fatal("input not owned by fresh task", err)
					}
				}
			}
			if winners != 1 {
				t.Fatalf("replicas committed %d tasks", winners)
			}
		})
	}
}
