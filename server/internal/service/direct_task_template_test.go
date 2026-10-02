package service

import (
	"encoding/json"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
	"slices"
	"testing"
)

func TestDirectTaskTemplateProtocolRequiresCataloguedR2(t *testing.T) {
	for _, c := range []struct {
		alias             string
		published, direct bool
	}{
		{"multica-m7-vda499f3161a007c0-r1-9a6bfa", true, false},
		{"multica-m7-vda499f3161a007c0-r2-9a6bfa", true, true},
		{"multica-m7-v0000000000000000-r2-9a6bfa", false, false},
		{"multica-m7-vda499f3161a007c0-r3-9a6bfa", false, false},
		{"multica-m7-vda499f3161a007c0-r2-9A6BFA", false, false},
	} {
		t.Run(c.alias, func(t *testing.T) {
			raw, _ := json.Marshal([]map[string]any{{"id": "immutable-template-r2", "aliases": []string{c.alias}, "status": "ready"}})
			templates, err := parseFCE2BTemplates(string(raw), testRuntimeProviderCatalog())
			if err != nil || len(templates) != 1 {
				t.Fatal(templates, err)
			}
			template := templates[0]
			if IsFCE2BTemplatePublished(template) != c.published {
				t.Fatal("catalog guard changed", template)
			}
			capabilities := FCE2BTemplateCapabilities("hermes", template)
			if slices.Contains(capabilities, protocol.DaemonCapabilityEmployeeDirectV1) != c.direct {
				t.Fatal("protocol capability incorrectly attested", capabilities)
			}
			metadata, _ := json.Marshal(map[string]any{"kind": "cloud-sandbox", "sandbox_backend": "aliyun_fc", "provider": "hermes", "artifact_kind": "e2b_template", "artifact_ref": template.ID, "artifact_alias": template.Template, "capabilities": capabilities})
			if DirectTaskRuntimeCapable(db.AgentRuntime{RuntimeMode: "cloud", Provider: "hermes", Status: "online", Metadata: metadata}) != c.direct {
				t.Fatal("capability lost in runtime metadata")
			}
		})
	}
}
