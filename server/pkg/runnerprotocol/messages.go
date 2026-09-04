package runnerprotocol

import "encoding/json"

const (
	MessageCall            = "runner:call"
	MessageResult          = "runner:result"
	MessageHeartbeat       = "runner:heartbeat"
	MessageHello           = "runner:hello"
	MessageBindingsChanged = "runner:bindings_changed"
	MessageCallsCancelled  = "runner:calls_cancelled"
	MessageInventory       = "runner:mcp_inventory"

	ManagedMCPServerName    = "multica_runner"
	ManagedMCPRoutingHeader = "X-Multica-Runner-MCP"
	ManagedMCPRoutingValue  = "v1"
	ManagedMCPPath          = "/api/runner-mcp"
)

type Envelope struct {
	Type string `json:"type"`
}

type Call struct {
	Type      string          `json:"type"`
	CallID    string          `json:"call_id"`
	ToolName  string          `json:"tool_name"`
	Arguments json.RawMessage `json:"arguments"`
	Roots     []string        `json:"roots"`
	ExpiresAt string          `json:"expires_at"`
}

type Result struct {
	Type         string          `json:"type"`
	CallID       string          `json:"call_id"`
	Succeeded    bool            `json:"succeeded"`
	Result       json.RawMessage `json:"result,omitempty"`
	ErrorCode    string          `json:"error_code,omitempty"`
	ErrorMessage string          `json:"error_message,omitempty"`
}

type Heartbeat struct {
	Type          string `json:"type"`
	ClientVersion string `json:"client_version"`
}

type Hello struct {
	Type      string `json:"type"`
	MachineID string `json:"machine_id"`
}

type BindingsChanged struct {
	Type               string `json:"type"`
	ActiveBindingCount int64  `json:"active_binding_count"`
}

type CallsCancelled struct {
	Type    string   `json:"type"`
	CallIDs []string `json:"call_ids"`
}

type MCPServerSummary struct {
	Name         string `json:"name"`
	Transport    string `json:"transport"`
	Availability string `json:"availability"`
	Fingerprint  string `json:"fingerprint"`
}

type MCPInventory struct {
	Type     string             `json:"type"`
	Revision string             `json:"revision"`
	Servers  []MCPServerSummary `json:"servers"`
	Config   []byte             `json:"config,omitempty"`
}
