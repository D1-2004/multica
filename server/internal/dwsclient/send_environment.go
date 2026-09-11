package dwsclient

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

func (c CLI) prepareEnvironment(dir string) error {
	if c.Environment == "" {
		return nil
	}
	mcp, terminal, err := sendEnvironment(c.Environment)
	if err != nil {
		return err
	}
	for name, value := range map[string]string{"mcp_url": mcp, "terminal_url": terminal} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0600); err != nil {
			return errors.New("configure isolated DWS environment")
		}
	}
	return nil
}

func sendEnvironment(environment string) (string, string, error) {
	switch environment {
	case "staging":
		return "https://pre-mcp.dingtalk.com", "https://pre-open-dev.dingtalk.com", nil
	case "production":
		return "https://mcp.dingtalk.com", "https://open-dev.dingtalk.com", nil
	default:
		return "", "", errors.New("unsupported DWS delivery environment")
	}
}

func (c CLI) commandEnv(dir string, values map[string]string) []string {
	if c.Environment == "" {
		return CommandEnv(dir, values)
	}
	mcp, terminal, _ := sendEnvironment(c.Environment)
	overrides := map[string]string{
		"DWS_MCP_URL": mcp, "DWS_TERMINAL_URL": terminal, "DWS_USE_PRE": "0",
	}
	if c.Environment == "staging" {
		overrides["DWS_USE_PRE"] = "1"
	}
	for key, value := range values {
		overrides[key] = value
	}
	env := CommandEnv(dir, nil)
	filtered := env[:0]
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		if _, overridden := overrides[key]; !overridden {
			filtered = append(filtered, entry)
		}
	}
	for key, value := range overrides {
		filtered = append(filtered, key+"="+value)
	}
	return filtered
}
