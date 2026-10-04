package handler

import (
	"testing"

	"github.com/multica-ai/multica/server/internal/service/employeeloop"
)

func TestEmployeeStopFailureReplyPreservesCommittedEffects(t *testing.T) {
	failed := employeeloop.ToolOutcome{ToolName: "stop_task", Error: "quote control refused"}
	if got := employeeStopFailureReply(employeeloop.Outcome{ToolOutcomes: []employeeloop.ToolOutcome{failed}}); got != "这次未能取消任务，任务可能仍在执行。" {
		t.Fatal("failed cancellation did not explain its uncommitted state", got)
	}
	changed := failed
	changed.Result.Content = `{"stop_not_requested":"state_changed"}`
	if got := employeeStopFailureReply(employeeloop.Outcome{ToolOutcomes: []employeeloop.ToolOutcome{changed}}); got != "这项工作的状态已变化，本次没有发出新的停止请求。" {
		t.Fatal("state-change refusal lost its specific acknowledgement", got)
	}
	committed := employeeloop.ToolOutcome{ToolName: "dispatch_task", Result: employeeloop.ToolResult{Receipt: "accepted-run"}}
	if got := employeeStopFailureReply(employeeloop.Outcome{ToolOutcomes: []employeeloop.ToolOutcome{failed, committed}}); got != "" {
		t.Fatal("failed stop replaced an actual committed acknowledgement", got)
	}
	readOnly := employeeloop.ToolOutcome{ToolName: "read_task", Error: "budget exhausted"}
	if got := employeeStopFailureReply(employeeloop.Outcome{ToolOutcomes: []employeeloop.ToolOutcome{readOnly}}); got != "" {
		t.Fatal("read-only failure invented a cancellation attempt", got)
	}
}
