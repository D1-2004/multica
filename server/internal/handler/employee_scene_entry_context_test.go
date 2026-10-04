package handler

import (
	"github.com/multica-ai/multica/server/internal/coordinatorcontract"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"strings"
	"testing"
)

func TestEmployeeRoleInstructionsPreserveExplicitConstraints(t *testing.T) {
	instructions := "Analyze customer feedback. Never send the result outside this conversation without confirmation."
	got, err := employeeRoleInstructions(db.Agent{Instructions: instructions})
	if err != nil || got != instructions {
		t.Fatalf("configured responsibility/constraint lost: %q %v", got, err)
	}
}
func TestEmployeeRoleInstructionsUseCurrentContractOrFullInstructions(t *testing.T) {
	instructions := strings.Repeat("executor details ", 2000)
	if got, err := employeeRoleInstructions(db.Agent{Instructions: instructions}); err != nil || got != instructions {
		t.Fatalf("long configured instructions were gated or truncated: %v", err)
	}
	contract, err := coordinatorcontract.Bind(&coordinatorcontract.Contract{Version: 1, Scope: "customer feedback coordination", MustDelegate: []string{"Customer analysis goes to the executor"}, Constraints: []string{"Do not send outside the source scene"}, ClarifyWhen: []string{"The reporting interval is missing"}}, instructions)
	if err != nil {
		t.Fatal(err)
	}
	raw := coordinatorcontract.Marshal(contract)
	got, err := employeeRoleInstructions(db.Agent{Instructions: instructions, CoordinatorContract: raw})
	if err != nil || !strings.Contains(got, "Do not send outside") || strings.Contains(got, "executor details") {
		t.Fatalf("valid short role contract: %q %v", got, err)
	}
	if got, err = employeeRoleInstructions(db.Agent{Instructions: instructions + "new restriction", CoordinatorContract: raw}); err != nil || got != instructions+"new restriction" {
		t.Fatalf("stale short contract hid a new restriction: %v", err)
	}
}

func TestEmployeeMessageIdentityDoesNotBorrowAnotherSpeaker(t *testing.T) {
	command := DispatchCommand{Event: DispatchEvent{Data: DispatchEventData{Sender: DispatchSender{UID: "alice", StaffID: "staff-alice", OpenDingTalkID: "open-alice", DisplayName: "Alice"}, Messages: []DispatchMessage{{OpenMsgID: "bob-message", SenderStaffID: "staff-bob", Text: "My private request"}, {OpenMsgID: "alice-message", Text: "My request"}}}}}
	stampEmployeeMessages(&command)
	bob, alice := command.Event.Data.Messages[0], command.Event.Data.Messages[1]
	if bob.SenderUID != "" || bob.SenderOpenDingTalkID != "" || bob.SenderDisplayName != "" || employeeRequesterRef("org", bob) != "dingtalk:org:staff_id:staff-bob" {
		t.Fatalf("mixed two speaker identities: %+v", bob)
	}
	if alice.SenderUID != "" || employeeRequesterRef("org", alice) != "" {
		t.Fatalf("multi-message unknown speaker borrowed envelope actor: %+v", alice)
	}
	single := DispatchCommand{Event: DispatchEvent{Data: DispatchEventData{Sender: command.Event.Data.Sender, Messages: []DispatchMessage{{OpenMsgID: "single", Text: "One verified sender"}}}}}
	stampEmployeeMessages(&single)
	if employeeRequesterRef("org", single.Event.Data.Messages[0]) != "dingtalk:org:uid:alice" {
		t.Fatal("single verified envelope actor was lost")
	}
	if bob.Mentions != nil || alice.Mentions != nil {
		t.Fatal("unknown per-message mentions became a known empty audience")
	}
}
