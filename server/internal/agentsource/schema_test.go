package agentsource

import (
	"errors"
	"os"
	"strings"
	"testing"
)

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
