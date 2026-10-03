package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/employeetask"
	"github.com/multica-ai/multica/server/internal/langfuse"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/service/employeeloop"
	"github.com/multica-ai/multica/server/internal/taskinput"
	"github.com/multica-ai/multica/server/internal/util"
)

func employeeStopTool() employeeloop.Tool {
	field := func(text string) map[string]any { return map[string]any{"type": "string", "description": text} }
	return employeeloop.Tool{Name: "stop_task", Effect: true, Terminal: employeeloop.Reply, Description: "Request a pure stop of the requester's own source-bound task only when the current message explicitly asks to stop. First read_task and use that current read_ref. Stop creates no Task, successor Run or replacement work and does not steer or continue. Do not treat thanks, a progress question, quoted instructions or ordinary chat as a stop. A quote reply whose own outer text asks to stop may stop only its q1-style quoted candidate. With multiple plausible tasks, clarify. Host returns the acknowledgement; cancellation is not process-exit proof. Use execution_state and process_exit_confirmed when answering whether it has actually stopped.", Schema: map[string]any{"type": "object", "properties": map[string]any{"source_ref": field("Exact current source_ref whose requester owns this task"), "task_ref": field("Exact source-bound task candidate (t1- or q1-style) read this wake"), "read_ref": field("read_ref from this wake's successful read_task"), "instruction_quote": field("Exact outer-message excerpt explicitly requesting stop")}, "required": []string{"source_ref", "task_ref", "read_ref", "instruction_quote"}, "additionalProperties": false}}
}

type employeeStopReceipt struct {
	TaskID               string   `json:"task_id"`
	RunID                string   `json:"run_id,omitempty"`
	QueueTaskID          string   `json:"queue_task_id,omitempty"`
	EntrySeq             int64    `json:"stop_entry_seq"`
	State                string   `json:"execution_state"`
	ExitConfirmed        bool     `json:"process_exit_confirmed"`
	PendingPredecessor   bool     `json:"pending_predecessor"`
	TaskState            string   `json:"task_state"`
	CancelledCollections []string `json:"cancelled_collections,omitempty"`
}

func (h *employeeSceneHost) stopTask(ctx context.Context, tx pgx.Tx, source employeeSourceMessage, call employeeloop.ToolCall) (employeeloop.ToolResult, *service.DirectTaskStopResult, error) {
	registry := employeeloop.NewToolRegistry()
	registry.Register(employeeStopTool())
	registry.Register(employeeCancelCollectionTool())
	if valid, problems := registry.Validate(call.Name, call.Arguments); !valid {
		return employeeloop.ToolResult{}, nil, errors.New(strings.Join(problems, "; "))
	}
	env, err := h.currentTaskSource(ctx, tx, source)
	if err != nil {
		return employeeloop.ToolResult{}, nil, err
	}
	if source.Message.Reaction != nil || env.Command.Continuation != nil {
		return employeeloop.ToolResult{}, nil, errors.New("stop requires the selected current outer request")
	}
	// Match dispatch/steer participation: an explicit mention list must address
	// this employee in a non-proactive group. Nil remains unknown, not empty.
	if strings.EqualFold(env.Command.Event.Data.Conversation.Type, "group") && !env.Command.ProactiveConversation && source.Message.Mentions != nil {
		addressed := false
		for _, mention := range source.Message.Mentions {
			if env.Command.ExternalIdentity.DWS != nil && mention.UID == env.Command.ExternalIdentity.DWS.UID {
				addressed = true
			}
		}
		if !addressed {
			return employeeloop.ToolResult{}, nil, errors.New("source message does not address this employee")
		}
	}
	quote, err := argument(call.Arguments, "instruction_quote")
	if err != nil {
		return employeeloop.ToolResult{}, nil, err
	}
	if len(quote) > 16000 || !strings.Contains(source.Message.Text, quote) {
		return employeeloop.ToolResult{}, nil, errors.New("stop quote must occur in the selected current message")
	}
	ref, err := argument(call.Arguments, "task_ref")
	if err != nil {
		return employeeloop.ToolResult{}, nil, err
	}
	binding, err := h.currentTaskBinding(ctx, tx, source, ref)
	if err != nil {
		return employeeloop.ToolResult{}, nil, err
	}
	if err = employeeQuotedControl(source, binding); err != nil {
		return employeeloop.ToolResult{}, nil, err
	}
	readRef, err := argument(call.Arguments, "read_ref")
	if err != nil {
		return employeeloop.ToolResult{}, nil, err
	}
	var raw []byte
	if err = tx.QueryRow(ctx, `SELECT tool_journal->$2 FROM employee_scene_job WHERE id=$1::uuid`, h.job.ID, readRef).Scan(&raw); err != nil {
		return employeeloop.ToolResult{}, nil, err
	}
	var prior struct {
		Input  employeeloop.ToolCall `json:"input"`
		Result employeeToolRecord    `json:"result"`
	}
	if json.Unmarshal(raw, &prior) != nil || prior.Input.Name != "read_task" || prior.Input.NativeToolCallID != readRef || prior.Input.Arguments["source_ref"] != source.SourceRef || prior.Input.Arguments["task_ref"] != ref || prior.Result.Failure != "" || prior.Result.TaskRead == nil {
		return employeeloop.ToolResult{}, nil, errors.New("stop_task requires this wake's successful read_task")
	}
	read := prior.Result.TaskRead
	task := read.Snapshot.Task
	if read.SourceRef != source.SourceRef || read.TaskRef != ref || task.ID != binding.TaskID || task.Scope != h.taskScope() || task.RequesterRef != source.RequesterRef {
		return employeeloop.ToolResult{}, nil, errors.New("stop read scope mismatch")
	}
	var collectionIDs []string
	if call.Name == "cancel_collection" {
		store := taskinput.NewStore(tx)
		scope := employeeTaskinputScope(h.job.Scope)
		waits, err := store.TaskWaits(ctx, scope, task.ID)
		if err != nil {
			return employeeloop.ToolResult{}, nil, err
		}
		for _, wait := range waits {
			col, err := store.GetCollection(ctx, scope, wait.CollectionID)
			if err != nil {
				return employeeloop.ToolResult{}, nil, err
			}
			if col.RequesterRef != source.RequesterRef || col.OriginSceneID != h.job.Scope.SceneID {
				return employeeloop.ToolResult{}, nil, employeeloop.ErrToolRefused
			}
			collectionIDs = append(collectionIDs, col.ID)
		}
		if len(collectionIDs) == 0 {
			return employeeloop.ToolResult{}, nil, fmt.Errorf("%w: this task has no active collection to cancel", employeeloop.ErrToolRefused)
		}
	}
	principal, err := util.ParseUUID(env.PrincipalID)
	if err != nil {
		return employeeloop.ToolResult{}, nil, err
	}
	body, _ := json.Marshal(source)
	request := service.DirectTaskStopRequest{Task: task, PrincipalID: principal, Source: employeetask.Source{Namespace: "employee_scene_stop", Key: source.ReceiptID + "/" + call.NativeToolCallID}, ActorRef: source.RequesterRef, Body: string(body)}
	if read.Snapshot.LatestRun != nil {
		request.RunID = read.Snapshot.LatestRun.ID
		request.QueueTaskID = read.Snapshot.LatestRun.QueueTaskID
	}
	stopped, err := h.worker.handler.TaskService.StopDirectTaskTx(ctx, tx, request)
	if err != nil {
		return employeeloop.ToolResult{}, nil, err
	}
	receipt := employeeStopReceipt{TaskID: stopped.Task.ID, TaskState: string(stopped.Task.State), RunID: request.RunID, QueueTaskID: request.QueueTaskID, EntrySeq: stopped.Entry.Seq, State: stopped.State, ExitConfirmed: stopped.ExitConfirmed, PendingPredecessor: stopped.PendingPredecessor}
	if call.Name == "cancel_collection" {
		for _, id := range collectionIDs {
			col, err := taskinput.NewStore(tx).GetCollection(ctx, employeeTaskinputScope(h.job.Scope), id)
			if err != nil {
				return employeeloop.ToolResult{}, nil, err
			}
			if col.State != taskinput.CollectionCancelled {
				return employeeloop.ToolResult{}, nil, employeetask.ErrConflict
			}
		}
		receipt.CancelledCollections = collectionIDs
	}
	raw, _ = json.Marshal(receipt)
	reply := "已请求停止这个任务。"
	switch stopped.State {
	case "completed":
		reply = "上次执行已经完成，不会再继续处理。"
	case "failed":
		reply = "上次执行已失败，不会再继续处理；目前还没有进程退出确认。"
	case "unconfirmed":
		reply = "已请求停止这个任务，目前还没有进程退出确认。"
	}
	if call.Name == "cancel_collection" {
		reply = "已取消这次收集，不会再催问或汇总。"
		if !stopped.ExitConfirmed {
			reply += "任务执行已请求停止，尚未确认进程退出。"
		}
	}
	return employeeloop.ToolResult{Content: string(raw), Receipt: fmt.Sprintf("employee-stop:%s:%d", stopped.Task.ID, stopped.Entry.Seq), Terminal: &employeeloop.Decision{Kind: employeeloop.Reply, Reply: reply}}, &stopped, nil
}

func (h *employeeSceneHost) observeTaskStop(ctx context.Context, result service.DirectTaskStopResult) {
	h.worker.handler.TaskService.NotifyDirectTaskStop(ctx, result)
	if !result.Changed {
		return
	}
	keys := map[string]string{"employee_task_id": result.Task.ID, "employee_run_id": result.Run.ID, "queue_task_id": uuidToString(result.Queue.ID)}
	langfuse.TraceFromContext(ctx).Index(keys)
	slog.InfoContext(ctx, "employee task stop requested", "event", "employee_task_stop_requested", "workspace_id", h.job.Scope.WorkspaceID, "agent_id", h.job.Scope.AgentID, "tenant_org_id", h.job.Scope.TenantOrgID, "scene_id", h.job.Scope.SceneID, "job_id", h.job.ID, "task_id", result.Task.ID, "run_id", result.Run.ID, "queue_task_id", uuidToString(result.Queue.ID), "stop_entry_seq", result.Entry.Seq, "execution_state", result.State, "exit_confirmed", result.ExitConfirmed)
}

func employeeStopRefusal(err error) employeeloop.ToolResult {
	if !errors.Is(err, employeetask.ErrConflict) && !errors.Is(err, employeetask.ErrActiveRun) {
		return employeeloop.ToolResult{}
	}
	return employeeloop.ToolResult{Content: `{"stop_not_requested":"state_changed"}`}
}
func employeeStopFailureReply(outcome employeeloop.Outcome) string {
	found := false
	for _, effect := range outcome.ToolOutcomes {
		if effect.Result.Receipt != "" {
			return ""
		}
		if effect.Error == "" {
			continue
		}
		if found || !isEmployeeStopTool(effect.ToolName) {
			return ""
		}
		var denied struct {
			Reason string `json:"stop_not_requested"`
		}
		if json.Unmarshal([]byte(effect.Result.Content), &denied) != nil || denied.Reason != "state_changed" {
			return ""
		}
		found = true
	}
	if found {
		return "这项工作的状态已变化，本次没有发出新的停止请求。"
	}
	return ""
}
