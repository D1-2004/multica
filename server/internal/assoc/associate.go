package assoc

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// AssociateInput links an Issue to the scene it came from. Scene is the
// Host-resolved scene (zero when the source had none); WaitingOn, when set,
// is another resolved scene the Issue waits on.
type AssociateInput struct {
	WorkspaceID   string
	AgentID       string
	IssueID       string
	IssueTitle    string
	RunID         string
	Scene         SceneNode
	EvidenceID    string
	PersonID      string
	PersonAliases []string
	DisplayName   string
	Intent        string
	Purpose       string
	WaitingOn     *SceneNode
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
	sceneID := strings.TrimSpace(in.Scene.SceneID)
	if sceneID != "" && !in.Scene.valid() {
		return fmt.Errorf("%w: scene_id is not a scene id", ErrInvalidQuery)
	}
	now := time.Now().UTC()
	personKey, personAliases := CanonicalPersonKey(append([]string{in.PersonID}, in.PersonAliases...)...)
	task, err := ensureIssueTask(ctx, store, BindOutboundInput{
		WorkspaceID: in.WorkspaceID,
		AgentID:     in.AgentID,
		IssueID:     in.IssueID,
		IssueTitle:  in.IssueTitle,
		Purpose:     in.Purpose,
		RunID:       in.RunID,
		Scene:       in.Scene,
		Intent:      in.Intent,
		DisplayName: in.DisplayName,
		WaitingOn:   in.WaitingOn,
	}, now)
	if err != nil {
		return err
	}
	actor := graphActor{WorkspaceID: in.WorkspaceID, AgentID: in.AgentID, RunID: in.RunID}
	if sceneID != "" {
		props := sceneProps(in.Scene)
		if err := bindEdge(ctx, store, actor, NodeTask, task.ID, NodeScene, sceneID, RelTaskScene, props, now); err != nil {
			return err
		}
		if err := bindEdge(ctx, store, actor, NodeTask, task.ID, NodeScene, sceneID, RelSpawnedFrom, props, now); err != nil {
			return err
		}
	}
	if personKey != "" {
		if err := store.EnsurePerson(ctx, in.WorkspaceID, in.AgentID, personKey, strings.TrimSpace(in.DisplayName), personAliases); err != nil {
			return err
		}
		personProps := map[string]any{}
		if name := strings.TrimSpace(in.DisplayName); name != "" {
			personProps["display_name"] = name
		}
		if err := bindEdge(ctx, store, actor, NodeTask, task.ID, NodePerson, personKey, RelTaskPerson, personProps, now); err != nil {
			return err
		}
	}
	if wait := in.WaitingOn; wait != nil && wait.valid() && wait.SceneID != sceneID {
		if err := bindEdge(ctx, store, actor, NodeTask, task.ID, NodeScene, wait.SceneID, RelWaitingOn, sceneProps(*wait), now); err != nil {
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
