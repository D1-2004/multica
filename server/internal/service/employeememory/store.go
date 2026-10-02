// Package employeememory stores EmployeeLoop learning independently of the
// Coordinator memory namespace. Callers supply Host-authorized scope and evidence.
package employeememory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/scene"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type ScopeKind string

const (
	ScopeScene   ScopeKind = "scene"
	ScopePrivate ScopeKind = "private"
)

// Scope is an authorization result constructed by the Host. It must never be
// decoded from model tool arguments. Private scope is also partitioned by scene.
type Scope struct {
	WorkspaceID, AgentID pgtype.UUID
	TenantOrgID          string
	Scene                scene.Ref
	Kind                 ScopeKind
	PrincipalID          string
}

// TrustedEvidence is supplied separately from model-controlled LearningRecord.
// HumanStated requires authenticated human-origin evidence. VerifiedExecution
// requires a Host verification result with TaskID and ExecutionID, not an
// agent's completion claim or model-selected task identifiers. There is
// intentionally no JSON/tool endpoint for this argument.
type TrustedEvidence struct {
	SourceID, EvidenceID, ActorID  string
	TaskID, ExecutionID            string
	HumanStated, VerifiedExecution bool
}

var (
	ErrInvalidScope        = errors.New("employee memory: invalid scope")
	ErrInvalidLearning     = errors.New("employee memory: invalid learning")
	ErrUntrustedCorrection = errors.New("employee memory: untrusted correction of trusted learning")
	ErrUnverified          = errors.New("employee memory: run has no verified outcome")
	learningKeyPattern     = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)
)

type Store struct{ pool *pgxpool.Pool }

func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

const scopePredicate = `workspace_id=$1 AND agent_id=$2 AND tenant_org_id=$3 AND scene_id=$4 AND scope_kind=$5 AND principal_id=$6`

func (s Scope) args() []any {
	return []any{s.WorkspaceID, s.AgentID, s.TenantOrgID, s.Scene.SceneID, string(s.Kind), s.PrincipalID}
}
func (s Scope) validate() error {
	if !s.WorkspaceID.Valid || !s.AgentID.Valid || s.WorkspaceID.Bytes == [16]byte{} || s.AgentID.Bytes == [16]byte{} || !validText(s.TenantOrgID, 128) || strings.TrimSpace(s.TenantOrgID) != s.TenantOrgID {
		return ErrInvalidScope
	}
	if _, err := scene.ParseID(s.Scene.SceneID); err != nil {
		return ErrInvalidScope
	}
	if (s.Kind == ScopeScene && s.PrincipalID == "") || (s.Kind == ScopePrivate && validText(s.PrincipalID, 256) && strings.TrimSpace(s.PrincipalID) == s.PrincipalID) {
		return nil
	}
	return ErrInvalidScope
}
func authorize(ctx context.Context, q *db.Queries, scope Scope) error {
	if err := scope.validate(); err != nil {
		return err
	}
	id, err := scene.ParseID(scope.Scene.SceneID)
	if err != nil {
		return ErrInvalidScope
	}
	row, err := scene.Get(ctx, q, scene.Owner{WorkspaceID: scope.WorkspaceID, AgentID: scope.AgentID}, id)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidScope, err)
	}
	if err := scene.CheckTenant(row, scope.TenantOrgID); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidScope, err)
	}
	return nil
}

// beginWrite takes the workspace lock first, so workspace teardown cannot race
// with namespace creation. The state row serializes writes across all replicas.
func (s *Store) beginWrite(ctx context.Context, scope Scope) (pgx.Tx, error) {
	if s == nil || s.pool == nil {
		return nil, ErrInvalidScope
	}
	if err := scope.validate(); err != nil {
		return nil, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (pgx.Tx, error) { _ = tx.Rollback(ctx); return nil, err }
	var id pgtype.UUID
	if err := tx.QueryRow(ctx, "SELECT id FROM workspace WHERE id=$1 FOR KEY SHARE", scope.WorkspaceID).Scan(&id); err != nil {
		return fail(err)
	}
	if err := authorize(ctx, db.New(tx), scope); err != nil {
		return fail(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO employee_memory_state(workspace_id,agent_id,tenant_org_id,scene_id,scope_kind,principal_id) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT DO NOTHING`, scope.args()...); err != nil {
		return fail(err)
	}
	var revision int64
	if err := tx.QueryRow(ctx, `SELECT revision FROM employee_memory_state WHERE `+scopePredicate+` FOR UPDATE`, scope.args()...).Scan(&revision); err != nil {
		return fail(err)
	}
	return tx, nil
}

// Record performs bounded synchronous database I/O only. It never calls a model,
// network memory backend, or wiki worker. Replays return the original record;
// reset tombstones remain to prevent old evidence from repopulating a namespace.
func (s *Store) Record(ctx context.Context, scope Scope, rec LearningRecord, e TrustedEvidence) (LearningRecord, error) {
	rec, err := normalizeRecord(rec, scope, e)
	if err != nil {
		return LearningRecord{}, err
	}
	replay := replayKey(e)
	tx, err := s.beginWrite(ctx, scope)
	if err != nil {
		return LearningRecord{}, err
	}
	defer tx.Rollback(ctx)
	var raw []byte
	err = tx.QueryRow(ctx, `SELECT record FROM employee_learning WHERE `+scopePredicate+` AND replay_key=$7`, append(scope.args(), replay)...).Scan(&raw)
	if err == nil {
		var existing LearningRecord
		if err = json.Unmarshal(raw, &existing); err != nil {
			return LearningRecord{}, err
		}
		return existing, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return LearningRecord{}, err
	}
	var previous LearningRecord
	err = tx.QueryRow(ctx, `SELECT record FROM employee_learning WHERE `+scopePredicate+` AND forgotten_at IS NULL AND superseded_by IS NULL AND record->>'type'=$7 AND record->>'key'=$8 ORDER BY created_at DESC,id DESC LIMIT 1`, append(scope.args(), string(rec.Type), rec.Key)...).Scan(&raw)
	if err == nil {
		if err = json.Unmarshal(raw, &previous); err != nil {
			return LearningRecord{}, err
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return LearningRecord{}, err
	}
	if rec.Supersedes != "" {
		if previous.ID != rec.Supersedes {
			return LearningRecord{}, fmt.Errorf("%w: supersedes must name the current record for this exact scope/type/key", ErrInvalidLearning)
		}
	}
	if previous.ID != "" {
		if previous.Trusted && !rec.Trusted {
			return LearningRecord{}, ErrUntrustedCorrection
		}
		rec.Supersedes = previous.ID
	}
	rec.ID = uuid.NewString()
	rec.CreatedAt = time.Now().UTC()
	timestamp := rec.CreatedAt.Format(time.RFC3339Nano)
	rec.Workflow = &MemoryWorkflow{Status: MemoryWorkflowStatusNotRequired, CreatedAt: timestamp, UpdatedAt: timestamp}
	recordMemoryWorkflowCapture(rec.Workflow, e.ActorID, MemoryWorkflowArtifact{Backend: "postgres", Source: e.SourceID, PageID: rec.ID, Title: rec.Key, Snippet: e.EvidenceID}, timestamp)
	rec.Workflow.Capture.SourceID = e.SourceID
	rec.Workflow.Capture.EvidenceID = e.EvidenceID
	raw, err = json.Marshal(rec)
	if err != nil {
		return LearningRecord{}, err
	}
	if len(raw) > 14000 {
		return LearningRecord{}, fmt.Errorf("%w: record too large", ErrInvalidLearning)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO employee_learning(workspace_id,agent_id,tenant_org_id,scene_id,scope_kind,principal_id,id,replay_key,record,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, append(scope.args(), rec.ID, replay, raw, rec.CreatedAt)...); err != nil {
		return LearningRecord{}, err
	}
	if previous.ID != "" {
		if _, err = tx.Exec(ctx, `UPDATE employee_learning SET superseded_by=$7 WHERE `+scopePredicate+` AND id=$8`, append(scope.args(), rec.ID, previous.ID)...); err != nil {
			return LearningRecord{}, err
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE employee_memory_state SET revision=revision+1,updated_at=now() WHERE `+scopePredicate, scope.args()...); err != nil {
		return LearningRecord{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return LearningRecord{}, err
	}
	return rec, nil
}

// Search first resolves the trusted scene directory, then fetches only the exact
// namespace. Fuzzy text matching and ranking operate on that authorized set.
func (s *Store) Search(ctx context.Context, scope Scope, query string, limit int) ([]LearningSearchResult, error) {
	if s == nil || s.pool == nil {
		return nil, ErrInvalidScope
	}
	if err := authorize(ctx, db.New(s.pool), scope); err != nil {
		return nil, err
	}
	if len(query) > 512 || !utf8.ValidString(query) {
		return nil, ErrInvalidLearning
	}
	if limit <= 0 {
		limit = 5
	}
	if limit > MaxLearningLimit {
		limit = MaxLearningLimit
	}
	rows, err := s.pool.Query(ctx, `SELECT record FROM employee_learning WHERE `+scopePredicate+` AND forgotten_at IS NULL AND superseded_by IS NULL ORDER BY created_at DESC,id DESC LIMIT 2000`, scope.args()...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var records []LearningRecord
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var rec LearningRecord
		if err := json.Unmarshal(raw, &rec); err != nil {
			return nil, err
		}
		records = append(records, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	results := make([]LearningSearchResult, 0, len(records))
	query = strings.TrimSpace(strings.ToLower(query))
	now := time.Now().UTC()
	for _, rec := range dedupeLearnings(records) {
		if query != "" && !learningMatchesQuery(rec, query) && memoryScore(rec, query) == 0 {
			continue
		}
		results = append(results, LearningSearchResult{LearningRecord: rec, EffectiveConfidence: effectiveLearningConfidence(rec, now)})
	}
	sort.Slice(results, func(i, j int) bool {
		a, b := results[i], results[j]
		if x, y := memoryScore(a.LearningRecord, query), memoryScore(b.LearningRecord, query); x != y {
			return x > y
		}
		if a.EffectiveConfidence != b.EffectiveConfidence {
			return a.EffectiveConfidence > b.EffectiveConfidence
		}
		if !a.CreatedAt.Equal(b.CreatedAt) {
			return a.CreatedAt.After(b.CreatedAt)
		}
		return a.ID > b.ID
	})
	if len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}
func memoryScore(rec LearningRecord, query string) int {
	return privateMemoryMatchScore(normalizeMemorySearchText(rec.Key+" "+rec.Insight), query)
}
func (s *Store) Brief(ctx context.Context, scope Scope, query string, limit int) (string, error) {
	records, err := s.Search(ctx, scope, query, limit)
	if err != nil {
		return "", err
	}
	return formatLearningBrief(records), nil
}

// Reset forgets only this exact namespace. Durable replay receipts remain, so a
// late task-completion replay cannot resurrect a previously forgotten learning.
func (s *Store) Reset(ctx context.Context, scope Scope) error {
	tx, err := s.beginWrite(ctx, scope)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `UPDATE employee_learning SET forgotten_at=now() WHERE `+scopePredicate+` AND forgotten_at IS NULL`, scope.args()...); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE employee_memory_state SET revision=revision+1,reset_at=now(),updated_at=now() WHERE `+scopePredicate, scope.args()...); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Distill is invoked by a background consumer after Host verification commits.
// Its text assembly is deterministic; no LLM or foreground waiting is involved.
func (s *Store) Distill(ctx context.Context, scope Scope, run VerifiedRun) (LearningRecord, error) {
	if !run.Passed || !validText(run.Proof, 2000) || !validText(run.ProofKind, 80) || !validText(run.TaskID, 128) || !validText(run.ExecutionID, 128) || !validText(run.Title, 512) || !validText(run.ActorID, 128) || !validText(run.EvidenceID, 256) || len(run.Details) > 8000 {
		return LearningRecord{}, ErrUnverified
	}
	return s.Record(ctx, scope, LearningRecord{Type: LearningTypeOperational, Key: learningKeyForTask(run.Title, run.TaskID), Insight: taskDistillInsight(run), Confidence: 7}, TrustedEvidence{TaskID: run.TaskID, ExecutionID: run.ExecutionID, SourceID: "execution:" + run.ExecutionID, EvidenceID: run.EvidenceID, ActorID: run.ActorID, VerifiedExecution: true})
}
func normalizeRecord(rec LearningRecord, scope Scope, e TrustedEvidence) (LearningRecord, error) {
	bad := func(message string) (LearningRecord, error) {
		return LearningRecord{}, fmt.Errorf("%w: %s", ErrInvalidLearning, message)
	}
	if !validText(e.SourceID, 256) || !validText(e.EvidenceID, 256) || !validText(e.ActorID, 128) || e.HumanStated && e.VerifiedExecution {
		return bad("Host evidence is missing or ambiguous")
	}
	rec.ID = ""
	rec.Workflow = nil
	rec.CreatedAt = time.Time{}
	rec.CreatedBy = e.ActorID
	rec.SourceID = e.SourceID
	rec.EvidenceID = e.EvidenceID
	rec.Scope = string(scope.Kind)
	rec.Key = strings.TrimSpace(rec.Key)
	rec.Insight = strings.TrimSpace(rec.Insight)
	rec.Trusted = e.HumanStated || e.VerifiedExecution
	switch {
	case e.HumanStated:
		rec.Source = LearningSourceUserStated
	case e.VerifiedExecution:
		rec.Source = LearningSourceExecution
	default:
		if rec.Source == "" || rec.Source == LearningSourceUserStated || rec.Source == LearningSourceExecution {
			rec.Source = LearningSourceInferred
		}
	}
	rec.TaskID = ""
	rec.ExecutionID = ""
	if e.VerifiedExecution {
		if !validText(e.TaskID, 128) || !validText(e.ExecutionID, 128) {
			return bad("verified execution requires Host task and execution IDs")
		}
		rec.TaskID = e.TaskID
		rec.ExecutionID = e.ExecutionID
	}
	typeOK, sourceOK := false, false
	for _, v := range ValidLearningTypes() {
		typeOK = typeOK || v == rec.Type
	}
	for _, v := range ValidLearningSources() {
		sourceOK = sourceOK || v == rec.Source
	}
	if !typeOK || !sourceOK || !learningKeyPattern.MatchString(rec.Key) || len(rec.Key) > MaxLearningKeyLen || !validText(rec.Insight, MaxLearningInsightLen) || containsInstructionLikeLearning(rec.Insight) || rec.Confidence < 1 || rec.Confidence > 10 {
		return bad("invalid type/key/insight/source/confidence")
	}
	if rec.Supersedes != "" {
		if _, err := uuid.Parse(rec.Supersedes); err != nil {
			return bad("invalid supersedes")
		}
	}
	if len(rec.TaskID) > 128 || len(rec.ExecutionID) > 128 || len(rec.PlaybookSlug) > 80 || len(rec.Files) > 16 || len(rec.Entities) > 16 {
		return bad("metadata exceeds limits")
	}
	for _, p := range rec.Files {
		if !validText(p, 256) || filepath.IsAbs(p) || strings.Contains(p, "..") || strings.HasPrefix(p, "~") {
			return bad("unsafe file reference")
		}
	}
	for _, v := range rec.Entities {
		if !validText(v, 128) {
			return bad("invalid entity")
		}
	}
	return rec, nil
}
func validText(value string, limit int) bool {
	return strings.TrimSpace(value) != "" && len(value) <= limit && utf8.ValidString(value) && !strings.ContainsRune(value, 0)
}

// replayKey depends only on stable Host identities, never model-selected key,
// type or task metadata. Tombstones therefore survive rephrasing after reset.
func replayKey(e TrustedEvidence) string {
	parts := []string{"evidence", e.SourceID, e.EvidenceID}
	if e.VerifiedExecution {
		parts = []string{"execution", e.TaskID, e.ExecutionID}
	}
	raw, _ := json.Marshal(parts)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
