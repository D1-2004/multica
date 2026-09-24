package handler

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"testing"
)

func TestDSHNativeInvokeDoesNotGrantAdminPrivateRunAccess(t *testing.T) {
	owner := pgtype.UUID{Bytes: uuid.New(), Valid: true}
	admin := pgtype.UUID{Bytes: uuid.New(), Valid: true}
	agent := db.Agent{ID: pgtype.UUID{Bytes: uuid.New(), Valid: true}, WorkspaceID: pgtype.UUID{Bytes: uuid.New(), Valid: true}, OwnerID: owner, PermissionMode: "private"}
	h := &Handler{}
	q := db.New(nil)
	if err := h.dshNativeInvoke(context.Background(), q, agent, owner); err != nil {
		t.Fatal("owner cannot invoke", err)
	}
	if err := h.dshNativeInvoke(context.Background(), q, agent, admin); !errors.Is(err, service.ErrDSHAccessDenied) {
		t.Fatal("management access bypassed private invocation")
	}
}
