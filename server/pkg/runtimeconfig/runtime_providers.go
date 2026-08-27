package runtimeconfig

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
)

const (
	RuntimeProvidersSchemaVersion = 1
	RuntimeProvidersDiamondDataID = "dt-fde-multica-runtime-manifest-fingerprints.json"
)

var runtimeProviderNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// RuntimeProvidersConfig is the deployment-wide provider selection list.
// It changes only when a provider is added or removed; image/template releases
// do not require a Diamond update.
type RuntimeProvidersConfig struct {
	Version   int      `json:"version"`
	Providers []string `json:"providers"`
}

type RuntimeProvidersSnapshot struct {
	Version    int
	Providers  []string
	Generation uint64
	SHA256     string
}

func ParseRuntimeProviders(data []byte) (RuntimeProvidersConfig, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var cfg RuntimeProvidersConfig
	if err := decoder.Decode(&cfg); err != nil {
		return RuntimeProvidersConfig{}, fmt.Errorf("parse Runtime providers: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return RuntimeProvidersConfig{}, errors.New("Runtime providers contain multiple JSON values")
		}
		return RuntimeProvidersConfig{}, fmt.Errorf("parse trailing Runtime providers: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return RuntimeProvidersConfig{}, err
	}
	return cloneRuntimeProvidersConfig(cfg), nil
}

func (c RuntimeProvidersConfig) Validate() error {
	if c.Version != RuntimeProvidersSchemaVersion {
		return fmt.Errorf("Runtime providers version must be %d", RuntimeProvidersSchemaVersion)
	}
	if c.Providers == nil {
		return errors.New("Runtime providers list is required")
	}
	seen := make(map[string]struct{}, len(c.Providers))
	for _, provider := range c.Providers {
		if !runtimeProviderNamePattern.MatchString(provider) || strings.TrimSpace(provider) != provider {
			return errors.New("Runtime provider names contain unsupported characters")
		}
		if _, exists := seen[provider]; exists {
			return errors.New("Runtime provider list must not contain duplicates")
		}
		seen[provider] = struct{}{}
	}
	return nil
}

func (s *Service) RuntimeProviders() RuntimeProvidersSnapshot {
	if s == nil {
		return RuntimeProvidersSnapshot{}
	}
	current := s.runtimeProviders.Load()
	if current == nil {
		return RuntimeProvidersSnapshot{}
	}
	return cloneRuntimeProvidersSnapshot(*current)
}

func (s *Service) ApplyRuntimeProvidersJSON(data []byte) (RuntimeProvidersSnapshot, error) {
	if s == nil {
		return RuntimeProvidersSnapshot{}, errors.New("runtime config service is nil")
	}
	s.runtimeProvidersApplyMu.Lock()
	defer s.runtimeProvidersApplyMu.Unlock()

	cfg, err := ParseRuntimeProviders(data)
	if err != nil {
		return s.RuntimeProviders(), err
	}
	previous := s.runtimeProviders.Load()
	generation := uint64(1)
	if previous != nil {
		generation = previous.Generation + 1
	}
	sum := sha256.Sum256(data)
	next := &RuntimeProvidersSnapshot{
		Version:    cfg.Version,
		Providers:  append([]string(nil), cfg.Providers...),
		Generation: generation,
		SHA256:     hex.EncodeToString(sum[:]),
	}
	s.runtimeProviders.Store(next)
	return cloneRuntimeProvidersSnapshot(*next), nil
}

func CapabilitiesForProviders(providers []string) []string {
	present := make(map[string]struct{}, len(providers))
	for _, provider := range providers {
		present[strings.ToLower(strings.TrimSpace(provider))] = struct{}{}
	}
	capabilities := []string{
		"dws",
		"dws.im_event",
		"mcp",
		"runtime_start_events_v1",
		"llm_trace_v1",
	}
	_, hasOpenCode := present["opencode"]
	_, hasOpenCodeV2 := present["opencode-v2"]
	_, hasDSH := present["dsh"]
	if hasOpenCode || hasOpenCodeV2 || hasDSH {
		capabilities = append(capabilities, "a2a_inbound_opencode_v1")
	}
	capabilities = append(capabilities, "a2a-invocation-v2")
	if _, ok := present["hermes"]; ok {
		capabilities = append(capabilities, "a2a_inbound_hermes_v1")
	}
	if _, ok := present["pi"]; ok {
		capabilities = append(capabilities, "a2a_inbound_pi_v1")
	}
	if hasDSH {
		capabilities = append(capabilities, "dsh_trajectory_v1")
	}
	return capabilities
}

func cloneRuntimeProvidersConfig(in RuntimeProvidersConfig) RuntimeProvidersConfig {
	return RuntimeProvidersConfig{
		Version:   in.Version,
		Providers: append([]string(nil), in.Providers...),
	}
}

func cloneRuntimeProvidersSnapshot(in RuntimeProvidersSnapshot) RuntimeProvidersSnapshot {
	out := in
	out.Providers = append([]string(nil), in.Providers...)
	return out
}
