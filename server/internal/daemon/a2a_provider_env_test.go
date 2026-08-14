package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/daemon/execenv"
	"gopkg.in/yaml.v3"
)

func setManagedA2AModelTestEnv(t *testing.T) {
	t.Helper()
	t.Setenv("OPENAI_BASE_URL", "https://model.example/v1")
	t.Setenv("OPENAI_API_KEY", "test-managed-key")
	t.Setenv("OPENAI_MODEL", "test-model")
	t.Setenv("MULTICA_TRACE_ID", "provider-generation-a2a")
}

func TestConfigureManagedA2AV2HermesEnv(t *testing.T) {
	setManagedA2AModelTestEnv(t)
	t.Setenv("HERMES_DISABLE_LAZY_INSTALLS", "1")
	t.Setenv("HERMES_S6_SUPERVISED_CHILD", "ambient")
	agentEnv := map[string]string{
		"HERMES_HOME":                "/host/hermes",
		"HERMES_S6_SUPERVISED_CHILD": "agent-override",
		"OPENAI_API_KEY":             "agent-key",
	}
	skills := []execenv.SkillContextForEnv{{Name: "Managed Skill", Content: "managed skill"}}
	root := t.TempDir()
	if err := configureManagedA2AV2ProviderEnv(agentEnv, "hermes", root, "runtime brief", skills); err != nil {
		t.Fatal(err)
	}

	home := agentEnv["HOME"]
	hermesHome := agentEnv["HERMES_HOME"]
	if filepath.Dir(hermesHome) != home || filepath.Base(hermesHome) != ".hermes" || !strings.HasPrefix(home, root+string(os.PathSeparator)) {
		t.Fatalf("unexpected isolated Hermes home: HOME=%q HERMES_HOME=%q", home, hermesHome)
	}
	if agentEnv["HERMES_S6_SUPERVISED_CHILD"] != "" || agentEnv["HERMES_DISABLE_LAZY_INSTALLS"] != "1" {
		t.Fatalf("Hermes ambient controls were not replaced: %#v", agentEnv)
	}
	if agentEnv["OPENAI_API_KEY"] != "test-managed-key" || agentEnv["MULTICA_A2A_PROVIDER_GENERATION"] != "provider-generation-a2a" {
		t.Fatalf("Hermes managed bootstrap was not restored: %#v", agentEnv)
	}

	configBytes, err := os.ReadFile(filepath.Join(hermesHome, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err := yaml.Unmarshal(configBytes, &config); err != nil {
		t.Fatal(err)
	}
	model := config["model"].(map[string]any)
	if model["provider"] != "custom" || model["model"] != "test-model" || model["api_key"] != "test-managed-key" {
		t.Fatalf("unexpected Hermes model config: %#v", model)
	}
	if memory := config["memory"].(map[string]any); memory["provider"] != "" {
		t.Fatalf("Hermes external memory was not disabled: %#v", memory)
	}
	if info, err := os.Stat(filepath.Join(hermesHome, "config.yaml")); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("Hermes config mode = %v err=%v", info, err)
	}
	if dotenv, err := os.ReadFile(filepath.Join(hermesHome, ".env")); err != nil || !strings.Contains(string(dotenv), `HERMES_HOME="`+hermesHome+`"`) {
		t.Fatalf("Hermes managed dotenv = %q err=%v", dotenv, err)
	}
	if skill, err := os.ReadFile(filepath.Join(hermesHome, "skills", "managed-skill", "SKILL.md")); err != nil || !strings.Contains(string(skill), "managed skill") {
		t.Fatalf("Hermes managed skill = %q err=%v", skill, err)
	}
}

func TestConfigureManagedA2AV2PiEnv(t *testing.T) {
	setManagedA2AModelTestEnv(t)
	extensionPath := filepath.Join(t.TempDir(), "index.mjs")
	if err := os.WriteFile(extensionPath, []byte("export {};"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MULTICA_PI_DEFAULT_PROVIDER", "deap")
	t.Setenv("MULTICA_PI_MCP_EXTENSION_PATH", extensionPath)
	t.Setenv("PI_SKIP_VERSION_CHECK", "1")
	t.Setenv("PI_TELEMETRY", "0")
	t.Setenv("PI_PACKAGE_DIR", "/host/packages")
	agentEnv := map[string]string{
		"HOME":                          "/host/home",
		"PI_PACKAGE_DIR":                "/agent/packages",
		"MULTICA_PI_MCP_EXTENSION_PATH": "/agent/extension.mjs",
	}
	root := t.TempDir()
	if err := configureManagedA2AV2ProviderEnv(agentEnv, "pi", root, "runtime brief", nil); err != nil {
		t.Fatal(err)
	}

	home := agentEnv["HOME"]
	if !strings.HasPrefix(home, root+string(os.PathSeparator)) || agentEnv["PI_PACKAGE_DIR"] != "" {
		t.Fatalf("Pi home or ambient controls were not isolated: %#v", agentEnv)
	}
	if agentEnv["MULTICA_PI_MCP_EXTENSION_PATH"] != extensionPath || agentEnv["MULTICA_PI_DEFAULT_PROVIDER"] != "deap" {
		t.Fatalf("Pi managed extension contract was not restored: %#v", agentEnv)
	}
	if agentEnv["PI_SKIP_VERSION_CHECK"] != "1" || agentEnv["PI_TELEMETRY"] != "0" {
		t.Fatalf("Pi network controls were not pinned: %#v", agentEnv)
	}

	modelsBytes, err := os.ReadFile(filepath.Join(home, ".pi", "agent", "models.json"))
	if err != nil {
		t.Fatal(err)
	}
	var models struct {
		Providers map[string]struct {
			BaseURL string            `json:"baseUrl"`
			APIKey  string            `json:"apiKey"`
			Headers map[string]string `json:"headers"`
			Models  []struct {
				ID    string   `json:"id"`
				Input []string `json:"input"`
			} `json:"models"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(modelsBytes, &models); err != nil {
		t.Fatal(err)
	}
	deap := models.Providers["deap"]
	if deap.BaseURL != "https://model.example/v1" || deap.APIKey != "$OPENAI_API_KEY" || deap.Headers["X-Multica-Provider-Generation"] != "$MULTICA_A2A_PROVIDER_GENERATION" || len(deap.Models) != 1 || deap.Models[0].ID != "test-model" {
		t.Fatalf("unexpected Pi managed models: %#v", deap)
	}
	settingsBytes, err := os.ReadFile(filepath.Join(home, ".pi", "agent", "settings.json"))
	if err != nil || !strings.Contains(string(settingsBytes), `"defaultProvider":"deap"`) || !strings.Contains(string(settingsBytes), `"defaultModel":"test-model"`) {
		t.Fatalf("Pi managed settings = %q err=%v", settingsBytes, err)
	}
}

func TestConfigureManagedA2AV2ProviderEnvRejectsUnsupportedProvider(t *testing.T) {
	if err := configureManagedA2AV2ProviderEnv(map[string]string{}, "codex", t.TempDir(), "", nil); err == nil {
		t.Fatal("unsupported managed A2A provider must fail closed")
	}
}
