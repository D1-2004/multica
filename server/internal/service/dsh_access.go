package service

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/dshhost"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

var ErrDSHAccessDenied = errors.New("DSH task access is unavailable or no longer authorized")

// DSHInvokeCheck checks employee invocation for scheduled platform tasks.
type DSHInvokeCheck func(context.Context, *db.Queries, db.Agent, pgtype.UUID) error

// Scheduled platform tasks retain the runtime/employee admission lock order.
func lockDSHEmployeeAdmission(ctx context.Context, tx pgx.Tx, key dshhost.Key, runtimeID pgtype.UUID) error {
	if tx == nil {
		return errors.New("DSH schedule requires a task transaction")
	}
	// Match Host startup and template cutover: Runtime, then employee, before
	// domain row locks. Transaction-scoped locks release on every error path.
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock_shared($1,$2)", fcE2BRuntimeLockClass, fcE2BRuntimeLockKey(runtimeID)); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1,$2)", dshEmployeeLockClass,
		dshEmployeeLockKey(pgtype.UUID{Bytes: key.WorkspaceID, Valid: true}, pgtype.UUID{Bytes: key.AgentID, Valid: true}))
	return err
}
