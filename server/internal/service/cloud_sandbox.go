package service

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type SandboxBackendKind string

const (
	CloudSandboxMetadataKind = "cloud-sandbox"

	SandboxBackendAliyunFC SandboxBackendKind = "aliyun_fc"
	SandboxBackendASB      SandboxBackendKind = "asb"

	CloudSandboxArtifactE2BTemplate = "e2b_template"
	CloudSandboxArtifactOCIImage    = "oci_image"

	CloudSandboxChannelStable    = "stable"
	CloudSandboxChannelCandidate = "candidate"
)

var (
	ErrCloudSandboxRuntimeRequired = errors.New("runtime is not a cloud sandbox runtime")
	ErrCloudSandboxMetadata        = errors.New("cloud sandbox runtime metadata is invalid")
	cloudSandboxOCIDigestPattern   = regexp.MustCompile(`^.+@sha256:[0-9a-f]{64}$`)
)

// CloudSandboxRuntimeMetadata is the normalized server model. Legacy
// kind=fc-e2b rows are projected into this shape without rewriting them.
type CloudSandboxRuntimeMetadata struct {
	Kind              string
	SandboxBackend    SandboxBackendKind
	Provider          string
	ArtifactKind      string
	ArtifactChannel   string
	ArtifactRef       string
	ArtifactBuildID   string
	ArtifactAlias     string
	ArtifactDigest    string
	ManifestVersion   int
	RunnerProtocol    string
	Runner            string
	Capabilities      []string
	ComponentVersions map[string]string
}

type cloudSandboxMetadataWire struct {
	Kind              string            `json:"kind"`
	SandboxBackend    string            `json:"sandbox_backend"`
	Provider          string            `json:"provider"`
	ArtifactKind      string            `json:"artifact_kind"`
	ArtifactChannel   string            `json:"artifact_channel"`
	ArtifactRef       string            `json:"artifact_ref"`
	ArtifactBuildID   string            `json:"artifact_build_id"`
	ArtifactAlias     string            `json:"artifact_alias"`
	ArtifactDigest    string            `json:"artifact_digest"`
	ManifestVersion   int               `json:"manifest_version"`
	RunnerProtocol    string            `json:"runner_protocol"`
	Runner            string            `json:"runner"`
	Capabilities      []string          `json:"capabilities"`
	ComponentVersions map[string]string `json:"component_versions"`

	// Legacy FC/E2B metadata.
	Template        string `json:"template"`
	TemplateID      string `json:"template_id"`
	TemplateBuildID string `json:"template_build_id"`
	TemplateAlias   string `json:"template_alias"`
	TemplateChannel string `json:"template_channel"`
}

func ParseCloudSandboxRuntime(rt db.AgentRuntime) (CloudSandboxRuntimeMetadata, error) {
	if rt.RuntimeMode != "cloud" || len(rt.Metadata) == 0 {
		return CloudSandboxRuntimeMetadata{}, ErrCloudSandboxRuntimeRequired
	}
	var wire cloudSandboxMetadataWire
	if err := json.Unmarshal(rt.Metadata, &wire); err != nil {
		return CloudSandboxRuntimeMetadata{}, ErrCloudSandboxMetadata
	}
	wire.Kind = strings.TrimSpace(wire.Kind)
	switch wire.Kind {
	case FCE2BMetadataKind:
		provider := strings.ToLower(strings.TrimSpace(rt.Provider))
		if provider == "" {
			provider = FCE2BProvider
		}
		artifactRef := firstNonEmptyString(wire.TemplateID, wire.Template)
		channel := normalizeCloudSandboxChannel(wire.TemplateChannel)
		return CloudSandboxRuntimeMetadata{
			Kind:              FCE2BMetadataKind,
			SandboxBackend:    SandboxBackendAliyunFC,
			Provider:          provider,
			ArtifactKind:      CloudSandboxArtifactE2BTemplate,
			ArtifactChannel:   channel,
			ArtifactRef:       artifactRef,
			ArtifactBuildID:   strings.TrimSpace(wire.TemplateBuildID),
			ArtifactAlias:     firstNonEmptyString(wire.TemplateAlias, wire.Template),
			ManifestVersion:   wire.ManifestVersion,
			RunnerProtocol:    strings.TrimSpace(wire.RunnerProtocol),
			Runner:            strings.TrimSpace(wire.Runner),
			Capabilities:      normalizeCloudSandboxCapabilities(wire.Capabilities),
			ComponentVersions: cloneStringMap(wire.ComponentVersions),
		}, nil
	case CloudSandboxMetadataKind:
		backend := SandboxBackendKind(strings.ToLower(strings.TrimSpace(wire.SandboxBackend)))
		if backend != SandboxBackendAliyunFC && backend != SandboxBackendASB {
			return CloudSandboxRuntimeMetadata{}, ErrCloudSandboxMetadata
		}
		provider := strings.ToLower(strings.TrimSpace(wire.Provider))
		if provider == "" {
			provider = strings.ToLower(strings.TrimSpace(rt.Provider))
		}
		if !IsFCE2BSupportedProvider(provider) {
			return CloudSandboxRuntimeMetadata{}, ErrCloudSandboxMetadata
		}
		artifactKind := strings.ToLower(strings.TrimSpace(wire.ArtifactKind))
		if (backend == SandboxBackendAliyunFC && artifactKind != CloudSandboxArtifactE2BTemplate) ||
			(backend == SandboxBackendASB && artifactKind != CloudSandboxArtifactOCIImage) {
			return CloudSandboxRuntimeMetadata{}, ErrCloudSandboxMetadata
		}
		artifactRef := strings.TrimSpace(wire.ArtifactRef)
		if artifactRef == "" {
			return CloudSandboxRuntimeMetadata{}, ErrCloudSandboxMetadata
		}
		if backend == SandboxBackendASB {
			if !cloudSandboxOCIDigestPattern.MatchString(artifactRef) {
				return CloudSandboxRuntimeMetadata{}, ErrCloudSandboxMetadata
			}
			digest := artifactRef[strings.LastIndex(artifactRef, "@")+1:]
			if strings.TrimSpace(wire.ArtifactDigest) != "" &&
				strings.TrimSpace(wire.ArtifactDigest) != digest {
				return CloudSandboxRuntimeMetadata{}, ErrCloudSandboxMetadata
			}
		}
		channel := normalizeCloudSandboxChannel(wire.ArtifactChannel)
		return CloudSandboxRuntimeMetadata{
			Kind:              CloudSandboxMetadataKind,
			SandboxBackend:    backend,
			Provider:          provider,
			ArtifactKind:      artifactKind,
			ArtifactChannel:   channel,
			ArtifactRef:       artifactRef,
			ArtifactBuildID:   strings.TrimSpace(wire.ArtifactBuildID),
			ArtifactAlias:     strings.TrimSpace(wire.ArtifactAlias),
			ArtifactDigest:    strings.TrimSpace(wire.ArtifactDigest),
			ManifestVersion:   wire.ManifestVersion,
			RunnerProtocol:    strings.TrimSpace(wire.RunnerProtocol),
			Runner:            strings.TrimSpace(wire.Runner),
			Capabilities:      normalizeCloudSandboxCapabilities(wire.Capabilities),
			ComponentVersions: cloneStringMap(wire.ComponentVersions),
		}, nil
	default:
		return CloudSandboxRuntimeMetadata{}, ErrCloudSandboxRuntimeRequired
	}
}

func IsCloudSandboxRuntime(rt db.AgentRuntime) bool {
	_, err := ParseCloudSandboxRuntime(rt)
	return err == nil
}

func IsASBRuntime(rt db.AgentRuntime) bool {
	metadata, err := ParseCloudSandboxRuntime(rt)
	return err == nil && metadata.SandboxBackend == SandboxBackendASB
}

func CloudSandboxBackend(rt db.AgentRuntime) SandboxBackendKind {
	metadata, err := ParseCloudSandboxRuntime(rt)
	if err != nil {
		return ""
	}
	return metadata.SandboxBackend
}

func CloudSandboxRuntimeProvider(rt db.AgentRuntime) string {
	metadata, err := ParseCloudSandboxRuntime(rt)
	if err != nil {
		return ""
	}
	return metadata.Provider
}

func CloudSandboxRuntimeHasCapability(rt db.AgentRuntime, capability string) bool {
	metadata, err := ParseCloudSandboxRuntime(rt)
	if err != nil {
		return false
	}
	capability = strings.ToLower(strings.TrimSpace(capability))
	if capability == "" {
		return false
	}
	for _, candidate := range metadata.Capabilities {
		if candidate == capability {
			return true
		}
	}
	return false
}

func CloudSandboxRuntimeChannel(rt db.AgentRuntime) string {
	metadata, err := ParseCloudSandboxRuntime(rt)
	if err != nil {
		return ""
	}
	return metadata.ArtifactChannel
}

func normalizeCloudSandboxChannel(raw string) string {
	if strings.EqualFold(strings.TrimSpace(raw), CloudSandboxChannelCandidate) {
		return CloudSandboxChannelCandidate
	}
	return CloudSandboxChannelStable
}

func normalizeCloudSandboxCapabilities(values []string) []string {
	normalized := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		normalized = append(normalized, value)
	}
	return normalized
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
