package assoc

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

type AssociateInput struct {
	WorkspaceID    string
	AgentID        string
	IssueID        string
	IssueTitle     string
	RunID          string
	ConversationID string
	EvidenceID     string
	PersonID       string
	PersonAliases  []string
	Intent         string
	Kind           string
}

func AssociateIssueConversation(ctx context.Context, store Store, in AssociateInput) error {
	return withStoreTx(ctx, store, func(store Store) error {
		return associateIssueConversation(ctx, store, in)
	})
}

func associateIssueConversation(ctx context.Context, store Store, in AssociateInput) error {
	if strings.TrimSpace(in.WorkspaceID) == "" || strings.TrimSpace(in.AgentID) == "" {
		return fmt.Errorf("%w: workspace_id and agent_id are required", ErrInvalidQuery)
	}
	if strings.TrimSpace(in.IssueID) == "" {
		return fmt.Errorf("%w: issue_id is required", ErrInvalidQuery)
	}
	cid := strings.TrimSpace(in.ConversationID)
	kind := normalizeSceneKind(in.Kind)
	now := time.Now().UTC()
	personKey, personAliases := CanonicalPersonKey(append([]string{in.PersonID}, in.PersonAliases...)...)
	task, err := ensureIssueTask(ctx, store, BindOutboundInput{
		WorkspaceID:    in.WorkspaceID,
		AgentID:        in.AgentID,
		IssueID:        in.IssueID,
		IssueTitle:     in.IssueTitle,
		RunID:          in.RunID,
		ConversationID: cid,
		Intent:         in.Intent,
	}, now)
	if err != nil {
		return err
	}
	actor := graphActor{WorkspaceID: in.WorkspaceID, AgentID: in.AgentID, RunID: in.RunID}
	if cid != "" {
		if err := store.EnsureScene(ctx, in.WorkspaceID, in.AgentID, cid, kind, now); err != nil {
			return err
		}
		props := map[string]any{"kind": kind, "conversation_id": cid}
		if err := bindEdge(ctx, store, actor, NodeTask, task.ID, NodeScene, cid, RelTaskScene, props, now); err != nil {
			return err
		}
		if err := bindEdge(ctx, store, actor, NodeTask, task.ID, NodeScene, cid, RelSpawnedFrom, props, now); err != nil {
			return err
		}
	}
	if personKey != "" {
		if err := store.EnsurePerson(ctx, in.WorkspaceID, in.AgentID, personKey, "", personAliases); err != nil {
			return err
		}
		if err := bindEdge(ctx, store, actor, NodeTask, task.ID, NodePerson, personKey, RelTaskPerson, map[string]any{}, now); err != nil {
			return err
		}
	}
	if evidence := strings.TrimSpace(in.EvidenceID); evidence != "" {
		if err := store.UpdateEventTask(ctx, in.WorkspaceID, in.AgentID, evidence, task.ID); err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
		if ev, err := store.GetEventByEvidence(ctx, in.WorkspaceID, in.AgentID, evidence); err == nil && ev.ID != "" {
			if err := bindEdge(ctx, store, actor, NodeEvent, ev.ID, NodeTask, task.ID, RelEventOf, map[string]any{}, now); err != nil {
				return err
			}
		}
	}
	return store.TouchTask(ctx, task.ID, now)
}
