package dingtalk

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/multica-ai/multica/server/internal/chattrace"
	"github.com/multica-ai/multica/server/internal/integrations/channel"
	channelengine "github.com/multica-ai/multica/server/internal/integrations/channel/engine"
	"github.com/multica-ai/multica/server/internal/integrations/orgemphsf"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

const (
	streamInboxPollInterval  = time.Second
	streamInboxConcurrency   = 4
	streamInboxCandidateSize = 16
	// At the five-minute capped backoff, 300 attempts retain and retry an
	// unclassified handler error for a little over 24 hours before
	// dead-lettering it. Known infrastructure failures (Dapr, HSF, database,
	// timeouts and dedup finalization) retry until recovery. Dead rows keep their
	// encrypted payload for the seven-day retention window so an operator can
	// repair the dependency and requeue without asking DingTalk to redeliver an
	// already-ACKed frame.
	streamInboxMaxAttempts          = 300
	streamInboxMaxBackoff           = 5 * time.Minute
	streamInboxPurgeInterval        = 6 * time.Hour
	streamInboxRetention            = 7 * 24 * time.Hour
	streamInboxPurgeTimeout         = 30 * time.Second
	streamInboxDedupFinalizeBackoff = 65 * time.Second
	streamInboxMutationTimeout      = 5 * time.Second
	streamInboxHandlerTimeout       = 90 * time.Second

	// "DTIN" namespaces this session-scoped advisory lock. A worker keeps
	// the lock's pooled connection pinned while it calls the inbound handler,
	// so no other replica can process the same installation concurrently.
	streamInboxAdvisoryLockClass int32 = 0x4454494e
)

const (
	streamInboxStatusProcessed = "processed"
	streamInboxStatusDiscarded = "discarded"
	streamInboxStatusDead      = "dead"
)

// Encrypter seals a callback payload before it is admitted to Postgres.
// Production uses the same secretbox key as DingTalk installation secrets.
type Encrypter func(plaintext []byte) (ciphertext []byte, err error)

// StreamFrameAdmission is the callback subset retained by the durable inbox.
// Data is encrypted before the first database write and must never be logged.
type StreamFrameAdmission struct {
	InstallationID   string
	ConnectionID     string
	NodeID           string
	ReceiverHostname string
	MessageID        string
	Topic            string
	SpecVersion      string
	Time             int64
	Data             string
}

// StreamInboxReceipt describes the committed row without exposing callback
// data or installation credentials.
type StreamInboxReceipt struct {
	ID             string
	InstallationID string
	Status         string
	DeliveryCount  int32
	ReceivedAt     time.Time
}

// StreamInbox is the transport-facing contract. PersistFrame returns only
// after the admission statement has committed; callers may ACK after that.
// Notify is a latency hint because workers also poll Postgres.
type StreamInbox interface {
	PersistFrame(context.Context, string, StreamFrameAdmission) (StreamInboxReceipt, error)
	Notify()
}

type streamInboxStore interface {
	Admit(context.Context, db.AdmitDingTalkStreamInboxParams) (db.DingtalkStreamInbox, error)
	ClaimNext(context.Context) (db.DingtalkStreamInbox, func(), error)
	Retry(context.Context, db.DingtalkStreamInbox, time.Time, string, string) error
	Complete(context.Context, db.DingtalkStreamInbox, string, string, string) error
	PurgeTerminal(context.Context, time.Time) (int64, error)
}

// StreamInboxWorker owns both durable admission and asynchronous dispatch.
// Postgres is the source of truth; the in-memory channel only avoids waiting
// for the next recovery poll after a local admission.
type StreamInboxWorker struct {
	store   streamInboxStore
	handler channel.InboundHandler
	encrypt Encrypter
	decrypt Decrypter
	logger  *slog.Logger
	notify  chan struct{}
	done    chan struct{}
	// Purge timing is stored on the worker so tests can exercise the lifecycle
	// without waiting for the production six-hour cadence.
	purgeInterval time.Duration
	retention     time.Duration
	now           func() time.Time
}

func NewStreamInboxWorker(
	pool *pgxpool.Pool,
	handler channel.InboundHandler,
	encrypt Encrypter,
	decrypt Decrypter,
	logger *slog.Logger,
) *StreamInboxWorker {
	if logger == nil {
		logger = slog.Default()
	}
	return newStreamInboxWorker(
		&postgresStreamInboxStore{pool: pool, queries: db.New(pool), logger: logger},
		handler,
		encrypt,
		decrypt,
		logger,
	)
}

func newStreamInboxWorker(
	store streamInboxStore,
	handler channel.InboundHandler,
	encrypt Encrypter,
	decrypt Decrypter,
	logger *slog.Logger,
) *StreamInboxWorker {
	if logger == nil {
		logger = slog.Default()
	}
	return &StreamInboxWorker{
		store:         store,
		handler:       handler,
		encrypt:       encrypt,
		decrypt:       decrypt,
		logger:        logger,
		notify:        make(chan struct{}, streamInboxConcurrency),
		done:          make(chan struct{}),
		purgeInterval: streamInboxPurgeInterval,
		retention:     streamInboxRetention,
		now:           time.Now,
	}
}

// PersistFrame encrypts and commits one callback before returning. DingTalk's
// bot msgId is the primary idempotency key; the Stream messageId is used only
// when an invalid payload has no bot msgId, so even poison deliveries remain
// bounded to one durable row per Stream delivery.
func (w *StreamInboxWorker) PersistFrame(
	ctx context.Context,
	clientID string,
	frame StreamFrameAdmission,
) (StreamInboxReceipt, error) {
	if w == nil || w.store == nil {
		return StreamInboxReceipt{}, errors.New("dingtalk stream inbox: store not configured")
	}
	if w.encrypt == nil {
		return StreamInboxReceipt{}, errors.New("dingtalk stream inbox: encrypter not configured")
	}
	clientID = strings.TrimSpace(clientID)
	if clientID == "" {
		return StreamInboxReceipt{}, errors.New("dingtalk stream inbox: client id is empty")
	}
	frame.InstallationID = strings.TrimSpace(frame.InstallationID)
	installationID, err := util.ParseUUID(frame.InstallationID)
	if err != nil {
		return StreamInboxReceipt{}, fmt.Errorf("dingtalk stream inbox: invalid installation id: %w", err)
	}
	frame.ConnectionID = strings.TrimSpace(frame.ConnectionID)
	if frame.ConnectionID == "" {
		return StreamInboxReceipt{}, errors.New("dingtalk stream inbox: connection id is empty")
	}
	frame.NodeID = strings.TrimSpace(frame.NodeID)
	if frame.NodeID == "" {
		return StreamInboxReceipt{}, errors.New("dingtalk stream inbox: node id is empty")
	}
	frame.ReceiverHostname = strings.TrimSpace(frame.ReceiverHostname)
	if frame.ReceiverHostname == "" {
		return StreamInboxReceipt{}, errors.New("dingtalk stream inbox: receiver hostname is empty")
	}
	frame.MessageID = strings.TrimSpace(frame.MessageID)
	if frame.MessageID == "" {
		return StreamInboxReceipt{}, errors.New("dingtalk stream inbox: stream message id is empty")
	}
	frame.Topic = strings.TrimSpace(frame.Topic)
	if frame.Topic == "" {
		return StreamInboxReceipt{}, errors.New("dingtalk stream inbox: topic is empty")
	}

	var metadata struct {
		MsgID string `json:"msgId"`
	}
	_ = json.Unmarshal([]byte(frame.Data), &metadata)
	metadata.MsgID = strings.TrimSpace(metadata.MsgID)
	dedupeKind := "stream"
	dedupeKey := "stream:" + frame.MessageID
	botMessageID := pgtype.Text{}
	if metadata.MsgID != "" {
		dedupeKind = "bot"
		dedupeKey = "bot:" + metadata.MsgID
		botMessageID = pgtype.Text{String: metadata.MsgID, Valid: true}
	}

	traceLog := w.logger.With(
		"installation_id", frame.InstallationID,
		"connection_id", frame.ConnectionID,
		"node_id", frame.NodeID,
		"receiver_hostname", frame.ReceiverHostname,
		"stream_message_id_hash", streamInboxTraceHash(frame.MessageID),
		"bot_message_id_hash", streamInboxTraceHash(metadata.MsgID),
	)
	sealed, err := w.encrypt([]byte(frame.Data))
	if err != nil {
		traceLog.Error("dingtalk stream inbox callback encryption failed",
			"event", "dingtalk_stream_inbox_admission_failed",
			"error_class", "encryption",
			"error", err,
		)
		return StreamInboxReceipt{}, fmt.Errorf("dingtalk stream inbox: encrypt callback: %w", err)
	}
	row, err := w.store.Admit(ctx, db.AdmitDingTalkStreamInboxParams{
		InstallationID:   installationID,
		ClientID:         clientID,
		ConnectionID:     frame.ConnectionID,
		NodeID:           frame.NodeID,
		ReceiverHostname: frame.ReceiverHostname,
		DedupeKey:        dedupeKey,
		StreamMessageID:  frame.MessageID,
		BotMessageID:     botMessageID,
		Topic:            frame.Topic,
		SpecVersion:      streamInboxOptionalText(frame.SpecVersion),
		FrameTime:        streamInboxOptionalInt8(frame.Time),
		PayloadEncrypted: sealed,
	})
	if err != nil {
		traceLog.Error("dingtalk stream inbox callback commit failed",
			"event", "dingtalk_stream_inbox_admission_failed",
			"error_class", "database",
			"error", err,
		)
		return StreamInboxReceipt{}, fmt.Errorf("dingtalk stream inbox: commit callback: %w", err)
	}
	if !row.ID.Valid || !row.ReceivedAt.Valid {
		return StreamInboxReceipt{}, errors.New("dingtalk stream inbox: admitted row has no trace identity")
	}
	receipt := StreamInboxReceipt{
		ID:             util.UUIDToString(row.ID),
		InstallationID: util.UUIDToString(row.InstallationID),
		Status:         row.Status,
		DeliveryCount:  row.DeliveryCount,
		ReceivedAt:     row.ReceivedAt.Time,
	}
	traceLog.Info("dingtalk stream inbox admitted",
		"event", "dingtalk_stream_inbox_admitted",
		"trace_id", receipt.ID,
		"trace_started_at_unix_ms", receipt.ReceivedAt.UnixMilli(),
		"inbox_id", receipt.ID,
		"dedupe_kind", dedupeKind,
		"status", receipt.Status,
		"delivery_count", receipt.DeliveryCount,
	)
	return receipt, nil
}

func (w *StreamInboxWorker) Notify() {
	if w == nil {
		return
	}
	select {
	case w.notify <- struct{}{}:
	default:
	}
}

// Run starts a bounded worker pool. Every process may run one pool: advisory
// locks serialize each installation across all processes, while distinct
// installations can progress concurrently.
func (w *StreamInboxWorker) Run(ctx context.Context) {
	if w == nil {
		return
	}
	defer close(w.done)
	if err := w.validateProcessor(); err != nil {
		w.logger.Error("dingtalk stream inbox worker is not configured",
			"event", "dingtalk_stream_inbox_worker_invalid",
			"error_class", "configuration",
		)
		return
	}
	purgeDone := make(chan struct{})
	go func() {
		defer close(purgeDone)
		w.runPurgeLoop(ctx)
	}()

	var workers sync.WaitGroup
	workers.Add(streamInboxConcurrency)
	for range streamInboxConcurrency {
		go func() {
			defer workers.Done()
			w.runLoop(ctx)
		}()
	}
	workers.Wait()
	<-purgeDone
}

func (w *StreamInboxWorker) runPurgeLoop(ctx context.Context) {
	interval := w.purgeInterval
	if interval <= 0 {
		interval = streamInboxPurgeInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.purgeTerminal(ctx)
		}
	}
}

func (w *StreamInboxWorker) purgeTerminal(ctx context.Context) {
	retention := w.retention
	if retention <= 0 {
		retention = streamInboxRetention
	}
	now := time.Now
	if w.now != nil {
		now = w.now
	}
	cutoff := now().Add(-retention)
	purgeCtx, cancel := context.WithTimeout(ctx, streamInboxPurgeTimeout)
	deleted, err := w.store.PurgeTerminal(purgeCtx, cutoff)
	cancel()
	if err != nil {
		if errors.Is(err, context.Canceled) && ctx.Err() != nil {
			return
		}
		w.logger.Error("dingtalk stream inbox terminal purge failed",
			"event", "dingtalk_stream_inbox_purge_failed",
			"error_class", "database",
			"retention_hours", int64(retention/time.Hour),
			"cutoff", cutoff.UTC(),
			"error", err,
		)
		return
	}
	w.logger.Info("dingtalk stream inbox terminal purge completed",
		"event", "dingtalk_stream_inbox_purge_completed",
		"retention_hours", int64(retention/time.Hour),
		"cutoff", cutoff.UTC(),
		"deleted_count", deleted,
	)
}

func (w *StreamInboxWorker) validateProcessor() error {
	switch {
	case w.store == nil:
		return errors.New("store not configured")
	case w.handler == nil:
		return errors.New("inbound handler not configured")
	case w.decrypt == nil:
		return errors.New("decrypter not configured")
	default:
		return nil
	}
}

func (w *StreamInboxWorker) runLoop(ctx context.Context) {
	ticker := time.NewTicker(streamInboxPollInterval)
	defer ticker.Stop()
	for {
		worked, err := w.ProcessNext(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			w.logger.Error("dingtalk stream inbox processing failed",
				"event", "dingtalk_stream_inbox_process_failed",
				"error_class", classifyStreamInboxError(err),
				"error", err,
			)
		}
		if worked {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-w.notify:
		case <-ticker.C:
		}
	}
}

func (w *StreamInboxWorker) WaitWithTimeout(timeout time.Duration) bool {
	if w == nil {
		return true
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-w.done:
		return true
	case <-timer.C:
		return false
	}
}

// ProcessNext claims one installation lane and keeps its advisory lock until
// handler completion. A token-fenced terminal/retry update then releases the
// row; if ownership changed, pgx.ErrNoRows is treated as a benign lost lease.
func (w *StreamInboxWorker) ProcessNext(ctx context.Context) (bool, error) {
	if err := w.validateProcessor(); err != nil {
		return false, fmt.Errorf("dingtalk stream inbox: %w", err)
	}
	row, release, err := w.store.ClaimNext(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("dingtalk stream inbox: claim: %w", err)
	}
	defer release()

	traceHash := streamInboxTraceHash(row.StreamMessageID)
	log := w.logger.With(
		"inbox_id", util.UUIDToString(row.ID),
		"installation_id", util.UUIDToString(row.InstallationID),
		"connection_id", row.ConnectionID,
		"node_id", row.NodeID,
		"receiver_hostname", row.ReceiverHostname.String,
		"stream_message_id_hash", traceHash,
		"bot_message_id_hash", streamInboxTraceHash(row.BotMessageID.String),
	)
	queueLatency := int64(0)
	if row.ReceivedAt.Valid {
		queueLatency = time.Since(row.ReceivedAt.Time).Milliseconds()
		if queueLatency < 0 {
			queueLatency = 0
		}
	}
	log.Info("dingtalk stream inbox processing started",
		"event", "dingtalk_stream_inbox_processing_started",
		"attempt_count", row.AttemptCount+1,
		"delivery_count", row.DeliveryCount,
		"queue_latency_ms", queueLatency,
	)
	plain, err := w.decrypt(row.PayloadEncrypted)
	if err != nil {
		return true, w.complete(ctx, row, streamInboxStatusDead, "decrypt", "payload decryption failed", traceHash)
	}
	var data botCallbackData
	if err := json.Unmarshal(plain, &data); err != nil {
		return true, w.complete(ctx, row, streamInboxStatusDiscarded, "payload_invalid", "callback payload invalid", traceHash)
	}
	sourcePayload, err := sanitizeDingTalkAgentSourcePayload(plain)
	if err != nil {
		return true, w.complete(ctx, row, streamInboxStatusDiscarded, "payload_invalid", "callback payload invalid", traceHash)
	}
	var streamSource protocol.DingTalkStreamSource
	if row.ReceiverHostname.Valid && strings.TrimSpace(row.ReceiverHostname.String) != "" {
		streamSource = protocol.DingTalkStreamSource{
			Hostname:     row.ReceiverHostname.String,
			NodeID:       row.NodeID,
			ConnectionID: row.ConnectionID,
		}
	}
	msg, ok := inboundFromBotCallbackForInstallation(data, row.ClientID, util.UUIDToString(row.InstallationID), streamSource, sourcePayload)
	if !ok {
		return true, w.complete(ctx, row, streamInboxStatusDiscarded, "payload_unusable", "callback has no message id", traceHash)
	}
	if !row.ReceivedAt.Valid {
		return true, w.complete(ctx, row, streamInboxStatusDead, "invalid_ingress_time", "inbox received_at is missing", traceHash)
	}
	msg.TraceID = util.UUIDToString(row.ID)
	msg.TraceChannel = "dingtalk_stream"
	msg.TraceStartedAtUnixMS = row.ReceivedAt.Time.UnixMilli()
	trace, err := chattrace.From(msg.TraceID, msg.TraceChannel, msg.TraceStartedAtUnixMS)
	if err != nil {
		return true, w.complete(ctx, row, streamInboxStatusDead, "invalid_chat_trace", "inbox chat trace is invalid", traceHash)
	}
	chattrace.LogStage(w.logger, trace, "stream_inbox_processing", "started",
		"inbox_id", msg.TraceID,
		"queue_latency_ms", queueLatency,
		"attempt_count", row.AttemptCount+1,
	)
	handlerCtx, handlerCancel := context.WithTimeout(ctx, streamInboxHandlerTimeout)
	handlerErr := w.handler(handlerCtx, msg)
	handlerCancel()
	if handlerErr != nil {
		errorClass := classifyStreamInboxError(handlerErr)
		detail := streamInboxErrorDetail(errorClass)
		if row.AttemptCount+1 >= streamInboxMaxAttempts && !streamInboxRetryIndefinitely(errorClass) {
			return true, w.complete(ctx, row, streamInboxStatusDead, errorClass, detail, traceHash)
		}
		backoff := streamInboxRetryBackoff(row.AttemptCount)
		if errors.Is(handlerErr, channelengine.ErrDedupFinalize) && backoff < streamInboxDedupFinalizeBackoff {
			backoff = streamInboxDedupFinalizeBackoff
		}
		availableAt := time.Now().Add(backoff)
		mutationCtx, mutationCancel := context.WithTimeout(context.Background(), streamInboxMutationTimeout)
		retryErr := w.store.Retry(mutationCtx, row, availableAt, errorClass, detail)
		mutationCancel()
		if retryErr != nil {
			if errors.Is(retryErr, pgx.ErrNoRows) {
				w.logLeaseLost(row, "retry", traceHash)
				return true, nil
			}
			log.Error("dingtalk stream inbox retry state update failed",
				"event", "dingtalk_stream_inbox_retry_update_failed",
				"error_class", "database",
				"attempt_count", row.AttemptCount+1,
				"error", retryErr,
			)
			return true, fmt.Errorf("dingtalk stream inbox: retry: %w", retryErr)
		}
		log.Warn("dingtalk stream inbox scheduled retry",
			"event", "dingtalk_stream_inbox_retry_scheduled",
			"attempt_count", row.AttemptCount+1,
			"error_class", errorClass,
			"retry_policy", streamInboxRetryPolicy(errorClass),
			"available_at", availableAt.UTC(),
			"error", handlerErr,
		)
		return true, nil
	}
	return true, w.complete(ctx, row, streamInboxStatusProcessed, "", "", traceHash)
}

func (w *StreamInboxWorker) complete(
	_ context.Context,
	row db.DingtalkStreamInbox,
	status string,
	errorClass string,
	detail string,
	traceHash string,
) error {
	mutationCtx, mutationCancel := context.WithTimeout(context.Background(), streamInboxMutationTimeout)
	defer mutationCancel()
	if err := w.store.Complete(mutationCtx, row, status, errorClass, detail); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			w.logLeaseLost(row, status, traceHash)
			return nil
		}
		w.logger.Error("dingtalk stream inbox terminal state update failed",
			"event", "dingtalk_stream_inbox_finalize_failed",
			"inbox_id", util.UUIDToString(row.ID),
			"installation_id", util.UUIDToString(row.InstallationID),
			"connection_id", row.ConnectionID,
			"node_id", row.NodeID,
			"stream_message_id_hash", traceHash,
			"bot_message_id_hash", streamInboxTraceHash(row.BotMessageID.String),
			"target_status", status,
			"error_class", "database",
			"error", err,
		)
		return fmt.Errorf("dingtalk stream inbox: complete %s: %w", status, err)
	}
	w.logger.Info("dingtalk stream inbox finalized",
		"event", "dingtalk_stream_inbox_finalized",
		"inbox_id", util.UUIDToString(row.ID),
		"installation_id", util.UUIDToString(row.InstallationID),
		"connection_id", row.ConnectionID,
		"node_id", row.NodeID,
		"stream_message_id_hash", traceHash,
		"bot_message_id_hash", streamInboxTraceHash(row.BotMessageID.String),
		"status", status,
		"attempt_count", row.AttemptCount+1,
		"error_class", errorClass,
		"payload_retained", status == streamInboxStatusDead,
		"retention_hours", int64(w.retention/time.Hour),
	)
	return nil
}

func (w *StreamInboxWorker) logLeaseLost(row db.DingtalkStreamInbox, operation, traceHash string) {
	w.logger.Warn("dingtalk stream inbox lease ownership changed",
		"event", "dingtalk_stream_inbox_lease_lost",
		"inbox_id", util.UUIDToString(row.ID),
		"installation_id", util.UUIDToString(row.InstallationID),
		"connection_id", row.ConnectionID,
		"node_id", row.NodeID,
		"stream_message_id_hash", traceHash,
		"bot_message_id_hash", streamInboxTraceHash(row.BotMessageID.String),
		"operation", operation,
	)
}

func streamInboxRetryBackoff(attemptCount int32) time.Duration {
	backoff := time.Second
	for i := int32(0); i < attemptCount && backoff < streamInboxMaxBackoff; i++ {
		backoff *= 2
		if backoff >= streamInboxMaxBackoff {
			return streamInboxMaxBackoff
		}
	}
	return backoff
}

func classifyStreamInboxError(err error) string {
	if errors.Is(err, channelengine.ErrDedupFinalize) {
		return "dedup_finalize"
	}
	if errors.Is(err, context.Canceled) {
		return "context_canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "context_deadline"
	}
	var hsfErr *orgemphsf.ServiceError
	if errors.As(err, &hsfErr) {
		return "hsf_service"
	}
	lower := strings.ToLower(err.Error())
	switch {
	case strings.Contains(lower, "dapr"):
		return "dapr"
	case strings.Contains(lower, "hsf"):
		return "hsf"
	case strings.Contains(lower, "database"), strings.Contains(lower, "postgres"), strings.Contains(lower, "sql"):
		return "database"
	default:
		return "handler"
	}
}

func streamInboxErrorDetail(errorClass string) string {
	switch errorClass {
	case "context_canceled", "context_deadline":
		return "handler context ended"
	case "hsf_service", "hsf":
		return "handler HSF request failed"
	case "dapr":
		return "handler Dapr request failed"
	case "database":
		return "handler database request failed"
	case "dedup_finalize":
		return "handler dedup finalization failed"
	default:
		return "inbound handler failed"
	}
}

func streamInboxRetryIndefinitely(errorClass string) bool {
	switch errorClass {
	case "context_canceled", "context_deadline", "hsf_service", "hsf", "dapr", "database", "dedup_finalize":
		return true
	default:
		return false
	}
}

func streamInboxRetryPolicy(errorClass string) string {
	if streamInboxRetryIndefinitely(errorClass) {
		return "until_dependency_recovers"
	}
	return "dead_letter_after_24h"
}

func streamInboxOptionalText(value string) pgtype.Text {
	value = strings.TrimSpace(value)
	return pgtype.Text{String: value, Valid: value != ""}
}

func streamInboxOptionalInt8(value int64) pgtype.Int8 {
	return pgtype.Int8{Int64: value, Valid: value != 0}
}

func streamInboxTraceHash(value string) string {
	if value == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:8])
}

type postgresStreamInboxStore struct {
	pool    *pgxpool.Pool
	queries *db.Queries
	logger  *slog.Logger
}

func (s *postgresStreamInboxStore) Admit(
	ctx context.Context,
	params db.AdmitDingTalkStreamInboxParams,
) (db.DingtalkStreamInbox, error) {
	if s.pool == nil || s.queries == nil {
		return db.DingtalkStreamInbox{}, errors.New("dingtalk stream inbox: database not configured")
	}
	return s.queries.AdmitDingTalkStreamInbox(ctx, params)
}

// ClaimNext pins one pool connection for the installation advisory lock. The
// generated claim statement itself commits before the handler runs; the
// session lock, not an open transaction, supplies lane serialization.
func (s *postgresStreamInboxStore) ClaimNext(ctx context.Context) (db.DingtalkStreamInbox, func(), error) {
	if s.pool == nil || s.queries == nil {
		return db.DingtalkStreamInbox{}, nil, errors.New("dingtalk stream inbox: database not configured")
	}
	installationIDs, err := s.queries.ListDueDingTalkStreamInboxInstallations(ctx, streamInboxCandidateSize)
	if err != nil {
		return db.DingtalkStreamInbox{}, nil, err
	}
	for _, installationID := range installationIDs {
		conn, err := s.pool.Acquire(ctx)
		if err != nil {
			return db.DingtalkStreamInbox{}, nil, err
		}
		key := streamInboxAdvisoryKey(installationID)
		var locked bool
		if err := conn.QueryRow(ctx,
			"SELECT pg_try_advisory_lock($1, $2)",
			streamInboxAdvisoryLockClass,
			key,
		).Scan(&locked); err != nil {
			conn.Release()
			return db.DingtalkStreamInbox{}, nil, err
		}
		if !locked {
			conn.Release()
			continue
		}

		release := s.releaseInstallationLock(conn, key)
		row, err := db.New(conn).ClaimNextDingTalkStreamInboxForInstallation(ctx, installationID)
		if errors.Is(err, pgx.ErrNoRows) {
			release()
			continue
		}
		if err != nil {
			release()
			return db.DingtalkStreamInbox{}, nil, err
		}
		return row, release, nil
	}
	return db.DingtalkStreamInbox{}, nil, pgx.ErrNoRows
}

func (s *postgresStreamInboxStore) releaseInstallationLock(conn *pgxpool.Conn, key int32) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var unlocked bool
			if err := conn.QueryRow(ctx,
				"SELECT pg_advisory_unlock($1, $2)",
				streamInboxAdvisoryLockClass,
				key,
			).Scan(&unlocked); err != nil || !unlocked {
				s.logger.Warn("dingtalk stream inbox advisory unlock failed",
					"event", "dingtalk_stream_inbox_unlock_failed",
					"error_class", "database",
					"connection_discarded", true,
					"error", err,
				)
				// Session advisory locks survive pool release. If unlock could not be
				// confirmed, remove this physical connection from the pool so it can
				// never retain the installation lane lock for a future borrower.
				closeCtx, closeCancel := context.WithTimeout(context.Background(), time.Second)
				defer closeCancel()
				if closeErr := conn.Hijack().Close(closeCtx); closeErr != nil {
					s.logger.Warn("dingtalk stream inbox advisory connection close failed",
						"event", "dingtalk_stream_inbox_unlock_connection_close_failed",
						"error_class", "database",
						"error", closeErr,
					)
				}
				return
			}
			conn.Release()
		})
	}
}

func (s *postgresStreamInboxStore) Retry(
	ctx context.Context,
	row db.DingtalkStreamInbox,
	availableAt time.Time,
	errorClass string,
	detail string,
) error {
	_, err := s.queries.RetryClaimedDingTalkStreamInbox(ctx, db.RetryClaimedDingTalkStreamInboxParams{
		AvailableAt:    pgtype.Timestamptz{Time: availableAt, Valid: true},
		LastErrorClass: streamInboxOptionalText(errorClass),
		LastError:      streamInboxOptionalText(detail),
		ID:             row.ID,
		LeaseToken:     row.LeaseToken,
	})
	return err
}

func (s *postgresStreamInboxStore) Complete(
	ctx context.Context,
	row db.DingtalkStreamInbox,
	status string,
	errorClass string,
	detail string,
) error {
	_, err := s.queries.CompleteClaimedDingTalkStreamInbox(ctx, db.CompleteClaimedDingTalkStreamInboxParams{
		Status:         status,
		LastErrorClass: streamInboxOptionalText(errorClass),
		LastError:      streamInboxOptionalText(detail),
		ID:             row.ID,
		LeaseToken:     row.LeaseToken,
	})
	return err
}

func (s *postgresStreamInboxStore) PurgeTerminal(ctx context.Context, cutoff time.Time) (int64, error) {
	return s.queries.PurgeTerminalDingTalkStreamInbox(ctx, pgtype.Timestamptz{
		Time:  cutoff,
		Valid: true,
	})
}

func streamInboxAdvisoryKey(installationID pgtype.UUID) int32 {
	h := fnv.New32a()
	_, _ = h.Write(installationID.Bytes[:])
	return int32(h.Sum32())
}
