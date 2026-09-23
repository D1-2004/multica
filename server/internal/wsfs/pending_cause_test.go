package wsfs

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/dshhost"
)

type deniedRAMAPI struct{}

func (deniedRAMAPI) Call(_ context.Context, call dshhost.CloudCall, _ any) error {
	return errors.New("DSH cloud " + call.Action + " returned HTTP 403 (NoPermission)")
}

func TestCompositeRolePendingNamesTheDeniedCall(t *testing.T) {
	c := CloudStorageProvider{API: deniedRAMAPI{}, Spec: testSpec()}
	_, err := c.ensureRAMRole(context.Background(), "wsfst-x", "desc", "acs:ram::123:role/wsfst-x")
	if !errors.Is(err, dshhost.ErrPending) {
		t.Fatalf("denied create must stay pending, got %v", err)
	}
	for _, want := range []string{"create: DSH cloud CreateRole returned HTTP 403 (NoPermission)", "lookup: DSH cloud GetRole returned HTTP 403 (NoPermission)"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("pending error %q does not name %q", err, want)
		}
	}
}

type stuckProvisionStore struct{ p WorkspaceProvision }

func (s stuckProvisionStore) BeginProvision(context.Context, uuid.UUID, dshhost.ProvisionSpec) (WorkspaceProvision, error) {
	return s.p, nil
}
func (s stuckProvisionStore) ClaimProvisionStep(context.Context, WorkspaceProvision) (WorkspaceProvision, error) {
	return s.p, errors.New("unexpected claim")
}
func (s stuckProvisionStore) CompleteProvisionStep(context.Context, WorkspaceProvision, string) (WorkspaceProvision, error) {
	return s.p, errors.New("unexpected completion")
}
func (s stuckProvisionStore) FinishProvision(context.Context, WorkspaceProvision, Binding) (Binding, error) {
	return Binding{}, errors.New("unexpected finish")
}

type deniedLookupProvider struct{}

func (deniedLookupProvider) PrepareStorageResource(context.Context, WorkspaceProvision) (func(context.Context) (string, error), error) {
	return nil, errors.New("unexpected prepare")
}
func (deniedLookupProvider) FindStorageResource(context.Context, WorkspaceProvision) (string, error) {
	return "", errors.New("DSH cloud GetRole returned HTTP 403 (NoPermission)")
}
func (deniedLookupProvider) VerifyStorage(context.Context, WorkspaceProvision) (Binding, error) {
	return Binding{}, errors.New("unexpected verify")
}

func TestProvisionPendingNamesTheStepAndCause(t *testing.T) {
	workspaceID := uuid.New()
	p := WorkspaceProvision{WorkspaceID: workspaceID, Spec: testSpec(), Intent: uuid.New(), Step: ProvisionRoleRO, State: "creating"}
	_, err := (Provisioner{Store: stuckProvisionStore{p: p}, Provider: deniedLookupProvider{}}).Ensure(context.Background(), workspaceID, testSpec())
	if !errors.Is(err, dshhost.ErrPending) {
		t.Fatalf("lost lookup must stay pending, got %v", err)
	}
	for _, want := range []string{"role_ro", "GetRole returned HTTP 403 (NoPermission)"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("pending error %q does not name %q", err, want)
		}
	}
}

type recordingProvisionStore struct {
	p         WorkspaceProvision
	completed *string
}

func (s recordingProvisionStore) BeginProvision(context.Context, uuid.UUID, dshhost.ProvisionSpec) (WorkspaceProvision, error) {
	return s.p, nil
}
func (s recordingProvisionStore) ClaimProvisionStep(context.Context, WorkspaceProvision) (WorkspaceProvision, error) {
	return s.p, errors.New("unexpected claim")
}
func (s recordingProvisionStore) CompleteProvisionStep(_ context.Context, _ WorkspaceProvision, id string) (WorkspaceProvision, error) {
	*s.completed = id
	return s.p, errors.New("stop after the recovered step")
}
func (s recordingProvisionStore) FinishProvision(context.Context, WorkspaceProvision, Binding) (Binding, error) {
	return Binding{}, errors.New("unexpected finish")
}

type rejectedOnceProvider struct {
	creates *int
}

func (p rejectedOnceProvider) PrepareStorageResource(context.Context, WorkspaceProvision) (func(context.Context) (string, error), error) {
	return func(context.Context) (string, error) {
		*p.creates++
		return "acs:ram::123:role/multica-wsfs-x-ro", nil
	}, nil
}
func (p rejectedOnceProvider) FindStorageResource(context.Context, WorkspaceProvision) (string, error) {
	return "", errors.New("DSH cloud GetRole returned HTTP 404 (EntityNotExist.Role)")
}
func (p rejectedOnceProvider) VerifyStorage(context.Context, WorkspaceProvision) (Binding, error) {
	return Binding{}, errors.New("unexpected verify")
}

func TestProvisionRetriesARejectedNamedRAMCreate(t *testing.T) {
	workspaceID := uuid.New()
	for _, tc := range []struct {
		step        int
		wantRetried bool
	}{
		{ProvisionRoleRO, true},
		{ProvisionPolicyRW, true},
		{ProvisionAttachRO, true},
		{ProvisionAccessPointRO, false},
		{ProvisionVolumeRW, false},
	} {
		t.Run(provisionStepName(tc.step), func(t *testing.T) {
			creates, completed := 0, ""
			p := WorkspaceProvision{WorkspaceID: workspaceID, Spec: testSpec(), Intent: uuid.New(), Step: tc.step, State: "creating"}
			m := Provisioner{Store: recordingProvisionStore{p: p, completed: &completed}, Provider: rejectedOnceProvider{creates: &creates}}
			_, err := m.Ensure(context.Background(), workspaceID, testSpec())
			if !tc.wantRetried {
				if creates != 0 || completed != "" || !errors.Is(err, dshhost.ErrPending) {
					t.Fatalf("non-RAM step must stay lookup-only: creates=%d completed=%q err=%v", creates, completed, err)
				}
				return
			}
			if creates != 1 || completed != "acs:ram::123:role/multica-wsfs-x-ro" {
				t.Fatalf("rejected RAM create was not retried: creates=%d completed=%q err=%v", creates, completed, err)
			}
		})
	}
}
