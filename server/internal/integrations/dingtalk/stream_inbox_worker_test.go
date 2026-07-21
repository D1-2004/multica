package dingtalk

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/integrations/channel"
	channelengine "github.com/multica-ai/multica/server/internal/integrations/channel/engine"
	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/internal/util/secretbox"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type streamInboxRetryCall struct {
	row          db.DingtalkStreamInbox
	availableAt  time.Time
	errorClass   string
	errorMessage string
}

type streamInboxCompleteCall struct {
	row          db.DingtalkStreamInbox
	status       string
	errorClass   string
	errorMessage string
}

type fakeStreamInboxStore struct {
	mu sync.Mutex

	admitParams  []db.AdmitDingTalkStreamInboxParams
	admitRow     db.DingtalkStreamInbox
	admitErr     error
	claims       []db.DingtalkStreamInbox
	claimErr     error
	retryCalls   []streamInboxRetryCall
	retryErr     error
	completions  []streamInboxCompleteCall
	completeErr  error
	purgeCalls   []time.Time
	purgeCount   int64
	purgeErr     error
	purgeEntered chan struct{}
	purgeRelease chan struct{}
	releases     int
}

func (s *fakeStreamInboxStore) Admit(
	_ context.Context,
	params db.AdmitDingTalkStreamInboxParams,
) (db.DingtalkStreamInbox, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.admitParams = append(s.admitParams, params)
	return s.admitRow, s.admitErr
}

func (s *fakeStreamInboxStore) ClaimNext(context.Context) (db.DingtalkStreamInbox, func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.claimErr != nil {
		return db.DingtalkStreamInbox{}, nil, s.claimErr
	}
	if len(s.claims) == 0 {
		return db.DingtalkStreamInbox{}, nil, pgx.ErrNoRows
	}
	row := s.claims[0]
	s.claims = s.claims[1:]
	return row, func() {
		s.mu.Lock()
		s.releases++
		s.mu.Unlock()
	}, nil
}

func (s *fakeStreamInboxStore) Retry(
	_ context.Context,
	row db.DingtalkStreamInbox,
	availableAt time.Time,
	errorClass string,
	errorMessage string,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.retryCalls = append(s.retryCalls, streamInboxRetryCall{
		row:          row,
		availableAt:  availableAt,
		errorClass:   errorClass,
		errorMessage: errorMessage,
	})
	return s.retryErr
}

func (s *fakeStreamInboxStore) Complete(
	_ context.Context,
	row db.DingtalkStreamInbox,
	status string,
	errorClass string,
	errorMessage string,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.completions = append(s.completions, streamInboxCompleteCall{
		row:          row,
		status:       status,
		errorClass:   errorClass,
		errorMessage: errorMessage,
	})
	return s.completeErr
}

func (s *fakeStreamInboxStore) PurgeTerminal(ctx context.Context, cutoff time.Time) (int64, error) {
	s.mu.Lock()
	s.purgeCalls = append(s.purgeCalls, cutoff)
	count, err := s.purgeCount, s.purgeErr
	entered, release := s.purgeEntered, s.purgeRelease
	s.mu.Unlock()
	if entered != nil {
		select {
		case entered <- struct{}{}:
		default:
		}
	}
	if release != nil {
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-release:
		}
	}
	return count, err
}

func TestStreamInboxPersistEncryptsPayloadAndUsesBotMessageID(t *testing.T) {
	box, err := secretbox.New(bytes.Repeat([]byte{0x42}, 32))
	if err != nil {
		t.Fatalf("secretbox.New: %v", err)
	}
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	store := &fakeStreamInboxStore{admitRow: db.DingtalkStreamInbox{
		ID:             util.MustParseUUID("00000000-0000-0000-0000-000000000001"),
		InstallationID: util.MustParseUUID("00000000-0000-0000-0000-000000000002"),
		Status:         "queued",
		DeliveryCount:  1,
		ReceivedAt:     pgtype.Timestamptz{Time: time.Now(), Valid: true},
	}}
	worker := newStreamInboxWorker(store, nil, box.Seal, box.Open, logger)
	payload := `{"msgId":"bot-message-1","senderStaffId":"staff-secret","sessionWebhook":"https://example.test/send?session=secret"}`

	receipt, err := worker.PersistFrame(context.Background(), "ding-client-secret", StreamFrameAdmission{
		InstallationID:   "00000000-0000-0000-0000-000000000002",
		ConnectionID:     "node-a-g1",
		NodeID:           "node-a",
		ReceiverHostname: "stream-host-a",
		MessageID:        "stream-message-1",
		Topic:            streamTopicBotMessage,
		SpecVersion:      "1.0",
		Time:             123,
		Data:             payload,
	})
	if err != nil {
		t.Fatalf("PersistFrame: %v", err)
	}
	if receipt.InstallationID != "00000000-0000-0000-0000-000000000002" || receipt.Status != "queued" {
		t.Fatalf("receipt = %+v", receipt)
	}
	if len(store.admitParams) != 1 {
		t.Fatalf("admissions = %d, want 1", len(store.admitParams))
	}
	params := store.admitParams[0]
	if params.InstallationID != util.MustParseUUID("00000000-0000-0000-0000-000000000002") || params.ConnectionID != "node-a-g1" || params.NodeID != "node-a" || params.ReceiverHostname != "stream-host-a" {
		t.Errorf("admission source fence = installation %s connection %q node %q", util.UUIDToString(params.InstallationID), params.ConnectionID, params.NodeID)
	}
	if params.DedupeKey != "bot:bot-message-1" || !params.BotMessageID.Valid || params.BotMessageID.String != "bot-message-1" {
		t.Errorf("dedupe params = key %q bot %+v", params.DedupeKey, params.BotMessageID)
	}
	if bytes.Equal(params.PayloadEncrypted, []byte(payload)) || bytes.Contains(params.PayloadEncrypted, []byte("session=secret")) {
		t.Fatalf("payload was not encrypted before admission")
	}
	plain, err := box.Open(params.PayloadEncrypted)
	if err != nil || string(plain) != payload {
		t.Fatalf("open admitted payload = %q, %v", plain, err)
	}
	for _, secret := range []string{"ding-client-secret", "staff-secret", "session=secret", payload} {
		if strings.Contains(logs.String(), secret) {
			t.Errorf("logs contain sensitive callback data %q: %s", secret, logs.String())
		}
	}
}

func TestStreamInboxPersistUsesStreamIDForMalformedPayload(t *testing.T) {
	box, err := secretbox.New(bytes.Repeat([]byte{0x24}, 32))
	if err != nil {
		t.Fatalf("secretbox.New: %v", err)
	}
	store := &fakeStreamInboxStore{admitRow: db.DingtalkStreamInbox{
		ID:             util.MustParseUUID("00000000-0000-0000-0000-000000000003"),
		InstallationID: util.MustParseUUID("00000000-0000-0000-0000-000000000002"),
		Status:         "queued",
		ReceivedAt:     pgtype.Timestamptz{Time: time.Now(), Valid: true},
	}}
	worker := newStreamInboxWorker(store, nil, box.Seal, box.Open, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))

	if _, err := worker.PersistFrame(context.Background(), "client", StreamFrameAdmission{
		InstallationID:   "00000000-0000-0000-0000-000000000002",
		ConnectionID:     "node-a-g1",
		NodeID:           "node-a",
		ReceiverHostname: "stream-host-a",
		MessageID:        "stream-2",
		Topic:            streamTopicBotMessage,
		Data:             "not-json",
	}); err != nil {
		t.Fatalf("PersistFrame: %v", err)
	}
	params := store.admitParams[0]
	if params.DedupeKey != "stream:stream-2" || params.BotMessageID.Valid {
		t.Errorf("malformed payload dedupe = key %q bot %+v", params.DedupeKey, params.BotMessageID)
	}
}

func TestStreamInboxPersistFailureLogsSafeSourceMetadata(t *testing.T) {
	box, err := secretbox.New(bytes.Repeat([]byte{0x25}, 32))
	if err != nil {
		t.Fatalf("secretbox.New: %v", err)
	}
	store := &fakeStreamInboxStore{admitErr: errors.New("database unavailable")}
	var logs bytes.Buffer
	worker := newStreamInboxWorker(store, nil, box.Seal, box.Open, slog.New(slog.NewJSONHandler(&logs, nil)))
	payload := `{"msgId":"bot-message-1","sessionWebhook":"https://example.test/send?session=must-not-log"}`
	_, err = worker.PersistFrame(context.Background(), "client-secret", StreamFrameAdmission{
		InstallationID:   "00000000-0000-0000-0000-000000000002",
		ConnectionID:     "node-a-g1",
		NodeID:           "node-a",
		ReceiverHostname: "stream-host-a",
		MessageID:        "stream-message-1",
		Topic:            streamTopicBotMessage,
		Data:             payload,
	})
	if err == nil {
		t.Fatal("PersistFrame succeeded, want database error")
	}
	for _, field := range []string{
		"dingtalk_stream_inbox_admission_failed",
		"00000000-0000-0000-0000-000000000002",
		"node-a-g1",
		"node-a",
		streamInboxTraceHash("stream-message-1"),
	} {
		if !strings.Contains(logs.String(), field) {
			t.Errorf("admission failure logs missing %q: %s", field, logs.String())
		}
	}
	for _, secret := range []string{"client-secret", "must-not-log", payload} {
		if strings.Contains(logs.String(), secret) {
			t.Errorf("admission failure logs contain sensitive value %q: %s", secret, logs.String())
		}
	}
}

func TestStreamInboxProcessNextDispatchesAndFinalizes(t *testing.T) {
	box, err := secretbox.New(bytes.Repeat([]byte{0x11}, 32))
	if err != nil {
		t.Fatalf("secretbox.New: %v", err)
	}
	row := testStreamInboxRow(t, box, 0, `{"msgId":"m1","conversationId":"cid","senderStaffId":"staff","conversationType":"1","msgtype":"text","text":{"content":" hello "}}`)
	store := &fakeStreamInboxStore{claims: []db.DingtalkStreamInbox{row}}
	var received []channel.InboundMessage
	worker := newStreamInboxWorker(store, func(_ context.Context, msg channel.InboundMessage) error {
		received = append(received, msg)
		return nil
	}, box.Seal, box.Open, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))

	worked, err := worker.ProcessNext(context.Background())
	if err != nil || !worked {
		t.Fatalf("ProcessNext = worked %v err %v", worked, err)
	}
	if len(received) != 1 || received[0].MessageID != "m1" || received[0].Text != "hello" {
		t.Fatalf("received = %+v", received)
	}
	if received[0].TraceID != "00000000-0000-0000-0000-000000000010" || received[0].TraceChannel != "dingtalk_stream" || received[0].TraceStartedAtUnixMS != row.ReceivedAt.Time.UnixMilli() {
		t.Fatalf("received trace = id %q channel %q started %d", received[0].TraceID, received[0].TraceChannel, received[0].TraceStartedAtUnixMS)
	}
	raw, err := decodeDingTalkRaw(received[0])
	if err != nil || raw.InstallationID != "00000000-0000-0000-0000-000000000020" {
		t.Fatalf("admission installation fence = %q, %v", raw.InstallationID, err)
	}
	if raw.StreamSource == nil || raw.StreamSource.Hostname != "stream-host-a" || raw.StreamSource.NodeID != "node-a" || raw.StreamSource.ConnectionID != "node-a-g1" {
		t.Fatalf("stream source = %+v", raw.StreamSource)
	}
	if len(store.completions) != 1 || store.completions[0].status != streamInboxStatusProcessed {
		t.Fatalf("completions = %+v", store.completions)
	}
	if store.releases != 1 {
		t.Fatalf("advisory releases = %d, want 1", store.releases)
	}
}

func TestStreamInboxProcessNextRetriesInfrastructureFailure(t *testing.T) {
	box, err := secretbox.New(bytes.Repeat([]byte{0x12}, 32))
	if err != nil {
		t.Fatalf("secretbox.New: %v", err)
	}
	row := testStreamInboxRow(t, box, 0, `{"msgId":"m1","conversationId":"cid","senderStaffId":"staff","conversationType":"2"}`)
	store := &fakeStreamInboxStore{claims: []db.DingtalkStreamInbox{row}}
	worker := newStreamInboxWorker(store, func(context.Context, channel.InboundMessage) error {
		return errors.New("upstream unavailable")
	}, box.Seal, box.Open, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	before := time.Now()

	worked, err := worker.ProcessNext(context.Background())
	if err != nil || !worked {
		t.Fatalf("ProcessNext = worked %v err %v", worked, err)
	}
	if len(store.retryCalls) != 1 || len(store.completions) != 0 {
		t.Fatalf("retries = %+v completions = %+v", store.retryCalls, store.completions)
	}
	retry := store.retryCalls[0]
	if retry.errorClass != "handler" || retry.errorMessage != "inbound handler failed" {
		t.Errorf("retry classification = %+v", retry)
	}
	if retry.availableAt.Before(before.Add(900*time.Millisecond)) || retry.availableAt.After(before.Add(2*time.Second)) {
		t.Errorf("first retry availableAt = %v", retry.availableAt)
	}
}

func TestStreamInboxDedupFinalizeRetryWaitsPastStaleClaimWindow(t *testing.T) {
	box, err := secretbox.New(bytes.Repeat([]byte{0x16}, 32))
	if err != nil {
		t.Fatalf("secretbox.New: %v", err)
	}
	row := testStreamInboxRow(t, box, 0, `{"msgId":"m1","conversationId":"cid","senderStaffId":"staff","conversationType":"2"}`)
	store := &fakeStreamInboxStore{claims: []db.DingtalkStreamInbox{row}}
	worker := newStreamInboxWorker(store, func(context.Context, channel.InboundMessage) error {
		return errors.Join(channelengine.ErrDedupFinalize, errors.New("release failed"))
	}, box.Seal, box.Open, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	before := time.Now()

	worked, err := worker.ProcessNext(context.Background())
	if err != nil || !worked {
		t.Fatalf("ProcessNext = worked %v err %v", worked, err)
	}
	if len(store.retryCalls) != 1 {
		t.Fatalf("retry calls = %d, want 1", len(store.retryCalls))
	}
	retry := store.retryCalls[0]
	if retry.errorClass != "dedup_finalize" || retry.errorMessage != "handler dedup finalization failed" {
		t.Fatalf("retry classification = %+v", retry)
	}
	if retry.availableAt.Before(before.Add(streamInboxDedupFinalizeBackoff - 100*time.Millisecond)) {
		t.Fatalf("dedup finalize retry too early: %v", retry.availableAt)
	}
}

func TestStreamInboxProcessNextMovesExhaustedFailureToDead(t *testing.T) {
	box, err := secretbox.New(bytes.Repeat([]byte{0x13}, 32))
	if err != nil {
		t.Fatalf("secretbox.New: %v", err)
	}
	row := testStreamInboxRow(t, box, streamInboxMaxAttempts-1, `{"msgId":"m1","conversationId":"cid"}`)
	store := &fakeStreamInboxStore{claims: []db.DingtalkStreamInbox{row}}
	worker := newStreamInboxWorker(store, func(context.Context, channel.InboundMessage) error {
		return errors.New("unclassified upstream unavailable")
	}, box.Seal, box.Open, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))

	worked, err := worker.ProcessNext(context.Background())
	if err != nil || !worked {
		t.Fatalf("ProcessNext = worked %v err %v", worked, err)
	}
	if len(store.retryCalls) != 0 || len(store.completions) != 1 {
		t.Fatalf("retries = %+v completions = %+v", store.retryCalls, store.completions)
	}
	completion := store.completions[0]
	if completion.status != streamInboxStatusDead || completion.errorClass != "handler" {
		t.Errorf("completion = %+v", completion)
	}
}

func TestStreamInboxInfrastructureFailureKeepsRetryingPastAttemptLimit(t *testing.T) {
	box, err := secretbox.New(bytes.Repeat([]byte{0x17}, 32))
	if err != nil {
		t.Fatalf("secretbox.New: %v", err)
	}
	row := testStreamInboxRow(t, box, streamInboxMaxAttempts+100, `{"msgId":"m1","conversationId":"cid"}`)
	store := &fakeStreamInboxStore{claims: []db.DingtalkStreamInbox{row}}
	worker := newStreamInboxWorker(store, func(context.Context, channel.InboundMessage) error {
		return errors.New("Dapr sidecar unavailable")
	}, box.Seal, box.Open, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))

	worked, err := worker.ProcessNext(context.Background())
	if err != nil || !worked {
		t.Fatalf("ProcessNext = worked %v err %v", worked, err)
	}
	if len(store.retryCalls) != 1 || len(store.completions) != 0 {
		t.Fatalf("infrastructure failure must remain retryable: retries=%+v completions=%+v", store.retryCalls, store.completions)
	}
	if store.retryCalls[0].errorClass != "dapr" {
		t.Fatalf("retry class = %q, want dapr", store.retryCalls[0].errorClass)
	}
}

func TestStreamInboxProcessNextDiscardsMalformedPayload(t *testing.T) {
	box, err := secretbox.New(bytes.Repeat([]byte{0x14}, 32))
	if err != nil {
		t.Fatalf("secretbox.New: %v", err)
	}
	row := testStreamInboxRow(t, box, 0, "not-json")
	store := &fakeStreamInboxStore{claims: []db.DingtalkStreamInbox{row}}
	handled := false
	worker := newStreamInboxWorker(store, func(context.Context, channel.InboundMessage) error {
		handled = true
		return nil
	}, box.Seal, box.Open, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))

	worked, err := worker.ProcessNext(context.Background())
	if err != nil || !worked {
		t.Fatalf("ProcessNext = worked %v err %v", worked, err)
	}
	if handled {
		t.Fatal("malformed payload reached inbound handler")
	}
	if len(store.completions) != 1 || store.completions[0].status != streamInboxStatusDiscarded {
		t.Fatalf("completions = %+v", store.completions)
	}
}

func TestStreamInboxProcessNextTreatsTokenFenceMissAsLeaseLoss(t *testing.T) {
	box, err := secretbox.New(bytes.Repeat([]byte{0x15}, 32))
	if err != nil {
		t.Fatalf("secretbox.New: %v", err)
	}
	row := testStreamInboxRow(t, box, 0, `{"msgId":"m1","conversationId":"cid"}`)
	store := &fakeStreamInboxStore{
		claims:      []db.DingtalkStreamInbox{row},
		completeErr: pgx.ErrNoRows,
	}
	var logs bytes.Buffer
	worker := newStreamInboxWorker(store, func(context.Context, channel.InboundMessage) error { return nil }, box.Seal, box.Open, slog.New(slog.NewJSONHandler(&logs, nil)))

	worked, err := worker.ProcessNext(context.Background())
	if err != nil || !worked {
		t.Fatalf("ProcessNext = worked %v err %v", worked, err)
	}
	if !strings.Contains(logs.String(), "dingtalk_stream_inbox_lease_lost") {
		t.Fatalf("lease-loss event missing from logs: %s", logs.String())
	}
}

func TestStreamInboxRunPurgesTerminalRowsAndStopsOnCancel(t *testing.T) {
	box, err := secretbox.New(bytes.Repeat([]byte{0x17}, 32))
	if err != nil {
		t.Fatalf("secretbox.New: %v", err)
	}
	store := &fakeStreamInboxStore{purgeCount: 7}
	var logs bytes.Buffer
	worker := newStreamInboxWorker(
		store,
		func(context.Context, channel.InboundMessage) error { return nil },
		box.Seal,
		box.Open,
		slog.New(slog.NewJSONHandler(&logs, nil)),
	)
	fixedNow := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	worker.purgeInterval = time.Millisecond
	worker.retention = 48 * time.Hour
	worker.now = func() time.Time { return fixedNow }

	ctx, cancel := context.WithCancel(context.Background())
	go worker.Run(ctx)
	deadline := time.Now().Add(time.Second)
	for {
		store.mu.Lock()
		purged := len(store.purgeCalls) > 0
		store.mu.Unlock()
		if purged || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	if !worker.WaitWithTimeout(time.Second) {
		t.Fatal("worker did not stop after purge context cancellation")
	}
	store.mu.Lock()
	if len(store.purgeCalls) == 0 {
		store.mu.Unlock()
		t.Fatal("terminal purge was not called")
	}
	cutoff := store.purgeCalls[0]
	store.mu.Unlock()
	if want := fixedNow.Add(-48 * time.Hour); !cutoff.Equal(want) {
		t.Fatalf("purge cutoff = %v, want %v", cutoff, want)
	}
	if !strings.Contains(logs.String(), "dingtalk_stream_inbox_purge_completed") || !strings.Contains(logs.String(), `"deleted_count":7`) {
		t.Fatalf("purge success log missing fields: %s", logs.String())
	}
}

func TestStreamInboxPurgeQueryIsInterruptedByShutdown(t *testing.T) {
	box, err := secretbox.New(bytes.Repeat([]byte{0x18}, 32))
	if err != nil {
		t.Fatalf("secretbox.New: %v", err)
	}
	store := &fakeStreamInboxStore{
		purgeEntered: make(chan struct{}, 1),
		purgeRelease: make(chan struct{}),
	}
	worker := newStreamInboxWorker(
		store,
		func(context.Context, channel.InboundMessage) error { return nil },
		box.Seal,
		box.Open,
		slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
	)
	worker.purgeInterval = time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	go worker.Run(ctx)
	select {
	case <-store.purgeEntered:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("purge query did not start")
	}
	cancel()
	if !worker.WaitWithTimeout(time.Second) {
		t.Fatal("worker did not interrupt the in-flight purge on shutdown")
	}
}

func TestStreamInboxPurgeLogsFailureWithoutSensitiveData(t *testing.T) {
	store := &fakeStreamInboxStore{purgeErr: errors.New("database unavailable")}
	var logs bytes.Buffer
	worker := newStreamInboxWorker(store, nil, nil, nil, slog.New(slog.NewJSONHandler(&logs, nil)))
	worker.now = func() time.Time { return time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC) }
	worker.purgeTerminal(context.Background())
	if !strings.Contains(logs.String(), "dingtalk_stream_inbox_purge_failed") || !strings.Contains(logs.String(), `"error_class":"database"`) {
		t.Fatalf("purge failure log missing fields: %s", logs.String())
	}
}

func TestStreamInboxRetryBackoffCapsAndAdvisoryKeyIsStable(t *testing.T) {
	if got := streamInboxRetryBackoff(0); got != time.Second {
		t.Errorf("first backoff = %v", got)
	}
	if got := streamInboxRetryBackoff(30); got != streamInboxMaxBackoff {
		t.Errorf("capped backoff = %v", got)
	}
	id := util.MustParseUUID("00000000-0000-0000-0000-000000000123")
	if a, b := streamInboxAdvisoryKey(id), streamInboxAdvisoryKey(id); a != b {
		t.Errorf("advisory key changed: %d != %d", a, b)
	}
}

func testStreamInboxRow(t *testing.T, box *secretbox.Box, attemptCount int32, payload string) db.DingtalkStreamInbox {
	t.Helper()
	sealed, err := box.Seal([]byte(payload))
	if err != nil {
		t.Fatalf("seal payload: %v", err)
	}
	return db.DingtalkStreamInbox{
		ID:               util.MustParseUUID("00000000-0000-0000-0000-000000000010"),
		InstallationID:   util.MustParseUUID("00000000-0000-0000-0000-000000000020"),
		ClientID:         "client-1",
		ConnectionID:     "node-a-g1",
		NodeID:           "node-a",
		ReceiverHostname: pgtype.Text{String: "stream-host-a", Valid: true},
		StreamMessageID:  "stream-1",
		PayloadEncrypted: sealed,
		Status:           "processing",
		AttemptCount:     attemptCount,
		LeaseToken:       pgtype.UUID{Bytes: [16]byte{1, 2, 3}, Valid: true},
		ReceivedAt:       pgtype.Timestamptz{Time: time.Now().Add(-time.Second), Valid: true},
	}
}
