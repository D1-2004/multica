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
	"github.com/multica-ai/multica/server/internal/service/employeememory"
	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/pkg/redact"
)

// Intent is a durable "distill this verified Run" watermark.
type Intent struct {
	RunID, WorkspaceID, AgentID, TenantOrgID, SceneID string
	TaskID, QueueTaskID, RequesterRef, SpecDigest     string
	GoalRevision, SpecRevision                        int64
	LearningScope                                     LearningScope
	VerificationIDs                                   []string
	RunFinishedAt                                     time.Time
	State, LearningID, Reason                         string
}

// DistillOptions configures the background consumer.
type DistillOptions struct {
	Memory *employeememory.Store
	// Fence lets the Host add current-authority checks it owns (for example an
	// archived agent). A non-empty reason skips the intent permanently; an
	// error leaves it pending for retry.
	Fence func(ctx context.Context, tx pgx.Tx, intent Intent) (string, error)
	// afterDistill is a test seam that fails between the memory write and the
	// commit, proving the two commit or roll back together.
	afterDistill func() error
}

// DistillOutcome reports one consumed intent.
type DistillOutcome struct {
	RunID, State, LearningID, Reason string
}

const intentColumns = `run_id::text,workspace_id::text,agent_id::text,tenant_org_id,scene_id::text,task_id::text,queue_task_id::text,requester_ref,spec_digest,goal_revision,spec_revision,learning_scope,verification_ids,run_finished_at,state,COALESCE(learning_id::text,''),reason`

func scanIntent(row pgx.Row) (Intent, error) {
	var i Intent
	var raw []byte
	if err := row.Scan(&i.RunID, &i.WorkspaceID, &i.AgentID, &i.TenantOrgID, &i.SceneID, &i.TaskID, &i.QueueTaskID, &i.RequesterRef, &i.SpecDigest, &i.GoalRevision, &i.SpecRevision, &i.LearningScope, &raw, &i.RunFinishedAt, &i.State, &i.LearningID, &i.Reason); err != nil {
		return Intent{}, err
	}
	if err := json.Unmarshal(raw, &i.VerificationIDs); err != nil {
		return Intent{}, err
	}
	return i, nil
}

// ProcessVerifiedDistill consumes pending intents in bounded batches. It is
// safe on every replica: each intent is consumed under its Task and intent row
// locks, the learning and the receipt commit together, and replays keep the
// memory store's (task, run) replay identity. No model or network call is made.
func (s *Store) ProcessVerifiedDistill(ctx context.Context, limit int, opts DistillOptions) ([]DistillOutcome, error) {
	if s == nil || s.db == nil || opts.Memory == nil || limit < 1 || limit > 1000 {
		return nil, ErrInvalid
	}
	rows, err := s.db.Query(ctx, `SELECT run_id::text,spec_digest FROM employee_task_verified_distill WHERE state='pending' ORDER BY created_at,run_id LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	type key struct{ run, digest string }
	var keys []key
	for rows.Next() {
		var k key
		if err = rows.Scan(&k.run, &k.digest); err != nil {
			rows.Close()
			return nil, err
		}
		keys = append(keys, k)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	var out []DistillOutcome
	for _, k := range keys {
		outcome, consumed, err := s.consumeIntent(ctx, k.run, k.digest, opts)
		if err != nil {
			return out, err
		}
		if consumed {
			out = append(out, outcome)
		}
	}
	return out, nil
}

func (s *Store) consumeIntent(ctx context.Context, runID, digest string, opts DistillOptions) (DistillOutcome, bool, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return DistillOutcome{}, false, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	peek, err := scanIntent(tx.QueryRow(ctx, `SELECT `+intentColumns+` FROM employee_task_verified_distill WHERE run_id=$1::uuid AND spec_digest=$2`, runID, digest))
	if errors.Is(err, pgx.ErrNoRows) {
		return DistillOutcome{}, false, nil
	}
	if err != nil || peek.State != "pending" {
		return DistillOutcome{}, false, err
	}
	// Lock order matches verification write-back: workspace -> Task -> intent
	// -> memory namespace (inside DistillTx).
	if err = lockWorkspace(ctx, tx, peek.WorkspaceID); errors.Is(err, ErrNotFound) {
		return DistillOutcome{}, false, nil
	} else if err != nil {
		return DistillOutcome{}, false, err
	}
	scope := employeetask.Scope{WorkspaceID: peek.WorkspaceID, AgentID: peek.AgentID, TenantOrgID: peek.TenantOrgID, Kind: employeetask.ScopeScene}
	scope.Scene.SceneID = peek.SceneID
	task, taskErr := loadTask(ctx, tx, scope, peek.TaskID, true)
	if taskErr != nil && !errors.Is(taskErr, ErrNotFound) {
		return DistillOutcome{}, false, taskErr
	}
	intent, err := scanIntent(tx.QueryRow(ctx, `SELECT `+intentColumns+` FROM employee_task_verified_distill WHERE run_id=$1::uuid AND spec_digest=$2 FOR UPDATE`, runID, digest))
	if err != nil || intent.State != "pending" {
		return DistillOutcome{}, false, err
	}
	reason, learningID := "", ""
	var run employeememory.VerifiedRun
	var memoryScope employeememory.Scope
	switch {
	case errors.Is(taskErr, ErrNotFound):
		reason = "task_missing"
	case task.State == string(employeetask.StateCancelled):
		reason = "task_cancelled"
	case task.GoalRevision != intent.GoalRevision:
		reason = "stale_goal_revision"
	case task.RequesterRef != intent.RequesterRef:
		reason = "requester_changed"
	}
	if reason == "" {
		if err = tenantFence(ctx, tx, scope); errors.Is(err, ErrStaleTenant) {
			reason = "stale_tenant"
		} else if err != nil {
			return DistillOutcome{}, false, err
		}
	}
	if reason == "" {
		run, reason, err = rebuildVerifiedRun(ctx, tx, scope, task, intent)
		if err != nil {
			return DistillOutcome{}, false, err
		}
	}
	if reason == "" && opts.Fence != nil {
		if reason, err = opts.Fence(ctx, tx, intent); err != nil {
			return DistillOutcome{}, false, err
		}
	}
	if reason == "" {
		memoryScope, reason = learningScopeFor(task, intent)
	}
	if reason == "" {
		learning, err := opts.Memory.DistillTx(ctx, tx, memoryScope, run)
		switch {
		case err == nil:
			learningID = learning.ID
		case errors.Is(err, employeememory.ErrPreResetEvidence):
			reason = "pre_reset_evidence"
		case errors.Is(err, employeememory.ErrInvalidScope):
			reason = "stale_scope"
		case errors.Is(err, employeememory.ErrInvalidLearning), errors.Is(err, employeememory.ErrUnverified), errors.Is(err, employeememory.ErrUntrustedCorrection):
			reason = "rejected_learning"
		default:
			return DistillOutcome{}, false, err
		}
	}
	if opts.afterDistill != nil {
		if err = opts.afterDistill(); err != nil {
			return DistillOutcome{}, false, err
		}
	}
	state := "captured"
	if reason != "" {
		state, learningID = "skipped", ""
	}
	if _, err = tx.Exec(ctx, `UPDATE employee_task_verified_distill SET state=$3,learning_id=NULLIF($4,'')::uuid,reason=$5,consumed_at=now() WHERE run_id=$1::uuid AND spec_digest=$2 AND state='pending'`, runID, digest, state, learningID, reason); err != nil {
		return DistillOutcome{}, false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return DistillOutcome{}, false, err
	}
	return DistillOutcome{RunID: runID, State: state, LearningID: learningID, Reason: reason}, true, nil
}

// learningScopeFor keeps human Task learning requester-private. Automation
// sources never borrow a human namespace: they learn into the scene only when
// their configuration explicitly says so, otherwise nothing is captured.
func learningScopeFor(task taskRow, intent Intent) (employeememory.Scope, string) {
	workspace, err1 := util.ParseUUID(intent.WorkspaceID)
	agent, err2 := util.ParseUUID(intent.AgentID)
	if err1 != nil || err2 != nil {
		return employeememory.Scope{}, "invalid_scope"
	}
	scope := employeememory.Scope{WorkspaceID: workspace, AgentID: agent, TenantOrgID: intent.TenantOrgID}
	scope.Scene.SceneID = intent.SceneID
	if humanOriginTask(task.SourceNamespace) {
		if intent.LearningScope != LearningScopeDefault {
			return employeememory.Scope{}, "invalid_scope"
		}
		scope.Kind, scope.PrincipalID = employeememory.ScopePrivate, intent.RequesterRef
		return scope, ""
	}
	if intent.LearningScope == LearningScopeScene {
		scope.Kind = employeememory.ScopeScene
		return scope, ""
	}
	return employeememory.Scope{}, "automation_scope_unconfigured"
}

// rebuildVerifiedRun reconstructs the Host VerifiedRun from retained records,
// never from the intent row's text or any model output.
func rebuildVerifiedRun(ctx context.Context, tx pgx.Tx, scope employeetask.Scope, task taskRow, intent Intent) (employeememory.VerifiedRun, string, error) {
	spec, err := currentSpec(ctx, tx, scope, task.ID)
	if errors.Is(err, ErrNoSpec) {
		return employeememory.VerifiedRun{}, "spec_changed", nil
	}
	if err != nil {
		return employeememory.VerifiedRun{}, "", err
	}
	if spec.Digest != intent.SpecDigest || spec.State != SpecActive {
		return employeememory.VerifiedRun{}, "spec_changed", nil
	}
	records, err := runRecords(ctx, tx, scope, task.ID, intent.RunID)
	if err != nil {
		return employeememory.VerifiedRun{}, "", err
	}
	byID := map[string]Record{}
	for _, r := range records {
		byID[r.ID] = r
	}
	gate := gateFor(spec, records)
	if gate.Status != GatePassed || !gate.Correct || len(intent.VerificationIDs) == 0 {
		return employeememory.VerifiedRun{}, "verification_mismatch", nil
	}
	var used []Record
	for _, id := range intent.VerificationIDs {
		r, ok := byID[id]
		if !ok || r.Outcome != OutcomePassed || r.RunID != intent.RunID || r.TaskID != task.ID {
			return employeememory.VerifiedRun{}, "verification_mismatch", nil
		}
		used = append(used, r)
	}
	var conditions, proofs []string
	kinds := map[string]bool{}
	for _, r := range used {
		if !correctnessKind(r.CheckKind) {
			continue
		}
		kinds[string(r.CheckKind)] = true
		conditions = append(conditions, describeCheck(r.Check))
		proofs = append(proofs, r.Detail)
	}
	if len(conditions) == 0 {
		return employeememory.VerifiedRun{}, "verification_mismatch", nil
	}
	sort.Strings(conditions)
	sort.Strings(proofs)
	kindList := make([]string, 0, len(kinds))
	for k := range kinds {
		kindList = append(kindList, k)
	}
	sort.Strings(kindList)
	title := clip(strings.Join(strings.Fields(redact.Text(task.Goal)), " "), 400)
	if title == "" {
		title = "employee task " + task.ID
	}
	return employeememory.VerifiedRun{
		TaskID: task.ID, ExecutionID: intent.RunID, Title: title,
		// The memory insight keeps only the tail of Details, so the conditions
		// stay short enough to survive intact.
		Details:   "Host-verified conditions: " + clip(strings.Join(conditions, "; "), 340) + ".",
		ProofKind: clip("host:"+strings.Join(kindList, "+"), 80),
		Proof:     clip(redact.Text(strings.Join(proofs, "; ")), 1800),
		ActorID:   "system:employee-verification",
		// The evidence identity names the verification set; the memory replay
		// identity stays (task, run), so a re-verified run cannot add a second
		// learning.
		EvidenceID: "employee-verification:" + intent.RunID + ":" + intent.SpecDigest[:16],
		Passed:     true,
		OccurredAt: intent.RunFinishedAt,
	}, "", nil
}

// describeCheck renders the verified condition deterministically so the
// learning states exactly which conditions the verification covered.
func describeCheck(c Check) string {
	switch c.Kind {
	case KindArtifactContents:
		parts := []string{c.File + " produced"}
		if len(c.Contains) > 0 {
			parts = append(parts, "contains "+quoteList(c.Contains))
		}
		if c.DataRows != nil {
			parts = append(parts, fmt.Sprintf("%d data rows", *c.DataRows))
		}
		if len(c.Columns) > 0 {
			parts = append(parts, "columns "+quoteList(c.Columns))
		}
		if c.SHA256 != "" {
			parts = append(parts, "exact bytes sha256:"+c.SHA256[:12])
		}
		return strings.Join(parts, ", ")
	case KindExecutionOutput:
		return fmt.Sprintf("%s %sequals %s", c.File, fieldLabel(c.Field), clip(c.Expect, 80))
	default:
		return string(c.Kind)
	}
}
