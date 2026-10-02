package employeetask

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/scene"
)

func compilerFixture() CompileInput {
	scope := Scope{WorkspaceID: "00000000-0000-4000-8000-000000000001", AgentID: "00000000-0000-4000-8000-000000000002", TenantOrgID: "org-a", Kind: ScopeScene, Scene: scene.Ref{SceneID: "00000000-0000-4000-8000-000000000003"}}
	principal := "00000000-0000-4000-8000-000000000004"
	material := func(ref, body string) PacketMaterial {
		return PacketMaterial{Ref: ref, Scope: scope, PrincipalID: principal, Body: body}
	}
	return CompileInput{
		Scope: scope, PrincipalID: principal, Definition: Definition{Goal: "  ship a report  ", Deliverables: []string{"markdown report"}, SuccessCriteria: []string{"numbers match the ledger"}, AccessNeeded: []string{"finance.write"}}, Prompt: "Model-proposed steps: inspect the ledger; I claim finance.write.",
		Source:         material("message:source", "Please check last month's numbers."),
		Corrections:    []PacketMaterial{material("message:correction", "Current correction: use September, do not send any message.")},
		CompletedSteps: []PacketMaterial{material("run:completed", "September invoices have already been imported.")},
		References:     []PacketMaterial{material("document:ledger", "Formal ledger: document://ledger-September"), material("document:unused", "")},
		History:        PacketHistory{State: HistoryAvailable, Items: []PacketMaterial{material("message:history", "Earlier scope discussed August.")}},
		Capabilities:   []string{"finance.read"}, ReturnAddress: "employee-job:job-1/source:message-source",
	}
}

// Adapted from gawkbot TestExecutionPacketCarriesDefinition and the execution
// packet context manifest: the actual assembled packet is the observable contract.
func TestCompileWorkPacketCarriesDefinitionAndHostFacts(t *testing.T) {
	input := compilerFixture()
	packet, err := Compile(input)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Work packet:", "DEFINITION (the contract you execute against):", "Goal: ship a report", "markdown report", "1. numbers match the ledger", "Access needed (requested, not granted): finance.write", input.Corrections[0].Body, input.CompletedSteps[0].Body, input.References[0].Body, input.Source.Body, "ACTUAL CAPABILITIES (Host verified): finance.read", input.ReturnAddress, "History: available"} {
		if !strings.Contains(packet.Text, want) {
			t.Errorf("packet omitted %q", want)
		}
	}
	if strings.Index(packet.Text, input.Corrections[0].Body) > strings.Index(packet.Text, "DEFINITION") {
		t.Fatal("current correction was buried behind older task definition")
	}
	wantRefs := []string{"message:correction", "message:source", "run:completed", "document:ledger", "message:history"}
	if !reflect.DeepEqual(packet.ContextUsed, wantRefs) {
		t.Fatalf("manifest=%v, want actual rendered refs %v", packet.ContextUsed, wantRefs)
	}
	if packet.Definition.Goal != "ship a report" || packet.Scope != input.Scope || packet.PrincipalID != input.PrincipalID {
		t.Fatalf("Host scope/definition changed: %+v", packet)
	}
	if strings.Contains(packet.Text, "ACTUAL CAPABILITIES (Host verified): finance.write") {
		t.Fatal("requested access was promoted to capability")
	}
}

func TestCompileRejectsCrossPrincipalAndCrossScopeMaterials(t *testing.T) {
	for _, field := range []string{"source", "correction", "completed", "reference", "history"} {
		for _, boundary := range []string{"principal", "scope"} {
			t.Run(field+"/"+boundary, func(t *testing.T) {
				input := compilerFixture()
				var material *PacketMaterial
				switch field {
				case "source":
					material = &input.Source
				case "correction":
					material = &input.Corrections[0]
				case "completed":
					material = &input.CompletedSteps[0]
				case "reference":
					material = &input.References[0]
				case "history":
					material = &input.History.Items[0]
				}
				if boundary == "principal" {
					material.PrincipalID = "00000000-0000-4000-8000-000000000009"
				} else {
					material.Scope.TenantOrgID = "org-other"
				}
				material.Body = "PRIVATE_OTHER_PRINCIPAL"
				packet, err := Compile(input)
				if !errors.Is(err, ErrInvalid) || packet.Text != "" || len(packet.ContextUsed) != 0 {
					t.Fatalf("foreign material escaped: err=%v packet=%+v", err, packet)
				}
			})
		}
	}
}

func TestCompileKeepsUnavailableEmptyAndTruncatedHistoryDistinct(t *testing.T) {
	for _, state := range []HistoryState{HistoryUnavailable, HistoryEmpty, HistoryTruncated} {
		t.Run(string(state), func(t *testing.T) {
			input := compilerFixture()
			input.History = PacketHistory{State: state}
			if state == HistoryTruncated {
				input.History.Items = []PacketMaterial{input.Source}
				input.History.Items[0].Ref = "history:partial"
			}
			packet, err := Compile(input)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(packet.Text, "History: "+string(state)) {
				t.Fatalf("history state lost: %s", packet.Text)
			}
			if state == HistoryUnavailable && !strings.Contains(packet.Text, "not evidence of empty history") {
				t.Fatal("unavailable could be mistaken for empty")
			}
			if state == HistoryTruncated && !strings.Contains(packet.Text, "partial context") {
				t.Fatal("truncation was hidden")
			}
		})
	}
	input := compilerFixture()
	input.History.State = "unknown"
	if _, err := Compile(input); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown history guessed: %v", err)
	}
}

func TestCompileIsPureStableAndDoesNotClipHumanConstraints(t *testing.T) {
	input := compilerFixture()
	input.Corrections[0].Body = strings.Repeat("context ", 3000) + "STOP: do not execute until a human resumes."
	before, _ := json.Marshal(input)
	first, err := Compile(input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Compile(input)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(input)
	if string(before) != string(after) || !reflect.DeepEqual(first, second) {
		t.Fatal("compiler mutated inputs or depended on hidden state")
	}
	if !strings.Contains(first.Text, input.Corrections[0].Body) {
		t.Fatal("human correction was truncated")
	}
	if !strings.Contains(first.Text, "does not resume stopped work") {
		t.Fatal("compiler packet could be mistaken for a resume")
	}
	input.Definition.Deliverables[0] = "mutated"
	if first.Definition.Deliverables[0] == "mutated" {
		t.Fatal("compiled packet aliases definition")
	}
}

func TestCompileGoalOnlyDoesNotInventAccessOrDeliverables(t *testing.T) {
	input := compilerFixture()
	input.Definition = Definition{Goal: "Reply with a summary"}
	input.Prompt = ""
	input.Corrections = nil
	input.CompletedSteps = nil
	input.References = nil
	input.Capabilities = nil
	input.History = PacketHistory{State: HistoryEmpty}
	packet, err := Compile(input)
	if err != nil {
		t.Fatal(err)
	}
	if packet.Definition.Goal != input.Definition.Goal || len(packet.Definition.Deliverables) != 0 || len(packet.Definition.SuccessCriteria) != 0 || len(packet.Definition.AccessNeeded) != 0 {
		t.Fatal("compiler invented optional contract fields")
	}
	if !strings.Contains(packet.Text, "ACTUAL CAPABILITIES (Host verified): none declared") {
		t.Fatal("empty capability set was not explicit")
	}
}
