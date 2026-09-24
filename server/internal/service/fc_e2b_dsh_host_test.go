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
	"github.com/multica-ai/multica/server/internal/wsfs"
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
	for _, stem := range []string{"9223_dsh_employee_host", "9224_dsh_employee_host_identity", "9225_dsh_employee_host_volume", "9226_dsh_employee_host_access_point", "9227_dsh_employee_host_space", "9235_dsh_native_access", "9265_dsh_native_access_parent", "9241_dsh_employee_profile", "9242_dsh_employee_profile_identity", "9243_dsh_profile_revision_identity", "9244_dsh_plugin_build_identity", "9261_dsh_profile_apply_queue", "9257_employee_filesystem_sandbox", "9258_employee_filesystem_sandbox_scope", "9259_employee_filesystem_host", "9304_dsh_employee_host_shared_observation", "9305_dsh_employee_host_observation_identity", "9306_employee_filesystem_sandbox_shared_observation"} {
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
	destroys   int
	destroyErr error
	failCreate bool
	healthErr  error
	live       map[uuid.UUID]string
	mountsFor  map[string][]dshhost.VolumeMountSpec
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
func (p *dshLaunchProvider) Healthy(context.Context, string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.healthErr
}

func (p *dshLaunchProvider) SandboxAbsent(_ context.Context, id string) (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, live := range p.live {
		if live == id {
			return false, nil
		}
	}
	return true, nil
}
func (p *dshLaunchProvider) DestroyAndConfirmAbsent(_ context.Context, id string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.destroyErr != nil {
		return p.destroyErr
	}
	p.destroys++
	for k, v := range p.live {
		if v == id {
			delete(p.live, k)
		}
	}
	return nil
}
func (p *dshLaunchProvider) InspectSandbox(_ context.Context, id string) (dshhost.SandboxDetail, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return dshhost.SandboxDetail{State: "running", Mounts: append([]dshhost.VolumeMountSpec(nil), p.mountsFor[id]...)}, nil
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
	probe := &fakeCommandRunner{out: []string{out, out, out, out, "{}", out}, errs: []error{nil, nil, nil, nil, nil, errors.New("supervisor unavailable")}}
	readOnlyLauncher := &FCE2BLauncher{Runner: probe}
	if !readOnlyLauncher.dshNativeHostHasProfile(context.Background(), host, digest, revision) {
		t.Fatal("live matching Profile was not reusable")
	}
	otherHost := host
	otherHost.Generation++
	otherRevision := revision
	otherRevision.ID++
	if readOnlyLauncher.dshNativeHostHasProfile(context.Background(), otherHost, digest, revision) ||
		readOnlyLauncher.dshNativeHostHasProfile(context.Background(), host, digest, otherRevision) ||
		readOnlyLauncher.dshNativeHostHasProfile(context.Background(), host, "another-catalog", revision) ||
		readOnlyLauncher.dshNativeHostHasProfile(context.Background(), host, digest, revision) ||
		readOnlyLauncher.dshNativeHostHasProfile(context.Background(), host, digest, revision) {
		t.Fatal("unconfirmed or mismatched live Profile was reusable")
	}
	for i, call := range probe.calls {
		if call.args[len(call.args)-1] != dshNativeHostHealthCommand || !probe.deadlines[i] || probe.timeouts[i] > 10*time.Second {
			t.Fatal("live Profile probe did not use the bounded read-only control request")
		}
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
	var sandbox string
	for i := range len(args) - 1 {
		values[args[i]] = args[i+1]
		if args[i] == "--" && i > 0 {
			sandbox = args[i-1]
		}
	}
	for _, arg := range args {
		if k, v, ok := strings.Cut(arg, "="); ok {
			values[k] = v
		}
	}
	if args[len(args)-1] == dshNativeHostHealthCommand {
		if receipt, ok := r.profiles.Load("health/" + sandbox); ok {
			return receipt.(string), nil
		}
		return "", errors.New("no running native Host")
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
		receipt := fmt.Sprintf(`{"version":1,"workspace_id":%q,"agent_id":%q,"generation":%s,"managed_profile_digest":%q,"employee_profile":{"revision":%q,"digest":%q}}`, values["MULTICA_DSH_WORKSPACE_ID"], values["MULTICA_DSH_AGENT_ID"], values["MULTICA_DSH_HOST_GENERATION"], digest, profile.Revision, profileDigest)
		r.profiles.Store("health/"+sandbox, receipt)
		return receipt, nil
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

type dshNoStagingRunner struct{ dshHomeRunner }

func (r dshNoStagingRunner) Run(ctx context.Context, name string, args, env []string) (string, error) {
	if args[len(args)-1] == "--stage" {
		return "", errors.New("live native Host must not be restaged")
	}
	return r.dshHomeRunner.Run(ctx, name, args, env)
}

func TestDSHNativeReopenAfterAnotherSandboxAcknowledgesSameProfile(t *testing.T) {
	pool, _ := dshLaunchPools(t)
	provider := &dshLaunchProvider{}
	l, rt, task := dshLaunchFixture(t, pool, provider)
	rt.Provider = "dsh"
	first, err := resolveDSHTest(t, l, rt, task, "template-1")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err = pool.Exec(ctx, `UPDATE dsh_employee_profile SET applied_sandbox_id='sbx-another-session',applied_generation=9 WHERE workspace_id=$1 AND agent_id=$2`, rt.WorkspaceID, task.AgentID); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO dsh_native_access(id,workspace_id,agent_id,user_id,generation,sandbox_id,kind,token_hash,expires_at) VALUES($1,$2,$3,$4,$5,$6,'session',$7,now()+interval '1 minute')`, uuid.New(), rt.WorkspaceID, task.AgentID, uuid.New(), first.Generation, first.SandboxID, strings.Repeat("c", 64)); err != nil {
		t.Fatal(err)
	}
	l.Runner = dshNoStagingRunner{l.Runner.(dshHomeRunner)}
	reopened, err := resolveDSHTest(t, l, rt, task, "template-1")
	if err != nil || reopened.SandboxID != first.SandboxID || reopened.Generation != first.Generation || provider.creates != 1 {
		t.Fatal("same live Profile could not reopen without restaging or replacing the Host", reopened, err)
	}
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
		_ = json.NewEncoder(w).Encode(map[string]any{"sandboxID": id, "state": "running", "endAt": time.Now().Add(time.Duration(dshhost.DefaultSandboxTaskTimeoutSeconds) * time.Second)})
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

type readyFailRunner struct{ dshHomeRunner }

func (r readyFailRunner) Run(ctx context.Context, name string, args, env []string) (string, error) {
	if len(args) > 0 && args[len(args)-1] == "true" {
		return "", errors.New("sandbox exec not ready")
	}
	return r.dshHomeRunner.Run(ctx, name, args, env)
}

func TestAdoptedCreateDoesNotHandOverAWritableMountAfterReadDowngrade(t *testing.T) {
	pool, _ := dshLaunchPools(t)
	provider := &dshLaunchProvider{failCreate: true}
	l, rt, task := dshLaunchFixture(t, pool, provider)
	if _, err := resolveDSHTest(t, l, rt, task, "template-1"); err == nil {
		t.Fatal("lost create returned a host")
	}
	provider.failCreate = false
	provider.mu.Lock()
	provider.mountsFor = map[string][]dshhost.VolumeMountSpec{
		"sbx-1": {
			{Name: "vol-rw", Path: dshhost.WorkspaceSharedRoot},
		},
	}
	provider.mu.Unlock()
	l.ReadWorkspaceMount = func(context.Context, wsfs.Database, uuid.UUID, uuid.UUID, *dshhost.Host) (wsfs.MountDecision, error) {
		return wsfs.MountDecision{
			Shared:   &dshhost.VolumeMountSpec{Name: "vol-ro", Path: dshhost.WorkspaceSharedRoot},
			RoleARN:  "role-read",
			Access:   wsfs.AccessRead,
			RWVolume: "vol-rw",
			ROVolume: "vol-ro",
		}, nil
	}
	rt.Metadata = []byte(`{"capabilities":["workspace_shared_disk"]}`)
	host, err := resolveDSHTest(t, l, rt, task, "template-1")
	if err != nil || host.SandboxID == "" || host.SandboxID == "sbx-1" || provider.creates != 2 || provider.destroys != 1 {
		t.Fatalf("adopted RW host was handed to a read grant: host=%+v creates=%d destroys=%d err=%v", host, provider.creates, provider.destroys, err)
	}
	for _, mount := range host.ExtraMounts {
		if mount.Name == "vol-rw" {
			t.Fatalf("replacement kept the writable volume: %+v", host)
		}
	}
}

func TestWriteExpansionKeepsHostWithNativeGrant(t *testing.T) {
	pool, _ := dshLaunchPools(t)
	provider := &dshLaunchProvider{}
	l, rt, task := dshLaunchFixture(t, pool, provider)
	first, err := resolveDSHTest(t, l, rt, task, "template-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(context.Background(), `INSERT INTO dsh_native_access(id,workspace_id,agent_id,user_id,generation,sandbox_id,kind,token_hash,expires_at) VALUES($1,$2,$3,$4,$5,$6,'session',$7,now()+interval '5 minute')`, uuid.New(), rt.WorkspaceID, task.AgentID, uuid.New(), first.Generation, first.SandboxID, strings.Repeat("d", 64)); err != nil {
		t.Fatal(err)
	}
	provider.mu.Lock()
	provider.mountsFor = map[string][]dshhost.VolumeMountSpec{
		first.SandboxID: {
			{Name: first.VolumeName, Path: dshhost.MountPath},
			{Name: "vol-ro", Path: dshhost.WorkspaceSharedRoot},
		},
	}
	provider.mu.Unlock()
	l.ReadWorkspaceMount = func(context.Context, wsfs.Database, uuid.UUID, uuid.UUID, *dshhost.Host) (wsfs.MountDecision, error) {
		return wsfs.MountDecision{
			Shared:   &dshhost.VolumeMountSpec{Name: "vol-rw", Path: dshhost.WorkspaceSharedRoot},
			RoleARN:  "role-write",
			Access:   wsfs.AccessWrite,
			ROVolume: "vol-ro",
			RWVolume: "vol-rw",
		}, nil
	}
	rt.Metadata = []byte(`{"capabilities":["workspace_shared_disk"]}`)
	got, err := resolveDSHTest(t, l, rt, task, "template-1")
	if err != nil || got.SandboxID != first.SandboxID || got.Generation != first.Generation || got.AuthRoleARN == "role-write" || provider.creates != 1 || provider.destroys != 0 {
		t.Fatalf("write expansion retired a host that has a native session: host=%+v err=%v creates=%d destroys=%d", got, err, provider.creates, provider.destroys)
	}
}

func TestReadDowngradeWithUnreadyRoleDoesNotKeepWritableMount(t *testing.T) {
	pool, _ := dshLaunchPools(t)
	ctx := context.Background()
	for _, stem := range []string{"9273_workspace_filesystem", "9280_workspace_filesystem_grant"} {
		data, err := os.ReadFile(filepath.Join("..", "..", "migrations", stem+".up.sql"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err = pool.Exec(ctx, string(data)); err != nil {
			t.Fatal(stem, err)
		}
	}
	provider := &dshLaunchProvider{}
	l, rt, task := dshLaunchFixture(t, pool, provider)
	first, err := resolveDSHTest(t, l, rt, task, "template-1")
	if err != nil {
		t.Fatal(err)
	}
	wsID, agID := uuid.UUID(rt.WorkspaceID.Bytes), uuid.UUID(task.AgentID.Bytes)
	if _, err = pool.Exec(ctx, `INSERT INTO workspace_filesystem (
 workspace_id, file_system_id, space_id, vpc_id, security_group_id, vswitch_ids,
 ro_access_point_arn, rw_access_point_arn, ro_role_arn, rw_role_arn, ro_volume_name, rw_volume_name,
 size_limit, file_count_limit) VALUES ($1,'fs','space','vpc','sg',ARRAY['vsw'],'ap-ro','ap-rw','role-ro','role-rw','vol-ro','vol-rw',1,1)`, wsID); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO workspace_filesystem_grant (workspace_id, agent_id, access, generation, task_role_arn)
 VALUES ($1,$2,'read',2,'')`, wsID, agID); err != nil {
		t.Fatal(err)
	}
	employee := &dshhost.Host{Storage: dshhost.Storage{VolumeName: first.VolumeName, AccessPointARN: "ap", RoleARN: "role-employee"}}
	decision, readErr := (wsfs.Controller{}).ReadMount(ctx, pool, wsID, agID, employee)
	if !errors.Is(readErr, wsfs.ErrSharedDiskNotReady) || decision.Access != wsfs.AccessRead || decision.Shared != nil || decision.RWVolume != "vol-rw" || decision.ROVolume != "vol-ro" {
		t.Fatalf("read constraint dropped while the composite role is unprepared: %+v %v", decision, readErr)
	}
	provider.mu.Lock()
	provider.mountsFor = map[string][]dshhost.VolumeMountSpec{
		first.SandboxID: {
			{Name: first.VolumeName, Path: dshhost.MountPath},
			{Name: "vol-rw", Path: dshhost.WorkspaceSharedRoot},
		},
	}
	provider.mu.Unlock()
	l.ReadWorkspaceMount = func(ctx context.Context, db wsfs.Database, workspaceID, agentID uuid.UUID, before *dshhost.Host) (wsfs.MountDecision, error) {
		return (wsfs.Controller{}).ReadMount(ctx, db, workspaceID, agentID, before)
	}
	rt.Metadata = []byte(`{"capabilities":["workspace_shared_disk"]}`)
	second, err := resolveDSHTest(t, l, rt, task, "template-1")
	if err == nil && second.SandboxID == first.SandboxID {
		t.Fatalf("read downgrade with an unprepared role kept the writable mount: %+v creates=%d destroys=%d", second, provider.creates, provider.destroys)
	}
	if err != nil || second.SandboxID == "" || second.SandboxID == first.SandboxID || provider.destroys != 1 {
		t.Fatalf("read downgrade did not drain the writable mount: host=%+v creates=%d destroys=%d err=%v", second, provider.creates, provider.destroys, err)
	}
	for _, mount := range second.ExtraMounts {
		if mount.Name == "vol-rw" {
			t.Fatalf("replacement kept the writable volume: %+v", second)
		}
	}
}

func TestUnreadyReadGrantKeepsHostWithoutSharedMount(t *testing.T) {
	pool, _ := dshLaunchPools(t)
	ctx := context.Background()
	for _, stem := range []string{"9273_workspace_filesystem", "9280_workspace_filesystem_grant"} {
		data, err := os.ReadFile(filepath.Join("..", "..", "migrations", stem+".up.sql"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err = pool.Exec(ctx, string(data)); err != nil {
			t.Fatal(stem, err)
		}
	}
	provider := &dshLaunchProvider{}
	l, rt, task := dshLaunchFixture(t, pool, provider)
	first, err := resolveDSHTest(t, l, rt, task, "template-1")
	if err != nil {
		t.Fatal(err)
	}
	wsID, agID := uuid.UUID(rt.WorkspaceID.Bytes), uuid.UUID(task.AgentID.Bytes)
	if _, err = pool.Exec(ctx, `INSERT INTO workspace_filesystem (
 workspace_id, file_system_id, space_id, vpc_id, security_group_id, vswitch_ids,
 ro_access_point_arn, rw_access_point_arn, ro_role_arn, rw_role_arn, ro_volume_name, rw_volume_name,
 size_limit, file_count_limit) VALUES ($1,'fs','space','vpc','sg',ARRAY['vsw'],'ap-ro','ap-rw','role-ro','role-rw','vol-ro','vol-rw',1,1)`, wsID); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO workspace_filesystem_grant (workspace_id, agent_id, access, generation, task_role_arn)
 VALUES ($1,$2,'read',2,'')`, wsID, agID); err != nil {
		t.Fatal(err)
	}
	provider.mu.Lock()
	provider.mountsFor = map[string][]dshhost.VolumeMountSpec{
		first.SandboxID: {{Name: first.VolumeName, Path: dshhost.MountPath}},
	}
	provider.mu.Unlock()
	l.ReadWorkspaceMount = func(ctx context.Context, db wsfs.Database, workspaceID, agentID uuid.UUID, before *dshhost.Host) (wsfs.MountDecision, error) {
		return (wsfs.Controller{}).ReadMount(ctx, db, workspaceID, agentID, before)
	}
	rt.Metadata = []byte(`{"kind":"fc-e2b"}`)
	got, err := resolveDSHTest(t, l, rt, task, "template-1")
	if err != nil || got.SandboxID != first.SandboxID || got.Generation != first.Generation || len(got.ExtraMounts) != 0 || provider.creates != 1 || provider.destroys != 0 {
		t.Fatalf("unready read grant rebuilt a host that has no shared mount: %+v err=%v creates=%d destroys=%d", got, err, provider.creates, provider.destroys)
	}
}

func TestFreshSharedMountDeclaresTheMountedAccess(t *testing.T) {
	for _, tc := range []struct {
		access string
		volume string
		role   string
	}{
		{access: wsfs.AccessWrite, volume: "vol-rw", role: "role-write"},
		{access: wsfs.AccessRead, volume: "vol-ro", role: "role-read"},
	} {
		t.Run(tc.access, func(t *testing.T) {
			pool, _ := dshLaunchPools(t)
			provider := &dshLaunchProvider{}
			l, rt, task := dshLaunchFixture(t, pool, provider)
			l.ReadWorkspaceMount = func(context.Context, wsfs.Database, uuid.UUID, uuid.UUID, *dshhost.Host) (wsfs.MountDecision, error) {
				return wsfs.MountDecision{
					Shared:  &dshhost.VolumeMountSpec{Name: tc.volume, Path: dshhost.WorkspaceSharedRoot},
					RoleARN: tc.role,
					Access:  tc.access,
				}, nil
			}
			rt.Metadata = []byte(`{"capabilities":["workspace_shared_disk"]}`)
			host, err := resolveDSHTest(t, l, rt, task, "template-1")
			if err != nil || host.SandboxID == "" || provider.creates != 1 || len(host.ExtraMounts) != 1 || host.ExtraMounts[0].Name != tc.volume {
				t.Fatalf("fresh %s mount was not created: %+v creates=%d err=%v", tc.access, host, provider.creates, err)
			}
			if host.SharedAccess != tc.access || effectiveWorkspaceFSAccess(&host) != tc.access {
				t.Fatalf("fresh %s mount declared %q", tc.access, effectiveWorkspaceFSAccess(&host))
			}
		})
	}
}

func TestPrivateLaunchDoesNotInjectSharedDiskEnv(t *testing.T) {
	pool, _ := dshLaunchPools(t)
	provider := &dshLaunchProvider{}
	l, rt, task := dshLaunchFixture(t, pool, provider)
	host, err := resolveDSHTest(t, l, rt, task, "template-1")
	if err != nil || host.SandboxID == "" || len(host.ExtraMounts) != 0 || provider.creates != 1 {
		t.Fatalf("private launch changed: %+v creates=%d err=%v", host, provider.creates, err)
	}
	if access := effectiveWorkspaceFSAccess(&host); access != "" {
		t.Fatalf("private launch injected shared access %q", access)
	}
}

func TestWriteExpansionKeepsReadOnlyMountButDeclaresRead(t *testing.T) {
	pool, _ := dshLaunchPools(t)
	ctx := context.Background()
	provider := &dshLaunchProvider{}
	l, rt, task := dshLaunchFixture(t, pool, provider)
	first, err := resolveDSHTest(t, l, rt, task, "template-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS workspace_filesystem_grant (
 workspace_id uuid NOT NULL, agent_id uuid NOT NULL, access text NOT NULL, generation bigint NOT NULL DEFAULT 0,
 task_role_arn text NOT NULL DEFAULT '', task_policy_name text NOT NULL DEFAULT '', updated_at timestamptz NOT NULL DEFAULT now(), updated_by uuid)`); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO workspace_filesystem_grant (workspace_id, agent_id, access, generation, task_role_arn)
 VALUES ($1,$2,'write',3,'role-write')`, rt.WorkspaceID, task.AgentID); err != nil {
		t.Fatal(err)
	}
	provider.mu.Lock()
	provider.mountsFor = map[string][]dshhost.VolumeMountSpec{
		first.SandboxID: {
			{Name: first.VolumeName, Path: dshhost.MountPath},
			{Name: "vol-ro", Path: dshhost.WorkspaceSharedRoot},
		},
	}
	provider.mu.Unlock()
	l.ReadWorkspaceMount = func(context.Context, wsfs.Database, uuid.UUID, uuid.UUID, *dshhost.Host) (wsfs.MountDecision, error) {
		return wsfs.MountDecision{
			Shared:   &dshhost.VolumeMountSpec{Name: "vol-rw", Path: dshhost.WorkspaceSharedRoot},
			RoleARN:  "role-write",
			Access:   wsfs.AccessWrite,
			ROVolume: "vol-ro",
			RWVolume: "vol-rw",
		}, nil
	}
	rt.Metadata = []byte(`{"capabilities":["workspace_shared_disk"]}`)
	got, err := resolveDSHTest(t, l, rt, task, "template-1")
	if err != nil || got.SandboxID != first.SandboxID || got.Generation != first.Generation || provider.creates != 1 || provider.destroys != 0 {
		t.Fatalf("write expansion rebuilt a healthy read-only host: %+v err=%v creates=%d destroys=%d", got, err, provider.creates, provider.destroys)
	}
	if got.SharedAccess != wsfs.AccessRead || effectiveWorkspaceFSAccess(&got) != wsfs.AccessRead {
		t.Fatalf("kept RO mount but effective access is %q", got.SharedAccess)
	}
	stored, err := (wsfs.Store{DB: pool}).GetGrant(ctx, uuid.UUID(rt.WorkspaceID.Bytes), uuid.UUID(task.AgentID.Bytes))
	if err != nil || stored.Access != wsfs.AccessWrite {
		t.Fatalf("desired grant changed: %+v %v", stored, err)
	}
}

func TestReadDowngradeWithoutCapabilityDoesNotKeepWritableMount(t *testing.T) {
	pool, _ := dshLaunchPools(t)
	provider := &dshLaunchProvider{}
	l, rt, task := dshLaunchFixture(t, pool, provider)
	first, err := resolveDSHTest(t, l, rt, task, "template-1")
	if err != nil || first.SandboxID == "" {
		t.Fatal(err)
	}
	provider.mu.Lock()
	provider.mountsFor = map[string][]dshhost.VolumeMountSpec{
		first.SandboxID: {
			{Name: first.VolumeName, Path: dshhost.MountPath},
			{Name: "vol-rw", Path: dshhost.WorkspaceSharedRoot},
		},
	}
	provider.mu.Unlock()
	l.ReadWorkspaceMount = func(context.Context, wsfs.Database, uuid.UUID, uuid.UUID, *dshhost.Host) (wsfs.MountDecision, error) {
		return wsfs.MountDecision{
			Shared:  &dshhost.VolumeMountSpec{Name: "vol-ro", Path: dshhost.WorkspaceSharedRoot},
			RoleARN: "role-read",
			Access:  wsfs.AccessRead,
		}, nil
	}
	rt.Metadata = []byte(`{"kind":"fc-e2b"}`)
	second, err := resolveDSHTest(t, l, rt, task, "template-1")
	if err != nil || second.SandboxID == "" || second.SandboxID == first.SandboxID || len(second.ExtraMounts) != 0 || provider.creates != 2 || provider.destroys != 1 {
		t.Fatalf("read downgrade with missing capability keeps host without inspecting existing writable mount: first=%s second=%+v creates=%d destroys=%d err=%v", first.SandboxID, second, provider.creates, provider.destroys, err)
	}
}

type firstSandboxNotReady struct{ dshHomeRunner }

func (r firstSandboxNotReady) Run(ctx context.Context, name string, args, env []string) (string, error) {
	if len(args) >= 3 && args[0] == "sandbox" && args[1] == "exec" && args[2] == "sbx-1" && args[len(args)-1] == "true" {
		return "", errors.New("sandbox exec not ready")
	}
	return r.dshHomeRunner.Run(ctx, name, args, env)
}

func TestSharedCreateReadinessFailureDoesNotFailLaunch(t *testing.T) {
	pool, _ := dshLaunchPools(t)
	provider := &dshLaunchProvider{}
	l, rt, task := dshLaunchFixture(t, pool, provider)
	prepared := 0
	l.PrepareWorkspaceMount = func(context.Context, wsfs.Database, uuid.UUID, uuid.UUID, *dshhost.Host) (wsfs.MountDecision, error) {
		prepared++
		return wsfs.MountDecision{}, errors.New("launch must not provision")
	}
	l.ReadWorkspaceMount = func(context.Context, wsfs.Database, uuid.UUID, uuid.UUID, *dshhost.Host) (wsfs.MountDecision, error) {
		return wsfs.MountDecision{
			Shared:  &dshhost.VolumeMountSpec{Name: "vol-shared", Path: dshhost.WorkspaceSharedRoot},
			RoleARN: "role-shared",
			Access:  wsfs.AccessRead,
		}, nil
	}
	rt.Metadata = []byte(`{"capabilities":["workspace_shared_disk"]}`)
	l.Runner = firstSandboxNotReady{l.Runner.(dshHomeRunner)}
	host, err := resolveDSHTest(t, l, rt, task, "template-1")
	if err != nil || host.SandboxID != "sbx-2" || len(host.ExtraMounts) != 0 || provider.creates != 2 || provider.destroys != 1 || prepared != 0 {
		t.Fatalf("shared readiness failure did not rebuild one private sandbox: host=%+v creates=%d destroys=%d prepared=%d err=%v", host, provider.creates, provider.destroys, prepared, err)
	}

	blockedPool, _ := dshLaunchPools(t)
	blocked := &dshLaunchProvider{destroyErr: errors.New("destroy unconfirmed")}
	blockedLauncher, blockedRT, blockedTask := dshLaunchFixture(t, blockedPool, blocked)
	blockedLauncher.ReadWorkspaceMount = l.ReadWorkspaceMount
	blockedRT.Metadata = rt.Metadata
	blockedLauncher.Runner = firstSandboxNotReady{blockedLauncher.Runner.(dshHomeRunner)}
	if _, err := resolveDSHTest(t, blockedLauncher, blockedRT, blockedTask, "template-1"); err == nil || blocked.creates != 1 || blocked.destroys != 0 {
		t.Fatalf("unconfirmed destroy started a second sandbox: creates=%d destroys=%d err=%v", blocked.creates, blocked.destroys, err)
	}

	privatePool, _ := dshLaunchPools(t)
	privateProvider := &dshLaunchProvider{}
	privateLauncher, privateRT, privateTask := dshLaunchFixture(t, privatePool, privateProvider)
	privateLauncher.Runner = readyFailRunner{privateLauncher.Runner.(dshHomeRunner)}
	if _, err := resolveDSHTest(t, privateLauncher, privateRT, privateTask, "template-1"); err == nil || privateProvider.creates != 1 {
		t.Fatalf("private readiness failure was ignored: creates=%d err=%v", privateProvider.creates, err)
	}

	lostPool, _ := dshLaunchPools(t)
	lost := &dshLaunchProvider{failCreate: true}
	lostLauncher, lostRT, lostTask := dshLaunchFixture(t, lostPool, lost)
	lostLauncher.ReadWorkspaceMount = l.ReadWorkspaceMount
	lostRT.Metadata = rt.Metadata
	_, _ = resolveDSHTest(t, lostLauncher, lostRT, lostTask, "template-1")
	if _, err := resolveDSHTest(t, lostLauncher, lostRT, lostTask, "template-1"); lost.creates != 1 {
		t.Fatalf("unconfirmed shared create was retried: creates=%d err=%v", lost.creates, err)
	}
}

func TestDSHLaunchRebuildsUnhealthyHostWhenIdle(t *testing.T) {
	a, _ := dshLaunchPools(t)
	provider := &dshLaunchProvider{}
	l, rt, task := dshLaunchFixture(t, a, provider)
	first, err := resolveDSHTest(t, l, rt, task, "template-1")
	if err != nil {
		t.Fatal(err)
	}
	provider.healthErr = errors.New("sandbox gone")
	next, err := resolveDSHTest(t, l, rt, task, "template-1")
	if err != nil || next.Generation != 2 || next.SandboxID == first.SandboxID || provider.creates != 2 {
		t.Fatalf("generation=%d sandbox=%s creates=%d err=%v", next.Generation, next.SandboxID, provider.creates, err)
	}
}

func TestDSHNativeHostFailureRequiresConfirmedSandboxRetirement(t *testing.T) {
	a, _ := dshLaunchPools(t)
	provider := &dshLaunchProvider{destroyErr: errors.New("not confirmed")}
	l, rt, task := dshLaunchFixture(t, a, provider)
	rt.Provider = "dsh"
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

func TestDSHPendingProfileDoesNotRetireLiveNativePage(t *testing.T) {
	pool, _ := dshLaunchPools(t)
	provider := &dshLaunchProvider{}
	l, rt, task := dshLaunchFixture(t, pool, provider)
	rt.Provider = "dsh"
	first, err := resolveDSHTest(t, l, rt, task, "template-1")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err = pool.Exec(ctx, `UPDATE dsh_employee_profile SET applied_revision=0 WHERE workspace_id=$1 AND agent_id=$2`, rt.WorkspaceID, task.AgentID); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO dsh_native_access(id,workspace_id,agent_id,user_id,generation,sandbox_id,kind,token_hash,expires_at) VALUES($1,$2,$3,$4,$5,$6,'session',$7,now()+interval '1 minute')`, uuid.New(), rt.WorkspaceID, task.AgentID, uuid.New(), first.Generation, first.SandboxID, strings.Repeat("b", 64)); err != nil {
		t.Fatal(err)
	}
	// Calling ensure for a mismatched live Profile would return a startup
	// error. A pending apply must wait without reaching that failure path.
	l.Runner = dshHomeRunner{wrongNativeReceipt: true, profiles: &sync.Map{}}
	if _, err = resolveDSHTest(t, l, rt, task, "template-1"); !errors.Is(err, errDSHHostWaiting) || errors.Is(err, errDSHHostStartup) {
		t.Fatal("pending Profile was treated as a failed live Host", err)
	}
	host, err := (dshhost.PostgresStore{DB: pool}).Get(ctx, first.Key)
	if err != nil || host.State != "running" || host.SandboxID != first.SandboxID || provider.creates != 1 {
		t.Fatal("native page lost its running generation", host, err)
	}
}

func TestDSHProfileFailureDoesNotStrandExistingLifecycle(t *testing.T) {
	for _, initial := range []string{"creating", "retiring"} {
		t.Run(initial, func(t *testing.T) {
			a, _ := dshLaunchPools(t)
			provider := &dshLaunchProvider{failCreate: initial == "creating"}
			l, rt, task := dshLaunchFixture(t, a, provider)
			rt.Provider = "dsh"
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

// Routed read connections may be interrupted by task recovery; they must not
// reserve a writer generation or force a pending task to wait for a browser.
func TestDSHRoutedGrantDoesNotBlockTemplateRecovery(t *testing.T) {
	pool, _ := dshLaunchPools(t)
	provider := &dshLaunchProvider{}
	launcher, rt, task := dshLaunchFixture(t, pool, provider)
	first, err := resolveDSHTest(t, launcher, rt, task, "template-1")
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(context.Background(), `INSERT INTO dsh_native_access
 (id,workspace_id,agent_id,user_id,generation,sandbox_id,kind,token_hash,expires_at,parent_access_id)
 VALUES($1,$2,$3,$4,$5,$6,'session',$7,now()+interval '15 minutes',$8)`,
		uuid.New(), rt.WorkspaceID, task.AgentID, uuid.New(), first.Generation, first.SandboxID, strings.Repeat("d", 64), uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	next, err := resolveDSHTest(t, launcher, rt, task, "template-2")
	if err != nil || next.Generation != first.Generation+1 {
		t.Fatal("routed read blocked task recovery", next, err)
	}
}

func TestDSHProfileAckFailureKeepsHealthyGeneration(t *testing.T) {
	pool, _ := dshLaunchPools(t)
	provider := &dshLaunchProvider{}
	l, rt, task := dshLaunchFixture(t, pool, provider)
	rt.Provider = "dsh"
	reads := 0
	l.ReadDSHProfileSource = func(_ context.Context, _ *db.Queries, _ dshhost.Key, template string) (dshprofile.Source, error) {
		reads++
		if reads == 2 {
			return dshprofile.Source{}, errors.New("transient configuration read failure")
		}
		return dshprofile.Source{TemplateID: template}, nil
	}
	if _, err := resolveDSHTest(t, l, rt, task, "template-1"); !errors.Is(err, errDSHHostWaiting) || errors.Is(err, errDSHHostStartup) {
		t.Fatal("ACK error classified as startup failure", err)
	}
	key := dshhost.Key{WorkspaceID: uuid.UUID(rt.WorkspaceID.Bytes), AgentID: uuid.UUID(task.AgentID.Bytes)}
	host, err := (dshhost.PostgresStore{DB: pool}).Get(context.Background(), key)
	if err != nil || host.State != "running" {
		t.Fatal("healthy host retired on ACK error", host, err)
	}
	next, err := resolveDSHTest(t, l, rt, task, "template-1")
	if err != nil || next.Generation != host.Generation || provider.creates != 1 {
		t.Fatal("ACK retry replaced healthy generation", next, err)
	}
}

type dshNoSnapshotRunner struct{ dshHomeRunner }

func (r dshNoSnapshotRunner) Run(ctx context.Context, name string, args, env []string) (string, error) {
	for _, arg := range args {
		if strings.Contains(arg, "--plugin-snapshot") {
			return "", errors.New("another session snapshot is broken")
		}
	}
	return r.dshHomeRunner.Run(ctx, name, args, env)
}
func TestDSHTaskAdmissionDoesNotReadNativePluginSnapshot(t *testing.T) {
	pool, _ := dshLaunchPools(t)
	provider := &dshLaunchProvider{}
	l, rt, task := dshLaunchFixture(t, pool, provider)
	rt.Provider = "dsh"
	l.Runner = dshNoSnapshotRunner{l.Runner.(dshHomeRunner)}
	l.SyncDSHProfileSource = func(context.Context, *pgxpool.Conn, dshhost.Key, string, dshprofile.NativeSnapshot) error {
		t.Fatal("task imported live native configuration")
		return nil
	}
	for i := 0; i < 2; i++ {
		if _, err := resolveDSHTest(t, l, rt, task, "template-1"); err != nil {
			t.Fatal(err)
		}
	}
}
