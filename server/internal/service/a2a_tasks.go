package service

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type a2aPublicTaskProjection struct {
	BindingID       pgtype.UUID
	EndpointID      pgtype.UUID
	ClientID        pgtype.UUID
	PublicTaskID    string
	PublicContextID string
	ArtifactID      string
	PublicState     string
	StatusMessage   []byte
	StatusUpdatedAt pgtype.Timestamptz
	CreatedAt       pgtype.Timestamptz
}

// SyncA2ALocalTask is the post-commit lifecycle bridge used by TaskService.
// Non-A2A tasks are intentionally a no-op; callers never need to pre-classify.
func (s *A2AService) SyncA2ALocalTask(ctx context.Context, localTaskID pgtype.UUID) {
	if s == nil || s.Queries == nil || s.TxStarter == nil || !localTaskID.Valid {
		return
	}
	binding, err := s.Queries.GetA2ATaskBindingForLocalTaskV2(ctx, localTaskID)
	if errors.Is(err, pgx.ErrNoRows) {
		return
	}
	if err != nil {
		slog.Warn("load A2A binding for local lifecycle sync failed", "local_task_id", localTaskID, "error", err)
		return
	}
	defer func() {
		// The query clears only terminal local rows. Calling it from every
		// lifecycle notification keeps the security cleanup independent from a
		// projection/event failure without removing a token before execution.
		if clearErr := s.Queries.ClearA2ALocalTaskIdentityContext(context.Background(), localTaskID); clearErr != nil {
			slog.Warn("clear terminal A2A external identity failed", "local_task_id", localTaskID, "error", clearErr)
		}
	}()
	if err = s.Queries.CompleteA2ATaskTurn(ctx, localTaskID); err != nil {
		slog.Warn("mark A2A turn lifecycle complete failed", "local_task_id", localTaskID, "error", err)
	}
	row, err := s.Queries.GetA2APublicTaskForClient(ctx, db.GetA2APublicTaskForClientParams{
		EndpointID:   binding.EndpointID,
		ClientID:     binding.ClientID,
		PublicTaskID: binding.PublicTaskID,
	})
	if err != nil {
		slog.Warn("load A2A public task for lifecycle sync failed", "public_task_id", binding.PublicTaskID, "error", err)
		return
	}
	if _, err = s.reconcileA2ATaskState(ctx, row); err != nil {
		slog.Warn("reconcile A2A lifecycle state failed", "public_task_id", binding.PublicTaskID, "error", err)
		return
	}
	localTask, loadErr := s.Queries.GetAgentTask(ctx, localTaskID)
	if loadErr == nil && (localTask.Status == "completed" || localTask.Status == "failed" || localTask.Status == "cancelled") {
		s.notifyNextA2ATask(ctx, row.ChatSessionID)
	} else if loadErr != nil && !errors.Is(loadErr, pgx.ErrNoRows) {
		slog.Warn("load terminal A2A task before queue wake failed", "local_task_id", localTaskID, "error", loadErr)
	}
}

func projectionFromGet(row db.GetA2APublicTaskForClientRow) a2aPublicTaskProjection {
	return a2aPublicTaskProjection{
		BindingID:       row.ID,
		EndpointID:      row.EndpointID,
		ClientID:        row.ClientID,
		PublicTaskID:    row.PublicTaskID,
		PublicContextID: row.PublicContextID,
		ArtifactID:      row.ArtifactID,
		PublicState:     row.PublicState,
		StatusMessage:   row.StatusMessage,
		StatusUpdatedAt: row.StatusUpdatedAt,
		CreatedAt:       row.CreatedAt,
	}
}

func (s *A2AService) reconcileA2ATaskState(
	ctx context.Context,
	row db.GetA2APublicTaskForClientRow,
) (db.GetA2APublicTaskForClientRow, error) {
	current := a2a.TaskState(row.PublicState)
	if current.Terminal() || current == a2a.TaskStateInputRequired || current == a2a.TaskStateAuthRequired {
		return row, nil
	}
	runtimeState, err := s.Queries.GetA2ATaskRuntimeState(ctx, row.ID)
	if err != nil {
		return row, err
	}
	next := a2a.TaskState(runtimeState)
	if next == current || next == a2a.TaskStateUnspecified {
		return row, nil
	}

	tx, err := s.TxStarter.Begin(ctx)
	if err != nil {
		return row, err
	}
	defer tx.Rollback(ctx)
	qtx := s.Queries.WithTx(tx)
	locked, err := qtx.LockA2ATaskBindingForClient(ctx, db.LockA2ATaskBindingForClientParams{
		EndpointID:   row.EndpointID,
		ClientID:     row.ClientID,
		PublicTaskID: row.PublicTaskID,
	})
	if err != nil {
		return row, err
	}
	lockedState := a2a.TaskState(locked.PublicState)
	if lockedState.Terminal() || lockedState == a2a.TaskStateInputRequired || lockedState == a2a.TaskStateAuthRequired {
		return row, tx.Commit(ctx)
	}
	nextValue, err := qtx.GetA2ATaskRuntimeState(ctx, locked.ID)
	if err != nil {
		return row, err
	}
	next = a2a.TaskState(nextValue)
	if next == a2a.TaskStateCompleted {
		turns, turnErr := qtx.ListA2ATaskTurnsWithOutcome(ctx, locked.ID)
		if turnErr != nil {
			return row, turnErr
		}
		if len(turns) == 0 {
			return row, errors.New("completed A2A task has no turns")
		}
		latest := turns[len(turns)-1]
		storedParts := []storedA2APart{{
			Kind:      "text",
			Text:      latest.AssistantResultText,
			MediaType: a2aTextMIMEType,
		}}
		parts, marshalErr := json.Marshal(storedParts)
		if marshalErr != nil {
			return row, marshalErr
		}
		artifact, artifactErr := qtx.UpsertA2AArtifact(ctx, db.UpsertA2AArtifactParams{
			BindingID:        locked.ID,
			PublicArtifactID: locked.ArtifactID,
			Name:             "result",
			Extensions:       []string{},
			Parts:            parts,
			Append:           false,
			LastChunk:        true,
		})
		if artifactErr != nil {
			return row, artifactErr
		}
		artifactDedupeKey := "artifact:" + locked.ArtifactID + ":final-text"
		artifactPayload, marshalErr := json.Marshal(storedA2AArtifactEvent{
			TaskID:    a2a.TaskID(locked.PublicTaskID),
			ContextID: row.PublicContextID,
			Artifact: storedA2AArtifact{
				ID:    a2a.ArtifactID(artifact.PublicArtifactID),
				Name:  artifact.Name,
				Parts: storedParts,
			},
			LastChunk: true,
			Metadata:  map[string]any{"eventId": stableA2AEventID(locked.PublicTaskID, artifactDedupeKey)},
		})
		if marshalErr != nil {
			return row, marshalErr
		}
		artifactEvent, appendErr := qtx.AppendA2ATaskEvent(ctx, db.AppendA2ATaskEventParams{
			BindingID: locked.ID,
			DedupeKey: artifactDedupeKey,
			EventType: "artifact",
			Payload:   artifactPayload,
		})
		if appendErr != nil {
			return row, appendErr
		}
		if enqueueErr := qtx.EnqueueA2APushDeliveriesForEvent(ctx, artifactEvent.ID); enqueueErr != nil {
			return row, enqueueErr
		}
	}
	updated, err := qtx.UpdateA2ATaskPublicState(ctx, db.UpdateA2ATaskPublicStateParams{
		PublicState: next.String(),
		BindingID:   locked.ID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return row, tx.Commit(ctx)
	}
	if err != nil {
		return row, err
	}
	event, err := appendA2AStatusEvent(
		ctx,
		qtx,
		updated,
		row.PublicContextID,
		next,
		nil,
		"state:"+strings.ToLower(next.String())+":"+strconv.FormatInt(updated.StatusUpdatedAt.Time.UnixNano(), 10),
	)
	if err != nil {
		return row, err
	}
	if err = qtx.EnqueueA2APushDeliveriesForEvent(ctx, event.ID); err != nil {
		return row, err
	}
	if err = tx.Commit(ctx); err != nil {
		return row, err
	}
	if s.PushNotifier != nil {
		s.PushNotifier.NotifyA2APush()
	}
	return s.Queries.GetA2APublicTaskForClient(ctx, db.GetA2APublicTaskForClientParams{
		EndpointID:   row.EndpointID,
		ClientID:     row.ClientID,
		PublicTaskID: row.PublicTaskID,
	})
}

func appendA2AStatusEvent(
	ctx context.Context,
	queries *db.Queries,
	binding db.A2aTaskBinding,
	publicContextID string,
	state a2a.TaskState,
	statusMessage []byte,
	dedupeKey string,
) (db.AppendA2ATaskEventRow, error) {
	var message *a2a.Message
	if len(statusMessage) > 0 {
		if err := json.Unmarshal(statusMessage, &message); err != nil {
			return db.AppendA2ATaskEventRow{}, fmt.Errorf("decode A2A status message: %w", err)
		}
	}
	now := time.Now().UTC()
	event := &a2a.TaskStatusUpdateEvent{
		TaskID:    a2a.TaskID(binding.PublicTaskID),
		ContextID: publicContextID,
		Status: a2a.TaskStatus{
			State:     state,
			Message:   message,
			Timestamp: &now,
		},
		Metadata: map[string]any{"eventId": stableA2AEventID(binding.PublicTaskID, dedupeKey)},
	}
	payload, err := json.Marshal(event)
	if err != nil {
		return db.AppendA2ATaskEventRow{}, err
	}
	return queries.AppendA2ATaskEvent(ctx, db.AppendA2ATaskEventParams{
		BindingID: binding.ID,
		DedupeKey: dedupeKey,
		EventType: "status",
		Payload:   payload,
	})
}

func stableA2AEventID(publicTaskID, dedupeKey string) string {
	digest := sha256.Sum256([]byte(publicTaskID + "\x00" + dedupeKey))
	return "evt_" + hex.EncodeToString(digest[:16])
}

func (s *A2AService) projectA2APublicTask(
	ctx context.Context,
	row db.GetA2APublicTaskForClientRow,
	historyLength int,
	includeArtifacts bool,
) (*a2a.Task, error) {
	projection := projectionFromGet(row)
	state := a2a.TaskState(projection.PublicState)
	if state == a2a.TaskStateUnspecified {
		return nil, fmt.Errorf("A2A task has no public state")
	}
	task := &a2a.Task{
		ID:        a2a.TaskID(projection.PublicTaskID),
		ContextID: projection.PublicContextID,
		Status: a2a.TaskStatus{
			State: state,
		},
	}
	if projection.StatusUpdatedAt.Valid {
		timestamp := projection.StatusUpdatedAt.Time
		task.Status.Timestamp = &timestamp
	}
	if len(projection.StatusMessage) > 0 {
		if err := json.Unmarshal(projection.StatusMessage, &task.Status.Message); err != nil {
			return nil, fmt.Errorf("decode A2A status message: %w", err)
		}
	}
	turns, err := s.Queries.ListA2ATaskTurnsWithOutcome(ctx, projection.BindingID)
	if err != nil {
		return nil, err
	}
	if historyLength > 0 {
		history := make([]*a2a.Message, 0, len(turns)*2)
		for _, turn := range turns {
			parts, decodeErr := decodeStoredA2AParts(ctx, s.Storage, turn.InputParts)
			if decodeErr != nil {
				return nil, decodeErr
			}
			message := &a2a.Message{
				ID:             turn.MessageID,
				ContextID:      projection.PublicContextID,
				TaskID:         a2a.TaskID(projection.PublicTaskID),
				Role:           a2a.MessageRoleUser,
				Parts:          parts,
				Extensions:     turn.MessageExtensions,
				ReferenceTasks: stringTaskIDs(turn.ReferenceTaskIds),
			}
			if len(turn.MessageMetadata) > 0 {
				if err := json.Unmarshal(turn.MessageMetadata, &message.Metadata); err != nil {
					return nil, fmt.Errorf("decode A2A message metadata: %w", err)
				}
			}
			history = append(history, message)
			if turn.AssistantResultText != "" {
				history = append(history, &a2a.Message{
					ID:        stableA2AAgentMessageID(projection.PublicTaskID, turn.Sequence),
					ContextID: projection.PublicContextID,
					TaskID:    a2a.TaskID(projection.PublicTaskID),
					Role:      a2a.MessageRoleAgent,
					Parts:     a2a.ContentParts{a2a.NewTextPart(turn.AssistantResultText)},
				})
			}
		}
		if len(history) > historyLength {
			history = history[len(history)-historyLength:]
		}
		task.History = history
	}
	if includeArtifacts {
		artifacts, err := s.Queries.ListA2AArtifactsForBinding(ctx, projection.BindingID)
		if err != nil {
			return nil, err
		}
		for _, stored := range artifacts {
			parts, decodeErr := decodeStoredA2AParts(ctx, s.Storage, stored.Parts)
			if decodeErr != nil {
				return nil, decodeErr
			}
			artifact := &a2a.Artifact{
				ID:          a2a.ArtifactID(stored.PublicArtifactID),
				Name:        stored.Name,
				Description: stored.Description,
				Extensions:  stored.Extensions,
				Parts:       parts,
			}
			if len(stored.Metadata) > 0 {
				if err := json.Unmarshal(stored.Metadata, &artifact.Metadata); err != nil {
					return nil, err
				}
			}
			task.Artifacts = append(task.Artifacts, artifact)
		}
	}
	return task, nil
}

func stableA2AAgentMessageID(publicTaskID string, sequence int32) string {
	digest := sha256.Sum256([]byte(publicTaskID + "\x00agent\x00" + strconv.Itoa(int(sequence))))
	return "msg_" + hex.EncodeToString(digest[:16])
}

func stringTaskIDs(values []string) []a2a.TaskID {
	result := make([]a2a.TaskID, 0, len(values))
	for _, value := range values {
		result = append(result, a2a.TaskID(value))
	}
	return result
}

type a2aTaskPageCursor struct {
	CreatedAt string `json:"createdAt"`
	TaskID    string `json:"taskId"`
}

func encodeA2ATaskPageCursor(createdAt time.Time, taskID string) string {
	encoded, _ := json.Marshal(a2aTaskPageCursor{
		CreatedAt: createdAt.UTC().Format(time.RFC3339Nano),
		TaskID:    taskID,
	})
	return base64.RawURLEncoding.EncodeToString(encoded)
}

func decodeA2ATaskPageCursor(raw string) (pgtype.Timestamptz, pgtype.Text, error) {
	if raw == "" {
		return pgtype.Timestamptz{}, pgtype.Text{}, nil
	}
	if strings.TrimSpace(raw) != raw || len(raw) > 4096 {
		return pgtype.Timestamptz{}, pgtype.Text{}, errors.New("pageToken is invalid")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return pgtype.Timestamptz{}, pgtype.Text{}, errors.New("pageToken is invalid")
	}
	var cursor a2aTaskPageCursor
	if err := json.Unmarshal(decoded, &cursor); err != nil || strings.TrimSpace(cursor.TaskID) == "" ||
		strings.TrimSpace(cursor.TaskID) != cursor.TaskID || len(cursor.TaskID) > 128 {
		return pgtype.Timestamptz{}, pgtype.Text{}, errors.New("pageToken is invalid")
	}
	createdAt, err := time.Parse(time.RFC3339Nano, cursor.CreatedAt)
	if err != nil {
		return pgtype.Timestamptz{}, pgtype.Text{}, errors.New("pageToken is invalid")
	}
	return pgtype.Timestamptz{Time: createdAt, Valid: true}, pgtype.Text{String: cursor.TaskID, Valid: true}, nil
}

// ListTasks returns a stable cursor page scoped to the authenticated endpoint
// and Client. Resource absence and cross-client access share the same shape.
func (s *A2AService) ListTasks(ctx context.Context, request *a2a.ListTasksRequest) (*a2a.ListTasksResponse, error) {
	if request == nil {
		request = &a2a.ListTasksRequest{}
	}
	_, principal, err := a2aPrincipalFromContext(ctx)
	if err != nil {
		return nil, err
	}
	pageSize := request.PageSize
	if pageSize == 0 {
		pageSize = 50
	}
	if pageSize < 1 || pageSize > 100 {
		return nil, a2a.NewError(a2a.ErrInvalidParams, "pageSize must be between 1 and 100")
	}
	historyLength := 50
	if request.HistoryLength != nil {
		historyLength = *request.HistoryLength
	}
	if historyLength < 0 || historyLength > 100 {
		return nil, a2a.NewError(a2a.ErrInvalidParams, "historyLength must be between 0 and 100")
	}
	if request.Status != a2a.TaskStateUnspecified && !validA2ATaskState(request.Status) {
		return nil, a2a.NewError(a2a.ErrInvalidParams, "status is invalid")
	}
	contextID := request.ContextID
	if strings.TrimSpace(contextID) != contextID || len(contextID) > 256 {
		return nil, a2a.NewError(a2a.ErrInvalidParams, "contextId is invalid")
	}
	beforeCreatedAt, beforeTaskID, err := decodeA2ATaskPageCursor(request.PageToken)
	if err != nil {
		return nil, a2a.NewError(a2a.ErrInvalidParams, err.Error())
	}
	filterContextID := optionalText(contextID)
	filterState := optionalText(string(request.Status))
	filterAfter := optionalTime(request.StatusTimestampAfter)
	totalSize, err := s.Queries.CountA2APublicTasksForClient(ctx, db.CountA2APublicTasksForClientParams{
		EndpointID:           principal.EndpointID,
		ClientID:             principal.ClientID,
		PublicContextID:      filterContextID,
		PublicState:          filterState,
		StatusTimestampAfter: filterAfter,
	})
	if err != nil {
		return nil, a2a.NewError(a2a.ErrInternalError, "unable to count A2A tasks")
	}
	rows, err := s.Queries.ListA2APublicTasksForClient(ctx, db.ListA2APublicTasksForClientParams{
		EndpointID:           principal.EndpointID,
		ClientID:             principal.ClientID,
		PublicContextID:      filterContextID,
		PublicState:          filterState,
		StatusTimestampAfter: filterAfter,
		BeforeCreatedAt:      beforeCreatedAt,
		BeforePublicTaskID:   beforeTaskID,
		PageLimit:            int32(pageSize + 1),
	})
	if err != nil {
		return nil, a2a.NewError(a2a.ErrInternalError, "unable to list A2A tasks")
	}
	response := &a2a.ListTasksResponse{Tasks: []*a2a.Task{}, PageSize: pageSize, TotalSize: int(totalSize)}
	if len(rows) > pageSize {
		last := rows[pageSize-1]
		response.NextPageToken = encodeA2ATaskPageCursor(last.CreatedAt.Time, last.PublicTaskID)
		rows = rows[:pageSize]
	}
	for _, listed := range rows {
		loaded, loadErr := s.GetTask(ctx, &a2a.GetTaskRequest{ID: a2a.TaskID(listed.PublicTaskID), HistoryLength: &historyLength})
		if loadErr != nil {
			return nil, loadErr
		}
		if !request.IncludeArtifacts {
			loaded.Artifacts = nil
		}
		response.Tasks = append(response.Tasks, loaded)
	}
	return response, nil
}

func validA2ATaskState(state a2a.TaskState) bool {
	switch state {
	case a2a.TaskStateAuthRequired, a2a.TaskStateCanceled, a2a.TaskStateCompleted,
		a2a.TaskStateFailed, a2a.TaskStateInputRequired, a2a.TaskStateRejected,
		a2a.TaskStateSubmitted, a2a.TaskStateWorking:
		return true
	default:
		return false
	}
}

func optionalText(value string) pgtype.Text {
	return pgtype.Text{String: value, Valid: value != ""}
}

func optionalTime(value *time.Time) pgtype.Timestamptz {
	if value == nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: *value, Valid: true}
}

// CancelTask makes the public task terminal first, then best-effort interrupts
// every currently active local turn. A terminal public task is never reopened.
func (s *A2AService) CancelTask(ctx context.Context, request *a2a.CancelTaskRequest) (*a2a.Task, error) {
	if s == nil || s.Queries == nil || s.TxStarter == nil || s.TaskService == nil {
		return nil, a2a.NewError(a2a.ErrInternalError, "A2A service is not configured")
	}
	if request == nil || strings.TrimSpace(string(request.ID)) == "" ||
		strings.TrimSpace(string(request.ID)) != string(request.ID) || len(request.ID) > 128 {
		return nil, a2a.NewError(a2a.ErrInvalidParams, "task id is required")
	}
	_, principal, err := a2aPrincipalFromContext(ctx)
	if err != nil {
		return nil, err
	}
	current, err := s.Queries.GetA2APublicTaskForClient(ctx, db.GetA2APublicTaskForClientParams{
		EndpointID: principal.EndpointID, ClientID: principal.ClientID, PublicTaskID: string(request.ID),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, a2a.ErrTaskNotFound
	}
	if err != nil {
		return nil, a2a.NewError(a2a.ErrInternalError, "unable to load A2A task")
	}
	if a2a.TaskState(current.PublicState).Terminal() {
		return nil, a2a.ErrTaskNotCancelable
	}
	tx, err := s.TxStarter.Begin(ctx)
	if err != nil {
		return nil, a2a.NewError(a2a.ErrInternalError, "unable to cancel A2A task")
	}
	defer tx.Rollback(ctx)
	qtx := s.Queries.WithTx(tx)
	binding, err := qtx.LockA2ATaskBindingForClient(ctx, db.LockA2ATaskBindingForClientParams{
		EndpointID: principal.EndpointID, ClientID: principal.ClientID, PublicTaskID: string(request.ID),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, a2a.ErrTaskNotFound
	}
	if err != nil {
		return nil, a2a.NewError(a2a.ErrInternalError, "unable to lock A2A task")
	}
	if a2a.TaskState(binding.PublicState).Terminal() {
		return nil, a2a.ErrTaskNotCancelable
	}
	// Read the active local turns only after locking the public binding. A
	// concurrent SendMessage must take the same lock before inserting its turn,
	// so cancellation cannot miss a just-queued execution.
	active, err := qtx.ListA2AActiveLocalTasksForBinding(ctx, binding.ID)
	if err != nil {
		return nil, a2a.NewError(a2a.ErrInternalError, "unable to load active A2A turns")
	}
	if _, err = qtx.SetA2ATaskCancelRequested(ctx, binding.ID); err != nil {
		return nil, a2a.NewError(a2a.ErrInternalError, "unable to mark A2A task canceled")
	}
	binding, err = qtx.UpdateA2ATaskPublicState(ctx, db.UpdateA2ATaskPublicStateParams{
		PublicState: a2a.TaskStateCanceled.String(), BindingID: binding.ID,
	})
	if err != nil {
		return nil, a2a.ErrTaskNotCancelable
	}
	event, err := appendA2AStatusEvent(ctx, qtx, binding, current.PublicContextID, a2a.TaskStateCanceled, nil, "state:canceled")
	if err != nil {
		return nil, a2a.NewError(a2a.ErrInternalError, "unable to append cancellation event")
	}
	if err = qtx.EnqueueA2APushDeliveriesForEvent(ctx, event.ID); err != nil {
		return nil, a2a.NewError(a2a.ErrInternalError, "unable to enqueue cancellation push")
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, a2a.NewError(a2a.ErrInternalError, "unable to commit A2A cancellation")
	}
	for _, localTask := range active {
		if _, cancelErr := s.TaskService.CancelTask(context.Background(), localTask.ID); cancelErr != nil {
			slog.Warn("A2A local turn interruption failed after public cancellation",
				"public_task_id", binding.PublicTaskID,
				"local_task_id", localTask.ID,
				"error", cancelErr,
			)
		}
	}
	if s.PushNotifier != nil {
		s.PushNotifier.NotifyA2APush()
	}
	s.notifyNextA2ATask(context.Background(), current.ChatSessionID)
	return s.GetTask(ctx, &a2a.GetTaskRequest{ID: request.ID})
}
