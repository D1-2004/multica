package agentsource

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
)

func packageZIP(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for name, content := range files {
		file, err := writer.Create(name)
		if err != nil { t.Fatal(err) }
		if _, err := file.Write([]byte(content)); err != nil { t.Fatal(err) }
	}
	if err := writer.Close(); err != nil { t.Fatal(err) }
	return buffer.Bytes()
}

func TestParseAgentPackagePreservesV2ConfigurationAndFiles(t *testing.T) {
	manifest := `{"$schema":"agent.schema.json","version":"multica.agent/v2","name":"Reviewer","instructions":"prompts/main.md","skills":[{"path":"skills/review","name":"review","enabled":false}],"configuration":{"persona":"Reviewer","inbound_coordinator":false,"custom_env":{"TOKEN":{"secret_ref":"review-token"}}},"access":{"permission_mode":"private","invocation_targets":[]}}`
	archive := packageZIP(t, map[string]string{"agent.json":manifest, "prompts/main.md":"Review carefully", "skills/review/SKILL.md":"Review skill", "skills/review/references/rules.md":"Team rules", "agent.schema.json":"{}"})
	parsed, err := ParseAgentPackage(context.Background(), archive)
	if err != nil { t.Fatal(err) }
	if parsed.Instructions != "Review carefully" || len(parsed.Skills) != 1 || !parsed.Skills[0].Disabled || len(parsed.Skills[0].Files) != 1 { t.Fatalf("unexpected parsed content: %#v", parsed) }
	var config map[string]json.RawMessage
	if err := json.Unmarshal(parsed.Manifest["configuration"], &config); err != nil { t.Fatal(err) }
	if string(config["inbound_coordinator"]) != "false" || !bytes.Contains(config["custom_env"], []byte("secret_ref")) || parsed.Manifest["access"] == nil { t.Fatal("v2 configuration was lost") }
}

func TestParseAgentPackageUsesEmbeddedSchema(t *testing.T) {
	archive := packageZIP(t, map[string]string{
		"agent.json":`{"$schema":"agent.schema.json","version":"multica.agent/v2","name":"Reviewer","instructions":"missing.md","skills":[],"configuration":{"persona":123}}`,
		"agent.schema.json":`{"type":"object","additionalProperties":true}`,
	})
	_, err := ParseAgentPackage(context.Background(), archive)
	var schemaError *ManifestSchemaError
	if !errors.As(err, &schemaError) || schemaError.Issues[0].Path != "/configuration/persona" { t.Fatalf("expected embedded schema error before missing-file error, got %v", err) }
}

type recordingPackageFS struct { fs.FS; reads *[]string }

func (r recordingPackageFS) Open(name string) (fs.File, error) {
	if strings.HasSuffix(name, ".md") || name == PortableManifestPath { *r.reads = append(*r.reads, name) }
	return r.FS.Open(name)
}

func TestPackageReadsManifestBeforeReferencedContent(t *testing.T) {
	reads := []string{}
	files := fstest.MapFS{"agent.json":{Data:[]byte(`{"version":"multica.agent/v2"}`)}, "AGENTS.md":{Data:[]byte("Must not read")}}
	_, err := ParseAgentPackageFS(context.Background(), recordingPackageFS{FS:files, reads:&reads})
	if err == nil { t.Fatal("accepted invalid manifest") }
	for _, name := range reads { if name == "AGENTS.md" { t.Fatal("read instructions before validating manifest") } }
}

func TestParseAgentPackageRejectsUnsafeArchives(t *testing.T) {
	for _, name := range []string{"../outside", "/outside", "a\\b", "a//b"} {
		_, err := ParseAgentPackage(context.Background(), packageZIP(t, map[string]string{name:"x"}))
		if err == nil { t.Fatalf("accepted unsafe archive name %q", name) }
	}
	var buffer bytes.Buffer
	w := zip.NewWriter(&buffer)
	for range 2 { f, _ := w.Create("agent.json"); _, _ = f.Write([]byte("{}")) }
	_ = w.Close()
	if _, err := ParseAgentPackage(context.Background(), buffer.Bytes()); err == nil { t.Fatal("accepted duplicate manifest") }
}
