package handler

import (
	"github.com/multica-ai/multica/server/internal/integrations/agentmessagerouter"
	"github.com/multica-ai/multica/server/internal/service/dingtalkresponse"
	"testing"
)

func TestCoordinatorFailureResponseKeepsDiagnosticsPrivate(t *testing.T) {
	for _, tc := range []struct {
		name, reason, text string
		visible            bool
	}{
		{"primary", "coordinator_job_failed", coordinatorFailureReply, true},
		{"collected", "coordinator_job_failed", "", false},
		{"raw diagnostic", "coordinator_job_failed", "finish needs revision: secret", false},
		{"other failure", "executor_failed", coordinatorFailureReply, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := agentmessagerouter.ExecutionResultRequest{RequestID: "request", ExecutionStatus: "failed", ResultMessage: tc.text, ExecutionResult: map[string]any{"failureReason": tc.reason, "error": "private diagnostic"}}
			in := responseInputForResult(dingtalkresponse.ActionInput{}, result)
			if tc.visible {
				if in.Text != coordinatorFailureReply || in.CloseState != "" {
					t.Fatalf("missing visible failure: %#v", in)
				}
			} else if in.Text != "" || in.CloseState != "failed" {
				t.Fatalf("unexpected visible failure: %#v", in)
			}
			if result.ExecutionStatus != "failed" {
				t.Fatal("failure was relabeled as success")
			}
		})
	}
}

func TestCoordinatorFailureReplyParticipation(t *testing.T) {
	for _, tc := range []struct {
		kind                                   string
		proactive, mention, taskFinished, want bool
	}{
		{"group", false, false, false, true},
		{"group", true, false, false, false},
		{"group", true, true, false, true},
		{"single", true, false, false, true},
		{"p2p", true, false, false, true},
		{"direct", true, false, false, true},
		{"", true, false, false, false},
		{"single", false, false, true, false},
	} {
		command := DispatchCommand{ProactiveConversation: tc.proactive, Event: DispatchEvent{Data: DispatchEventData{Conversation: DispatchConversation{Type: tc.kind}}}}
		if tc.mention {
			command.ExternalIdentity.DWS = &AgentDispatchDWSIdentity{UID: "123"}
			command.Event.Data.Mentions = []DispatchMention{{UID: "123"}}
		}
		if tc.taskFinished {
			command.TaskFinishedTaskID = "task"
		}
		if got := coordinatorFailureNeedsReply(command); got != tc.want {
			t.Fatalf("%+v got %v", tc, got)
		}
	}
}
