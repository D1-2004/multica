package inboundcoord

import (
	"strings"
	"testing"
)

func TestReferenceUniquenessRemainsHostEnforced(t *testing.T) {
	turn := Turn{Source: SourceWeb, Message: "你好", CoordinationReads: []CoordinationRead{{ReadRef: "r1", Tool: toolAssocRecall}}}
	cases := []struct{ name, raw, want string }{
		{"valid greeting", `{"actions":[{"kind":"acknowledge","source_refs":["u1"],"reply":"你好","ack_kind":"greeting"}]}`, ""},
		{"duplicate source", `{"actions":[{"kind":"acknowledge","source_refs":["u1","u1"],"reply":"你好","ack_kind":"greeting"}]}`, "duplicate source_ref"},
		{"valid status", `{"actions":[{"kind":"report_status","source_refs":["u1"],"state_refs":["r1"],"reply":"当前没有事项记录。"}]}`, ""},
		{"duplicate state", `{"actions":[{"kind":"report_status","source_refs":["u1"],"state_refs":["r1","r1"],"reply":"当前没有事项记录。"}]}`, "duplicate state_ref"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseValidatedWindowPlan(tc.raw, turn, nil, nil)
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("wanted %q, got %v", tc.want, err)
			}
		})
	}
}
