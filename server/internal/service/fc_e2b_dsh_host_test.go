package service

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
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
	"github.com/multica-ai/multica/server/internal/dshprofile"
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
	for _, stem := range []string{"9223_dsh_employee_host", "9224_dsh_employee_host_identity", "9225_dsh_employee_host_volume", "9226_dsh_employee_host_access_point", "9227_dsh_employee_host_space", "9235_dsh_native_access", "9241_dsh_employee_profile", "9242_dsh_employee_profile_identity", "9243_dsh_profile_revision_identity", "9244_dsh_plugin_build_identity", "9257_employee_filesystem_sandbox", "9258_employee_filesystem_sandbox_scope", "9259_employee_filesystem_host"} {
		data, err := os.ReadFile(filepath.Join("..", "..", "migrations", stem+".up.sql"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err = a.Exec(ctx, string(data)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = a.Exec(ctx, `CREATE TABLE workspace(id uuid);
 CREATE TABLE agent (id uuid,workspace_id uuid,kind text DEFAULT 'user',archived_at timestamptz,runtime_mode text DEFAULT 'cloud');
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

type dshHomeRunner struct {
	wrongReceipt, wrongNativeReceipt bool
	profiles                         *sync.Map
}

func TestDSHNativeManagedProfileReceipt(t *testing.T) {
	models := []string{"fixture-model"}
	raw, digest, err := dshManagedCatalog(models)
	if err != nil || raw != `["fixture-model"]` || digest != "5500dbd702e43af880c4c9771cb08f3209a658983d683a8d153f4ab4ce7c7dc4" {
		t.Fatalf("invalid managed catalog receipt: %v", err)
	}
	for _, invalid := range [][]string{nil, {""}, {"a", "a"}, {" model"}, {"a\n"}} {
		if _, _, err := dshManagedCatalog(invalid); err == nil {
			t.Fatal("invalid managed catalog accepted")
		}
	}
	host := dshhost.Host{Key: dshhost.Key{WorkspaceID: uuid.New(), AgentID: uuid.New()}, Generation: 2, SandboxID: "sandbox"}
	descriptor, descriptorDigest, err := dshprofile.Resolve(host.Key, 1, dshprofile.Source{TemplateID: "template-1"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	revision := dshprofile.Revision{ID: 1, TemplateID: "template-1", Descriptor: descriptor, Digest: descriptorDigest}
	runner := dshHomeRunner{profiles: &sync.Map{}}
	launcher := &FCE2BLauncher{Runner: runner}
	if err := launcher.stageDSHProfile(context.Background(), host, revision); err != nil {
		t.Fatal(err)
	}
	out, err := runner.Run(context.Background(), "", dshNativeHostEnsureArgs(host, raw, "https://pre.multica.test", "https://33124-sandbox.fc.test", strings.Repeat("a", 64), revision), nil)
	if err != nil || validateDSHNativeHostReceipt(out, host, digest, revision) != nil {
		t.Fatalf("matching managed profile rejected: %v", err)
	}
	_, changed, _ := dshManagedCatalog([]string{"other-model"})
	if validateDSHNativeHostReceipt(out, host, changed, revision) == nil || validateDSHNativeHostReceipt(out, host, "", revision) == nil {
		t.Fatal("a different or missing managed profile was accepted")
	}
	var receipt map[string]any
	if err := json.Unmarshal([]byte(out), &receipt); err != nil {
		t.Fatal(err)
	}
	delete(receipt, "managed_profile_digest")
	missing, _ := json.Marshal(receipt)
	if validateDSHNativeHostReceipt(string(missing), host, digest, revision) == nil {
		t.Fatal("native process health without plugin configuration proof was accepted")
	}
	if err := json.Unmarshal([]byte(out), &receipt); err != nil {
		t.Fatal(err)
	}
	delete(receipt, "employee_profile")
	missing, _ = json.Marshal(receipt)
	if validateDSHNativeHostReceipt(string(missing), host, digest, revision) == nil {
		t.Fatal("missing employee Profile receipt accepted")
	}
	changedRevision := revision
	changedRevision.ID++
	if validateDSHNativeHostReceipt(out, host, digest, changedRevision) == nil {
		t.Fatal("another employee Profile revision accepted")
	}
}

func (r dshHomeRunner) Run(_ context.Context, _ string, args []string, _ []string) (string, error) {
	values := map[string]string{}
	for i := range len(args) - 1 {
		values[args[i]] = args[i+1]
	}
	for _, arg := range args {
		if k, v, ok := strings.Cut(arg, "="); ok {
			values[k] = v
		}
	}
	if args[len(args)-1] == "--stage" {
		if len(strings.Join(args, " ")) > 64000 {
			return "", errors.New("Profile invocation exceeds argument budget")
		}
		count, err := strconv.Atoi(values["MULTICA_DSH_PROFILE_PART_COUNT"])
		if err != nil {
			return "", err
		}
		digest := values["MULTICA_DSH_PROFILE_DIGEST"]
		if index, ok := values["MULTICA_DSH_PROFILE_PART_INDEX"]; ok {
			part, err := strconv.Atoi(index)
			if err != nil {
				return "", err
			}
			r.profiles.Store(digest+"/"+index, values["MULTICA_DSH_PROFILE_PART"])
			out, _ := json.Marshal(map[string]any{"workspace_id": values["MULTICA_DSH_WORKSPACE_ID"], "agent_id": values["MULTICA_DSH_AGENT_ID"], "digest": digest, "part_index": part, "part_count": count})
			return string(out), nil
		}
		var encoded string
		for i := 0; i < count; i++ {
			chunk, ok := r.profiles.Load(digest + "/" + strconv.Itoa(i))
			if !ok {
				return "", errors.New("missing Profile part")
			}
			encoded += chunk.(string)
		}
		raw, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return "", err
		}
		var descriptor dshprofile.Descriptor
		if err := json.Unmarshal(raw, &descriptor); err != nil {
			return "", err
		}
		digest = fmt.Sprintf("%x", sha256.Sum256(raw))
		path := dshhost.MountPath + "/home/.multica/profile-inputs/" + digest + ".json"
		r.profiles.Store(path, string(raw))
		out, _ := json.Marshal(map[string]string{"workspace_id": values["MULTICA_DSH_WORKSPACE_ID"], "agent_id": values["MULTICA_DSH_AGENT_ID"], "revision": descriptor.Revision, "digest": digest, "path": path})
		return string(out), nil
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
		var models []string
		if err := json.Unmarshal([]byte(values["MULTICA_DSH_MODEL_CATALOG_JSON"]), &models); err != nil {
			return "", err
		}
		_, digest, err := dshManagedCatalog(models)
		if err != nil {
			return "", err
		}
		var profile dshprofile.Descriptor
		stored, ok := r.profiles.Load(values["MULTICA_DSH_EMPLOYEE_PROFILE_FILE"])
		if !ok {
			return "", errors.New("missing staged Profile")
		}
		profileRaw := stored.(string)
		if err := json.Unmarshal([]byte(profileRaw), &profile); err != nil {
			return "", err
		}
		profileDigest := fmt.Sprintf("%x", sha256.Sum256([]byte(profileRaw)))
		return fmt.Sprintf(`{"version":1,"workspace_id":%q,"agent_id":%q,"generation":%s,"managed_profile_digest":%q,"employee_profile":{"revision":%q,"digest":%q}}`, values["MULTICA_DSH_WORKSPACE_ID"], values["MULTICA_DSH_AGENT_ID"], values["MULTICA_DSH_HOST_GENERATION"], digest, profile.Revision, profileDigest), nil
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
	if _, err = pool.Exec(context.Background(), `INSERT INTO workspace VALUES($1)`, workspace); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(context.Background(), `INSERT INTO agent(id,workspace_id) VALUES ($1,$2)`, agent, workspace); err != nil {
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
	launcher := &FCE2BLauncher{nativeAuthority: newDSHNativeAuthorityBridge("test-secret"), Pool: pool, Runner: dshHomeRunner{profiles: &sync.Map{}}, Config: FCE2BConfig{ServerURL: "https://production-relay.test", DSHNativeAuthority: "https://pre.multica.test", Domain: "fc.test", APIURL: httpServer.URL, APIKey: "test-key", LLMModels: []string{"fixture-model"}, SandboxReadyTimeout: time.Second}, dshProvider: func(dshhost.Storage) (dshhost.Provider, error) { return p, nil }}
	launcher.ReadDSHProfileSource = func(_ context.Context, _ *db.Queries, _ dshhost.Key, template string) (dshprofile.Source, error) {
		return dshprofile.Source{TemplateID: template}, nil
	}
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
	trace := chattrace.New("dsh_native_entry")
	if task.ID.Valid {
		trace, err = chattrace.ForTask(task.Context, uuid.UUID(task.ID.Bytes).String(), time.Now())
	}
	if err != nil {
		return dshhost.Host{}, err
	}
	h, _, err := l.resolveEmployeeFilesystemSandbox(ctx, dshhost.Key{WorkspaceID: uuid.UUID(rt.WorkspaceID.Bytes), AgentID: uuid.UUID(task.AgentID.Bytes)}, task.ID, rt, template, conn, trace)
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
	l.Runner = dshHomeRunner{wrongNativeReceipt: true, profiles: &sync.Map{}}
	if _, err := resolveDSHTest(t, l, rt, task, "template-1"); !errors.Is(err, errDSHHostWaiting) {
		t.Fatal("native Host mismatch did not stop admissions", err)
	}
	l.Runner = dshHomeRunner{profiles: &sync.Map{}}
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
	l.Runner = dshHomeRunner{wrongReceipt: true, profiles: &sync.Map{}}
	if _, err := resolveDSHTest(t, l, rt, task, "template-1"); err == nil {
		t.Fatal("accepted another employee's Home")
	}
	if provider.creates != 1 {
		t.Fatal("retried ambiguous create")
	}
	l.Runner = dshHomeRunner{profiles: &sync.Map{}}
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

// Runs only against an explicitly configured remote test database. Native
// startup excludes no task, and an active UI grant must protect its generation.
func TestDSHNativeStartupSharesWriterAndDrainsNativeGrants(t *testing.T) {
	pool, _ := dshLaunchPools(t)
	provider := &dshLaunchProvider{}
	l, rt, task := dshLaunchFixture(t, pool, provider)
	native := task
	native.ID = pgtype.UUID{}
	first, err := resolveDSHTest(t, l, rt, native, "template-1")
	if err != nil {
		t.Fatal(err)
	}
	same, err := resolveDSHTest(t, l, rt, task, "template-1")
	if err != nil || same.SandboxID != first.SandboxID || provider.creates != 1 {
		t.Fatal("platform and native did not share one writer", err)
	}
	ctx := context.Background()
	if _, err = pool.Exec(ctx, `INSERT INTO agent_task_queue VALUES($1,$2,'running')`, task.ID, task.AgentID); err != nil {
		t.Fatal(err)
	}
	if _, err = resolveDSHTest(t, l, rt, native, "template-2"); !errors.Is(err, errDSHHostWaiting) {
		t.Fatal("native startup ignored active task", err)
	}
	if _, err = pool.Exec(ctx, `UPDATE agent_task_queue SET status='completed'`); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO dsh_native_access(id,workspace_id,agent_id,user_id,generation,sandbox_id,kind,token_hash,expires_at) VALUES($1,$2,$3,$4,$5,$6,'session',$7,now()+interval '1 minute')`, uuid.New(), rt.WorkspaceID, task.AgentID, uuid.New(), first.Generation, first.SandboxID, strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err = resolveDSHTest(t, l, rt, task, "template-2"); !errors.Is(err, errDSHHostWaiting) {
		t.Fatal("template replacement ignored live native session", err)
	}
	if provider.creates != 1 {
		t.Fatal("replaced an admitted writer")
	}
	if _, err = pool.Exec(ctx, `UPDATE dsh_native_access SET kind='revoked'`); err != nil {
		t.Fatal(err)
	}
	next, err := resolveDSHTest(t, l, rt, native, "template-2")
	if err != nil || next.Generation != first.Generation+1 {
		t.Fatal("drained native host did not recover", err)
	}
}

func TestDSHNativeStartupRejectsMissingIdentityAndDisabledDeployment(t *testing.T) {
	ctx := context.Background()
	key := dshhost.Key{WorkspaceID: uuid.New(), AgentID: uuid.New()}
	for _, launcher := range []*FCE2BLauncher{nil, {}, {Config: FCE2BConfig{Enabled: true}}} {
		if _, err := launcher.EnsureDSHEmployeeHost(ctx, key); err == nil {
			t.Fatal("unconfigured native startup accepted")
		}
	}
}

func TestDSHProfileFailureDoesNotStrandExistingLifecycle(t *testing.T) {
	for _, initial := range []string{"creating", "retiring"} {
		t.Run(initial, func(t *testing.T) {
			a, _ := dshLaunchPools(t)
			provider := &dshLaunchProvider{failCreate: initial == "creating"}
			l, rt, task := dshLaunchFixture(t, a, provider)
			if initial == "retiring" {
				l.Runner = dshHomeRunner{wrongNativeReceipt: true, profiles: &sync.Map{}}
			}
			if _, err := resolveDSHTest(t, l, rt, task, "template-1"); !errors.Is(err, errDSHHostWaiting) {
				t.Fatal(err)
			}
			l.ReadDSHProfileSource = func(context.Context, *db.Queries, dshhost.Key, string) (dshprofile.Source, error) {
				return dshprofile.Source{}, errors.New("invalid saved plugin settings")
			}
			if _, err := resolveDSHTest(t, l, rt, task, "template-1"); !errors.Is(err, errDSHHostWaiting) {
				t.Fatal(err)
			}
			key := dshhost.Key{WorkspaceID: uuid.UUID(rt.WorkspaceID.Bytes), AgentID: uuid.UUID(task.AgentID.Bytes)}
			host, err := (dshhost.PostgresStore{DB: a}).Get(context.Background(), key)
			if err != nil {
				t.Fatal(err)
			}
			if provider.creates != 1 {
				t.Fatal("invalid Profile admitted a replacement")
			}
			if initial == "creating" && (host.State != "running" || host.SandboxID != "sbx-1") {
				t.Fatal("unknown create not reconciled")
			}
			if initial == "retiring" && (host.State == "retiring" || len(provider.live) != 0) {
				t.Fatal("retirement stranded by invalid Profile")
			}
		})
	}
}

func TestPrepareDSHTaskFilesystem(t *testing.T) {
	key := dshhost.Key{WorkspaceID: uuid.New(), AgentID: uuid.New()}
	for _, tc := range []struct {
		name         string
		provisionErr error
		wantWaiting  bool
	}{
		{"ready", nil, false},
		{"unknown create", dshhost.ErrPending, true},
		{"competing preparation", dshhost.ErrChanged, true},
		{"preparation timeout", context.DeadlineExceeded, true},
		{"unavailable credentials", errors.New("unavailable credentials"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			l := &FCE2BLauncher{ProvisionDSHStorage: func(ctx context.Context, db dshhost.Database, got dshhost.Key) (dshhost.Host, error) {
				calls++
				if got != key {
					t.Fatal("employee identity changed")
				}
				deadline, ok := ctx.Deadline()
				if !ok || time.Until(deadline) > 45*time.Second {
					t.Fatal("provisioning is not bounded")
				}
				return dshhost.Host{}, tc.provisionErr
			}}
			err := l.prepareDSHTaskFilesystem(context.Background(), nil, key)
			if calls != 1 {
				t.Fatalf("cloud provisioning replayed: %d", calls)
			}
			if errors.Is(err, errDSHHostWaiting) != tc.wantWaiting {
				t.Fatalf("waiting=%v error=%v", tc.wantWaiting, err)
			}
			if tc.provisionErr == nil && err != nil {
				t.Fatal(err)
			}
			if tc.provisionErr != nil && !tc.wantWaiting && !errors.Is(err, tc.provisionErr) {
				t.Fatalf("provisioning failure lost: %v", err)
			}
		})
	}
	if err := (&FCE2BLauncher{}).prepareDSHTaskFilesystem(context.Background(), nil, key); err == nil {
		t.Fatal("missing provisioning configuration must not allow ephemeral fallback")
	}
}
