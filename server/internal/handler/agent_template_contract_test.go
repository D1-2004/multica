package handler

import (
	"encoding/json"
	"testing"

	"github.com/multica-ai/multica/server/internal/agenttmpl"
	"github.com/multica-ai/multica/server/internal/coordinatorcontract"
)

func TestTemplateCoordinatorContractPreservesBindingUntilExplicitlyReplaced(t *testing.T) {
	bound, err := coordinatorcontract.Bind(&coordinatorcontract.Contract{Version: 1, Scope: "Route product questions"}, "original SOP")
	if err != nil {
		t.Fatal(err)
	}
	tmpl := agenttmpl.Template{Instructions: "original SOP", CoordinatorContract: bound}
	for _, tc := range []struct {
		name         string
		raw          json.RawMessage
		instructions string
		state        string
	}{
		{"inherit", nil, "original SOP", coordinatorcontract.StateLoaded},
		{"changed instructions without renewed contract", nil, "new SOP", coordinatorcontract.StateStale},
		{"copied stale contract", coordinatorcontract.Marshal(bound), "new SOP", coordinatorcontract.StateStale},
		{"explicit replacement", json.RawMessage(`{"version":1,"scope":"Route new product questions"}`), "new SOP", coordinatorcontract.StateLoaded},
		{"explicit clear", json.RawMessage(`null`), "original SOP", coordinatorcontract.StateNotConfigured},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := templateCoordinatorContract(tmpl, tc.raw, tc.instructions)
			if err != nil {
				t.Fatal(err)
			}
			if _, state := coordinatorcontract.Resolve(raw, tc.instructions); state != tc.state {
				t.Fatalf("state = %s, want %s", state, tc.state)
			}
		})
	}
	if _, err := templateCoordinatorContract(tmpl, json.RawMessage(`{"version":1,"scope":"route","allow_reply":true}`), "new SOP"); err == nil {
		t.Fatal("unknown contract field accepted")
	}
	if _, state := coordinatorcontract.Resolve(coordinatorcontract.Marshal(tmpl.CoordinatorContract), "original SOP"); state != coordinatorcontract.StateLoaded {
		t.Fatal("request mutated shared template contract")
	}
	response := templateToDetail(tmpl)
	if response.CoordinatorContract == nil || response.CoordinatorContract.SourceInstructionsSHA256 != bound.SourceInstructionsSHA256 {
		t.Fatal("template detail lost authored contract")
	}
}
