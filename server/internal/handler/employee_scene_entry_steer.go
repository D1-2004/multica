package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/employeetask"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/service/employeeloop"
	"github.com/multica-ai/multica/server/internal/util"
)

// employeeSteerRecentWindow bounds implicit target selection: a correction
// without a task_id considers the requester's running tasks and tasks that
// became ready or succeeded recently in this scene. Stopped and failed tasks
// are never implicit targets.
const employeeSteerRecentWindow = "30 minutes"

func employeeSteerTool(source map[string]any, stringField func(string) map[string]any) employeeloop.Tool {
	return employeeloop.Tool{
		Name:        "steer_task",
		Effect:      true,
		Description: "Apply the requester's correction to their own background task in this conversation that is running or just finished: the Host interrupts the current run and resumes the same task with the correction as the next input. Use it instead of dispatch_task when the selected message corrects, narrows, redirects or adds a constraint to work already dispatched. Use task_ref from this wake's source-bound candidates or read_task for natural references. Omit the target only when the requester has exactly one such task; with multiple plausible candidates ask which task to correct. It never creates a new task. Include the acknowledgement to send after the correction is committed.",
		Schema: map[string]any{"type": "object", "properties": map[string]any{
			"source_ref": source,
			"correction": stringField("The requester's correction as complete instructions, preserving their wording and constraints."),
			"reply":      stringField("Briefly confirm acceptance of the correction and intent to apply it. Acceptance does not prove the old executor has stopped or the next execution has started. Without separate observed execution evidence, do not claim work has started, is running, has stopped, or has completed. Use natural wording such as 收到，按新要求处理，跑完发你; do not narrate internal queue or sandbox states."),
			"task_ref":   stringField("Exact source-bound t1-style candidate from this wake or read_task; do not also supply task_id."),
			"task_id":    stringField("Legacy explicit EmployeeTask UUID; prefer task_ref for natural references. Do not also supply task_ref."),
		}, "required": []string{"source_ref", "correction", "reply"}, "additionalProperties": false},
	}
}

// steer is the EmployeeLoop entry to the Task Service steer capability. The
// Host, not the model, decides which task the requester may correct.
func (h *employeeSceneHost) steer(ctx context.Context, tx pgx.Tx, source employeeSourceMessage, env employeeDispatchEnvelope, call employeeloop.ToolCall) (employeeloop.ToolResult, error) {
	ref, explicit, err := employeeSteerReference(call)
	if err != nil {
		return employeeloop.ToolResult{}, err
	}
	// Resolve short references through the same admitted-source snapshot that
	// read_task uses. Neither the alias nor a legacy UUID grants authority.
	if _, err := h.currentTaskSource(ctx, tx, source); err != nil {
		return employeeloop.ToolResult{}, err
	}
	if ref != "" {
		binding, err := h.currentTaskBinding(ctx, tx, source, ref)
		if err != nil {
			return employeeloop.ToolResult{}, err
		}
		if binding.SharedReadOnly {
			return employeeloop.ToolResult{}, errors.New("shared_read_only task cannot be steered")
		}
		explicit = binding.TaskID
	}
	if source.Message.Reaction != nil || env.Command.Continuation != nil {
		return employeeloop.ToolResult{}, errors.New("a reaction or structured continuation cannot steer a task")
	}
	if strings.EqualFold(env.Command.Event.Data.Conversation.Type, "group") && !env.Command.ProactiveConversation && source.Message.Mentions != nil {
		addressed := false
		for _, mention := range source.Message.Mentions {
			if env.Command.ExternalIdentity.DWS != nil && mention.UID == env.Command.ExternalIdentity.DWS.UID {
				addressed = true
			}
		}
		if !addressed {
			return employeeloop.ToolResult{}, errors.New("source message does not address this employee")
		}
	}
	correction, err := argument(call.Arguments, "correction")
	if err != nil {
		return employeeloop.ToolResult{}, err
	}
	reply, err := argument(call.Arguments, "reply")
	if err != nil {
		return employeeloop.ToolResult{}, err
	}
	if len(correction) > 8000 || len(reply) > 8000 {
		return employeeloop.ToolResult{}, errors.New("employee steer instruction exceeds bounds")
	}
	task, candidates, err := h.steerTarget(ctx, source.RequesterRef, explicit)
	if err != nil {
		return employeeloop.ToolResult{}, err
	}
	if task.ID == "" {
		listed, _ := json.Marshal(map[string]any{"candidates": candidates, "instruction": "Several tasks match; select a source-bound task_ref from this wake, or use an exact UUID from candidates.task_id as the legacy task_id. Never infer a t1-to-UUID mapping from list order. Ask the requester if the intended task is unclear."})
		return employeeloop.ToolResult{Content: string(listed)}, nil
	}
	sourceKey := source.ReceiptID + "/" + call.NativeToolCallID
	overlay, err := employeeCorrectionContext(env, source, sourceKey)
	if err != nil {
		return employeeloop.ToolResult{}, err
	}
	control := service.EmployeeTaskControl{Tasks: h.worker.handler.TaskService}
	result, err := control.Steer(ctx, service.EmployeeTaskSteerRequest{
		Task:          task,
		Source:        employeetask.Source{Namespace: "employee_scene", Key: sourceKey + "/steer"},
		ActorRef:      source.RequesterRef,
		Content:       correction,
		Context:       overlay,
		SameRequester: true,
	})
	if err != nil {
		return employeeloop.ToolResult{}, err
	}
	out, _ := json.Marshal(map[string]string{"task_id": task.ID, "outcome": string(result.Outcome), "run_id": result.Run.ID, "queue_task_id": util.UUIDToString(result.Queue.ID)})
	return employeeloop.ToolResult{Content: string(out), Receipt: result.Run.ID, Terminal: &employeeloop.Decision{Kind: employeeloop.Dispatched, Reply: strings.TrimSpace(reply)}}, nil
}

// Old frozen model calls may put t1 in task_id. Accept it only as a source-bound
// reference; retain real UUIDs and implicit single-target selection unchanged.
func employeeSteerReference(call employeeloop.ToolCall) (ref, explicit string, err error) {
	_, hasRef := call.Arguments["task_ref"]
	_, hasID := call.Arguments["task_id"]
	if hasRef && hasID {
		return "", "", errors.New("steer_task accepts task_ref or task_id, not both")
	}
	if hasRef {
		ref, err = argument(call.Arguments, "task_ref")
		return strings.TrimSpace(ref), "", err
	}
	if hasID {
		explicit, err = argument(call.Arguments, "task_id")
		if err != nil {
			return "", "", err
		}
		explicit = strings.TrimSpace(explicit)
		if _, err := util.ParseUUID(explicit); err != nil {
			return explicit, "", nil
		}
	}
	return "", explicit, nil
}

func (h *employeeSceneHost) steerReplay(ctx context.Context, tx pgx.Tx, source employeeSourceMessage, call employeeloop.ToolCall, raw json.RawMessage) (json.RawMessage, error) {
	ref, _, err := employeeSteerReference(call)
	if err != nil {
		return nil, err
	}
	if ref != "" {
		// Revalidate without changing the original journal key or payload.
		args := map[string]any{"task_ref": ref}
		return h.currentTaskReplay(ctx, tx, source, employeeloop.ToolCall{Arguments: args}, raw)
	}
	if _, err := h.currentTaskSource(ctx, tx, source); err != nil {
		return nil, err
	}
	return raw, nil
}

type employeeSteerCandidate struct {
	TaskID string `json:"task_id"`
	Goal   string `json:"goal"`
	State  string `json:"state"`
}

// steerTarget returns the one task the requester may correct, or candidates
// when the choice is ambiguous. An explicit task_id must still be the
// requester's own Direct task in this scene.
func (h *employeeSceneHost) steerTarget(ctx context.Context, requester, explicit string) (employeetask.Task, []employeeSteerCandidate, error) {
	storeDB, ok := employeeEntryDB(h.worker.handler)
	if !ok {
		return employeetask.Task{}, nil, errors.New("employee task storage is unavailable")
	}
	store := employeetask.NewStore(storeDB)
	scope := h.taskScope()
	if explicit != "" {
		task, err := store.Get(ctx, scope, explicit)
		if err != nil {
			return employeetask.Task{}, nil, err
		}
		if task.RequesterRef != requester || task.OwnerLoop != employeetask.LoopEmployee || task.DispatchMode != employeetask.DispatchDirect {
			return employeetask.Task{}, nil, errors.New("task is not owned by this requester")
		}
		return task, nil, nil
	}
	rows, err := storeDB.Query(ctx, `SELECT id::text, definition->>'goal', state FROM employee_task
 WHERE workspace_id=$1::uuid AND agent_id=$2::uuid AND tenant_org_id=$3 AND scope_kind='scene' AND scene_id=$4::uuid
   AND owner_loop='employee' AND dispatch_mode='direct' AND requester_ref=$5
   AND (state IN ('running','waiting') OR (state IN ('ready','succeeded') AND updated_at > now() - $6::interval))
 ORDER BY (state IN ('running','waiting')) DESC, updated_at DESC LIMIT 5`, scope.WorkspaceID, scope.AgentID, scope.TenantOrgID, scope.Scene.SceneID, requester, employeeSteerRecentWindow)
	if err != nil {
		return employeetask.Task{}, nil, err
	}
	candidates, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (employeeSteerCandidate, error) {
		var c employeeSteerCandidate
		err := row.Scan(&c.TaskID, &c.Goal, &c.State)
		return c, err
	})
	if err != nil {
		return employeetask.Task{}, nil, err
	}
	// Pick implicitly only when the requester has exactly one candidate: a
	// correction meant for a finished task must never interrupt a running one.
	switch {
	case len(candidates) == 0:
		return employeetask.Task{}, nil, errors.New("the requester has no running or recent task here to correct; use dispatch_task for new work")
	case len(candidates) == 1:
		task, err := store.Get(ctx, scope, candidates[0].TaskID)
		return task, nil, err
	default:
		return employeetask.Task{}, candidates, nil
	}
}

// employeeCorrectionContext carries the correction's own launch identity. The
// Task Service keeps only identity keys from it; delivery stays bound to the
// task's original request, which is what the Employee notice verifies.
func employeeCorrectionContext(env employeeDispatchEnvelope, source employeeSourceMessage, sourceKey string) (json.RawMessage, error) {
	command := env.Command
	command.Event.Data.Messages = []DispatchMessage{source.Message}
	command.CompletionCallback = nil
	command.ExtraCompletionCallbacks = nil
	var fields map[string]any
	if err := json.Unmarshal(dispatchRuntimeContext(command, sourceKey), &fields); err != nil {
		return nil, fmt.Errorf("correction context: %w", err)
	}
	return json.Marshal(fields)
}
