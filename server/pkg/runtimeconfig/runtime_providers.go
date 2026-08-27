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

var (
	runtimeCommitPattern       = regexp.MustCompile(`^[0-9a-f]{40}$`)
	runtimeProviderNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
)

type FCTemplateProviders struct {
	RuntimeCommit string   `json:"runtime_commit"`
	Providers     []string `json:"providers"`
}

// RuntimeProvidersConfig is only a provider-selection directory. FC entries
// are addressed by template ID, so aliases are display-only. ASB entries are
// addressed by the full Runtime source commit supplied with the image.
type RuntimeProvidersConfig struct {
	Version     int                            `json:"version"`
	FCTemplates map[string]FCTemplateProviders `json:"fc_templates"`
	ASBCommits  map[string][]string            `json:"asb_commits"`
}

type RuntimeProvidersSnapshot struct {
	Version     int
	FCTemplates map[string]FCTemplateProviders
	ASBCommits  map[string][]string
	Generation  uint64
	SHA256      string
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
	if c.FCTemplates == nil || c.ASBCommits == nil {
		return errors.New("Runtime providers fc_templates and asb_commits maps are required")
	}
	for templateID, entry := range c.FCTemplates {
		if strings.TrimSpace(templateID) != templateID || templateID == "" {
			return errors.New("FC Runtime provider template IDs must be non-empty and trimmed")
		}
		if !runtimeCommitPattern.MatchString(entry.RuntimeCommit) {
			return errors.New("FC Runtime provider entries require a 40-character lowercase Runtime commit")
		}
		if err := validateRuntimeProviderList(entry.Providers); err != nil {
			return err
		}
	}
	for commit, providers := range c.ASBCommits {
		if !runtimeCommitPattern.MatchString(commit) {
			return errors.New("ASB Runtime provider keys must be 40-character lowercase Runtime commits")
		}
		if err := validateRuntimeProviderList(providers); err != nil {
			return err
		}
	}
	return nil
}

func validateRuntimeProviderList(providers []string) error {
	seen := make(map[string]struct{}, len(providers))
	for _, provider := range providers {
		if !runtimeProviderNamePattern.MatchString(provider) || strings.TrimSpace(provider) != provider {
			return errors.New("Runtime provider names contain unsupported characters")
		}
		if _, exists := seen[provider]; exists {
			return errors.New("Runtime provider lists must not contain duplicates")
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
		Version:     cfg.Version,
		FCTemplates: cloneFCTemplateProviders(cfg.FCTemplates),
		ASBCommits:  cloneRuntimeProviderLists(cfg.ASBCommits),
		Generation:  generation,
		SHA256:      hex.EncodeToString(sum[:]),
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
		Version:     in.Version,
		FCTemplates: cloneFCTemplateProviders(in.FCTemplates),
		ASBCommits:  cloneRuntimeProviderLists(in.ASBCommits),
	}
}

func cloneRuntimeProvidersSnapshot(in RuntimeProvidersSnapshot) RuntimeProvidersSnapshot {
	out := in
	out.FCTemplates = cloneFCTemplateProviders(in.FCTemplates)
	out.ASBCommits = cloneRuntimeProviderLists(in.ASBCommits)
	return out
}

func cloneFCTemplateProviders(in map[string]FCTemplateProviders) map[string]FCTemplateProviders {
	if in == nil {
		return nil
	}
	out := make(map[string]FCTemplateProviders, len(in))
	for templateID, entry := range in {
		entry.Providers = append([]string(nil), entry.Providers...)
		out[templateID] = entry
	}
	return out
}

func cloneRuntimeProviderLists(in map[string][]string) map[string][]string {
	if in == nil {
		return nil
	}
	out := make(map[string][]string, len(in))
	for key, providers := range in {
		out[key] = append([]string(nil), providers...)
	}
	return out
}
