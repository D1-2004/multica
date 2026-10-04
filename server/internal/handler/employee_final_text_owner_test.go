package handler

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/humanquestion"
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
	for _, want := range []string{"result or actionable failure as final assistant text", "The Host delivers it", "verified file-only/no-summary requests", "do not send the same reply through tools", "explicitly requested files", "proactive messages", "verified receipts", "local paths are not delivered files", "tag-round-result/v1", "summary is the user-facing result", "Do not replace the required JSON with a raw reply", "do not inspect CLI help"} {
		if !strings.Contains(got, want) {
			t.Fatal("output contract missing", want)
		}
	}
	if strings.Contains(got, "no final assistant output") {
		t.Fatal("failures could disappear")
	}
	for _, want := range []string{"plain final assistant text", "result or actionable failure", "The Host owns terminal delivery and any configured notices", "do not send it to the routine's scene yourself", "only when explicitly requested by the routine", "verified receipts"} {
		if !strings.Contains(employeeRoutineOutputInstruction, want) {
			t.Fatal("routine output contract missing", want)
		}
	}
}

// Regression for a Direct run investigating transport or replacing its required
// round-result object with raw user-facing prose.
func TestEmployeeDirectClaimPreservesStructuredResultContract(t *testing.T) {
	compiled := "Current work; output contract: tag-round-result/v1 with summary."
	prompt := employeeDirectPrompt(compiled)
	if !strings.HasPrefix(prompt, compiled+"\n\n") || !strings.Contains(prompt, "the Host parses the object and sends the summary") || !strings.Contains(prompt, "Do not replace the required JSON with a raw reply") {
		t.Fatalf("claim guidance contradicts the selected result contract: %s", prompt)
	}
	if strings.Contains(prompt, "Your final assistant text is the user-facing reply") || !strings.Contains(prompt, "do not inspect CLI help, binaries, platform internals or transport formats") {
		t.Fatalf("claim guidance invites raw delivery or transport discovery: %s", prompt)
	}
	if !strings.Contains(employeeDirectPrompt("Legacy unstructured packet"), "If this run has no structured output contract") {
		t.Fatal("legacy result parser was given an unsupported JSON contract")
	}
	if !strings.Contains(employeeRoutineOutputInstruction, "a one-shot occurrence has no start notice") || strings.Contains(employeeRoutineOutputInstruction, "attaches your final output") {
		t.Fatal("routine claim guidance misstates one-shot notices or result parsing")
	}
}

// A creation run's round-result contract remains historical material when the
// admitted routine's consumer expects a plain result. Business JSON is separate.
func TestRoutineClaimDoesNotInheritHistoricalTagResultProtocol(t *testing.T) {
	historical := "Original work packet (historical data):\n" + humanquestion.PromptContract
	prompt := historical + "\n\n" + employeeRoutineOutputInstruction
	if !strings.HasPrefix(prompt, historical) || !strings.HasSuffix(prompt, employeeRoutineOutputInstruction) {
		t.Fatal("historical material changed or current claim contract is not last")
	}
	current := prompt[len(historical):]
	if !strings.Contains(current, "Current routine result contract: return plain final assistant text") || !strings.Contains(current, "they are not this occurrence's output protocol") || !strings.Contains(current, "Do not wrap it in a tag-round-result/v1 control envelope or add choice") {
		t.Fatal("historical Tag protocol can override the current plain-text consumer", current)
	}
	if strings.Contains(current, "When it requires tag-round-result/v1") || !strings.Contains(current, "does not prohibit business JSON data or JSON files") {
		t.Fatal("routine contract is ambiguous or forbids requested JSON deliverables", current)
	}
}
