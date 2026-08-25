package service

import (
	"bytes"
	"encoding/json"
	"strings"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type skillRuntimeRequirements struct {
	Capabilities []string
	Backends     []string
}

// filterAgentSkillsForRuntime applies workspace skill capability requirements
// at the task's exact Runtime boundary. Cloud tasks use the persisted startup
// backend plus the Runtime capability contract; local runtimes instead own
// their machine-local tool and skill availability.
func filterAgentSkillsForRuntime(skills []AgentSkillData, runtime db.AgentRuntime, taskBackend SandboxBackendKind) []AgentSkillData {
	if len(skills) == 0 {
		return skills
	}
	filtered := make([]AgentSkillData, 0, len(skills))
	for _, skill := range skills {
		if skillVisibleToRuntime(skill.Config, runtime, taskBackend) {
			filtered = append(filtered, skill)
		}
	}
	return filtered
}

func skillVisibleToRuntime(rawConfig json.RawMessage, runtime db.AgentRuntime, taskBackend SandboxBackendKind) bool {
	if runtime.RuntimeMode != "cloud" {
		return true
	}

	requirements, declared, valid := parseSkillRuntimeRequirements(rawConfig)
	if !valid {
		return false
	}
	if !declared {
		return true
	}
	metadata, err := ParseCloudSandboxRuntime(runtime)
	if err != nil ||
		(taskBackend != SandboxBackendAliyunFC && taskBackend != SandboxBackendASB) ||
		metadata.SandboxBackend != taskBackend {
		return false
	}
	if len(requirements.Backends) > 0 {
		matched := false
		for _, backend := range requirements.Backends {
			backend = strings.ToLower(strings.TrimSpace(backend))
			if backend != string(SandboxBackendAliyunFC) && backend != string(SandboxBackendASB) {
				return false
			}
			if backend == string(metadata.SandboxBackend) {
				matched = true
			}
		}
		if !matched {
			return false
		}
	}
	for _, capability := range requirements.Capabilities {
		if strings.TrimSpace(capability) == "" || !CloudSandboxRuntimeHasCapability(runtime, capability) {
			return false
		}
	}
	return true
}

func parseSkillRuntimeRequirements(rawConfig json.RawMessage) (skillRuntimeRequirements, bool, bool) {
	if len(bytes.TrimSpace(rawConfig)) == 0 {
		return skillRuntimeRequirements{}, false, true
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(rawConfig, &root); err != nil || root == nil {
		return skillRuntimeRequirements{}, false, false
	}
	executionRaw, exists := root["execution"]
	if !exists {
		return skillRuntimeRequirements{}, false, true
	}
	if bytes.Equal(bytes.TrimSpace(executionRaw), []byte("null")) {
		return skillRuntimeRequirements{}, true, false
	}
	var execution map[string]json.RawMessage
	if err := json.Unmarshal(executionRaw, &execution); err != nil || execution == nil {
		return skillRuntimeRequirements{}, true, false
	}

	requirements := skillRuntimeRequirements{}
	declared := false
	if capabilitiesRaw, ok := execution["required_runtime_capabilities"]; ok {
		declared = true
		if bytes.Equal(bytes.TrimSpace(capabilitiesRaw), []byte("null")) ||
			json.Unmarshal(capabilitiesRaw, &requirements.Capabilities) != nil {
			return skillRuntimeRequirements{}, true, false
		}
	}
	if backendsRaw, ok := execution["required_sandbox_backends"]; ok {
		declared = true
		if bytes.Equal(bytes.TrimSpace(backendsRaw), []byte("null")) ||
			json.Unmarshal(backendsRaw, &requirements.Backends) != nil {
			return skillRuntimeRequirements{}, true, false
		}
	}
	return requirements, declared && (len(requirements.Capabilities) > 0 || len(requirements.Backends) > 0), true
}
