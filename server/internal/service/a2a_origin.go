package service

import (
	"encoding/json"

	a2aintegration "github.com/multica-ai/multica/server/internal/integrations/a2a"
)

const (
	a2aTaskOriginContextKey               = "multica_origin"
	a2aTaskOriginValue                    = "a2a"
	a2aTaskDEAPDWSTokenRequiredContextKey = "deap_dws_token_required"
	a2aTaskContextJSON                    = `{"multica_origin":"a2a"}`
	a2aTaskDEAPDWSContextJSON             = `{"deap_dws_token_required":true,"multica_origin":"a2a"}`
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
// Runtime-specific identity policy is applied separately: FC accepts only the
// task-scoped external identity, while ASB may attach the Multica Agent's
// enterprise identity.
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
