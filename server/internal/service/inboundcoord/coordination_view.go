package inboundcoord

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/multica-ai/multica/server/internal/assoc"
	"github.com/multica-ai/multica/server/internal/util"
)

const (
	coordinationRecallDefault     = 3
	coordinationRecallMax         = 5
	coordinationRecallBudget      = 6000
	coordinationWorkStateBudget   = 2000
	coordinationGoalBudget        = 160
	coordinationWaitingMax        = 2
	coordinationRecallScope       = "agent_workspace_associations"
	coordinationIssueScope        = "current_agent_workspace_issue"
	coordinationIssueStatusSource = "issue_database"
)

// The graph finds candidate work. Its waiting/open status and old comments do
// not establish the current Issue status, an execution state, or business facts.
type coordinatorRecallItem struct {
	IssueID              string             `json:"issue_id"`
	Purpose              string             `json:"purpose"`
	Intent               string             `json:"intent,omitempty"`
	Status               string             `json:"status"`
	StatusSource         string             `json:"status_source"`
	StateRef             string             `json:"state_ref,omitempty"`
	TaskStatus           string             `json:"task_status"`
	OnThisScene          bool               `json:"on_this_scene"`
	Why                  string             `json:"why,omitempty"`
	Who                  string             `json:"who,omitempty"`
	AssociationUpdatedAt string             `json:"association_updated_at,omitempty"`
	UpdatedAt            string             `json:"updated_at,omitempty"`
	WaitingOn            []assoc.WaitingRef `json:"waiting_on"`
	WaitingOnSource      string             `json:"waiting_on_source"`
	Truncated            bool               `json:"truncated"`
}

type coordinationRecallScopeView struct {
	Source   string `json:"source"`
	Since    string `json:"since,omitempty"`
	Until    string `json:"until,omitempty"`
	Q        string `json:"q,omitempty"`
	PersonID string `json:"person_id,omitempty"`
	IssueID  string `json:"issue_id,omitempty"`
}

type coordinatorRecallView struct {
	Status          string                      `json:"status"`
	ConversationID  string                      `json:"conversation_id,omitempty"`
	Scope           coordinationRecallScopeView `json:"scope"`
	Items           []coordinatorRecallItem     `json:"items"`
	Limit           int                         `json:"limit"`
	Returned        int                         `json:"returned"`
	Complete        bool                        `json:"complete"`
	Truncated       bool                        `json:"truncated"`
	CharacterBudget int                         `json:"character_budget"`
}

type coordinationWorkState struct {
	IssueID         string `json:"issue_id"`
	Status          string `json:"status"`
	StatusSource    string `json:"status_source"`
	Title           string `json:"title"`
	OriginalGoal    string `json:"original_goal"`
	GoalSource      string `json:"goal_source"`
	UpdatedAt       string `json:"updated_at,omitempty"`
	Scope           string `json:"scope"`
	TaskStatus      string `json:"task_status"`
	Complete        bool   `json:"complete"`
	Truncated       bool   `json:"truncated"`
	CharacterBudget int    `json:"character_budget"`
}

func coordinationRecallLimit(limit int) int {
	if limit <= 0 {
		return coordinationRecallDefault
	}
	if limit > coordinationRecallMax {
		return coordinationRecallMax
	}
	return limit
}

func newCoordinatorRecallView(result assoc.Result, limit int) coordinatorRecallView {
	limit = coordinationRecallLimit(limit)
	view := coordinatorRecallView{
		Status: "loaded", ConversationID: assoc.NormalizeConversationID(result.ConversationID),
		Scope: coordinationRecallScopeView{Source: coordinationRecallScope, Q: result.Q},
		Items: make([]coordinatorRecallItem, 0, limit), Limit: limit,
		// The graph caps source events before computing candidates. Even a
		// short candidate list cannot certify an exhaustive historical search.
		Complete: false, Truncated: len(result.Items) > limit,
	}
	if !result.Since.IsZero() {
		view.Scope.Since = result.Since.UTC().Format(time.RFC3339Nano)
	}
	if !result.Until.IsZero() {
		view.Scope.Until = result.Until.UTC().Format(time.RFC3339Nano)
	}
	for _, item := range result.Items {
		if !assoc.PurposeNamesEvent(item.Purpose) || (view.ConversationID != "" && !item.OnThisScene) {
			view.Truncated = true
			continue
		}
		if len(view.Items) == limit {
			view.Truncated = true
			break
		}
		card := coordinatorRecallItem{
			IssueID: firstNonEmpty(item.IssueID, item.Issue), Purpose: item.Purpose, Intent: item.Intent,
			Status: "unknown", StatusSource: "not_loaded", TaskStatus: "not_loaded",
			OnThisScene: item.OnThisScene, Why: item.WhyListed, Who: recallWho(item.People),
			WaitingOn: item.WaitingOn, WaitingOnSource: "association_snapshot",
		}
		if !item.LastTouchedAt.IsZero() {
			card.AssociationUpdatedAt = item.LastTouchedAt.UTC().Format(time.RFC3339Nano)
		}
		view.Items = append(view.Items, card)
	}
	return view
}

func marshalCoordinatorRecall(result assoc.Result) (string, error) {
	return marshalCoordinationView(toolAssocRecall, newCoordinatorRecallView(result, coordinationRecallDefault))
}

func (t *AssocTools) loadRecallIssueStates(ctx context.Context, turn Turn, view *coordinatorRecallView) {
	if t == nil || t.Issues == nil {
		return
	}
	for i := range view.Items {
		card := &view.Items[i]
		args, _ := json.Marshal(issueIDArgs{IssueID: card.IssueID})
		issue, err := t.loadAgentIssue(ctx, turn, string(args))
		if err != nil {
			card.StatusSource = "unavailable"
			continue
		}
		card.Status = strings.TrimSpace(issue.Status)
		card.StatusSource = coordinationIssueStatusSource
		card.StateRef = util.UUIDToString(issue.ID)
		if issue.UpdatedAt.Valid {
			card.UpdatedAt = issue.UpdatedAt.Time.UTC().Format(time.RFC3339Nano)
		}
	}
}

func (t *AssocTools) workState(ctx context.Context, turn Turn, raw string) (string, error) {
	// The loop additionally checks that this ID came from this turn's recall.
	issue, err := t.loadAgentIssue(ctx, turn, raw)
	if err != nil {
		return "", err
	}
	view := coordinationWorkState{
		IssueID: util.UUIDToString(issue.ID), Status: strings.TrimSpace(issue.Status),
		StatusSource: coordinationIssueStatusSource, Title: strings.TrimSpace(issue.Title),
		OriginalGoal: strings.TrimSpace(issue.Description.String), GoalSource: "issue_description_excerpt",
		Scope: coordinationIssueScope, TaskStatus: "not_loaded", Complete: true,
	}
	if issue.UpdatedAt.Valid {
		view.UpdatedAt = issue.UpdatedAt.Time.UTC().Format(time.RFC3339Nano)
	}
	return marshalCoordinationView(toolWorkState, view)
}

// NormalizeCoordinationRead is the Host boundary for inbound work reads,
// including replay fixtures. Only declared coordination fields enter model
// context; raw graph events, old comments and execution results never do.
// History, full job constraints and task-finished results have separate read
// contracts and must not be routed through this projection.
func NormalizeCoordinationRead(name, raw string) (string, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &root); err != nil || root == nil {
		return "", fmt.Errorf("invalid %s response", name)
	}
	switch name {
	case toolAssocRecall:
		var view coordinatorRecallView
		// Decode only the current envelope; items are projected separately so
		// legacy waiting_on strings cannot make the complete response fail.
		view.ConversationID = assoc.NormalizeConversationID(readCoordinationString(root, "conversation_id"))
		_ = json.Unmarshal(root["scope"], &view.Scope)
		if view.Scope.Since == "" {
			view.Scope.Since = readCoordinationString(root, "since")
		}
		if view.Scope.Until == "" {
			view.Scope.Until = readCoordinationString(root, "until")
		}
		if view.Scope.Q == "" {
			view.Scope.Q = readCoordinationString(root, "q")
		}
		view.Scope.Source = coordinationRecallScope
		_ = json.Unmarshal(root["limit"], &view.Limit)
		view.Limit = coordinationRecallLimit(view.Limit)
		_ = json.Unmarshal(root["truncated"], &view.Truncated)
		// Recall does not promise a complete census: the graph query has
		// bounded events, rank selection and the requested time window.
		view.Complete = false
		view.Status = "loaded"
		for _, status := range []string{"unavailable", "not_loaded", "stale"} {
			if readCoordinationString(root, "status") == status {
				view.Status = status
			}
		}
		var items []map[string]json.RawMessage
		if data, ok := root["items"]; !ok || json.Unmarshal(data, &items) != nil {
			return "", fmt.Errorf("assoc_recall response requires items")
		}
		view.Items = make([]coordinatorRecallItem, 0, min(len(items), view.Limit))
		for _, item := range items {
			if len(view.Items) == view.Limit {
				view.Truncated = true
				break
			}
			card := normalizeRecallItem(item, view.ConversationID)
			if card.IssueID == "" {
				view.Truncated = true
				continue
			}
			view.Truncated = view.Truncated || card.Truncated
			view.Items = append(view.Items, card)
		}
		if len(view.Items) == 0 && view.Status == "loaded" {
			view.Status = "empty"
		}
		view.Returned = len(view.Items)
		view.CharacterBudget = coordinationRecallBudget
		var clipped bool
		if utf8.RuneCountInString(view.ConversationID) > 160 {
			return "", fmt.Errorf("assoc_recall conversation_id exceeds coordination budget")
		}
		view.Scope.Q = clipCoordinationField(view.Scope.Q, 120, &clipped)
		if utf8.RuneCountInString(view.Scope.PersonID) > 128 || utf8.RuneCountInString(view.Scope.IssueID) > 128 {
			return "", fmt.Errorf("assoc_recall scope identifier exceeds coordination budget")
		}
		view.Scope.Since = normalizedCoordinationTime(view.Scope.Since)
		view.Scope.Until = normalizedCoordinationTime(view.Scope.Until)
		view.Truncated = view.Truncated || clipped
		for {
			encoded, err := json.Marshal(view)
			if err != nil {
				return "", err
			}
			if utf8.RuneCount(encoded) <= coordinationRecallBudget {
				return string(encoded), nil
			}
			if len(view.Items) == 0 {
				return "", fmt.Errorf("assoc_recall response exceeds coordination budget")
			}
			view.Items = view.Items[:len(view.Items)-1]
			view.Returned = len(view.Items)
			view.Truncated = true
		}
	case toolWorkState:
		var view coordinationWorkState
		if err := json.Unmarshal([]byte(raw), &view); err != nil || strings.TrimSpace(view.IssueID) == "" {
			return "", fmt.Errorf("work_state response requires issue_id")
		}
		view.IssueID = strings.TrimSpace(view.IssueID)
		if id, err := util.ParseUUID(view.IssueID); err != nil || !id.Valid {
			return "", fmt.Errorf("work_state response requires an exact Issue UUID")
		}
		if view.Scope != coordinationIssueScope {
			return "", fmt.Errorf("work_state response must preserve its current-agent workspace scope")
		}
		view.Title = clipCoordinationField(view.Title, 120, &view.Truncated)
		view.OriginalGoal = clipCoordinationField(view.OriginalGoal, coordinationGoalBudget, &view.Truncated)
		view.Scope = coordinationIssueScope
		view.GoalSource = "issue_description_excerpt"
		view.Status, view.StatusSource = normalizeIssueStatus(view.Status, view.StatusSource)
		view.UpdatedAt = normalizedCoordinationTime(view.UpdatedAt)
		view.TaskStatus = "not_loaded"
		view.Complete = !view.Truncated && view.StatusSource == coordinationIssueStatusSource
		view.CharacterBudget = coordinationWorkStateBudget
		encoded, err := json.Marshal(view)
		if err != nil {
			return "", err
		}
		if utf8.RuneCount(encoded) > coordinationWorkStateBudget {
			return "", fmt.Errorf("work_state response exceeds coordination budget")
		}
		return string(encoded), nil
	default:
		return "", fmt.Errorf("%s is not an inbound coordination read", name)
	}
}

func normalizeRecallItem(raw map[string]json.RawMessage, cid string) coordinatorRecallItem {
	card := coordinatorRecallItem{
		IssueID: firstNonEmpty(readCoordinationString(raw, "issue_id"), readCoordinationString(raw, "issue")),
		Purpose: readCoordinationString(raw, "purpose"), Intent: readCoordinationString(raw, "intent"),
		Status: readCoordinationString(raw, "status"), StatusSource: readCoordinationString(raw, "status_source"),
		Why: firstNonEmpty(readCoordinationString(raw, "why"), readCoordinationString(raw, "why_listed")),
		Who: readCoordinationString(raw, "who"), UpdatedAt: readCoordinationString(raw, "updated_at"),
		AssociationUpdatedAt: firstNonEmpty(readCoordinationString(raw, "association_updated_at"), readCoordinationString(raw, "last_touched_at")),
		WaitingOn:            make([]assoc.WaitingRef, 0), WaitingOnSource: "association_snapshot", TaskStatus: "not_loaded",
	}
	_ = json.Unmarshal(raw["truncated"], &card.Truncated)
	var onThisScene *bool
	_ = json.Unmarshal(raw["on_this_scene"], &onThisScene)
	card.OnThisScene = recalledItemOnThisScene(onThisScene, raw["waiting_on"], cid)
	if readCoordinationString(raw, "matched_via") == "window" || card.Why == "关键词命中，不是本会话" {
		card.OnThisScene = false
	}
	if data := raw["waiting_on"]; len(data) > 0 {
		if err := json.Unmarshal(data, &card.WaitingOn); err != nil {
			if waiting := readCoordinationString(raw, "waiting_on"); waiting != "" {
				card.WaitingOn = []assoc.WaitingRef{{ConversationID: waiting}}
			}
		}
	}
	if card.WaitingOn == nil {
		card.WaitingOn = make([]assoc.WaitingRef, 0)
	}
	if len(card.WaitingOn) > coordinationWaitingMax {
		card.WaitingOn = card.WaitingOn[:coordinationWaitingMax]
		card.Truncated = true
	}
	keptWaiting := card.WaitingOn[:0]
	for _, ref := range card.WaitingOn {
		ref.ConversationID = assoc.NormalizeConversationID(ref.ConversationID)
		ref.PersonID = strings.TrimSpace(ref.PersonID)
		if utf8.RuneCountInString(ref.ConversationID) > 160 || utf8.RuneCountInString(ref.PersonID) > 128 {
			card.Truncated = true
			continue
		}
		keptWaiting = append(keptWaiting, ref)
	}
	card.WaitingOn = keptWaiting
	// Never cut identifiers into a different, potentially valid reference.
	if utf8.RuneCountInString(card.IssueID) > 128 {
		card.IssueID = ""
		card.Truncated = true
	}
	card.Purpose = clipCoordinationField(card.Purpose, coordinationGoalBudget, &card.Truncated)
	card.Intent = clipCoordinationField(card.Intent, 32, &card.Truncated)
	card.Why = clipCoordinationField(card.Why, 80, &card.Truncated)
	card.Who = clipCoordinationField(card.Who, 80, &card.Truncated)
	card.Status, card.StatusSource = normalizeIssueStatus(card.Status, card.StatusSource)
	card.UpdatedAt = normalizedCoordinationTime(card.UpdatedAt)
	card.AssociationUpdatedAt = normalizedCoordinationTime(card.AssociationUpdatedAt)
	if card.StatusSource == coordinationIssueStatusSource {
		if id, err := util.ParseUUID(card.IssueID); err == nil && id.Valid {
			card.StateRef = card.IssueID
		}
	} else {
		card.UpdatedAt = ""
	}
	return card
}

func normalizeIssueStatus(status, source string) (string, string) {
	if source == coordinationIssueStatusSource {
		switch strings.TrimSpace(status) {
		case "backlog", "todo", "in_progress", "in_review", "done", "blocked", "cancelled":
			return strings.TrimSpace(status), source
		}
		return "unknown", "unavailable"
	}
	if source != "unavailable" && source != "stale" {
		source = "not_loaded"
	}
	return "unknown", source
}

func normalizedCoordinationTime(value string) string {
	if at, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return at.UTC().Format(time.RFC3339Nano)
	}
	return ""
}

func readCoordinationString(raw map[string]json.RawMessage, key string) string {
	var value string
	_ = json.Unmarshal(raw[key], &value)
	return strings.TrimSpace(value)
}

func clipCoordinationField(value string, budget int, truncated *bool) string {
	value = strings.TrimSpace(value)
	if utf8.RuneCountInString(value) <= budget {
		return value
	}
	*truncated = true
	return string([]rune(value)[:budget])
}

func marshalCoordinationView(name string, view any) (string, error) {
	body, err := json.Marshal(view)
	if err != nil {
		return "", err
	}
	return NormalizeCoordinationRead(name, string(body))
}

func recallWho(people []assoc.PersonRef) string {
	var names []string
	seen := map[string]struct{}{}
	for _, person := range people {
		name := firstNonEmpty(person.Name, person.DisplayName)
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	return strings.Join(names, "、")
}
