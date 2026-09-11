package agentsource

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestManifestSchemaReportsEveryFailureWithValidatorMessages(t *testing.T) {
	manifest := map[string]any{"$schema":"agent.schema.json", "version":"multica.agent/v2", "name":false, "instructions":42, "skills":[]any{}}
	for index := 0; index < 25; index++ { manifest["extra" + strings.Repeat("x", index)] = true }
	manifest["configuration"] = map[string]any{"persona":123, "max_concurrent_tasks":0}
	content, _ := json.Marshal(manifest)
	_, err := ValidateManifestJSON(content)
	if err == nil { t.Fatal("invalid manifest accepted") }
	for _, expected := range []string{"expected string", "max_concurrent_tasks", "minimum", "persona", "additionalProperties"} {
		if !strings.Contains(err.Error(), expected) { t.Errorf("missing validator detail %q: %s", expected, err) }
	}
}

func TestManifestSchemaDoesNotTruncateIssues(t *testing.T) {
	skills := make([]any, 30)
	for index := range skills { skills[index] = map[string]any{"path":42, "name":false} }
	content, _ := json.Marshal(map[string]any{"$schema":"agent.schema.json", "version":"multica.agent/v2", "name":"All errors", "instructions":"AGENTS.md", "skills":skills})
	_, err := ValidateManifestJSON(content)
	var invalid *ManifestSchemaError
	if !errors.As(err, &invalid) || len(invalid.Issues) <= 20 || !strings.Contains(err.Error(), "/skills/29") { t.Fatalf("schema issues were truncated: %v", err) }
}

func TestEmbeddedSchemaValidatesFullV2Example(t *testing.T) {
	content, err := os.ReadFile("../../../docs/examples/agent-manifest-v2.json")
	if err != nil { t.Fatal(err) }
	fields, err := ValidateManifestJSON(content)
	if err != nil { t.Fatal(err) }
	if fields["configuration"] == nil || fields["bindings"] == nil || fields["a2a"] == nil { t.Fatal("schema validation discarded configuration") }
}

func TestManifestSchemaErrorsDoNotEchoValues(t *testing.T) {
	value := `{"$schema":"agent.schema.json","version":"multica.agent/v2","name":"Reviewer","instructions":"AGENTS.md","skills":[],"configuration":{"runtime_config":{"gateway":{"token":"fixture-private-value"}}}}`
	_, err := ValidateManifestJSON([]byte(value))
	var invalid *ManifestSchemaError
	if !errors.As(err, &invalid) { t.Fatalf("expected schema error: %v", err) }
	if strings.Contains(err.Error(), "fixture-private-value") { t.Fatal("schema error echoed a value") }
	if len(invalid.Issues) == 0 || invalid.Issues[0].Path != "/configuration/runtime_config/gateway/token" { t.Fatalf("missing field path: %#v", invalid.Issues) }
}

func TestManifestCannotSelectAnExternalSchema(t *testing.T) {
	_, err := ValidateManifestJSON([]byte(`{"$schema":"https://untrusted.example.test/schema.json","version":"multica.agent/v2","name":"Reviewer","instructions":"AGENTS.md","skills":[]}`))
	var invalid *ManifestSchemaError
	if !errors.As(err, &invalid) { t.Fatalf("expected local schema rejection: %v", err) }
}
