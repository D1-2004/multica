package service

import (
	"encoding/json"
	"testing"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestFilterAgentSkillsForRuntimeUsesCloudSandboxCapabilities(t *testing.T) {
	t.Parallel()

	fcRuntime := db.AgentRuntime{
		RuntimeMode: "cloud",
		Provider:    "hermes",
		Metadata:    []byte(`{"kind":"fc-e2b","capabilities":["hermes","dws"]}`),
	}
	asbRuntime := db.AgentRuntime{
		RuntimeMode: "cloud",
		Provider:    "hermes",
		Metadata: []byte(`{
			"kind":"cloud-sandbox",
			"sandbox_backend":"asb",
			"provider":"hermes",
			"artifact_kind":"oci_image",
			"artifact_ref":"registry.example/runtime@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			"capabilities":["hermes","dws","a1","buc"]
		}`),
	}
	skills := []AgentSkillData{
		{Name: "common", Config: json.RawMessage(`{}`)},
		{Name: "a1-only", Config: json.RawMessage(`{"execution":{"required_runtime_capabilities":["a1"]}}`)},
		{Name: "a1-and-buc", Config: json.RawMessage(`{"execution":{"required_runtime_capabilities":["a1","buc"]}}`)},
		{Name: "asb-only", Config: json.RawMessage(`{"execution":{"required_sandbox_backends":["asb"]}}`)},
	}

	if got := skillNames(filterAgentSkillsForRuntime(skills, fcRuntime, SandboxBackendAliyunFC)); !equalStrings(got, []string{"common"}) {
		t.Fatalf("FC-visible skills = %v, want [common]", got)
	}
	if got := skillNames(filterAgentSkillsForRuntime(skills, asbRuntime, SandboxBackendASB)); !equalStrings(got, []string{"common", "a1-only", "a1-and-buc", "asb-only"}) {
		t.Fatalf("ASB-visible skills = %v", got)
	}
	if got := skillNames(filterAgentSkillsForRuntime(skills, asbRuntime, SandboxBackendAliyunFC)); !equalStrings(got, []string{"common"}) {
		t.Fatalf("metadata/backend drift exposed restricted skills: %v", got)
	}
}

func TestSkillVisibleToRuntimeFailsClosedForInvalidCloudRequirements(t *testing.T) {
	t.Parallel()

	cloudRuntime := db.AgentRuntime{
		RuntimeMode: "cloud",
		Provider:    "hermes",
		Metadata:    []byte(`{"kind":"fc-e2b","capabilities":["hermes"]}`),
	}
	localRuntime := db.AgentRuntime{RuntimeMode: "local", Provider: "hermes"}
	invalidConfigs := []json.RawMessage{
		json.RawMessage(`{"execution":null}`),
		json.RawMessage(`{"execution":{"required_runtime_capabilities":null}}`),
		json.RawMessage(`{"execution":{"required_sandbox_backends":null}}`),
		json.RawMessage(`{"execution":{"required_runtime_capabilities":"a1"}}`),
		json.RawMessage(`{"execution":{"required_runtime_capabilities":[""]}}`),
		json.RawMessage(`{"execution":{"required_sandbox_backends":[""]}}`),
		json.RawMessage(`{"execution":{"required_sandbox_backends":["asb","unknown"]}}`),
	}
	for _, config := range invalidConfigs {
		if skillVisibleToRuntime(config, cloudRuntime, SandboxBackendAliyunFC) {
			t.Fatalf("invalid cloud requirement was accepted: %s", config)
		}
		if !skillVisibleToRuntime(config, localRuntime, "") {
			t.Fatalf("cloud capability policy affected a local runtime: %s", config)
		}
	}
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
