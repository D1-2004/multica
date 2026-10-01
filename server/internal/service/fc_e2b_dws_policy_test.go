package service

import (
	"slices"
	"testing"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

// Every image in the provider catalog declares the managed DWS wrapper;
// an image outside the catalog declares nothing.
func TestFCE2BDWSMessagePolicyForEveryCataloguedImage(t *testing.T) {
	t.Parallel()
	const templates = `[
		{"templateID":"older","aliases":["multica-m7-vdd95d8b615567a87-r1-111111"]},
		{"templateID":"newer","aliases":["multica-m7-va2eb67817f146ef4-r1-222222"]},
		{"templateID":"uncatalogued","aliases":["multica-m7-v1111111111111111-r1-333333"]}
	]`
	catalog := map[string][]string{
		"dd95d8b615567a87": {"hermes", "opencode", "pi", "dsh", "opencode-v2"},
		"a2eb67817f146ef4": {"hermes", "opencode", "pi", "dsh", "opencode-v2"},
	}
	got, err := parseFCE2BTemplates(templates, catalog)
	if err != nil || len(got) != 3 {
		t.Fatalf("parse: %v, %v", got, err)
	}
	for _, template := range got {
		want := template.ID != "uncatalogued"
		if has := slices.Contains(template.Capabilities, protocol.DWSMessagePolicyCapability); has != want {
			t.Errorf("%s capabilities %v; want wrapper=%v", template.ID, template.Capabilities, want)
		}
	}
}
