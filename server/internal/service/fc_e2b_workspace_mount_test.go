package service

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/dshhost"
	"github.com/multica-ai/multica/server/internal/wsfs"
)

func TestClassifySharedLaunchSeparatesRevokeFromNotReady(t *testing.T) {
	revoked := wsfs.MountDecision{Revoked: true}
	if classifySharedLaunch(false, revoked, true) != sharedLaunchRevoke {
		t.Fatal("an explicit revoke must drop the shared mount even when the image is old or the binding is unready")
	}
	ready := wsfs.MountDecision{Shared: &dshhost.VolumeMountSpec{Name: "vol-shared", Path: dshhost.WorkspaceSharedRoot}, RoleARN: "role-composite"}
	if classifySharedLaunch(false, ready, false) != sharedLaunchKeep {
		t.Fatal("an image that does not declare shared disk must keep the historical launch")
	}
	readOnly := wsfs.MountDecision{Shared: &dshhost.VolumeMountSpec{Name: "vol-ro", Path: dshhost.WorkspaceSharedRoot}, RoleARN: "role-read", Access: wsfs.AccessRead}
	if classifySharedLaunch(false, readOnly, false) != sharedLaunchConstrain {
		t.Fatal("a read grant must still be compared with an existing mount when shared disk is not declared")
	}
	write := wsfs.MountDecision{Shared: &dshhost.VolumeMountSpec{Name: "vol-rw", Path: dshhost.WorkspaceSharedRoot}, RoleARN: "role-write", Access: wsfs.AccessWrite}
	if classifySharedLaunch(false, write, false) != sharedLaunchKeep {
		t.Fatal("a write grant on an image without shared disk must not offer a new mount")
	}
	if classifySharedLaunch(true, ready, true) != sharedLaunchKeep {
		t.Fatal("a shared disk that is not ready must not retire a healthy sandbox")
	}
	if classifySharedLaunch(true, ready, false) != sharedLaunchOffer {
		t.Fatal("a capable runtime with a ready binding may offer the shared mount on create")
	}
	if classifySharedLaunch(true, wsfs.MountDecision{}, false) != sharedLaunchKeep {
		t.Fatal("a missing grant is not a revoke")
	}
}

func TestWorkspaceMountDecisionNeverBlocksLaunch(t *testing.T) {
	key := dshhost.Key{WorkspaceID: uuid.New(), AgentID: uuid.New()}
	before := &dshhost.Host{Key: key, Storage: dshhost.Storage{VolumeName: "vol-employee", RoleARN: "role-employee", AccessPointARN: "ap-employee"}}
	shared := &dshhost.VolumeMountSpec{Name: "vol-shared", Path: dshhost.WorkspaceSharedRoot}
	capable := []byte(`{"capabilities":["workspace_shared_disk"]}`)
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"pending storage", fmt.Errorf("%w: storage step role_ro creation outcome is unconfirmed", dshhost.ErrPending)},
		{"pending role", errors.Join(dshhost.ErrPending, errors.New("composite role creation outcome is unconfirmed"))},
		{"changed grant", dshhost.ErrChanged},
		{"hard failure", errors.New("invalid shared access point for composite role")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := &FCE2BLauncher{ReadWorkspaceMount: func(context.Context, wsfs.Database, uuid.UUID, uuid.UUID, *dshhost.Host) (wsfs.MountDecision, error) {
				return wsfs.MountDecision{}, tc.err
			}}
			got, mode := l.workspaceMountDecision(context.Background(), nil, key, before, capable)
			if mode != sharedLaunchKeep || got.Shared != nil || got.RoleARN != "role-employee" {
				t.Fatalf("an unready shared mount must keep the historical launch, got %+v mode=%v", got, mode)
			}
		})
	}

	l := &FCE2BLauncher{ReadWorkspaceMount: func(context.Context, wsfs.Database, uuid.UUID, uuid.UUID, *dshhost.Host) (wsfs.MountDecision, error) {
		return wsfs.MountDecision{Private: before, Shared: shared, RoleARN: "role-composite", Access: wsfs.AccessRead}, nil
	}}
	if got, mode := l.workspaceMountDecision(context.Background(), nil, key, before, capable); mode != sharedLaunchOffer || got.Shared != shared {
		t.Fatalf("a capable runtime may offer a ready shared mount, got %+v mode=%v", got, mode)
	}
	if _, mode := l.workspaceMountDecision(context.Background(), nil, key, before, []byte(`{"kind":"fc-e2b"}`)); mode != sharedLaunchConstrain {
		t.Fatal("a read grant on an image without shared disk must be checked against an existing mount")
	}
	revoked := &FCE2BLauncher{ReadWorkspaceMount: func(context.Context, wsfs.Database, uuid.UUID, uuid.UUID, *dshhost.Host) (wsfs.MountDecision, error) {
		return wsfs.MountDecision{Private: before, RoleARN: before.RoleARN, Revoked: true}, nil
	}}
	if _, mode := revoked.workspaceMountDecision(context.Background(), nil, key, before, []byte(`{"kind":"fc-e2b"}`)); mode != sharedLaunchRevoke {
		t.Fatal("missing shared-disk capability must not skip an explicit revoke")
	}
	if got, mode := (&FCE2BLauncher{}).workspaceMountDecision(context.Background(), nil, key, before, capable); mode != sharedLaunchKeep || got.Shared != nil {
		t.Fatalf("no reader must keep the historical launch, got %+v mode=%v", got, mode)
	}
}
