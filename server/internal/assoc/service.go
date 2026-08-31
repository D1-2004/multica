package assoc

import "context"

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

func (s *Service) Store() Store {
	return s.store
}
