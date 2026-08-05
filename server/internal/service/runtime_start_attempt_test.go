package service

import (
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestRuntimeStartProtocolIsCapabilityNegotiated(t *testing.T) {
	t.Parallel()

	legacy := db.AgentRuntime{
		RuntimeMode: "cloud",
		Provider:    "hermes",
		Metadata:    []byte(`{"kind":"fc-e2b","capabilities":["dws","mcp"]}`),
	}
	if got := runtimeStartProtocolForRuntime(legacy); got != RuntimeStartProtocolLegacyV1 {
		t.Fatalf("legacy Runtime protocol = %q", got)
	}

	capableFC := legacy
	capableFC.Metadata = []byte(`{"kind":"fc-e2b","capabilities":["dws","mcp","runtime_start_events_v1"]}`)
	if got := runtimeStartProtocolForRuntime(capableFC); got != RuntimeStartProtocolHTTPJSONV1 {
		t.Fatalf("capable FC Runtime protocol = %q", got)
	}

	capableASB := db.AgentRuntime{
		RuntimeMode: "cloud",
		Provider:    "pi",
		Metadata: []byte(`{
			"kind":"cloud-sandbox",
			"sandbox_backend":"asb",
			"provider":"pi",
			"artifact_kind":"oci_image",
			"artifact_channel":"stable",
			"artifact_ref":"runtime@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			"artifact_build_id":"build-1",
			"artifact_digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			"capabilities":["runtime_start_events_v1"]
		}`),
	}
	if got := runtimeStartProtocolForRuntime(capableASB); got != RuntimeStartProtocolHTTPJSONV1 {
		t.Fatalf("capable ASB Runtime protocol = %q", got)
	}
}

func TestRuntimeStartUserMessageIsStructuredAndDoesNotExposeInternalDetail(t *testing.T) {
	t.Parallel()

	taskID := util.MustParseUUID("11111111-1111-4111-8111-111111111111")
	failure := NewRuntimeStartFailure(
		SandboxBackendAliyunFC,
		"FCE2B-RUNNER-CLAIM-TIMEOUT",
		"daemon_started",
		true,
		"Runner 已提交，但没有在规定时间内完成任务领取。",
		"internal upstream token=secret-value",
	)
	message := FormatRuntimeStartUserMessage(taskID, failure)
	for _, expected := range []string{
		"FCE2B-RUNNER-CLAIM-TIMEOUT",
		"daemon_started",
		util.UUIDToString(taskID),
	} {
		if !strings.Contains(message, expected) {
			t.Fatalf("user message %q does not contain %q", message, expected)
		}
	}
	if strings.Contains(message, "secret-value") || strings.Contains(message, "internal upstream") {
		t.Fatalf("user message exposes internal detail: %q", message)
	}
}

func TestValidateRuntimeStartStage(t *testing.T) {
	t.Parallel()

	if err := ValidateRuntimeStartStage("daemon_started"); err != nil {
		t.Fatalf("valid stage rejected: %v", err)
	}
	for _, invalid := range []string{"", "DaemonStarted", "../claim", strings.Repeat("a", 65)} {
		if err := ValidateRuntimeStartStage(invalid); err == nil {
			t.Fatalf("invalid stage %q accepted", invalid)
		}
	}
}

func TestGenericRuntimeStartFailureIsRefinedToLastObservedStage(t *testing.T) {
	t.Parallel()
	failure := ClassifyRuntimeStartFailure(SandboxBackendASB, "unexpected transport failure")
	refined := refineRuntimeStartFailureAtStage(failure, "runner_probing")
	if refined.Code != "ASB-RUNNER-PROBING-FAILED" || refined.Phase != "runner_probing" {
		t.Fatalf("refined failure = %+v", refined)
	}
	if !strings.Contains(refined.PublicMessage, "runner_probing") || !strings.Contains(refined.InternalDetail, "last_stage=runner_probing") {
		t.Fatalf("refined failure lacks stage detail: %+v", refined)
	}
}

func TestASBReleaseManifestRequiresAdditiveStartupEventsCapability(t *testing.T) {
	t.Parallel()

	manifest := map[string]any{
		"schema_version":   3,
		"sandbox_backends": []string{"aliyun_fc", "asb"},
		"providers":        []string{"hermes", "opencode", "pi"},
		"capabilities_by_backend": map[string][]string{
			"asb": {"dws", "mcp", "a1", "mw", "buc"},
		},
		"identity_modes_by_backend": map[string][]string{
			"asb": {"agent_identity", "spiffe", "buc_wireguard"},
		},
		"runner_protocol": "root-log-v1",
	}
	if err := validateASBRuntimeManifest(manifest); err != nil {
		t.Fatalf("existing ASB runtime manifest rejected: %v", err)
	}
	if err := validateASBReleaseManifest(manifest); err == nil {
		t.Fatal("new ASB release accepted without runtime start events")
	}
	manifest["capabilities_by_backend"] = map[string][]string{
		"asb": {"dws", "mcp", "a1", "mw", "buc", RuntimeStartCapabilityEventsV1},
	}
	if err := validateASBReleaseManifest(manifest); err != nil {
		t.Fatalf("new ASB release manifest rejected: %v", err)
	}
}
