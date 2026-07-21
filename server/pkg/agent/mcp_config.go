package agent

import (
	"bytes"
	"encoding/json"
)

// hasManagedMcpConfig preserves the API's three-state MCP semantics. Only SQL
// NULL / JSON null mean "inherit the runtime configuration"; any object,
// including an explicitly empty one, is a managed set and enables strict mode.
func hasManagedMcpConfig(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null"))
}

// HasManagedMCPConfig exposes the managed/unmanaged distinction to claim-time
// runtime-capability checks. Keeping this predicate here ensures the API and
// provider backends agree that an explicit empty object is still a managed,
// strict MCP configuration rather than an instruction to inherit ambient
// runtime state.
func HasManagedMCPConfig(raw json.RawMessage) bool {
	return hasManagedMcpConfig(raw)
}
