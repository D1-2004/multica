package handler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/employeeentry"
	"github.com/multica-ai/multica/server/internal/langfuse"
	"github.com/multica-ai/multica/server/internal/modelregistry"
	"github.com/multica-ai/multica/server/internal/scene"
	"github.com/multica-ai/multica/server/internal/service/employeeloop"
	"github.com/multica-ai/multica/server/internal/service/employeememory/digest"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/shared"
)

// Host adapters for the Employee scene digest writer (memory design M11) and
// the deterministic scene ledger. Timing: _shared/memory-flush-timing.md.

// NewEmployeeMemoryDigest wires the writer to this Handler's database, the
// shared Coordinator model chain (no new provider) and the use-time fence.
// transcript is M8's persisted group transcript reader and facts is M5's
// scene fact store; ready must require every live replica to advertise
// digest.MemoryMarker. A nil dependency leaves the writer disabled.
func NewEmployeeMemoryDigest(h *Handler, routes employeeModelRoutes, client *langfuse.Client, transcript digest.Transcript, facts digest.FactStore, ready func(context.Context) error) *digest.Writer {
	database, ok := employeeEntryDB(h)
	if !ok || routes == nil {
		return nil
	}
	return &digest.Writer{DB: database, Transcript: transcript, Facts: facts, Model: employeeDigestModel{routes: routes}, Fence: employeeDigestFence, Ready: ready, Langfuse: client}
}

// runEmployeeMemoryDigest drains due scenes and runs zero-model maintenance.
// It is a separate goroutine so a model call never delays the
// reconciliation tick.
func runEmployeeMemoryDigest(ctx context.Context, writer *digest.Writer) {
	if !writer.Enabled() {
		return
	}
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	var lastMaintain time.Time
	for {
		for ctx.Err() == nil {
			worked, err := writer.ProcessNext(ctx)
			if err != nil && !errors.Is(err, context.Canceled) {
				slog.WarnContext(ctx, "employee scene digest failed", "error", err)
			}
			if !worked || err != nil {
				break
			}
		}
		if time.Since(lastMaintain) >= time.Minute {
			lastMaintain = time.Now()
			maintainCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			if _, err := writer.Maintain(maintainCtx, 20); err != nil && !errors.Is(err, context.Canceled) {
				slog.WarnContext(ctx, "employee scene digest maintenance failed", "error", err)
			}
			cancel()
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// employeeDigestFence re-validates the writer's authority at read and commit
// time: the agent still exists, is not archived and still runs in employee
// mode, and the scene is still a group/dm scene of the recorded tenant.
func employeeDigestFence(ctx context.Context, tx pgx.Tx, key digest.SceneKey) (string, error) {
	var mode string
	var archived *time.Time
	err := tx.QueryRow(ctx, `SELECT coordination_mode,archived_at FROM agent WHERE workspace_id=$1::uuid AND id=$2::uuid`, key.WorkspaceID, key.AgentID).Scan(&mode, &archived)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return "agent_missing", nil
	case err != nil:
		return "", err
	case archived != nil:
		return "agent_archived", nil
	case mode != "employee":
		return "agent_not_employee", nil
	}
	sceneID, err := scene.ParseID(key.SceneID)
	if err != nil {
		return "scene_invalid", nil
	}
	row, err := scene.Get(ctx, db.New(tx), scene.Owner{WorkspaceID: parseUUID(key.WorkspaceID), AgentID: parseUUID(key.AgentID)}, sceneID)
	if errors.Is(err, scene.ErrNotFound) {
		return "scene_missing", nil
	}
	if err != nil {
		return "", err
	}
	if scene.CheckTenant(row, key.TenantOrgID) != nil {
		return "tenant_revoked", nil
	}
	if row.SceneKind != scene.KindGroup && row.SceneKind != scene.KindDM {
		return "scene_kind_not_shared", nil
	}
	return "", nil
}

// employeeDigestModel sends one request through the first available
// candidate of the shared Coordinator chain, with the Employee fast request
// profile (no thinking). One real request per call: the digest budget is
// charged per call before I/O, so there is no fallback after a request.
type employeeDigestModel struct{ routes employeeModelRoutes }

func (m employeeDigestModel) Chat(ctx context.Context, params openai.ChatCompletionNewParams) (*openai.ChatCompletion, string, error) {
	plan, err := m.routes.CoordinatorPlan(ctx)
	if err != nil {
		return nil, "", err
	}
	var unavailable error
	for index, ref := range plan.Candidates {
		attempt, err := m.routes.PrepareCoordinatorAttempt(ctx, plan, index)
		if errors.Is(err, modelregistry.ErrCandidateUnavailable) {
			unavailable = errors.Join(unavailable, err)
			continue
		}
		if err != nil {
			return nil, ref.String(), err
		}
		request := params
		request.Model = ref.Model
		request.ReasoningEffort = shared.ReasoningEffortNone
		extra := map[string]any{"tool_choice": "required", "enable_thinking": false}
		model := strings.ToLower(ref.Model)
		if strings.Contains(model, "deepseek") && strings.Contains(model, "flash") {
			request.ReasoningEffort = ""
			extra["thinking"] = map[string]any{"type": "disabled"}
		}
		request.SetExtraFields(extra)
		out, err := attempt.Chat(ctx, request)
		return out, ref.String(), err
	}
	return nil, "", errors.Join(errors.New("employee digest: no coordinator model candidate is available"), unavailable)
}

// employeeWakeLedgerRequests lists who asked what in a chat wake window.
func employeeWakeLedgerRequests(envelopes []employeeDispatchEnvelope) []digest.LedgerRequest {
	var out []digest.LedgerRequest
	for _, env := range envelopes {
		org := dispatchRecordedOrg(env.Command)
		for _, m := range env.Command.Event.Data.Messages {
			out = append(out, digest.LedgerRequest{RequesterRef: employeeRequesterRef(org, m), SpeakerName: m.SenderDisplayName, MessageID: m.OpenMsgID, Text: m.Text})
		}
	}
	return out
}

// employeeLedgerFailure maps a saved failure to a category; raw provider
// text never enters the ledger.
func employeeLedgerFailure(failure string) string {
	lower := strings.ToLower(failure)
	switch {
	case failure == "":
		return ""
	case strings.Contains(lower, "deadline") || strings.Contains(lower, "timeout") || strings.Contains(lower, "timed out"):
		return "model_timeout"
	case strings.Contains(lower, "budget") || strings.Contains(lower, "model call"):
		return "model_budget"
	case failure == errEmployeeWindowTooLarge.Error():
		return "window_too_large"
	case strings.Contains(lower, "no longer served"):
		return "tenant_revoked"
	case strings.Contains(lower, "tool"):
		return "tool_rejected"
	default:
		return "error"
	}
}

// employeeWakeLedgerEntry assembles one wake's journal entry from Host facts
// only (GawkBot task_ledger.go): the frozen outcome, tool outcomes and the
// committed send action ids, never a model self-summary.
func employeeWakeLedgerEntry(job employeeentry.Job, requests []digest.LedgerRequest, wakeKind string, saved employeeSavedOutcome, actionIDs []string) digest.LedgerEntry {
	outcome := saved.Outcome
	body := digest.LedgerBody{JobID: job.ID, JobKind: job.Kind, WakeKind: wakeKind, Requests: requests, Outcome: string(outcome.Kind), Failure: employeeLedgerFailure(saved.Failure), Rescue: saved.Rescue, SendActionIDs: actionIDs}
	if body.Outcome == "" {
		body.Outcome = string(employeeloop.Quiet)
	}
	if outcome.Kind != employeeloop.Quiet {
		body.Reply = strings.TrimSpace(outcome.Reply)
	}
	executed := map[string]employeeloop.ToolOutcome{}
	for _, effect := range outcome.ToolOutcomes {
		executed[effect.NativeToolCallID] = effect
	}
	for _, entry := range outcome.Entries {
		if entry.Message == nil {
			continue
		}
		for _, call := range entry.Message.ToolCalls {
			effect, ok := executed[call.ID]
			body.Tools = append(body.Tools, digest.LedgerTool{Name: call.Function.Name, OK: ok && effect.Error == "", Receipt: effect.Result.Receipt})
		}
	}
	for _, receipt := range outcome.Receipts {
		if receipt.ToolName != "reply" && receipt.ID != "" {
			body.TaskRefs = append(body.TaskRefs, receipt.ToolName+":"+receipt.ID)
		}
	}
	body.NoDurableTrace = body.Reply == "" && len(actionIDs) == 0 && len(body.TaskRefs) == 0
	return digest.LedgerEntry{Kind: digest.LedgerWake, SourceID: job.ID, OccurredAt: time.Now(), Body: body}
}

func employeeDigestKey(scope employeeentry.Scope) digest.SceneKey {
	return digest.SceneKey{WorkspaceID: scope.WorkspaceID, AgentID: scope.AgentID, TenantOrgID: scope.TenantOrgID, SceneID: scope.SceneID}
}

// recordWakeLedgerTx runs inside the wake's completion transaction under a
// savepoint: a ledger problem is logged and never blocks delivery. A replayed
// completion inserts nothing (unique per job).
func (w *EmployeeSceneWorker) recordWakeLedgerTx(ctx context.Context, tx pgx.Tx, job employeeentry.Job, entry digest.LedgerEntry) error {
	if job.Scope.SceneID == "" {
		return nil
	}
	sp, err := tx.Begin(ctx)
	if err != nil {
		return err
	}
	key := employeeDigestKey(job.Scope)
	if _, err = digest.AppendLedgerTx(ctx, sp, key, entry); err == nil {
		err = digest.TouchSceneActivityTx(ctx, sp, key)
	}
	if err != nil {
		slog.WarnContext(ctx, "employee scene ledger skipped", "event", "employee_scene_ledger_failed", "job_id", job.ID, "scene_id", job.Scope.SceneID, "error", err)
		return sp.Rollback(ctx)
	}
	return sp.Commit(ctx)
}

// ReconcileEmployeeSceneTaskLedger appends one deterministic task_terminal
// entry per terminal Employee Task (goal, state, last Run result, Host
// verification) to its scene. Idempotent per (task, goal revision, state).
func (h *Handler) ReconcileEmployeeSceneTaskLedger(ctx context.Context, limit int) (int, error) {
	if h == nil || h.DB == nil || h.TxStarter == nil || limit < 1 || limit > 500 {
		return 0, errors.New("invalid employee scene task ledger reconciliation")
	}
	rows, err := h.DB.Query(ctx, `SELECT t.workspace_id::text,t.agent_id::text,t.tenant_org_id,t.scene_id::text,t.id::text,t.state,t.goal_revision,COALESCE(t.definition->>'goal',''),t.requester_ref,t.updated_at
FROM employee_task t
WHERE t.owner_loop='employee' AND t.scope_kind='scene' AND t.state IN ('succeeded','failed','cancelled') AND t.updated_at>now()-interval '24 hours'
 AND NOT EXISTS(SELECT 1 FROM employee_scene_ledger l WHERE l.workspace_id=t.workspace_id AND l.agent_id=t.agent_id AND l.scene_id=t.scene_id AND l.entry_kind='task_terminal' AND l.source_id=t.id::text||':'||t.goal_revision::text||':'||t.state)
ORDER BY t.updated_at LIMIT $1`, limit)
	if err != nil {
		return 0, err
	}
	type terminal struct {
		key                    digest.SceneKey
		task, state, goal, req string
		revision               int64
		updatedAt              time.Time
	}
	var tasks []terminal
	for rows.Next() {
		var t terminal
		if err = rows.Scan(&t.key.WorkspaceID, &t.key.AgentID, &t.key.TenantOrgID, &t.key.SceneID, &t.task, &t.state, &t.revision, &t.goal, &t.req, &t.updatedAt); err != nil {
			rows.Close()
			return 0, err
		}
		tasks = append(tasks, t)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return 0, err
	}
	count := 0
	var failures []error
	for _, t := range tasks {
		body := digest.LedgerBody{TaskID: t.task, Goal: t.goal, TaskState: t.state, RequesterRef: t.req, Outcome: t.state}
		var runID string
		err = h.DB.QueryRow(ctx, `SELECT id::text,state,result FROM employee_task_run WHERE workspace_id=$1::uuid AND agent_id=$2::uuid AND task_id=$3::uuid ORDER BY created_at DESC,id DESC LIMIT 1`, t.key.WorkspaceID, t.key.AgentID, t.task).Scan(&runID, &body.RunState, &body.RunResult)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			failures = append(failures, err)
			continue
		}
		if runID != "" {
			var passed, failed int
			if err = h.DB.QueryRow(ctx, `SELECT count(*) FILTER (WHERE outcome='passed'),count(*) FILTER (WHERE outcome<>'passed') FROM employee_task_verification WHERE workspace_id=$1::uuid AND agent_id=$2::uuid AND task_id=$3::uuid AND run_id=$4::uuid`, t.key.WorkspaceID, t.key.AgentID, t.task, runID).Scan(&passed, &failed); err != nil {
				failures = append(failures, err)
				continue
			}
			switch {
			case failed > 0:
				body.Verification = "failed"
			case passed > 0:
				body.Verification = "passed"
			}
		}
		entry := digest.LedgerEntry{Kind: digest.LedgerTaskTerminal, SourceID: fmt.Sprintf("%s:%d:%s", t.task, t.revision, t.state), OccurredAt: t.updatedAt, Body: body}
		inserted, err := digest.AppendLedgerTx(ctx, h.DB, t.key, entry)
		if err != nil {
			failures = append(failures, fmt.Errorf("employee task %s ledger: %w", t.task, err))
			continue
		}
		if inserted {
			count++
		}
	}
	return count, errors.Join(failures...)
}
