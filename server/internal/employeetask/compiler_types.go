package employeetask

// PacketMaterial is a Host-selected, authorized fact or source excerpt. The Host
// checks access before supplying it; text never establishes scope or principal.
type PacketMaterial struct {
	Ref         string `json:"ref"`
	Scope       Scope  `json:"scope"`
	PrincipalID string `json:"principal_id"`
	Body        string `json:"body"`
}

type HistoryState string

const (
	HistoryUnavailable HistoryState = "unavailable"
	HistoryEmpty       HistoryState = "empty"
	HistoryAvailable   HistoryState = "available"
	HistoryTruncated   HistoryState = "truncated"
)

type PacketHistory struct {
	State HistoryState     `json:"state"`
	Items []PacketMaterial `json:"items,omitempty"`
}

// CompileInput contains data already selected and fenced by the Host. Prompt is
// task text, not a source of permissions. Capabilities and ReturnAddress are Host
// facts, never values copied from model tool arguments or user claims.
type CompileInput struct {
	Scope          Scope
	PrincipalID    string
	Definition     Definition
	Prompt         string
	Source         PacketMaterial
	Corrections    []PacketMaterial
	CompletedSteps []PacketMaterial
	References     []PacketMaterial
	History        PacketHistory
	Capabilities   []string
	ReturnAddress  string
}

// WorkPacket is deterministic compiler output. ContextUsed lists only refs that
// were actually rendered, in first-use order; it is not a model's self-report.
type WorkPacket struct {
	Scope       Scope      `json:"scope"`
	PrincipalID string     `json:"principal_id"`
	Definition  Definition `json:"definition"`
	Text        string     `json:"text"`
	ContextUsed []string   `json:"context_used,omitempty"`
}
