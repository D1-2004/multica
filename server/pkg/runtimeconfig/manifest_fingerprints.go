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
	ManifestFingerprintsSchemaVersion = 1
	ManifestFingerprintsDiamondDataID = "dt-fde-multica-runtime-manifest-fingerprints.json"
)

var (
	manifestFingerprintPattern = regexp.MustCompile(`^[0-9a-f]{16}$`)
	manifestComponentVersion   = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
	manifestComponentKeys      = [...]string{"hermes", "opencode", "opencode-v2", "dsh", "pi", "dws"}
	manifestProviders          = [...]string{"hermes", "opencode", "pi", "dsh", "opencode-v2", "claude", "codex"}
	manifestCapabilities       = [...]string{
		"dws",
		"dws.im_event",
		"mcp",
		"runtime_start_events_v1",
		"llm_trace_v1",
		"a2a_inbound_opencode_v1",
		"a2a-invocation-v2",
		"a2a_inbound_hermes_v1",
		"a2a_inbound_pi_v1",
		"dsh_trajectory_v1",
	}
)

const manifestRunnerProtocol = "root-log-v1"

// ManifestFingerprintsConfig is the complete Diamond document that resolves a
// compact Runtime manifest fingerprint to the exact component versions used to
// produce it.
type ManifestFingerprintsConfig struct {
	Version      int                          `json:"version"`
	Fingerprints map[string]map[string]string `json:"fingerprints"`
}

// ManifestFingerprintsSnapshot is an immutable point-in-time view. Callers get
// a deep copy from Service.ManifestFingerprints and cannot mutate service state.
type ManifestFingerprintsSnapshot struct {
	Version      int
	Fingerprints map[string]map[string]string
	Generation   uint64
	SHA256       string
}

// ParseManifestFingerprintsStrict decodes one complete fingerprint document.
// Unknown fields and trailing JSON are rejected before validating the exact
// fingerprint and component-key contracts.
func ParseManifestFingerprintsStrict(data []byte) (ManifestFingerprintsConfig, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var cfg ManifestFingerprintsConfig
	if err := decoder.Decode(&cfg); err != nil {
		return ManifestFingerprintsConfig{}, fmt.Errorf("parse Runtime manifest fingerprints: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return ManifestFingerprintsConfig{}, errors.New("Runtime manifest fingerprints contain multiple JSON values")
		}
		return ManifestFingerprintsConfig{}, fmt.Errorf("parse trailing Runtime manifest fingerprints: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return ManifestFingerprintsConfig{}, err
	}
	return cloneManifestFingerprintsConfig(cfg), nil
}

func (c ManifestFingerprintsConfig) Validate() error {
	if c.Version != ManifestFingerprintsSchemaVersion {
		return fmt.Errorf("Runtime manifest fingerprints version must be %d", ManifestFingerprintsSchemaVersion)
	}
	if len(c.Fingerprints) == 0 {
		return errors.New("Runtime manifest fingerprints must not be empty")
	}
	for fingerprint, components := range c.Fingerprints {
		if !manifestFingerprintPattern.MatchString(fingerprint) {
			return errors.New("Runtime manifest fingerprint keys must be exactly 16 lowercase hexadecimal characters")
		}
		if len(components) != len(manifestComponentKeys) {
			return errors.New("each Runtime manifest fingerprint must contain exactly six component versions")
		}
		for _, key := range manifestComponentKeys {
			version, ok := components[key]
			if !ok {
				return errors.New("each Runtime manifest fingerprint must contain the exact required component keys")
			}
			if !manifestComponentVersion.MatchString(version) || strings.TrimSpace(version) != version {
				return errors.New("Runtime manifest component versions contain unsupported characters")
			}
		}
		expected, err := manifestFingerprint(components)
		if err != nil {
			return err
		}
		if fingerprint != expected {
			return errors.New("Runtime manifest fingerprint does not match its component versions")
		}
	}
	return nil
}

func manifestFingerprint(components map[string]string) (string, error) {
	contract := map[string]any{
		"capabilities":       manifestCapabilities[:],
		"component_versions": components,
		"providers":          manifestProviders[:],
		"runner_protocol":    manifestRunnerProtocol,
		"schema_version":     7,
	}
	encoded, err := json.Marshal(contract)
	if err != nil {
		return "", fmt.Errorf("encode Runtime manifest fingerprint contract: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:8]), nil
}

// ManifestFingerprints returns a safe deep copy of the last valid snapshot.
func (s *Service) ManifestFingerprints() ManifestFingerprintsSnapshot {
	if s == nil {
		return ManifestFingerprintsSnapshot{}
	}
	current := s.manifestFingerprints.Load()
	if current == nil {
		return ManifestFingerprintsSnapshot{}
	}
	return cloneManifestFingerprintsSnapshot(*current)
}

// ApplyManifestFingerprintsJSON atomically replaces the fingerprint snapshot
// only after the complete document passes strict validation.
func (s *Service) ApplyManifestFingerprintsJSON(data []byte) (ManifestFingerprintsSnapshot, error) {
	if s == nil {
		return ManifestFingerprintsSnapshot{}, errors.New("runtime config service is nil")
	}
	s.manifestFingerprintsApplyMu.Lock()
	defer s.manifestFingerprintsApplyMu.Unlock()

	cfg, err := ParseManifestFingerprintsStrict(data)
	if err != nil {
		return s.ManifestFingerprints(), err
	}
	previous := s.manifestFingerprints.Load()
	generation := uint64(1)
	if previous != nil {
		generation = previous.Generation + 1
	}
	sum := sha256.Sum256(data)
	next := &ManifestFingerprintsSnapshot{
		Version:      cfg.Version,
		Fingerprints: cloneManifestFingerprints(cfg.Fingerprints),
		Generation:   generation,
		SHA256:       hex.EncodeToString(sum[:]),
	}
	s.manifestFingerprints.Store(next)
	return cloneManifestFingerprintsSnapshot(*next), nil
}

func cloneManifestFingerprintsConfig(in ManifestFingerprintsConfig) ManifestFingerprintsConfig {
	return ManifestFingerprintsConfig{
		Version:      in.Version,
		Fingerprints: cloneManifestFingerprints(in.Fingerprints),
	}
}

func cloneManifestFingerprintsSnapshot(in ManifestFingerprintsSnapshot) ManifestFingerprintsSnapshot {
	out := in
	out.Fingerprints = cloneManifestFingerprints(in.Fingerprints)
	return out
}

func cloneManifestFingerprints(in map[string]map[string]string) map[string]map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]map[string]string, len(in))
	for fingerprint, components := range in {
		componentCopy := make(map[string]string, len(components))
		for name, version := range components {
			componentCopy[name] = version
		}
		out[fingerprint] = componentCopy
	}
	return out
}
