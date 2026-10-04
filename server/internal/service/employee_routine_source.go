package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/contextcap"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/redact"
)

// CaptureRoutineSource captures the authenticated queue's allowlisted original
// input. The caller has already resolved the scene and authorized configuration.
// Sender identities are provenance, never principals or capability grants.
func CaptureRoutineSource(ctx context.Context, store contextcap.DBTX, task db.AgentTaskQueue, workspaceID, agentID, tenantOrgID, sceneID string) (*contextcap.RoutineSource, error) {
	if store == nil || util.UUIDToString(task.AgentID) != agentID || !task.ID.Valid {
		return nil, contextcap.ErrInvalidInput
	}
	var envelope struct {
		WorkspaceID string `json:"workspace_id"`
		Scene       struct {
			SceneID string `json:"scene_id"`
		} `json:"agent_scene"`
		EmployeeTaskID string `json:"employee_task_id"`
		PrincipalID    string `json:"direct_principal_id"`
		SourceRef      string `json:"employee_source_ref"`
		Prompt         string `json:"direct_task_prompt"`
		DispatchPrompt string `json:"dispatch_context_prompt"`
		Replayed       bool   `json:"replayed_dispatch_context"`
		Event          *struct {
			Sender   contextcap.RoutineRequester       `json:"sender"`
			Messages []contextcap.RoutineSourceMessage `json:"messages"`
		} `json:"dispatch_event_data"`
	}
	if json.Unmarshal(task.Context, &envelope) != nil || envelope.Replayed || envelope.Scene.SceneID != sceneID || (envelope.WorkspaceID != "" && envelope.WorkspaceID != workspaceID) {
		return nil, contextcap.ErrInvalidInput
	}
	parsedScope := contextcap.ScopeFromTaskContext(task.Context)
	if parsedScope.DispatchOrgID != "" && parsedScope.DispatchOrgID != tenantOrgID {
		return nil, contextcap.ErrInvalidInput
	}
	// Validate the complete scene binding from the directory, not a payload org.
	var bound bool
	if err := store.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent_scene WHERE id=$1::uuid AND workspace_id=$2::uuid AND agent_id=$3::uuid AND tenant_org_id=$4 AND scene_kind IN ('group','dm'))`, sceneID, workspaceID, agentID, tenantOrgID).Scan(&bound); err != nil {
		return nil, err
	}
	if !bound {
		return nil, contextcap.ErrInvalidInput
	}
	source := &contextcap.RoutineSource{Schema: contextcap.RoutineSourceSchema, WorkspaceID: workspaceID, AgentID: agentID, TenantOrgID: tenantOrgID, SceneID: sceneID, QueueTaskID: util.UUIDToString(task.ID), PrincipalID: envelope.PrincipalID, SourceRef: envelope.SourceRef}
	if envelope.EmployeeTaskID != "" {
		if _, ok := ParseDirectTaskContext(task); !ok {
			return nil, contextcap.ErrInvalidInput
		}
		// The context marker alone is insufficient: the queue must be the real Run
		// of the same EmployeeTask in this exact scene, agent and tenant.
		err := store.QueryRow(ctx, `SELECT t.id::text,r.id::text,t.requester_ref FROM employee_task t JOIN employee_task_run r ON r.task_id=t.id AND r.workspace_id=t.workspace_id AND r.agent_id=t.agent_id AND r.tenant_org_id=t.tenant_org_id WHERE t.id=$1::uuid AND t.workspace_id=$2::uuid AND t.agent_id=$3::uuid AND t.tenant_org_id=$4 AND t.scene_id=$5::uuid AND t.scope_kind='scene' AND r.queue_task_id=$6`, envelope.EmployeeTaskID, workspaceID, agentID, tenantOrgID, sceneID, task.ID).Scan(&source.EmployeeTaskID, &source.EmployeeRunID, &source.RequesterRef)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, contextcap.ErrInvalidInput
		}
		if err != nil {
			return nil, fmt.Errorf("capture routine source: %w", err)
		}
		source.OriginalWorkPacket = envelope.Prompt
	} else {
		// Older issue executions have no EmployeeTask work packet; retain their
		// explicit dispatch request and source messages without a fabricated Run.
		source.OriginalWorkPacket = envelope.DispatchPrompt
	}
	if envelope.Event != nil {
		source.Requester = envelope.Event.Sender
		source.Messages = envelope.Event.Messages
		if source.RequesterRef == "" {
			switch {
			case source.Requester.StaffID != "":
				source.RequesterRef = "dingtalk:staff:" + source.Requester.StaffID
			case source.Requester.OpenDingTalkID != "":
				source.RequesterRef = "dingtalk:open:" + source.Requester.OpenDingTalkID
			case source.Requester.SenderOpenDingTalkID != "":
				source.RequesterRef = "dingtalk:open:" + source.Requester.SenderOpenDingTalkID
			case source.Requester.UID != "":
				source.RequesterRef = "dingtalk:uid:" + source.Requester.UID
			}
		}
	}
	if source.RequesterRef == "" {
		source.RequesterRef = "queue_task:" + source.QueueTaskID
	}
	// Filter recognizable credentials from selected human/model text as well.
	source.OriginalWorkPacket = redact.Text(source.OriginalWorkPacket)
	for i := range source.Messages {
		source.Messages[i].Text = redact.Text(source.Messages[i].Text)
	}
	if strings.TrimSpace(source.OriginalWorkPacket) == "" && len(source.Messages) == 0 {
		return nil, contextcap.ErrInvalidInput
	}
	if err := source.ValidateBinding(workspaceID, agentID, tenantOrgID, sceneID); err != nil {
		return nil, err
	}
	return source, nil
}
