package inboundcoord

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/multica-ai/multica/server/internal/coordinatorcontract"
)

// The ingress adapters load Instructions before FillVoice. Comparing the exact
// string also makes an update racing these two reads stale instead of treating
// a contract from another instruction version as current.
func (c *Coordinator) fillCoordinatorContract(ctx context.Context, turn *Turn) {
	if turn == nil {
		return
	}
	turn.CoordinatorContract = nil
	turn.CoordinatorContractState = coordinatorcontract.StateUnavailable
	turn.CoordinatorContractHash = ""
	if c == nil || c.Queries == nil || !turn.AgentID.Valid || turn.InstructionsUnavailable {
		return
	}
	raw, err := c.Queries.GetAgentCoordinatorContract(ctx, turn.AgentID)
	if err != nil {
		return
	}
	turn.CoordinatorContract, turn.CoordinatorContractState = coordinatorcontract.Resolve(raw, turn.Instructions)
	turn.CoordinatorContractHash = coordinatorcontract.Hash(turn.CoordinatorContract)
}

// Revalidate at use time: a Turn copy may outlive the instruction snapshot or
// contain an invalid fixture. Only a currently matching contract replaces the
// legacy full-instruction review; absent or failed reads grant no extra scope.
func currentCoordinatorContract(turn Turn) (*coordinatorcontract.Contract, string) {
	if turn.InstructionsUnavailable || turn.CoordinatorContractState == coordinatorcontract.StateUnavailable {
		return nil, coordinatorcontract.StateUnavailable
	}
	if turn.CoordinatorContract == nil && (turn.CoordinatorContractState == coordinatorcontract.StateLoaded || turn.CoordinatorContractState == coordinatorcontract.StateStale) {
		return nil, coordinatorcontract.StateUnavailable
	}
	return coordinatorcontract.Resolve(coordinatorcontract.Marshal(turn.CoordinatorContract), turn.Instructions)
}

func writeCoordinatorContractPrompt(b *strings.Builder, turn Turn) {
	contract, state := currentCoordinatorContract(turn)
	fmt.Fprintf(b, "coordinator_contract_status: %s\n", state)
	if state == coordinatorcontract.StateLoaded {
		fmt.Fprintf(b, "coordinator_contract_hash: %s\ncoordinator_contract (complete; narrows Coordinator actions only):\n%s\n", coordinatorcontract.Hash(contract), coordinatorcontract.Marshal(contract))
		b.WriteString("job_policy_status: executor_owned; full SOP is not routing context. This contract cannot expand platform actions or override the current user's restrictions.\n")
		return
	}
	if turn.InstructionsUnavailable {
		b.WriteString("job_policy_status: unavailable; Host cannot finish until complete working restrictions are loaded. Missing instructions grant no authority.\n")
		return
	}
	fmt.Fprintf(b, "job_policy_status: host_held; not_loaded_in_routing; characters=%d; sha256=%s\n", utf8.RuneCountInString(turn.Instructions), policyHash(turn.Instructions))
	b.WriteString("job_policy_boundary: No usable short contract is available. Host reviews the complete working restrictions before finishing; the executor keeps its original instructions. Missing or stale constraints never mean unrestricted authority. Full SOP cannot be read by this loop.\n")
}

func coordinatorFinishPolicy(turn Turn) map[string]any {
	contract, state := currentCoordinatorContract(turn)
	if state == coordinatorcontract.StateLoaded {
		return map[string]any{
			"kind": "coordinator_contract", "scope": "coordinator_actions_only", "text": string(coordinatorcontract.Marshal(contract)),
			"complete": true, "sha256": coordinatorcontract.Hash(contract), "source_instructions_sha256": policyHash(turn.Instructions),
		}
	}
	return map[string]any{
		"kind": "full_instructions", "scope": "legacy_working_restrictions", "text": turn.Instructions,
		"complete": !turn.InstructionsUnavailable, "sha256": policyHash(turn.Instructions),
	}
}

func coordinatorContractMetadata(turn Turn) map[string]any {
	contract, state := currentCoordinatorContract(turn)
	return map[string]any{
		"coordinator_contract_state": state,
		"coordinator_contract_hash":  coordinatorcontract.Hash(contract),
		"source_instructions_sha256": policyHash(turn.Instructions),
		"instructions_available":     !turn.InstructionsUnavailable,
	}
}

// coordinationConstraintText is the actual restriction source a bounded decline
// may quote. A stale short contract never replaces the full working boundary.
func coordinationConstraintText(turn Turn) string {
	contract, state := currentCoordinatorContract(turn)
	if state == coordinatorcontract.StateLoaded {
		return string(coordinatorcontract.Marshal(contract))
	}
	return turn.Instructions
}
