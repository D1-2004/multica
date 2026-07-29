package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type fakeCloudSandboxRuntimeStore struct {
	runtime db.AgentRuntime
}

func (f fakeCloudSandboxRuntimeStore) GetAgentRuntime(context.Context, pgtype.UUID) (db.AgentRuntime, error) {
	return f.runtime, nil
}

type recordingCloudSandboxLauncher struct {
	calls int
}

func (r *recordingCloudSandboxLauncher) LaunchTask(context.Context, db.AgentTaskQueue) error {
	r.calls++
	return nil
}

func TestParseCloudSandboxRuntimeProjectsLegacyFCMetadata(t *testing.T) {
	t.Parallel()

	runtime := db.AgentRuntime{
		RuntimeMode: "cloud",
		Provider:    "opencode",
		Metadata: json.RawMessage(`{
			"kind": "fc-e2b",
			"template": "fc-template",
			"template_id": "template-id",
			"template_build_id": "build-id",
			"template_channel": "candidate",
			"capabilities": ["dws", "mcp"]
		}`),
	}
	metadata, err := ParseCloudSandboxRuntime(runtime)
	if err != nil {
		t.Fatalf("ParseCloudSandboxRuntime: %v", err)
	}
	if metadata.SandboxBackend != SandboxBackendAliyunFC ||
		metadata.ArtifactKind != CloudSandboxArtifactE2BTemplate ||
		metadata.ArtifactRef != "template-id" ||
		metadata.ArtifactBuildID != "build-id" ||
		metadata.ArtifactChannel != CloudSandboxChannelCandidate ||
		metadata.Provider != "opencode" {
		t.Fatalf("legacy metadata = %#v", metadata)
	}
}

func TestParseCloudSandboxRuntimeRequiresImmutableASBImage(t *testing.T) {
	t.Parallel()

	valid := db.AgentRuntime{
		RuntimeMode: "cloud",
		Provider:    "hermes",
		Metadata: json.RawMessage(`{
			"kind": "cloud-sandbox",
			"sandbox_backend": "asb",
			"provider": "hermes",
			"artifact_kind": "oci_image",
			"artifact_channel": "stable",
			"artifact_ref": "hub.example/runtime@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			"artifact_build_id": "aone-run:123",
			"artifact_digest": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			"manifest_version": 3,
			"runner_protocol": "root-log-v1",
			"runner": "multica-fc-hermes-container-log-entry",
			"capabilities": ["dws", "a1", "mw", "buc"]
		}`),
	}
	metadata, err := ParseCloudSandboxRuntime(valid)
	if err != nil {
		t.Fatalf("ParseCloudSandboxRuntime: %v", err)
	}
	if metadata.SandboxBackend != SandboxBackendASB ||
		metadata.ArtifactKind != CloudSandboxArtifactOCIImage ||
		metadata.ManifestVersion != 3 ||
		!CloudSandboxRuntimeHasCapability(valid, "A1") {
		t.Fatalf("ASB metadata = %#v", metadata)
	}

	var mutable map[string]any
	if err := json.Unmarshal(valid.Metadata, &mutable); err != nil {
		t.Fatal(err)
	}
	mutable["artifact_ref"] = "hub.example/runtime:latest"
	valid.Metadata, _ = json.Marshal(mutable)
	if _, err := ParseCloudSandboxRuntime(valid); !errors.Is(err, ErrCloudSandboxMetadata) {
		t.Fatalf("mutable ASB image error = %v", err)
	}
}

func TestParseCloudSandboxRuntimeRejectsBackendArtifactMismatch(t *testing.T) {
	t.Parallel()

	runtime := db.AgentRuntime{
		RuntimeMode: "cloud",
		Provider:    "pi",
		Metadata: json.RawMessage(`{
			"kind": "cloud-sandbox",
			"sandbox_backend": "aliyun_fc",
			"provider": "pi",
			"artifact_kind": "oci_image",
			"artifact_ref": "image@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		}`),
	}
	if _, err := ParseCloudSandboxRuntime(runtime); !errors.Is(err, ErrCloudSandboxMetadata) {
		t.Fatalf("backend/artifact mismatch error = %v", err)
	}
}

func TestCloudSandboxLauncherDispatchesOnlyToSelectedBackend(t *testing.T) {
	t.Parallel()

	runtimeID := util.MustParseUUID("11111111-1111-1111-1111-111111111111")
	asb := &recordingCloudSandboxLauncher{}
	fc := &recordingCloudSandboxLauncher{}
	launcher := NewCloudSandboxLauncher(fakeCloudSandboxRuntimeStore{
		runtime: db.AgentRuntime{
			ID:          runtimeID,
			RuntimeMode: "cloud",
			Provider:    "hermes",
			Metadata: json.RawMessage(`{
				"kind":"cloud-sandbox",
				"sandbox_backend":"asb",
				"provider":"hermes",
				"artifact_kind":"oci_image",
				"artifact_ref":"hub.example/runtime@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			}`),
		},
	}, fc, asb)
	if err := launcher.LaunchTask(context.Background(), db.AgentTaskQueue{RuntimeID: runtimeID}); err != nil {
		t.Fatalf("LaunchTask: %v", err)
	}
	if asb.calls != 1 || fc.calls != 0 {
		t.Fatalf("ASB calls=%d FC calls=%d", asb.calls, fc.calls)
	}
}
