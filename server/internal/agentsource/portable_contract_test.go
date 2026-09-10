package agentsource

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/multica-ai/multica/server/internal/coordinatorcontract"
)

func TestPortableCoordinatorContractPreservesAuthoredAndCopiedVersions(t *testing.T) {
	const instructions = " \nCurrent executor instructions.\n "
	old, err := coordinatorcontract.Bind(&coordinatorcontract.Contract{Version: 1, Scope: "Coordinate only", Constraints: []string{"Draft only"}}, "old instructions")
	if err != nil {
		t.Fatal(err)
	}
	for _, version := range []string{"multica.agent/v1", "multica.agent/v2"} {
		for _, tc := range []struct {
			name  string
			raw   json.RawMessage
			state string
		}{
			{"authored", json.RawMessage(`{"version":1,"scope":"Coordinate only","constraints":["Draft only"]}`), coordinatorcontract.StateLoaded},
			{"copied stale", coordinatorcontract.Marshal(old), coordinatorcontract.StateStale},
			{"explicit null", json.RawMessage(`null`), coordinatorcontract.StateNotConfigured},
		} {
			t.Run(version+"/"+tc.name, func(t *testing.T) {
				definition := map[string]any{"$schema": PortableSchemaPath, "version": version, "name": "Contract source", "instructions": "AGENTS.md", "skills": []any{}, "coordinator_contract": tc.raw}
				archive, err := ExportAgentPackage(context.Background(), definition, instructions, nil, nil)
				if err != nil {
					t.Fatal(err)
				}
				parsed, err := ParseAgentPackage(context.Background(), archive)
				if err != nil {
					t.Fatal(err)
				}
				bundle, err := parsed.Bundle()
				if err != nil {
					t.Fatal(err)
				}
				if bundle.Instructions != instructions {
					t.Fatal("raw executor instructions changed")
				}
				if _, state := coordinatorcontract.Resolve(coordinatorcontract.Marshal(bundle.CoordinatorContract), instructions); state != tc.state {
					t.Fatalf("state=%s want=%s", state, tc.state)
				}
				if tc.state == coordinatorcontract.StateStale && bundle.CoordinatorContract.SourceInstructionsSHA256 != old.SourceInstructionsSHA256 {
					t.Fatal("portable copy was recertified")
				}
				encoded, _ := json.Marshal(bundle)
				var restored Bundle
				if err := json.Unmarshal(encoded, &restored); err != nil {
					t.Fatal(err)
				}
				if err := ValidateBundle(restored); err != nil {
					t.Fatal(err)
				}
				if bundle.CoordinatorContract == nil {
					return
				}
				bad := bundle
				changed := *bundle.Manifest.Spec.CoordinatorContract
				changed.Scope = "Another scope"
				bad.Manifest.Spec.CoordinatorContract = &changed
				bad.Hash = hashBundle(bad)
				if err := ValidateBundle(bad); err == nil {
					t.Fatal("portable source and internal contract mismatch was accepted")
				}
				var raw map[string]any
				_ = json.Unmarshal(encoded, &raw)
				raw["portable_config"].(map[string]any)["coordinator_contract"].(map[string]any)["allow_reply"] = true
				encoded, _ = json.Marshal(raw)
				if err := json.Unmarshal(encoded, new(Bundle)); err == nil {
					t.Fatal("unknown field in persisted portable contract was dropped")
				}
			})
		}
	}
}

func TestPortableCoordinatorContractRejectsInvalidSchemaAndAggregateBudget(t *testing.T) {
	for _, version := range []string{"multica.agent/v1", "multica.agent/v2"} {
		for _, contract := range []json.RawMessage{
			json.RawMessage(`{"version":1,"scope":"Route","allow_reply":true}`),
			json.RawMessage(`{"version":1,"scope":"Route","source_instructions_sha256":"fake"}`),
			json.RawMessage(`{"version":1,"scope":"` + strings.Repeat("界", 800) + `","constraints":["` + strings.Repeat("限", 800) + `"]}`),
		} {
			definition := map[string]any{"$schema": PortableSchemaPath, "version": version, "name": "Invalid contract", "instructions": "missing.md", "skills": []any{}, "coordinator_contract": contract}
			content, _ := json.Marshal(definition)
			if _, err := ValidateManifestJSON(content); err == nil {
				t.Fatal("invalid or oversized portable contract accepted")
			}
			if _, err := ParseAgentPackageFS(context.Background(), fstest.MapFS{PortableManifestPath: {Data: content}}); err == nil || strings.Contains(err.Error(), "required file") {
				t.Fatalf("invalid contract was not rejected before file loading: %v", err)
			}
		}
	}
}
