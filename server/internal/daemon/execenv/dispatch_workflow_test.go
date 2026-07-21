package execenv

import (
	"strings"
	"testing"
)

func TestDWSIssueOutboundIsIntegratedIntoIssueWorkflow(t *testing.T) {
	const workflowPrompt = "DWS lifecycle: add-emoji first; reply to latest_message after delivery."
	for name, ctx := range map[string]TaskContextForEnv{
		"assignment": {
			IssueID:                  "issue-1",
			DispatchSurfaceType:      "issue",
			DispatchOutboundMode:     "dws",
			DispatchWorkflowPrompt:   workflowPrompt,
		},
		"comment": {
			IssueID:                  "issue-1",
			TriggerCommentID:         "comment-1",
			DispatchSurfaceType:      "issue",
			DispatchOutboundMode:     "dws",
			DispatchWorkflowPrompt:   workflowPrompt,
		},
	} {
		t.Run(name, func(t *testing.T) {
			out := buildMetaSkillContent("hermes", ctx)
			for _, want := range []string{
				"#### Dispatch Outbound Delivery",
				"acknowledgement reaction",
				"Post the final result as the required Multica Issue comment",
				"exactly the same content",
				workflowPrompt,
				"both the Issue comment and the DingTalk DWS reply",
			} {
				if !strings.Contains(out, want) {
					t.Errorf("%s DWS issue workflow missing %q\n---\n%s", name, want, out)
				}
			}
			workflowIndex := strings.Index(out, "### Workflow")
			outboundIndex := strings.Index(out, "#### Dispatch Outbound Delivery")
			outputIndex := strings.Index(out, "## Output")
			if workflowIndex < 0 || outboundIndex < workflowIndex || outputIndex < outboundIndex {
				t.Fatalf("%s outbound instructions are not inside the workflow region: workflow=%d outbound=%d output=%d", name, workflowIndex, outboundIndex, outputIndex)
			}
			if strings.Contains(out, "The user does NOT see your terminal output, assistant chat text, or run logs — only comments on the issue") {
				t.Fatalf("%s DWS issue workflow retained comment-only delivery claim\n---\n%s", name, out)
			}
		})
	}
}

func TestDWSChatOutboundUsesChatWorkflowPosition(t *testing.T) {
	const workflowPrompt = "DWS chat lifecycle prompt."
	out := buildMetaSkillContent("hermes", TaskContextForEnv{
		ChatSessionID:          "chat-1",
		DispatchSurfaceType:    "chat",
		DispatchOutboundMode:   "dws",
		DispatchWorkflowPrompt: workflowPrompt,
	})
	chatIndex := strings.Index(out, "You are in chat mode")
	outboundIndex := strings.Index(out, "#### Dispatch Outbound Delivery")
	outputIndex := strings.Index(out, "## Output")
	if chatIndex < 0 || outboundIndex < chatIndex || outputIndex < outboundIndex {
		t.Fatalf("DWS chat outbound is not in the chat workflow region: chat=%d outbound=%d output=%d\n---\n%s", chatIndex, outboundIndex, outputIndex, out)
	}
	if !strings.Contains(out, workflowPrompt) || !strings.Contains(out, "DingTalk DWS reply") {
		t.Fatalf("DWS chat workflow missing outbound delivery instructions\n---\n%s", out)
	}
}

func TestRobotSDKDoesNotInjectAgentOutboundWorkflow(t *testing.T) {
	out := buildMetaSkillContent("hermes", TaskContextForEnv{
		ChatSessionID:          "chat-1",
		DispatchSurfaceType:    "chat",
		DispatchOutboundMode:   "robot_sdk",
		DispatchWorkflowPrompt: "must stay private from the agent workflow",
	})
	for _, banned := range []string{"#### Dispatch Outbound Delivery", "must stay private from the agent workflow"} {
		if strings.Contains(out, banned) {
			t.Fatalf("robot_sdk chat must not inject agent-owned outbound text %q\n---\n%s", banned, out)
		}
	}
}
