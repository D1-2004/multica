// Copyright (c) 2026 Nex.
// Adapted from gawkbot task-scoped packet assembly at 71e82a1809565281cbd0bf8185d3c125b715d934.
// See LICENSE and SOURCE_MAP.md for provenance and modifications.
package employeetask

import (
	"fmt"
	"strings"
)

// Compile canonicalizes intent and assembles an already-authorized work packet.
// It performs no retrieval, model calls, persistence, cursor movement or resume.
func Compile(input CompileInput) (WorkPacket, error) {
	if err := validateScope(input.Scope); err != nil {
		return WorkPacket{}, err
	}
	if !validUUID(input.PrincipalID) {
		return WorkPacket{}, fmt.Errorf("%w: trusted principal is required", ErrInvalid)
	}
	if strings.TrimSpace(input.ReturnAddress) == "" {
		return WorkPacket{}, fmt.Errorf("%w: Host return address is required", ErrInvalid)
	}
	definition, err := NormalizeDefinition(input.Definition)
	if err != nil {
		return WorkPacket{}, err
	}
	if err := validatePacketMaterial(input, input.Source); err != nil {
		return WorkPacket{}, err
	}
	for _, materials := range [][]PacketMaterial{input.Corrections, input.CompletedSteps, input.References, input.History.Items} {
		for _, material := range materials {
			if err := validatePacketMaterial(input, material); err != nil {
				return WorkPacket{}, err
			}
		}
	}
	switch input.History.State {
	case HistoryUnavailable, HistoryEmpty:
		if len(input.History.Items) > 0 {
			return WorkPacket{}, fmt.Errorf("%w: unavailable/empty history has items", ErrInvalid)
		}
	case HistoryAvailable:
		available := false
		for _, item := range input.History.Items {
			available = available || strings.TrimSpace(item.Body) != ""
		}
		if !available {
			return WorkPacket{}, fmt.Errorf("%w: available history has no content", ErrInvalid)
		}
	case HistoryTruncated:
	default:
		return WorkPacket{}, fmt.Errorf("%w: explicit history state is required", ErrInvalid)
	}
	input.CompletionNotice, err = NormalizeCompletionNoticePolicy(input.CompletionNotice, input.Source.Ref)
	if err != nil {
		return WorkPacket{}, err
	}
	input.Definition = definition
	text, contextUsed := buildTaskExecutionPacketWithContext(input)
	return WorkPacket{Scope: input.Scope, PrincipalID: input.PrincipalID, Definition: definition, Text: text, ContextUsed: contextUsed, CompletionNotice: input.CompletionNotice}, nil
}

func validatePacketMaterial(input CompileInput, material PacketMaterial) error {
	if material.Scope != input.Scope || material.PrincipalID != input.PrincipalID {
		return fmt.Errorf("%w: material scope/principal mismatch", ErrInvalid)
	}
	if material.Ref == "" || material.Ref != strings.TrimSpace(material.Ref) || strings.ContainsAny(material.Ref, "\r\n") {
		return fmt.Errorf("%w: material reference is required", ErrInvalid)
	}
	return nil
}
