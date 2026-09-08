package service

import (
	"slices"
	"testing"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestFCE2BDWSMessagePolicyRequiresExactFingerprint(t *testing.T) {
	t.Parallel()
	const templates = `[
		{"templateID":"old","aliases":["multica-m7-vbc80cb4524f2bc75-r1-111111"]},
		{"templateID":"new","aliases":["multica-m7-vdd95d8b615567a87-r1-222222"]},
		{"templateID":"uncatalogued","aliases":["multica-m7-v1111111111111111-r1-333333"]}
	]`
	catalog := map[string][]string{
		"bc80cb4524f2bc75": {"hermes", "opencode", "pi", "dsh", "opencode-v2"},
		"dd95d8b615567a87": {"hermes", "opencode", "pi", "dsh", "opencode-v2"},
	}
	for _, allowlist := range [][]string{nil, {"dd95d8b615567a87", "1111111111111111"}, {"dd95d8b615567a8"}, {"DD95D8B615567A87"}} {
		got, err := parseFCE2BTemplates(templates, catalog, allowlist)
		if err != nil || len(got) != 3 {
			t.Fatalf("parse: %v, %v", got, err)
		}
		for _, template := range got {
			want := template.ID == "new" && slices.Contains(allowlist, "dd95d8b615567a87")
			if has := slices.Contains(template.Capabilities, protocol.DWSMessagePolicyCapability); has != want {
				t.Errorf("%s capabilities %v with allowlist %v; want new hook=%v", template.ID, template.Capabilities, allowlist, want)
			}
		}
	}
}
