package service

import (
	"errors"
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

func TestExternalRuntimeStartDetailSurvivesStageRefinement(t *testing.T) {
	t.Parallel()

	secret := "sk-" + strings.Repeat("x", 24)
	externalErr := withRuntimeStartUserDetail(
		errors.New("ASB upstream failed"),
		"Aone Sandbox 实例额度已满 token="+secret,
	)
	failure := ClassifyRuntimeStartError(SandboxBackendASB, externalErr)
	refined := refineRuntimeStartFailureAtStage(failure, "sandbox_resolving")

	if refined.Code != "ASB-SANDBOX-RESOLVING-FAILED" || refined.Phase != "sandbox_resolving" {
		t.Fatalf("refined external failure = %+v", refined)
	}
	if !strings.Contains(refined.PublicMessage, "Aone Sandbox 实例额度已满") {
		t.Fatalf("public message lost external detail: %q", refined.PublicMessage)
	}
	if strings.Contains(refined.PublicMessage, secret) || !strings.Contains(refined.PublicMessage, "[REDACTED") {
		t.Fatalf("public message was not redacted: %q", refined.PublicMessage)
	}
}

func TestInternalRuntimeStartErrorKeepsGenericStageMessage(t *testing.T) {
	t.Parallel()

	failure := ClassifyRuntimeStartError(
		SandboxBackendASB,
		errors.New("database password=do-not-expose"),
	)
	refined := refineRuntimeStartFailureAtStage(failure, "sandbox_resolving")
	if refined.PublicMessage != "Runtime 在 sandbox_resolving 阶段启动失败。" {
		t.Fatalf("internal error became user-visible: %q", refined.PublicMessage)
	}
}

func TestASBCapacityErrorUsesGenericExternalDetailChannel(t *testing.T) {
	t.Parallel()

	failure := ClassifyRuntimeStartError(SandboxBackendASB, ErrASBCapacityUnavailable)
	refined := refineRuntimeStartFailureAtStage(failure, "sandbox_resolving")
	if refined.Code != "ASB-SANDBOX-RESOLVING-FAILED" {
		t.Fatalf("ASB capacity code was specially classified: %q", refined.Code)
	}
	if refined.PublicMessage != asbCapacityUnavailableMessage {
		t.Fatalf("ASB capacity detail = %q", refined.PublicMessage)
	}
}

func TestASBReleaseManifestRequiresSchemaSevenAndCurrentCapabilities(t *testing.T) {
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
	for _, schemaVersion := range []int{3, 4, 5, 6} {
		manifest["schema_version"] = schemaVersion
		if err := validateASBRuntimeManifest(manifest); err != nil {
			t.Fatalf("known ASB runtime manifest schema %d rejected: %v", schemaVersion, err)
		}
	}
	for _, schemaVersion := range []int{2, 8} {
		manifest["schema_version"] = schemaVersion
		if err := validateASBRuntimeManifest(manifest); err == nil {
			t.Fatalf("unknown ASB runtime manifest schema %d accepted", schemaVersion)
		}
	}
	manifest["capabilities_by_backend"] = map[string][]string{
		"asb": {"dws", "mcp", "a1", "mw", "buc", RuntimeStartCapabilityEventsV1, LLMTraceCapability, A2AInvocationV2Capability},
	}
	manifest["schema_version"] = 5
	if err := validateASBReleaseManifest(manifest); err == nil {
		t.Fatal("new ASB release accepted with a legacy manifest schema")
	}
	manifest["schema_version"] = 7
	manifest["providers"] = []string{"hermes", "opencode", "pi", "dsh", "opencode-v2", "claude", "codex"}
	manifest["capabilities_by_backend"] = map[string][]string{
		"asb": {"dws", "mcp", "a1", "mw", "buc", RuntimeStartCapabilityEventsV1, A2AInvocationV2Capability, DSHTrajectoryCapability},
	}
	if err := validateASBReleaseManifest(manifest); err == nil {
		t.Fatal("new ASB release accepted without LLM trace capability")
	}
	manifest["capabilities_by_backend"] = map[string][]string{
		"asb": {"dws", "mcp", "a1", "mw", "buc", RuntimeStartCapabilityEventsV1, LLMTraceCapability, DSHTrajectoryCapability},
	}
	if err := validateASBReleaseManifest(manifest); err == nil {
		t.Fatal("new ASB release accepted without A2A invocation v2 capability")
	}
	manifest["capabilities_by_backend"] = map[string][]string{
		"asb": {"dws", "mcp", "a1", "mw", "buc", RuntimeStartCapabilityEventsV1, LLMTraceCapability, A2AInvocationV2Capability},
	}
	if err := validateASBReleaseManifest(manifest); err == nil {
		t.Fatal("new ASB release accepted without DSH trajectory capability")
	}
	manifest["capabilities_by_backend"] = map[string][]string{
		"asb": {"dws", "mcp", "a1", "mw", "buc", RuntimeStartCapabilityEventsV1, LLMTraceCapability, A2AInvocationV2Capability, DSHTrajectoryCapability},
	}
	if err := validateASBReleaseManifest(manifest); err != nil {
		t.Fatalf("new ASB release manifest rejected: %v", err)
	}
}
