package dingtalkresponse

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/dwsclient"
)

func firstFeedbackOutboxFixture(t *testing.T, s *Service, pool *pgxpool.Pool) (string, string) {
	t.Helper()
	ctx := context.Background()
	job, receipt := uuid.NewString(), uuid.NewString()
	in := inputFixture()
	for _, sql := range []string{`CREATE TABLE employee_scene_job(id uuid,state text,outcome jsonb,workspace_id uuid,agent_id uuid,tenant_org_id text,scene_id uuid)`, `CREATE TABLE employee_first_feedback(job_id uuid,action_id text,state text)`, `CREATE TABLE employee_scene_participation(workspace_id uuid,agent_id uuid,tenant_org_id text,scene_id uuid,mode text)`} {
		if _, err := pool.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO employee_scene_job(id,state,workspace_id,agent_id,tenant_org_id,scene_id) VALUES($1,'running',$2,$3,$4,$5)`, job, in.WorkspaceID, in.AgentID, in.DWSOrgID, uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	id, err := s.EnqueueFirstFeedback(ctx, tx, in, job, receipt, receipt+"/human-message")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO employee_first_feedback VALUES($1,$2,'enqueued')`, job, id); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return id, job
}
func TestFirstFeedbackDeliveredWithoutReceiptAndUnknownNeverResends(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		t.Run(map[bool]string{false: "delivered", true: "unknown"}[unknown], func(t *testing.T) {
			pool := responsePool(t)
			provider := &fakeProvider{}
			if unknown {
				provider.send = func(context.Context, ActionInput, string) (dwsclient.SendResult, error) {
					return dwsclient.SendResult{}, errors.New("submission uncertain")
				}
			}
			receipts := &fakeReceipts{}
			s := NewService(pool, provider, receipts)
			id, _ := firstFeedbackOutboxFixture(t, s, pool)
			process(t, s)
			dueNow(t, pool, id)
			process(t, s)
			expected := "delivered"
			if unknown {
				expected = "unknown"
			}
			if stateOf(t, pool, id) != expected || provider.sends.Load() != 1 || len(receipts.rows) != 0 {
				t.Fatal(stateOf(t, pool, id), provider.sends.Load(), len(receipts.rows))
			}
			if unknown {
				provider.send = nil
				in := inputFixture()
				final, err := s.Submit(context.Background(), in)
				if err != nil {
					t.Fatal(err)
				}
				process(t, s)
				dueNow(t, pool, final)
				process(t, s)
				if stateOf(t, pool, final) != "delivered" || provider.sends.Load() != 2 || len(receipts.rows) != 1 {
					t.Fatal("unknown feedback blocked final")
				}
			}
		})
	}
}
func TestFirstFeedbackFinalBetweenGuardAndSubmissionIsNotResurrected(t *testing.T) {
	pool := responsePool(t)
	provider := &fakeProvider{}
	receipts := &fakeReceipts{}
	s := NewService(pool, provider, receipts)
	id, job := firstFeedbackOutboxFixture(t, s, pool)
	s.BeforeSend = func(ctx context.Context, in ActionInput) error {
		if _, err := pool.Exec(ctx, `UPDATE employee_scene_job SET state='completed',outcome='{}' WHERE id=$1`, job); err != nil {
			return err
		}
		_, err := pool.Exec(ctx, `UPDATE response_action SET state='cancelled',error_code='host_send_suppressed:final_resolved' WHERE id=$1`, id)
		return err
	}
	process(t, s)
	if stateOf(t, pool, id) != "cancelled" || provider.sends.Load() != 0 || len(receipts.rows) != 0 {
		t.Fatal("cancelled feedback submitted", stateOf(t, pool, id))
	}
}
func TestFirstFeedbackGuardResolutionWithoutOutboxCancellationSuppresses(t *testing.T) {
	pool := responsePool(t)
	provider := &fakeProvider{}
	s := NewService(pool, provider, nil)
	id, job := firstFeedbackOutboxFixture(t, s, pool)
	if _, err := pool.Exec(context.Background(), `UPDATE employee_scene_job SET outcome='{}' WHERE id=$1`, job); err != nil {
		t.Fatal(err)
	}
	process(t, s)
	if stateOf(t, pool, id) != "cancelled" || provider.sends.Load() != 0 {
		t.Fatal("resolved feedback sent or deferred forever")
	}
}
func TestFirstFeedbackDedicatedEnqueueCannotClaimOtherEffects(t *testing.T) {
	pool := responsePool(t)
	s := NewService(pool, nil, nil)
	ctx := context.Background()
	in := inputFixture()
	in.EmployeeFirstFeedbackJobID = uuid.NewString()
	if _, err := s.Submit(ctx, in); err == nil {
		t.Fatal("ordinary enqueue forged feedback")
	}
	in.EmployeeFirstFeedbackJobID = ""
	job, receipt := uuid.NewString(), uuid.NewString()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	id, err := s.EnqueueFirstFeedback(ctx, tx, in, job, receipt, receipt+"/m")
	if err != nil {
		t.Fatal(err)
	}
	same, err := s.EnqueueFirstFeedback(ctx, tx, in, job, receipt, receipt+"/m")
	if err != nil || id != same {
		t.Fatal("stable key", same, err)
	}
	in.Text = "changed"
	if _, err = s.EnqueueFirstFeedback(ctx, tx, in, job, receipt, receipt+"/m"); err == nil {
		t.Fatal("body changed on stable key")
	}
	if err = tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	var n int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM response_action`).Scan(&n); err != nil || n != 0 {
		t.Fatal("uncommitted feedback persisted", n, err)
	}
}
