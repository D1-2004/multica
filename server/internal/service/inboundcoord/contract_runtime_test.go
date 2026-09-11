package inboundcoord

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/coordinatorcontract"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	openai "github.com/openai/openai-go/v3"
)

func runtimeTestContract(t *testing.T, instructions string) *coordinatorcontract.Contract {
	t.Helper()
	bound, err := coordinatorcontract.Bind(&coordinatorcontract.Contract{
		Version: 1, Scope: "Only coordinate product questions",
		MustDelegate: []string{"Product facts require executor research"},
		Constraints:  []string{"Only draft; never send without current approval"},
		ClarifyWhen:  []string{"The intended recipient is unknown"},
	}, instructions)
	if err != nil {
		t.Fatal(err)
	}
	return bound
}

func TestCoordinatorContractFillVoiceTracksRawInstructionsAndFailures(t *testing.T) {
	const instructions = " \nFull executor SOP.\n "
	contract := runtimeTestContract(t, instructions)
	for _, loop := range []Loop{LoopInbound, LoopTaskFinished} {
		for _, tc := range []struct {
			name         string
			raw          []byte
			err          error
			instructions string
			state        string
		}{
			{"matching raw instructions", coordinatorcontract.Marshal(contract), nil, instructions, coordinatorcontract.StateLoaded},
			{"trimmed instructions differ", coordinatorcontract.Marshal(contract), nil, strings.TrimSpace(instructions), coordinatorcontract.StateStale},
			{"missing", nil, nil, instructions, coordinatorcontract.StateNotConfigured},
			{"invalid", []byte(`{"version":1,"scope":"route","allow_reply":true}`), nil, instructions, coordinatorcontract.StateUnavailable},
			{"read failure clears previous snapshot", nil, context.DeadlineExceeded, instructions, coordinatorcontract.StateUnavailable},
		} {
			t.Run(string(loop)+"/"+tc.name, func(t *testing.T) {
				turn := Turn{Loop: loop, AgentID: testAgentID(), Instructions: tc.instructions, CoordinatorContract: contract, CoordinatorContractState: coordinatorcontract.StateLoaded, CoordinatorContractHash: "old"}
				c := &Coordinator{Queries: &coordQueriesStub{contract: tc.raw, contractErr: tc.err}}
				c.FillVoice(context.Background(), &turn)
				if turn.CoordinatorContractState != tc.state {
					t.Fatalf("state=%s want=%s", turn.CoordinatorContractState, tc.state)
				}
				if turn.Instructions != tc.instructions {
					t.Fatal("FillVoice altered executor instructions")
				}
				if tc.state == coordinatorcontract.StateUnavailable && (turn.CoordinatorContract != nil || turn.CoordinatorContractHash != "") {
					t.Fatal("failed read retained prior trusted contract")
				}
				if tc.state == coordinatorcontract.StateLoaded && turn.CoordinatorContractHash != coordinatorcontract.Hash(contract) {
					t.Fatal("loaded contract hash missing")
				}
			})
		}
	}
}

func TestTurnFromChatSessionLoadsInstructionsBeforeContract(t *testing.T) {
	const instructions = " \nExact raw job instructions.\n "
	contract := runtimeTestContract(t, instructions)
	c := &Coordinator{Queries: &coordQueriesStub{agent: db.Agent{Instructions: instructions}, contract: coordinatorcontract.Marshal(contract)}}
	turn := c.TurnFromChatSession(context.Background(), testSession(), SourceWeb, true, "p2p", "", "User", "Check product facts")
	if turn.CoordinatorContractState != coordinatorcontract.StateLoaded || turn.Instructions != instructions {
		t.Fatalf("instructions were not loaded before contract: %s", turn.CoordinatorContractState)
	}
}

func TestCoordinatorContractSeparatesRoutingFromExecutorAndFallbackReview(t *testing.T) {
	instructions := " \n" + strings.Repeat("EXECUTOR_ONLY_DETAILS ", 1000) + "Only draft, never send.\n "
	contract := runtimeTestContract(t, instructions)
	for _, loop := range []Loop{LoopInbound, LoopTaskFinished} {
		for _, state := range []string{coordinatorcontract.StateLoaded, coordinatorcontract.StateStale, coordinatorcontract.StateNotConfigured, coordinatorcontract.StateUnavailable} {
			t.Run(string(loop)+"/"+state, func(t *testing.T) {
				turn := Turn{Loop: loop, Instructions: instructions, CoordinatorContract: contract, CoordinatorContractState: state, Message: "Check product facts", TaskResult: "Checked result", IssueID: "current-issue"}
				if state == coordinatorcontract.StateStale {
					turn.Instructions += "New restriction."
				}
				if state == coordinatorcontract.StateNotConfigured {
					turn.CoordinatorContract = nil
				}
				prompt := buildUserPrompt(turn)
				if strings.Contains(prompt, "EXECUTOR_ONLY_DETAILS") || strings.Contains(prompt, "kind=job_policy") {
					t.Fatal("routing exposed full SOP or a read escape")
				}
				if !strings.Contains(prompt, "coordinator_contract_status: "+state) {
					t.Fatalf("missing state in routing: %s", state)
				}
				policy := coordinatorFinishPolicy(turn)
				if state == coordinatorcontract.StateLoaded {
					if !strings.Contains(prompt, contract.MustDelegate[0]) || policy["kind"] != "coordinator_contract" || strings.Contains(policy["text"].(string), "EXECUTOR_ONLY_DETAILS") {
						t.Fatal("loaded contract must be the complete bounded Coordinator policy")
					}
					if coordinationConstraintText(turn) != policy["text"] {
						t.Fatal("decline boundary differs from reviewed restrictions")
					}
				} else if policy["kind"] != "full_instructions" || policy["text"] != turn.Instructions || coordinationConstraintText(turn) != turn.Instructions {
					t.Fatal("unusable contract removed full legacy working restrictions")
				}
				if policy["complete"] != true {
					t.Fatal("review policy was truncated")
				}
				if loop == LoopTaskFinished && !strings.Contains(prompt, currentResultRef(turn)) {
					t.Fatal("completion is missing its current result reference")
				}
			})
		}
	}
}

func TestCoordinatorContractFinishRequestUsesSelectedPolicy(t *testing.T) {
	const instructions = " \nExecutor steps. Never send a draft without approval.\n "
	contract := runtimeTestContract(t, instructions)
	for _, loop := range []Loop{LoopInbound, LoopTaskFinished} {
		for _, configured := range []bool{true, false} {
			name := string(loop) + "/legacy"
			if configured {
				name = string(loop) + "/contract"
			}
			t.Run(name, func(t *testing.T) {
				turn := Turn{Loop: loop, Source: SourceDigitalEmployee, Message: "请报告处理状态", Instructions: instructions, TaskResult: "已查到官方证据", TaskDeliveryContext: "Current result is not delivered.", IssueID: "current-issue"}
				if configured {
					turn.CoordinatorContract = contract
					turn.CoordinatorContractState = coordinatorcontract.StateLoaded
				}
				chat := &scriptedCompleter{checkRounds: []openai.ChatCompletion{scriptedFinishVerdict("allow", "The selected restrictions are respected.")}}
				_, err := (&Coordinator{Chat: chat}).checkFinish(context.Background(), turn, Decision{Action: ActionReply, UserText: "处理已完成。", CoordinationActions: []CoordinationAction{{Kind: "report_status", SourceRefs: []string{"u1"}, StateRefs: []string{"r1"}, Reply: "处理已完成。"}}}, nil, 0, nil)
				if err != nil || len(chat.checkParams) != 1 {
					t.Fatalf("review missing: calls=%d err=%v", len(chat.checkParams), err)
				}
				// Messages[1] is the Agent configuration segment (job policy),
				// Messages[2] the turn context (current task result).
				var input struct {
					Policy   map[string]any `json:"job_policy"`
					Result   string         `json:"current_task_result"`
					Ref      string         `json:"current_result_ref"`
					Delivery string         `json:"task_delivery_context"`
				}
				for _, index := range []int{1, 2} {
					raw, _ := json.Marshal(chat.checkParams[0].Messages[index])
					var message struct{ Content string }
					if err := json.Unmarshal(raw, &message); err != nil {
						t.Fatal(err)
					}
					if err := json.Unmarshal([]byte(message.Content), &input); err != nil {
						t.Fatal(err)
					}
				}
				if configured && input.Policy["kind"] != "coordinator_contract" {
					t.Fatal("review did not use the short contract")
				}
				if !configured && input.Policy["text"] != instructions {
					t.Fatal("review dropped raw legacy instructions")
				}
				if loop == LoopTaskFinished && (input.Result != turn.TaskResult || input.Ref != currentResultRef(turn) || input.Delivery != turn.TaskDeliveryContext) {
					t.Fatal("completion reviewer lacks exact current result or delivery evidence")
				}
			})
		}
	}
}

func TestCoordinatorContractDisablesFullSOPToolRead(t *testing.T) {
	turn := Turn{Source: SourceDigitalEmployee, Instructions: "Only draft", HistoryStatus: "loaded"}
	if _, err := (&Coordinator{}).readHistoryContext(context.Background(), &turn, `{"kind":"job_policy"}`); err == nil {
		t.Fatal("full job policy read still exists")
	}
	raw, err := json.Marshal(contextReadTool())
	if err != nil || strings.Contains(string(raw), "job_policy") {
		t.Fatalf("tool still exposes job_policy: %s %v", raw, err)
	}
	if names := toolParamNames(toolsForDisclosure(turn, 0, false)); slices.Contains(names, toolContextRead) {
		t.Fatal("SOP presence still exposes an otherwise unnecessary context read")
	}
}

func TestCoordinatorContractCannotReviewAfterInstructionsReadFails(t *testing.T) {
	c := &Coordinator{Queries: &coordQueriesStub{agentErr: context.DeadlineExceeded}, Chat: &scriptedCompleter{}}
	turn := c.TurnFromChatSession(context.Background(), testSession(), SourceWeb, true, "p2p", "", "User", "Check product facts")
	if !turn.InstructionsUnavailable || turn.CoordinatorContractState != coordinatorcontract.StateUnavailable {
		t.Fatal("failed instruction read was treated as an empty working policy")
	}
	if !strings.Contains(buildUserPrompt(turn), "job_policy_status: unavailable") || coordinatorFinishPolicy(turn)["complete"] != false {
		t.Fatal("failed policy load was presented as complete")
	}
	if _, err := c.checkFinish(context.Background(), turn, Decision{Action: ActionReply, UserText: "No restrictions apply.", CoordinationActions: []CoordinationAction{{Kind: "describe_capabilities", SourceRefs: []string{"u1"}, Reply: "No restrictions apply."}}}, nil, 0, nil); err == nil {
		t.Fatal("finish was reviewed without loading working restrictions")
	}
	if c.Chat.(*scriptedCompleter).checkCalls != 0 {
		t.Fatal("review model was called with incomplete working restrictions")
	}
}
