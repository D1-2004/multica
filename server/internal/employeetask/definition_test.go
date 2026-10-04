// Copyright (c) 2026 Nex.
// Adapted from gawkbot task_definition_test.go at 71e82a1809565281cbd0bf8185d3c125b715d934.
// See LICENSE and SOURCE_MAP.md for modifications.
package employeetask

import (
	"strings"
	"testing"
)

// Migrated from TestNormalizeTaskDefinition; the local Definition uses string
// deliverables and no caller-controlled DefinedAt field.
func TestNormalizeDefinition(t *testing.T) {
	t.Parallel()
	for _, in := range []Definition{{Goal: "   "}, {Goal: "ship it", SuccessCriteria: []string{"tests pass", "  "}}, {Goal: "ship it", Deliverables: []string{" "}}} {
		if _, err := NormalizeDefinition(in); err == nil {
			t.Fatalf("invalid definition accepted: %+v", in)
		}
	}
	in := Definition{Goal: "  ship the export  ", Deliverables: []string{" export script (python file) "}, SuccessCriteria: []string{" round-trips big ints "}, AccessNeeded: []string{" prod read replica ", ""}}
	out, err := NormalizeDefinition(in)
	if err != nil {
		t.Fatal(err)
	}
	if out.Goal != "ship the export" || out.Deliverables[0] != "export script (python file)" || out.SuccessCriteria[0] != "round-trips big ints" || len(out.AccessNeeded) != 1 || out.AccessNeeded[0] != "prod read replica" {
		t.Fatalf("definition not canonical: %+v", out)
	}
	in.Deliverables[0] = "mutated"
	in.SuccessCriteria[0] = "mutated"
	in.AccessNeeded[0] = "mutated"
	if strings.Contains(strings.Join(out.Deliverables, " "), "mutated") || out.SuccessCriteria[0] == "mutated" || out.AccessNeeded[0] == "mutated" {
		t.Fatal("normalized definition aliases caller input")
	}
	minimal, err := NormalizeDefinition(Definition{Goal: " just answer "})
	if err != nil || minimal.Goal != "just answer" || len(minimal.Deliverables) != 0 || len(minimal.AccessNeeded) != 0 {
		t.Fatalf("optional fields became gates: %+v err=%v", minimal, err)
	}
}
