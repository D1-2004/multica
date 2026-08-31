package inboundcoord

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/assoc"
	"github.com/multica-ai/multica/server/internal/util"
)

const (
	toolAssocRecall = "assoc_recall"
	toolAssocBind   = "assoc_bind"
	toolFinish      = "finish"
)

// Tools runs coordinator loop function calls. The sandbox still owns DWS.
type Tools interface {
	Call(ctx context.Context, turn Turn, name, arguments string) (string, error)
}

// AssocTools exposes scene-graph recall and bind to the coordinator loop.
type AssocTools struct {
	Service *assoc.Service
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
	Purpose        string `json:"purpose"`
	Kind           string `json:"kind"`
}

func (t *AssocTools) Call(ctx context.Context, turn Turn, name, arguments string) (string, error) {
	if t == nil || t.Service == nil {
		return "", fmt.Errorf("association store is not configured")
	}
	switch strings.TrimSpace(name) {
	case toolAssocRecall:
		return t.recall(ctx, turn, arguments)
	case toolAssocBind:
		return t.bind(ctx, turn, arguments)
	default:
		return "", fmt.Errorf("unknown tool %q", name)
	}
}

func (t *AssocTools) recall(ctx context.Context, turn Turn, raw string) (string, error) {
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
	q := assoc.Query{
		WorkspaceID:    strings.TrimSpace(turn.WorkspaceID),
		AgentID:        util.UUIDToString(turn.AgentID),
		Since:          since,
		Until:          now,
		ConversationID: firstNonEmpty(args.ConversationID, turn.ConversationID),
		PersonID:       strings.TrimSpace(args.PersonID),
		IssueID:        strings.TrimSpace(args.Issue),
		Q:              strings.TrimSpace(args.Q),
		Limit:          args.Limit,
	}
	result, err := t.Service.Recall(ctx, q)
	if err != nil {
		return "", err
	}
	body, err := json.Marshal(result)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func (t *AssocTools) bind(ctx context.Context, turn Turn, raw string) (string, error) {
	var args bindArgs
	if strings.TrimSpace(raw) != "" && json.Unmarshal([]byte(raw), &args) != nil {
		return "", fmt.Errorf("invalid assoc_bind arguments")
	}
	cid := firstNonEmpty(args.ConversationID, turn.ConversationID)
	if cid == "" {
		return "", fmt.Errorf("conversation_id is required")
	}
	issueID := strings.TrimSpace(args.IssueID)
	if issueID == "" {
		got, err := t.Service.BindOutbound(ctx, assoc.BindOutboundInput{
			WorkspaceID:    strings.TrimSpace(turn.WorkspaceID),
			AgentID:        util.UUIDToString(turn.AgentID),
			ConversationID: cid,
			EvidenceID:     firstNonEmpty(args.EvidenceID, turn.EvidenceID),
			PersonID:       firstNonEmpty(args.PersonID, turn.PersonID),
			Kind:           firstNonEmpty(args.Kind, turn.Kind),
			Purpose:        strings.TrimSpace(args.Purpose),
		})
		if err != nil {
			return "", err
		}
		body, err := json.Marshal(got)
		if err != nil {
			return "", err
		}
		return string(body), nil
	}
	if err := t.Service.AssociateIssueConversation(ctx, assoc.AssociateInput{
		WorkspaceID:    strings.TrimSpace(turn.WorkspaceID),
		AgentID:        util.UUIDToString(turn.AgentID),
		IssueID:        issueID,
		IssueTitle:     strings.TrimSpace(args.Purpose),
		Purpose:        strings.TrimSpace(args.Purpose),
		ConversationID: cid,
		EvidenceID:     firstNonEmpty(args.EvidenceID, turn.EvidenceID),
		PersonID:       firstNonEmpty(args.PersonID, turn.PersonID),
		Kind:           firstNonEmpty(args.Kind, turn.Kind),
	}); err != nil {
		return "", err
	}
	body, err := json.Marshal(map[string]any{
		"conversation_id": cid,
		"issue_id":        issueID,
		"linked":          true,
	})
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
