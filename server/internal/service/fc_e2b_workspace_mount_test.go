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

func TestWorkspaceMountDecisionNeverBlocksLaunch(t *testing.T) {
	key := dshhost.Key{WorkspaceID: uuid.New(), AgentID: uuid.New()}
	before := &dshhost.Host{Key: key, Storage: dshhost.Storage{VolumeName: "vol-employee", RoleARN: "role-employee", AccessPointARN: "ap-employee"}}
	shared := &dshhost.VolumeMountSpec{Name: "vol-shared", Path: dshhost.WorkspaceSharedRoot}
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
			l := &FCE2BLauncher{PrepareWorkspaceMount: func(context.Context, wsfs.Database, uuid.UUID, uuid.UUID, *dshhost.Host) (wsfs.MountDecision, error) {
				return wsfs.MountDecision{Private: before, Shared: shared, RoleARN: "role-composite"}, tc.err
			}}
			got, fallback := l.workspaceMountDecision(context.Background(), nil, key, before)
			if !fallback || got.Shared != nil || got.RoleARN != "role-employee" || got.Private != before {
				t.Fatalf("an unready shared mount must launch private-only with fallback, got %+v fallback=%v", got, fallback)
			}
		})
	}

	l := &FCE2BLauncher{PrepareWorkspaceMount: func(context.Context, wsfs.Database, uuid.UUID, uuid.UUID, *dshhost.Host) (wsfs.MountDecision, error) {
		return wsfs.MountDecision{Private: before, Shared: shared, RoleARN: "role-composite", Access: wsfs.AccessRead}, nil
	}}
	if got, fallback := l.workspaceMountDecision(context.Background(), nil, key, before); fallback || got.Shared != shared || got.RoleARN != "role-composite" {
		t.Fatalf("a ready shared mount must be used, got %+v fallback=%v", got, fallback)
	}
	if got, fallback := (&FCE2BLauncher{}).workspaceMountDecision(context.Background(), nil, key, before); fallback || got.Shared != nil || got.RoleARN != "role-employee" {
		t.Fatalf("no workspace controller must launch private-only, got %+v fallback=%v", got, fallback)
	}
}
