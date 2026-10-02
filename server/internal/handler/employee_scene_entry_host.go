package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/employeeentry"
	"github.com/multica-ai/multica/server/internal/employeetask"
	"github.com/multica-ai/multica/server/internal/scene"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/service/employeeloop"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type employeeToolRecord struct {
	Result  employeeloop.ToolResult `json:"result"`
	Failure string                  `json:"failure,omitempty"`
}

type employeeSceneHost struct {
	worker    *EmployeeSceneWorker
	job       employeeentry.Job
	envelopes []employeeDispatchEnvelope
	abort     context.CancelFunc
}

func employeeSceneTools() []employeeloop.Tool {
	stringField := func(description string) map[string]any {
		return map[string]any{"type": "string", "description": description}
	}
	stringArray := func(description string) map[string]any {
		return map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": description}
	}
	source := stringField("Exact source_ref from the current window; the Host resolves its original requester.")
	readSchema := map[string]any{"type": "object", "properties": map[string]any{"source_ref": source, "task_id": stringField("A known EmployeeTask UUID belonging to this requester in this scene.")}, "required": []string{"source_ref", "task_id"}, "additionalProperties": false}
	return []employeeloop.Tool{
		{Name: "stay_quiet", Description: "Record that this window does not require a response from this employee. No message is sent and no task is created or cancelled.", Schema: map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}},
		{Name: "dispatch_task", Description: "Accept only self-contained new work explicitly requested in the selected source message. If it refers to previous work without a concrete new goal, clarify instead; continuation is not registered. Include the acknowledgement to send after the queue commit. Continuation, control, reactions and quoted work are not supported by this tool.", Effect: true, Schema: map[string]any{"type": "object", "properties": map[string]any{"source_ref": source, "goal": stringField("The complete user goal, without inventing requirements."), "prompt": stringField("Complete execution instruction, preserving user constraints and material references."), "reply": stringField("Brief acknowledgement of accepted work, never a completed result."), "deliverables": stringArray("Optional concrete outputs explicitly required by the requester. Omit when unspecified; do not invent deliverables."), "success_criteria": stringArray("Optional acceptance conditions explicitly required by the requester. Omit when unspecified."), "access_needed": stringArray("Optional access the request says is needed. This is a request only and never grants access or capabilities.")}, "required": []string{"source_ref", "goal", "prompt", "reply"}, "additionalProperties": false}},
		{Name: "read_task", Description: "Read the requester's own explicitly identified task. A task UUID does not grant access.", Schema: readSchema},
		{Name: "read_task_history", Description: "Read up to twenty entries of the requester's own explicitly identified task.", Schema: readSchema},
	}
}
func (h *employeeSceneHost) source(ref string) (employeeSourceMessage, employeeDispatchEnvelope, error) {
	for i, item := range h.job.Items {
		for _, source := range employeeSourceMessages(item, h.envelopes[i]) {
			if ref != "" && source.SourceRef == ref && source.RequesterRef != "" {
				return source, h.envelopes[i], nil
			}
		}
	}
	return employeeSourceMessage{}, employeeDispatchEnvelope{}, errors.New("source_ref does not identify a frozen requester")
}
func argument(args map[string]any, key string) (string, error) {
	s, ok := args[key].(string)
	if !ok || strings.TrimSpace(s) == "" {
		return "", fmt.Errorf("%s is required", key)
	}
	return s, nil
}
func optionalStringArray(args map[string]any, key string) ([]string, error) {
	raw, present := args[key]
	if !present {
		return nil, nil
	}
	if values, ok := raw.([]string); ok {
		return append([]string(nil), values...), nil
	}
	values, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("%s must be an array of strings", key)
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		text, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("%s must be an array of strings", key)
		}
		out = append(out, text)
	}
	return out, nil
}

func (h *employeeSceneHost) Execute(ctx context.Context, identity employeeloop.Identity, call employeeloop.ToolCall) (employeeloop.ToolResult, error) {
	if identity.WorkspaceID != h.job.Scope.WorkspaceID || identity.AgentID != h.job.Scope.AgentID || identity.TenantOrgID != h.job.Scope.TenantOrgID || identity.Scene.SceneID != h.job.Scope.SceneID || identity.ReceiptID != h.job.Items[0].ReceiptID {
		return employeeloop.ToolResult{}, errors.New("employee Host identity mismatch")
	}
	if _, err := employeeSceneFence(ctx, h.worker.handler, h.job); err != nil {
		return employeeloop.ToolResult{}, err
	}
	var source employeeSourceMessage
	var env employeeDispatchEnvelope
	if call.Name != "stay_quiet" {
		ref, err := argument(call.Arguments, "source_ref")
		if err != nil {
			return employeeloop.ToolResult{}, err
		}
		source, env, err = h.source(ref)
		if err != nil {
			return employeeloop.ToolResult{}, err
		}
		if err = employeePrincipalAllowed(ctx, h.worker.handler, h.job.Scope, env.PrincipalID); err != nil {
			return employeeloop.ToolResult{}, err
		}
	}
	encoded, err := json.Marshal(call)
	if err != nil {
		return employeeloop.ToolResult{}, err
	}
	raw, err := h.worker.store.ExecuteTool(ctx, h.job, call.NativeToolCallID, encoded, func(tx pgx.Tx) (json.RawMessage, error) {
		var result employeeloop.ToolResult
		var err error
		switch call.Name {
		case "stay_quiet":
			if len(call.Arguments) != 0 {
				err = errors.New("stay_quiet accepts no arguments")
			} else {
				result = employeeloop.ToolResult{Content: "Quiet decision recorded.", Terminal: &employeeloop.Decision{Kind: employeeloop.Quiet}}
			}
		case "dispatch_task":
			result, err = h.dispatch(ctx, source, env, call)
		case "read_task", "read_task_history":
			result, err = h.read(ctx, source, call)
		default:
			err = errors.New("employee tool is not registered")
		}
		record := employeeToolRecord{Result: result}
		if err != nil {
			record.Failure = err.Error()
		}
		return json.Marshal(record)
	})
	var record employeeToolRecord
	if len(raw) > 0 {
		if decodeErr := json.Unmarshal(raw, &record); decodeErr != nil {
			if h.abort != nil {
				h.abort()
			}
			return employeeloop.ToolResult{}, decodeErr
		}
	}
	if err != nil {
		if h.abort != nil {
			h.abort()
		}
		return record.Result, err
	}
	if record.Failure != "" {
		return record.Result, errors.New(record.Failure)
	}
	return record.Result, nil
}
func (h *employeeSceneHost) taskScope() employeetask.Scope {
	return employeetask.Scope{WorkspaceID: h.job.Scope.WorkspaceID, AgentID: h.job.Scope.AgentID, TenantOrgID: h.job.Scope.TenantOrgID, Kind: employeetask.ScopeScene, Scene: scene.Ref{SceneID: h.job.Scope.SceneID}}
}
func (h *employeeSceneHost) dispatch(ctx context.Context, source employeeSourceMessage, env employeeDispatchEnvelope, call employeeloop.ToolCall) (employeeloop.ToolResult, error) {
	if source.Message.Reaction != nil || source.Message.ReferencedMessage != nil || env.Command.Continuation != nil {
		return employeeloop.ToolResult{}, errors.New("EmployeeLoop continuation, reaction and control are not ready; clarify the intended new work")
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
	goal, err := argument(call.Arguments, "goal")
	if err != nil {
		return employeeloop.ToolResult{}, err
	}
	prompt, err := argument(call.Arguments, "prompt")
	if err != nil {
		return employeeloop.ToolResult{}, err
	}
	reply, err := argument(call.Arguments, "reply")
	if err != nil {
		return employeeloop.ToolResult{}, err
	}
	if len(goal) > 16000 || len(prompt) > 64000 || len(reply) > 8000 {
		return employeeloop.ToolResult{}, errors.New("employee task instruction exceeds bounds")
	}
	definition := employeetask.Definition{Goal: goal}
	if definition.Deliverables, err = optionalStringArray(call.Arguments, "deliverables"); err != nil {
		return employeeloop.ToolResult{}, err
	}
	if definition.SuccessCriteria, err = optionalStringArray(call.Arguments, "success_criteria"); err != nil {
		return employeeloop.ToolResult{}, err
	}
	if definition.AccessNeeded, err = optionalStringArray(call.Arguments, "access_needed"); err != nil {
		return employeeloop.ToolResult{}, err
	}
	storeDB, ok := employeeEntryDB(h.worker.handler)
	if !ok {
		return employeeloop.ToolResult{}, errors.New("employee task storage is unavailable")
	}
	evidence, err := json.Marshal(source)
	if err != nil {
		return employeeloop.ToolResult{}, err
	}
	packet, err := employeetask.Compile(employeetask.CompileInput{
		Scope: h.taskScope(), PrincipalID: env.PrincipalID, Definition: definition, Prompt: prompt,
		Source:        employeetask.PacketMaterial{Ref: source.SourceRef, Scope: h.taskScope(), PrincipalID: env.PrincipalID, Body: string(evidence)},
		History:       employeetask.PacketHistory{State: employeetask.HistoryUnavailable},
		ReturnAddress: "scene:" + h.job.Scope.SceneID + "; source_ref:" + source.SourceRef,
	})
	if err != nil {
		return employeeloop.ToolResult{}, err
	}
	sourceKey := source.ReceiptID + "/" + call.NativeToolCallID
	task, err := employeetask.NewStore(storeDB).Create(ctx, employeetask.CreateParams{Scope: h.taskScope(), OwnerLoop: employeetask.LoopEmployee, DispatchMode: employeetask.DispatchDirect, RequesterRef: source.RequesterRef, Definition: packet.Definition, Source: employeetask.Source{Namespace: "employee_scene", Key: sourceKey + "/definition"}, Input: string(evidence)})
	if err != nil {
		return employeeloop.ToolResult{}, err
	}
	command := env.Command
	command.Event.Data.Messages = []DispatchMessage{source.Message}
	command.Event.Data.Sender = DispatchSender{UID: source.Message.SenderUID, StaffID: source.Message.SenderStaffID, OpenDingTalkID: source.Message.SenderOpenDingTalkID, DisplayName: source.Message.SenderDisplayName}
	// The scene job owns the initial callback. Runtime results have an Employee
	// delivery owner and must not flow through the old Coordinator completion path.
	command.CompletionCallback = nil
	command.ExtraCompletionCallbacks = nil
	contextJSON := dispatchRuntimeContext(command, sourceKey)
	var taskContext map[string]any
	if err = json.Unmarshal(contextJSON, &taskContext); err != nil {
		return employeeloop.ToolResult{}, err
	}
	delete(taskContext, "completion_callback")
	delete(taskContext, "execution_update_callback")
	taskContext["employee_delivery_owner"] = "employee"
	taskContext["employee_job_id"] = h.job.ID
	taskContext["employee_source_ref"] = source.SourceRef
	taskContext["employee_context_used"] = packet.ContextUsed
	contextJSON, err = json.Marshal(taskContext)
	if err != nil {
		return employeeloop.ToolResult{}, err
	}
	principal, err := util.ParseUUID(env.PrincipalID)
	if err != nil {
		return employeeloop.ToolResult{}, err
	}
	accepted, err := h.worker.handler.TaskService.EnqueueDirectTask(ctx, service.DirectTaskRequest{Task: task, Source: employeetask.Source{Namespace: "employee_scene", Key: sourceKey + "/run"}, Prompt: packet.Text, PrincipalID: principal, Context: contextJSON})
	if err != nil {
		return employeeloop.ToolResult{}, err
	}
	result, _ := json.Marshal(map[string]string{"task_id": task.ID, "run_id": accepted.Run.ID, "queue_task_id": util.UUIDToString(accepted.Task.ID)})
	return employeeloop.ToolResult{Content: string(result), Receipt: accepted.Run.ID, Terminal: &employeeloop.Decision{Kind: employeeloop.Dispatched, Reply: reply}}, nil
}
func (h *employeeSceneHost) read(ctx context.Context, source employeeSourceMessage, call employeeloop.ToolCall) (employeeloop.ToolResult, error) {
	id, err := argument(call.Arguments, "task_id")
	if err != nil {
		return employeeloop.ToolResult{}, err
	}
	if !strings.Contains(source.Message.Text, id) {
		return employeeloop.ToolResult{}, errors.New("task read requires an explicit task reference from its requester")
	}
	storeDB, ok := employeeEntryDB(h.worker.handler)
	if !ok {
		return employeeloop.ToolResult{}, errors.New("employee task storage is unavailable")
	}
	store := employeetask.NewStore(storeDB)
	task, err := store.Get(ctx, h.taskScope(), id)
	if err != nil {
		return employeeloop.ToolResult{}, err
	}
	if task.RequesterRef != source.RequesterRef {
		return employeeloop.ToolResult{}, errors.New("task is not owned by this requester")
	}
	var result any = task
	if call.Name == "read_task_history" {
		result, err = store.ReadEntries(ctx, h.taskScope(), task.ID, max(int64(0), task.LastEntrySeq-20), 20)
		if err != nil {
			return employeeloop.ToolResult{}, err
		}
	}
	raw, err := json.Marshal(result)
	return employeeloop.ToolResult{Content: string(raw)}, err
}

func employeePrincipalAllowed(ctx context.Context, h *Handler, scope employeeentry.Scope, principal string) error {
	agentID, err := util.ParseUUID(scope.AgentID)
	if err != nil {
		return err
	}
	workspaceID, err := util.ParseUUID(scope.WorkspaceID)
	if err != nil {
		return err
	}
	principalID, err := util.ParseUUID(principal)
	if err != nil {
		return err
	}
	// An Agent owner shortcut is an invocation rule, not proof that the
	// original admission principal still belongs to this workspace.
	if _, err = h.Queries.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{UserID: principalID, WorkspaceID: workspaceID}); err != nil {
		return err
	}
	agent, err := h.Queries.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{ID: agentID, WorkspaceID: workspaceID})
	if err != nil {
		return err
	}
	if agent.ArchivedAt.Valid || !h.canInvokeAgent(ctx, agent, "member", principal, principal, scope.WorkspaceID) {
		return errors.New("employee admission principal no longer has invoke access")
	}
	return nil
}
