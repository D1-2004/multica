package dshhost

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// These tests use separate real PostgreSQL pools to exercise replica races.
// They create isolated schemas and never require the application's tables.
func stores(t *testing.T) (PostgresStore, PostgresStore) {
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
	schema := "dsh_host_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Error(err)
		}
		if err := admin.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	newStore := func() PostgresStore {
		cfg, err := pgxpool.ParseConfig(url)
		if err != nil {
			t.Fatal(err)
		}
		cfg.ConnConfig.RuntimeParams["search_path"] = schema
		cfg.MaxConns = 4
		pool, err := pgxpool.NewWithConfig(ctx, cfg)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(pool.Close)
		return PostgresStore{DB: pool}
	}
	a, b := newStore(), newStore()
	for _, name := range []string{"9223_dsh_employee_host", "9224_dsh_employee_host_identity", "9257_employee_filesystem_sandbox", "9258_employee_filesystem_sandbox_scope", "9259_employee_filesystem_host", "9225_dsh_employee_host_volume", "9226_dsh_employee_host_access_point", "9227_dsh_employee_host_space", "9228_dsh_session_binding", "9229_dsh_session_scope", "9230_dsh_session_identity", "9231_dsh_task_identity", "9232_dsh_request_identity", "9233_dsh_storage_provision", "9234_dsh_storage_provision_identity", "9235_dsh_native_access", "9265_dsh_native_access_parent", "9236_dsh_native_access_hash", "9237_dsh_native_access_id", "9238_dsh_browser_session_identity", "9260_dsh_session_sandbox_scope", "9262_dsh_session_workdir", "9268_dsh_session_epoch", "9269_dsh_session_epoch_identity", "9270_dsh_session_epoch_scope", "9304_dsh_employee_host_shared_observation", "9305_dsh_employee_host_observation_identity", "9306_employee_filesystem_sandbox_shared_observation"} {
		data, err := os.ReadFile(filepath.Join("..", "..", "migrations", name+".up.sql"))
		if err != nil {
			t.Fatal(err)
		}
		// Execute twice to verify migration replay, outside transactions so
		// CREATE INDEX CONCURRENTLY exercises its actual deployment contract.
		for range 2 {
			if _, err := a.DB.Exec(ctx, string(data)); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
		}
	}
	return a, b
}

func bind(t *testing.T, s PostgresStore) Host {
	t.Helper()
	h, err := s.BindStorage(context.Background(), Key{uuid.New(), uuid.New()}, Storage{
		FileSystemID: "fs-test", SpaceID: "space-" + uuid.NewString(), VPCID: "vpc-test", SecurityGroupID: "sg-test", VSwitchIDs: []string{"vsw-test"},
		VolumeName: "volume-" + uuid.NewString(), AccessPointARN: "ap-" + uuid.NewString(), RoleARN: "role-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

type cloud struct {
	mu                                        sync.Mutex
	creates, destroys                         int
	live                                      map[uuid.UUID]string
	createErr, healthErr, destroyErr, findErr error
	cancelOnCreate                            context.CancelFunc
	hidden                                    bool
}

func (p *cloud) Create(_ context.Context, h Host) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.creates++
	if p.live == nil {
		p.live = map[uuid.UUID]string{}
	}
	id := fmt.Sprintf("sandbox-%d", p.creates)
	p.live[h.CreateIntent] = id
	if p.cancelOnCreate != nil {
		p.cancelOnCreate()
	}
	return id, p.createErr
}
func (p *cloud) Healthy(context.Context, string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.healthErr
}
func (p *cloud) DestroyAndConfirmAbsent(_ context.Context, id string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.destroys++
	if p.destroyErr != nil {
		return p.destroyErr
	}
	for intent, live := range p.live {
		if live == id {
			delete(p.live, intent)
		}
	}
	return nil
}
func (p *cloud) FindCreated(_ context.Context, h Host) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.findErr != nil {
		return "", p.findErr
	}
	if p.hidden {
		return "", nil
	}
	return p.live[h.CreateIntent], nil
}

func (p *cloud) SandboxAbsent(_ context.Context, id string) (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, live := range p.live {
		if live == id {
			return false, nil
		}
	}
	return true, nil
}

type mountedCloud struct {
	cloud
	mounts []VolumeMountSpec
}

func (p *mountedCloud) InspectSandbox(context.Context, string) (SandboxDetail, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return SandboxDetail{State: "running", Mounts: append([]VolumeMountSpec(nil), p.mounts...)}, nil
}

func TestReadOnlyGrantDoesNotKeepWritableMount(t *testing.T) {
	a, _ := stores(t)
	h := bind(t, a)
	p := &mountedCloud{}
	m := Manager{a, p}
	ctx := context.Background()
	first, err := m.Ensure(ctx, h.Key, "template-1")
	if err != nil {
		t.Fatal(err)
	}
	p.mounts = []VolumeMountSpec{
		{Name: h.VolumeName, Path: MountPath},
		{Name: "vol-rw", Path: WorkspaceSharedRoot},
	}
	readOnly := VolumeMountSpec{Name: "vol-ro", Path: WorkspaceSharedRoot}
	got, err := m.EnsureWithShared(ctx, h.Key, "template-1", readOnly, "role-read")
	if !errors.Is(err, ErrRetireRequired) || got.AuthRoleARN == "role-read" || p.creates != 1 || p.destroys != 0 {
		t.Fatalf("writable mount survived a read-only grant: host=%+v err=%v creates=%d destroys=%d", got, err, p.creates, p.destroys)
	}
	if got.SandboxID == first.SandboxID && got.State == "running" && len(got.ExtraMounts) > 0 {
		t.Fatalf("returned the running host with the writable mount still attached: %+v", got)
	}
}

type inspectFailCloud struct{ cloud }

func (p *inspectFailCloud) InspectSandbox(context.Context, string) (SandboxDetail, error) {
	return SandboxDetail{}, errors.New("detail unavailable")
}

func TestInspectionFailureDoesNotReuseAfterReadOnlyGrant(t *testing.T) {
	a, _ := stores(t)
	h := bind(t, a)
	p := &inspectFailCloud{}
	m := Manager{a, p}
	ctx := context.Background()
	first, err := m.Ensure(ctx, h.Key, "template-1")
	if err != nil {
		t.Fatal(err)
	}
	readOnly := VolumeMountSpec{Name: "vol-ro", Path: WorkspaceSharedRoot}
	got, err := m.EnsureWithShared(ctx, h.Key, "template-1", readOnly, "role-read")
	if !errors.Is(err, ErrPending) || errors.Is(err, ErrRetireRequired) || got.SandboxID == first.SandboxID || p.creates != 1 || p.destroys != 0 {
		t.Fatalf("unconfirmed existing mount reused after read-only grant and inspection failure: host=%+v err=%v creates=%d destroys=%d", got, err, p.creates, p.destroys)
	}
}

func TestWriteGrantKeepsReadOnlyHost(t *testing.T) {
	a, _ := stores(t)
	h := bind(t, a)
	p := &mountedCloud{}
	m := Manager{a, p}
	ctx := context.Background()
	first, err := m.Ensure(ctx, h.Key, "template-1")
	if err != nil {
		t.Fatal(err)
	}
	p.mounts = []VolumeMountSpec{
		{Name: h.VolumeName, Path: MountPath},
		{Name: "vol-ro", Path: WorkspaceSharedRoot},
	}
	got, err := m.EnsureWithSharedGrant(ctx, h.Key, "template-1", SharedTarget{Access: "write", Volume: "vol-rw", OtherVolume: "vol-ro", RoleARN: "role-write"})
	if err != nil || got.SandboxID != first.SandboxID || got.State != "running" || got.AuthRoleARN == "role-write" || p.creates != 1 || p.destroys != 0 {
		t.Fatalf("read-only host was retired to gain write: host=%+v err=%v creates=%d destroys=%d", got, err, p.creates, p.destroys)
	}
}

func TestInspectFailureReusesRecordedMountWhenGrantIsNotTightened(t *testing.T) {
	a, _ := stores(t)
	h := bind(t, a)
	p := &mountedCloud{}
	m := Manager{a, p}
	ctx := context.Background()
	first, err := m.Ensure(ctx, h.Key, "template-1")
	if err != nil {
		t.Fatal(err)
	}
	p.mounts = []VolumeMountSpec{
		{Name: h.VolumeName, Path: MountPath},
		{Name: "vol-ro", Path: WorkspaceSharedRoot},
	}
	if _, err = m.EnsureWithSharedGrant(ctx, h.Key, "template-1", SharedTarget{Access: "read", Volume: "vol-ro", RoleARN: "role-read"}); err != nil {
		t.Fatal(err)
	}
	bad := &inspectFailCloud{}
	got, err := (Manager{Store: a, Provider: bad}).EnsureWithSharedGrant(ctx, h.Key, "template-1", SharedTarget{Access: "read", Volume: "vol-ro", RoleARN: "role-read"})
	if err != nil || got.SandboxID != first.SandboxID || bad.creates != 0 || bad.destroys != 0 {
		t.Fatalf("recorded read mount blocked a healthy host: host=%+v err=%v creates=%d destroys=%d", got, err, bad.creates, bad.destroys)
	}
	revoked, err := (Manager{Store: a, Provider: bad}).EnsurePrivate(ctx, h.Key, "template-1")
	if !errors.Is(err, ErrPending) || errors.Is(err, ErrRetireRequired) || revoked.SandboxID == first.SandboxID || bad.destroys != 0 {
		t.Fatalf("tightened grant reused a host after a failed mount check: host=%+v err=%v destroys=%d", revoked, err, bad.destroys)
	}
}

type goneCloud struct{ cloud }

func (p *goneCloud) InspectSandbox(context.Context, string) (SandboxDetail, error) {
	return SandboxDetail{}, &FCStatusError{Status: 404}
}

func TestExpiredSandbox404RebuildsEvenWithObservation(t *testing.T) {
	a, _ := stores(t)
	h := bind(t, a)
	p := &mountedCloud{}
	m := Manager{a, p}
	ctx := context.Background()
	first, err := m.Ensure(ctx, h.Key, "template-1")
	if err != nil {
		t.Fatal(err)
	}
	readOnly := SharedTarget{Access: "read", Volume: "vol-ro", RoleARN: "role-read"}
	p.mounts = []VolumeMountSpec{{Name: h.VolumeName, Path: MountPath}, {Name: "vol-ro", Path: WorkspaceSharedRoot}}
	if _, err = m.EnsureWithSharedGrant(ctx, h.Key, "template-1", readOnly); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"no observation", "recorded observation"} {
		provider := &goneCloud{}
		store := a
		if name == "no observation" {
			bare := bind(t, a)
			if _, err = (Manager{a, &cloud{}}).Ensure(ctx, bare.Key, "template-1"); err != nil {
				t.Fatal(err)
			}
			got, err := (Manager{a, provider}).EnsureWithSharedGrant(ctx, bare.Key, "template-1", readOnly)
			if !errors.Is(err, ErrRetireRequired) || !strings.Contains(err.Error(), WaitSandboxUnhealthy) || got.SandboxID != "" || provider.creates != 0 || provider.destroys != 0 {
				t.Fatalf("%s: FC 404 did not enter rebuild: host=%+v err=%v", name, got, err)
			}
			continue
		}
		got, err := (Manager{store, provider}).EnsureWithSharedGrant(ctx, h.Key, "template-1", readOnly)
		if !errors.Is(err, ErrRetireRequired) || !strings.Contains(err.Error(), WaitSandboxUnhealthy) || got.SandboxID == first.SandboxID || provider.creates != 0 || provider.destroys != 0 {
			t.Fatalf("%s: FC 404 did not enter rebuild: host=%+v err=%v", name, got, err)
		}
	}
}

func TestFilesystemScopeRetainsSharedObservation(t *testing.T) {
	a, _ := stores(t)
	employee := bind(t, a)
	ctx := context.Background()
	scope := FilesystemSandboxStore{DB: a.DB, ScopeID: uuid.New()}
	if _, err := scope.Bind(ctx, employee.Key); err != nil {
		t.Fatal(err)
	}
	p := &mountedCloud{}
	m := Manager{Store: scope, Provider: p}
	first, err := m.Ensure(ctx, employee.Key, "template-1")
	if err != nil {
		t.Fatal(err)
	}
	p.mounts = []VolumeMountSpec{{Name: employee.VolumeName, Path: MountPath}, {Name: "vol-ro", Path: WorkspaceSharedRoot}}
	readOnly := SharedTarget{Access: "read", Volume: "vol-ro", RoleARN: "role-read"}
	if _, err = m.EnsureWithSharedGrant(ctx, employee.Key, "template-1", readOnly); err != nil {
		t.Fatal(err)
	}
	got, err := (Manager{Store: scope, Provider: &inspectFailCloud{}}).EnsureWithSharedGrant(ctx, employee.Key, "template-1", readOnly)
	if err != nil || got.SandboxID != first.SandboxID || !got.ObservedSharedKnown {
		t.Fatalf("scoped observation was lost: known=%v returned=%s err=%v", got.ObservedSharedKnown, got.SandboxID, err)
	}
	other := FilesystemSandboxStore{DB: a.DB, ScopeID: uuid.New()}
	if _, err = other.Bind(ctx, employee.Key); err != nil {
		t.Fatal(err)
	}
	otherHost, err := (Manager{Store: other, Provider: &cloud{}}).Ensure(ctx, employee.Key, "template-1")
	if err != nil {
		t.Fatal(err)
	}
	blocked, err := (Manager{Store: other, Provider: &inspectFailCloud{}}).EnsureWithSharedGrant(ctx, employee.Key, "template-1", readOnly)
	if !errors.Is(err, ErrPending) || blocked.SandboxID == otherHost.SandboxID {
		t.Fatalf("another scope reused this scope's observation: host=%+v err=%v", blocked, err)
	}
}

func TestStaleObservationDoesNotAuthorizeAnotherGeneration(t *testing.T) {
	a, _ := stores(t)
	h := bind(t, a)
	p := &mountedCloud{}
	m := Manager{a, p}
	ctx := context.Background()
	first, err := m.Ensure(ctx, h.Key, "template-1")
	if err != nil {
		t.Fatal(err)
	}
	p.mounts = []VolumeMountSpec{{Name: h.VolumeName, Path: MountPath}}
	if _, err = m.EnsurePrivate(ctx, h.Key, "template-1"); err != nil {
		t.Fatal(err)
	}
	if _, err = a.DB.Exec(ctx, `UPDATE dsh_employee_host
 SET generation=generation+1, sandbox_id='legacy-new-rw'
 WHERE workspace_id=$1 AND agent_id=$2 AND sandbox_id=$3`, h.WorkspaceID, h.AgentID, first.SandboxID); err != nil {
		t.Fatal(err)
	}
	got, err := (Manager{a, &inspectFailCloud{}}).EnsurePrivate(ctx, h.Key, "template-1")
	if err == nil || got.SandboxID == "legacy-new-rw" {
		t.Fatalf("old observation authorized a different generation after revoke: returned=%s err=%v", got.SandboxID, err)
	}
	reloaded, err := a.Get(ctx, h.Key)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Generation == first.Generation || observationMatches(reloaded) {
		t.Fatalf("stale observation still matches generation=%d sandbox=%s observed=%s/%d", reloaded.Generation, reloaded.SandboxID, reloaded.ObservedSandboxID, reloaded.ObservedHostGeneration)
	}
}

func TestEnsureWithSharedKeepsHealthySandboxWithoutTheMount(t *testing.T) {
	a, _ := stores(t)
	h := bind(t, a)
	p := &cloud{}
	m := Manager{a, p}
	ctx := context.Background()
	first, err := m.Ensure(ctx, h.Key, "template-1")
	if err != nil {
		t.Fatal(err)
	}
	shared := VolumeMountSpec{Name: "vol-shared", Path: WorkspaceSharedRoot}
	got, err := m.EnsureWithShared(ctx, h.Key, "template-1", shared, "role-composite")
	if err != nil || got.SandboxID != first.SandboxID || got.State != "running" || p.creates != 1 || p.destroys != 0 {
		t.Fatalf("healthy sandbox was rebuilt for a shared mount: %+v err=%v creates=%d destroys=%d", got, err, p.creates, p.destroys)
	}
}

func TestEnsureDuringRetireDoesNotCreateAnotherSandbox(t *testing.T) {
	a, b := stores(t)
	h := bind(t, a)
	p := &cloud{}
	ctx := context.Background()
	first, err := (Manager{a, p}).Ensure(ctx, h.Key, "template-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.BeginRetire(ctx, first); err != nil {
		t.Fatal(err)
	}
	if _, err := (Manager{b, p}).Ensure(ctx, h.Key, "template-1"); !errors.Is(err, ErrPending) {
		t.Fatal(err)
	}
	if p.creates != 1 || p.destroys != 0 {
		t.Fatalf("a launch during retire destroyed or replaced the sandbox: creates=%d destroys=%d", p.creates, p.destroys)
	}
}

func TestReplicasCreateOneWriter(t *testing.T) {
	a, b := stores(t)
	h := bind(t, a)
	p := &cloud{}
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range 24 {
		s := a
		if i%2 == 1 {
			s = b
		}
		wg.Go(func() {
			<-start
			_, err := (Manager{s, p}).Ensure(context.Background(), h.Key, "template-1")
			if err != nil && !errors.Is(err, ErrChanged) && !errors.Is(err, ErrPending) {
				t.Error(err)
			}
		})
	}
	close(start)
	wg.Wait()
	if p.creates != 1 || len(p.live) != 1 {
		t.Fatalf("creates=%d live=%d", p.creates, len(p.live))
	}
	got, err := b.Get(context.Background(), h.Key)
	if err != nil || got.State != "running" || got.Generation != 1 {
		t.Fatalf("host=%+v err=%v", got, err)
	}
}

func TestUnknownCreateCannotRetryAndReconcilesAcrossReplica(t *testing.T) {
	a, b := stores(t)
	h := bind(t, a)
	p := &cloud{createErr: errors.New("response lost"), hidden: true}
	m := Manager{a, p}
	if _, err := m.Ensure(context.Background(), h.Key, "template-1"); !errors.Is(err, ErrPending) {
		t.Fatal(err)
	}
	n := Manager{b, p}
	if _, err := n.Ensure(context.Background(), h.Key, "template-1"); !errors.Is(err, ErrPending) {
		t.Fatal(err)
	}
	if _, err := n.ReconcileCreate(context.Background(), h.Key); !errors.Is(err, ErrPending) {
		t.Fatal(err)
	}
	if p.creates != 1 {
		t.Fatalf("retried ambiguous create: %d", p.creates)
	}
	p.hidden = false
	got, err := n.ReconcileCreate(context.Background(), h.Key)
	if err != nil || got.SandboxID != "sandbox-1" {
		t.Fatalf("host=%+v err=%v", got, err)
	}
}

func TestStaleEmptyCreateIntentStaysPending(t *testing.T) {
	a, _ := stores(t)
	h := bind(t, a)
	p := &cloud{createErr: errors.New("response lost"), hidden: true}
	m := Manager{a, p}
	if _, err := m.Ensure(context.Background(), h.Key, "template-1"); !errors.Is(err, ErrPending) {
		t.Fatal(err)
	}
	if p.creates != 1 {
		t.Fatalf("creates=%d", p.creates)
	}
	if _, err := a.DB.Exec(context.Background(), "UPDATE dsh_employee_host SET updated_at=now()-interval '10 minutes'"); err != nil {
		t.Fatal(err)
	}
	p.createErr = nil
	if _, err := m.Ensure(context.Background(), h.Key, "template-1"); !errors.Is(err, ErrPending) {
		t.Fatal(err)
	}
	if _, err := m.ReconcileCreate(context.Background(), h.Key); !errors.Is(err, ErrPending) {
		t.Fatal(err)
	}
	if p.creates != 1 {
		t.Fatalf("empty listing spawned a second sandbox: creates=%d", p.creates)
	}
	got, err := a.Get(context.Background(), h.Key)
	if err != nil || got.State != "creating" || got.Generation != 1 {
		t.Fatalf("host=%+v err=%v", got, err)
	}
}

func TestTransportErrorDoesNotAbandonStaleCreate(t *testing.T) {
	a, _ := stores(t)
	h := bind(t, a)
	p := &cloud{createErr: errors.New("response lost"), findErr: errors.New("listing unavailable")}
	m := Manager{a, p}
	if _, err := m.Ensure(context.Background(), h.Key, "template-1"); !errors.Is(err, ErrPending) {
		t.Fatal(err)
	}
	if _, err := a.DB.Exec(context.Background(), "UPDATE dsh_employee_host SET updated_at=now()-interval '10 minutes'"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Ensure(context.Background(), h.Key, "template-1"); !errors.Is(err, ErrPending) {
		t.Fatal(err)
	}
	if p.creates != 1 {
		t.Fatalf("abandoned create after transport error: creates=%d", p.creates)
	}
}

type deadlineCloud struct {
	cloud
}

func (p *deadlineCloud) DestroyAndConfirmAbsent(ctx context.Context, id string) error {
	p.mu.Lock()
	p.live = map[uuid.UUID]string{}
	p.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	return context.DeadlineExceeded
}

func (p *deadlineCloud) SandboxAbsent(ctx context.Context, id string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return p.cloud.SandboxAbsent(ctx, id)
}

func TestCancelledDestroyCompletesRetireWhenSandboxGone(t *testing.T) {
	a, _ := stores(t)
	h := bind(t, a)
	p := &deadlineCloud{}
	m := Manager{a, p}
	ctx := context.Background()
	first, err := m.Ensure(ctx, h.Key, "template-1")
	if err != nil {
		t.Fatal(err)
	}
	retiring, err := a.BeginRetire(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	cancelCtx, cancel := context.WithCancel(ctx)
	cancel()
	if err := m.finishRetire(cancelCtx, retiring); err != nil {
		t.Fatal(err)
	}
	got, err := a.Get(ctx, h.Key)
	if err != nil || got.State != "offline" || got.Generation != first.Generation {
		t.Fatalf("host=%+v err=%v", got, err)
	}
	next, err := m.Ensure(ctx, h.Key, "template-2")
	if err != nil || next.Generation != 2 || p.creates != 2 {
		t.Fatalf("host=%+v err=%v creates=%d", next, err, p.creates)
	}
}

func TestRetireBusyQueryErrorDoesNotDestroyOnRetry(t *testing.T) {
	a, _ := stores(t)
	h := bind(t, a)
	p := &cloud{}
	m := Manager{a, p}
	ctx := context.Background()
	first, err := m.Ensure(ctx, h.Key, "template-1")
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	busy := func(Host) (bool, error) {
		calls++
		if calls == 1 {
			return false, errors.New("grant query failed")
		}
		return true, nil
	}
	if err := m.RetireUnlessBusy(ctx, first.Key, first.Generation, busy); err == nil || err.Error() != "grant query failed" {
		t.Fatal(err)
	}
	got, err := a.Get(ctx, first.Key)
	if err != nil || got.State != "running" || p.destroys != 0 {
		t.Fatalf("query error left host retiring: %+v destroys=%d err=%v", got, p.destroys, err)
	}
	if err := m.RetireUnlessBusy(ctx, first.Key, first.Generation, busy); !errors.Is(err, ErrPending) || p.destroys != 0 {
		t.Fatalf("retry skipped the hold: err=%v destroys=%d", err, p.destroys)
	}
	got, err = a.Get(ctx, first.Key)
	if err != nil || got.State != "running" || got.SandboxID != first.SandboxID {
		t.Fatalf("retry destroyed host: %+v err=%v", got, err)
	}
}

func TestAlreadyRetiringHostRechecksHold(t *testing.T) {
	a, _ := stores(t)
	h := bind(t, a)
	p := &cloud{}
	m := Manager{a, p}
	ctx := context.Background()
	first, err := m.Ensure(ctx, h.Key, "template-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.BeginRetire(ctx, first); err != nil {
		t.Fatal(err)
	}
	err = m.RetireUnlessBusy(ctx, first.Key, first.Generation, func(Host) (bool, error) { return true, nil })
	if !errors.Is(err, ErrPending) || p.destroys != 0 {
		t.Fatalf("already-retiring host skipped the hold: err=%v destroys=%d", err, p.destroys)
	}
	got, err := a.Get(ctx, first.Key)
	if err != nil || got.State != "retiring" || got.SandboxID != first.SandboxID {
		t.Fatalf("hold restored a retire that may already be deleting: %+v err=%v", got, err)
	}
}

func TestRetireAbortsWhenHoldAppearsAfterBegin(t *testing.T) {
	a, _ := stores(t)
	h := bind(t, a)
	p := &cloud{}
	m := Manager{a, p}
	ctx := context.Background()
	first, err := m.Ensure(ctx, h.Key, "template-1")
	if err != nil {
		t.Fatal(err)
	}
	err = m.RetireUnlessBusy(ctx, first.Key, first.Generation, func(Host) (bool, error) { return true, nil })
	if !errors.Is(err, ErrPending) {
		t.Fatal(err)
	}
	got, err := a.Get(ctx, first.Key)
	if err != nil || got.State != "running" || got.SandboxID != first.SandboxID || p.destroys != 0 {
		t.Fatalf("aborted retire destroyed host: %+v destroys=%d err=%v", got, p.destroys, err)
	}
}

func TestFailedHealthAndDestroyNeverAuthorizeReplacement(t *testing.T) {
	a, b := stores(t)
	h := bind(t, a)
	p := &cloud{}
	m := Manager{a, p}
	ctx := context.Background()
	first, err := m.Ensure(ctx, h.Key, "template-1")
	if err != nil {
		t.Fatal(err)
	}
	p.healthErr = errors.New("network unavailable")
	if _, err := m.Ensure(ctx, h.Key, "template-1"); !errors.Is(err, ErrRetireRequired) {
		t.Fatal(err)
	}
	if _, err := m.Ensure(ctx, h.Key, "template-2"); !errors.Is(err, ErrRetireRequired) {
		t.Fatal(err)
	}
	p.destroyErr = errors.New("accepted but not confirmed")
	if err := m.Retire(ctx, h.Key, first.Generation); !errors.Is(err, ErrPending) {
		t.Fatal(err)
	}
	if _, err := (Manager{b, p}).Ensure(ctx, h.Key, "template-2"); !errors.Is(err, ErrPending) {
		t.Fatal(err)
	}
	if p.creates != 1 || len(p.live) != 1 {
		t.Fatal("created a competing writer")
	}
	p.destroyErr = nil
	p.healthErr = nil
	if err := (Manager{b, p}).Retire(ctx, h.Key, first.Generation); err != nil {
		t.Fatal(err)
	}
	next, err := m.Ensure(ctx, h.Key, "template-2")
	if err != nil || next.Generation != 2 || len(p.live) != 1 {
		t.Fatalf("host=%+v err=%v live=%d", next, err, len(p.live))
	}
	if err := m.Retire(ctx, h.Key, first.Generation); !errors.Is(err, ErrChanged) {
		t.Fatal("stale generation retired new host", err)
	}
	if err := a.CompleteRetire(ctx, first); !errors.Is(err, ErrChanged) {
		t.Fatal("stale receipt cleared new host", err)
	}
}

func TestCancelledRequestStillRecordsKnownSandbox(t *testing.T) {
	a, _ := stores(t)
	h := bind(t, a)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := &cloud{cancelOnCreate: cancel}
	got, err := (Manager{a, p}).Ensure(ctx, h.Key, "template-1")
	if err != nil || got.State != "running" {
		t.Fatalf("host=%+v err=%v", got, err)
	}
}

func TestEmployeeStorageCannotAliasOrBeRedirected(t *testing.T) {
	a, b := stores(t)
	h := bind(t, a)
	ctx := context.Background()
	if _, err := b.BindStorage(ctx, h.Key, h.Storage); err != nil {
		t.Fatal(err)
	}
	changed := h.Storage
	changed.VolumeName = "different"
	if _, err := b.BindStorage(ctx, h.Key, changed); !errors.Is(err, ErrChanged) {
		t.Fatal(err)
	}
	other := Key{h.WorkspaceID, uuid.New()}
	if _, err := b.BindStorage(ctx, other, h.Storage); err == nil {
		t.Fatal("two employees share a volume")
	}
	if _, err := b.BindStorage(ctx, other, changed); err == nil {
		t.Fatal("two volume names alias the same access point")
	}
	changed.AccessPointARN = "different-ap"
	if _, err := b.BindStorage(ctx, other, changed); err == nil {
		t.Fatal("different access points alias the same employee Space")
	}
	changed.SpaceID = "different-space"
	if _, err := b.BindStorage(ctx, other, changed); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Get(ctx, Key{uuid.New(), h.AgentID}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("cross-workspace host lookup succeeded", err)
	}
}
