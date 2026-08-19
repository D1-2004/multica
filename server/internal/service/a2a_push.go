package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	a2aDefaultPushPageSize = 50
	a2aMaxPushPageSize     = 100
	a2aMaxPushSecretBytes  = 16 << 10
	a2aPushResponseLimit   = 1 << 20
	a2aMaxPushURLBytes     = 2048
)

type a2aPushPageCursor struct {
	CreatedAt string `json:"createdAt"`
	ConfigID  string `json:"configId"`
}

func encodeA2APushPageCursor(createdAt time.Time, configID string) string {
	encoded, _ := json.Marshal(a2aPushPageCursor{
		CreatedAt: createdAt.UTC().Format(time.RFC3339Nano),
		ConfigID:  configID,
	})
	return base64.RawURLEncoding.EncodeToString(encoded)
}

func decodeA2APushPageCursor(raw string) (pgtype.Timestamptz, pgtype.Text, error) {
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
	var cursor a2aPushPageCursor
	if err = json.Unmarshal(decoded, &cursor); err != nil || strings.TrimSpace(cursor.ConfigID) == "" ||
		strings.TrimSpace(cursor.ConfigID) != cursor.ConfigID || len(cursor.ConfigID) > 128 {
		return pgtype.Timestamptz{}, pgtype.Text{}, errors.New("pageToken is invalid")
	}
	createdAt, err := time.Parse(time.RFC3339Nano, cursor.CreatedAt)
	if err != nil {
		return pgtype.Timestamptz{}, pgtype.Text{}, errors.New("pageToken is invalid")
	}
	return pgtype.Timestamptz{Time: createdAt, Valid: true}, pgtype.Text{String: cursor.ConfigID, Valid: true}, nil
}

func validateA2APushConfig(config *a2a.PushConfig, taskID string) error {
	if config == nil {
		return a2a.NewError(a2a.ErrInvalidParams, "push notification config is required")
	}
	if strings.TrimSpace(config.Tenant) != "" {
		return a2a.NewError(a2a.ErrInvalidParams, "tenant is not supported by this endpoint")
	}
	if strings.TrimSpace(taskID) == "" || strings.TrimSpace(taskID) != taskID || len(taskID) > 128 {
		return a2a.NewError(a2a.ErrInvalidParams, "taskId is required")
	}
	if config.TaskID != "" && string(config.TaskID) != taskID {
		return a2a.NewError(a2a.ErrInvalidParams, "push notification taskId does not match the task")
	}
	if string(config.TaskID) != strings.TrimSpace(string(config.TaskID)) || len(config.TaskID) > 128 {
		return a2a.NewError(a2a.ErrInvalidParams, "push notification taskId is invalid")
	}
	if len(config.URL) > a2aMaxPushURLBytes || strings.ContainsAny(config.URL, "\r\n") {
		return a2a.NewError(a2a.ErrInvalidParams, "push notification URL is invalid")
	}
	if err := validateA2APublicHTTPSURL(config.URL); err != nil {
		return a2a.NewError(a2a.ErrInvalidParams, "push notification URL must be a public HTTPS URL")
	}
	if len(config.Token) > a2aMaxPushSecretBytes || strings.ContainsAny(config.Token, "\r\n") {
		return a2a.NewError(a2a.ErrInvalidParams, "push notification token is too large")
	}
	if strings.TrimSpace(config.ID) != config.ID || len(config.ID) > 128 {
		return a2a.NewError(a2a.ErrInvalidParams, "push notification config id is too large")
	}
	if config.Auth == nil {
		return nil
	}
	if strings.TrimSpace(config.Auth.Scheme) != config.Auth.Scheme || strings.ContainsAny(config.Auth.Scheme, "\r\n") {
		return a2a.NewError(a2a.ErrInvalidParams, "push notification authentication scheme is invalid")
	}
	scheme := strings.ToLower(config.Auth.Scheme)
	if scheme != "bearer" && scheme != "basic" {
		return a2a.NewError(a2a.ErrInvalidParams, "push notification authentication must use Bearer or Basic")
	}
	if strings.TrimSpace(config.Auth.Credentials) == "" || strings.ContainsAny(config.Auth.Credentials, "\r\n") {
		return a2a.NewError(a2a.ErrInvalidParams, "push notification authentication credentials are required")
	}
	if len(config.Auth.Credentials) > a2aMaxPushSecretBytes {
		return a2a.NewError(a2a.ErrInvalidParams, "push notification authentication credentials are too large")
	}
	return nil
}

func (s *A2AService) createTaskPushConfigInTx(
	ctx context.Context,
	queries *db.Queries,
	principal a2aPrincipalIDs,
	binding db.A2aTaskBinding,
	config *a2a.PushConfig,
) (*a2a.PushConfig, error) {
	if s.PushSecrets == nil {
		return nil, a2a.ErrPushNotificationNotSupported
	}
	if err := validateA2APushConfig(config, binding.PublicTaskID); err != nil {
		return nil, err
	}
	configID := strings.TrimSpace(config.ID)
	if configID == "" {
		var err error
		configID, err = newA2APublicID("push_")
		if err != nil {
			return nil, a2a.NewError(a2a.ErrInternalError, "unable to allocate push notification config id")
		}
	}
	var encryptedToken []byte
	var encryptedCredentials []byte
	var authScheme pgtype.Text
	var err error
	if config.Token != "" {
		encryptedToken, err = s.PushSecrets.Seal([]byte(config.Token))
		if err != nil {
			return nil, a2a.NewError(a2a.ErrInternalError, "unable to protect push notification token")
		}
	}
	if config.Auth != nil {
		authScheme = pgtype.Text{String: strings.ToLower(strings.TrimSpace(config.Auth.Scheme)), Valid: true}
		encryptedCredentials, err = s.PushSecrets.Seal([]byte(config.Auth.Credentials))
		if err != nil {
			return nil, a2a.NewError(a2a.ErrInternalError, "unable to protect push notification credentials")
		}
	}
	stored, err := queries.CreateA2APushConfig(ctx, db.CreateA2APushConfigParams{
		PublicConfigID:             configID,
		CallbackUrl:                strings.TrimSpace(config.URL),
		NotificationTokenEncrypted: encryptedToken,
		AuthScheme:                 authScheme,
		AuthCredentialsEncrypted:   encryptedCredentials,
		BindingID:                  binding.ID,
		EndpointID:                 principal.EndpointID,
		ClientID:                   principal.ClientID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, a2a.ErrTaskNotFound
	}
	if err != nil {
		return nil, a2a.NewError(a2a.ErrInternalError, "unable to save push notification config")
	}
	return publicA2APushConfig(binding.PublicTaskID, stored), nil
}

func publicA2APushConfig(taskID string, config db.A2aPushConfig) *a2a.PushConfig {
	result := &a2a.PushConfig{
		TaskID: a2a.TaskID(taskID),
		ID:     config.PublicConfigID,
		URL:    config.CallbackUrl,
	}
	if config.AuthScheme.Valid {
		// Stored secrets are deliberately never returned through protocol reads.
		result.Auth = &a2a.PushAuthInfo{Scheme: config.AuthScheme.String}
	}
	return result
}

func (s *A2AService) CreateTaskPushConfig(ctx context.Context, config *a2a.PushConfig) (*a2a.PushConfig, error) {
	if s == nil || s.Queries == nil || s.PushSecrets == nil {
		return nil, a2a.ErrPushNotificationNotSupported
	}
	if config == nil {
		return nil, a2a.NewError(a2a.ErrInvalidParams, "push notification config is required")
	}
	_, principal, err := a2aPrincipalFromContext(ctx)
	if err != nil {
		return nil, err
	}
	taskID := strings.TrimSpace(string(config.TaskID))
	if err = validateA2APushConfig(config, taskID); err != nil {
		return nil, err
	}
	binding, err := s.Queries.GetA2ATaskBindingForClient(ctx, db.GetA2ATaskBindingForClientParams{
		EndpointID:   principal.EndpointID,
		ClientID:     principal.ClientID,
		PublicTaskID: taskID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, a2a.ErrTaskNotFound
	}
	if err != nil {
		return nil, a2a.NewError(a2a.ErrInternalError, "unable to load A2A task")
	}
	return s.createTaskPushConfigInTx(ctx, s.Queries, principal, binding, config)
}

func (s *A2AService) GetTaskPushConfig(ctx context.Context, request *a2a.GetTaskPushConfigRequest) (*a2a.PushConfig, error) {
	if s == nil || s.Queries == nil || s.PushSecrets == nil {
		return nil, a2a.ErrPushNotificationNotSupported
	}
	if request == nil || strings.TrimSpace(string(request.TaskID)) == "" || strings.TrimSpace(request.ID) == "" ||
		strings.TrimSpace(string(request.TaskID)) != string(request.TaskID) || len(request.TaskID) > 128 ||
		strings.TrimSpace(request.ID) != request.ID || len(request.ID) > 128 {
		return nil, a2a.NewError(a2a.ErrInvalidParams, "taskId and id are required")
	}
	_, principal, err := a2aPrincipalFromContext(ctx)
	if err != nil {
		return nil, err
	}
	stored, err := s.Queries.GetA2APushConfigForClient(ctx, db.GetA2APushConfigForClientParams{
		EndpointID:     principal.EndpointID,
		ClientID:       principal.ClientID,
		PublicTaskID:   string(request.TaskID),
		PublicConfigID: request.ID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, a2a.ErrTaskNotFound
	}
	if err != nil {
		return nil, a2a.NewError(a2a.ErrInternalError, "unable to load push notification config")
	}
	return publicA2APushConfig(string(request.TaskID), stored), nil
}

func (s *A2AService) ListTaskPushConfigs(ctx context.Context, request *a2a.ListTaskPushConfigRequest) (*a2a.ListTaskPushConfigResponse, error) {
	if s == nil || s.Queries == nil || s.PushSecrets == nil {
		return nil, a2a.ErrPushNotificationNotSupported
	}
	if request == nil || strings.TrimSpace(string(request.TaskID)) == "" ||
		strings.TrimSpace(string(request.TaskID)) != string(request.TaskID) || len(request.TaskID) > 128 {
		return nil, a2a.NewError(a2a.ErrInvalidParams, "taskId is required")
	}
	pageSize := request.PageSize
	if pageSize == 0 {
		pageSize = a2aDefaultPushPageSize
	}
	if pageSize < 1 || pageSize > a2aMaxPushPageSize {
		return nil, a2a.NewError(a2a.ErrInvalidParams, "pageSize must be between 1 and 100")
	}
	beforeCreatedAt, beforeConfigID, err := decodeA2APushPageCursor(request.PageToken)
	if err != nil {
		return nil, a2a.NewError(a2a.ErrInvalidParams, err.Error())
	}
	_, principal, err := a2aPrincipalFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if _, err = s.Queries.GetA2ATaskBindingForClient(ctx, db.GetA2ATaskBindingForClientParams{
		EndpointID: principal.EndpointID, ClientID: principal.ClientID, PublicTaskID: string(request.TaskID),
	}); errors.Is(err, pgx.ErrNoRows) {
		return nil, a2a.ErrTaskNotFound
	} else if err != nil {
		return nil, a2a.NewError(a2a.ErrInternalError, "unable to load A2A task")
	}
	rows, err := s.Queries.ListA2APushConfigsForClient(ctx, db.ListA2APushConfigsForClientParams{
		EndpointID:           principal.EndpointID,
		ClientID:             principal.ClientID,
		PublicTaskID:         string(request.TaskID),
		BeforeCreatedAt:      beforeCreatedAt,
		BeforePublicConfigID: beforeConfigID,
		PageLimit:            int32(pageSize + 1),
	})
	if err != nil {
		return nil, a2a.NewError(a2a.ErrInternalError, "unable to list push notification configs")
	}
	response := &a2a.ListTaskPushConfigResponse{Configs: []*a2a.PushConfig{}}
	if len(rows) > pageSize {
		last := rows[pageSize-1]
		response.NextPageToken = encodeA2APushPageCursor(last.CreatedAt.Time, last.PublicConfigID)
		rows = rows[:pageSize]
	}
	for _, row := range rows {
		response.Configs = append(response.Configs, publicA2APushConfig(string(request.TaskID), row))
	}
	return response, nil
}

func (s *A2AService) DeleteTaskPushConfig(ctx context.Context, request *a2a.DeleteTaskPushConfigRequest) error {
	if s == nil || s.Queries == nil || s.PushSecrets == nil {
		return a2a.ErrPushNotificationNotSupported
	}
	if request == nil || strings.TrimSpace(string(request.TaskID)) == "" || strings.TrimSpace(request.ID) == "" ||
		strings.TrimSpace(string(request.TaskID)) != string(request.TaskID) || len(request.TaskID) > 128 ||
		strings.TrimSpace(request.ID) != request.ID || len(request.ID) > 128 {
		return a2a.NewError(a2a.ErrInvalidParams, "taskId and id are required")
	}
	_, principal, err := a2aPrincipalFromContext(ctx)
	if err != nil {
		return err
	}
	rows, err := s.Queries.DeleteA2APushConfigForClient(ctx, db.DeleteA2APushConfigForClientParams{
		EndpointID:     principal.EndpointID,
		ClientID:       principal.ClientID,
		PublicTaskID:   string(request.TaskID),
		PublicConfigID: request.ID,
	})
	if err != nil {
		return a2a.NewError(a2a.ErrInternalError, "unable to delete push notification config")
	}
	if rows == 0 {
		return a2a.ErrTaskNotFound
	}
	return nil
}

var a2aPushRetrySchedule = [...]time.Duration{
	5 * time.Second,
	30 * time.Second,
	2 * time.Minute,
	10 * time.Minute,
	30 * time.Minute,
	2 * time.Hour,
	6 * time.Hour,
	24 * time.Hour,
}

// A2APushWorker delivers the durable PostgreSQL outbox. A failure only changes
// delivery state; it never changes the owning public Task.
type A2APushWorker struct {
	Queries *db.Queries
	Service *A2AService
	Client  *http.Client
	wake    chan struct{}
	wg      sync.WaitGroup
}

func NewA2APushWorker(queries *db.Queries, service *A2AService) *A2APushWorker {
	return &A2APushWorker{
		Queries: queries,
		Service: service,
		Client:  newA2AOutboundHTTPClient(20 * time.Second),
		wake:    make(chan struct{}, 1),
	}
}

func (worker *A2APushWorker) NotifyA2APush() {
	if worker == nil {
		return
	}
	select {
	case worker.wake <- struct{}{}:
	default:
	}
}

func (worker *A2APushWorker) Run(ctx context.Context) {
	if worker == nil || worker.Queries == nil || worker.Service == nil || worker.Service.PushSecrets == nil {
		return
	}
	worker.wg.Add(1)
	defer worker.wg.Done()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		for worker.processNext(ctx) {
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-worker.wake:
		}
	}
}

func (worker *A2APushWorker) WaitWithTimeout(timeout time.Duration) bool {
	if worker == nil {
		return true
	}
	done := make(chan struct{})
	go func() {
		worker.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}

func (worker *A2APushWorker) processNext(ctx context.Context) bool {
	delivery, err := worker.Queries.ClaimNextA2APushDelivery(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return false
	}
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			slog.Error("claim A2A push delivery failed", "error", err)
		}
		return false
	}
	err = worker.deliver(ctx, delivery)
	if err == nil {
		if markErr := worker.Queries.MarkA2APushDeliveryDelivered(ctx, delivery.ID); markErr != nil {
			slog.Error("mark A2A push delivery complete failed", "delivery_id", delivery.ID, "error", markErr)
			return false
		}
		return true
	}
	deadLetter := int(delivery.AttemptCount) > len(a2aPushRetrySchedule)
	nextAttempt := time.Now().UTC()
	if !deadLetter {
		nextAttempt = nextAttempt.Add(a2aPushRetrySchedule[delivery.AttemptCount-1])
	}
	if retryErr := worker.Queries.RetryA2APushDelivery(ctx, db.RetryA2APushDeliveryParams{
		DeadLetter:    deadLetter,
		NextAttemptAt: pgtype.Timestamptz{Time: nextAttempt, Valid: true},
		LastError:     safeA2APushError(err),
		DeliveryID:    delivery.ID,
	}); retryErr != nil {
		slog.Error("update A2A push delivery retry failed", "delivery_id", delivery.ID, "error", retryErr)
		return false
	}
	return true
}

func (worker *A2APushWorker) deliver(ctx context.Context, delivery db.ClaimNextA2APushDeliveryRow) error {
	if err := validateA2APublicHTTPSURL(delivery.CallbackUrl); err != nil {
		return errors.New("callback URL is not allowed")
	}
	event, err := worker.Service.decodeA2ATaskEvent(ctx, db.A2aTaskEvent{
		EventType: delivery.EventType,
		Payload:   delivery.EventPayload,
	})
	if err != nil {
		return errors.New("stored push event is invalid")
	}
	payload, err := json.Marshal(a2a.StreamResponse{Event: event})
	if err != nil {
		return errors.New("stored push event cannot be encoded")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, delivery.CallbackUrl, bytes.NewReader(payload))
	if err != nil {
		return errors.New("callback request cannot be created")
	}
	request.Header.Set("Content-Type", "application/json")
	if len(delivery.NotificationTokenEncrypted) > 0 {
		token, openErr := worker.Service.PushSecrets.Open(delivery.NotificationTokenEncrypted)
		if openErr != nil {
			return errors.New("notification token cannot be decrypted")
		}
		request.Header.Set("A2A-Notification-Token", string(token))
	}
	if delivery.AuthScheme.Valid {
		credentials, openErr := worker.Service.PushSecrets.Open(delivery.AuthCredentialsEncrypted)
		if openErr != nil {
			return errors.New("authentication credentials cannot be decrypted")
		}
		request.Header.Set("Authorization", canonicalA2AAuthScheme(delivery.AuthScheme.String)+" "+string(credentials))
	}
	client := worker.Client
	if client == nil {
		client = newA2AOutboundHTTPClient(20 * time.Second)
	}
	response, err := client.Do(request)
	if err != nil {
		return errors.New("callback request failed")
	}
	readBytes, readErr := io.Copy(io.Discard, io.LimitReader(response.Body, a2aPushResponseLimit+1))
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil {
		return errors.New("callback response could not be read")
	}
	if readBytes > a2aPushResponseLimit {
		return errors.New("callback response is too large")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("callback returned HTTP %d", response.StatusCode)
	}
	return nil
}

func canonicalA2AAuthScheme(scheme string) string {
	if strings.EqualFold(scheme, "basic") {
		return "Basic"
	}
	return "Bearer"
}

func safeA2APushError(err error) string {
	if err == nil {
		return ""
	}
	message := err.Error()
	if len(message) > 500 {
		message = message[:500]
	}
	return message
}
