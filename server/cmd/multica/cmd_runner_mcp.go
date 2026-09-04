package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/multica-ai/multica/server/pkg/runnerprotocol"
)

var runnerMCPServerNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

type runnerMCPServerSummary = runnerprotocol.MCPServerSummary
type runnerMCPInventory = runnerprotocol.MCPInventory

type runnerMCPServerConfig struct {
	Name        string
	Transport   string
	Fingerprint string
	Raw         json.RawMessage
}

type runnerMCPConfigDocument struct {
	Inventory runnerMCPInventory
	Servers   map[string]runnerMCPServerConfig
}

func runnerMCPInventoryFromJSON(raw []byte) (runnerMCPInventory, error) {
	document, err := parseRunnerMCPConfig(raw)
	if err != nil {
		return runnerMCPInventory{}, err
	}
	return document.Inventory, nil
}

func runnerMCPConfigPath() (string, error) {
	dir, err := runnerStateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "mcp.json"), nil
}

func loadRunnerMCPConfig() (runnerMCPConfigDocument, error) {
	path, err := runnerMCPConfigPath()
	if err != nil {
		return runnerMCPConfigDocument{}, err
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return parseRunnerMCPConfig([]byte(`{"mcpServers":{}}`))
	}
	if err != nil {
		return runnerMCPConfigDocument{}, fmt.Errorf("read Runner MCP config: %w", err)
	}
	return parseRunnerMCPConfig(raw)
}

func parseRunnerMCPConfig(raw []byte) (runnerMCPConfigDocument, error) {
	if len(raw) == 0 || len(raw) > 1<<20 || !json.Valid(raw) {
		return runnerMCPConfigDocument{}, errors.New("Runner MCP config must be one valid JSON document of at most 1 MiB")
	}
	var envelope struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	if err := decoder.Decode(&envelope); err != nil {
		return runnerMCPConfigDocument{}, fmt.Errorf("parse Runner MCP config: %w", err)
	}
	if envelope.MCPServers == nil {
		return runnerMCPConfigDocument{}, errors.New("Runner MCP config requires mcpServers")
	}

	names := make([]string, 0, len(envelope.MCPServers))
	for name := range envelope.MCPServers {
		names = append(names, name)
	}
	sort.Strings(names)
	document := runnerMCPConfigDocument{
		Inventory: runnerMCPInventory{Type: runnerprotocol.MessageInventory, Servers: make([]runnerMCPServerSummary, 0, len(names)), Config: append([]byte(nil), raw...)},
		Servers:   make(map[string]runnerMCPServerConfig, len(names)),
	}
	for _, name := range names {
		if !runnerMCPServerNamePattern.MatchString(name) || name == "." || name == ".." {
			return runnerMCPConfigDocument{}, fmt.Errorf("invalid MCP server name %q", name)
		}
		entry := envelope.MCPServers[name]
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(entry, &fields); err != nil {
			return runnerMCPConfigDocument{}, fmt.Errorf("parse MCP server %q: %w", name, err)
		}
		var command, rawURL string
		_ = json.Unmarshal(fields["command"], &command)
		_ = json.Unmarshal(fields["url"], &rawURL)
		if (strings.TrimSpace(command) == "") == (strings.TrimSpace(rawURL) == "") {
			return runnerMCPConfigDocument{}, fmt.Errorf("MCP server %q must configure exactly one of command or url", name)
		}
		transport := "stdio"
		if rawURL != "" {
			if err := validateRunnerMCPURL(rawURL); err != nil {
				return runnerMCPConfigDocument{}, fmt.Errorf("MCP server %q: %w", name, err)
			}
			transport = "http"
		}
		canonical := make(map[string]any)
		if err := json.Unmarshal(entry, &canonical); err != nil {
			return runnerMCPConfigDocument{}, fmt.Errorf("parse MCP server %q: %w", name, err)
		}
		canonicalRaw, err := json.Marshal(canonical)
		if err != nil {
			return runnerMCPConfigDocument{}, err
		}
		hash := sha256.Sum256(canonicalRaw)
		fingerprint := "sha256:" + hex.EncodeToString(hash[:])
		document.Servers[name] = runnerMCPServerConfig{Name: name, Transport: transport, Fingerprint: fingerprint, Raw: canonicalRaw}
		document.Inventory.Servers = append(document.Inventory.Servers, runnerMCPServerSummary{
			Name: name, Transport: transport, Availability: "available", Fingerprint: fingerprint,
		})
	}
	revisionHash := sha256.Sum256(raw)
	document.Inventory.Revision = "sha256:" + hex.EncodeToString(revisionHash[:])
	return document, nil
}

func validateRunnerMCPURL(raw string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Hostname() == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil {
		return errors.New("url must be an HTTP(S) endpoint without userinfo")
	}
	return nil
}
