package handler

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/google/uuid"
)

// Pi composes names as mcp__<server>__<tool> and rejects long names before
// starting the Agent. The compact server ID leaves room for readable tools;
// long upstream names use stable aliases that resolve back at the relay.
const connectorPresentedToolMaxBytes = 38

func connectorServerName(id string) string {
	parsed, err := uuid.Parse(id)
	if err != nil {
		return ""
	}
	return "c" + hex.EncodeToString(parsed[:8])
}

func connectorPresentedToolName(name string) string {
	if len(name) <= connectorPresentedToolMaxBytes {
		return name
	}
	sum := sha256.Sum256([]byte(name))
	return "t_" + hex.EncodeToString(sum[:8])
}

func connectorOriginalAllowedTool(allowed []string, presented string) (string, bool) {
	for _, name := range allowed {
		if connectorPresentedToolName(name) == presented {
			return name, true
		}
	}
	return "", false
}
