package service

import (
	"encoding/json"
	"strings"

	"github.com/multica-ai/multica/server/internal/employeetask"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const DirectTaskNoticePolicyKey = "employee_completion_notice_policy"
const DirectTaskNoticeOriginKey = "employee_completion_notice_origin"

// DirectTaskNoticeOrigin is a locator, never an authorization grant. The Host
// verifies its original queue, accepted source and tool checkpoint before use.
type DirectTaskNoticeOrigin struct {
	Version     int    `json:"version"`
	TaskID      string `json:"task_id"`
	QueueTaskID string `json:"queue_task_id"`
	SourceRef   string `json:"source_ref"`
}

// InheritDirectTaskNoticePolicy retains the frozen delivery constraint and its
// first authorization locator. Steer may retain an older frozen execution input,
// so a top-level origin can carry the locator but cannot establish permission.
func InheritDirectTaskNoticePolicy(predecessor db.AgentTaskQueue) (employeetask.CompletionNoticePolicy, *DirectTaskNoticeOrigin, error) {
	var fields struct {
		Input  json.RawMessage         `json:"employee_direct_input"`
		Origin *DirectTaskNoticeOrigin `json:"employee_completion_notice_origin"`
	}
	var input struct {
		SourceRef string                              `json:"employee_source_ref"`
		Policy    employeetask.CompletionNoticePolicy `json:"employee_completion_notice_policy"`
		Origin    *DirectTaskNoticeOrigin             `json:"employee_completion_notice_origin"`
	}
	if json.Unmarshal(predecessor.Context, &fields) != nil || json.Unmarshal(fields.Input, &input) != nil {
		return input.Policy, nil, employeetask.ErrInvalid
	}
	frozen := predecessor
	frozen.Context = fields.Input
	direct, valid := ParseDirectTaskContext(frozen)
	if !valid {
		return input.Policy, nil, employeetask.ErrInvalid
	}
	origin := input.Origin
	if origin == nil {
		origin = fields.Origin
	} else if fields.Origin != nil && *origin != *fields.Origin {
		return input.Policy, nil, employeetask.ErrInvalid
	}
	sourceRef := input.SourceRef
	if origin != nil {
		sourceRef = origin.SourceRef
	}
	policy, err := employeetask.NormalizeCompletionNoticePolicy(input.Policy, sourceRef)
	if err != nil {
		return policy, nil, err
	}
	if policy.Mode == employeetask.CompletionNoticeAlways {
		if origin != nil {
			return policy, nil, employeetask.ErrInvalid
		}
		return policy, nil, nil
	}
	if origin == nil {
		origin = &DirectTaskNoticeOrigin{Version: 1, TaskID: direct.EmployeeTaskID, QueueTaskID: util.UUIDToString(predecessor.ID), SourceRef: policy.SourceRef}
	}
	if origin.Version != 1 || origin.TaskID != direct.EmployeeTaskID || origin.SourceRef != policy.SourceRef || strings.TrimSpace(origin.SourceRef) == "" {
		return policy, nil, employeetask.ErrInvalid
	}
	if _, err := util.ParseUUID(origin.QueueTaskID); err != nil {
		return policy, nil, employeetask.ErrInvalid
	}
	return policy, origin, nil
}
