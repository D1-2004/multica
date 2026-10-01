package contextcap

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Routine is one row of context_scope_routine: a cron or webhook routine
// (例行任务) bound to one Agent work scene, a group or a 1:1 chat. The
// autopilot it points at carries the schedule, trigger and run history; the
// row freezes what the Host needs to scope its runs and to post the start
// and end notices into the scene (docs/context-capabilities.md §9).
type Routine struct {
	ID          string
	WorkspaceID string
	AgentID     string
	SceneID     string
	TenantOrgID string
	SceneKind   string
	AutopilotID string
	// DeliveryOpenDingTalkID is the 1:1 counterpart's openDingTalkId ("" for
	// a group): a dm send needs it.
	DeliveryOpenDingTalkID string
	// PersonStaffID is the 1:1 counterpart's staffId when the creation proved
	// it; a run then carries that person's capability layer.
	PersonStaffID string
	DedupeKey     string
	CreatedByType string
	CreatedByID   string
	CreatedTaskID string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// Routine creator types.
const (
	RoutineCreatedByMember = "member"
	RoutineCreatedByAgent  = "agent"
)

// ErrRoutineDuplicate: another routine of the scene already has the dedupe
// key an update would give this one.
var ErrRoutineDuplicate = errors.New("a routine with the same purpose and schedule exists in this scene")

const routineColumns = `id::text, workspace_id::text, agent_id::text, scene_id::text, tenant_org_id, scene_kind,
	autopilot_id::text, delivery_open_dingtalk_id, person_staff_id, dedupe_key, created_by_type,
	COALESCE(created_by_id::text, ''), COALESCE(created_task_id::text, ''), created_at, updated_at`

func scanRoutine(row pgx.Row) (Routine, error) {
	var r Routine
	err := row.Scan(&r.ID, &r.WorkspaceID, &r.AgentID, &r.SceneID, &r.TenantOrgID, &r.SceneKind,
		&r.AutopilotID, &r.DeliveryOpenDingTalkID, &r.PersonStaffID, &r.DedupeKey, &r.CreatedByType,
		&r.CreatedByID, &r.CreatedTaskID, &r.CreatedAt, &r.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Routine{}, ErrNotFound
	}
	return r, err
}

// RoutineDedupeKey is the identity of a routine within its scene: the purpose
// as a sorted set of lower-cased words (word order and repeats do not make a
// new routine), the trigger kind, and for a schedule its cron expression and
// timezone. Re-registering the same purpose on the same schedule updates the
// routine; the same purpose on another schedule is another routine.
func RoutineDedupeKey(purpose, triggerKind, cron, timezone string) string {
	words := strings.FieldsFunc(strings.ToLower(purpose), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
	seen := make(map[string]bool, len(words))
	unique := words[:0]
	for _, word := range words {
		if !seen[word] {
			seen[word] = true
			unique = append(unique, word)
		}
	}
	sort.Strings(unique)
	parts := []string{strings.Join(unique, " "), strings.TrimSpace(triggerKind)}
	if triggerKind == "schedule" {
		parts = append(parts, strings.Join(strings.Fields(cron), " "), strings.TrimSpace(timezone))
	}
	return strings.Join(parts, "|")
}

// InsertRoutine stores a new routine. The caller runs it in the transaction
// that creates the autopilot. A routine of the same scene with the same
// dedupe key is ErrRoutineDuplicate.
func InsertRoutine(ctx context.Context, db DBTX, r Routine) (Routine, error) {
	if !ValidSceneID(r.SceneID) || strings.TrimSpace(r.TenantOrgID) == "" || r.DedupeKey == "" {
		return Routine{}, ErrInvalidInput
	}
	switch r.SceneKind {
	case SceneKindGroup, SceneKindDM:
	default:
		return Routine{}, ErrInvalidInput
	}
	switch r.CreatedByType {
	case RoutineCreatedByMember, RoutineCreatedByAgent:
	default:
		return Routine{}, ErrInvalidInput
	}
	created, err := scanRoutine(db.QueryRow(ctx, `INSERT INTO context_scope_routine (
			workspace_id, agent_id, scene_id, tenant_org_id, scene_kind, autopilot_id,
			delivery_open_dingtalk_id, person_staff_id, dedupe_key, created_by_type, created_by_id, created_task_id
		) VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5, $6::uuid, $7, $8, $9, $10, NULLIF($11, '')::uuid, NULLIF($12, '')::uuid)
		ON CONFLICT (scene_id, dedupe_key) DO NOTHING
		RETURNING `+routineColumns,
		r.WorkspaceID, r.AgentID, r.SceneID, strings.TrimSpace(r.TenantOrgID), r.SceneKind, r.AutopilotID,
		r.DeliveryOpenDingTalkID, r.PersonStaffID, r.DedupeKey, r.CreatedByType, r.CreatedByID, r.CreatedTaskID))
	if errors.Is(err, ErrNotFound) {
		return Routine{}, ErrRoutineDuplicate
	}
	return created, err
}

// GetRoutine loads one routine of the agent.
func GetRoutine(ctx context.Context, db DBTX, workspaceID, agentID, id string) (Routine, error) {
	if !validUUID(id) {
		return Routine{}, ErrInvalidInput
	}
	return scanRoutine(db.QueryRow(ctx, `SELECT `+routineColumns+` FROM context_scope_routine
		WHERE id = $1::uuid AND workspace_id = $2::uuid AND agent_id = $3::uuid`, id, workspaceID, agentID))
}

// GetRoutineByAutopilot returns the routine an autopilot belongs to, or
// ErrNotFound for an ordinary autopilot.
func GetRoutineByAutopilot(ctx context.Context, db DBTX, autopilotID string) (Routine, error) {
	if !validUUID(autopilotID) {
		return Routine{}, ErrInvalidInput
	}
	return scanRoutine(db.QueryRow(ctx, `SELECT `+routineColumns+` FROM context_scope_routine
		WHERE autopilot_id = $1::uuid`, autopilotID))
}

// GetRoutineByDedupe finds the routine of a scene with a dedupe key.
func GetRoutineByDedupe(ctx context.Context, db DBTX, sceneID, dedupeKey string) (Routine, error) {
	if !ValidSceneID(sceneID) {
		return Routine{}, ErrInvalidInput
	}
	return scanRoutine(db.QueryRow(ctx, `SELECT `+routineColumns+` FROM context_scope_routine
		WHERE scene_id = $1::uuid AND dedupe_key = $2`, sceneID, dedupeKey))
}

// ListSceneRoutines lists the routines of one scene of the agent, oldest
// first.
func ListSceneRoutines(ctx context.Context, db DBTX, workspaceID, agentID, sceneID string) ([]Routine, error) {
	if !ValidSceneID(sceneID) {
		return nil, ErrInvalidInput
	}
	rows, err := db.Query(ctx, `SELECT `+routineColumns+` FROM context_scope_routine
		WHERE workspace_id = $1::uuid AND agent_id = $2::uuid AND scene_id = $3::uuid
		ORDER BY created_at, id`, workspaceID, agentID, sceneID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Routine
	for rows.Next() {
		r, err := scanRoutine(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SetRoutineDedupeKey re-keys a routine after an edit changed its purpose or
// schedule; another routine of the scene holding the key is
// ErrRoutineDuplicate.
func SetRoutineDedupeKey(ctx context.Context, db DBTX, id, dedupeKey string) error {
	_, err := db.Exec(ctx, `UPDATE context_scope_routine SET dedupe_key = $2, updated_at = now() WHERE id = $1::uuid`, id, dedupeKey)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return ErrRoutineDuplicate
	}
	return err
}

// validUUID accepts a canonical lowercase UUID (a routine or autopilot id).
func validUUID(id string) bool { return ValidSceneID(id) }

// DeleteRoutine removes a routine row; the caller archives its autopilot in
// the same transaction.
func DeleteRoutine(ctx context.Context, db DBTX, id string) error {
	if _, err := db.Exec(ctx, `DELETE FROM context_scope_routine WHERE id = $1::uuid`, id); err != nil {
		return fmt.Errorf("delete scene routine: %w", err)
	}
	return nil
}
