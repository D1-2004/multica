package assoc

import (
	"context"
	"fmt"
	"strings"
	"time"
)

type Service struct {
	store Store
}

func NewService(store Store) *Service {
	return &Service{store: store}
}

func (s *Service) Recall(ctx context.Context, q Query) (Result, error) {
	return Recall(ctx, s.store, q)
}

func (s *Service) CreateTask(ctx context.Context, task Task) (Task, error) {
	return s.store.InsertTask(ctx, task)
}

func (s *Service) BindEdge(ctx context.Context, edge Edge) (Edge, error) {
	return s.store.InsertEdge(ctx, edge)
}

func (s *Service) RecordEvent(ctx context.Context, event Event) (Event, error) {
	return s.store.InsertEvent(ctx, event)
}

func (s *Service) BindOutbound(ctx context.Context, in BindOutboundInput) (BindOutboundResult, error) {
	return BindOutbound(ctx, s.store, in)
}

func (s *Service) AssociateIssueConversation(ctx context.Context, in AssociateInput) error {
	return AssociateIssueConversation(ctx, s.store, in)
}

// CloseSceneAssociations closes every open graph link of one scene and
// unlinks its events (/reset-memory).
func (s *Service) CloseSceneAssociations(ctx context.Context, workspaceID, agentID, sceneID string) (CloseSceneResult, error) {
	if strings.TrimSpace(workspaceID) == "" || strings.TrimSpace(agentID) == "" {
		return CloseSceneResult{}, fmt.Errorf("%w: workspace_id and agent_id are required", ErrInvalidQuery)
	}
	sceneID = strings.TrimSpace(sceneID)
	if !validSceneNodeID(sceneID) {
		return CloseSceneResult{}, fmt.Errorf("%w: scene_id is required", ErrInvalidQuery)
	}
	var result CloseSceneResult
	err := withStoreTx(ctx, s.store, func(store Store) error {
		var closeErr error
		result, closeErr = store.CloseSceneAssociations(ctx, workspaceID, agentID, sceneID)
		return closeErr
	})
	return result, err
}

func (s *Service) ListEventsByScene(ctx context.Context, workspaceID, agentID, sceneID string, since time.Time, limit int) ([]Event, error) {
	if strings.TrimSpace(workspaceID) == "" || strings.TrimSpace(agentID) == "" {
		return nil, fmt.Errorf("%w: workspace_id and agent_id are required", ErrInvalidQuery)
	}
	if !validSceneNodeID(strings.TrimSpace(sceneID)) {
		return nil, fmt.Errorf("%w: scene_id is required", ErrInvalidQuery)
	}
	if since.IsZero() {
		return nil, fmt.Errorf("%w: since is required", ErrInvalidQuery)
	}
	return s.store.ListEventsByScene(ctx, workspaceID, agentID, strings.TrimSpace(sceneID), since, limit)
}

func (s *Service) Store() Store {
	return s.store
}

func (s *Service) ListTasksByIssue(ctx context.Context, workspaceID, agentID, issueID string, since, until time.Time) ([]Task, error) {
	if s == nil || s.store == nil {
		return nil, fmt.Errorf("%w: association store is not configured", ErrInvalidQuery)
	}
	issueID = strings.TrimSpace(issueID)
	if strings.TrimSpace(workspaceID) == "" || strings.TrimSpace(agentID) == "" || issueID == "" {
		return nil, fmt.Errorf("%w: workspace_id, agent_id, and issue_id are required", ErrInvalidQuery)
	}
	return s.store.ListTasksByIssue(ctx, workspaceID, agentID, issueID, since, until)
}
