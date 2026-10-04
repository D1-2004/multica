package employeeverification

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/employeetask"
	"github.com/multica-ai/multica/server/internal/scene"
	"github.com/multica-ai/multica/server/internal/service/employeememory"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	alice   = "dingtalk:org-a:uid:alice"
	mallory = "dingtalk:org-a:uid:mallory"
)

// fixture installs the real migrations in an isolated schema. Concurrency
// tests use separate pool connections, never fake serialization.
type fixture struct {
	pool    *pgxpool.Pool
	scope   employeetask.Scope
	cid     string
	tasks   *employeetask.Store
	store   *Store
	memory  *employeememory.Store
	mu      sync.Mutex
	objects map[string][]byte
	// onRead runs inside ReadArtifact (outside every lock) to inject races.
	onRead func(Artifact)
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dsn := os.Getenv("EMPLOYEE_MEMORY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set EMPLOYEE_MEMORY_TEST_DATABASE_URL to an isolated PostgreSQL database")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "employee_verification_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err = admin.Exec(ctx, `CREATE SCHEMA `+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), `DROP SCHEMA `+pgx.Identifier{schema}.Sanitize()+` CASCADE`)
		_ = admin.Close(context.Background())
	})
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	config.MaxConns = 12
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	_, self, _, _ := runtime.Caller(0)
	dir := filepath.Join(filepath.Dir(self), "..", "..", "migrations")
	if _, err = pool.Exec(ctx, `CREATE TABLE workspace (id uuid NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	var files []string
	for _, pattern := range []string{"950[0-2]_*.up.sql", "9511_*.up.sql", "960*.up.sql", "9650_*.up.sql", "9760_*.up.sql", "990*.up.sql", "962[0-4]_*.up.sql", "966[0-4]_*.up.sql", "964[7-9]_*.up.sql", "914[4-5]_*.up.sql", "9153_*.up.sql", "997*.up.sql", "987[01]_*.up.sql"} {
		matches, err := filepath.Glob(filepath.Join(dir, pattern))
		if err != nil {
			t.Fatal(err)
		}
		sort.Strings(matches)
		files = append(files, matches...)
	}
	for _, path := range files {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = pool.Exec(ctx, string(raw)); err != nil {
			t.Fatalf("%s: %v", filepath.Base(path), err)
		}
	}
	ws, agent := uuid.NewString(), uuid.NewString()
	if _, err = pool.Exec(ctx, `INSERT INTO workspace(id) VALUES($1::uuid)`, ws); err != nil {
		t.Fatal(err)
	}
	wsID, _ := util.ParseUUID(ws)
	agentID, _ := util.ParseUUID(agent)
	cid := "cid-" + uuid.NewString()
	row, err := scene.Resolve(ctx, db.New(pool), scene.Owner{WorkspaceID: wsID, AgentID: agentID}, scene.DingTalkConversation("org-a", scene.KindDM, cid), scene.Observation{KindStated: true})
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{
		pool:    pool,
		scope:   employeetask.Scope{WorkspaceID: ws, AgentID: agent, TenantOrgID: "org-a", Kind: employeetask.ScopeScene, Scene: scene.RefOf(row)},
		cid:     cid,
		tasks:   employeetask.NewStore(pool),
		store:   NewStore(pool),
		memory:  employeememory.NewStore(pool),
		objects: map[string][]byte{},
	}
}

func (f *fixture) verifier() *Verifier {
	return &Verifier{DB: f.pool, Evidence: PGEvidence{Reader: f.read}}
}

func (f *fixture) read(_ context.Context, a Artifact) ([]byte, error) {
	if f.onRead != nil {
		f.onRead(a)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	data, ok := f.objects[a.AttachmentID]
	if !ok {
		return nil, errors.New("object missing")
	}
	return append([]byte(nil), data...), nil
}

// task creates an Employee scene Task. namespace "employee_scene" is a human
// message origin; anything else is automation.
func (f *fixture) task(t *testing.T, namespace, requester, goal string) employeetask.Task {
	t.Helper()
	task, err := f.tasks.Create(context.Background(), employeetask.CreateParams{Scope: f.scope, OwnerLoop: employeetask.LoopEmployee, DispatchMode: employeetask.DispatchDirect,
		RequesterRef: requester, Definition: employeetask.Definition{Goal: goal}, Source: employeetask.Source{Namespace: namespace, Key: uuid.NewString()}, Input: goal})
	if err != nil {
		t.Fatal(err)
	}
	return task
}

// run starts and finishes one Run. result is the assistant's final text: a
// claim, never evidence.
func (f *fixture) run(t *testing.T, task employeetask.Task, state employeetask.State, result string) employeetask.Run {
	t.Helper()
	ctx := context.Background()
	current, err := f.tasks.Get(ctx, f.scope, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.State == employeetask.StateSucceeded {
		if current, _, err = f.tasks.Resume(ctx, f.scope, task.ID, employeetask.ResumeParams{Source: employeetask.Source{Namespace: "test", Key: uuid.NewString()}, ActorRef: current.RequesterRef, Body: "again", ExpectedVersion: current.Version}); err != nil {
			t.Fatal(err)
		}
	}
	run, err := f.tasks.StartRun(ctx, f.scope, task.ID, employeetask.StartRunParams{Source: employeetask.Source{Namespace: "test", Key: uuid.NewString()}, QueueTaskID: uuid.NewString(), ExpectedVersion: current.Version})
	if err != nil {
		t.Fatal(err)
	}
	_, run, err = f.tasks.RecordResult(ctx, f.scope, task.ID, employeetask.ResultParams{Source: employeetask.Source{Namespace: "queue_terminal", Key: run.QueueTaskID}, RunID: run.ID, State: state, Result: result})
	if err != nil {
		t.Fatal(err)
	}
	return run
}

// artifact records a ready Run artifact in the Host ledger with its bytes.
func (f *fixture) artifact(t *testing.T, taskID string, run employeetask.Run, name string, data []byte) string {
	t.Helper()
	return f.artifactBound(t, taskID, run.ID, run.QueueTaskID, run.GoalRevision, name, data)
}

func (f *fixture) artifactBound(t *testing.T, taskID, runID, queueID string, goalRevision int64, name string, data []byte) string {
	t.Helper()
	id := uuid.NewString()
	if _, err := f.pool.Exec(context.Background(), `INSERT INTO employee_task_artifact(attachment_id,workspace_id,agent_id,tenant_org_id,scene_id,task_id,run_id,queue_task_id,goal_revision,filename,content_type,sha256,size_bytes,storage_key,storage_url,state)
VALUES($1::uuid,$2::uuid,$3::uuid,$4,$5::uuid,$6::uuid,$7::uuid,$8::uuid,$9,$10,'text/plain',$11,$12,'k/'||$1,'u/'||$1,'ready')`,
		id, f.scope.WorkspaceID, f.scope.AgentID, f.scope.TenantOrgID, f.scope.Scene.SceneID, taskID, runID, queueID, goalRevision, name, sha256Hex(data), len(data)); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.objects[id] = append([]byte(nil), data...)
	f.mu.Unlock()
	return id
}

// delivery records the Run notice and its provider outbox state.
func (f *fixture) delivery(t *testing.T, task employeetask.Task, run employeetask.Run, state, messageID string) {
	t.Helper()
	ctx := context.Background()
	action := "action-" + uuid.NewString()
	if _, err := f.pool.Exec(ctx, `INSERT INTO response_action(id,workspace_id,agent_id,request_id,kind,input,state,provider_conversation_id,provider_message_id) VALUES($1,$2::uuid,$3::uuid,$4,'message.send','{}'::jsonb,$5,'cid-origin',$6)`,
		action, f.scope.WorkspaceID, f.scope.AgentID, run.ID, state, messageID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO employee_run_notice(run_id,workspace_id,agent_id,tenant_org_id,scene_id,task_id,queue_task_id,source_ref,requester_ref,result_state,state,action_id) VALUES($1::uuid,$2::uuid,$3::uuid,$4,$5::uuid,$6::uuid,$7::uuid,'src',$8,'succeeded','enqueued',$9)`,
		run.ID, f.scope.WorkspaceID, f.scope.AgentID, f.scope.TenantOrgID, f.scope.Scene.SceneID, task.ID, run.QueueTaskID, task.RequesterRef, action); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) spec(t *testing.T, task employeetask.Task, origin Origin, author string, checks []Check) Spec {
	t.Helper()
	spec, err := f.store.SetSpec(context.Background(), f.scope, task.ID, SetSpecParams{Origin: origin, SourceRef: "test-source:" + uuid.NewString(), AuthorRef: author, Checks: checks})
	if err != nil {
		t.Fatal(err)
	}
	return spec
}

func (f *fixture) count(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (f *fixture) privateScope(principal string) employeememory.Scope {
	ws, _ := util.ParseUUID(f.scope.WorkspaceID)
	agent, _ := util.ParseUUID(f.scope.AgentID)
	return employeememory.Scope{WorkspaceID: ws, AgentID: agent, TenantOrgID: f.scope.TenantOrgID, Scene: f.scope.Scene, Kind: employeememory.ScopePrivate, PrincipalID: principal}
}

func (f *fixture) sceneScope() employeememory.Scope {
	s := f.privateScope("")
	s.Kind, s.PrincipalID = employeememory.ScopeScene, ""
	return s
}

func intPtr(v int) *int { return &v }

const reportCSV = "姓名,部门,状态\n张三,研发部,已完成\n李四,产品部,已完成\n王五,研发部,进行中\n"
