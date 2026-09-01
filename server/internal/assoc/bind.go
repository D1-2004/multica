package assoc

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

type BindOutboundInput struct {
	WorkspaceID    string
	AgentID        string
	IssueID        string
	IssueTitle     string
	RunID          string
	ConversationID string
	PersonID       string
	PersonAliases  []string
	EvidenceID     string
	Kind           string
	Intent         string
	Purpose        string
}

type BindOutboundResult struct {
	ConversationID string `json:"conversation_id"`
	TaskID         string `json:"task_id,omitempty"`
	IssueID        string `json:"issue_id,omitempty"`
	Linked         bool   `json:"linked"`
}

type graphActor struct {
	WorkspaceID string
	AgentID     string
	RunID       string
}

func BindOutbound(ctx context.Context, store Store, in BindOutboundInput) (BindOutboundResult, error) {
	var result BindOutboundResult
	err := withStoreTx(ctx, store, func(store Store) error {
		var err error
		result, err = bindOutbound(ctx, store, in)
		return err
	})
	return result, err
}

// ValidSceneID rejects tool command text that used to be stored as a conversation id.
func ValidSceneID(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" || strings.ContainsAny(s, " \t\n") {
		return false
	}
	if strings.HasPrefix(s, "$") || strings.Contains(s, "dws") || strings.Contains(s, "--") {
		return false
	}
	return true
}

func bindOutbound(ctx context.Context, store Store, in BindOutboundInput) (BindOutboundResult, error) {
	cid := strings.TrimSpace(in.ConversationID)
	if cid == "" {
		return BindOutboundResult{}, fmt.Errorf("%w: conversation_id is required", ErrInvalidQuery)
	}
	if !ValidSceneID(cid) {
		return BindOutboundResult{}, fmt.Errorf("%w: conversation_id is not a scene id", ErrInvalidQuery)
	}
	if strings.TrimSpace(in.WorkspaceID) == "" || strings.TrimSpace(in.AgentID) == "" {
		return BindOutboundResult{}, fmt.Errorf("%w: workspace_id and agent_id are required", ErrInvalidQuery)
	}
	kind := normalizeSceneKind(in.Kind)
	now := time.Now().UTC()
	evidenceID := strings.TrimSpace(in.EvidenceID)
	if evidenceID == "" {
		evidenceID = "outbound:" + cid + ":" + now.UTC().Format(time.RFC3339Nano)
	}
	personKey, personAliases := CanonicalPersonKey(append([]string{in.PersonID}, in.PersonAliases...)...)

	event := Event{
		WorkspaceID: in.WorkspaceID,
		AgentID:     in.AgentID,
		Source:      "outbound_im",
		Direction:   DirOutbound,
		EvidenceID:  evidenceID,
		OccurredAt:  now,
		SceneKey:    cid,
		PersonKey:   personKey,
	}

	if err := store.EnsureScene(ctx, in.WorkspaceID, in.AgentID, cid, kind, now); err != nil {
		return BindOutboundResult{}, err
	}
	if personKey != "" {
		if err := store.EnsurePerson(ctx, in.WorkspaceID, in.AgentID, personKey, "", personAliases); err != nil {
			return BindOutboundResult{}, err
		}
	}

	if strings.TrimSpace(in.IssueID) == "" {
		if _, err := store.InsertEvent(ctx, event); err != nil {
			return BindOutboundResult{}, err
		}
		return BindOutboundResult{ConversationID: cid, Linked: false}, nil
	}

	task, err := ensureIssueTask(ctx, store, in, now)
	if err != nil {
		return BindOutboundResult{}, err
	}
	event.TaskID = task.ID
	recorded, err := store.InsertEvent(ctx, event)
	if err != nil {
		return BindOutboundResult{}, err
	}
	actor := graphActor{WorkspaceID: in.WorkspaceID, AgentID: in.AgentID, RunID: in.RunID}
	props := map[string]any{"kind": kind, "conversation_id": cid}
	if err := bindEdge(ctx, store, actor, NodeTask, task.ID, NodeScene, cid, RelTaskScene, props, now); err != nil {
		return BindOutboundResult{}, err
	}
	if err := bindEdge(ctx, store, actor, NodeTask, task.ID, NodeScene, cid, RelOutreach, props, now); err != nil {
		return BindOutboundResult{}, err
	}
	if err := bindEdge(ctx, store, actor, NodeTask, task.ID, NodeScene, cid, RelWaitingOn, props, now); err != nil {
		return BindOutboundResult{}, err
	}
	if err := bindEdge(ctx, store, actor, NodeTask, task.ID, NodeIssue, in.IssueID, RelTaskIssue, map[string]any{}, now); err != nil {
		return BindOutboundResult{}, err
	}
	if personKey != "" {
		if err := bindEdge(ctx, store, actor, NodeTask, task.ID, NodePerson, personKey, RelTaskPerson, map[string]any{"delegated": true}, now); err != nil {
			return BindOutboundResult{}, err
		}
	}
	if recorded.ID != "" {
		if err := bindEdge(ctx, store, actor, NodeEvent, recorded.ID, NodeTask, task.ID, RelEventOf, map[string]any{}, now); err != nil {
			return BindOutboundResult{}, err
		}
	}
	if err := store.TouchTask(ctx, task.ID, now); err != nil {
		return BindOutboundResult{}, err
	}
	return BindOutboundResult{
		ConversationID: cid,
		TaskID:         task.ID,
		IssueID:        in.IssueID,
		Linked:         true,
	}, nil
}

func ensureIssueTask(ctx context.Context, store Store, in BindOutboundInput, now time.Time) (Task, error) {
	task, err := store.GetOpenTaskByIssue(ctx, in.WorkspaceID, in.AgentID, in.IssueID)
	if err == nil {
		return task, nil
	}
	if err != nil && !errors.Is(err, ErrNotFound) {
		return Task{}, err
	}
	purpose, err := ResolvePurpose(in.Purpose, in.IssueTitle)
	if err != nil {
		return Task{}, err
	}
	return store.InsertTask(ctx, Task{
		WorkspaceID:   in.WorkspaceID,
		AgentID:       in.AgentID,
		IssueID:       in.IssueID,
		Purpose:       purpose,
		Status:        StatusWaiting,
		Intent:        strings.TrimSpace(in.Intent),
		RunID:         in.RunID,
		LastTouchedAt: now,
	})
}

func bindEdge(ctx context.Context, store Store, actor graphActor, srcType, srcID, dstType, dstID, rel string, props map[string]any, now time.Time) error {
	_, err := store.InsertEdge(ctx, Edge{
		WorkspaceID:   actor.WorkspaceID,
		AgentID:       actor.AgentID,
		SrcType:       srcType,
		SrcID:         srcID,
		DstType:       dstType,
		DstID:         dstID,
		Rel:           rel,
		Status:        StatusOpen,
		Props:         props,
		LastTouchedAt: now,
		OpenedByRunID: actor.RunID,
	})
	return err
}

func normalizeSceneKind(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "group", "2":
		return "group"
	default:
		return "dm"
	}
}
