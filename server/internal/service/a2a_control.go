package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	A2ATaskControlRequestInput    = "request_input"
	A2ATaskControlRequestAuth     = "request_auth"
	A2ATaskControlPublishArtifact = "publish_artifact"
	a2aMaxControlMetadataBytes    = 64 << 10
)

var (
	ErrA2ATaskControlInvalid  = errors.New("invalid A2A task control request")
	ErrA2ATaskControlNotFound = errors.New("A2A task control target not found")
	ErrA2ATaskControlConflict = errors.New("A2A task control conflict")
)

// A2ATaskControlRequest is accepted only through the daemon-authenticated
// task-control route. The loopback MCP capability binds the caller to the
// local task ID, so no protocol identifier is accepted from the Agent.
type A2ATaskControlRequest struct {
	Action      string           `json:"action"`
	Parts       a2a.ContentParts `json:"parts,omitempty"`
	Schema      any              `json:"schema,omitempty"`
	AuthRequest any              `json:"auth_request,omitempty"`
	ArtifactID  string           `json:"artifact_id,omitempty"`
	Name        string           `json:"name,omitempty"`
	Description string           `json:"description,omitempty"`
	Extensions  []string         `json:"extensions,omitempty"`
	Metadata    map[string]any   `json:"metadata,omitempty"`
	Append      bool             `json:"append,omitempty"`
	LastChunk   *bool            `json:"last_chunk,omitempty"`
}

type A2ATaskControlResponse struct {
	State      a2a.TaskState `json:"state,omitempty"`
	ArtifactID string        `json:"artifact_id,omitempty"`
}

type storedA2AArtifactEvent struct {
	TaskID    a2a.TaskID        `json:"taskId"`
	ContextID string            `json:"contextId"`
	Artifact  storedA2AArtifact `json:"artifact"`
	Append    bool              `json:"append,omitempty"`
	LastChunk bool              `json:"lastChunk,omitempty"`
	Metadata  map[string]any    `json:"metadata,omitempty"`
}

type storedA2AArtifact struct {
	ID          a2a.ArtifactID  `json:"artifactId"`
	Name        string          `json:"name,omitempty"`
	Description string          `json:"description,omitempty"`
	Extensions  []string        `json:"extensions,omitempty"`
	Metadata    map[string]any  `json:"metadata,omitempty"`
	Parts       []storedA2APart `json:"parts"`
}

func (s *A2AService) ControlA2ATask(ctx context.Context, localTaskID pgtype.UUID, request A2ATaskControlRequest) (A2ATaskControlResponse, error) {
	if s == nil || s.Queries == nil || s.TxStarter == nil || !localTaskID.Valid {
		return A2ATaskControlResponse{}, errors.New("A2A task control is not configured")
	}
	switch strings.TrimSpace(request.Action) {
	case A2ATaskControlRequestInput:
		return s.pauseA2ATask(ctx, localTaskID, request, a2a.TaskStateInputRequired)
	case A2ATaskControlRequestAuth:
		return s.pauseA2ATask(ctx, localTaskID, request, a2a.TaskStateAuthRequired)
	case A2ATaskControlPublishArtifact:
		return s.publishA2AArtifact(ctx, localTaskID, request)
	default:
		return A2ATaskControlResponse{}, fmt.Errorf("%w: unsupported action", ErrA2ATaskControlInvalid)
	}
}

func validateA2AControlPromptParts(parts a2a.ContentParts) error {
	if err := validateA2AParts(parts); err != nil {
		return err
	}
	for _, part := range parts {
		switch part.Content.(type) {
		case a2a.Text, a2a.Data:
		default:
			return errors.New("request_input and request_auth support only text and data parts")
		}
	}
	return nil
}

func validateA2AControlMetadata(value any, rejectSecrets bool) error {
	if value == nil {
		return nil
	}
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) > a2aMaxControlMetadataBytes {
		return errors.New("control metadata must be valid JSON no larger than 64 KiB")
	}
	if !rejectSecrets {
		return nil
	}
	var inspect func(any) bool
	inspect = func(candidate any) bool {
		switch typed := candidate.(type) {
		case map[string]any:
			for key, child := range typed {
				normalized := strings.ToLower(strings.TrimSpace(key))
				for _, fragment := range []string{"token", "secret", "password", "credential"} {
					if strings.Contains(normalized, fragment) {
						return false
					}
				}
				if !inspect(child) {
					return false
				}
			}
		case []any:
			for _, child := range typed {
				if !inspect(child) {
					return false
				}
			}
		}
		return true
	}
	if !inspect(value) {
		return errors.New("auth_request must describe authentication without containing credentials")
	}
	return nil
}

func (s *A2AService) pauseA2ATask(
	ctx context.Context,
	localTaskID pgtype.UUID,
	request A2ATaskControlRequest,
	state a2a.TaskState,
) (A2ATaskControlResponse, error) {
	if err := validateA2AControlPromptParts(request.Parts); err != nil {
		return A2ATaskControlResponse{}, fmt.Errorf("%w: %v", ErrA2ATaskControlInvalid, err)
	}
	if state == a2a.TaskStateInputRequired {
		if request.Schema != nil {
			if _, ok := request.Schema.(map[string]any); !ok {
				return A2ATaskControlResponse{}, fmt.Errorf("%w: schema must be a JSON object", ErrA2ATaskControlInvalid)
			}
		}
		if err := validateA2AControlMetadata(request.Schema, false); err != nil {
			return A2ATaskControlResponse{}, fmt.Errorf("%w: %v", ErrA2ATaskControlInvalid, err)
		}
	} else {
		if _, ok := request.AuthRequest.(map[string]any); !ok {
			return A2ATaskControlResponse{}, fmt.Errorf("%w: auth_request must be a JSON object", ErrA2ATaskControlInvalid)
		}
		if err := validateA2AControlMetadata(request.AuthRequest, true); err != nil {
			return A2ATaskControlResponse{}, fmt.Errorf("%w: %v", ErrA2ATaskControlInvalid, err)
		}
	}

	tx, err := s.TxStarter.Begin(ctx)
	if err != nil {
		return A2ATaskControlResponse{}, errors.New("begin A2A control transaction")
	}
	defer tx.Rollback(ctx)
	qtx := s.Queries.WithTx(tx)
	binding, err := qtx.GetA2ATaskBindingForLocalTaskV2(ctx, localTaskID)
	if errors.Is(err, pgx.ErrNoRows) {
		return A2ATaskControlResponse{}, ErrA2ATaskControlNotFound
	}
	if err != nil {
		return A2ATaskControlResponse{}, errors.New("load A2A task control target")
	}
	locked, err := qtx.LockA2ATaskBindingForClient(ctx, db.LockA2ATaskBindingForClientParams{
		EndpointID:   binding.EndpointID,
		ClientID:     binding.ClientID,
		PublicTaskID: binding.PublicTaskID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return A2ATaskControlResponse{}, ErrA2ATaskControlNotFound
	}
	if err != nil {
		return A2ATaskControlResponse{}, errors.New("lock A2A task control target")
	}
	if a2a.TaskState(locked.PublicState).Terminal() {
		return A2ATaskControlResponse{}, fmt.Errorf("%w: A2A task is no longer active", ErrA2ATaskControlConflict)
	}
	if _, err = qtx.LockRunningA2ALocalTask(ctx, localTaskID); errors.Is(err, pgx.ErrNoRows) {
		return A2ATaskControlResponse{}, fmt.Errorf("%w: local A2A turn is not running", ErrA2ATaskControlConflict)
	} else if err != nil {
		return A2ATaskControlResponse{}, errors.New("lock local A2A turn")
	}
	controlPayload, err := json.Marshal(request)
	if err != nil {
		return A2ATaskControlResponse{}, errors.New("encode A2A control request")
	}
	controlSignal := "input_required"
	if state == a2a.TaskStateAuthRequired {
		controlSignal = "auth_required"
	}
	turn, err := qtx.SetA2ATurnControlSignal(ctx, db.SetA2ATurnControlSignalParams{
		ControlSignal:  pgtype.Text{String: controlSignal, Valid: true},
		ControlPayload: controlPayload,
		LocalTaskID:    localTaskID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return A2ATaskControlResponse{}, fmt.Errorf("%w: this turn already accepted a pause or terminal signal", ErrA2ATaskControlConflict)
	}
	if err != nil {
		return A2ATaskControlResponse{}, errors.New("persist A2A control signal")
	}
	contextRow, err := qtx.GetA2AContextByIDForClient(ctx, db.GetA2AContextByIDForClientParams{
		ID: locked.ContextID, EndpointID: locked.EndpointID, ClientID: locked.ClientID,
	})
	if err != nil {
		return A2ATaskControlResponse{}, errors.New("load A2A context")
	}
	message := &a2a.Message{
		ID:        stableA2AAgentMessageID(locked.PublicTaskID, turn.Sequence),
		Role:      a2a.MessageRoleAgent,
		TaskID:    a2a.TaskID(locked.PublicTaskID),
		ContextID: contextRow.PublicContextID,
		Parts:     request.Parts,
		Metadata:  map[string]any{},
	}
	if state == a2a.TaskStateInputRequired && request.Schema != nil {
		message.Metadata["urn:multica:a2a:input-schema:v1"] = request.Schema
	}
	if state == a2a.TaskStateAuthRequired && request.AuthRequest != nil {
		message.Metadata["urn:multica:a2a:auth-request:v1"] = request.AuthRequest
	}
	statusMessage, err := json.Marshal(message)
	if err != nil {
		return A2ATaskControlResponse{}, errors.New("encode A2A status message")
	}
	locked, err = qtx.UpdateA2ATaskPublicState(ctx, db.UpdateA2ATaskPublicStateParams{
		PublicState:   state.String(),
		StatusMessage: statusMessage,
		BindingID:     locked.ID,
	})
	if err != nil {
		return A2ATaskControlResponse{}, errors.New("update A2A task waiting state")
	}
	event, err := appendA2AStatusEvent(ctx, qtx, locked, contextRow.PublicContextID, state, statusMessage, "turn:"+fmt.Sprint(turn.Sequence)+":"+strings.ToLower(state.String()))
	if err != nil {
		return A2ATaskControlResponse{}, errors.New("append A2A waiting event")
	}
	if _, err = qtx.CancelAgentTask(ctx, localTaskID); err != nil {
		return A2ATaskControlResponse{}, errors.New("pause local A2A execution")
	}
	if err = qtx.ClearA2ALocalTaskIdentityContext(ctx, localTaskID); err != nil {
		return A2ATaskControlResponse{}, errors.New("clear external identity context")
	}
	if err = qtx.EnqueueA2APushDeliveriesForEvent(ctx, event.ID); err != nil {
		return A2ATaskControlResponse{}, errors.New("enqueue A2A waiting push")
	}
	if err = tx.Commit(ctx); err != nil {
		return A2ATaskControlResponse{}, errors.New("commit A2A waiting state")
	}
	if s.PushNotifier != nil {
		s.PushNotifier.NotifyA2APush()
	}
	return A2ATaskControlResponse{State: state}, nil
}

func (s *A2AService) publishA2AArtifact(
	ctx context.Context,
	localTaskID pgtype.UUID,
	request A2ATaskControlRequest,
) (A2ATaskControlResponse, error) {
	if strings.TrimSpace(request.Name) != request.Name || utf8.RuneCountInString(request.Name) > 256 ||
		strings.TrimSpace(request.Description) != request.Description || utf8.RuneCountInString(request.Description) > 2048 {
		return A2ATaskControlResponse{}, fmt.Errorf("%w: artifact name or description is invalid", ErrA2ATaskControlInvalid)
	}
	if err := validateA2AControlMetadata(request.Metadata, false); err != nil {
		return A2ATaskControlResponse{}, fmt.Errorf("%w: %v", ErrA2ATaskControlInvalid, err)
	}
	extensions, err := normalizeA2AExtensions(request.Extensions, "artifact extensions")
	if err != nil {
		return A2ATaskControlResponse{}, fmt.Errorf("%w: %v", ErrA2ATaskControlInvalid, err)
	}
	binding, err := s.Queries.GetA2ATaskBindingForLocalTaskV2(ctx, localTaskID)
	if errors.Is(err, pgx.ErrNoRows) {
		return A2ATaskControlResponse{}, ErrA2ATaskControlNotFound
	}
	if err != nil {
		return A2ATaskControlResponse{}, errors.New("load A2A artifact target")
	}
	localTask, err := s.Queries.GetAgentTask(ctx, localTaskID)
	if err != nil || (localTask.Status != "dispatched" && localTask.Status != "running") {
		return A2ATaskControlResponse{}, fmt.Errorf("%w: local A2A turn is not running", ErrA2ATaskControlConflict)
	}
	artifactID := strings.TrimSpace(request.ArtifactID)
	if artifactID == "" {
		artifactID, err = newA2APublicID("art_")
		if err != nil {
			return A2ATaskControlResponse{}, errors.New("allocate A2A artifact id")
		}
	}
	if (artifactID != request.ArtifactID && request.ArtifactID != "") || utf8.RuneCountInString(artifactID) > 128 {
		return A2ATaskControlResponse{}, fmt.Errorf("%w: artifact id is invalid", ErrA2ATaskControlInvalid)
	}
	if err := validateA2AParts(request.Parts); err != nil {
		return A2ATaskControlResponse{}, fmt.Errorf("%w: %v", ErrA2ATaskControlInvalid, err)
	}
	prefix := "a2a/artifacts/" + uuidStringOrOpaque(binding.PublicTaskID) + "/" + uuidStringOrOpaque(artifactID)
	parts, err := materializeA2AParts(ctx, s.Storage, s.HTTPClient, prefix, request.Parts)
	if err != nil {
		return A2ATaskControlResponse{}, err
	}
	committed := false
	defer func() {
		if !committed {
			deleteMaterializedA2AParts(s.Storage, parts)
		}
	}()
	storedParts := make([]storedA2APart, 0, len(parts))
	for _, part := range parts {
		storedParts = append(storedParts, part.Stored)
	}
	partsJSON, err := json.Marshal(storedParts)
	if err != nil {
		return A2ATaskControlResponse{}, errors.New("encode A2A artifact parts")
	}
	metadataJSON, err := json.Marshal(request.Metadata)
	if err != nil {
		return A2ATaskControlResponse{}, errors.New("encode A2A artifact metadata")
	}
	tx, err := s.TxStarter.Begin(ctx)
	if err != nil {
		return A2ATaskControlResponse{}, errors.New("begin A2A artifact transaction")
	}
	defer tx.Rollback(ctx)
	qtx := s.Queries.WithTx(tx)
	locked, err := qtx.LockA2ATaskBindingForClient(ctx, db.LockA2ATaskBindingForClientParams{
		EndpointID: binding.EndpointID, ClientID: binding.ClientID, PublicTaskID: binding.PublicTaskID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return A2ATaskControlResponse{}, ErrA2ATaskControlNotFound
	}
	if err != nil {
		return A2ATaskControlResponse{}, errors.New("lock A2A artifact target")
	}
	if a2a.TaskState(locked.PublicState).Terminal() || a2a.TaskState(locked.PublicState) == a2a.TaskStateInputRequired || a2a.TaskState(locked.PublicState) == a2a.TaskStateAuthRequired {
		return A2ATaskControlResponse{}, fmt.Errorf("%w: A2A task is not accepting artifacts", ErrA2ATaskControlConflict)
	}
	localTask, err = qtx.LockRunningA2ALocalTask(ctx, localTaskID)
	if errors.Is(err, pgx.ErrNoRows) {
		return A2ATaskControlResponse{}, fmt.Errorf("%w: local A2A turn is not running", ErrA2ATaskControlConflict)
	}
	if err != nil {
		return A2ATaskControlResponse{}, errors.New("lock local A2A turn")
	}
	lastChunk := true
	if request.LastChunk != nil {
		lastChunk = *request.LastChunk
	}
	var replacedObjectKeys []string
	existing, existingErr := qtx.GetA2AArtifactForBinding(ctx, db.GetA2AArtifactForBindingParams{
		BindingID: locked.ID, PublicArtifactID: artifactID,
	})
	switch {
	case existingErr == nil && existing.LastChunk:
		return A2ATaskControlResponse{}, fmt.Errorf("%w: finalized artifact cannot be changed", ErrA2ATaskControlConflict)
	case errors.Is(existingErr, pgx.ErrNoRows) && request.Append:
		return A2ATaskControlResponse{}, fmt.Errorf("%w: append requires an existing artifact", ErrA2ATaskControlConflict)
	case existingErr != nil && !errors.Is(existingErr, pgx.ErrNoRows):
		return A2ATaskControlResponse{}, errors.New("load existing A2A artifact")
	case existingErr == nil && !request.Append:
		var previous []storedA2APart
		if unmarshalErr := json.Unmarshal(existing.Parts, &previous); unmarshalErr != nil {
			return A2ATaskControlResponse{}, errors.New("decode existing A2A artifact")
		}
		for _, part := range previous {
			if part.ObjectKey != "" {
				replacedObjectKeys = append(replacedObjectKeys, part.ObjectKey)
			}
		}
	}
	artifact, err := qtx.UpsertA2AArtifact(ctx, db.UpsertA2AArtifactParams{
		BindingID:        locked.ID,
		PublicArtifactID: artifactID,
		Name:             request.Name,
		Description:      request.Description,
		Extensions:       extensions,
		Metadata:         metadataJSON,
		Parts:            partsJSON,
		Append:           request.Append,
		LastChunk:        lastChunk,
	})
	if err != nil {
		return A2ATaskControlResponse{}, errors.New("persist A2A artifact")
	}
	contextRow, err := qtx.GetA2AContextByIDForClient(ctx, db.GetA2AContextByIDForClientParams{
		ID: locked.ContextID, EndpointID: locked.EndpointID, ClientID: locked.ClientID,
	})
	if err != nil {
		return A2ATaskControlResponse{}, errors.New("load A2A artifact context")
	}
	eventID, err := uuid.NewV7()
	if err != nil {
		return A2ATaskControlResponse{}, errors.New("allocate A2A artifact event")
	}
	dedupeKey := "artifact:" + artifactID + ":" + eventID.String()
	eventPayload, err := json.Marshal(storedA2AArtifactEvent{
		TaskID:    a2a.TaskID(locked.PublicTaskID),
		ContextID: contextRow.PublicContextID,
		Artifact: storedA2AArtifact{
			ID:          a2a.ArtifactID(artifact.PublicArtifactID),
			Name:        artifact.Name,
			Description: artifact.Description,
			Extensions:  artifact.Extensions,
			Metadata:    request.Metadata,
			Parts:       storedParts,
		},
		Append:    request.Append,
		LastChunk: lastChunk,
		Metadata:  map[string]any{"eventId": stableA2AEventID(locked.PublicTaskID, dedupeKey)},
	})
	if err != nil {
		return A2ATaskControlResponse{}, errors.New("encode A2A artifact event")
	}
	event, err := qtx.AppendA2ATaskEvent(ctx, db.AppendA2ATaskEventParams{
		BindingID: locked.ID,
		DedupeKey: dedupeKey,
		EventType: "artifact",
		Payload:   eventPayload,
	})
	if err != nil {
		return A2ATaskControlResponse{}, errors.New("append A2A artifact event")
	}
	if err = qtx.EnqueueA2APushDeliveriesForEvent(ctx, event.ID); err != nil {
		return A2ATaskControlResponse{}, errors.New("enqueue A2A artifact push")
	}
	if err = tx.Commit(ctx); err != nil {
		return A2ATaskControlResponse{}, errors.New("commit A2A artifact")
	}
	committed = true
	if len(replacedObjectKeys) > 0 && s.Storage != nil {
		s.Storage.DeleteKeys(context.Background(), replacedObjectKeys)
	}
	if s.PushNotifier != nil {
		s.PushNotifier.NotifyA2APush()
	}
	return A2ATaskControlResponse{ArtifactID: artifactID}, nil
}
