package employeeverification

import (
	"context"
	"errors"

	"github.com/multica-ai/multica/server/internal/employeetask"
)

// PendingOutcome reports one discovered Run after a verification attempt.
type PendingOutcome struct {
	TaskID, RunID string
	Gate          GateStatus
	Intent        bool
	// Err is set when the attempt was fenced or failed; the Run is retried on
	// a later pass unless its fence is permanent (cancelled, older goal).
	Err error
}

// ProcessPending is the durable verification trigger: it discovers succeeded
// Runs of the current goal whose Task has an active spec and no committed
// attempt of the current evidence generation under the current spec digest,
// and verifies each. A crash between a Run's terminal commit and its
// verification therefore only delays it. A Run whose provider sends are still
// being confirmed is deferred (ErrEvidencePending) and retried. Once attempted,
// a Run is not rediscovered until its spec or the evidence generation changes.
func (v *Verifier) ProcessPending(ctx context.Context, limit int) ([]PendingOutcome, error) {
	if v == nil || v.DB == nil || limit < 1 || limit > 500 {
		return nil, ErrInvalid
	}
	rows, err := v.DB.Query(ctx, `WITH current_spec AS (
  SELECT DISTINCT ON (task_id) task_id,workspace_id,agent_id,tenant_org_id,spec_digest,state
  FROM employee_task_verification_spec WHERE updated_at > now() - interval '30 days'
  ORDER BY task_id,revision DESC)
SELECT t.workspace_id::text,t.agent_id::text,t.tenant_org_id,t.scene_id::text,t.id::text,r.id::text
FROM current_spec s
JOIN employee_task t ON t.id=s.task_id AND t.workspace_id=s.workspace_id AND t.agent_id=s.agent_id AND t.tenant_org_id=s.tenant_org_id
JOIN employee_task_run r ON r.task_id=t.id AND r.workspace_id=t.workspace_id AND r.agent_id=t.agent_id AND r.tenant_org_id=t.tenant_org_id
WHERE s.state='active' AND t.owner_loop='employee' AND t.scope_kind='scene' AND t.state<>'cancelled'
  AND r.state='succeeded' AND r.goal_revision=t.goal_revision AND r.finished_at > now() - interval '7 days'
  AND NOT EXISTS (SELECT 1 FROM employee_task_verification_attempt a WHERE a.run_id=r.id AND a.spec_digest=s.spec_digest AND a.evidence_generation>=$2)
ORDER BY r.finished_at,r.id LIMIT $1`, limit, EvidenceGeneration)
	if err != nil {
		return nil, err
	}
	type candidate struct {
		scope       employeetask.Scope
		task, runID string
	}
	var candidates []candidate
	for rows.Next() {
		var c candidate
		c.scope.Kind = employeetask.ScopeScene
		if err = rows.Scan(&c.scope.WorkspaceID, &c.scope.AgentID, &c.scope.TenantOrgID, &c.scope.Scene.SceneID, &c.task, &c.runID); err != nil {
			rows.Close()
			return nil, err
		}
		candidates = append(candidates, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	out := make([]PendingOutcome, 0, len(candidates))
	for _, c := range candidates {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		result, err := v.VerifyRun(ctx, c.scope, c.task, c.runID)
		out = append(out, PendingOutcome{TaskID: c.task, RunID: c.runID, Gate: result.Gate.Status, Intent: result.Intent, Err: err})
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return out, err
		}
	}
	return out, nil
}
