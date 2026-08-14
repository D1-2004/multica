package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/multica-ai/multica/server/internal/daemon/execenv"
	"gopkg.in/yaml.v3"
)

type managedA2AModelBootstrap struct {
	BaseURL            string
	APIKey             string
	Model              string
	ProviderGeneration string
}

func supportsManagedA2AV2Provider(provider string) bool {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "hermes", "opencode", "pi":
		return true
	default:
		return false
	}
}

func configureManagedA2AV2ProviderEnv(agentEnv map[string]string, provider, envRoot, runtimeBrief string, skills []execenv.SkillContextForEnv) error {
	provider = strings.ToLower(strings.TrimSpace(provider))
	switch provider {
	case "hermes":
		return configureManagedA2AV2HermesEnv(agentEnv, envRoot, skills)
	case "opencode":
		return configureManagedA2AV2OpenCodeEnv(agentEnv, provider, envRoot, runtimeBrief, skills)
	case "pi":
		return configureManagedA2AV2PiEnv(agentEnv, envRoot)
	default:
		return fmt.Errorf("managed A2A v2 runtime does not support provider %q", provider)
	}
}

func loadManagedA2AModelBootstrap(requireModel bool) (managedA2AModelBootstrap, error) {
	bootstrap := managedA2AModelBootstrap{
		BaseURL:            strings.TrimSpace(os.Getenv("OPENAI_BASE_URL")),
		APIKey:             strings.TrimSpace(os.Getenv("OPENAI_API_KEY")),
		Model:              strings.TrimSpace(os.Getenv("OPENAI_MODEL")),
		ProviderGeneration: strings.TrimSpace(os.Getenv("MULTICA_TRACE_ID")),
	}
	for key, value := range map[string]string{
		"OPENAI_BASE_URL":  bootstrap.BaseURL,
		"OPENAI_API_KEY":   bootstrap.APIKey,
		"MULTICA_TRACE_ID": bootstrap.ProviderGeneration,
	} {
		if value == "" {
			return managedA2AModelBootstrap{}, fmt.Errorf("managed A2A v2 runtime is missing %s", key)
		}
	}
	if requireModel && bootstrap.Model == "" {
		return managedA2AModelBootstrap{}, errors.New("managed A2A v2 runtime is missing OPENAI_MODEL")
	}
	if strings.ContainsAny(bootstrap.APIKey, "\r\n") {
		return managedA2AModelBootstrap{}, errors.New("managed A2A v2 runtime received an invalid OPENAI_API_KEY")
	}
	return bootstrap, nil
}

func prepareManagedA2AProviderHome(envRoot, provider string) (string, string, error) {
	envRoot = strings.TrimSpace(envRoot)
	if envRoot == "" {
		return "", "", errors.New("managed A2A v2 runtime requires an execution root")
	}
	root, err := os.MkdirTemp(envRoot, ".a2a-"+provider+"-")
	if err != nil {
		return "", "", fmt.Errorf("prepare managed A2A %s root: %w", provider, err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		return "", "", fmt.Errorf("secure managed A2A %s root: %w", provider, err)
	}
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		return "", "", fmt.Errorf("prepare managed A2A %s home: %w", provider, err)
	}
	return root, home, nil
}

func maskManagedProviderPrefix(agentEnv map[string]string, prefixes ...string) {
	mask := func(key string) {
		upper := strings.ToUpper(key)
		for _, prefix := range prefixes {
			if strings.HasPrefix(upper, prefix) {
				agentEnv[key] = ""
				return
			}
		}
	}
	for _, entry := range os.Environ() {
		if key, _, ok := strings.Cut(entry, "="); ok {
			mask(key)
		}
	}
	for key := range agentEnv {
		mask(key)
	}
}

func writeManagedJSON(path string, value any) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	encodeErr := json.NewEncoder(file).Encode(value)
	closeErr := file.Close()
	if encodeErr != nil {
		return encodeErr
	}
	return closeErr
}

func configureManagedA2AV2HermesEnv(agentEnv map[string]string, envRoot string, skills []execenv.SkillContextForEnv) error {
	bootstrap, err := loadManagedA2AModelBootstrap(true)
	if err != nil {
		return err
	}
	if strings.TrimSpace(os.Getenv("HERMES_DISABLE_LAZY_INSTALLS")) != "1" {
		return errors.New("managed A2A v2 Hermes runtime requires HERMES_DISABLE_LAZY_INSTALLS=1")
	}
	_, home, err := prepareManagedA2AProviderHome(envRoot, "hermes")
	if err != nil {
		return err
	}
	hermesHome := filepath.Join(home, ".hermes")
	if err := os.MkdirAll(hermesHome, 0o700); err != nil {
		return fmt.Errorf("prepare managed A2A Hermes config directory: %w", err)
	}
	config := map[string]any{
		"model": map[string]any{
			"provider":        "custom",
			"base_url":        bootstrap.BaseURL,
			"model":           bootstrap.Model,
			"supports_vision": true,
			"api_key":         bootstrap.APIKey,
			"default_headers": map[string]string{
				"X-Multica-Provider-Generation": "${MULTICA_A2A_PROVIDER_GENERATION}",
			},
		},
		"memory": map[string]any{"provider": ""},
		"skills": map[string]any{"external_dirs": []string{}},
	}
	configBytes, err := yaml.Marshal(config)
	if err != nil {
		return fmt.Errorf("marshal managed A2A Hermes config: %w", err)
	}
	if err := os.WriteFile(filepath.Join(hermesHome, "config.yaml"), configBytes, 0o600); err != nil {
		return fmt.Errorf("write managed A2A Hermes config: %w", err)
	}
	dotenv := "OPENAI_API_KEY=" + strconv.Quote(bootstrap.APIKey) + "\n" +
		"HERMES_HOME=" + strconv.Quote(hermesHome) + "\n" +
		"MULTICA_A2A_PROVIDER_GENERATION=" + strconv.Quote(bootstrap.ProviderGeneration) + "\n"
	if err := os.WriteFile(filepath.Join(hermesHome, ".env"), []byte(dotenv), 0o600); err != nil {
		return fmt.Errorf("write managed A2A Hermes environment: %w", err)
	}
	if len(skills) > 0 {
		if err := execenv.WriteManagedSkills(filepath.Join(hermesHome, "skills"), skills); err != nil {
			return fmt.Errorf("write managed A2A Hermes skills: %w", err)
		}
	}

	maskManagedProviderPrefix(agentEnv, "HERMES_")
	agentEnv["HOME"] = home
	agentEnv["HERMES_HOME"] = hermesHome
	agentEnv["HERMES_DISABLE_LAZY_INSTALLS"] = "1"
	agentEnv["OPENAI_BASE_URL"] = bootstrap.BaseURL
	agentEnv["OPENAI_API_KEY"] = bootstrap.APIKey
	agentEnv["OPENAI_MODEL"] = bootstrap.Model
	agentEnv["MULTICA_A2A_PROVIDER_GENERATION"] = bootstrap.ProviderGeneration
	return nil
}

func configureManagedA2AV2PiEnv(agentEnv map[string]string, envRoot string) error {
	bootstrap, err := loadManagedA2AModelBootstrap(true)
	if err != nil {
		return err
	}
	extensionPath := strings.TrimSpace(os.Getenv("MULTICA_PI_MCP_EXTENSION_PATH"))
	if extensionPath == "" {
		return errors.New("managed A2A v2 Pi runtime is missing MULTICA_PI_MCP_EXTENSION_PATH")
	}
	if strings.TrimSpace(os.Getenv("MULTICA_PI_DEFAULT_PROVIDER")) != "deap" {
		return errors.New("managed A2A v2 Pi runtime requires MULTICA_PI_DEFAULT_PROVIDER=deap")
	}
	if strings.TrimSpace(os.Getenv("PI_SKIP_VERSION_CHECK")) != "1" || strings.TrimSpace(os.Getenv("PI_TELEMETRY")) != "0" {
		return errors.New("managed A2A v2 Pi runtime requires version checks and telemetry to be disabled")
	}
	_, home, err := prepareManagedA2AProviderHome(envRoot, "pi")
	if err != nil {
		return err
	}
	piHome := filepath.Join(home, ".pi", "agent")
	if err := os.MkdirAll(piHome, 0o700); err != nil {
		return fmt.Errorf("prepare managed A2A Pi config directory: %w", err)
	}
	models := map[string]any{
		"providers": map[string]any{
			"deap": map[string]any{
				"name":       "DEAP",
				"baseUrl":    bootstrap.BaseURL,
				"api":        "openai-completions",
				"apiKey":     "$OPENAI_API_KEY",
				"authHeader": true,
				"headers": map[string]string{
					"X-Multica-Provider-Generation": "$MULTICA_A2A_PROVIDER_GENERATION",
				},
				"models": []map[string]any{{"id": bootstrap.Model, "input": []string{"text", "image"}}},
			},
		},
	}
	if err := writeManagedJSON(filepath.Join(piHome, "models.json"), models); err != nil {
		return fmt.Errorf("write managed A2A Pi models: %w", err)
	}
	settings := map[string]any{
		"defaultProvider":      "deap",
		"defaultModel":         bootstrap.Model,
		"defaultThinkingLevel": "off",
	}
	if err := writeManagedJSON(filepath.Join(piHome, "settings.json"), settings); err != nil {
		return fmt.Errorf("write managed A2A Pi settings: %w", err)
	}

	maskManagedProviderPrefix(agentEnv, "PI_", "MULTICA_PI_")
	agentEnv["HOME"] = home
	agentEnv["PI_SKIP_VERSION_CHECK"] = "1"
	agentEnv["PI_TELEMETRY"] = "0"
	agentEnv["MULTICA_PI_DEFAULT_PROVIDER"] = "deap"
	agentEnv["MULTICA_PI_MCP_EXTENSION_PATH"] = extensionPath
	agentEnv["OPENAI_BASE_URL"] = bootstrap.BaseURL
	agentEnv["OPENAI_API_KEY"] = bootstrap.APIKey
	agentEnv["OPENAI_MODEL"] = bootstrap.Model
	agentEnv["MULTICA_A2A_PROVIDER_GENERATION"] = bootstrap.ProviderGeneration
	return nil
}
