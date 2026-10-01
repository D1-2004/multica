package service

import (
	"encoding/json"
	"slices"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// RuntimeSupportsDWSMessagePolicy reports whether a runtime runs the managed
// DWS wrapper, which a managed DingTalk response policy relies on: a cloud
// sandbox declares it among its capabilities, a daemon runtime among its
// reported client capabilities.
func RuntimeSupportsDWSMessagePolicy(runtime db.AgentRuntime) bool {
	if IsCloudSandboxRuntime(runtime) {
		return CloudSandboxRuntimeHasCapability(runtime, protocol.DWSMessagePolicyCapability)
	}
	var metadata struct {
		ClientCapabilities []string `json:"client_capabilities"`
	}
	if json.Unmarshal(runtime.Metadata, &metadata) != nil {
		return false
	}
	return slices.Contains(metadata.ClientCapabilities, protocol.DWSMessagePolicyCapability)
}
