package assoc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
)

const edgeUpsertSQL = `
INSERT INTO assoc_edge (
    workspace_id, agent_id, src_type, src_id, dst_type, dst_id, rel, status, props, last_touched_at, opened_by_run_id
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
ON CONFLICT (agent_id, src_type, src_id, dst_type, dst_id, rel) WHERE status = 'open'
DO UPDATE SET
    last_touched_at = EXCLUDED.last_touched_at,
    props = COALESCE(assoc_edge.props, '{}'::jsonb) || COALESCE(EXCLUDED.props, '{}'::jsonb)
RETURNING id, workspace_id, agent_id, src_type, src_id, dst_type, dst_id, rel, status, props, opened_at, last_touched_at, closed_at, opened_by_run_id`

const eventUpsertSQL = `
INSERT INTO assoc_event (
    workspace_id, agent_id, source, direction, evidence_id, occurred_at, scene_key, person_key, task_id
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
ON CONFLICT (workspace_id, agent_id, evidence_id)
DO UPDATE SET
    direction = CASE WHEN assoc_event.direction = '' THEN EXCLUDED.direction ELSE assoc_event.direction END,
    task_id = COALESCE(assoc_event.task_id, EXCLUDED.task_id)
RETURNING id, workspace_id, agent_id, source, direction, evidence_id, occurred_at, scene_key, person_key, task_id, created_at`

type DBTX interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

type txBeginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

type SQLStore struct {
	db DBTX
}

func NewSQLStore(db DBTX) *SQLStore {
	return &SQLStore{db: db}
}

func (s *SQLStore) InTx(ctx context.Context, fn func(Store) error) error {
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return fn(s)
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(NewSQLStore(tx)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *SQLStore) InsertTask(ctx context.Context, task Task) (Task, error) {
	if err := ValidatePurpose(task.Purpose); err != nil {
		return Task{}, err
	}
	if task.Status == "" {
		task.Status = StatusOpen
	}
	if task.LastTouchedAt.IsZero() {
		task.LastTouchedAt = time.Now().UTC()
	}
	props, err := json.Marshal(cloneProps(task.Props))
	if err != nil {
		return Task{}, err
	}
	ws, err := requireUUID(task.WorkspaceID)
	if err != nil {
		return Task{}, err
	}
	agent, err := requireUUID(task.AgentID)
	if err != nil {
		return Task{}, err
	}
	issue, err := requireUUID(task.IssueID)
	if err != nil {
		return Task{}, err
	}
	runID, err := optionalUUID(task.RunID)
	if err != nil {
		return Task{}, err
	}
	row := s.db.QueryRow(ctx, `
INSERT INTO assoc_task (
    workspace_id, agent_id, issue_id, purpose, status, intent, props, run_id, last_touched_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING id, workspace_id, agent_id, issue_id, purpose, status, intent, props, run_id, last_touched_at, created_at, updated_at`,
		ws, agent, issue, task.Purpose, task.Status, task.Intent, props, runID, task.LastTouchedAt,
	)
	return scanTask(row)
}

func (s *SQLStore) GetOpenTaskByIssue(ctx context.Context, workspaceID, agentID, issueID string) (Task, error) {
	ws, err := requireUUID(workspaceID)
	if err != nil {
		return Task{}, err
	}
	agent, err := requireUUID(agentID)
	if err != nil {
		return Task{}, err
	}
	issue, err := requireUUID(issueID)
	if err != nil {
		return Task{}, err
	}
	row := s.db.QueryRow(ctx, `
SELECT id, workspace_id, agent_id, issue_id, purpose, status, intent, props, run_id, last_touched_at, created_at, updated_at
FROM assoc_task
WHERE workspace_id = $1 AND agent_id = $2 AND issue_id = $3
  AND status IN ('open', 'waiting')
ORDER BY last_touched_at DESC
LIMIT 1`, ws, agent, issue)
	task, err := scanTask(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Task{}, ErrNotFound
	}
	return task, err
}

func (s *SQLStore) TouchTask(ctx context.Context, id string, at time.Time) error {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	taskID, err := requireUUID(id)
	if err != nil {
		return err
	}
	tag, err := s.db.Exec(ctx, `
UPDATE assoc_task SET last_touched_at = $2, updated_at = $2 WHERE id = $1`, taskID, at)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *SQLStore) GetTask(ctx context.Context, id string) (Task, error) {
	taskID, err := requireUUID(id)
	if err != nil {
		return Task{}, err
	}
	row := s.db.QueryRow(ctx, `
SELECT id, workspace_id, agent_id, issue_id, purpose, status, intent, props, run_id, last_touched_at, created_at, updated_at
FROM assoc_task WHERE id = $1`, taskID)
	task, err := scanTask(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Task{}, ErrNotFound
	}
	return task, err
}

func (s *SQLStore) ListTasksByIssue(ctx context.Context, workspaceID, agentID, issueID string, since, until time.Time) ([]Task, error) {
	ws, err := requireUUID(workspaceID)
	if err != nil {
		return nil, err
	}
	agent, err := requireUUID(agentID)
	if err != nil {
		return nil, err
	}
	issue, err := requireUUID(issueID)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, `
SELECT id, workspace_id, agent_id, issue_id, purpose, status, intent, props, run_id, last_touched_at, created_at, updated_at
FROM assoc_task
WHERE workspace_id = $1 AND agent_id = $2 AND issue_id = $3
  AND last_touched_at >= $4 AND last_touched_at <= $5
ORDER BY last_touched_at DESC`,
		ws, agent, issue, since, until)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanTasks(rows)
}

func (s *SQLStore) ListTasksByIDs(ctx context.Context, ids []string) ([]Task, error) {
	if len(ids) == 0 {
		return []Task{}, nil
	}
	uuids := make([]pgtype.UUID, 0, len(ids))
	for _, id := range ids {
		u, err := util.ParseUUID(id)
		if err != nil {
			continue
		}
		uuids = append(uuids, u)
	}
	rows, err := s.db.Query(ctx, `
SELECT id, workspace_id, agent_id, issue_id, purpose, status, intent, props, run_id, last_touched_at, created_at, updated_at
FROM assoc_task WHERE id = ANY($1::uuid[])`, uuids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanTasks(rows)
}

func (s *SQLStore) ListTasksInWindow(ctx context.Context, workspaceID, agentID string, since, until time.Time) ([]Task, error) {
	ws, err := requireUUID(workspaceID)
	if err != nil {
		return nil, err
	}
	agent, err := requireUUID(agentID)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, `
SELECT id, workspace_id, agent_id, issue_id, purpose, status, intent, props, run_id, last_touched_at, created_at, updated_at
FROM assoc_task
WHERE workspace_id = $1 AND agent_id = $2
  AND last_touched_at >= $3 AND last_touched_at <= $4
ORDER BY last_touched_at DESC`, ws, agent, since, until)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanTasks(rows)
}

func (s *SQLStore) InsertEdge(ctx context.Context, edge Edge) (Edge, error) {
	if edge.Status == "" {
		edge.Status = StatusOpen
	}
	if edge.LastTouchedAt.IsZero() {
		edge.LastTouchedAt = time.Now().UTC()
	}
	props, err := json.Marshal(cloneProps(edge.Props))
	if err != nil {
		return Edge{}, err
	}
	ws, err := requireUUID(edge.WorkspaceID)
	if err != nil {
		return Edge{}, err
	}
	agent, err := requireUUID(edge.AgentID)
	if err != nil {
		return Edge{}, err
	}
	runID, err := optionalUUID(edge.OpenedByRunID)
	if err != nil {
		return Edge{}, err
	}
	return scanEdge(s.db.QueryRow(ctx, edgeUpsertSQL,
		ws, agent, edge.SrcType, edge.SrcID, edge.DstType, edge.DstID, edge.Rel, edge.Status,
		props, edge.LastTouchedAt, runID,
	))
}

func (s *SQLStore) ListEdgesByDst(ctx context.Context, workspaceID, agentID, dstType, dstID string, since time.Time) ([]Edge, error) {
	ws, err := requireUUID(workspaceID)
	if err != nil {
		return nil, err
	}
	agent, err := requireUUID(agentID)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, `
SELECT id, workspace_id, agent_id, src_type, src_id, dst_type, dst_id, rel, status, props, opened_at, last_touched_at, closed_at, opened_by_run_id
FROM assoc_edge
WHERE workspace_id = $1 AND agent_id = $2 AND dst_type = $3 AND dst_id = $4
  AND last_touched_at >= $5 AND status <> 'closed'`,
		ws, agent, dstType, dstID, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEdges(rows)
}

func (s *SQLStore) ListEdgesBySrc(ctx context.Context, workspaceID, agentID, srcType, srcID string) ([]Edge, error) {
	ws, err := requireUUID(workspaceID)
	if err != nil {
		return nil, err
	}
	agent, err := requireUUID(agentID)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, `
SELECT id, workspace_id, agent_id, src_type, src_id, dst_type, dst_id, rel, status, props, opened_at, last_touched_at, closed_at, opened_by_run_id
FROM assoc_edge
WHERE workspace_id = $1 AND agent_id = $2 AND src_type = $3 AND src_id = $4`,
		ws, agent, srcType, srcID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEdges(rows)
}

func (s *SQLStore) InsertEvent(ctx context.Context, event Event) (Event, error) {
	if event.OccurredAt.IsZero() {
		event.OccurredAt = time.Now().UTC()
	}
	ws, err := requireUUID(event.WorkspaceID)
	if err != nil {
		return Event{}, err
	}
	agent, err := requireUUID(event.AgentID)
	if err != nil {
		return Event{}, err
	}
	taskID, err := optionalUUID(event.TaskID)
	if err != nil {
		return Event{}, err
	}
	return scanEvent(s.db.QueryRow(ctx, eventUpsertSQL,
		ws, agent, event.Source, event.Direction, event.EvidenceID, event.OccurredAt,
		event.SceneKey, event.PersonKey, taskID,
	))
}

func (s *SQLStore) GetEventByEvidence(ctx context.Context, workspaceID, agentID, evidenceID string) (Event, error) {
	ws, err := requireUUID(workspaceID)
	if err != nil {
		return Event{}, err
	}
	agent, err := requireUUID(agentID)
	if err != nil {
		return Event{}, err
	}
	row := s.db.QueryRow(ctx, `
SELECT id, workspace_id, agent_id, source, direction, evidence_id, occurred_at, scene_key, person_key, task_id, created_at
FROM assoc_event
WHERE workspace_id = $1 AND agent_id = $2 AND evidence_id = $3`,
		ws, agent, evidenceID)
	event, err := scanEvent(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Event{}, ErrNotFound
	}
	return event, err
}

func (s *SQLStore) UpdateEventTask(ctx context.Context, workspaceID, agentID, evidenceID, taskID string) error {
	ws, err := requireUUID(workspaceID)
	if err != nil {
		return err
	}
	agent, err := requireUUID(agentID)
	if err != nil {
		return err
	}
	task, err := requireUUID(taskID)
	if err != nil {
		return err
	}
	tag, err := s.db.Exec(ctx, `
UPDATE assoc_event SET task_id = $4
WHERE workspace_id = $1 AND agent_id = $2 AND evidence_id = $3`,
		ws, agent, evidenceID, task)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *SQLStore) EnsureScene(ctx context.Context, workspaceID, agentID, sceneKey, kind string, at time.Time) error {
	sceneKey = strings.TrimSpace(sceneKey)
	if sceneKey == "" {
		return nil
	}
	if kind == "" {
		kind = "dm"
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	ws, err := requireUUID(workspaceID)
	if err != nil {
		return err
	}
	agent, err := requireUUID(agentID)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `
INSERT INTO assoc_scene (workspace_id, agent_id, scene_key, kind, last_touched_at)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (agent_id, scene_key) DO UPDATE
SET last_touched_at = EXCLUDED.last_touched_at, kind = EXCLUDED.kind, updated_at = EXCLUDED.last_touched_at`,
		ws, agent, sceneKey, kind, at)
	return err
}

func (s *SQLStore) EnsurePerson(ctx context.Context, workspaceID, agentID, personKey, displayName string, aliases []string) error {
	personKey = strings.TrimSpace(personKey)
	if personKey == "" {
		return nil
	}
	ws, err := requireUUID(workspaceID)
	if err != nil {
		return err
	}
	agent, err := requireUUID(agentID)
	if err != nil {
		return err
	}
	if _, err := s.db.Exec(ctx, `
INSERT INTO assoc_person (workspace_id, agent_id, person_key, display_name)
VALUES ($1, $2, $3, $4)
ON CONFLICT (agent_id, person_key) DO UPDATE
SET display_name = CASE
    WHEN EXCLUDED.display_name <> '' THEN EXCLUDED.display_name
    ELSE assoc_person.display_name
END, updated_at = now()`, ws, agent, personKey, displayName); err != nil {
		return err
	}
	ids := append([]string{personKey}, aliases...)
	for _, alias := range ids {
		alias = strings.TrimSpace(alias)
		if alias == "" {
			continue
		}
		if _, err := s.db.Exec(ctx, `
INSERT INTO assoc_person_alias (workspace_id, agent_id, person_key, alias_key)
VALUES ($1, $2, $3, $4)
ON CONFLICT (agent_id, alias_key) DO NOTHING`, ws, agent, personKey, alias); err != nil {
			return err
		}
	}
	return nil
}

func (s *SQLStore) ResolvePersonKey(ctx context.Context, workspaceID, agentID, identifier string) (string, error) {
	identifier = strings.TrimSpace(identifier)
	if identifier == "" {
		return "", ErrNotFound
	}
	ws, err := requireUUID(workspaceID)
	if err != nil {
		return "", err
	}
	agent, err := requireUUID(agentID)
	if err != nil {
		return "", err
	}
	var key string
	err = s.db.QueryRow(ctx, `
SELECT person_key FROM assoc_person_alias
WHERE workspace_id = $1 AND agent_id = $2 AND alias_key = $3`, ws, agent, identifier).Scan(&key)
	if err == nil && key != "" {
		return key, nil
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	return identifier, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanTask(row rowScanner) (Task, error) {
	var (
		id, ws, agent, issue      pgtype.UUID
		runID                     pgtype.UUID
		purpose, status, intent   string
		props                     []byte
		touched, created, updated time.Time
	)
	if err := row.Scan(&id, &ws, &agent, &issue, &purpose, &status, &intent, &props, &runID, &touched, &created, &updated); err != nil {
		return Task{}, err
	}
	task := Task{
		ID:            util.UUIDToString(id),
		WorkspaceID:   util.UUIDToString(ws),
		AgentID:       util.UUIDToString(agent),
		IssueID:       util.UUIDToString(issue),
		Purpose:       purpose,
		Status:        status,
		Intent:        intent,
		Props:         map[string]any{},
		LastTouchedAt: touched,
		CreatedAt:     created,
		UpdatedAt:     updated,
	}
	if runID.Valid {
		task.RunID = util.UUIDToString(runID)
	}
	if len(props) > 0 {
		_ = json.Unmarshal(props, &task.Props)
	}
	return task, nil
}

func scanTasks(rows pgx.Rows) ([]Task, error) {
	out := []Task{}
	for rows.Next() {
		task, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, task)
	}
	return out, rows.Err()
}

func scanEdge(row rowScanner) (Edge, error) {
	var (
		id, ws, agent                               pgtype.UUID
		srcType, srcID, dstType, dstID, rel, status string
		props                                       []byte
		opened, touched                             time.Time
		closed                                      pgtype.Timestamptz
		runID                                       pgtype.UUID
	)
	if err := row.Scan(&id, &ws, &agent, &srcType, &srcID, &dstType, &dstID, &rel, &status, &props, &opened, &touched, &closed, &runID); err != nil {
		return Edge{}, err
	}
	edge := Edge{
		ID:            util.UUIDToString(id),
		WorkspaceID:   util.UUIDToString(ws),
		AgentID:       util.UUIDToString(agent),
		SrcType:       srcType,
		SrcID:         srcID,
		DstType:       dstType,
		DstID:         dstID,
		Rel:           rel,
		Status:        status,
		Props:         map[string]any{},
		OpenedAt:      opened,
		LastTouchedAt: touched,
	}
	if closed.Valid {
		t := closed.Time
		edge.ClosedAt = &t
	}
	if runID.Valid {
		edge.OpenedByRunID = util.UUIDToString(runID)
	}
	if len(props) > 0 {
		_ = json.Unmarshal(props, &edge.Props)
	}
	return edge, nil
}

func scanEdges(rows pgx.Rows) ([]Edge, error) {
	out := []Edge{}
	for rows.Next() {
		edge, err := scanEdge(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, edge)
	}
	return out, rows.Err()
}

func scanEvent(row rowScanner) (Event, error) {
	var (
		id, ws, agent                              pgtype.UUID
		source, direction, evidence, scene, person string
		occurred, created                          time.Time
		taskID                                     pgtype.UUID
	)
	if err := row.Scan(&id, &ws, &agent, &source, &direction, &evidence, &occurred, &scene, &person, &taskID, &created); err != nil {
		return Event{}, err
	}
	event := Event{
		ID:          util.UUIDToString(id),
		WorkspaceID: util.UUIDToString(ws),
		AgentID:     util.UUIDToString(agent),
		Source:      source,
		Direction:   direction,
		EvidenceID:  evidence,
		OccurredAt:  occurred,
		SceneKey:    scene,
		PersonKey:   person,
		CreatedAt:   created,
	}
	if taskID.Valid {
		event.TaskID = util.UUIDToString(taskID)
	}
	return event, nil
}

func requireUUID(s string) (pgtype.UUID, error) {
	if strings.TrimSpace(s) == "" {
		return pgtype.UUID{}, fmt.Errorf("%w: uuid is required", ErrInvalidQuery)
	}
	u, err := util.ParseUUID(s)
	if err != nil {
		return pgtype.UUID{}, fmt.Errorf("%w: %v", ErrInvalidQuery, err)
	}
	return u, nil
}

func optionalUUID(s string) (any, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	u, err := util.ParseUUID(s)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidQuery, err)
	}
	return u, nil
}
