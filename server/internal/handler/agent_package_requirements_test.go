package handler

import (
	"encoding/json"
	"testing"

	"github.com/multica-ai/multica/server/internal/agentsource"
)

func TestPackageRequirementsExposeOnlyDeclaredResources(t *testing.T) {
	var definition map[string]json.RawMessage
	if err := json.Unmarshal([]byte(`{"bindings":{"runtime":{"ref":"worker","provider":"codex"},"github_identity":{"ref":"maintainer"},"runner":null,"bots":[]},"dsh_plugins":[],"configuration":{"persona":"Not a resource declaration","custom_env":{"TOKEN":{"secret_ref":"service-token"}}}}`), &definition); err != nil { t.Fatal(err) }
	encoded, err := json.Marshal(packageRequirements(agentsource.Bundle{Definition:definition}))
	if err != nil { t.Fatal(err) }
	var report struct { Declarations []struct { Path string `json:"path"`; Declaration json.RawMessage `json:"declaration"` } `json:"binding_declarations"` }
	if err := json.Unmarshal(encoded, &report); err != nil { t.Fatal(err) }
	want := map[string]string{"/bindings/runtime":`{"ref":"worker","provider":"codex"}`,"/bindings/github_identity":`{"ref":"maintainer"}`,"/bindings/runner":`null`,"/bindings/bots":`[]`,"/dsh_plugins":`[]`}
	if len(report.Declarations) != len(want) { t.Fatalf("declared resources: got %s", encoded) }
	for _, item := range report.Declarations {
		value, exists := want[item.Path]
		if !exists || packageValueHash(item.Declaration) != packageValueHash(json.RawMessage(value)) { t.Errorf("unexpected declaration %s: %s", item.Path, item.Declaration) }
		delete(want,item.Path)
	}
	if len(want) != 0 { t.Errorf("missing declarations: %v",want) }
}
