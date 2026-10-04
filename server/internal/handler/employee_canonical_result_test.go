package handler

import (
	"testing"

	"github.com/multica-ai/multica/server/internal/service/employeeloop"
)

func TestEmployeeReadyContinuationExplainsUnconfirmedGoal(t *testing.T) {
	result := employeeContinuationRefusalResult(employeeContinuationRefusal("ready"))
	out := employeeloop.Outcome{ToolOutcomes: []employeeloop.ToolOutcome{{ToolName: "continue_task", Result: result, Error: "continuation not started: ready"}}}
	if got := employeeContinuationFailureReply(out); got != "这项工作尚未确认完成，本次没有启动续接。" {
		t.Fatal("ready refusal became an unexplained transient retry", got)
	}
}
