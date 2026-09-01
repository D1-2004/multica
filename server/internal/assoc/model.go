package assoc

import (
	"encoding/json"
	"time"
)

const (
	StatusOpen      = "open"
	StatusWaiting   = "waiting"
	StatusDone      = "done"
	StatusCancelled = "cancelled"
	StatusDormant   = "dormant"
	StatusClosed    = "closed"

	RelTaskIssue   = "task_issue"
	RelTaskScene   = "task_scene"
	RelTaskPerson  = "task_person"
	RelOutreach    = "outreach"
	RelWaitingOn   = "waiting_on"
	RelSpawnedFrom = "spawned_from"
	RelEventOf     = "event_of"

	NodePerson = "person"
	NodeScene  = "scene"
	NodeTask   = "task"
	NodeEvent  = "event"
	NodeIssue  = "issue"

	DirInbound  = "inbound"
	DirOutbound = "outbound"

	DefaultLimit    = 20
	MaxLimit        = 50
	MinPurposeRunes = 8
	RecencyTau      = 6 * time.Hour
)

type Task struct {
	ID            string
	WorkspaceID   string
	AgentID       string
	IssueID       string
	Purpose       string
	Status        string
	Intent        string
	Props         map[string]any
	RunID         string
	LastTouchedAt time.Time
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

type Edge struct {
	ID            string
	WorkspaceID   string
	AgentID       string
	SrcType       string
	SrcID         string
	DstType       string
	DstID         string
	Rel           string
	Status        string
	Props         map[string]any
	OpenedAt      time.Time
	LastTouchedAt time.Time
	ClosedAt      *time.Time
	OpenedByRunID string
}

type Event struct {
	ID          string
	WorkspaceID string
	AgentID     string
	Source      string
	Direction   string
	EvidenceID  string
	OccurredAt  time.Time
	SceneKey    string
	PersonKey   string
	TaskID      string
	CreatedAt   time.Time
}

type Query struct {
	WorkspaceID    string
	AgentID        string
	Since          time.Time
	Until          time.Time
	ConversationID string
	PersonID       string
	IssueID        string
	Q              string
	Intent         string
	Limit          int
}

type ConversationRef struct {
	ConversationID string   `json:"conversation_id"`
	Kind           string   `json:"kind,omitempty"`
	Rel            string   `json:"rel"`
	Rels           []string `json:"rels,omitempty"`
}

type PersonRef struct {
	PersonID    string `json:"person_id"`
	DisplayName string `json:"display_name,omitempty"`
}

type WaitingRef struct {
	ConversationID string `json:"conversation_id,omitempty"`
	PersonID       string `json:"person_id,omitempty"`
}

type OriginRef struct {
	ConversationID string `json:"conversation_id,omitempty"`
	Rel            string `json:"rel"`
}

type Item struct {
	Issue         string            `json:"issue"`
	TaskID        string            `json:"task_id"`
	Purpose       string            `json:"purpose"`
	Intent        string            `json:"intent"`
	Status        string            `json:"status"`
	LastTouchedAt time.Time         `json:"last_touched_at"`
	Score         float64           `json:"score"`
	Conversations []ConversationRef `json:"conversations"`
	People        []PersonRef       `json:"people"`
	WaitingOn     []WaitingRef      `json:"waiting_on"`
	Origin        *OriginRef        `json:"origin,omitempty"`
}

type EventRef struct {
	ID         string    `json:"id"`
	Direction  string    `json:"direction"`
	Source     string    `json:"source"`
	EvidenceID string    `json:"evidence_id"`
	TaskID     string    `json:"task_id,omitempty"`
	PersonID   string    `json:"person_id,omitempty"`
	OccurredAt time.Time `json:"occurred_at"`
}

type Result struct {
	Since  time.Time  `json:"since"`
	Until  time.Time  `json:"until"`
	Items  []Item     `json:"items"`
	Events []EventRef `json:"events"`
}

func cloneProps(in map[string]any) map[string]any {
	if in == nil {
		return map[string]any{}
	}
	raw, err := json.Marshal(in)
	if err != nil {
		return map[string]any{}
	}
	out := map[string]any{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return map[string]any{}
	}
	return out
}

func mergeProps(dst, src map[string]any) map[string]any {
	out := cloneProps(dst)
	for k, v := range src {
		out[k] = v
	}
	return out
}
