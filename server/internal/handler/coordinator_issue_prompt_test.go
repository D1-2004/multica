package handler

import (
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/service/inboundcoord"
	"github.com/multica-ai/multica/server/pkg/featureflag"
)

// coordinatorIssueClaimContextForTest is the persisted context of an Issue task
// the Coordinator created from a digital-employee DingTalk dispatch, exactly as
// IndependentIssueTaskContext derives it.
func coordinatorIssueClaimContextForTest(t *testing.T) []byte {
	t.Helper()
	raw := dispatchTaskContextWithPromptForTest(t, DispatchCommand{
		SchemaVersion: "2.0",
		Source:        DispatchSource{Platform: "dingtalk", Type: "digital_employee"},
		Event: DispatchEvent{Domain: "channel", Type: "message.created", Data: DispatchEventData{
			Conversation: DispatchConversation{OpenConversationID: "cid-requester", Type: "single"},
			Sender:       DispatchSender{DisplayName: "须莫", OpenDingTalkID: "open-requester"},
			Messages:     []DispatchMessage{{OpenMsgID: "msg-current", Text: "帮我查一下"}},
		}},
		Surface:  DispatchSurface{Type: "chat"},
		Outbound: DispatchOutbound{Mode: "dws", ReplyTo: "latest_message"},
		CompletionCallback: &DispatchCompletionCallback{
			URL: "/api/v1/dispatch-tasks/router-task/execution-result",
		},
	}, "ROUTER CONTEXT")
	encoded, err := inboundcoord.IndependentIssueTaskContext(raw, inboundcoord.CoordinatorIssueTriggerCreate)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func coordinatorIssueDiamondFlagsForTest(t *testing.T) *featureflag.Service {
	t.Helper()
	provider := featureflag.NewDiamondProvider()
	if _, _, err := provider.ApplyJSON([]byte(`{
	  "common":{"prompt":"COMMON POLICY"},
	  "issue":{"prompt":"ISSUE MODE POLICY"}
	}`)); err != nil {
		t.Fatalf("seed Diamond prompts: %v", err)
	}
	return featureflag.NewService(provider)
}

// An Issue task the Coordinator created is plain Issue work: the short loop
// already consumed the inbound dispatch, so the claim must not re-inject the
// dispatch-mode policy (Diamond common + issue, or the Agent's replacement for
// it) nor the Router's short-loop delivery facts. The DingTalk facts the task
// needs to reach people stay.
func TestCoordinatorIssueClaimSkipsDispatchPolicyAndRouterContext(t *testing.T) {
	context := coordinatorIssueClaimContextForTest(t)
	stored, present := parsePersistedDispatchContext(context)
	if !present || !stored.CoordinatorIssueFollowUp {
		t.Fatalf("coordinator Issue context was not recognized: present=%v stored=%+v", present, stored)
	}

	// Both the managed Diamond composition and an Agent-authored replacement
	// are gated: the override replaces the policy, it does not re-enable it.
	for name, overrides := range map[string]map[string]string{
		"diamond":  nil,
		"override": {DispatchSegmentPolicy: "AGENT AUTHORED POLICY"},
	} {
		segments := composeDispatchInstructionSegments(dispatchInstructionInputs{
			Stored:                     stored,
			Present:                    present,
			DingTalkContext:            true,
			Flags:                      coordinatorIssueDiamondFlagsForTest(t),
			Overrides:                  overrides,
			EnterpriseAuthorizationURL: "https://multica.example/login?next=%2Fws%2Fagents%2Fa",
		})
		byID := map[string]DispatchPromptSegment{}
		for _, segment := range segments {
			byID[segment.ID] = segment
		}
		for _, id := range []string{DispatchSegmentPolicy, DispatchSegmentContext} {
			segment := byID[id]
			if segment.Included {
				t.Errorf("%s: segment %q reached a coordinator Issue task: %+v", name, id, segment)
			}
			// The literal is the API contract the settings dialog maps to copy.
			if segment.ExcludedReason != "coordinator_issue" {
				t.Errorf("%s: segment %q excluded_reason = %q, want %q", name, id, segment.ExcludedReason, "coordinator_issue")
			}
		}
		for _, id := range []string{
			DispatchSegmentDingTalkConversation,
			DispatchSegmentSceneGraph,
			DispatchSegmentReplyFormatting,
			DispatchSegmentEnterpriseIdentity,
		} {
			if !byID[id].Included {
				t.Errorf("%s: segment %q was dropped from a coordinator Issue task: %+v", name, id, byID[id])
			}
		}
	}

	response := AgentTaskResponse{Agent: &TaskAgentData{Instructions: "AGENT BRIEF"}}
	applyTaskInstructionForClaim(&response, context, coordinatorIssueDiamondFlagsForTest(t),
		map[string]string{DispatchSegmentPolicy: "AGENT AUTHORED POLICY"}, "", false)
	combined := response.Agent.Instructions + "\n" + response.Instruction
	for _, leaked := range []string{"COMMON POLICY", "ISSUE MODE POLICY", "AGENT AUTHORED POLICY", "ROUTER CONTEXT"} {
		if strings.Contains(combined, leaked) {
			t.Errorf("coordinator Issue claim still carries %q:\n%s", leaked, combined)
		}
	}
	if !strings.HasPrefix(response.Agent.Instructions, "AGENT BRIEF") {
		t.Fatalf("agent brief was replaced: %q", response.Agent.Instructions)
	}
	for _, want := range []string{
		"## Scene graph",
		"## DingTalk Reply Formatting",
	} {
		if !strings.Contains(response.Agent.Instructions, want) {
			t.Errorf("runtime brief missing %q:\n%s", want, response.Agent.Instructions)
		}
	}
	for _, want := range []string{
		"## DingTalk Conversation",
		"short loop already closed the inbound acknowledgement",
		"dingtalk_sender_name\":\"须莫",
	} {
		if !strings.Contains(response.Instruction, want) {
			t.Errorf("per-turn instruction missing %q:\n%s", want, response.Instruction)
		}
	}
}

// The same dispatch that is not a coordinator follow-up keeps receiving the
// policy and the Router context, so the gate is the follow-up flag and nothing
// else in the envelope.
func TestDirectDispatchClaimStillCarriesDispatchPolicyAndRouterContext(t *testing.T) {
	context := dispatchTaskContextWithPromptForTest(t, DispatchCommand{
		SchemaVersion: "2.0",
		Source:        DispatchSource{Platform: "dingtalk", Type: "digital_employee"},
		Event: DispatchEvent{Domain: "channel", Type: "message.created", Data: DispatchEventData{
			Conversation: DispatchConversation{OpenConversationID: "cid-requester", Type: "single"},
			Sender:       DispatchSender{DisplayName: "须莫", OpenDingTalkID: "open-requester"},
			Messages:     []DispatchMessage{{OpenMsgID: "msg-current", Text: "帮我查一下"}},
		}},
		Surface:  DispatchSurface{Type: "issue"},
		Outbound: DispatchOutbound{Mode: "dws", ReplyTo: "latest_message"},
	}, "ROUTER CONTEXT")

	response := AgentTaskResponse{Agent: &TaskAgentData{Instructions: "AGENT BRIEF"}}
	applyTaskInstructionForClaim(&response, context, coordinatorIssueDiamondFlagsForTest(t), nil, "", false)
	for _, want := range []string{"COMMON POLICY", "ISSUE MODE POLICY"} {
		if !strings.Contains(response.Agent.Instructions, want) {
			t.Errorf("direct dispatch brief missing %q:\n%s", want, response.Agent.Instructions)
		}
	}
	if !strings.Contains(response.Instruction, "ROUTER CONTEXT") {
		t.Errorf("direct dispatch per-turn instruction missing the Router context:\n%s", response.Instruction)
	}
}

func TestCoordinatorIssueLegacyClaimSkipsDispatchPolicy(t *testing.T) {
	stored, present := parsePersistedDispatchContext(coordinatorIssueClaimContextForTest(t))
	if !present {
		t.Fatal("coordinator Issue context was not recognized")
	}
	instruction := buildLegacyDispatchInstruction(stored, coordinatorIssueDiamondFlagsForTest(t), "AGENT AUTHORED POLICY")
	for _, leaked := range []string{"COMMON POLICY", "ISSUE MODE POLICY", "AGENT AUTHORED POLICY", "ROUTER CONTEXT"} {
		if strings.Contains(instruction, leaked) {
			t.Errorf("legacy coordinator Issue instruction still carries %q:\n%s", leaked, instruction)
		}
	}
	if !strings.Contains(instruction, "short loop already closed the inbound acknowledgement") {
		t.Errorf("legacy coordinator Issue instruction lost the follow-up contract:\n%s", instruction)
	}

	// The summary branch adopts the Router context verbatim for a direct
	// dispatch; the Coordinator gate must win over it too.
	summary := stored
	summary.Type = dispatchEventTypeConversationSummary
	summaryInstruction := buildLegacyDispatchInstruction(summary, coordinatorIssueDiamondFlagsForTest(t), "")
	if strings.Contains(summaryInstruction, "ROUTER CONTEXT") {
		t.Errorf("legacy summary branch leaked the Router context into a coordinator Issue task:\n%s", summaryInstruction)
	}
}
