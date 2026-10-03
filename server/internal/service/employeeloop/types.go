// Copyright (c) 2026 Nex.
// Copied and modified from gawkbot at 71e82a1809565281cbd0bf8185d3c125b715d934.
// See LICENSE and SOURCE_MAP.md for provenance and modification details.
package employeeloop

import (
	"context"
	"errors"

	"github.com/multica-ai/multica/server/internal/scene"
	openai "github.com/openai/openai-go/v3"
)

// Phase represents the lifecycle phase of a foreground wake.
type Phase string

const (
	PhaseIdle         Phase = "idle"
	PhaseBuildContext Phase = "build_context"
	PhaseStreamLLM    Phase = "stream_llm"
	PhaseExecuteTool  Phase = "execute_tool"
	PhaseDone         Phase = "done"
	PhaseError        Phase = "error"
	MaxModelCalls           = 3
)

var (
	ErrModelBudget      = errors.New("employee loop: three-call model budget exhausted")
	ErrBusy             = errors.New("employee loop: wake already running")
	ErrMissingReceipt   = errors.New("employee loop: effect has no host receipt")
	ErrTerminalConflict = errors.New("employee loop: conflicting terminal tool results")
)

// Model performs exactly one provider request per invocation. When using
// pkg/llm.Client, construct it with Config.MaxRetries=-1 to disable SDK retries.
// GenerateJSON helpers must not be used: their implicit retries defeat the budget.
type Model interface {
	Chat(context.Context, openai.ChatCompletionNewParams) (*openai.ChatCompletion, error)
}

// Identity is supplied by the admission Host, never decoded from model or user data.
// Host.Execute must enforce tenant fencing and permissions using this identity.
type Identity struct {
	WorkspaceID string
	AgentID     string
	TenantOrgID string
	Scene       scene.Ref
	ReceiptID   string
}

// Input contains a single foreground wake and existing context snapshots.
// All text fields are untrusted data and are sent as user messages, not authority.
type Input struct {
	Identity           Identity
	CurrentWindow      string
	Memory             string
	TaskBrief          string
	FollowUps          []string
	RecentConversation string `json:"RecentConversation,omitempty"`
}

// Persona contains trusted employee configuration, separate from conversation data.
type Persona struct {
	Name, Personality, Tone string
	// Instructions contains trusted configured duties and business constraints.
	// It does not grant Host permissions or capabilities.
	Instructions string
	Expertise    []string
}
type Config struct {
	// OnBatchRejected observes validation failures before Host effects.
	OnBatchRejected func([]ToolCall, error) `json:"-"`
	Persona         Persona
	Model           string
	Tools           []Tool
}

// State holds the runtime state of a foreground wake.
type State struct {
	Phase      Phase
	ModelCalls int
	Error      string
}

// Tool is a named capability supplied by the Host. Effect requires a durable receipt.
type Tool struct {
	Name        string
	Description string
	Schema      map[string]any
	Effect      bool
	// Terminal declares a known disposition so incompatible batches can be
	// rejected before any Host effect. Empty means the result is not fixed.
	Terminal Disposition
}

// ToolCall preserves the provider's correlation ID for the entire native batch.
type ToolCall struct {
	NativeToolCallID string         `json:"native_tool_call_id"`
	Name             string         `json:"name"`
	Arguments        map[string]any `json:"arguments"`
}

// Host owns all reads, permissions, durable state and irreversible effects.
// Execute must honor ctx and deduplicate effects using identity plus the call ID.
// A successful effect must return its durable receipt, even if ctx was canceled
// immediately after the commit. The loop never manufactures an execution receipt.
type Host interface {
	Execute(context.Context, Identity, ToolCall) (ToolResult, error)
}

type Disposition string

const (
	Reply      Disposition = "reply"
	Quiet      Disposition = "quiet"
	Dispatched Disposition = "dispatched"
	Waiting    Disposition = "waiting"
)

// Decision is a terminal disposition accepted by the Host or a normal text reply.
// Reply may accompany Dispatched/Waiting, avoiding another model composition pass.
type Decision struct {
	Kind  Disposition
	Reply string
}
type ToolResult struct {
	Content  string
	Receipt  string
	Terminal *Decision
}
type Receipt struct {
	NativeToolCallID string
	ToolName         string
	ID               string
}

// ToolOutcome preserves each Host result in native-call order, including committed
// effects returned alongside errors or cancellation, and rejected effect parameters.
// It is not a claim that the
// whole batch completed; callers must also check Run's error and final Decision.
type ToolOutcome struct {
	NativeToolCallID string
	ToolName         string
	Result           ToolResult
	Error            string
}

type Outcome struct {
	Decision
	ModelCalls   int
	Receipts     []Receipt
	Entries      []SessionEntry
	ToolOutcomes []ToolOutcome
}

// SessionEntry is one entry in the per-wake session history. Native assistant
// tool batches and tool IDs replace the upstream text-only tool markers.
type SessionEntry struct {
	Type             string
	Content          string
	NativeToolCallID string
	Message          *openai.ChatCompletionMessage
}
