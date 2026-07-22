package dingtalk

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/integrations/channel"
	channelengine "github.com/multica-ai/multica/server/internal/integrations/channel/engine"
	"github.com/multica-ai/multica/server/internal/util/secretbox"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type fakeStreamEmotionQueries struct {
	*fakeTypingQueries
	mu   sync.Mutex
	rows map[string]db.DingtalkProcessingEmotion
	next byte
}

func newFakeStreamEmotionQueries(inst db.ChannelInstallation) *fakeStreamEmotionQueries {
	return &fakeStreamEmotionQueries{
		fakeTypingQueries: &fakeTypingQueries{inst: inst, binding: db.ChannelChatSessionBinding{InstallationID: inst.ID}},
		rows:              make(map[string]db.DingtalkProcessingEmotion),
		next:              30,
	}
}

func streamEmotionKey(installationID pgtype.UUID, sourceMessageID string) string {
	return string(installationID.Bytes[:]) + "\x00" + sourceMessageID
}

func (f *fakeStreamEmotionQueries) BeginDingTalkProcessingEmotion(_ context.Context, arg db.BeginDingTalkProcessingEmotionParams) (db.DingtalkProcessingEmotion, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := streamEmotionKey(arg.InstallationID, arg.SourceMessageID)
	if _, exists := f.rows[key]; exists {
		return db.DingtalkProcessingEmotion{}, pgx.ErrNoRows
	}
	f.next++
	row := db.DingtalkProcessingEmotion{
		ID: typingTestUUID(f.next), InstallationID: arg.InstallationID,
		SourceMessageID: arg.SourceMessageID, OpenConversationID: arg.OpenConversationID,
		OpenMsgID: arg.OpenMsgID, RobotCode: arg.RobotCode, State: "adding",
	}
	f.rows[key] = row
	return row, nil
}

func (f *fakeStreamEmotionQueries) BindDingTalkProcessingEmotionTask(_ context.Context, arg db.BindDingTalkProcessingEmotionTaskParams) (db.DingtalkProcessingEmotion, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := streamEmotionKey(arg.InstallationID, arg.SourceMessageID)
	row, ok := f.rows[key]
	if !ok {
		return db.DingtalkProcessingEmotion{}, pgx.ErrNoRows
	}
	row.ChatSessionID, row.TaskID = arg.ChatSessionID, arg.TaskID
	f.rows[key] = row
	return row, nil
}

func (f *fakeStreamEmotionQueries) MarkDingTalkProcessingEmotionAdded(_ context.Context, id pgtype.UUID) (db.DingtalkProcessingEmotion, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for key, row := range f.rows {
		if row.ID != id {
			continue
		}
		row.AddCompleted = true
		if row.State != "settled" {
			row.State = "active"
		}
		f.rows[key] = row
		return row, nil
	}
	return db.DingtalkProcessingEmotion{}, pgx.ErrNoRows
}

func (f *fakeStreamEmotionQueries) SettleDingTalkProcessingEmotionTask(_ context.Context, arg db.SettleDingTalkProcessingEmotionTaskParams) ([]db.DingtalkProcessingEmotion, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var rows []db.DingtalkProcessingEmotion
	for key, row := range f.rows {
		if row.ChatSessionID == arg.ChatSessionID && row.TaskID == arg.TaskID {
			row.State = "settled"
			f.rows[key] = row
			rows = append(rows, row)
		}
	}
	return rows, nil
}

func (f *fakeStreamEmotionQueries) SettleDingTalkProcessingEmotionSource(_ context.Context, arg db.SettleDingTalkProcessingEmotionSourceParams) ([]db.DingtalkProcessingEmotion, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := streamEmotionKey(arg.InstallationID, arg.SourceMessageID)
	row, ok := f.rows[key]
	if !ok {
		return nil, nil
	}
	row.State = "settled"
	f.rows[key] = row
	return []db.DingtalkProcessingEmotion{row}, nil
}

func (f *fakeStreamEmotionQueries) MarkOrphanedDingTalkProcessingEmotionsSettled(context.Context, int32) ([]db.DingtalkProcessingEmotion, error) {
	return nil, nil
}

func (f *fakeStreamEmotionQueries) ClaimDueDingTalkProcessingEmotions(context.Context, int32) ([]db.DingtalkProcessingEmotion, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	rows := make([]db.DingtalkProcessingEmotion, 0, len(f.rows))
	for _, row := range f.rows {
		if row.State == "adding" || row.State == "settled" {
			rows = append(rows, row)
		}
	}
	return rows, nil
}

func (f *fakeStreamEmotionQueries) RetryDingTalkProcessingEmotion(_ context.Context, arg db.RetryDingTalkProcessingEmotionParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for key, row := range f.rows {
		if row.ID == arg.ID {
			row.AttemptCount++
			row.NextAttemptAt = arg.NextAttemptAt
			f.rows[key] = row
		}
	}
	return nil
}

func (f *fakeStreamEmotionQueries) DeleteDingTalkProcessingEmotion(_ context.Context, id pgtype.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for key, row := range f.rows {
		if row.ID == id {
			delete(f.rows, key)
		}
	}
	return nil
}

func (f *fakeStreamEmotionQueries) rowCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.rows)
}

func (f *fakeStreamEmotionQueries) row(installationID pgtype.UUID, sourceMessageID string) (db.DingtalkProcessingEmotion, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	row, ok := f.rows[streamEmotionKey(installationID, sourceMessageID)]
	return row, ok
}

func historicalStreamInstallation(t *testing.T, id pgtype.UUID) db.ChannelInstallation {
	t.Helper()
	config, err := json.Marshal(dingtalkInstallConfig{
		AppID: "legacy-stream-client", AppSecretEncrypted: "bGVnYWN5LXNlY3JldA==",
	})
	if err != nil {
		t.Fatal(err)
	}
	return db.ChannelInstallation{ID: id, ChannelType: string(TypeDingtalk), Status: "active", Config: config}
}

func TestStreamEmotionDurablyBindsAndRecallsBeforeReply(t *testing.T) {
	rec, srv := newEmotionAPIServer(t)
	inst := historicalStreamInstallation(t, typingTestUUID(21))
	q := newFakeStreamEmotionQueries(inst)
	mgr := NewTypingIndicatorManager(NewRobotMessenger(srv.URL, srv.URL, srv.Client()), plaintextDecrypter, q, nil)

	session, task := typingTestUUID(22), typingTestUUID(23)
	mgr.beginStreamEmotion(context.Background(), inst.ID, "source-1", EmotionTarget{
		OpenConversationID: "cid", OpenMsgID: "msg", RobotCode: "robot-from-callback",
	}, time.Now().UnixMilli())
	mgr.bindStreamEmotion(context.Background(), inst.ID, "source-1", session, task)
	mgr.settleStreamTask(context.Background(), session, task)

	if len(rec.replies) != 1 || len(rec.recalls) != 1 {
		t.Fatalf("emotion calls add=%d recall=%d", len(rec.replies), len(rec.recalls))
	}
	if q.rowCount() != 0 {
		t.Fatalf("durable emotion rows = %d, want 0 after accepted recall", q.rowCount())
	}
}

func TestStreamInboxAddsEmotionBeforeDispatchAndBindsResult(t *testing.T) {
	box, err := secretbox.New(bytes.Repeat([]byte{0x52}, 32))
	if err != nil {
		t.Fatal(err)
	}
	inboxRow := testStreamInboxRow(t, box, 0, `{"msgId":"source-worker","conversationId":"cid-worker","robotCode":"robot-from-callback","senderStaffId":"staff","conversationType":"2","msgtype":"text","text":{"content":"hello"}}`)
	store := &fakeStreamInboxStore{claims: []db.DingtalkStreamInbox{inboxRow}}
	rec, srv := newEmotionAPIServer(t)
	inst := historicalStreamInstallation(t, inboxRow.InstallationID)
	q := newFakeStreamEmotionQueries(inst)
	mgr := NewTypingIndicatorManager(NewRobotMessenger(srv.URL, srv.URL, srv.Client()), plaintextDecrypter, q, nil)
	session, task := typingTestUUID(41), typingTestUUID(42)
	worker := newStreamInboxWorker(store, nil, box.Seal, box.Open, nil)
	worker.typing = mgr
	worker.resultHandler = func(context.Context, channel.InboundMessage) (channelengine.Result, error) {
		if len(rec.replies) != 1 {
			t.Fatalf("dispatch started before processing emotion, adds=%d", len(rec.replies))
		}
		return channelengine.Result{Outcome: channelengine.OutcomeIngested, ChatSessionID: session, TaskID: task}, nil
	}
	worked, err := worker.ProcessNext(context.Background())
	if err != nil || !worked {
		t.Fatalf("ProcessNext worked=%v err=%v", worked, err)
	}
	row, ok := q.row(inst.ID, "source-worker")
	if !ok || row.ChatSessionID != session || row.TaskID != task || row.State != "active" {
		t.Fatalf("bound stream emotion = %+v, present=%v", row, ok)
	}
}

func TestStreamEmotionTerminalWhileAddInFlightRecallsLateAttach(t *testing.T) {
	addStarted := make(chan struct{})
	releaseAdd := make(chan struct{})
	var recalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1.0/oauth2/accessToken":
			_, _ = w.Write([]byte(`{"accessToken":"tok_test","expireIn":7200}`))
		case "/v1.0/robot/emotion/reply":
			close(addStarted)
			<-releaseAdd
			_, _ = w.Write([]byte(`{}`))
		case "/v1.0/robot/emotion/recall":
			recalls.Add(1)
			_, _ = w.Write([]byte(`{}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	inst := historicalStreamInstallation(t, typingTestUUID(24))
	q := newFakeStreamEmotionQueries(inst)
	mgr := NewTypingIndicatorManager(NewRobotMessenger(server.URL, server.URL, server.Client()), plaintextDecrypter, q, nil)
	session, task := typingTestUUID(25), typingTestUUID(26)
	done := make(chan struct{})
	go func() {
		defer close(done)
		mgr.beginStreamEmotion(context.Background(), inst.ID, "source-race", EmotionTarget{
			OpenConversationID: "cid", OpenMsgID: "msg-race", RobotCode: "robot-from-callback",
		}, time.Now().UnixMilli())
	}()
	<-addStarted
	mgr.bindStreamEmotion(context.Background(), inst.ID, "source-race", session, task)
	mgr.settleStreamTask(context.Background(), session, task)
	if recalls.Load() != 0 {
		t.Fatalf("recall ran before the in-flight add completed")
	}
	close(releaseAdd)
	<-done
	if recalls.Load() != 1 || q.rowCount() != 0 {
		t.Fatalf("late attach cleanup recalls=%d rows=%d", recalls.Load(), q.rowCount())
	}
}

func TestStreamEmotionRecallFailureKeepsDurableRetry(t *testing.T) {
	var recallCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1.0/oauth2/accessToken":
			_, _ = w.Write([]byte(`{"accessToken":"tok_test","expireIn":7200}`))
		case "/v1.0/robot/emotion/reply":
			_, _ = w.Write([]byte(`{}`))
		case "/v1.0/robot/emotion/recall":
			if recallCalls.Add(1) == 1 {
				w.WriteHeader(http.StatusBadGateway)
				_, _ = w.Write([]byte(`{"message":"temporary"}`))
				return
			}
			_, _ = w.Write([]byte(`{}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	inst := historicalStreamInstallation(t, typingTestUUID(27))
	q := newFakeStreamEmotionQueries(inst)
	mgr := NewTypingIndicatorManager(NewRobotMessenger(server.URL, server.URL, server.Client()), plaintextDecrypter, q, nil)
	session, task := typingTestUUID(28), typingTestUUID(29)
	mgr.beginStreamEmotion(context.Background(), inst.ID, "source-retry", EmotionTarget{
		OpenConversationID: "cid", OpenMsgID: "msg-retry", RobotCode: "robot-from-callback",
	}, time.Now().UnixMilli())
	mgr.bindStreamEmotion(context.Background(), inst.ID, "source-retry", session, task)
	mgr.settleStreamTask(context.Background(), session, task)
	if q.rowCount() != 1 {
		t.Fatalf("failed recall discarded durable row")
	}
	mgr.reconcileStreamEmotions(context.Background(), q)
	if recallCalls.Load() != 2 || q.rowCount() != 0 {
		t.Fatalf("retry recalls=%d rows=%d", recallCalls.Load(), q.rowCount())
	}
}
