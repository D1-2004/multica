package handler

import "github.com/multica-ai/multica/server/internal/service/employeeloop"

// Collection cancellation is goal control, never a new background execution.
// It shares stop_task's source/read/CAS boundary and transaction, while its
// narrower native contract requires an active collection owned by the source.
func employeeCancelCollectionTool() employeeloop.Tool {
	tool := employeeStopTool()
	tool.Name = "cancel_collection"
	tool.Description = "Cancel the requester's own waiting collection and stop its invitations, reminders and future summary when the current message asks to cancel it, stop asking, or stop summarizing (收集取消/不用再问/不要继续汇总). First read_task on its source-bound t1/q1 candidate, then pass the current read_ref and an exact outer-message instruction_quote. This is direct goal control: never dispatch a new background cancellation task, create another collection, or claim cancellation with reply alone. With several plausible collections ask which one. Host atomically cancels the goal, waits, collections and pending ready intents, and returns the actual committed acknowledgement. It does not retract messages already sent. If there is an active execution, cancellation is not process-exit proof."
	return tool
}

func isEmployeeStopTool(name string) bool { return name == "stop_task" || name == "cancel_collection" }
