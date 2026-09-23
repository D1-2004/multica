package dshhost

import (
	"context"
	"errors"
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
}

func (p mountedProvider) Create(context.Context, Host) (string, error) {
	return "", errors.New("unexpected create")
}
func (p mountedProvider) Healthy(context.Context, string) error { return nil }
func (p mountedProvider) DestroyAndConfirmAbsent(context.Context, string) error {
	return errors.New("unexpected destroy")
}
func (p mountedProvider) FindCreated(context.Context, Host) (string, error) {
	return "", errors.New("unexpected find")
}
func (p mountedProvider) InspectMounts(context.Context, string) ([]VolumeMountSpec, error) {
	return p.mounts, p.inspectErr
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
	}{
		{"private only", mountedProvider{mounts: []VolumeMountSpec{private}}, false},
		{"stale shared write mount", mountedProvider{mounts: []VolumeMountSpec{private, shared}}, true},
		{"unknown mounts", mountedProvider{inspectErr: errors.New("DSH FC returned HTTP 500")}, true},
		{"mounts missing from detail", mountedProvider{}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := Manager{Store: runningHostStore{h: running}, Provider: tc.provider}
			got, err := m.EnsurePrivate(context.Background(), key, "tpl")
			if tc.wantRebuild {
				if !errors.Is(err, ErrRetireRequired) {
					t.Fatalf("fallback reused a sandbox that may keep a revoked shared mount: %+v %v", got, err)
				}
				return
			}
			if err != nil || got.SandboxID != "sbx-1" {
				t.Fatalf("private-only sandbox must be reused, got %+v %v", got, err)
			}
			// The ordinary path keeps today's reuse semantics.
			if _, err := (Manager{Store: runningHostStore{h: running}, Provider: mountedProvider{mounts: []VolumeMountSpec{private, shared}}}).Ensure(context.Background(), key, "tpl"); err != nil {
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
