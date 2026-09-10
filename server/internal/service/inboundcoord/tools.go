package inboundcoord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/assoc"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	toolAssocRecall      = "assoc_recall"
	toolAssocBind        = "assoc_bind"
	toolIssueGet         = "issue_get"
	toolWorkState        = "work_state"
	toolIssueCommentList = "issue_comment_list"
	toolIssueCommentAdd  = "issue_comment_add"
	toolFinish           = "finish"
	issueDescBudget      = 240
	commentClipBudget    = 200
	commentAddBudget     = 500
	commentListDefault   = 20
)

// Tools runs coordinator loop function calls. The sandbox still owns DWS.
type Tools interface {
	Call(ctx context.Context, turn Turn, name, arguments string) (string, error)
}

// IssueAccess is the Issue read/write surface the short loop is allowed to use.
type IssueAccess interface {
	GetIssueInWorkspace(ctx context.Context, arg db.GetIssueInWorkspaceParams) (db.Issue, error)
	ListCommentsForIssue(ctx context.Context, arg db.ListCommentsForIssueParams) ([]db.Comment, error)
}

// ErrIssueBusy means a member comment could not trigger the next Issue task
// until the current one settles. The dispatch caller should retry the same
// inbound message instead of creating another Issue.
var ErrIssueBusy = errors.New("issue already has an active task")

// IssueCommentEffect is the durable member comment and its Issue-owned task.
// TaskID is not returned to Router as its external task: Router is settled by
// finish.text while this task continues independently on the Issue.
type IssueCommentEffect struct {
	IssueID         string `json:"issue_id"`
	IssueIdentifier string `json:"issue_identifier,omitempty"`
	CommentID       string `json:"comment_id"`
	TaskID          string `json:"task_id"`
}

// IssueCommentWriter routes a short-loop comment through the normal member
// comment service so the existing Issue self-drive mechanism enqueues work.
type IssueCommentWriter interface {
	AddMemberComment(ctx context.Context, turn Turn, issue db.Issue, content string, parentID pgtype.UUID) (IssueCommentEffect, error)
}

// AssocTools exposes scene-graph recall, bind, and Issue lookup to the loop.
type AssocTools struct {
	Service       *assoc.Service
	Issues        IssueAccess
	CommentWriter IssueCommentWriter
}

type recallArgs struct {
	Since          string `json:"since"`
	ConversationID string `json:"conversation_id"`
	PersonID       string `json:"person_id"`
	Issue          string `json:"issue"`
	Q              string `json:"q"`
	Limit          int    `json:"limit"`
}

type bindArgs struct {
	ConversationID string `json:"conversation_id"`
	IssueID        string `json:"issue_id"`
	EvidenceID     string `json:"evidence_id"`
	PersonID       string `json:"person_id"`
	DisplayName    string `json:"display_name"`
	Delegator      string `json:"delegator"`
	Place          string `json:"place"`
	Purpose        string `json:"purpose"`
	Intent         string `json:"intent"`
	Kind           string `json:"kind"`
	WaitingOn      string `json:"waiting_on"`
}

type issueIDArgs struct {
	IssueID   string `json:"issue_id"`
	Thread    string `json:"thread"`
	Since     string `json:"since"`
	Tail      int    `json:"tail"`
	Content   string `json:"content"`
	Parent    string `json:"parent"`
	ReplyText string `json:"reply_text"`
}

func (t *AssocTools) Call(ctx context.Context, turn Turn, name, arguments string) (string, error) {
	switch strings.TrimSpace(name) {
	case toolAssocRecall:
		if t == nil || t.Service == nil {
			return "", fmt.Errorf("association store is not configured")
		}
		return t.recall(ctx, turn, arguments)
	case toolAssocBind:
		if t == nil || t.Service == nil {
			return "", fmt.Errorf("association store is not configured")
		}
		return t.bind(ctx, turn, arguments)
	case toolWorkState:
		return t.workState(ctx, turn, arguments)
	case toolIssueGet:
		return t.issueGet(ctx, turn, arguments)
	case toolIssueCommentList:
		return t.issueCommentList(ctx, turn, arguments)
	case toolIssueCommentAdd:
		return t.issueCommentAdd(ctx, turn, arguments)
	default:
		return "", fmt.Errorf("unknown tool %q", name)
	}
}

// defaultRecallConversationID fills the inbound scene cid when the model
// omitted it. q is a filter on that scene, not a substitute for dropping cid.
func defaultRecallConversationID(raw, inbound string) string {
	inbound = strings.TrimSpace(inbound)
	if inbound == "" {
		return raw
	}
	var args map[string]any
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		args = map[string]any{}
	} else if err := json.Unmarshal([]byte(trimmed), &args); err != nil || args == nil {
		return raw
	}
	cid, _ := args["conversation_id"].(string)
	if strings.TrimSpace(cid) != "" {
		return raw
	}
	args["conversation_id"] = inbound
	out, err := json.Marshal(args)
	if err != nil {
		return raw
	}
	return string(out)
}

func (t *AssocTools) recall(ctx context.Context, turn Turn, raw string) (string, error) {
	raw = defaultRecallConversationID(raw, turn.ConversationID)
	var args recallArgs
	if strings.TrimSpace(raw) != "" && json.Unmarshal([]byte(raw), &args) != nil {
		return "", fmt.Errorf("invalid assoc_recall arguments")
	}
	now := time.Now().UTC()
	sinceRaw := strings.TrimSpace(args.Since)
	if sinceRaw == "" {
		sinceRaw = "48h"
	}
	since, err := assoc.ParseSince(sinceRaw, now)
	if err != nil {
		return "", err
	}
	cid := strings.TrimSpace(args.ConversationID)
	issue := strings.TrimSpace(args.Issue)
	needle := strings.TrimSpace(args.Q)
	if cid == "" && issue == "" && needle == "" {
		cid = strings.TrimSpace(turn.ConversationID)
	}
	limit := coordinationRecallLimit(args.Limit)
	q := assoc.Query{
		WorkspaceID:    strings.TrimSpace(turn.WorkspaceID),
		AgentID:        util.UUIDToString(turn.AgentID),
		Since:          since,
		Until:          now,
		ConversationID: cid,
		PersonID:       strings.TrimSpace(args.PersonID),
		IssueID:        issue,
		Q:              needle,
		Limit:          limit + 1,
	}
	result, err := t.Service.Recall(ctx, q)
	if err != nil {
		return "", err
	}
	view := newCoordinatorRecallView(result, limit)
	view.Scope.PersonID = q.PersonID
	view.Scope.IssueID = q.IssueID
	t.loadRecallIssueStates(ctx, turn, &view)
	return marshalCoordinationView(toolAssocRecall, view)
}

func (t *AssocTools) bind(ctx context.Context, turn Turn, raw string) (string, error) {
	var args bindArgs
	if strings.TrimSpace(raw) != "" && json.Unmarshal([]byte(raw), &args) != nil {
		return "", fmt.Errorf("invalid assoc_bind arguments")
	}
	intent, ok := assoc.CoordinatorIntent(args.Intent)
	if !ok {
		return "", hintErr("intent must be one of ask, confirm, notify, lookup, wait, other", hintIntent)
	}
	cid := assoc.NormalizeConversationID(firstNonEmpty(args.ConversationID, turn.ConversationID))
	if cid == "" {
		return "", hintErr("conversation_id is required", hintConversation)
	}
	issueID := strings.TrimSpace(args.IssueID)
	if issueID == "" {
		return "", hintErr("issue_id is required; assoc_bind attaches this scene to an existing Issue from assoc_recall", hintBindNeedsIssue)
	}
	delegator := firstNonEmpty(args.Delegator, turn.SenderName)
	purpose, err := assoc.ComposeCoordinatorPurpose(delegator, args.Place, args.Purpose)
	if err != nil {
		return "", hintWrap("purpose must name 委托人, 事件, and 目的, such as 冬翔委托：向辰驷确认明天几点打球", hintPurpose, err)
	}
	kind := firstNonEmpty(args.Kind, turn.Kind)
	display := firstNonEmpty(args.DisplayName, turn.SenderName)
	waitingOn := strings.TrimSpace(args.WaitingOn)
	payload := map[string]any{
		"conversation_id": cid,
		"purpose":         purpose,
		"delegator":       delegator,
		"intent":          intent,
		"intent_label":    assoc.IntentLabel(intent),
		"kind":            kind,
		"display_name":    display,
		"waiting_on":      waitingOn,
		"person_id":       firstNonEmpty(args.PersonID, turn.PersonID),
	}
	if place := strings.TrimSpace(args.Place); place != "" && place != "地点未说明" {
		payload["place"] = place
	}
	if err := t.Service.AssociateIssueConversation(ctx, assoc.AssociateInput{
		WorkspaceID:    strings.TrimSpace(turn.WorkspaceID),
		AgentID:        util.UUIDToString(turn.AgentID),
		IssueID:        issueID,
		IssueTitle:     purpose,
		Purpose:        purpose,
		Intent:         intent,
		ConversationID: cid,
		EvidenceID:     firstNonEmpty(args.EvidenceID, turn.EvidenceID),
		PersonID:       firstNonEmpty(args.PersonID, turn.PersonID),
		DisplayName:    display,
		Kind:           kind,
		WaitingOn:      waitingOn,
	}); err != nil {
		return "", err
	}
	payload["issue_id"] = issueID
	payload["pending"] = false
	payload["linked"] = true
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func (t *AssocTools) issueGet(ctx context.Context, turn Turn, raw string) (string, error) {
	issue, err := t.loadAgentIssue(ctx, turn, raw)
	if err != nil {
		return "", err
	}
	payload := map[string]any{
		"issue_id":    util.UUIDToString(issue.ID),
		"title":       strings.TrimSpace(issue.Title),
		"status":      strings.TrimSpace(issue.Status),
		"description": assoc.ClipBody(issue.Description.String, issueDescBudget),
	}
	if issue.UpdatedAt.Valid {
		payload["updated_at"] = issue.UpdatedAt.Time.UTC().Format(time.RFC3339)
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func (t *AssocTools) issueCommentList(ctx context.Context, turn Turn, raw string) (string, error) {
	issue, err := t.loadAgentIssue(ctx, turn, raw)
	if err != nil {
		return "", err
	}
	var args issueIDArgs
	_ = json.Unmarshal([]byte(strings.TrimSpace(raw)), &args)
	limit := args.Tail
	if limit <= 0 {
		limit = commentListDefault
	}
	if limit > assoc.MaxLimit {
		limit = assoc.MaxLimit
	}
	rows, err := t.Issues.ListCommentsForIssue(ctx, db.ListCommentsForIssueParams{
		IssueID:     issue.ID,
		WorkspaceID: issue.WorkspaceID,
		Limit:       int32(limit),
	})
	if err != nil {
		return "", err
	}
	comments := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		item := map[string]any{
			"id":          util.UUIDToString(row.ID),
			"author_type": row.AuthorType,
			"content":     assoc.ClipBody(row.Content, commentClipBudget),
			"created_at":  row.CreatedAt.Time.UTC().Format(time.RFC3339),
		}
		if row.ParentID.Valid {
			item["parent_id"] = util.UUIDToString(row.ParentID)
		}
		comments = append(comments, item)
	}
	body, err := json.Marshal(map[string]any{
		"issue_id": util.UUIDToString(issue.ID),
		"comments": comments,
	})
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func (t *AssocTools) issueCommentAdd(ctx context.Context, turn Turn, raw string) (string, error) {
	issue, err := t.loadAgentIssue(ctx, turn, raw)
	if err != nil {
		return "", err
	}
	var args issueIDArgs
	if strings.TrimSpace(raw) != "" && json.Unmarshal([]byte(raw), &args) != nil {
		return "", fmt.Errorf("invalid issue_comment_add arguments")
	}
	content := assoc.ClipBody(args.Content, commentAddBudget)
	if content == "" {
		return "", fmt.Errorf("content is required")
	}
	if !turn.UserID.Valid {
		return "", fmt.Errorf("member identity is required")
	}
	if t == nil || t.CommentWriter == nil {
		return "", fmt.Errorf("issue comment writer is not configured")
	}
	var parentID pgtype.UUID
	if parent := strings.TrimSpace(args.Parent); parent != "" {
		parsed, parseErr := util.ParseUUID(parent)
		if parseErr != nil {
			return "", fmt.Errorf("parent must be a comment UUID")
		}
		parentID = parsed
	}
	effect, err := t.CommentWriter.AddMemberComment(ctx, turn, issue, content, parentID)
	if err != nil {
		return "", err
	}
	body, err := json.Marshal(effect)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func (t *AssocTools) loadAgentIssue(ctx context.Context, turn Turn, raw string) (db.Issue, error) {
	if t == nil || t.Issues == nil {
		return db.Issue{}, fmt.Errorf("issue store is not configured")
	}
	var args issueIDArgs
	if strings.TrimSpace(raw) != "" && json.Unmarshal([]byte(raw), &args) != nil {
		return db.Issue{}, fmt.Errorf("invalid issue arguments")
	}
	issueID := strings.TrimSpace(args.IssueID)
	if issueID == "" {
		return db.Issue{}, fmt.Errorf("issue_id is required")
	}
	parsedIssue, err := util.ParseUUID(issueID)
	if err != nil {
		return db.Issue{}, fmt.Errorf("issue_id must be copied exactly from assoc_recall")
	}
	workspace, err := util.ParseUUID(strings.TrimSpace(turn.WorkspaceID))
	if err != nil {
		return db.Issue{}, fmt.Errorf("workspace_id is required")
	}
	if !turn.AgentID.Valid {
		return db.Issue{}, fmt.Errorf("agent_id is required")
	}
	issue, err := t.Issues.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{
		ID:          parsedIssue,
		WorkspaceID: workspace,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return db.Issue{}, fmt.Errorf("issue not found")
		}
		return db.Issue{}, err
	}
	if !issue.AssigneeType.Valid || issue.AssigneeType.String != "agent" || issue.AssigneeID != turn.AgentID {
		return db.Issue{}, fmt.Errorf("issue is not assigned to this agent")
	}
	return issue, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
