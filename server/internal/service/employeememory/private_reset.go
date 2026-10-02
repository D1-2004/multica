package employeememory

import (
	"context"
	"github.com/jackc/pgx/v5"
)

// ResetPrivateTx clears only the Host-authorized requester namespace. It is
// separate from the scene-management API, which never clears private records.
func (s *Store) ResetPrivateTx(ctx context.Context, tx pgx.Tx, scope Scope) error {
	if tx == nil || scope.Kind != ScopePrivate || scope.PrincipalID == "" {
		return ErrInvalidScope
	}
	if err := lockWriteScope(ctx, tx, scope); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE employee_learning SET forgotten_at=now() WHERE `+scopePredicate+` AND forgotten_at IS NULL`, scope.args()...); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE employee_memory_state SET revision=revision+1,reset_at=now(),updated_at=now() WHERE `+scopePredicate, scope.args()...)
	return err
}
