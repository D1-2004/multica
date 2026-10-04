package contextcap

import "encoding/json"

const RoutineSourceSchema = "scene.routine.source/1"
const MaxRoutineSourceBytes = 256 << 10

// RoutineSource is immutable provenance and already selected task material.
// It grants no authority and must never be restored as dispatch/credential context.
type RoutineSource struct {
	Schema             string                 `json:"schema"`
	WorkspaceID        string                 `json:"workspace_id"`
	AgentID            string                 `json:"agent_id"`
	TenantOrgID        string                 `json:"tenant_org_id"`
	SceneID            string                 `json:"scene_id"`
	QueueTaskID        string                 `json:"queue_task_id"`
	EmployeeTaskID     string                 `json:"employee_task_id,omitempty"`
	EmployeeRunID      string                 `json:"employee_run_id,omitempty"`
	PrincipalID        string                 `json:"principal_id,omitempty"`
	RequesterRef       string                 `json:"requester_ref"`
	SourceRef          string                 `json:"source_ref,omitempty"`
	Requester          RoutineRequester       `json:"requester"`
	Messages           []RoutineSourceMessage `json:"messages,omitempty"`
	OriginalWorkPacket string                 `json:"original_work_packet,omitempty"`
}

type RoutineRequester struct {
	DisplayName          string `json:"displayName,omitempty"`
	OpenDingTalkID       string `json:"openDingTalkId,omitempty"`
	SenderOpenDingTalkID string `json:"senderOpenDingTalkId,omitempty"`
	StaffID              string `json:"staffId,omitempty"`
	UID                  string `json:"uid,omitempty"`
}

type RoutineSourceMessage struct {
	OpenMsgID            string `json:"openMsgId"`
	OccurredAt           int64  `json:"occurredAt"`
	Text                 string `json:"text,omitempty"`
	SenderDisplayName    string `json:"senderDisplayName,omitempty"`
	SenderUID            string `json:"senderUid,omitempty"`
	SenderOpenDingTalkID string `json:"senderOpenDingTalkId,omitempty"`
	SenderStaffID        string `json:"senderStaffId,omitempty"`
}

// ValidateBinding fences persisted source material to its routine. The original
// principal is provenance only; callers authorize execution with current rights.
func (s *RoutineSource) ValidateBinding(workspaceID, agentID, tenantOrgID, sceneID string) error {
	if s == nil {
		return nil
	}
	if s.Schema != RoutineSourceSchema || s.WorkspaceID != workspaceID || s.AgentID != agentID || s.TenantOrgID != tenantOrgID || s.SceneID != sceneID || !validUUID(s.QueueTaskID) || s.RequesterRef == "" {
		return ErrInvalidInput
	}
	raw, err := json.Marshal(s)
	if err != nil || len(raw) > MaxRoutineSourceBytes {
		return ErrInvalidInput
	}
	return nil
}
