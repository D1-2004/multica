package assoc

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// BindOutboundInput links an outbound message to the graph. Scene is the
// Host-resolved scene of the conversation the message went to; WaitingOn,
// when set, is another resolved scene the Issue now waits on.
type BindOutboundInput struct {
	WorkspaceID   string
	AgentID       string
	IssueID       string
	IssueTitle    string
	RunID         string
	Scene         SceneNode
	PersonID      string
	PersonAliases []string
	DisplayName   string
	EvidenceID    string
	Intent        string
	Purpose       string
	WaitingOn     *SceneNode
}

type BindOutboundResult struct {
	SceneID        string `json:"scene_id"`
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

// ValidConversationID rejects tool command text that used to be stored as a
// conversation id.
func ValidConversationID(s string) bool {
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
	if !in.Scene.valid() {
		return BindOutboundResult{}, fmt.Errorf("%w: scene_id is required", ErrInvalidQuery)
	}
	if strings.TrimSpace(in.WorkspaceID) == "" || strings.TrimSpace(in.AgentID) == "" {
		return BindOutboundResult{}, fmt.Errorf("%w: workspace_id and agent_id are required", ErrInvalidQuery)
	}
	sceneID := in.Scene.SceneID
	now := time.Now().UTC()
	evidenceID := strings.TrimSpace(in.EvidenceID)
	if evidenceID == "" {
		evidenceID = "outbound:" + sceneID + ":" + now.UTC().Format(time.RFC3339Nano)
	}
	personKey, personAliases := CanonicalPersonKey(append([]string{in.PersonID}, in.PersonAliases...)...)

	event := Event{
		WorkspaceID: in.WorkspaceID,
		AgentID:     in.AgentID,
		Source:      "outbound_im",
		Direction:   DirOutbound,
		EvidenceID:  evidenceID,
		Body:        ClipBody(in.Purpose, EventBodyMaxRunes),
		OccurredAt:  now,
		SceneID:     sceneID,
		PersonKey:   personKey,
	}

	if personKey != "" {
		if err := store.EnsurePerson(ctx, in.WorkspaceID, in.AgentID, personKey, strings.TrimSpace(in.DisplayName), personAliases); err != nil {
			return BindOutboundResult{}, err
		}
	}
	result := BindOutboundResult{SceneID: sceneID, ConversationID: in.Scene.ConversationID}

	if strings.TrimSpace(in.IssueID) == "" {
		if _, err := store.InsertEvent(ctx, event); err != nil {
			return BindOutboundResult{}, err
		}
		return result, nil
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
	props := sceneProps(in.Scene)
	for _, rel := range []string{RelTaskScene, RelOutreach, RelWaitingOn} {
		if err := bindEdge(ctx, store, actor, NodeTask, task.ID, NodeScene, sceneID, rel, props, now); err != nil {
			return BindOutboundResult{}, err
		}
	}
	if err := bindEdge(ctx, store, actor, NodeTask, task.ID, NodeIssue, in.IssueID, RelTaskIssue, map[string]any{}, now); err != nil {
		return BindOutboundResult{}, err
	}
	if personKey != "" {
		personProps := map[string]any{"delegated": true}
		if name := strings.TrimSpace(in.DisplayName); name != "" {
			personProps["display_name"] = name
		}
		if err := bindEdge(ctx, store, actor, NodeTask, task.ID, NodePerson, personKey, RelTaskPerson, personProps, now); err != nil {
			return BindOutboundResult{}, err
		}
	}
	if wait := in.WaitingOn; wait != nil && wait.valid() && wait.SceneID != sceneID {
		if err := bindEdge(ctx, store, actor, NodeTask, task.ID, NodeScene, wait.SceneID, RelWaitingOn, sceneProps(*wait), now); err != nil {
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
	result.TaskID = task.ID
	result.IssueID = in.IssueID
	result.Linked = true
	return result, nil
}

// sceneProps are the display facts a scene edge keeps beside its scene_id
// node: the conversation id and kind from the scene directory.
func sceneProps(n SceneNode) map[string]any {
	props := map[string]any{"scene_id": n.SceneID}
	if cid := strings.TrimSpace(n.ConversationID); cid != "" {
		props["conversation_id"] = cid
	}
	if kind := strings.TrimSpace(n.Kind); kind != "" {
		props["kind"] = kind
	}
	return props
}

// validSceneNodeID accepts a scene node id: an agent_scene UUID. Raw
// conversation ids stored as scene nodes before scene ids existed are not
// scene nodes and never match.
func validSceneNodeID(s string) bool {
	_, err := uuid.Parse(strings.TrimSpace(s))
	return err == nil && strings.TrimSpace(s) == s && len(s) == 36
}

func ensureIssueTask(ctx context.Context, store Store, in BindOutboundInput, now time.Time) (Task, error) {
	intent, ok := NormalizeIntent(in.Intent)
	if !ok {
		return Task{}, fmt.Errorf("%w: intent is not a known value", ErrInvalidQuery)
	}
	task, err := store.GetOpenTaskByIssue(ctx, in.WorkspaceID, in.AgentID, in.IssueID)
	if err == nil {
		purpose, perr := ResolvePurpose(in.Purpose, in.IssueTitle, task.Purpose)
		if perr != nil {
			purpose = task.Purpose
		}
		if intent == "" {
			intent = task.Intent
		}
		if purpose != task.Purpose || intent != task.Intent {
			if err := store.UpdateTaskCard(ctx, task.ID, purpose, intent, now); err != nil {
				return Task{}, err
			}
			task.Purpose = purpose
			task.Intent = intent
			task.LastTouchedAt = now
		}
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
		Intent:        intent,
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
