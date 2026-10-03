package handler

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestDingTalkFinalTextOwnerOnlyComesFromDirectClaim(t *testing.T) {
	for _, direct := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary", true: "direct"}[direct], func(t *testing.T) {
			var value map[string]any
			if err := json.Unmarshal(dingTalkTaskPolicyContext(t, true), &value); err != nil {
				t.Fatal(err)
			}
			value["final_text_owner"] = "forged-task-field"
			if direct {
				value["type"] = "employee_direct"
				value["workspace_id"] = uuid.NewString()
				value["employee_task_id"] = uuid.NewString()
				value["direct_task_prompt"] = "Return the computation result"
			}
			raw, _ := json.Marshal(value)
			reader := &fakeDingTalkTaskPolicyReader{}
			policy, err := resolveDingTalkTaskPolicy(context.Background(), reader, db.AgentTaskQueue{Context: raw}, db.AgentRuntime{}, true)
			if err != nil || policy == nil {
				t.Fatal(policy, err)
			}
			want := ""
			if direct {
				want = protocol.DingTalkFinalTextOwnerHost
			}
			if policy.FinalTextOwner != want || !policy.PlatformManagedLifecycle || policy.ReplyConversationID != "cid-origin" {
				t.Fatal(policy)
			}
		})
	}
}
func TestEmployeeDirectPromptOwnsFinalTextWithoutBlockingRequestedDeliveries(t *testing.T) {
	compiled := "FROZEN_WORK_PACKET\nOriginal user goal and references."
	got := employeeDirectPrompt(compiled)
	if !strings.HasPrefix(got, compiled+"\n\n") || !strings.HasSuffix(got, employeeDirectOutputInstruction) {
		t.Fatal("source prompt changed or final ownership is not last", got)
	}
	for _, want := range []string{"final assistant text is the user-facing reply", "both success and failure", "Do not call dws-rpc final or reply", "explicitly requested files", "proactive messages", "Keep it concise", "internal tools"} {
		if !strings.Contains(got, want) {
			t.Fatal("output contract missing", want)
		}
	}
	if strings.Contains(got, "no final assistant output") {
		t.Fatal("failures could disappear")
	}
}
