package dshhost

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

type runningHostStore struct{ h Host }

func (s runningHostStore) Get(context.Context, Key) (Host, error) { return s.h, nil }
func (s runningHostStore) BeginCreate(context.Context, Key, int64, uuid.UUID, string) (Host, error) {
	return Host{}, errors.New("unexpected create")
}
func (s runningHostStore) CompleteCreate(context.Context, Host, string) (Host, error) {
	return Host{}, errors.New("unexpected create")
}
func (s runningHostStore) BeginRetire(context.Context, Host) (Host, error) {
	return Host{}, errors.New("unexpected retire")
}
func (s runningHostStore) CompleteRetire(context.Context, Host) error {
	return errors.New("unexpected retire")
}
func (s runningHostStore) AbortRetire(context.Context, Host) error {
	return errors.New("unexpected retire")
}
func (s runningHostStore) AbandonCreate(context.Context, Host) error {
	return errors.New("unexpected abandon")
}

type mountedProvider struct {
	mounts     []VolumeMountSpec
	inspectErr error
	state      string
}

func (p mountedProvider) Create(context.Context, Host) (string, error) {
	return "", errors.New("unexpected create")
}
func (p mountedProvider) Healthy(context.Context, string) error {
	return errors.New("private-only reuse must prove health from the detail read")
}
func (p mountedProvider) DestroyAndConfirmAbsent(context.Context, string) error {
	return errors.New("unexpected destroy")
}
func (p mountedProvider) FindCreated(context.Context, Host) (string, error) {
	return "", errors.New("unexpected find")
}
func (p mountedProvider) InspectSandbox(context.Context, string) (SandboxDetail, error) {
	state := p.state
	if state == "" {
		state = "running"
	}
	return SandboxDetail{State: state, Mounts: p.mounts}, p.inspectErr
}

func TestEnsurePrivateRebuildsSandboxesThatMayCarryASharedMount(t *testing.T) {
	key := Key{WorkspaceID: uuid.New(), AgentID: uuid.New()}
	running := Host{Key: key, Storage: Storage{VolumeName: "vol-employee"}, State: "running", Generation: 1, SandboxID: "sbx-1", TemplateID: "tpl"}
	private := VolumeMountSpec{Name: "vol-employee", Path: MountPath}
	shared := VolumeMountSpec{Name: "vol-shared-rw", Path: WorkspaceSharedRoot}
	for _, tc := range []struct {
		name        string
		provider    mountedProvider
		wantRebuild bool
		wantWait    bool
	}{
		{"private only", mountedProvider{mounts: []VolumeMountSpec{private}}, false, false},
		{"stale shared write mount", mountedProvider{mounts: []VolumeMountSpec{private, shared}}, true, false},
		{"unknown mounts", mountedProvider{inspectErr: errors.New("DSH FC returned HTTP 500")}, false, true},
		{"mounts missing from detail", mountedProvider{}, true, false},
		{"sandbox not running", mountedProvider{mounts: []VolumeMountSpec{private}, state: "paused"}, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := Manager{Store: runningHostStore{h: running}, Provider: tc.provider}
			got, err := m.EnsurePrivate(context.Background(), key, "tpl")
			if tc.wantWait {
				if !errors.Is(err, ErrPending) || errors.Is(err, ErrRetireRequired) || got.SandboxID == running.SandboxID {
					t.Fatalf("unconfirmed revoke destroyed or reused the host: %+v %v", got, err)
				}
				return
			}
			if tc.wantRebuild {
				if !errors.Is(err, ErrRetireRequired) {
					t.Fatalf("fallback reused a sandbox that may keep a revoked shared mount: %+v %v", got, err)
				}
				return
			}
			if err != nil || got.SandboxID != "sbx-1" {
				t.Fatalf("private-only sandbox must be reused, got %+v %v", got, err)
			}
			// The ordinary path keeps today's reuse semantics (health via Healthy).
			if _, err := (Manager{Store: runningHostStore{h: running}, Provider: healthyProvider{mountedProvider{mounts: []VolumeMountSpec{private, shared}}}}).Ensure(context.Background(), key, "tpl"); err != nil {
				t.Fatalf("Ensure changed behavior: %v", err)
			}
		})
	}
}

func TestRequirePrivateChecksAnAdoptedSandbox(t *testing.T) {
	adopted := Host{Storage: Storage{VolumeName: "vol-employee"}, SandboxID: "sbx-adopted"}
	private := VolumeMountSpec{Name: "vol-employee", Path: MountPath}
	shared := VolumeMountSpec{Name: "vol-shared-ro", Path: WorkspaceSharedRoot}
	if err := (Manager{Provider: mountedProvider{mounts: []VolumeMountSpec{private}}}).RequirePrivate(context.Background(), adopted); err != nil {
		t.Fatalf("private-only adopted sandbox must be kept: %v", err)
	}
	if err := (Manager{Provider: mountedProvider{mounts: []VolumeMountSpec{private, shared}}}).RequirePrivate(context.Background(), adopted); !errors.Is(err, ErrRetireRequired) {
		t.Fatalf("adopted dual-mount sandbox must be retired under the fallback, got %v", err)
	}
}

type offlineHostStore struct {
	h         Host
	abandoned *bool
}

func (s offlineHostStore) Get(context.Context, Key) (Host, error) { return s.h, nil }
func (s offlineHostStore) BeginCreate(_ context.Context, _ Key, generation int64, intent uuid.UUID, template string) (Host, error) {
	h := s.h
	h.State, h.Generation, h.CreateIntent, h.TemplateID = "creating", generation+1, intent, template
	return h, nil
}
func (s offlineHostStore) CompleteCreate(context.Context, Host, string) (Host, error) {
	return Host{}, errors.New("unexpected completion")
}
func (s offlineHostStore) BeginRetire(context.Context, Host) (Host, error) {
	return Host{}, errors.New("unexpected retire")
}
func (s offlineHostStore) CompleteRetire(context.Context, Host) error {
	return errors.New("unexpected retire")
}
func (s offlineHostStore) AbortRetire(context.Context, Host) error {
	return errors.New("unexpected retire")
}
func (s offlineHostStore) AbandonCreate(_ context.Context, h Host) error {
	if h.State != "creating" || h.CreateIntent == uuid.Nil {
		return errors.New("abandoned a non-creating host")
	}
	*s.abandoned = true
	return nil
}

type rejectingProvider struct{ err error }

func (p rejectingProvider) Create(context.Context, Host) (string, error) { return "", p.err }
func (p rejectingProvider) Healthy(context.Context, string) error        { return nil }
func (p rejectingProvider) DestroyAndConfirmAbsent(context.Context, string) error {
	return errors.New("unexpected destroy")
}
func (p rejectingProvider) FindCreated(context.Context, Host) (string, error) {
	return "", errors.New("unexpected find")
}

func TestEnsureReleasesIntentsFCRejected(t *testing.T) {
	key := Key{WorkspaceID: uuid.New(), AgentID: uuid.New()}
	offline := Host{Key: key, State: "offline"}
	for _, tc := range []struct {
		name          string
		err           error
		wantAbandoned bool
		wantRejected  bool
	}{
		{"bad request", &FCStatusError{Status: 400}, true, true},
		{"forbidden", &FCStatusError{Status: 403}, true, true},
		{"throttled", &FCStatusError{Status: 429}, true, false},
		{"template not found", &FCStatusError{Status: 404}, true, true},
		{"request timeout", &FCStatusError{Status: 408}, false, false},
		{"conflict", &FCStatusError{Status: 409}, false, false},
		{"locked", &FCStatusError{Status: 423}, false, false},
		{"server error", &FCStatusError{Status: 502}, false, false},
		{"transport", errors.New("DSH FC transport outcome unconfirmed"), false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			abandoned := false
			m := Manager{Store: offlineHostStore{h: offline, abandoned: &abandoned}, Provider: rejectingProvider{err: tc.err}}
			_, err := m.Ensure(context.Background(), key, "tpl")
			if abandoned != tc.wantAbandoned {
				t.Fatalf("abandoned=%v, want %v (err %v)", abandoned, tc.wantAbandoned, err)
			}
			if errors.Is(err, ErrCreateRejected) != tc.wantRejected || errors.Is(err, ErrPending) == tc.wantRejected {
				t.Fatalf("unexpected classification: %v", err)
			}
		})
	}
}

type healthyProvider struct{ mountedProvider }

func (healthyProvider) Healthy(context.Context, string) error { return nil }

func TestPrivateOnlyUnhealthyKeepsTheWaitReason(t *testing.T) {
	key := Key{WorkspaceID: uuid.New(), AgentID: uuid.New()}
	running := Host{Key: key, Storage: Storage{VolumeName: "vol-employee"}, State: "running", Generation: 1, SandboxID: "sbx-1", TemplateID: "tpl"}
	m := Manager{Store: runningHostStore{h: running}, Provider: mountedProvider{inspectErr: &FCStatusError{Status: 404}}}
	_, err := m.EnsurePrivate(context.Background(), key, "tpl")
	if !errors.Is(err, ErrRetireRequired) || !strings.Contains(err.Error(), WaitSandboxUnhealthy) {
		t.Fatalf("an unreadable sandbox must surface sandbox_unhealthy, got %v", err)
	}
}

func TestCreateRejectedMessageIsClassifiedAsSandboxCreate(t *testing.T) {
	if !strings.Contains(strings.ToLower(ErrCreateRejected.Error()), "sandbox create") {
		t.Fatalf("the launch failure classifier matches \"sandbox create\"; got %q", ErrCreateRejected)
	}
}
