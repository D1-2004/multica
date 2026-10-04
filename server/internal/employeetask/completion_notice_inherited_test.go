package employeetask

import (
	"slices"
	"strings"
	"testing"
)

func TestWorkPacketInheritedNoticeRequiresFencedSource(t *testing.T) {
	input := compilerFixture()
	origin := input.Source
	origin.Ref = "original-file-authorization"
	origin.Body = "Send the file and do not send an additional summary."
	input.CompletionNotice = CompletionNoticePolicy{Mode: CompletionNoticeIfNotDelivered, RequireDelivery: "file", SourceRef: origin.Ref, InstructionQuote: origin.Body}
	input.CompletionNoticeSource = &origin
	packet, err := Compile(input)
	if err != nil || !strings.Contains(packet.Text, origin.Body) || !slices.Contains(packet.ContextUsed, origin.Ref) || packet.CompletionNotice.SourceRef != origin.Ref {
		t.Fatal("inherited authorization was not rendered and traced", packet, err)
	}
	for _, invalid := range []string{"scope", "principal", "ref", "body"} {
		t.Run(invalid, func(t *testing.T) {
			changed := origin
			switch invalid {
			case "scope":
				changed.Scope.Scene.SceneID = "foreign-scene"
			case "principal":
				changed.PrincipalID = "foreign-principal"
			case "ref":
				changed.Ref = "foreign-source"
			case "body":
				changed.Body = ""
			}
			input.CompletionNoticeSource = &changed
			if _, err := Compile(input); err == nil {
				t.Fatal("unfenced inherited authorization accepted")
			}
		})
	}
}
