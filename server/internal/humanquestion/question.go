package humanquestion

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/employeeentry"
)

var (
	ErrInvalid   = errors.New("invalid human question")
	ErrConflict  = errors.New("human question input conflicts with its accepted identity")
	ErrNotFound  = errors.New("human question not found")
	ErrStale     = errors.New("human question no longer targets current work")
	ErrForbidden = errors.New("human question response is not allowed")
)

// Question is a Host-owned binding, separate from model-authored display fields.
type Question struct {
	ID              string              `json:"question_ref"`
	Scope           employeeentry.Scope `json:"-"`
	PrincipalID     string              `json:"-"`
	SourceJobID     string              `json:"-"`
	SourceReceiptID string              `json:"-"`
	SourceRef       string              `json:"-"`
	RequesterRef    string              `json:"-"`
	OperatorOpenID  string              `json:"-"`
	TaskID          string              `json:"task_id,omitempty"`
	RunID           string              `json:"run_id,omitempty"`
	GoalRevision    int64               `json:"goal_revision,omitempty"`
	Version         int                 `json:"version"`
	Summary         string              `json:"summary"`
	Choice          Choice              `json:"choice"`
	PublicID        string              `json:"-"`
	ActionID        string              `json:"-"`
	State           string              `json:"state"`
	ResponseID      string              `json:"-"`
	CreatedAt       time.Time           `json:"-"`
}

// Response preserves actual input. A choice ID is never synthesized human text.
type Response struct {
	ID            string   `json:"response_ref"`
	QuestionID    string   `json:"question_ref"`
	EventID       string   `json:"event_id"`
	Surface       string   `json:"input_surface"`
	RequesterRef  string   `json:"requester_ref"`
	Intent        string   `json:"intent"`
	Selected      []string `json:"selected,omitempty"`
	RawText       string   `json:"raw_text,omitempty"`
	EvidenceQuote string   `json:"evidence_quote,omitempty"`
	ReceiptID     string   `json:"-"`
	JobID         string   `json:"-"`
}

func idValid(v string) bool { u, e := uuid.Parse(v); return e == nil && u != uuid.Nil }
func (q Question) Validate() error {
	if !idValid(q.ID) || !idValid(q.Scope.WorkspaceID) || !idValid(q.Scope.AgentID) || q.Scope.TenantOrgID == "" || !idValid(q.Scope.SceneID) || !idValid(q.PrincipalID) || !idValid(q.SourceJobID) || !idValid(q.SourceReceiptID) || q.SourceRef == "" || q.RequesterRef == "" || q.OperatorOpenID == "" || q.Version != 1 || strings.TrimSpace(q.Summary) == "" || len(q.Summary) > 32000 || q.Choice.Validate() != nil {
		return ErrInvalid
	}
	if (q.TaskID == "") != (q.RunID == "") || (q.TaskID == "" && q.GoalRevision != 0) || (q.TaskID != "" && (!idValid(q.TaskID) || !idValid(q.RunID) || q.GoalRevision < 1)) {
		return ErrInvalid
	}
	return nil
}

// ValidateResponse uses exact frozen choices for a click, while typed words may
// supply new information or change the request. Original wording stays intact.
func (q Question) ValidateResponse(r Response) error {
	if !idValid(r.ID) || r.QuestionID != q.ID || r.RequesterRef != q.RequesterRef || r.EventID == "" || len(r.EventID) > 512 || len(r.RawText) > 32000 {
		return ErrForbidden
	}
	if r.Surface != "a2ui_action" && r.Surface != "chat_text" {
		return ErrInvalid
	}
	switch r.Intent {
	case "answer", "provide_info", "amend", "cancel", "skip":
	default:
		return ErrInvalid
	}
	seen := map[string]bool{}
	for _, id := range r.Selected {
		if seen[id] {
			return ErrInvalid
		}
		seen[id] = true
		found := false
		for _, o := range q.Choice.Options {
			if id == o.ID {
				found = true
				break
			}
		}
		if !found {
			return ErrInvalid
		}
	}
	c := q.Choice.Normalized()
	if len(r.Selected) > c.Max || (len(r.Selected) > 0 && len(r.Selected) < c.Min) {
		return ErrInvalid
	}
	if r.Surface == "a2ui_action" {
		if r.Intent != "answer" && r.Intent != "skip" {
			return ErrInvalid
		}
		if r.Intent == "answer" && len(r.Selected) == 0 && (!c.AllowCustom || strings.TrimSpace(r.RawText) == "") {
			return ErrInvalid
		}
	} else if strings.TrimSpace(r.RawText) == "" || r.EvidenceQuote == "" || !strings.Contains(r.RawText, r.EvidenceQuote) {
		return ErrInvalid
	}
	return nil
}

func responseBody(r Response) ([]byte, error) { return json.Marshal(r) }
