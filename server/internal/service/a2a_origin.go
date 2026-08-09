package service

import "encoding/json"

const (
	a2aTaskOriginContextKey = "multica_origin"
	a2aTaskOriginValue      = "a2a"
	a2aTaskContextJSON      = `{"multica_origin":"a2a"}`
)

// newA2ATaskContext marks an execution as A2A-originated on the durable local
// task row. The marker is intentionally protocol-neutral and contains no
// external or local identifiers. Retry tasks inherit task context, so the
// privacy boundary follows the complete local lineage.
func newA2ATaskContext() []byte {
	return []byte(a2aTaskContextJSON)
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

// IsA2ATaskOrigin is the cross-runtime security gate for execution identity
// and personal integration injection. Callers must use the durable task
// context supplied by CreateA2AChatTask, never request headers or in-memory
// state.
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
