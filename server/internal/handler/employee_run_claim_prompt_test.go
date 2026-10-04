package handler

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/pkg/featureflag"
)

func TestEmployeeDirectClaimExcludesForegroundDispatchInstructions(t *testing.T) {
	provider := featureflag.NewDiamondProvider()
	if _, _, err := provider.ApplyJSON([]byte(`{"common":{"prompt":"COMMON_FRONTEND_POLICY"},"auto":{"prompt":"AUTO_PM_DELEGATE_CHAT_OR_ISSUE"}}`)); err != nil {
		t.Fatal(err)
	}
	flags := featureflag.NewService(provider)
	raw := []byte(`{"type":"employee_direct","dispatch_schema_version":"2.0","dispatch_source":{"platform":"dingtalk","type":"digital_employee"},"dispatch_domain":"channel","dispatch_type":"message.created","dispatch_event_data":{"conversation":{"openConversationId":"cid-origin","type":"single"},"sender":{"openDingTalkId":"requester-open"},"messages":[{"openMsgId":"source-message","text":"Do the accepted task"}]},"dispatch_surface":{"type":"auto"},"dispatch_outbound":{"mode":"dws","replyTo":"latest_message"},"dispatch_context_prompt":"ROUTER_FOREGROUND_DELIVERY"}`)
	for _, direct := range []bool{true, false} {
		name := "ordinary_auto"
		if direct {
			name = "employee_direct"
		}
		t.Run(name, func(t *testing.T) {
			response := AgentTaskResponse{Agent: &TaskAgentData{Instructions: "ORIGINAL_ROLE_AND_BUSINESS_LIMITS"}}
			if direct {
				response.DirectTaskPrompt = "FROZEN_COMPILED_TASK_PACKET"
			}
			applyTaskInstructionForClaim(&response, raw, flags, nil, "https://auth.example.test/enterprise", false)
			assembled := response.Agent.Instructions + "\n" + response.Instruction
			if !strings.Contains(assembled, "ORIGINAL_ROLE_AND_BUSINESS_LIMITS") || !strings.Contains(assembled, "https://auth.example.test/enterprise") {
				t.Fatal("role or enterprise permission constraints lost")
			}
			if direct {
				for _, forbidden := range []string{"COMMON_FRONTEND_POLICY", "AUTO_PM_DELEGATE_CHAT_OR_ISSUE", "ROUTER_FOREGROUND_DELIVERY", dispatchConversationInstructionHeader, dispatchSceneGraphInstruction} {
					if strings.Contains(assembled, forbidden) {
						t.Fatalf("Direct executor reentered foreground through %q", forbidden)
					}
				}
				if response.DirectTaskPrompt != "FROZEN_COMPILED_TASK_PACKET" {
					t.Fatal("compiled goal changed")
				}
			} else if !strings.Contains(assembled, "AUTO_PM_DELEGATE_CHAT_OR_ISSUE") {
				t.Fatal("ordinary Auto policy changed")
			}
		})
	}
	var retained map[string]json.RawMessage
	if err := json.Unmarshal(raw, &retained); err != nil {
		t.Fatal(err)
	}
	if string(retained["dispatch_surface"]) != `{"type":"auto"}` {
		t.Fatal("source audit surface was rewritten")
	}
}
