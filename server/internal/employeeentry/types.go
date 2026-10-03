// Package employeeentry owns the durable business consumer of admitted scene events.
package employeeentry

import (
	"encoding/json"
	"errors"
	"time"
)

const (
	Coordinator       = "coordinator"
	Employee          = "employee"
	MaxWindowItems    = 16
	MaxWindowMessages = 32

	// KindMessage is a human message window; KindTaskWake is one internal,
	// Host-derived Task wake. The kind is fixed when the job is created.
	KindMessage  = "message"
	KindTaskWake = "task_wake"
)

var (
	ErrInvalid     = errors.New("invalid employee scene entry")
	ErrNotFound    = errors.New("employee scene entry not found")
	ErrConflict    = errors.New("employee scene entry conflicts with accepted input")
	ErrNoJob       = errors.New("no employee scene job ready")
	ErrLease       = errors.New("employee scene lease is no longer current")
	ErrModelBudget = errors.New("employee scene model request budget exhausted")
)

type Scope struct {
	WorkspaceID string `json:"workspace_id"`
	AgentID     string `json:"agent_id"`
	TenantOrgID string `json:"tenant_org_id"`
	SceneID     string `json:"scene_id,omitempty"`
}

// Item carries a Host-assembled command and its authenticated execution principal.
// Payload must never be decoded directly from the HTTP request into this type.
type Item struct {
	ReceiptID    string          `json:"receipt_id"`
	PrincipalID  string          `json:"principal_id"`
	Payload      json.RawMessage `json:"payload"`
	MessageCount int             `json:"message_count"`
}

type Admission struct {
	Scope          Scope
	Item           Item
	Owner          string
	ConfigRevision string
	HoldReason     string
}

type Consumption struct {
	ReceiptID string
	Owner     string
	JobID     string
	State     string
	Reason    string
}

type ModelTurn struct {
	Request  json.RawMessage      `json:"request"`
	Response json.RawMessage      `json:"response,omitempty"`
	Failure  string               `json:"failure,omitempty"`
	Route    *ModelRouteSelection `json:"route,omitempty"`
}

// ModelRouteSelection freezes provider identity independently of an upstream
// model ID, which can exist at multiple providers. NextCandidate is journaled
// with a failure so restart never guesses retry policy from an error string.
type ModelRouteSelection struct {
	Revision      int64  `json:"revision"`
	Ref           string `json:"ref"`
	Candidate     int    `json:"candidate"`
	NextCandidate int    `json:"next_candidate"`
}

type Job struct {
	ID            string
	Kind          string
	Scope         Scope
	PrincipalID   string
	Items         []Item
	State         string
	LeaseToken    string
	LeaseUntil    *time.Time
	Generation    int64
	Attempts      int
	ModelAttempts int
	InputSnapshot json.RawMessage
	ModelJournal  []ModelTurn
	Outcome       json.RawMessage
	LastError     string
	CreatedAt     time.Time
}

// ModelFailure replays a recorded provider failure without another request.
type ModelFailure struct {
	Message string
	Route   *ModelRouteSelection
}

func (e *ModelFailure) Error() string { return e.Message }
