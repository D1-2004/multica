// Package a2ui is the server's parameterized DingTalk card loop.
// Callers name a kind and the fields a person should see. The package
// projects the A2UI document, sends it through pkg/dws, and records the
// click that comes back on the native subscription under the same public id.
package a2ui

import (
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/multica-ai/multica/server/pkg/dws"
)

const (
	// Version is written into the card and required when a click names one.
	Version = "a2ui-loop/v1"
	// NativeEventKey is the personal event the native subscription adds for
	// this loop. IM events stay on their own consumers.
	NativeEventKey = dws.EventCardAction

	maxHeader      = 120
	maxQuestion    = 2000
	maxOptions     = 20
	maxLabel       = 200
	maxDescription = 1000
	maxMarkdown    = 8000
	maxCustom      = 8000
	maxPoints      = 50
	maxID          = 256
)

var (
	ErrNotFound  = errors.New("a2ui interaction not found")
	ErrConflict  = errors.New("a2ui interaction already exists")
	ErrInvalid   = errors.New("invalid a2ui interaction")
	errForeign   = errors.New("foreign a2ui card event")
	errMalformed = errors.New("malformed a2ui card event")
)

// Kind is the card a caller asks for. The public id family is derived from it:
// confirm, choose and person are ask; chart and note are show; approval is appr.
type Kind string

const (
	KindConfirm  Kind = "confirm"
	KindChoose   Kind = "choose"
	KindPerson   Kind = "person"
	KindChart    Kind = "chart"
	KindNote     Kind = "note"
	KindApproval Kind = "approval"
)

// Family is the public-id prefix for kind.
func (k Kind) Family() string {
	switch k {
	case KindChart, KindNote:
		return "show"
	case KindApproval:
		return "appr"
	case KindConfirm, KindChoose, KindPerson:
		return "ask"
	default:
		return ""
	}
}

// Waits reports whether a click is required before the interaction is done.
func (k Kind) Waits() bool {
	return k == KindConfirm || k == KindChoose || k == KindPerson || k == KindApproval
}

// Status is the row's lifecycle. Approval decisions use approved and rejected.
type Status string

const (
	StatusOpen      Status = "open"
	StatusDelivered Status = "delivered"
	StatusAnswered  Status = "answered"
	StatusSkipped   Status = "skipped"
	StatusFailed    Status = "failed"
	StatusApproved  Status = "approved"
	StatusRejected  Status = "rejected"
)

// Ref is the public handle shared by the caller, the card and the click.
type Ref struct {
	Family string
	ID     uuid.UUID
}

// PublicID is family plus the row id, for example appr:<uuid>.
func (r Ref) PublicID() string { return r.Family + ":" + r.ID.String() }

// SurfaceID is the A2UI surface. It keeps component ids free of the colon.
func (r Ref) SurfaceID() string { return "s-" + r.ID.String() }

// ParseRef accepts ask:<uuid>, show:<uuid> and appr:<uuid>.
func ParseRef(raw string) (Ref, bool) {
	family, id, ok := strings.Cut(strings.TrimSpace(raw), ":")
	if !ok {
		return Ref{}, false
	}
	switch family {
	case "ask", "show", "appr":
	default:
		return Ref{}, false
	}
	parsed, err := uuid.Parse(id)
	if err != nil || parsed == uuid.Nil {
		return Ref{}, false
	}
	return Ref{Family: family, ID: parsed}, true
}

// Option is one choice. The package assigns o0, o1, … when it opens the card.
type Option struct {
	// Emphasis chooses a native button variant: primary, secondary or none.
	Emphasis    string
	Label       string
	Description string
}

// ChartPoint is one x/y pair. Y is the numeric value.
type ChartPoint struct {
	X string  `json:"x"`
	Y float64 `json:"y"`
}

// Chart is a line, bar or pie. Empty Type means line.
type Chart struct {
	Type   string
	Points []ChartPoint
}

// OpenRequest is one card to send. Exactly one of ConversationID and
// ReceiverOpenDingTalkID addresses it. OperatorUID, when set, drops clicks
// from anyone else. IdempotencyKey returns the existing row for this agent.
type OpenRequest struct {
	WorkspaceID            uuid.UUID
	AgentID                uuid.UUID
	SenderUID              string
	SenderOrgID            string
	SceneID                string
	ConversationID         string
	ThreadID               string
	SourceRef              string
	ReceiverOpenDingTalkID string
	Kind                   Kind
	Header                 string
	Question               string
	Options                []Option
	AllowCustom            *bool
	Chart                  *Chart
	Markdown               string
	IdempotencyKey         string
	OperatorUID            string
	// EmployeeCompact uses one-click frozen choices for Employee questions.
	// Generic person pickers and approvals keep their existing projection.
	EmployeeCompact bool
	SourceQuote     string
}

// Result is what the person did. Disabled is a host-only display outcome; native
// answers remain answered, skipped, approved or rejected.
type Result struct {
	Outcome  string   `json:"outcome"`
	Selected []string `json:"selected"`
	Labels   []string `json:"labels"`
	Custom   string   `json:"custom,omitempty"`
}

// Interaction is one stored card. PublicID is the handle to pass around,
// including the pending-approval id (appr:<uuid>).
type Interaction struct {
	ID             uuid.UUID
	PublicID       string
	WorkspaceID    uuid.UUID
	AgentID        uuid.UUID
	SenderUID      string
	SenderOrgID    string
	SceneID        string
	ConversationID string
	// MessageID is the card message's openMessageId, filled from the send
	// receipt the same way a dws text send fills Sent.MessageID. ThreadID is
	// the thread the card was sent into, when it has one. SourceRef is the
	// employee-loop handle receipt_id/openMsgId. A click resolves by PublicID;
	// MessageID plus Result is which message received which reply.
	MessageID      string
	ThreadID       string
	SourceRef      string
	Kind           Kind
	Status         Status
	Header         string
	Question       string
	CardBizID      string
	EventID        string
	OperatorUID    string
	IdempotencyKey string
	Result         Result
	CreatedAt      time.Time
	ResolvedAt     time.Time
	spec           storedRequest
}

// Actor is the native subscription identity the click arrived on: the card sender.
type Actor struct {
	AgentID uuid.UUID
	UID     string
	OrgID   string
}

type storedOption struct {
	Emphasis    string `json:"emphasis,omitempty"`
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

type storedChart struct {
	Type   string       `json:"type"`
	Points []ChartPoint `json:"points"`
}

type storedRequest struct {
	Options                []storedOption `json:"options,omitempty"`
	AllowCustom            bool           `json:"allow_custom,omitempty"`
	Multiple               bool           `json:"multiple,omitempty"`
	Chart                  *storedChart   `json:"chart,omitempty"`
	Markdown               string         `json:"markdown,omitempty"`
	OperatorUID            string         `json:"operator_uid,omitempty"`
	ReceiverOpenDingTalkID string         `json:"receiver_open_dingtalk_id,omitempty"`
	SurfaceID              string         `json:"surface_id,omitempty"`
	EmployeeCompact        bool           `json:"employee_compact,omitempty"`
	SourceQuote            string         `json:"source_quote,omitempty"`
}

func (row Interaction) clone() Interaction {
	row.spec.Options = append([]storedOption(nil), row.spec.Options...)
	if row.spec.Chart != nil {
		chart := *row.spec.Chart
		chart.Points = append([]ChartPoint(nil), chart.Points...)
		row.spec.Chart = &chart
	}
	row.Result.Selected = append([]string(nil), row.Result.Selected...)
	row.Result.Labels = append([]string(nil), row.Result.Labels...)
	return row
}
