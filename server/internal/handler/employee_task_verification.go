package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/dwsclient"
	"github.com/multica-ai/multica/server/internal/employeeentry"
	"github.com/multica-ai/multica/server/internal/employeetask"
	"github.com/multica-ai/multica/server/internal/employeeverification"
	"github.com/multica-ai/multica/server/internal/scene"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/service/dingtalkresponse"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Host adapters for deterministic Employee Run verification (G1). The scene
// entry worker's maintenance tick runs ReconcileEmployeeVerifications and then
// ReconcileEmployeeVerifiedDistill beside ReconcileEmployeeLearnings; task
// links and the Task HTTP view read the gate with employeeverification.GateTx.
// VerifyEmployeeRun is the single-Run entry for callers holding Host records.

func (h *Handler) employeeVerifier() (*employeeverification.Verifier, error) {
	database, ok := employeeEntryDB(h)
	if !ok {
		return nil, employeeverification.ErrInvalid
	}
	evidence := employeeverification.PGEvidence{Reader: h.readEmployeeVerificationArtifact}
	if provider := h.employeeVerificationProvider(); provider != nil {
		evidence.Files = employeeVerificationFiles{h: h, provider: provider}
	}
	return &employeeverification.Verifier{
		DB:          database,
		Evidence:    evidence,
		TenantFence: employeeVerificationTenantFence,
	}, nil
}

// employeeVerificationProvider reads provider messages as the agent: the
// worker's injected provider, else the DingTalk response service.
func (h *Handler) employeeVerificationProvider() employeeResourceProvider {
	if h.EmployeeSceneWorker != nil && h.EmployeeSceneWorker.ResourceProvider != nil {
		return h.EmployeeSceneWorker.ResourceProvider
	}
	if h.DingTalkResponses != nil {
		return h.DingTalkResponses
	}
	return nil
}

const employeeVerificationReadTimeout = 15 * time.Second

// employeeVerificationFiles downloads the own file resources of a message a
// Run delivered through DingTalk (G1.1). Reads use D1's provider path as the
// agent's bound DWS identity, outside every database lock.
type employeeVerificationFiles struct {
	h        *Handler
	provider employeeResourceProvider
}

func (f employeeVerificationFiles) ReadDeliveredFiles(ctx context.Context, scope employeetask.Scope, taskID, _ string, queueTaskID string, m employeeverification.DeliveredMessage, maxBytes int64) ([]employeeverification.DeliveredFile, error) {
	in, err := f.h.employeeVerificationActionInput(ctx, scope, taskID, queueTaskID)
	if err != nil {
		return nil, err
	}
	// The receipt froze the sending identity and destination; a different
	// current identity or scene target cannot read the message as its sender.
	if m.DWSUID != in.DWSUID {
		return []employeeverification.DeliveredFile{{Unreadable: "identity_changed"}}, nil
	}
	if m.ConversationID != in.ConversationID {
		return []employeeverification.DeliveredFile{{Unreadable: "conversation_changed"}}, nil
	}
	readCtx, cancel := context.WithTimeout(ctx, employeeVerificationReadTimeout)
	defer cancel()
	message, err := f.provider.ReadMessageResources(readCtx, in, m.ConversationID, m.MessageID)
	if errors.Is(err, dwsclient.ErrMessageUnverified) {
		return []employeeverification.DeliveredFile{{Unreadable: "message_unverified"}}, nil
	}
	if err != nil {
		return nil, err
	}
	var out []employeeverification.DeliveredFile
	for _, r := range message.Resources {
		if r.Type != "file" || r.IDType != "fileId" {
			continue
		}
		file, err := f.provider.DownloadMessageFile(readCtx, in, r.ID, maxBytes)
		if errors.Is(err, dwsclient.ErrMessageFileTooLarge) {
			out = append(out, employeeverification.DeliveredFile{FileID: r.ID, Unreadable: "file_too_large"})
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, employeeverification.DeliveredFile{FileID: r.ID, Name: file.Name, Data: file.Data})
	}
	return out, nil
}

// employeeVerificationActionInput binds a provider read to the Run's own
// execution: the queue must be this Task's Employee Direct execution, the
// scene must still serve the tenant, and the identity and DWS gateway come
// from the agent binding and the admitted dispatch, never from the receipt.
func (h *Handler) employeeVerificationActionInput(ctx context.Context, scope employeetask.Scope, taskID, queueTaskID string) (dingtalkresponse.ActionInput, error) {
	var in dingtalkresponse.ActionInput
	queue, err := h.Queries.GetAgentTask(ctx, parseUUID(queueTaskID))
	if err != nil {
		return in, err
	}
	direct, ok := service.ParseDirectTaskContext(queue)
	if !ok || direct.EmployeeTaskID != taskID || direct.WorkspaceID != scope.WorkspaceID || uuidToString(queue.AgentID) != scope.AgentID {
		return in, errors.New("employee verification: execution does not belong to the task")
	}
	if err = employeeVerificationTenantFence(ctx, h.DB, scope); err != nil {
		return in, err
	}
	owner := scene.Owner{WorkspaceID: parseUUID(scope.WorkspaceID), AgentID: parseUUID(scope.AgentID)}
	registered, err := scene.Get(ctx, h.Queries, owner, parseUUID(scope.Scene.SceneID))
	if err != nil {
		return in, err
	}
	identity, err := h.Queries.GetAgentDingTalkIdentity(ctx, db.GetAgentDingTalkIdentityParams{WorkspaceID: owner.WorkspaceID, AgentID: owner.AgentID})
	if err != nil {
		return in, fmt.Errorf("employee verification identity: %w", err)
	}
	environment := ""
	var meta struct {
		JobID string `json:"employee_job_id"`
	}
	if json.Unmarshal(queue.Context, &meta) == nil && meta.JobID != "" {
		var items []employeeentry.Item
		err = h.DB.QueryRow(ctx, `SELECT items FROM employee_scene_job WHERE id=$1::uuid AND workspace_id=$2::uuid AND agent_id=$3::uuid`, meta.JobID, scope.WorkspaceID, scope.AgentID).Scan(&items)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return in, err
		}
		if len(items) > 0 {
			var env employeeDispatchEnvelope
			if err = json.Unmarshal(items[0].Payload, &env); err != nil {
				return in, err
			}
			if env.Command.ExternalIdentity.DWS != nil && env.Command.ExternalIdentity.DWS.UID != identity.DwsUid {
				return in, errors.New("employee verification: agent identity changed since dispatch")
			}
			environment = commandDWSEnvironment(env.Command)
		}
	}
	return dingtalkresponse.ActionInput{WorkspaceID: scope.WorkspaceID, AgentID: scope.AgentID, TaskID: queueTaskID, RequestID: "employee-verification:" + queueTaskID,
		DWSUID: identity.DwsUid, DWSOrgID: scope.TenantOrgID, SceneID: scope.Scene.SceneID, ConversationID: registered.ExternalSceneID,
		IsGroup: registered.SceneKind == scene.KindGroup, DWSEnvironment: environment}, nil
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

// ReconcileEmployeeVerifications is the durable verification trigger: it
// verifies succeeded Runs whose Task has an active spec and no result under
// the current spec digest. Fenced or failed attempts are logged and retried
// on a later pass; nothing here calls a model.
func (h *Handler) ReconcileEmployeeVerifications(ctx context.Context, limit int) (int, error) {
	verifier, err := h.employeeVerifier()
	if err != nil {
		return 0, err
	}
	outcomes, err := verifier.ProcessPending(ctx, limit)
	for _, o := range outcomes {
		if o.Err != nil {
			slog.WarnContext(ctx, "employee_task_verification_deferred", "task_id", o.TaskID, "run_id", o.RunID, "error", o.Err)
			continue
		}
		slog.InfoContext(ctx, "employee_task_verification", "task_id", o.TaskID, "run_id", o.RunID, "gate", o.Gate, "distill_intent", o.Intent)
	}
	return len(outcomes), err
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
