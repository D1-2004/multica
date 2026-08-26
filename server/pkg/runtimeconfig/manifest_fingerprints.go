package runtimeconfig

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"regexp"
	"sort"
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

// decodeManifestFingerprints performs the JSON half of parsing: unknown fields
// and trailing values are rejected. Shared by the strict and structural parsers
// so the two can never disagree about what a well-formed document looks like.
func decodeManifestFingerprints(data []byte) (ManifestFingerprintsConfig, error) {
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
	return cfg, nil
}

// parseManifestFingerprintsStructural is what the running server uses: the
// document must be well-formed and indexable, but its keys are taken as
// authored. See Service.ApplyManifestFingerprintsJSON.
func parseManifestFingerprintsStructural(data []byte) (ManifestFingerprintsConfig, error) {
	cfg, err := decodeManifestFingerprints(data)
	if err != nil {
		return ManifestFingerprintsConfig{}, err
	}
	if err := cfg.ValidateStructure(); err != nil {
		return ManifestFingerprintsConfig{}, err
	}
	return cloneManifestFingerprintsConfig(cfg), nil
}

// logManifestFingerprintsMismatch names the disagreeing keys once per apply so
// a stale document is visible in the log tail instead of surfacing later as an
// FC/E2B template that silently never appears.
func (s *Service) logManifestFingerprintMismatches(cfg ManifestFingerprintsConfig) {
	if s == nil || s.logger == nil {
		return
	}
	mismatches, err := cfg.FingerprintMismatches()
	if err != nil || len(mismatches) == 0 {
		return
	}
	for _, mismatch := range mismatches {
		s.logger.Error("Runtime manifest fingerprint does not match this binary's component contract; entry kept as authored",
			slog.String("data_id", ManifestFingerprintsDiamondDataID),
			slog.String("fingerprint", mismatch.Fingerprint),
			slog.String("expected_fingerprint", mismatch.Expected),
			slog.String("impact", "templates published under this fingerprint resolve only if the Runtime image mints the same key"),
		)
	}
}

// ParseManifestFingerprintsStrict decodes one complete fingerprint document.
// Unknown fields and trailing JSON are rejected before validating the exact
// fingerprint and component-key contracts.
func ParseManifestFingerprintsStrict(data []byte) (ManifestFingerprintsConfig, error) {
	cfg, err := decodeManifestFingerprints(data)
	if err != nil {
		return ManifestFingerprintsConfig{}, err
	}
	if err := cfg.Validate(); err != nil {
		return ManifestFingerprintsConfig{}, err
	}
	return cloneManifestFingerprintsConfig(cfg), nil
}

// Validate enforces the full contract: the document is structurally sound AND
// every key equals the fingerprint this binary computes for its component
// versions. Used by ParseManifestFingerprintsStrict, which is what CI and the
// documented example are checked against.
//
// The running server does NOT use this. See ValidateStructure.
func (c ManifestFingerprintsConfig) Validate() error {
	if err := c.ValidateStructure(); err != nil {
		return err
	}
	mismatches, err := c.FingerprintMismatches()
	if err != nil {
		return err
	}
	if len(mismatches) > 0 {
		return errors.New("Runtime manifest fingerprint does not match its component versions")
	}
	return nil
}

// ValidateStructure checks everything that makes the document usable at all:
// schema version, key shape, the exact six component keys, and version charset.
// A document failing any of these cannot be indexed and is a real load failure.
//
// Deliberately excludes the checksum comparison. See FingerprintMismatches.
func (c ManifestFingerprintsConfig) ValidateStructure() error {
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
	}
	return nil
}

// ManifestFingerprintMismatch reports one key whose checksum disagrees with what
// this binary computes for the component versions filed under it.
type ManifestFingerprintMismatch struct {
	Fingerprint string
	Expected    string
}

// FingerprintMismatches lists the keys this binary would have computed
// differently. It is a disagreement, not a corruption — and which side is right
// is not knowable from here.
//
// The key is minted when a Runtime sandbox image is built and travels to the
// server inside the template alias (`multica-m7-v<16hex>-r1-<6hex>`), where
// service.applyFCE2BTemplateManifestAlias looks it up. Recomputing it from
// manifestProviders / manifestCapabilities asserts that the operator's document
// agrees with THIS binary's compiled contract — so the moment the server's
// contract moves ahead of the deployed images (adding a provider changes the
// hash), a document that is still correct for every image in production reads
// as invalid.
//
// A miss costs one template not being published (applyFCE2BTemplateManifestAlias
// returns false and the template is skipped). That is a degraded cloud-runtime
// catalog, which is why it must never be a reason to refuse to start: see
// Service.ApplyManifestFingerprintsJSON.
func (c ManifestFingerprintsConfig) FingerprintMismatches() ([]ManifestFingerprintMismatch, error) {
	var mismatches []ManifestFingerprintMismatch
	for fingerprint, components := range c.Fingerprints {
		expected, err := manifestFingerprint(components)
		if err != nil {
			return nil, err
		}
		if fingerprint != expected {
			mismatches = append(mismatches, ManifestFingerprintMismatch{Fingerprint: fingerprint, Expected: expected})
		}
	}
	sort.Slice(mismatches, func(i, j int) bool { return mismatches[i].Fingerprint < mismatches[j].Fingerprint })
	return mismatches, nil
}

func manifestFingerprint(components map[string]string) (string, error) {
	return manifestFingerprintForProviders(components, manifestProviders[:])
}

func manifestFingerprintForProviders(components map[string]string, providers []string) (string, error) {
	contract := map[string]any{
		"capabilities":       ManifestCapabilitiesForProviders(providers),
		"component_versions": components,
		"providers":          providers,
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

// ManifestCapabilitiesForProviders derives the schema-v7 image capability set
// in stable wire order. Shared execution capabilities are always present;
// provider-specific capabilities are advertised only when the corresponding
// runner is actually part of the image.
func ManifestCapabilitiesForProviders(providers []string) []string {
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

// ManifestProvidersForFingerprint recovers the exact non-empty provider set
// that minted one schema-v7 alias. Provider cardinality is part of the alias
// hash, while Diamond deliberately stores only the six component versions.
// Enumerating the bounded server-supported provider subsets lets older
// five-provider images and newer images with additional providers coexist
// without making the catalog claim a binary exists when it does not.
func ManifestProvidersForFingerprint(
	fingerprint string,
	components map[string]string,
) ([]string, bool, error) {
	if !manifestFingerprintPattern.MatchString(fingerprint) {
		return nil, false, errors.New("Runtime manifest fingerprint must be exactly 16 lowercase hexadecimal characters")
	}
	var matched []string
	for mask := 1; mask < 1<<len(manifestProviders); mask++ {
		providers := make([]string, 0, len(manifestProviders))
		for index, provider := range manifestProviders {
			if mask&(1<<index) != 0 {
				providers = append(providers, provider)
			}
		}
		candidate, err := manifestFingerprintForProviders(components, providers)
		if err != nil {
			return nil, false, err
		}
		if candidate != fingerprint {
			continue
		}
		if matched != nil {
			return nil, false, errors.New("Runtime manifest fingerprint matches multiple provider sets")
		}
		matched = providers
	}
	if matched == nil {
		return nil, false, nil
	}
	return append([]string(nil), matched...), true, nil
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
// once the document is structurally sound.
//
// A checksum disagreement is reported, not refused. The keys are minted by
// Runtime sandbox images and this binary's expectation of them changes whenever
// the compiled provider or capability list does — so treating a disagreement as
// a load failure makes every such change a flag day that can only be survived by
// updating an operator-managed Diamond document in the same instant. It was
// worse than that at startup: NewFromEnv turned the error into os.Exit(1), so a
// document that was still correct for every deployed image took the whole server
// down and left the deploy rolling back, while the identical document arriving
// through the live listener was tolerated (the callback logs and retains). One
// of those two behaviours had to go, and the fatal one is the wrong one — the
// cost of a stale entry is one unpublished FC/E2B template, not an outage.
//
// The entries are stored exactly as authored. Re-keying them to the computed
// value would be a guess about which side is stale, and would break lookups
// whenever the images, not the document, are the ones that are right.
func (s *Service) ApplyManifestFingerprintsJSON(data []byte) (ManifestFingerprintsSnapshot, error) {
	if s == nil {
		return ManifestFingerprintsSnapshot{}, errors.New("runtime config service is nil")
	}
	s.manifestFingerprintsApplyMu.Lock()
	defer s.manifestFingerprintsApplyMu.Unlock()

	cfg, err := parseManifestFingerprintsStructural(data)
	if err != nil {
		return s.ManifestFingerprints(), err
	}
	s.logManifestFingerprintMismatches(cfg)
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
