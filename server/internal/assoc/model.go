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

	DefaultLimit      = 20
	MaxLimit          = 50
	MinPurposeRunes   = 8
	EventBodyMaxRunes = 160
	EventCardLimit    = 8
	RecencyTau        = 48 * time.Hour

	RecallReadThis = "候选。先判断本条是否需要执行：同交付物且有实质推进才续接；催促、收尾只沟通；不同交付物作为新计划项。"
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
	Body        string
	OccurredAt  time.Time
	// SceneID is the Agent work scene (agent_scene.id) the event happened
	// in (docs/agent-scene.md); "" for an event without a resolved scene.
	SceneID   string
	PersonKey string
	TaskID    string
	CreatedAt time.Time
}

type Query struct {
	WorkspaceID string
	AgentID     string
	Since       time.Time
	Until       time.Time
	// SceneID scopes the recall to one Agent work scene. The Host resolves
	// it from the caller's conversation id; ConversationID only echoes that
	// id back in the result.
	SceneID        string
	ConversationID string
	PersonID       string
	IssueID        string
	Q              string
	Intent         string
	Limit          int
}

// SceneNode is a scene node of the graph: the Host-resolved Agent work
// scene and, for display and DingTalk tool arguments, its conversation id
// and kind as the directory records them.
type SceneNode struct {
	SceneID        string
	ConversationID string
	Kind           string
}

func (n SceneNode) valid() bool { return validSceneNodeID(n.SceneID) }

type ConversationRef struct {
	SceneID        string   `json:"scene_id,omitempty"`
	ConversationID string   `json:"conversation_id"`
	Kind           string   `json:"kind,omitempty"`
	Rel            string   `json:"rel"`
	Rels           []string `json:"rels,omitempty"`
}

type PersonRef struct {
	PersonID    string `json:"person_id"`
	DisplayName string `json:"display_name,omitempty"`
	Name        string `json:"name,omitempty"`
}

type WaitingRef struct {
	SceneID        string `json:"scene_id,omitempty"`
	ConversationID string `json:"conversation_id,omitempty"`
	PersonID       string `json:"person_id,omitempty"`
}

type OriginRef struct {
	SceneID        string `json:"scene_id,omitempty"`
	ConversationID string `json:"conversation_id,omitempty"`
	Rel            string `json:"rel"`
}

type Item struct {
	Issue          string            `json:"issue"`
	IssueID        string            `json:"issue_id,omitempty"`
	TaskID         string            `json:"task_id"`
	Purpose        string            `json:"purpose"`
	Intent         string            `json:"intent,omitempty"`
	IntentLabel    string            `json:"intent_label,omitempty"`
	Status         string            `json:"status"`
	OnThisScene    bool              `json:"on_this_scene"`
	WhyListed      string            `json:"why_listed,omitempty"`
	LastTouchedAt  time.Time         `json:"last_touched_at"`
	LastTouchedAge string            `json:"last_touched_age,omitempty"`
	AgeSeconds     int64             `json:"age_seconds"`
	MatchedVia     string            `json:"matched_via,omitempty"`
	LastComment    string            `json:"last_comment,omitempty"`
	LastCommentAge string            `json:"last_comment_age,omitempty"`
	LastCommentAt  *time.Time        `json:"last_comment_at,omitempty"`
	Score          float64           `json:"-"`
	Conversations  []ConversationRef `json:"conversations"`
	People         []PersonRef       `json:"people"`
	WaitingOn      []WaitingRef      `json:"waiting_on"`
	Origin         *OriginRef        `json:"origin,omitempty"`
}

type EventRef struct {
	ID         string    `json:"id"`
	Direction  string    `json:"direction"`
	Source     string    `json:"source"`
	EvidenceID string    `json:"evidence_id"`
	Text       string    `json:"text,omitempty"`
	TaskID     string    `json:"task_id,omitempty"`
	PersonID   string    `json:"person_id,omitempty"`
	OccurredAt time.Time `json:"occurred_at"`
	When       string    `json:"when,omitempty"`
	Age        string    `json:"age,omitempty"`
	AgeSeconds int64     `json:"age_seconds,omitempty"`
}

type Result struct {
	ReadThis       string     `json:"read_this"`
	Since          time.Time  `json:"since"`
	Until          time.Time  `json:"until"`
	SceneID        string     `json:"scene_id,omitempty"`
	ConversationID string     `json:"conversation_id,omitempty"`
	Q              string     `json:"q,omitempty"`
	Items          []Item     `json:"items"`
	Events         []EventRef `json:"events"`
	EventsNote     string     `json:"events_note,omitempty"`
}

// CloseSceneResult is how many graph links /reset-memory dropped for one scene.
type CloseSceneResult struct {
	ClosedEdges    int
	UnlinkedEvents int
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
