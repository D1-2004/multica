package service

import (
	"encoding/json"
	"strings"

	a2aintegration "github.com/multica-ai/multica/server/internal/integrations/a2a"
)

const (
	a2aTaskOriginContextKey               = "multica_origin"
	a2aTaskOriginValue                    = "a2a"
	a2aTaskDEAPDWSTokenRequiredContextKey = "deap_dws_token_required"
	// a2aTaskOperatorDWSIdentityContextKey marks an A2A turn admitted while the
	// Agent had an operator-bound DEAP employee identity and the caller sent
	// neither X-DWS-Token nor an external ContextToken. It is non-secret: the
	// launcher re-reads the binding, so clearing it revokes future launches.
	a2aTaskOperatorDWSIdentityContextKey = "a2a_operator_dws_identity"
	a2aTaskContextJSON                   = `{"multica_origin":"a2a"}`
	a2aTaskDEAPDWSContextJSON            = `{"deap_dws_token_required":true,"multica_origin":"a2a"}`
)

// newA2ATaskContext marks an execution as A2A-originated on the durable local
// task row. The marker is intentionally protocol-neutral and contains no
// external or local identifiers. Retry tasks inherit task context, so the
// privacy boundary follows the complete local lineage.
func newA2ATaskContext(identity ...a2aintegration.InvocationIdentity) []byte {
	if len(identity) == 0 {
		return []byte(a2aTaskContextJSON)
	}
	if identity[0].DEAPDWSToken != "" {
		// Only the non-secret execution requirement is durable. The opaque DEAP
		// credential remains exclusively in the live request context.
		return []byte(a2aTaskDEAPDWSContextJSON)
	}
	if identity[0].ContextToken == "" {
		return []byte(a2aTaskContextJSON)
	}
	context := map[string]any{
		a2aTaskOriginContextKey:                   a2aTaskOriginValue,
		"agent_identity_context_token":            identity[0].ContextToken,
		"agent_identity_context_token_expires_at": identity[0].ExpiresAtUnixMS,
		"agent_identity_context_token_source":     "external",
	}
	encoded, err := json.Marshal(context)
	if err != nil {
		panic(err)
	}
	return encoded
}

// newA2ATaskContextWithBinding is newA2ATaskContext plus the operator-identity
// marker. A caller-supplied identity (DEAP X-DWS-Token or an external
// ContextToken) always wins over the operator binding.
func newA2ATaskContextWithBinding(identity a2aintegration.InvocationIdentity, bound a2aDingTalkBoundIdentity) []byte {
	taskContext := newA2ATaskContext(identity)
	if bound.UID != "" && bound.OrgID != "" && identity.DEAPDWSToken == "" && identity.ContextToken == "" {
		return withA2AOperatorDWSIdentity(taskContext)
	}
	return taskContext
}

// withA2AOperatorDWSIdentity adds the operator-identity marker to an A2A task
// context produced by newA2ATaskContext.
func withA2AOperatorDWSIdentity(taskContext []byte) []byte {
	var envelope map[string]any
	if err := json.Unmarshal(taskContext, &envelope); err != nil || envelope == nil {
		return taskContext
	}
	envelope[a2aTaskOperatorDWSIdentityContextKey] = true
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return taskContext
	}
	return encoded
}

// UsesA2AOperatorDWSIdentity reports whether the A2A turn should run as the
// operator-bound DEAP employee identity.
func UsesA2AOperatorDWSIdentity(taskContext []byte) bool {
	if len(taskContext) == 0 || !IsA2ATaskOrigin(taskContext) {
		return false
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(taskContext, &envelope); err != nil {
		return false
	}
	raw, ok := envelope[a2aTaskOperatorDWSIdentityContextKey]
	if !ok {
		return false
	}
	var marked bool
	return json.Unmarshal(raw, &marked) == nil && marked
}

func requiresA2ADEAPDWSToken(taskContext []byte) bool {
	if len(taskContext) == 0 {
		return false
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(taskContext, &envelope); err != nil {
		return false
	}
	raw, ok := envelope[a2aTaskDEAPDWSTokenRequiredContextKey]
	if !ok {
		return false
	}
	var required bool
	return json.Unmarshal(raw, &required) == nil && required
}

func hasA2ATaskOrigin(taskContext []byte) bool {
	if len(taskContext) == 0 {
		return false
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(taskContext, &envelope); err != nil {
		return false
	}
	encoded, ok := envelope[a2aTaskOriginContextKey]
	if !ok {
		return false
	}
	var origin string
	return json.Unmarshal(encoded, &origin) == nil && origin == a2aTaskOriginValue
}

// IsA2ATaskOrigin is the durable cross-runtime marker for A2A-specific
// execution and credential isolation. Callers must use the task context
// supplied by CreateA2AChatTask, never request headers or in-memory state.
// Ordinary A2A tasks accept only their task-scoped external identity. A task
// carrying the durable DEAP DWS requirement is the explicit hybrid ASB case:
// DWS stays bound to the request-scoped external token, while the Agent's
// enterprise identity may be mounted solely for non-DWS corporate access.
func IsA2ATaskOrigin(taskContext []byte) bool {
	return hasA2ATaskOrigin(taskContext)
}

// ShouldInjectRuntimeOwnerProfile keeps the runtime owner's personal profile
// out of executions requested by an external A2A principal. Agent-owned base
// configuration remains available; only the human identity/"on behalf of"
// brief is suppressed.
func ShouldInjectRuntimeOwnerProfile(taskContext []byte) bool {
	return !IsA2ATaskOrigin(taskContext)
}

// ShouldInjectA2ARunnerMCP is the DEAP hybrid: the Agent's bound local
// Runner machines stay available, matching the robot path. Ordinary external
// A2A stays isolated from the owner's desktop.
func ShouldInjectA2ARunnerMCP(taskContext []byte) bool {
	return requiresA2ADEAPDWSToken(taskContext)
}

// deapA2AOpenConversationID reads the DingTalk conversation id DEAP puts in
// A2A request metadata. DEAP leaves A2A contextId empty and places the
// conversation under metadata.context.attributes.sessionInfo.openConversationId.
func deapA2AOpenConversationID(metadatas ...map[string]any) string {
	for _, metadata := range metadatas {
		if id := openConversationIDFromMetadata(metadata); id != "" {
			return id
		}
	}
	return ""
}

func openConversationIDFromMetadata(metadata map[string]any) string {
	if id := metadataStringAt(metadata, "context", "attributes", "sessionInfo", "openConversationId"); id != "" {
		return id
	}
	if id := metadataStringAt(metadata, "attributes", "sessionInfo", "openConversationId"); id != "" {
		return id
	}
	if id := metadataStringAt(metadata, "sessionInfo", "openConversationId"); id != "" {
		return id
	}
	return metadataStringAt(metadata, "openConversationId")
}

func metadataStringAt(root map[string]any, keys ...string) string {
	var current any = root
	for _, key := range keys {
		object, ok := current.(map[string]any)
		if !ok {
			return ""
		}
		current = object[key]
	}
	value, _ := current.(string)
	return strings.TrimSpace(value)
}
