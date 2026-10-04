package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/employeetask"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/scene"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type directFixture struct {
	service *TaskService
	pool    *pgxpool.Pool
	request DirectTaskRequest
}

func directDatabase(t *testing.T) directFixture {
	t.Helper()
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL required for Direct EmployeeTask integration tests")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	ws, uid, aid, rid := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		for _, table := range []string{"employee_task_entry", "employee_task_run", "employee_task", "agent_scene"} {
			if _, err := pool.Exec(ctx, `DELETE FROM `+table+` WHERE workspace_id=$1::uuid`, ws); err != nil {
				t.Error(err)
			}
		}
		for _, q := range []string{`DELETE FROM task_message WHERE task_id IN (SELECT id FROM agent_task_queue WHERE agent_id=$1::uuid)`, `DELETE FROM agent_task_queue WHERE agent_id=$1::uuid`, `DELETE FROM agent WHERE id=$1::uuid`} {
			if _, err := pool.Exec(ctx, q, aid); err != nil {
				t.Error(err)
			}
		}
		for _, q := range []string{`DELETE FROM agent_runtime WHERE workspace_id=$1::uuid`, `DELETE FROM member WHERE workspace_id=$1::uuid`, `DELETE FROM workspace WHERE id=$1::uuid`} {
			if _, err := pool.Exec(ctx, q, ws); err != nil {
				t.Error(err)
			}
		}
		_, _ = pool.Exec(ctx, `DELETE FROM "user" WHERE id=$1::uuid`, uid)
	})
	exec(`INSERT INTO "user"(id,name,email) VALUES($1::uuid,'Direct fixture',$2)`, uid, uid+"@test.invalid")
	exec(`INSERT INTO workspace(id,name,slug) VALUES($1::uuid,'Direct fixture',$2)`, ws, "direct-"+ws)
	exec(`INSERT INTO member(workspace_id,user_id,role) VALUES($1::uuid,$2::uuid,'owner')`, ws, uid)
	exec(`INSERT INTO agent_runtime(id,workspace_id,daemon_id,name,runtime_mode,provider,status,owner_id,metadata) VALUES($1::uuid,$2::uuid,$3,'Direct fixture','local','codex','online',$4::uuid,'{"client_capabilities":["employee-direct-v1"]}')`, rid, ws, uuid.NewString(), uid)
	exec(`INSERT INTO agent(id,workspace_id,name,runtime_mode,runtime_id,owner_id,permission_mode) VALUES($1::uuid,$2::uuid,'Direct fixture','local',$3::uuid,$4::uuid,'private')`, aid, ws, rid, uid)
	q := db.New(pool)
	w, _ := util.ParseUUID(ws)
	a, _ := util.ParseUUID(aid)
	u, _ := util.ParseUUID(uid)
	sc, err := scene.Resolve(ctx, q, scene.Owner{WorkspaceID: w, AgentID: a}, scene.DingTalkConversation("direct-org", scene.KindGroup, "cid-"+uuid.NewString()), scene.Observation{KindStated: true})
	if err != nil {
		t.Fatal(err)
	}
	task, err := employeetask.NewStore(pool).Create(ctx, employeetask.CreateParams{Scope: employeetask.Scope{WorkspaceID: ws, AgentID: aid, TenantOrgID: "direct-org", Kind: employeetask.ScopeScene, Scene: scene.RefOf(sc)}, OwnerLoop: employeetask.LoopEmployee, DispatchMode: employeetask.DispatchDirect, RequesterRef: "member:" + uid, Definition: employeetask.Definition{Goal: "Produce exact direct result"}, Source: employeetask.Source{Namespace: "test", Key: uuid.NewString()}, Input: "Produce exact direct result"})
	if err != nil {
		t.Fatal(err)
	}
	return directFixture{&TaskService{Queries: q, TxStarter: pool, Bus: events.New()}, pool, DirectTaskRequest{Task: task, Source: employeetask.Source{Namespace: "execute", Key: "first"}, Prompt: "Produce exact direct result", PrincipalID: u, OriginatorUserID: u}}
}

func TestDirectTaskConcurrentReplayAndScope(t *testing.T) {
	f := directDatabase(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	out := make(chan DirectTaskResult, 8)
	errs := make(chan error, 8)
	for range 8 {
		wg.Go(func() { r, e := f.service.EnqueueDirectTask(ctx, f.request); out <- r; errs <- e })
	}
	wg.Wait()
	close(out)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var first DirectTaskResult
	for r := range out {
		if first.Run.ID == "" {
			first = r
		}
		if r.Run.ID != first.Run.ID || r.Task.ID != first.Task.ID {
			t.Fatal("replay created another execution")
		}
	}
	if first.Task.IssueID.Valid || first.Task.AutopilotRunID.Valid || first.Task.ChatSessionID.Valid {
		t.Fatal("direct task created a facade")
	}
	if first.Task.OriginatorUserID != f.request.OriginatorUserID || first.Task.AccountableUserID != f.request.PrincipalID || f.service.ResolveTaskWorkspaceID(ctx, first.Task) != f.request.Task.Scope.WorkspaceID {
		t.Fatal("attribution/scope lost")
	}
	var count int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM agent_task_queue WHERE agent_id=$1`, first.Task.AgentID).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	changed := f.request
	changed.Prompt = "different"
	if _, err := f.service.EnqueueDirectTask(ctx, changed); !errors.Is(err, employeetask.ErrConflict) {
		t.Fatal("changed replay accepted", err)
	}
	changed = f.request
	changed.Source.Key = "another"
	if _, err := f.service.EnqueueDirectTask(ctx, changed); !errors.Is(err, employeetask.ErrActiveRun) {
		t.Fatal("parallel writer accepted", err)
	}
}

func TestDirectTaskTerminalBridgeAndRevision(t *testing.T) {
	for _, status := range []string{"completed", "failed", "cancelled"} {
		t.Run(status, func(t *testing.T) {
			f := directDatabase(t)
			ctx := context.Background()
			r, err := f.service.EnqueueDirectTask(ctx, f.request)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.pool.Exec(ctx, `UPDATE agent_task_queue SET status='running',started_at=now() WHERE id=$1`, r.Task.ID); err != nil {
				t.Fatal(err)
			}
			store := employeetask.NewStore(f.pool)
			latest, err := store.Get(ctx, f.request.Task.Scope, f.request.Task.ID)
			if err != nil {
				t.Fatal(err)
			}
			if status == "completed" {
				_, _, err = store.AppendInput(ctx, latest.Scope, latest.ID, employeetask.InputParams{ExpectedVersion: latest.Version, Source: employeetask.Source{Namespace: "human", Key: "correction"}, ActorRef: "member", Body: "change goal", Correction: &employeetask.Definition{Goal: "Updated goal"}})
				if err != nil {
					t.Fatal(err)
				}
			}
			switch status {
			case "completed":
				_, err = f.service.CompleteTask(ctx, r.Task.ID, []byte(`{"output":"direct result","input_tokens":11,"output_tokens":7}`), "session", "", false, "")
			case "failed":
				_, err = f.service.FailTask(ctx, r.Task.ID, "fake backend failed", "", "", "agent_error", false, "")
			case "cancelled":
				_, err = f.service.CancelTask(ctx, r.Task.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			latest, err = store.Get(ctx, latest.Scope, latest.ID)
			if err != nil {
				t.Fatal(err)
			}
			want := employeetask.State(status)
			if status == "completed" {
				want = employeetask.StateReady
			}
			if latest.State != want || latest.ActiveRunID != "" {
				t.Fatalf("terminal state=%s active=%s", latest.State, latest.ActiveRunID)
			}
			var runState, result string
			if err = f.pool.QueryRow(ctx, `SELECT state,result FROM employee_task_run WHERE id=$1::uuid`, r.Run.ID).Scan(&runState, &result); err != nil {
				t.Fatal(err)
			}
			if status == "completed" && (runState != "succeeded" || result != "direct result") {
				t.Fatal(runState, result)
			}
			if _, err = f.service.ReconcileEmployeeRuns(ctx, 100); err != nil {
				t.Fatal(err)
			}
			var n int
			if err = f.pool.QueryRow(ctx, `SELECT count(*) FROM employee_task_entry WHERE task_id=$1::uuid AND kind='result'`, latest.ID).Scan(&n); err != nil || n != 1 {
				t.Fatal("duplicate result", n, err)
			}
		})
	}
}

func TestDirectTaskRejectsUntrustedPrincipal(t *testing.T) {
	f := directDatabase(t)
	f.request.PrincipalID = pgtype.UUID{Bytes: uuid.New(), Valid: true}
	if _, err := f.service.EnqueueDirectTask(context.Background(), f.request); !errors.Is(err, ErrDirectTaskAccessDenied) {
		t.Fatal(err)
	}
}

func TestDirectTaskCapabilityBackoffAndRecovery(t *testing.T) {
	f := directDatabase(t)
	ctx := context.Background()
	r, err := f.service.EnqueueDirectTask(ctx, f.request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.pool.Exec(ctx, `UPDATE agent_task_queue SET status='dispatched',dispatched_at=now() WHERE id=$1`, r.Task.ID); err != nil {
		t.Fatal(err)
	}
	r.Task, err = f.service.Queries.GetAgentTask(ctx, r.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.service.DeferDirectTaskAfterClaimFailure(ctx, r.Task, "requires employee-direct-v1"); err != nil {
		t.Fatal(err)
	}
	current, err := f.service.Queries.GetAgentTask(ctx, r.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != "deferred" || !current.FireAt.Valid || current.DispatchedAt.Valid {
		t.Fatalf("unsupported claim not deferred: %+v", current)
	}
	if _, err = f.pool.Exec(ctx, `UPDATE agent_task_queue SET status='failed',error='runtime start failed',failure_reason='runtime_start_failed' WHERE id=$1`, r.Task.ID); err != nil {
		t.Fatal(err)
	}
	if n, err := f.service.ReconcileEmployeeRuns(ctx, 100); err != nil || n < 1 {
		t.Fatal(n, err)
	}
	task, err := employeetask.NewStore(f.pool).Get(ctx, f.request.Task.Scope, f.request.Task.ID)
	if err != nil || task.State != employeetask.StateFailed || task.ActiveRunID != "" {
		t.Fatal(task, err)
	}
}
func TestDirectTaskTerminalRollsBackWithRunFailure(t *testing.T) {
	f := directDatabase(t)
	ctx := context.Background()
	r, err := f.service.EnqueueDirectTask(ctx, f.request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.pool.Exec(ctx, `UPDATE agent_task_queue SET status='running',started_at=now() WHERE id=$1`, r.Task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.pool.Exec(ctx, `DELETE FROM employee_task_run WHERE queue_task_id=$1`, r.Task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.service.CompleteTask(ctx, r.Task.ID, []byte(`{"output":"must roll back"}`), "", "", false, ""); err == nil {
		t.Fatal("completion ignored lost Run mapping")
	}
	task, err := f.service.Queries.GetAgentTask(ctx, r.Task.ID)
	if err != nil || task.Status != "running" {
		t.Fatal(task.Status, err)
	}
}

func TestDirectTaskRuntimeCapabilityRequiresCurrentProof(t *testing.T) {
	for _, c := range []struct {
		mode, status, metadata string
		want                   bool
	}{
		{"local", "online", `{"client_capabilities":["employee-direct-v1"]}`, true},
		{"local", "online", `{"capabilities":["employee-direct-v1"]}`, false},
		{"local", "online", `{"cli_version":"999.0.0"}`, false},
		{"local", "offline", `{"client_capabilities":["employee-direct-v1"]}`, false},
		{"cloud", "online", `{"kind":"cloud-sandbox","sandbox_backend":"aliyun_fc","provider":"hermes","artifact_kind":"e2b_template","artifact_ref":"template-fixture","capabilities":["employee-direct-v1"]}`, true},
		{"cloud", "online", `{"client_capabilities":["employee-direct-v1"]}`, false},
	} {
		if got := DirectTaskRuntimeCapable(db.AgentRuntime{RuntimeMode: c.mode, Status: c.status, Metadata: []byte(c.metadata)}); got != c.want {
			t.Fatal(c, got)
		}
	}
}

func TestDirectTaskReplaySurvivesRuntimeContextEnrichmentAndTokenRotation(t *testing.T) {
	f := directDatabase(t)
	ctx := context.Background()
	f.request.Context = []byte(`{"agent_identity_context_token":"private-first","agent_identity_context_token_expires_at":1,"external_identity":{"dws":{"uid":"trusted-employee","orgId":"direct-org"}}}`)
	first, err := f.service.EnqueueDirectTask(ctx, f.request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.pool.Exec(ctx, `UPDATE agent_task_queue SET context=context || '{"runtime_enrichment":"later","agent_identity_context_token":"runtime-rotated","agent_identity_context_token_expires_at":3}'::jsonb WHERE id=$1`, first.Task.ID); err != nil {
		t.Fatal(err)
	}
	f.request.Context = []byte(`{"agent_identity_context_token":"private-second","agent_identity_context_token_expires_at":2,"external_identity":{"dws":{"uid":"trusted-employee","orgId":"direct-org"}}}`)
	replay, err := f.service.EnqueueDirectTask(ctx, f.request)
	if err != nil || replay.Task.ID != first.Task.ID || replay.Run.ID != first.Run.ID {
		t.Fatal("original Host request could not recover execution", err)
	}
	var receipt string
	if err = f.pool.QueryRow(ctx, `SELECT (context->'employee_direct_input')::text FROM agent_task_queue WHERE id=$1`, first.Task.ID).Scan(&receipt); err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"private-first", "private-second", "runtime-rotated"} {
		if strings.Contains(receipt, secret) {
			t.Fatal("credential duplicated into replay receipt")
		}
	}
	f.request.Context = []byte(`{"external_identity":{"dws":{"uid":"different-employee","orgId":"direct-org"}}}`)
	if _, err = f.service.EnqueueDirectTask(ctx, f.request); !errors.Is(err, employeetask.ErrConflict) {
		t.Fatal("changed Host identity reused prior execution", err)
	}
}

func TestDirectTaskConcurrentDistinctSourcesKeepOneWriter(t *testing.T) {
	f := directDatabase(t)
	ctx := context.Background()
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, key := range []string{"attempt-a", "attempt-b"} {
		request := f.request
		request.Source.Key = key
		wg.Go(func() { _, err := f.service.EnqueueDirectTask(ctx, request); errs <- err })
	}
	wg.Wait()
	close(errs)
	winners, blocked := 0, 0
	for err := range errs {
		if err == nil {
			winners++
		} else if errors.Is(err, employeetask.ErrActiveRun) {
			blocked++
		} else {
			t.Fatal(err)
		}
	}
	if winners != 1 || blocked != 1 {
		t.Fatal(winners, blocked)
	}
	var queues, runs int
	if err := f.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM agent_task_queue WHERE agent_id=$1::uuid),(SELECT count(*) FROM employee_task_run WHERE agent_id=$1::uuid)`, f.request.Task.Scope.AgentID).Scan(&queues, &runs); err != nil || queues != 1 || runs != 1 {
		t.Fatal(queues, runs, err)
	}
}

func TestDirectTaskAutomaticAttributionDoesNotImpersonateHuman(t *testing.T) {
	for _, failClosed := range []bool{false, true} {
		t.Run(fmt.Sprint(failClosed), func(t *testing.T) {
			f := directDatabase(t)
			ctx := context.Background()
			f.request.OriginatorUserID = pgtype.UUID{}
			if _, err := f.pool.Exec(ctx, `UPDATE workspace SET attribution_fail_closed=$2 WHERE id=$1::uuid`, f.request.Task.Scope.WorkspaceID, failClosed); err != nil {
				t.Fatal(err)
			}
			r, err := f.service.EnqueueDirectTask(ctx, f.request)
			if failClosed {
				if !errors.Is(err, ErrAttributionFailClosed) {
					t.Fatal(err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if r.Task.OriginatorUserID.Valid || r.Task.AccountableUserID != f.request.PrincipalID || r.Task.OriginatorSource.String != "owner_fallback" || r.Task.TriggerEvidenceKind.String != "employee_task" {
				t.Fatal("automatic trigger impersonated a human or lost evidence")
			}
		})
	}
}
