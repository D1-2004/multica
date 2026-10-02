package handler

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/util"
	"net/http"
	"strings"

	"github.com/multica-ai/multica/server/internal/employeeentry"
	"github.com/multica-ai/multica/server/internal/employeeloopconfig"
	"github.com/multica-ai/multica/server/internal/eventrouter"
	"github.com/multica-ai/multica/server/internal/scene"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// employeeDispatchEnvelope is assembled after endpoint authentication and scene
// admission. It is persisted internally; the Router wire DTO cannot populate it.
type employeeDispatchEnvelope struct {
	Command             DispatchCommand `json:"command"`
	EndpointID          string          `json:"endpoint_id"`
	EndpointNamespaceID string          `json:"endpoint_namespace_id"`
	PrincipalID         string          `json:"principal_id"`
	TargetIdentity      string          `json:"target_identity"`
}

type employeeSourceMessage struct {
	SourceRef    string          `json:"source_ref"`
	ReceiptID    string          `json:"receipt_id"`
	RequesterRef string          `json:"requester_ref"`
	Message      DispatchMessage `json:"message"`
}

func employeeRequesterRef(org string, m DispatchMessage) string {
	for _, key := range []struct{ kind, value string }{{"uid", m.SenderUID}, {"open_id", m.SenderOpenDingTalkID}, {"staff_id", m.SenderStaffID}} {
		if value := strings.TrimSpace(key.value); value != "" {
			return "dingtalk:" + org + ":" + key.kind + ":" + value
		}
	}
	return ""
}
func employeeSourceMessages(item employeeentry.Item, env employeeDispatchEnvelope) []employeeSourceMessage {
	result := make([]employeeSourceMessage, 0, len(env.Command.Event.Data.Messages))
	for _, m := range env.Command.Event.Data.Messages {
		ref := ""
		if m.OpenMsgID != "" {
			ref = item.ReceiptID + "/" + m.OpenMsgID
		}
		result = append(result, employeeSourceMessage{ref, item.ReceiptID, employeeRequesterRef(dispatchRecordedOrg(env.Command), m), m})
	}
	return result
}
func employeeEntryDB(h *Handler) (employeeentry.DB, bool) {
	db, ok := h.TxStarter.(employeeentry.DB)
	return db, ok
}

// prepareEmployeeReceipt validates new work before opening the receipt
// transaction. Its returned hook only persists the frozen owner and payload.
func (h *Handler) prepareEmployeeReceipt(w http.ResponseWriter, r *http.Request, c DispatchCommand, dc agentDispatchContext, org, unmappedReason string, event eventrouter.Event) (eventrouter.ReceiptHook, bool) {
	database, ok := employeeEntryDB(h)
	if !ok {
		return nil, false
	}
	_, err := h.Queries.GetSceneEventReceipt(r.Context(), db.GetSceneEventReceiptParams{WorkspaceID: dc.WorkspaceID, AgentID: dc.AgentID, Source: event.Source, SourceEventID: event.ID})
	if err == nil {
		return nil, false
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusServiceUnavailable, "existing event lookup failed")
		return nil, true
	}
	config, err := employeeloopconfig.Load(r.Context(), database, dc.WorkspaceID, dc.AgentID)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "work owner configuration is unavailable")
		return nil, true
	}
	owner := employeeentry.Coordinator
	if config.Enabled && config.Mode == employeeloopconfig.Employee {
		owner = employeeentry.Employee
	}
	scope := employeeentry.Scope{WorkspaceID: uuidToString(dc.WorkspaceID), AgentID: uuidToString(dc.AgentID), TenantOrgID: org}
	holdReason := ""
	if c.Continuation != nil || c.TaskFinishedTaskID != "" || (c.Control != nil && (c.Control.Action == "cancel" || c.Control.TargetExternalTaskID != "")) {
		owner = employeeentry.Coordinator
		boundOwner, e := employeeBoundOwner(r.Context(), database, scope, c)
		if e != nil {
			writeError(w, http.StatusServiceUnavailable, "bound work owner lookup failed")
			return nil, true
		}
		if boundOwner == employeeentry.Employee {
			owner = boundOwner
			holdReason = "employee_continuation_not_ready"
		}
	}
	if h.EmployeeSceneWorker == nil {
		if owner == employeeentry.Employee {
			writeError(w, http.StatusServiceUnavailable, "employee scene consumer is not ready")
			return nil, true
		}
		return nil, false
	}
	if owner == employeeentry.Employee {
		if h.EmployeeSceneWorker.ReplicaReady == nil || h.EmployeeSceneWorker.ReplicaReady(r.Context()) != nil {
			writeError(w, http.StatusServiceUnavailable, "employee server replicas are not ready")
			return nil, true
		}
		if c.Source.Type != "digital_employee" || c.Event.Domain != "channel" || c.Event.Type != "message.created" || c.Outbound.Mode != protocol.DispatchOutboundModeDWS || c.ExternalIdentity.DWS == nil {
			holdReason = "unsupported_source"
		}
		if c.Continuation != nil || c.TaskFinishedTaskID != "" || (c.Control != nil && (c.Control.Action == "cancel" || c.Control.TargetExternalTaskID != "")) {
			holdReason = "employee_continuation_not_ready"
		}
		if len(c.Event.Data.Messages) == 0 || len(c.Event.Data.Messages) > employeeentry.MaxWindowMessages {
			if holdReason == "" {
				holdReason = "unsupported_window_size"
			}
		}
		if dispatchIsAgentSelfMessage(c) || dispatchIsAgentSelfEmotion(c) {
			holdReason = "ignored_self"
		}
		if unmappedReason != "" {
			holdReason = unmappedReason
		}
		if holdReason == "" {
			if h.EmployeeLoopReady == nil || h.EmployeeLoopReady(r.Context(), dc.WorkspaceID, dc.AgentID) != nil {
				writeError(w, http.StatusServiceUnavailable, "employee scene consumer is not ready")
				return nil, true
			}
			if _, ok := h.resolveAgentDispatchAgent(w, r, dc.UserID, dc.WorkspaceID, dc.AgentID); !ok {
				return nil, true
			}
			if h.EventTriggers != nil && strings.EqualFold(c.Event.Data.Conversation.Type, "group") {
				enabled, e := h.EventTriggers.Enabled(r.Context(), dc.AgentID, dc.WorkspaceID)
				if e != nil {
					writeError(w, http.StatusServiceUnavailable, "conversation participation configuration is unavailable")
					return nil, true
				}
				c.ProactiveConversation = enabled
			}
		}
	}
	snapshot := employeeStoredCommand(c)
	return func(ctx context.Context, tx pgx.Tx, receipt db.SceneEventReceipt) error {
		actualScope := scope
		actualScope.TenantOrgID = receipt.TenantOrgID
		actualScope.SceneID = uuidToString(receipt.SceneID)
		actualHold := holdReason
		if !receipt.SceneID.Valid || receipt.Reason != "" {
			if owner == employeeentry.Coordinator {
				return nil
			}
			actualHold = firstNonEmpty(receipt.Reason, "unmapped_scene")
		}
		command := snapshot
		command.EventReceiptID = uuidToString(receipt.ID)
		command.AgentScene = nil
		if receipt.SceneID.Valid {
			command.AgentScene = &scene.Ref{SceneID: actualScope.SceneID}
		}
		envelope := employeeDispatchEnvelope{Command: command, EndpointID: dc.EndpointID, EndpointNamespaceID: uuidToString(dc.EndpointNamespaceID), PrincipalID: uuidToString(dc.UserID), TargetIdentity: h.TaskCompletionTargetIdentity}
		raw, err := json.Marshal(envelope)
		if err != nil {
			return err
		}
		count := len(command.Event.Data.Messages)
		if count == 0 {
			count = 1
		}
		_, err = employeeentry.NewStore(tx).Admit(ctx, employeeentry.Admission{Scope: actualScope, Item: employeeentry.Item{ReceiptID: command.EventReceiptID, PrincipalID: envelope.PrincipalID, Payload: raw, MessageCount: count}, Owner: owner, ConfigRevision: string(config.Mode), HoldReason: actualHold})
		return err
	}, false
}

func employeeStoredCommand(c DispatchCommand) DispatchCommand {
	snapshot := c
	snapshot.Event.Data.Messages = append([]DispatchMessage(nil), c.Event.Data.Messages...)
	stampEmployeeMessages(&snapshot)
	// The DWS identity reference is sufficient for renewed execution credentials.
	// Do not persist inbound bearer/telemetry credentials in the new job ledger.
	snapshot.ExternalIdentity.ContextToken = ""
	snapshot.ExternalIdentity.ExpiresAt = 0
	if c.CompletionCallback != nil {
		callback := *c.CompletionCallback
		callback.TelemetryToken = ""
		snapshot.CompletionCallback = &callback
	}
	snapshot.ExtraCompletionCallbacks = append([]DispatchCompletionCallback(nil), c.ExtraCompletionCallbacks...)
	for i := range snapshot.ExtraCompletionCallbacks {
		snapshot.ExtraCompletionCallbacks[i].TelemetryToken = ""
	}
	return snapshot
}

// admitEmployeeScene only projects the already-committed consumer. A historical
// receipt without one belongs to the original contract and is never upgraded.
func (h *Handler) admitEmployeeScene(w http.ResponseWriter, r *http.Request, c *DispatchCommand, dc agentDispatchContext, receipt db.SceneEventReceipt) bool {
	database, ok := employeeEntryDB(h)
	if !ok {
		return false
	}
	scope := employeeentry.Scope{WorkspaceID: uuidToString(dc.WorkspaceID), AgentID: uuidToString(dc.AgentID), TenantOrgID: receipt.TenantOrgID, SceneID: uuidToString(receipt.SceneID)}
	consumption, err := employeeentry.NewStore(database).Lookup(r.Context(), scope, c.EventReceiptID)
	if errors.Is(err, employeeentry.ErrNotFound) {
		return false
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "work owner lookup failed")
		return true
	}
	if consumption.Owner == employeeentry.Coordinator {
		return false
	}
	if consumption.State == "queued" {
		if h.EmployeeSceneWorker == nil || h.EmployeeLoopReady == nil || h.EmployeeLoopReady(r.Context(), dc.WorkspaceID, dc.AgentID) != nil {
			writeError(w, http.StatusServiceUnavailable, "employee scene consumer is not ready")
			return true
		}
		h.EmployeeSceneWorker.Notify()
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"event_receipt_id": c.EventReceiptID, "employee_job_id": consumption.JobID, "owner_loop": consumption.Owner, "state": consumption.State, "reason": consumption.Reason})
	return true
}

func employeeSceneFence(ctx context.Context, h *Handler, job employeeentry.Job) (db.AgentScene, error) {
	ws, err := scene.ParseID(job.Scope.WorkspaceID)
	if err != nil {
		return db.AgentScene{}, err
	}
	agent, err := scene.ParseID(job.Scope.AgentID)
	if err != nil {
		return db.AgentScene{}, err
	}
	return fencedScene(ctx, h.Queries, &scene.Ref{SceneID: job.Scope.SceneID}, scene.Owner{WorkspaceID: ws, AgentID: agent}, job.Scope.TenantOrgID)
}

// Explicit legacy references keep their persisted work owner. Only identifiers
// are inspected here; their contents and authority remain in the owning backend.
func employeeBoundOwner(ctx context.Context, database employeeentry.DB, scope employeeentry.Scope, c DispatchCommand) (string, error) {
	refs := []string{c.TaskFinishedTaskID}
	if c.Continuation != nil {
		refs = append(refs, c.Continuation.IssueID)
	}
	if c.Control != nil {
		refs = append(refs, c.Control.TargetExternalTaskID)
	}
	for _, ref := range refs {
		if _, err := util.ParseUUID(ref); err != nil {
			continue
		}
		var owner string
		err := database.QueryRow(ctx, `SELECT owner_loop FROM employee_task t WHERE workspace_id=$1::uuid AND agent_id=$2::uuid AND tenant_org_id=$3 AND (id=$4::uuid OR issue_id=$4::uuid OR EXISTS(SELECT 1 FROM employee_task_run r WHERE r.task_id=t.id AND r.queue_task_id=$4::uuid)) LIMIT 1`, scope.WorkspaceID, scope.AgentID, scope.TenantOrgID, ref).Scan(&owner)
		if err == nil {
			return owner, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return "", err
		}
	}
	return employeeentry.Coordinator, nil
}
