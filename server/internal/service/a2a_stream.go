package service

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"strings"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/jackc/pgx/v5"
	a2aintegration "github.com/multica-ai/multica/server/internal/integrations/a2a"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const a2aStreamEventBatchSize = 100

// SendStreamingMessage durably accepts the turn before opening the event
// stream. The first item is a coherent Task snapshot; later items are ordered
// PostgreSQL events for that Task.
func (s *A2AService) SendStreamingMessage(ctx context.Context, request *a2a.SendMessageRequest) iter.Seq2[a2a.Event, error] {
	return func(yield func(a2a.Event, error) bool) {
		if request == nil {
			yield(nil, a2a.NewError(a2a.ErrInvalidParams, "message is required"))
			return
		}
		immediate := *request
		if request.Config == nil {
			immediate.Config = &a2a.SendMessageConfig{}
		} else {
			config := *request.Config
			immediate.Config = &config
		}
		immediate.Config.ReturnImmediately = true
		var requestBoundHolder *a2aRequestBoundTurnClaimHolder
		if identity, ok := a2aintegration.InvocationIdentityFromContext(ctx); ok {
			identity.RequestBound = true
			ctx = a2aintegration.WithInvocationIdentity(ctx, identity)
			if strings.TrimSpace(identity.DEAPDWSToken) != "" {
				ctx, requestBoundHolder = withA2ARequestBoundTurnClaimHolder(ctx)
				defer func() {
					if requestBoundHolder != nil {
						s.releaseA2ARequestBoundTurn(requestBoundHolder.claim)
					}
				}()
			}
		}
		result, err := s.SendMessage(ctx, &immediate)
		if err != nil {
			yield(nil, err)
			return
		}
		task, ok := result.(*a2a.Task)
		if !ok {
			yield(result, nil)
			return
		}
		historyLength := *a2aSendHistoryLength(&immediate)
		snapshot, afterSequence, err := s.a2aStreamSnapshot(ctx, task.ID, historyLength)
		if err != nil {
			yield(nil, err)
			return
		}
		if !yield(snapshot, nil) || snapshot.Status.State.Terminal() {
			return
		}
		s.streamA2AEvents(ctx, snapshot.ID, afterSequence, yield)
	}
}

// SubscribeToTask starts with the current Task and then emits only later
// events. Terminal Tasks reject new subscriptions because their complete
// projection is available through GetTask.
func (s *A2AService) SubscribeToTask(ctx context.Context, request *a2a.SubscribeToTaskRequest) iter.Seq2[a2a.Event, error] {
	return func(yield func(a2a.Event, error) bool) {
		if request == nil || strings.TrimSpace(string(request.ID)) == "" ||
			strings.TrimSpace(string(request.ID)) != string(request.ID) || len(request.ID) > 128 {
			yield(nil, a2a.NewError(a2a.ErrInvalidParams, "task id is required"))
			return
		}
		task, afterSequence, err := s.a2aStreamSnapshot(ctx, request.ID, 50)
		if err != nil {
			yield(nil, err)
			return
		}
		if task.Status.State.Terminal() {
			yield(nil, a2a.NewError(a2a.ErrInvalidParams, "terminal tasks cannot be subscribed"))
			return
		}
		if !yield(task, nil) {
			return
		}
		s.streamA2AEvents(ctx, task.ID, afterSequence, yield)
	}
}

func (s *A2AService) a2aStreamSnapshot(ctx context.Context, taskID a2a.TaskID, historyLength int) (*a2a.Task, int64, error) {
	_, principal, err := a2aPrincipalFromContext(ctx)
	if err != nil {
		return nil, 0, err
	}
	row, err := s.Queries.GetA2APublicTaskForClient(ctx, db.GetA2APublicTaskForClientParams{
		EndpointID:   principal.EndpointID,
		ClientID:     principal.ClientID,
		PublicTaskID: string(taskID),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, 0, a2a.ErrTaskNotFound
	}
	if err != nil {
		return nil, 0, a2a.NewError(a2a.ErrInternalError, "unable to load A2A task")
	}
	row, err = s.reconcileA2ATaskState(ctx, row)
	if err != nil {
		return nil, 0, a2a.NewError(a2a.ErrInternalError, "unable to reconcile A2A task")
	}
	task, err := s.projectA2APublicTask(ctx, row, historyLength, true)
	if err != nil {
		return nil, 0, a2a.NewError(a2a.ErrInternalError, "unable to project A2A task")
	}
	afterSequence := row.NextEventSequence - 1
	if afterSequence < 0 {
		afterSequence = 0
	}
	return task, afterSequence, nil
}

func (s *A2AService) streamA2AEvents(
	ctx context.Context,
	taskID a2a.TaskID,
	afterSequence int64,
	yield func(a2a.Event, error) bool,
) {
	_, principal, err := a2aPrincipalFromContext(ctx)
	if err != nil {
		yield(nil, err)
		return
	}
	ticker := time.NewTicker(a2aBlockingPollInterval)
	defer ticker.Stop()
	for {
		s.maintainA2ARequestBoundTurn(ctx)
		// Reconciliation turns local runtime changes into durable public events.
		current, loadErr := s.GetTask(ctx, &a2a.GetTaskRequest{ID: taskID})
		if loadErr != nil {
			yield(nil, loadErr)
			return
		}
		events, listErr := s.Queries.ListA2ATaskEventsAfter(ctx, db.ListA2ATaskEventsAfterParams{
			EndpointID:    principal.EndpointID,
			ClientID:      principal.ClientID,
			PublicTaskID:  string(taskID),
			AfterSequence: afterSequence,
			EventLimit:    a2aStreamEventBatchSize,
		})
		if listErr != nil {
			yield(nil, a2a.NewError(a2a.ErrInternalError, "unable to load A2A task events"))
			return
		}
		for _, stored := range events {
			event, decodeErr := s.decodeA2ATaskEvent(ctx, stored)
			if decodeErr != nil {
				yield(nil, a2a.NewError(a2a.ErrInternalError, "unable to decode A2A task event"))
				return
			}
			afterSequence = stored.Sequence
			if !yield(event, nil) {
				return
			}
			if status, ok := event.(*a2a.TaskStatusUpdateEvent); ok && status.Status.State.Terminal() {
				return
			}
		}
		if current.Status.State.Terminal() && len(events) == 0 {
			return
		}
		if len(events) == a2aStreamEventBatchSize {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *A2AService) decodeA2ATaskEvent(ctx context.Context, stored db.A2aTaskEvent) (a2a.Event, error) {
	switch stored.EventType {
	case "status":
		var event a2a.TaskStatusUpdateEvent
		if err := json.Unmarshal(stored.Payload, &event); err != nil {
			return nil, err
		}
		return &event, nil
	case "artifact":
		var persisted storedA2AArtifactEvent
		if err := json.Unmarshal(stored.Payload, &persisted); err != nil {
			return nil, err
		}
		encodedParts, err := json.Marshal(persisted.Artifact.Parts)
		if err != nil {
			return nil, err
		}
		parts, err := decodeStoredA2AParts(ctx, s.Storage, encodedParts)
		if err != nil {
			return nil, err
		}
		return &a2a.TaskArtifactUpdateEvent{
			TaskID:    persisted.TaskID,
			ContextID: persisted.ContextID,
			Artifact: &a2a.Artifact{
				ID:          persisted.Artifact.ID,
				Name:        persisted.Artifact.Name,
				Description: persisted.Artifact.Description,
				Extensions:  persisted.Artifact.Extensions,
				Metadata:    persisted.Artifact.Metadata,
				Parts:       parts,
			},
			Append:    persisted.Append,
			LastChunk: persisted.LastChunk,
			Metadata:  persisted.Metadata,
		}, nil
	default:
		return nil, errors.New("unsupported A2A task event type")
	}
}
