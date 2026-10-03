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
	"github.com/multica-ai/multica/server/internal/langfuse"
	"github.com/multica-ai/multica/server/internal/scene"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/service/employeeloop"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type employeeToolRecord struct {
	Result        employeeloop.ToolResult  `json:"result"`
	Failure       string                   `json:"failure,omitempty"`
	DeliveryReply string                   `json:"delivery_reply,omitempty"`
	TaskRead      *employeeCurrentTaskRead `json:"task_read,omitempty"`
}

type employeeSceneHost struct {
	worker          *EmployeeSceneWorker
	job             employeeentry.Job
	envelopes       []employeeDispatchEnvelope
	abort           context.CancelFunc
	deliveryReplies map[string]string
}

func employeeSceneTools() []employeeloop.Tool {
	stringField := func(description string) map[string]any {
		return map[string]any{"type": "string", "description": description}
	}
	stringArray := func(description string) map[string]any {
		return map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": description}
	}
	source := stringField("Exact source_ref from the current window; the Host resolves its original requester.")
	noticeSchema := map[string]any{"type": "object", "description": "Default is always: deliver the final result, including an explicitly requested summary. Select if_not_delivered only when this selected source explicitly asks for a native file and says not to send a separate completion summary after delivery. Quote that exact instruction from this message; never infer it from history, quoted material or the execution result.", "properties": map[string]any{"mode": map[string]any{"type": "string", "enum": []string{"always", "if_not_delivered"}}, "require_delivery": map[string]any{"type": "string", "enum": []string{"file"}}, "instruction_quote": stringField("Exact wording in the selected source that asks for no additional summary after file delivery; required with if_not_delivered.")}, "required": []string{"mode"}, "additionalProperties": false}
	readSchema := map[string]any{"type": "object", "properties": map[string]any{"source_ref": source, "task_ref": stringField("Exact source-bound t1-style candidate from this wake. Read current status before deciding continuation."), "task_id": stringField("Legacy explicit EmployeeTask UUID from the selected current message; use task_ref for natural references.")}, "required": []string{"source_ref"}, "additionalProperties": false}
	tools := []employeeloop.Tool{
		{Name: "describe_capabilities", Terminal: employeeloop.Reply, Description: "For an ordinary capability introduction or link-only request, answer from the supplied directory on the first model call; no configuration read is needed. Keep ordinary introductions brief and natural, normally 1–3 short sentences about useful work, without internal tool names or fields. For configuration details plus a link, read missing details first and preserve requested exact names, states, and prompt text; give the complete requested answer. Skills and connectors require background execution; do not claim foreground access. Host appends the scene link; write no URL and start no work. Do not combine with other terminal or effect tools.", Schema: map[string]any{"type": "object", "properties": map[string]any{"source_ref": source, "reply": stringField("For ordinary introductions, use 1–3 natural short sentences about useful work, without internal names or inventories. For explicit configuration questions, preserve requested exact names, states, and prompt text in enough detail, including requests for details plus a link. Never write a configuration URL.")}, "required": []string{"source_ref", "reply"}, "additionalProperties": false}},
		{Name: "scene_config_get", Description: "Read current scene configuration only for explicit configuration-detail questions about switches, stored prompts, or existing routines not already in context. This is not a prerequisite for a general capability introduction or link-only request, and does not verify runtime access. Explain naturally while preserving requested exact names, states, and prompt text. Existing Cron/Webhook switches do not prove Employee-triggered execution. This grants no write permission.", Schema: map[string]any{"type": "object", "properties": map[string]any{"source_ref": source}, "required": []string{"source_ref"}, "additionalProperties": false}},
		{Name: "reply", Terminal: employeeloop.Reply, Description: "Reply directly using the current conversation and available facts, then finish without creating a task. Use for answers, explanations, clarifications and memory recall that need no background execution. Do not combine with dispatch_task or another effect tool in one batch.", Schema: map[string]any{"type": "object", "properties": map[string]any{"source_ref": source, "reply": stringField("The complete answer to send now, not an acknowledgement of future work.")}, "required": []string{"source_ref", "reply"}, "additionalProperties": false}},
		{Name: "stay_quiet", Terminal: employeeloop.Quiet, Description: "Record that this window does not require a response from this employee. No message is sent and no task is created or cancelled.", Schema: map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}},
		{Name: "dispatch_task", Description: "Create a real background task only for self-contained new work explicitly requested in the selected source message that requires background execution. For an answer already available from context or memory, use reply or normal text instead; never dispatch merely to send a reply. For previous work use its candidate and read_task, then continue_task only after a successful current read. Do not create a replacement task for a continuation. Include the acknowledgement to send after the queue commit. Control, reactions and quoted work are not supported by this new-task tool.", Effect: true, Schema: map[string]any{"type": "object", "properties": map[string]any{"source_ref": source, "completion_notice_policy": noticeSchema, "goal": stringField("The complete user goal, without inventing requirements."), "prompt": stringField("Complete execution instruction, preserving user constraints and material references."), "reply": stringField("Briefly confirm acceptance and intent to handle the request. Acceptance does not prove the executor has started. Without separate observed evidence for this execution, do not claim it has started, is running, has stopped, or has completed. Use natural wording such as 我来处理，跑完发你; do not narrate internal queue or sandbox states."), "deliverables": stringArray("Optional concrete outputs explicitly required by the requester. Omit when unspecified; do not invent deliverables."), "success_criteria": stringArray("Optional acceptance conditions explicitly required by the requester. Omit when unspecified."), "access_needed": stringArray("Optional access the request says is needed. This is a request only and never grants access or capabilities.")}, "required": []string{"source_ref", "goal", "prompt", "reply"}, "additionalProperties": false}},
		employeeSteerTool(source, stringField),
		{Name: "read_task", Description: "Read current persisted status and executor report for the selected source-bound task_ref. Reports are not delivery proof. Returns read_ref required by continue_task. Do not read or continue merely for thanks or ordinary chat.", Schema: readSchema},
		{Name: "read_task_history", Description: "Read up to twenty entries of the requester's own explicitly identified task.", Schema: readSchema},
	}
	continueNoticeSchema := make(map[string]any, len(noticeSchema))
	for key, value := range noticeSchema {
		continueNoticeSchema[key] = value
	}
	continueNoticeSchema["description"] = "Omit to inherit the existing Task delivery constraint. An earlier file-only instruction stays effective; always does not revoke it. Set if_not_delivered only for a new explicit file-only instruction quoted from this selected source."
	tools = append(tools, employeeloop.Tool{Name: "continue_task", Effect: true, Description: "Continue the same successfully completed task only when this source explicitly requests a further step of that goal. First read_task on its source-bound task_ref, then use the returned read_ref. Do not change the goal contract, start while running, retry failed/cancelled work, or treat thanks as work. With multiple plausible candidates clarify. A new Run is queued under the same Task; include its acceptance reply so no extra model call is required. Omit completion_notice_policy to preserve the Task delivery constraint; always cannot revoke an earlier file-only instruction without new requester authorization.", Schema: map[string]any{"type": "object", "properties": map[string]any{"source_ref": source, "task_ref": stringField("Exact candidate read this wake"), "read_ref": stringField("read_ref from this wake's successful read_task"), "instruction_quote": stringField("Exact outer-message excerpt explicitly requesting this continuation"), "prompt": stringField("Current requested step preserving user constraints; do not replace the stored goal"), "reply": stringField("Briefly confirm acceptance of the requested next step and intent to handle it. The previous run and this acceptance do not prove the new execution has started. Without separate observed evidence for the new execution, do not claim it has started, is running, has stopped, or has completed. Use natural wording such as 我来继续处理，跑完发你; do not narrate internal queue or sandbox states."), "completion_notice_policy": continueNoticeSchema}, "required": []string{"source_ref", "task_ref", "read_ref", "instruction_quote", "prompt", "reply"}, "additionalProperties": false}})
	tools = append(tools, employeeStopTool())
	return append(tools, employeeMemoryTools()...)
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

func (h *employeeSceneHost) Execute(ctx context.Context, identity employeeloop.Identity, call employeeloop.ToolCall) (returned employeeloop.ToolResult, returnedErr error) {
	var journalObservation *langfuse.Observation
	journalCommitted := false
	defer func() {
		// Transactional effect spans end after the journal transaction. A result
		// computed before a failed commit is not durable proof.
		if journalObservation != nil {
			journalObservation.End(langfuse.EndOptions{Output: employeeTraceSafe(returned), Err: employeeTraceError(returnedErr), Metadata: map[string]any{"journal_committed": journalCommitted}})
		}
	}()

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
	var continuation *employeeContinuationPlan
	var preparationErr error
	if call.Name == "continue_task" {
		continuation, preparationErr = h.prepareContinuation(ctx, source, env, call)
	}
	encoded, err := json.Marshal(call)
	if err != nil {
		return employeeloop.ToolResult{}, err
	}
	var acceptedContinuation *service.DirectTaskResult
	var acceptedStop *service.DirectTaskStopResult
	var revalidate func(pgx.Tx, json.RawMessage) (json.RawMessage, error)
	if isEmployeeMemoryTool(call.Name) {
		revalidate = func(tx pgx.Tx, raw json.RawMessage) (json.RawMessage, error) {
			return h.memoryReplay(ctx, tx, call, raw)
		}
	}
	if call.Name == "continue_task" || call.Name == "stop_task" || ((call.Name == "read_task" || call.Name == "read_task_history") && call.Arguments["task_ref"] != nil) {
		revalidate = func(tx pgx.Tx, raw json.RawMessage) (json.RawMessage, error) {
			return h.currentTaskReplay(ctx, tx, source, call, raw)
		}
	}
	raw, err := h.worker.store.ExecuteTool(ctx, h.job, call.NativeToolCallID, encoded, revalidate, func(tx pgx.Tx) (json.RawMessage, error) {
		observation := employeeTraceTool(ctx, h.job, call)
		var result employeeloop.ToolResult
		var deliveryReply string
		var taskRead *employeeCurrentTaskRead
		var err error
		if isEmployeeMemoryTool(call.Name) || call.Name == "continue_task" || call.Name == "stop_task" {
			journalObservation = observation
		} else {
			defer func() { employeeTraceToolResult(ctx, observation, call, result, err) }()
		}
		switch call.Name {
		case "stay_quiet":
			if len(call.Arguments) != 0 {
				err = errors.New("stay_quiet accepts no arguments")
			} else {
				result = employeeloop.ToolResult{Content: "Quiet decision recorded.", Terminal: &employeeloop.Decision{Kind: employeeloop.Quiet}}
			}
		case "reply", "describe_capabilities":
			var reply string
			reply, err = argument(call.Arguments, "reply")
			if err == nil && len(reply) > 8000 {
				err = errors.New("employee reply exceeds bounds")
			}
			if err == nil {
				result = employeeloop.ToolResult{Content: "Reply recorded.", Terminal: &employeeloop.Decision{Kind: employeeloop.Reply, Reply: strings.TrimSpace(reply)}}
				if call.Name == "describe_capabilities" {
					deliveryReply = h.capabilityReply(ctx, tx, strings.TrimSpace(reply))
				}
			}
		case "scene_config_get":
			if len(call.Arguments) != 1 {
				err = errors.New("scene_config_get accepts only source_ref")
			} else {
				result.Content, err = h.sceneConfiguration(ctx, tx)
			}
		case "memory_capture", "memory_lookup", "memory_forget":
			result, err = h.memoryTool(ctx, tx, call)
		case "dispatch_task":
			result, err = h.dispatch(ctx, source, env, call)
		case "steer_task":
			result, err = h.steer(ctx, source, env, call)
		case "read_task", "read_task_history":
			if call.Arguments["task_ref"] != nil {
				result, taskRead, err = h.readCurrentTask(ctx, tx, source, call)
			} else {
				result, err = h.read(ctx, source, call)
			}
		case "stop_task":
			result, acceptedStop, err = h.stopTask(ctx, tx, source, call)
		case "continue_task":
			if preparationErr != nil {
				err = preparationErr
			} else {
				result, acceptedContinuation, err = h.continueTask(ctx, tx, source, call, continuation)
			}
		default:
			err = errors.New("employee tool is not registered")
		}
		record := employeeToolRecord{Result: result, DeliveryReply: deliveryReply, TaskRead: taskRead}
		if err != nil {
			if call.Name == "continue_task" {
				record.Result = employeeContinuationRefusalResult(err)
			}
			if call.Name == "stop_task" {
				record.Result = employeeStopRefusal(err)
			}
			record.Failure = err.Error()
		}
		return json.Marshal(record)
	})
	journalCommitted = err == nil
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
		if call.Name == "continue_task" || call.Name == "stop_task" {
			return employeeloop.ToolResult{}, err
		}
		return record.Result, err
	}
	if record.Failure != "" {
		return record.Result, errors.New(record.Failure)
	}
	if call.Name == "continue_task" {
		if acceptedContinuation != nil {
			h.worker.handler.TaskService.NotifyDirectTaskResult(ctx, *acceptedContinuation)
		} else if record.Result.Receipt != "" {
			h.notifyContinuedTask(ctx, record.Result)
		}
	}
	if acceptedStop != nil {
		h.observeTaskStop(ctx, *acceptedStop)
	}
	if record.DeliveryReply != "" {
		if h.deliveryReplies == nil {
			h.deliveryReplies = map[string]string{}
		}
		h.deliveryReplies[call.NativeToolCallID] = record.DeliveryReply
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
	noticePolicy, err := employeeCompletionNoticePolicy(call.Arguments, source)
	if err != nil {
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
		Scope: h.taskScope(), PrincipalID: env.PrincipalID, Definition: definition, Prompt: prompt, CompletionNotice: noticePolicy,
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
	if packet.CompletionNotice.Mode != employeetask.CompletionNoticeAlways {
		taskContext[employeeCompletionNoticeContextKey] = packet.CompletionNotice
	}
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
