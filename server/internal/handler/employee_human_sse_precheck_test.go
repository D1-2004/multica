package handler

import (
	"context"
	"strings"
	"testing"
)

func TestEmployeeHumanAndDiscoveryKeepIndependentReadyGates(t *testing.T) {
	for _, human := range []bool{false, true} {
		for _, discovery := range []bool{false, true} {
			worker := &EmployeeSceneWorker{
				HumanQuestionsReady: func(context.Context) (bool, error) { return human, nil },
				TaskDiscoveryReady:  func(context.Context) (bool, error) { return discovery, nil },
			}
			seen := map[string]bool{}
			for _, tool := range worker.newInputTools(context.Background()) {
				seen[tool.Name] = true
			}
			if seen["a2ui_ask"] != human || seen["accept_human_response"] != human || seen["find_tasks"] != discovery {
				t.Fatalf("independent ready gates lost tools: human=%v discovery=%v tools=%v", human, discovery, seen)
			}
		}
	}
}

func TestEmployeeHumanAndDiscoveryJointInputKeepsPendingQuestion(t *testing.T) {
	f, dc, q := humanCardFixture(t)
	worker := f.h.EmployeeSceneWorker
	worker.TaskDiscoveryReady = func(context.Context) (bool, error) { return true, nil }
	message := f.command.Event.Data.Messages[0]
	message.OpenMsgID, message.Text = "joint-answer", "办公流程，简短一点，先别发给别人。"
	host, _, _ := employeeMemoryHost(t, f, dc, []DispatchMessage{message})
	input, err := worker.buildInput(context.Background(), host.job, host.envelopes, host.envelopes)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, tool := range input.Config.Tools {
		seen[tool.Name] = true
	}
	for _, name := range []string{"a2ui_ask", "accept_human_response", "find_tasks", "first_feedback"} {
		if !seen[name] {
			t.Fatalf("joint new input lost %s", name)
		}
	}
	if !input.Config.StreamedFeedback || !strings.Contains(input.Input.TaskBrief, "pending_human_questions") || !strings.Contains(input.Input.TaskBrief, q.ID) || !strings.Contains(input.Config.Persona.DecisionRules, "REQUEST DECISION") {
		t.Fatalf("discovery replaced pending question or feedback readiness: %+v", input)
	}
}
