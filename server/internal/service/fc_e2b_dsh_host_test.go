package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/chattrace"
	"github.com/multica-ai/multica/server/internal/dshhost"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func dshLaunchPools(t *testing.T) (*pgxpool.Pool, *pgxpool.Pool) {
	t.Helper()
	url := os.Getenv("DSH_HOST_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set DSH_HOST_TEST_DATABASE_URL to an isolated test database")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	schema := "dsh_launch_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, err := admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
		if err != nil {
			t.Error(err)
		}
		if err := admin.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	newPool := func() *pgxpool.Pool {
		cfg, err := pgxpool.ParseConfig(url)
		if err != nil {
			t.Fatal(err)
		}
		cfg.ConnConfig.RuntimeParams["search_path"] = schema
		cfg.MaxConns = 1
		pool, err := pgxpool.NewWithConfig(ctx, cfg)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(pool.Close)
		return pool
	}
	a, b := newPool(), newPool()
	for _, stem := range []string{"9223_dsh_employee_host", "9224_dsh_employee_host_identity", "9225_dsh_employee_host_volume", "9226_dsh_employee_host_access_point", "9227_dsh_employee_host_space"} {
		data, err := os.ReadFile(filepath.Join("..", "..", "migrations", stem+".up.sql"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err = a.Exec(ctx, string(data)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = a.Exec(ctx, `CREATE TABLE agent (id uuid,workspace_id uuid);
 CREATE TABLE agent_task_queue (id uuid,agent_id uuid,status text);
 CREATE TABLE agent_task_runtime_start_attempt (task_id uuid,sandbox_id text,status text)`); err != nil {
		t.Fatal(err)
	}
	return a, b
}

type dshLaunchProvider struct {
	mu         sync.Mutex
	creates    int
	destroyErr error
	failCreate bool
	live       map[uuid.UUID]string
}

func (p *dshLaunchProvider) Create(_ context.Context, h dshhost.Host) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.creates++
	id := fmt.Sprintf("sbx-%d", p.creates)
	if p.live == nil {
		p.live = map[uuid.UUID]string{}
	}
	p.live[h.CreateIntent] = id
	if p.failCreate {
		return "", errors.New("lost response")
	}
	return id, nil
}
func (p *dshLaunchProvider) Healthy(context.Context, string) error { return nil }
func (p *dshLaunchProvider) DestroyAndConfirmAbsent(_ context.Context, id string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.destroyErr != nil {
		return p.destroyErr
	}
	for k, v := range p.live {
		if v == id {
			delete(p.live, k)
		}
	}
	return nil
}
func (p *dshLaunchProvider) FindCreated(_ context.Context, h dshhost.Host) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.live[h.CreateIntent], nil
}

type dshHomeRunner struct{ wrongReceipt, wrongNativeReceipt bool }

func (r dshHomeRunner) Run(_ context.Context, _ string, args []string, _ []string) (string, error) {
	values := map[string]string{}
	for i := range len(args) - 1 {
		values[args[i]] = args[i+1]
	}
	if args[len(args)-1] == "--ensure" {
		for _, arg := range args {
			if k, v, ok := strings.Cut(arg, "="); ok {
				values[k] = v
			}
		}
		if r.wrongNativeReceipt {
			values["MULTICA_DSH_AGENT_ID"] = uuid.NewString()
		}
		return fmt.Sprintf(`{"version":1,"workspace_id":%q,"agent_id":%q,"generation":%s}`, values["MULTICA_DSH_WORKSPACE_ID"], values["MULTICA_DSH_AGENT_ID"], values["MULTICA_DSH_HOST_GENERATION"]), nil
	}
	if values["--agent"] == "" {
		return "", nil
	}
	agent := values["--agent"]
	if r.wrongReceipt {
		agent = uuid.NewString()
	}
	return fmt.Sprintf(`{"version":1,"workspace_id":%q,"agent_id":%q,"generation":%s,"mount":%q,"dsh_home":%q,"uid":1000,"gid":1000}`, values["--workspace"], agent, values["--generation"], dshhost.MountPath, dshhost.MountPath+"/home"), nil
}

func dshLaunchFixture(t *testing.T, pool *pgxpool.Pool, p *dshLaunchProvider) (*FCE2BLauncher, db.AgentRuntime, db.AgentTaskQueue) {
	t.Helper()
	workspace, agent := uuid.New(), uuid.New()
	_, err := (dshhost.PostgresStore{DB: pool}).BindStorage(context.Background(), dshhost.Key{WorkspaceID: workspace, AgentID: agent}, dshhost.Storage{
		FileSystemID: "fs-test", SpaceID: uuid.NewString(), VolumeName: uuid.NewString(), AccessPointARN: "acs:nas:cn-beijing:123:accesspoint/ap-test", RoleARN: "role-test", VPCID: "vpc-test", SecurityGroupID: "sg-test", VSwitchIDs: []string{"vsw-test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(context.Background(), `INSERT INTO agent VALUES ($1,$2)`, agent, workspace); err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			w.WriteHeader(204)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/sandboxes/")
		_ = json.NewEncoder(w).Encode(map[string]any{"sandboxID": id, "state": "running", "endAt": time.Now().Add(time.Hour)})
	}))
	t.Cleanup(httpServer.Close)
	u := func(id uuid.UUID) pgtype.UUID { return pgtype.UUID{Bytes: id, Valid: true} }
	rt := db.AgentRuntime{ID: u(uuid.New()), WorkspaceID: u(workspace)}
	task := db.AgentTaskQueue{ID: u(uuid.New()), AgentID: u(agent), RuntimeID: rt.ID}
	launcher := &FCE2BLauncher{Pool: pool, Runner: dshHomeRunner{}, Config: FCE2BConfig{APIURL: httpServer.URL, APIKey: "test-key", SandboxReadyTimeout: time.Second}, dshProvider: func(dshhost.Storage) (dshhost.Provider, error) { return p, nil }}
	return launcher, rt, task
}

func resolveDSHTest(t *testing.T, l *FCE2BLauncher, rt db.AgentRuntime, task db.AgentTaskQueue, template string) (dshhost.Host, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := l.Pool.Acquire(ctx)
	if err != nil {
		return dshhost.Host{}, err
	}
	defer conn.Release()
	release, err := lockDSHEmployee(ctx, conn, rt.WorkspaceID, task.AgentID)
	if err != nil {
		return dshhost.Host{}, err
	}
	defer release()
	trace, err := chattrace.ForTask(task.Context, uuid.UUID(task.ID.Bytes).String(), time.Now())
	if err != nil {
		return dshhost.Host{}, err
	}
	h, _, err := l.resolveDSHEmployeeSandbox(ctx, task, rt, template, conn, trace)
	return h, err
}

func TestDSHLaunchUsesOneConnectionAndOneEmployeeHostAcrossReplicas(t *testing.T) {
	a, b := dshLaunchPools(t)
	provider := &dshLaunchProvider{}
	l, rt, task := dshLaunchFixture(t, a, provider)
	other := *l
	other.Pool = b
	var wg sync.WaitGroup
	for i := range 8 {
		launcher := l
		if i%2 == 1 {
			launcher = &other
		}
		wg.Go(func() {
			h, err := resolveDSHTest(t, launcher, rt, task, "template-1")
			if err != nil || h.SandboxID != "sbx-1" {
				t.Errorf("host=%s error=%v", h.SandboxID, err)
			}
		})
	}
	wg.Wait()
	if provider.creates != 1 {
		t.Fatalf("created %d hosts", provider.creates)
	}
}

func TestDSHNativeHostFailureRequiresConfirmedSandboxRetirement(t *testing.T) {
	a, _ := dshLaunchPools(t)
	provider := &dshLaunchProvider{destroyErr: errors.New("not confirmed")}
	l, rt, task := dshLaunchFixture(t, a, provider)
	l.Runner = dshHomeRunner{wrongNativeReceipt: true}
	if _, err := resolveDSHTest(t, l, rt, task, "template-1"); !errors.Is(err, errDSHHostWaiting) {
		t.Fatal("native Host mismatch did not stop admissions", err)
	}
	l.Runner = dshHomeRunner{}
	if _, err := resolveDSHTest(t, l, rt, task, "template-1"); !errors.Is(err, errDSHHostWaiting) {
		t.Fatal("native failure bypassed sandbox retirement", err)
	}
	if provider.creates != 1 {
		t.Fatal("replacement started before confirmed destruction")
	}
	provider.destroyErr = nil
	host, err := resolveDSHTest(t, l, rt, task, "template-1")
	if err != nil || host.Generation != 2 {
		t.Fatalf("generation=%d err=%v", host.Generation, err)
	}
}

func TestDSHExecutionScopeMatchesConversationIsolation(t *testing.T) {
	u := func() pgtype.UUID { return pgtype.UUID{Bytes: uuid.New(), Valid: true} }
	key := dshhost.Key{WorkspaceID: uuid.New(), AgentID: uuid.New()}
	task := db.AgentTaskQueue{ID: u(), ChatSessionID: u(), IssueID: u(), AutopilotRunID: u()}
	if got := dshExecutionScope(key, task); got.Kind != "issue" || got.ID != uuid.UUID(task.IssueID.Bytes) || got.Key != key {
		t.Fatal("issue scope does not follow task serialization")
	}
	task.IssueID = pgtype.UUID{}
	if got := dshExecutionScope(key, task); got.Kind != "chat" || got.ID != uuid.UUID(task.ChatSessionID.Bytes) {
		t.Fatal("chat scope is not stable")
	}
	task.ChatSessionID = pgtype.UUID{}
	if got := dshExecutionScope(key, task); got.Kind != "task" || got.ID != uuid.UUID(task.ID.Bytes) {
		t.Fatal("unscoped tasks accidentally share a Session")
	}
}

func TestDSHLaunchDrainsSubmittedQueuedTasksBeforeTemplateReplacement(t *testing.T) {
	a, _ := dshLaunchPools(t)
	provider := &dshLaunchProvider{}
	l, rt, task := dshLaunchFixture(t, a, provider)
	first, err := resolveDSHTest(t, l, rt, task, "template-1")
	if err != nil {
		t.Fatal(err)
	}
	queued := uuid.New()
	if _, err = a.Exec(context.Background(), `INSERT INTO agent_task_queue VALUES($1,$2,'queued')`, queued, task.AgentID); err != nil {
		t.Fatal(err)
	}
	if _, err = a.Exec(context.Background(), `INSERT INTO agent_task_runtime_start_attempt VALUES($1,$2,'starting')`, queued, first.SandboxID); err != nil {
		t.Fatal(err)
	}
	if _, err = resolveDSHTest(t, l, rt, task, "template-2"); !errors.Is(err, errDSHHostWaiting) {
		t.Fatal("did not wait for queued submitted runner", err)
	}
	if provider.creates != 1 {
		t.Fatal("replaced active host")
	}
	if _, err = a.Exec(context.Background(), `UPDATE agent_task_queue SET status='completed'`); err != nil {
		t.Fatal(err)
	}
	provider.destroyErr = errors.New("not confirmed")
	if _, err = resolveDSHTest(t, l, rt, task, "template-2"); !errors.Is(err, errDSHHostWaiting) {
		t.Fatal(err)
	}
	if provider.creates != 1 {
		t.Fatal("created despite unconfirmed destruction")
	}
	provider.destroyErr = nil
	next, err := resolveDSHTest(t, l, rt, task, "template-2")
	if err != nil || next.Generation != 2 {
		t.Fatalf("generation=%d err=%v", next.Generation, err)
	}
}

func TestDSHLaunchRecoversUnknownCreateAndRejectsWrongHomeReceipt(t *testing.T) {
	a, _ := dshLaunchPools(t)
	provider := &dshLaunchProvider{failCreate: true}
	l, rt, task := dshLaunchFixture(t, a, provider)
	if _, err := resolveDSHTest(t, l, rt, task, "template-1"); !errors.Is(err, errDSHHostWaiting) {
		t.Fatal(err)
	}
	provider.failCreate = false
	l.Runner = dshHomeRunner{wrongReceipt: true}
	if _, err := resolveDSHTest(t, l, rt, task, "template-1"); err == nil {
		t.Fatal("accepted another employee's Home")
	}
	if provider.creates != 1 {
		t.Fatal("retried ambiguous create")
	}
	l.Runner = dshHomeRunner{}
	if _, err := resolveDSHTest(t, l, rt, task, "template-1"); err != nil {
		t.Fatal(err)
	}
}

func TestDSHEmployeeCapabilityIsNotAdvertisedForOtherProviders(t *testing.T) {
	for _, provider := range FCE2BSupportedProviders {
		caps := FCE2BTemplateCapabilities(provider, FCE2BTemplate{Capabilities: []string{DSHEmployeeHostCapability}})
		found := false
		for _, c := range caps {
			found = found || c == DSHEmployeeHostCapability
		}
		if found != (provider == "dsh") {
			t.Errorf("provider=%s capabilities=%v", provider, caps)
		}
	}
}
