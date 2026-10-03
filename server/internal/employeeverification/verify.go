package employeeverification

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/employeetask"
	"github.com/multica-ai/multica/server/internal/scene"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Verifier runs the deterministic checks of a Task's active spec against one
// succeeded Run: locked snapshot -> unlocked evidence I/O and checking ->
// locked write-back that re-validates every fence. A spec or evidence change
// in between discards the result.
type Verifier struct {
	DB       DB
	Evidence EvidenceSource
	// CheckerVersion defaults to the package CheckerVersion.
	CheckerVersion string
	// TenantFence lets the Host add its current agent/tenant binding check
	// (identity org or created tenant). It runs in the snapshot and again in
	// the locked write-back; an error discards the result.
	TenantFence func(ctx context.Context, q Querier, scope employeetask.Scope) error
}

// MaxVerifiedArtifactBytes bounds the bytes read for one artifact. Larger
// artifacts are not read and their content checks stay unknown.
const MaxVerifiedArtifactBytes = 16 << 20

// RunResult is the committed state after VerifyRun.
type RunResult struct {
	Gate    Gate     `json:"gate"`
	Records []Record `json:"records,omitempty"`
	// Intent is true when a distill intent exists for this Run and spec.
	Intent bool `json:"intent"`
}

type runRow struct {
	ID, QueueTaskID, State string
	GoalRevision           int64
	FinishedAt             *time.Time
}

type snapshot struct {
	task      taskRow
	run       runRow
	spec      Spec
	evidence  runEvidence
	delivery  string
	delivered DeliveredSet
}

// EvidenceGeneration is recorded per verified (run, spec): generation 2 reads
// provider-delivered files. Runs verified only by an older generation are
// rediscovered once, so a replica that could not see deliveries never leaves
// a Run judged without them.
const EvidenceGeneration = 2

// VerifyRun is idempotent: concurrent or repeated calls over the same
// evidence converge on one record per (run, check, evidence) and at most one
// distill intent per (run, spec).
func (v *Verifier) VerifyRun(ctx context.Context, scope employeetask.Scope, taskID, runID string) (RunResult, error) {
	if v == nil || v.DB == nil || v.Evidence == nil || !validScope(scope) || !validUUID(taskID) || !validUUID(runID) {
		return RunResult{}, ErrInvalid
	}
	version := v.CheckerVersion
	if version == "" {
		version = CheckerVersion
	}
	snap, err := v.snapshot(ctx, scope, taskID, runID)
	if errors.Is(err, ErrNoSpec) {
		return RunResult{Gate: Gate{Status: GateNone}}, nil
	}
	if err != nil {
		return RunResult{}, err
	}
	if snap.spec.State != SpecActive {
		return RunResult{Gate: Gate{Status: GateNone}}, nil
	}
	// Object-store reads happen with no transaction or row lock held.
	snap.evidence.Bytes = map[string][]byte{}
	wanted := map[string]bool{}
	for _, c := range snap.spec.Checks {
		if c.File != "" {
			wanted[c.File] = true
		}
	}
	for _, a := range snap.evidence.Artifacts {
		if !wanted[a.Filename] || a.State != "ready" || a.Size > MaxVerifiedArtifactBytes {
			continue
		}
		data, err := v.Evidence.ReadArtifact(ctx, a)
		if err != nil {
			return RunResult{}, fmt.Errorf("read artifact %s: %w", a.AttachmentID, err)
		}
		snap.evidence.Bytes[a.AttachmentID] = data
	}
	if files, ok := v.Evidence.(DeliveredFileSource); ok && len(wanted) > 0 {
		for _, m := range snap.delivered.Messages {
			read, err := files.ReadDeliveredFiles(ctx, scope, taskID, runID, snap.run.QueueTaskID, m, MaxVerifiedArtifactBytes)
			if err != nil {
				return RunResult{}, fmt.Errorf("read delivered message %s: %w", m.MessageID, err)
			}
			for _, f := range read {
				if f.Unreadable != "" || f.FileID == "" {
					snap.evidence.Unreadable = append(snap.evidence.Unreadable, clip(m.MessageID+": "+f.Unreadable, 160))
					continue
				}
				ref := "dws-message-file:" + m.MessageID + "/" + f.FileID
				a := Artifact{Ref: ref, AttachmentID: ref, TaskID: snap.task.ID, RunID: snap.run.ID, QueueTaskID: snap.run.QueueTaskID, GoalRevision: snap.run.GoalRevision,
					Filename: f.Name, SHA256: sha256Hex(f.Data), Size: int64(len(f.Data)), State: "ready"}
				snap.evidence.Delivered = append(snap.evidence.Delivered, a)
				snap.evidence.Bytes[ref] = f.Data
			}
		}
	}
	var observations []observation
	for _, c := range snap.spec.Checks {
		observations = append(observations, evaluate(c, snap.evidence)...)
	}
	return v.writeback(ctx, scope, snap, observations, version)
}

func loadRun(ctx context.Context, q Querier, scope employeetask.Scope, taskID, runID string) (runRow, error) {
	var r runRow
	err := q.QueryRow(ctx, `SELECT id::text,queue_task_id::text,state,goal_revision,finished_at FROM employee_task_run WHERE workspace_id=$1::uuid AND agent_id=$2::uuid AND tenant_org_id=$3 AND task_id=$4::uuid AND id=$5::uuid`,
		scope.WorkspaceID, scope.AgentID, scope.TenantOrgID, taskID, runID).Scan(&r.ID, &r.QueueTaskID, &r.State, &r.GoalRevision, &r.FinishedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return runRow{}, ErrNotFound
	}
	return r, err
}

// fence applies the use-time checks shared by snapshot and write-back.
func fence(ctx context.Context, q Querier, scope employeetask.Scope, task taskRow, run runRow) error {
	if task.State == string(employeetask.StateCancelled) {
		return ErrTaskCancelled
	}
	if run.State != string(employeetask.StateSucceeded) || run.FinishedAt == nil {
		return ErrNotVerifiable
	}
	if run.GoalRevision != task.GoalRevision {
		return ErrStaleGoal
	}
	return tenantFence(ctx, q, scope)
}

func tenantFence(ctx context.Context, q Querier, scope employeetask.Scope) error {
	dbtx, ok := q.(db.DBTX)
	if !ok {
		return ErrInvalid
	}
	workspace, err1 := scene.ParseID(scope.WorkspaceID)
	agent, err2 := scene.ParseID(scope.AgentID)
	id, err3 := scene.ParseID(scope.Scene.SceneID)
	if err1 != nil || err2 != nil || err3 != nil {
		return ErrInvalid
	}
	row, err := scene.Get(ctx, db.New(dbtx), scene.Owner{WorkspaceID: workspace, AgentID: agent}, id)
	if errors.Is(err, scene.ErrNotFound) {
		return ErrStaleTenant
	}
	if err != nil {
		return err
	}
	if scene.CheckTenant(row, scope.TenantOrgID) != nil {
		return ErrStaleTenant
	}
	return nil
}

func (v *Verifier) snapshot(ctx context.Context, scope employeetask.Scope, taskID, runID string) (snapshot, error) {
	tx, err := v.DB.Begin(ctx)
	if err != nil {
		return snapshot{}, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	var s snapshot
	if s.task, err = loadTask(ctx, tx, scope, taskID, false); err != nil {
		return snapshot{}, err
	}
	if s.run, err = loadRun(ctx, tx, scope, taskID, runID); err != nil {
		return snapshot{}, err
	}
	if err = fence(ctx, tx, scope, s.task, s.run); err != nil {
		return snapshot{}, err
	}
	if v.TenantFence != nil {
		if err = v.TenantFence(ctx, tx, scope); err != nil {
			return snapshot{}, err
		}
	}
	if s.spec, err = currentSpec(ctx, tx, scope, taskID); err != nil {
		return snapshot{}, err
	}
	if s.evidence, s.delivery, err = v.readEvidence(ctx, tx, scope, s); err != nil {
		return snapshot{}, err
	}
	if s.delivered, err = v.readDelivered(ctx, tx, scope, s); err != nil {
		return snapshot{}, err
	}
	if s.delivered.Pending {
		return snapshot{}, ErrEvidencePending
	}
	s.evidence.DeliveredDigest = s.delivered.digest()
	return s, nil
}

// readDelivered reads the delivered receipt set when a check names a file and
// the evidence source can read provider deliveries.
func (v *Verifier) readDelivered(ctx context.Context, q Querier, scope employeetask.Scope, s snapshot) (DeliveredSet, error) {
	files, ok := v.Evidence.(DeliveredFileSource)
	if !ok {
		return DeliveredSet{}, nil
	}
	for _, c := range s.spec.Checks {
		if c.File != "" {
			return files.RunDeliveredMessages(ctx, q, scope, s.task.ID, s.run.ID)
		}
	}
	return DeliveredSet{}, nil
}

func (v *Verifier) readEvidence(ctx context.Context, q Querier, scope employeetask.Scope, s snapshot) (runEvidence, string, error) {
	e := runEvidence{RunID: s.run.ID, TaskID: s.task.ID, QueueTaskID: s.run.QueueTaskID, GoalRevision: s.run.GoalRevision}
	var err error
	if e.Artifacts, err = v.Evidence.RunArtifacts(ctx, q, scope, s.task.ID, s.run.ID); err != nil {
		return runEvidence{}, "", err
	}
	needsDelivery := false
	for _, c := range s.spec.Checks {
		needsDelivery = needsDelivery || c.Kind == KindDeliveryReceipt
	}
	if needsDelivery {
		if e.Deliveries, err = v.Evidence.RunDeliveries(ctx, q, scope, s.task.ID, s.run.ID); err != nil {
			return runEvidence{}, "", err
		}
	}
	lines := make([]string, 0, len(e.Deliveries))
	for _, d := range e.Deliveries {
		lines = append(lines, d.ActionID+"|"+d.State+"|"+d.ConversationID+"|"+d.MessageID)
	}
	sort.Strings(lines)
	return e, sha256Hex([]byte(strings.Join(lines, "\n"))), nil
}

const recordColumns = `id::text,task_id::text,run_id::text,queue_task_id::text,goal_revision,requester_ref,spec_revision,spec_digest,check_id,check_kind,check_spec,evidence_ref,evidence_sha256,checker_version,outcome,detail,run_finished_at,completed_at`

func scanRecord(row pgx.Row) (Record, error) {
	var r Record
	var raw []byte
	if err := row.Scan(&r.ID, &r.TaskID, &r.RunID, &r.QueueTaskID, &r.GoalRevision, &r.RequesterRef, &r.SpecRevision, &r.SpecDigest, &r.CheckID, &r.CheckKind, &raw, &r.EvidenceRef, &r.EvidenceSHA256, &r.CheckerVersion, &r.Outcome, &r.Detail, &r.RunFinishedAt, &r.CompletedAt); err != nil {
		return Record{}, err
	}
	if err := json.Unmarshal(raw, &r.Check); err != nil {
		return Record{}, err
	}
	return r, nil
}

func (v *Verifier) writeback(ctx context.Context, scope employeetask.Scope, snap snapshot, observations []observation, version string) (RunResult, error) {
	tx, err := v.DB.Begin(ctx)
	if err != nil {
		return RunResult{}, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	if err = lockWorkspace(ctx, tx, scope.WorkspaceID); err != nil {
		return RunResult{}, err
	}
	task, err := loadTask(ctx, tx, scope, snap.task.ID, true)
	if err != nil {
		return RunResult{}, err
	}
	if task.RequesterRef != snap.task.RequesterRef {
		return RunResult{}, ErrNotRequester
	}
	if task.GoalRevision != snap.task.GoalRevision {
		return RunResult{}, ErrStaleGoal
	}
	run, err := loadRun(ctx, tx, scope, snap.task.ID, snap.run.ID)
	if err != nil {
		return RunResult{}, err
	}
	if err = fence(ctx, tx, scope, task, run); err != nil {
		return RunResult{}, err
	}
	if v.TenantFence != nil {
		if err = v.TenantFence(ctx, tx, scope); err != nil {
			return RunResult{}, err
		}
	}
	spec, err := currentSpec(ctx, tx, scope, snap.task.ID)
	if err != nil && !errors.Is(err, ErrNoSpec) {
		return RunResult{}, err
	}
	if err != nil || spec.Revision != snap.spec.Revision || spec.Digest != snap.spec.Digest || spec.State != SpecActive {
		return RunResult{}, ErrSpecChanged
	}
	current := snapshot{task: task, run: run, spec: spec}
	evidence, delivery, err := v.readEvidence(ctx, tx, scope, current)
	if err != nil {
		return RunResult{}, err
	}
	delivered, err := v.readDelivered(ctx, tx, scope, current)
	if err != nil {
		return RunResult{}, err
	}
	if evidence.hostManifestDigest() != snap.evidence.hostManifestDigest() || delivery != snap.delivery || delivered.Pending || delivered.digest() != snap.delivered.digest() {
		return RunResult{}, ErrEvidence
	}
	written := make([]Record, 0, len(observations))
	for _, o := range observations {
		r, err := insertRecord(ctx, tx, scope, snap, o, version)
		if err != nil {
			return RunResult{}, err
		}
		written = append(written, r)
	}
	all, err := runRecords(ctx, tx, scope, snap.task.ID, snap.run.ID)
	if err != nil {
		return RunResult{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO employee_task_verification_attempt(run_id,workspace_id,spec_digest,evidence_generation) VALUES($1::uuid,$2::uuid,$3,$4)
ON CONFLICT (run_id,spec_digest) DO UPDATE SET evidence_generation=GREATEST(employee_task_verification_attempt.evidence_generation,EXCLUDED.evidence_generation),attempted_at=now()`,
		snap.run.ID, scope.WorkspaceID, snap.spec.Digest, EvidenceGeneration); err != nil {
		return RunResult{}, err
	}
	gate := gateFor(spec, all)
	result := RunResult{Gate: gate, Records: written}
	if gate.Status == GatePassed && gate.Correct {
		if err = insertIntent(ctx, tx, scope, snap, spec, all); err != nil {
			return RunResult{}, err
		}
		result.Intent = true
	}
	return result, tx.Commit(ctx)
}

// insertRecord never overwrites: the same (run, check, evidence) returns the
// retained record only when checker version, evidence hash and outcome agree.
func insertRecord(ctx context.Context, tx pgx.Tx, scope employeetask.Scope, snap snapshot, o observation, version string) (Record, error) {
	checkJSON, err := json.Marshal(o.Check)
	if err != nil {
		return Record{}, err
	}
	checkID := o.Check.ID
	if !checkIDFormat.MatchString(checkID) {
		checkID = checkIDPrefix + sha256Hex(checkJSON)[:checkIDHexLength]
	}
	kind := string(o.Check.Kind)
	if kind == "" || len(kind) > 64 {
		kind = "unknown"
	}
	r, err := scanRecord(tx.QueryRow(ctx, `INSERT INTO employee_task_verification(workspace_id,agent_id,tenant_org_id,scene_id,task_id,run_id,queue_task_id,goal_revision,requester_ref,spec_revision,spec_digest,check_id,check_kind,check_spec,evidence_ref,evidence_sha256,checker_version,outcome,detail,run_finished_at)
VALUES($1::uuid,$2::uuid,$3,$4::uuid,$5::uuid,$6::uuid,$7::uuid,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20)
ON CONFLICT (run_id,check_id,evidence_ref) DO NOTHING RETURNING `+recordColumns,
		scope.WorkspaceID, scope.AgentID, scope.TenantOrgID, scope.Scene.SceneID, snap.task.ID, snap.run.ID, snap.run.QueueTaskID, snap.run.GoalRevision, snap.task.RequesterRef,
		snap.spec.Revision, snap.spec.Digest, checkID, kind, checkJSON, clip(o.EvidenceRef, 500), o.EvidenceSHA256, version, o.Outcome, clip(o.Detail, 3900), *snap.run.FinishedAt))
	if err == nil {
		return r, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Record{}, err
	}
	existing, err := scanRecord(tx.QueryRow(ctx, `SELECT `+recordColumns+` FROM employee_task_verification WHERE run_id=$1::uuid AND check_id=$2 AND evidence_ref=$3`, snap.run.ID, checkID, clip(o.EvidenceRef, 500)))
	if err != nil {
		return Record{}, err
	}
	if existing.CheckerVersion != version || existing.EvidenceSHA256 != o.EvidenceSHA256 || existing.Outcome != o.Outcome || existing.TaskID != snap.task.ID {
		return Record{}, fmt.Errorf("%w: retained %s result for %s differs (checker %s, evidence %s)", ErrConflict, existing.Outcome, existing.EvidenceRef, existing.CheckerVersion, existing.EvidenceSHA256[:12])
	}
	return existing, nil
}

func runRecords(ctx context.Context, q Querier, scope employeetask.Scope, taskID, runID string) ([]Record, error) {
	rows, err := q.Query(ctx, `SELECT `+recordColumns+` FROM employee_task_verification WHERE workspace_id=$1::uuid AND agent_id=$2::uuid AND tenant_org_id=$3 AND task_id=$4::uuid AND run_id=$5::uuid ORDER BY completed_at,id`,
		scope.WorkspaceID, scope.AgentID, scope.TenantOrgID, taskID, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Record
	for rows.Next() {
		r, err := scanRecord(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// insertIntent is written in the same transaction as the passing records, so
// a crash after commit leaves a durable, discoverable intent.
func insertIntent(ctx context.Context, tx pgx.Tx, scope employeetask.Scope, snap snapshot, spec Spec, records []Record) error {
	required := map[string]bool{}
	for _, c := range spec.Checks {
		if !c.Optional {
			required[c.ID] = true
		}
	}
	ids := []string{}
	for _, r := range records {
		if required[r.CheckID] && r.Outcome == OutcomePassed {
			ids = append(ids, r.ID)
		}
	}
	sort.Strings(ids)
	raw, err := json.Marshal(ids)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO employee_task_verified_distill(run_id,workspace_id,agent_id,tenant_org_id,scene_id,task_id,queue_task_id,goal_revision,requester_ref,spec_revision,spec_digest,learning_scope,verification_ids,run_finished_at)
VALUES($1::uuid,$2::uuid,$3::uuid,$4,$5::uuid,$6::uuid,$7::uuid,$8,$9,$10,$11,$12,$13,$14) ON CONFLICT (run_id,spec_digest) DO NOTHING`,
		snap.run.ID, scope.WorkspaceID, scope.AgentID, scope.TenantOrgID, scope.Scene.SceneID, snap.task.ID, snap.run.QueueTaskID, snap.run.GoalRevision, snap.task.RequesterRef,
		spec.Revision, spec.Digest, spec.LearningScope, raw, *snap.run.FinishedAt)
	return err
}

// GateTx reports the Run's verification state against the Task's current
// spec inside the caller's transaction (for P1 CompleteGoalTx). GateNone means
// no active contract.
func GateTx(ctx context.Context, q Querier, scope employeetask.Scope, taskID, runID string) (Gate, error) {
	if q == nil || !validUUID(runID) {
		return Gate{}, ErrInvalid
	}
	if _, err := loadTask(ctx, q, scope, taskID, false); err != nil {
		return Gate{}, err
	}
	spec, err := currentSpec(ctx, q, scope, taskID)
	if errors.Is(err, ErrNoSpec) {
		return Gate{Status: GateNone}, nil
	}
	if err != nil {
		return Gate{}, err
	}
	records, err := runRecords(ctx, q, scope, taskID, runID)
	if err != nil {
		return Gate{}, err
	}
	return gateFor(spec, records), nil
}

// Feedback returns the retained failed and unknown results of the most
// recently verified Run of the Task's current goal revision, for the next
// wake's work packet. Results are kept for audit; nothing here deletes them.
func (s *Store) Feedback(ctx context.Context, scope employeetask.Scope, taskID string, limit int) ([]Record, error) {
	if s == nil || s.db == nil {
		return nil, ErrInvalid
	}
	if limit <= 0 || limit > 32 {
		limit = 8
	}
	task, err := loadTask(ctx, s.db, scope, taskID, false)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, `WITH latest AS (
  SELECT run_id FROM employee_task_verification WHERE workspace_id=$1::uuid AND agent_id=$2::uuid AND tenant_org_id=$3 AND task_id=$4::uuid AND goal_revision=$5 ORDER BY completed_at DESC,id DESC LIMIT 1)
SELECT `+recordColumns+` FROM employee_task_verification WHERE workspace_id=$1::uuid AND agent_id=$2::uuid AND tenant_org_id=$3 AND task_id=$4::uuid AND goal_revision=$5
  AND run_id=(SELECT run_id FROM latest) AND outcome IN ('failed','unknown') ORDER BY completed_at DESC,id DESC LIMIT $6`,
		scope.WorkspaceID, scope.AgentID, scope.TenantOrgID, taskID, task.GoalRevision, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Record
	for rows.Next() {
		r, err := scanRecord(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

const (
	feedbackOpen  = "== HOST VERIFICATION (deterministic checks of produced evidence; not model claims) =="
	feedbackClose = "== END HOST VERIFICATION =="
)

// FormatFeedback renders retained results as bounded background data for the
// next wake. Details are Host text over evidence, neutralized against
// delimiter injection; the block grants no permission.
func FormatFeedback(records []Record) string {
	if len(records) == 0 {
		return ""
	}
	lines := []string{feedbackOpen}
	for _, r := range records {
		detail := strings.Join(strings.Fields(r.Detail), " ")
		for _, marker := range []string{feedbackOpen, feedbackClose, "== HOST VERIFICATION"} {
			detail = strings.ReplaceAll(detail, marker, "[verification marker]")
		}
		lines = append(lines, fmt.Sprintf("- %s %s (%s; evidence %s; checker %s): %s", strings.ToUpper(string(r.Outcome)), r.CheckKind, r.CheckID, clip(r.EvidenceRef, 120), r.CheckerVersion, clip(detail, 400)))
	}
	lines = append(lines, "Only new evidence from a new run changes these results; saying the work is done does not.", feedbackClose)
	return strings.Join(lines, "\n")
}
