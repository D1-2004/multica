// Copyright (c) 2026 Nex.
// Copied and modified from gawkbot at 71e82a1809565281cbd0bf8185d3c125b715d934.
// See LICENSE and SOURCE_MAP.md for provenance and modifications.
package employeetask

import (
	"fmt"
	"strings"
)

// NormalizeDefinition validates and canonicalizes a definition from the
// wire. Goal is required; success-criteria entries must be non-empty.
// Returns a fresh struct so caller-owned slices never alias the result.
// AccessNeeded remains a request and never becomes an execution capability.
func NormalizeDefinition(in Definition) (Definition, error) {
	goal := strings.TrimSpace(in.Goal)
	if goal == "" {
		return Definition{}, fmt.Errorf("%w: definition goal is required", ErrInvalid)
	}
	out := Definition{Goal: goal}
	for i, d := range in.Deliverables {
		name := strings.TrimSpace(d)
		if name == "" {
			return Definition{}, fmt.Errorf("%w: definition deliverables[%d] is empty", ErrInvalid, i)
		}
		out.Deliverables = append(out.Deliverables, name)
	}
	for i, c := range in.SuccessCriteria {
		c = strings.TrimSpace(c)
		if c == "" {
			return Definition{}, fmt.Errorf("%w: definition success_criteria[%d] is empty", ErrInvalid, i)
		}
		out.SuccessCriteria = append(out.SuccessCriteria, c)
	}
	for _, a := range in.AccessNeeded {
		if a = strings.TrimSpace(a); a != "" {
			out.AccessNeeded = append(out.AccessNeeded, a)
		}
	}
	return out, nil
}

// taskDefinitionPacketLines renders the definition for work packets
// (packet.go). The caller prepends its own header line; these
// lines are indented to sit under it. Nil/empty definitions render nothing.
func taskDefinitionPacketLines(def *Definition) []string {
	if def == nil || strings.TrimSpace(def.Goal) == "" {
		return nil
	}
	// Preserve the complete contract; a tail constraint must not disappear.
	lines := []string{"  Goal: " + strings.TrimSpace(def.Goal)}
	if len(def.Deliverables) > 0 {
		lines = append(lines, "  Deliverables: "+strings.Join(def.Deliverables, "; "))
	}
	if len(def.SuccessCriteria) > 0 {
		lines = append(lines, "  Success criteria:")
		for i, c := range def.SuccessCriteria {
			lines = append(lines, fmt.Sprintf("    %d. %s", i+1, c))
		}
	}
	if len(def.AccessNeeded) > 0 {
		lines = append(lines, "  Access needed (requested, not granted): "+strings.Join(def.AccessNeeded, "; "))
	}
	return lines
}
