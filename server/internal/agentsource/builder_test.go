package agentsource

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBuilderPackageUsesZIPValidatorAndRetainsFullDefinition(t *testing.T) {
	manifest := json.RawMessage(`{"$schema":"agent.schema.json","version":"multica.agent/v2","name":"Builder","instructions":"AGENTS.md","skills":[{"path":"skills/review","name":"review","enabled":false}],"configuration":{"persona":"Full package","max_concurrent_tasks":3},"okrs":[{"objective":"Quality","key_results":["Review changes"]}]}`)
	files := map[string]string{"AGENTS.md":"Builder instructions", "skills/review/SKILL.md":"Review", "skills/review/references/check.md":"Check"}
	archive, err := BuildAgentPackage(t.Context(), manifest, files)
	if err != nil { t.Fatal(err) }
	parsed, err := ParseAgentPackage(t.Context(), archive)
	if err != nil || parsed.Manifest["okrs"] == nil || len(parsed.Skills) != 1 || !parsed.Skills[0].Disabled || len(parsed.Skills[0].Files) != 1 { t.Fatalf("incomplete Builder package: %v", err) }
	delete(files, "AGENTS.md")
	if _, err := BuildAgentPackage(t.Context(), manifest, files); err == nil || !strings.Contains(err.Error(), "AGENTS.md") { t.Fatalf("missing file was accepted: %v", err) }
	files["../escape"] = "escape"
	if _, err := BuildAgentPackage(t.Context(), manifest, files); err == nil { t.Fatal("unsafe file path accepted") }
}
