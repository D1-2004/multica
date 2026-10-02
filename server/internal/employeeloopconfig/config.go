// Package employeeloopconfig stores the selected owner of new agent work.
// Admission freezes that owner separately; changing this setting never reroutes existing work.
package employeeloopconfig

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type Mode string

const (
	Coordinator Mode = "coordinator"
	Employee    Mode = "employee"
)

var (
	ErrInvalidMode = errors.New("coordination_mode must be coordinator or employee")
	ErrNotReady    = errors.New("EmployeeLoop is not ready for this agent")
)

type Config struct {
	Mode    Mode
	Enabled bool
}

type Queryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

type Readiness func(context.Context, pgtype.UUID, pgtype.UUID) error

func Validate(mode string) error {
	if Mode(mode) != Coordinator && Mode(mode) != Employee {
		return ErrInvalidMode
	}
	return nil
}

func scan(row pgx.Row) (Config, error) {
	var config Config
	if err := row.Scan(&config.Mode, &config.Enabled); err != nil {
		return Config{}, err
	}
	return config, Validate(string(config.Mode))
}

// Load requires trusted workspace and agent identities. Tenant admission remains
// a Host concern because this is an agent-wide configuration, not a scene grant.
func Load(ctx context.Context, queryer Queryer, workspaceID, agentID pgtype.UUID) (Config, error) {
	return scan(queryer.QueryRow(ctx, `SELECT coordination_mode,inbound_coordinator FROM agent WHERE workspace_id=$1 AND id=$2`, workspaceID, agentID))
}

// ResolveUpdate holds the agent row until the caller commits all related policy
// and event-trigger writes. Omitted mode preserves the stored owner for old clients.
func ResolveUpdate(ctx context.Context, tx pgx.Tx, workspaceID, agentID pgtype.UUID, mode *string, enabled *bool, ready Readiness) (Config, error) {
	current, err := scan(tx.QueryRow(ctx, `SELECT coordination_mode,inbound_coordinator FROM agent WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, workspaceID, agentID))
	if err != nil {
		return Config{}, err
	}
	if mode != nil {
		if err := Validate(*mode); err != nil {
			return Config{}, err
		}
		current.Mode = Mode(*mode)
	}
	if enabled != nil {
		current.Enabled = *enabled
	}
	if current.Mode == Employee && (mode != nil || (enabled != nil && *enabled)) {
		if ready == nil || ready(ctx, workspaceID, agentID) != nil {
			return Config{}, ErrNotReady
		}
	}
	return current, nil
}

// SaveMode is used after ResolveUpdate, in the same transaction as the enabled
// policy. The existing inbound_coordinator API remains the independent total switch.
func SaveMode(ctx context.Context, tx pgx.Tx, workspaceID, agentID pgtype.UUID, mode Mode) error {
	if err := Validate(string(mode)); err != nil {
		return err
	}
	result, err := tx.Exec(ctx, `UPDATE agent SET coordination_mode=$3,updated_at=now() WHERE workspace_id=$1 AND id=$2`, workspaceID, agentID, mode)
	if err == nil && result.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return err
}
