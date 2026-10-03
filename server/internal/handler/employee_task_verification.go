package handler

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/employeetask"
	"github.com/multica-ai/multica/server/internal/employeeverification"
	"github.com/multica-ai/multica/server/internal/scene"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Host adapters for deterministic Employee Run verification (G1). Nothing in
// the request path calls these yet: the Task lifecycle owner invokes
// VerifyEmployeeRun from its terminal/CompleteGoal path, and the scene entry
// worker calls ReconcileEmployeeVerifiedDistill beside ReconcileEmployeeLearnings.

func (h *Handler) employeeVerifier() (*employeeverification.Verifier, error) {
	database, ok := employeeEntryDB(h)
	if !ok {
		return nil, employeeverification.ErrInvalid
	}
	return &employeeverification.Verifier{
		DB:          database,
		Evidence:    employeeverification.PGEvidence{Reader: h.readEmployeeVerificationArtifact},
		TenantFence: employeeVerificationTenantFence,
	}, nil
}

// VerifyEmployeeRun checks one succeeded Run against its Task's active
// verification contract. scope and IDs must come from Host records, never
// from model arguments.
func (h *Handler) VerifyEmployeeRun(ctx context.Context, scope employeetask.Scope, taskID, runID string) (employeeverification.RunResult, error) {
	verifier, err := h.employeeVerifier()
	if err != nil {
		return employeeverification.RunResult{}, err
	}
	result, err := verifier.VerifyRun(ctx, scope, taskID, runID)
	if err == nil && result.Gate.Status != employeeverification.GateNone {
		slog.InfoContext(ctx, "employee_task_verification", "task_id", taskID, "run_id", runID, "gate", result.Gate.Status,
			"correct", result.Gate.Correct, "spec_revision", result.Gate.SpecRevision, "records", len(result.Records), "distill_intent", result.Intent)
	}
	return result, err
}

// ReconcileEmployeeVerifiedDistill consumes durable verified-distill intents
// into Employee memory. It performs database work only; no model is called.
func (h *Handler) ReconcileEmployeeVerifiedDistill(ctx context.Context, limit int) (int, error) {
	if h == nil || h.EmployeeMemory == nil {
		return 0, nil
	}
	database, ok := employeeEntryDB(h)
	if !ok {
		return 0, employeeverification.ErrInvalid
	}
	outcomes, err := employeeverification.NewStore(database).ProcessVerifiedDistill(ctx, limit, employeeverification.DistillOptions{Memory: h.EmployeeMemory, Fence: employeeVerifiedDistillFence})
	for _, o := range outcomes {
		slog.InfoContext(ctx, "employee_verified_distill", "run_id", o.RunID, "state", o.State, "learning_id", o.LearningID, "reason", o.Reason)
	}
	return len(outcomes), err
}

// employeeVerificationTenantFence applies the same current tenant binding as
// fencedScene: the scene must still belong to an org this agent serves.
func employeeVerificationTenantFence(ctx context.Context, q employeeverification.Querier, scope employeetask.Scope) error {
	dbtx, ok := q.(db.DBTX)
	if !ok {
		return employeeverification.ErrInvalid
	}
	owner := scene.Owner{WorkspaceID: parseUUID(scope.WorkspaceID), AgentID: parseUUID(scope.AgentID)}
	ref := scope.Scene
	if _, err := fencedScene(ctx, db.New(dbtx), &ref, owner, scope.TenantOrgID); err != nil {
		if errors.Is(err, scene.ErrNotFound) || errors.Is(err, scene.ErrStaleTenant) || errors.Is(err, scene.ErrUnresolved) {
			return fmt.Errorf("%w: %v", employeeverification.ErrStaleTenant, err)
		}
		return err
	}
	return nil
}

// employeeVerifiedDistillFence skips intents whose agent was archived or
// whose tenant binding no longer holds.
func employeeVerifiedDistillFence(ctx context.Context, tx pgx.Tx, intent employeeverification.Intent) (string, error) {
	queries := db.New(tx)
	row, err := queries.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{ID: parseUUID(intent.AgentID), WorkspaceID: parseUUID(intent.WorkspaceID)})
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && row.ArchivedAt.Valid) {
		return "agent_archived", nil
	}
	if err != nil {
		return "", err
	}
	scope := employeetask.Scope{WorkspaceID: intent.WorkspaceID, AgentID: intent.AgentID, TenantOrgID: intent.TenantOrgID, Kind: employeetask.ScopeScene, Scene: scene.Ref{SceneID: intent.SceneID}}
	if err = employeeVerificationTenantFence(ctx, tx, scope); errors.Is(err, employeeverification.ErrStaleTenant) {
		return "stale_tenant", nil
	}
	return "", err
}

// readEmployeeVerificationArtifact opens the sealed Storage object of one
// ready Run artifact. The sealed envelope binds id, Run binding and sha256, so
// a swapped or truncated object fails instead of being checked.
func (h *Handler) readEmployeeVerificationArtifact(ctx context.Context, ref employeeverification.Artifact) ([]byte, error) {
	if h.Storage == nil || h.contextCredentialBox() == nil {
		return nil, errors.New("artifact storage unavailable")
	}
	a, err := scanEmployeeArtifact(h.DB.QueryRow(ctx, `SELECT `+employeeArtifactColumns+` FROM employee_task_artifact WHERE attachment_id=$1::uuid`, ref.AttachmentID))
	if err != nil {
		return nil, err
	}
	if a.State != "ready" || a.Binding.TaskID != ref.TaskID || a.Binding.RunID != ref.RunID || a.Binding.QueueTaskID != ref.QueueTaskID || a.SHA256 != ref.SHA256 || a.SizeBytes > employeeverification.MaxVerifiedArtifactBytes {
		return nil, errEmployeeArtifactInvalid
	}
	if a.StoredSizeBytes < 28 || a.StoredSizeBytes > maxUploadSize*2 {
		return nil, errEmployeeArtifactInvalid
	}
	reader, err := h.Storage.GetReader(ctx, a.StorageKey)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	sealed, err := io.ReadAll(io.LimitReader(reader, a.StoredSizeBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(sealed)) != a.StoredSizeBytes {
		return nil, errEmployeeArtifactInvalid
	}
	return h.openEmployeeArtifact(a, sealed)
}
