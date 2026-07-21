package dingtalk

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// emotionAPIServer fakes the DingTalk token + emotion endpoints and
// records emotion posts.
type emotionAPIServer struct {
	mu      sync.Mutex
	replies []map[string]any
	recalls []map[string]any
}

func newEmotionAPIServer(t *testing.T) (*emotionAPIServer, *httptest.Server) {
	t.Helper()
	rec := &emotionAPIServer{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1.0/oauth2/accessToken":
			_, _ = w.Write([]byte(`{"accessToken":"tok_test","expireIn":7200}`))
		case "/v1.0/robot/emotion/reply", "/v1.0/robot/emotion/recall":
			if got := r.Header.Get("x-acs-dingtalk-access-token"); got != "tok_test" {
				t.Errorf("emotion call missing access token, got %q", got)
			}
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			rec.mu.Lock()
			if r.URL.Path == "/v1.0/robot/emotion/reply" {
				rec.replies = append(rec.replies, body)
			} else {
				rec.recalls = append(rec.recalls, body)
			}
			rec.mu.Unlock()
			_, _ = w.Write([]byte(`{}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return rec, srv
}

func testInstallationRow(t *testing.T, id pgtype.UUID, clientID string) db.ChannelInstallation {
	t.Helper()
	cfg, err := json.Marshal(dingtalkInstallConfig{
		AppID:              clientID,
		RobotCode:          "robot_" + clientID,
		AppSecretEncrypted: base64.StdEncoding.EncodeToString([]byte("secret_" + clientID)),
	})
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	return db.ChannelInstallation{ID: id, ChannelType: string(TypeDingtalk), Config: cfg, Status: "active"}
}

// plaintextDecrypter round-trips the "ciphertext" unchanged.
func plaintextDecrypter(b []byte) ([]byte, error) { return b, nil }

type fakeTypingQueries struct {
	binding    db.ChannelChatSessionBinding
	inst       db.ChannelInstallation
	calls      int
	indicators []db.ChannelTypingIndicator
	pending    []db.ListPendingChatMessagePreviewsAfterTaskRow
	tasks      map[pgtype.UUID]db.AgentTaskQueue
}

func (f *fakeTypingQueries) GetAgentTask(_ context.Context, id pgtype.UUID) (db.AgentTaskQueue, error) {
	if task, ok := f.tasks[id]; ok {
		return task, nil
	}
	return db.AgentTaskQueue{ID: id}, nil
}

func (f *fakeTypingQueries) GetChannelChatSessionBindingBySession(_ context.Context, _ db.GetChannelChatSessionBindingBySessionParams) (db.ChannelChatSessionBinding, error) {
	f.calls++
	return f.binding, nil
}

func (f *fakeTypingQueries) GetChannelInstallation(_ context.Context, _ db.GetChannelInstallationParams) (db.ChannelInstallation, error) {
	return f.inst, nil
}

func (f *fakeTypingQueries) ListPendingChatMessagePreviewsAfterTask(_ context.Context, _ pgtype.UUID) ([]db.ListPendingChatMessagePreviewsAfterTaskRow, error) {
	return append([]db.ListPendingChatMessagePreviewsAfterTaskRow(nil), f.pending...), nil
}

func typingTestUUID(b byte) pgtype.UUID {
	var raw [16]byte
	raw[15] = b
	return pgtype.UUID{Bytes: raw, Valid: true}
}

func TestTypingIndicatorAddAndClear(t *testing.T) {
	rec, srv := newEmotionAPIServer(t)
	messenger := NewRobotMessenger(srv.URL, srv.URL, srv.Client())

	instID := typingTestUUID(1)
	instRow := testInstallationRow(t, instID, "client_a")
	q := &fakeTypingQueries{
		binding: db.ChannelChatSessionBinding{InstallationID: instID},
		inst:    instRow,
	}
	mgr := NewTypingIndicatorManager(messenger, plaintextDecrypter, q, nil)

	session := typingTestUUID(2)
	task := typingTestUUID(3)
	target := EmotionTarget{OpenConversationID: "cid_1", OpenMsgID: "msg_1"}
	mgr.Add(context.Background(), instRow, session, task, target, time.Now().UnixMilli())

	if len(rec.replies) != 1 {
		t.Fatalf("expected 1 emotion reply, got %d", len(rec.replies))
	}
	reply := rec.replies[0]
	if reply["robotCode"] != "robot_client_a" || reply["openMsgId"] != "msg_1" || reply["openConversationId"] != "cid_1" {
		t.Fatalf("unexpected reply payload: %v", reply)
	}
	if reply["emotionType"] != float64(2) || reply["textEmotion"] == nil {
		t.Fatalf("expected text emotion payload, got %v", reply)
	}

	mgr.Clear(context.Background(), session)
	if len(rec.recalls) != 1 {
		t.Fatalf("expected 1 emotion recall, got %d", len(rec.recalls))
	}
	if rec.recalls[0]["openMsgId"] != "msg_1" {
		t.Fatalf("recall targets wrong message: %v", rec.recalls[0])
	}

	// Second clear is a state no-op: no further lookups or recalls.
	q.calls = 0
	mgr.Clear(context.Background(), session)
	if q.calls != 0 || len(rec.recalls) != 1 {
		t.Fatalf("expected cleared session to be a no-op, lookups=%d recalls=%d", q.calls, len(rec.recalls))
	}
}

func TestTypingIndicatorSkipsStaleMessages(t *testing.T) {
	rec, srv := newEmotionAPIServer(t)
	messenger := NewRobotMessenger(srv.URL, srv.URL, srv.Client())
	instRow := testInstallationRow(t, typingTestUUID(1), "client_a")
	mgr := NewTypingIndicatorManager(messenger, plaintextDecrypter, &fakeTypingQueries{}, nil)

	stale := time.Now().Add(-3 * time.Minute).UnixMilli()
	mgr.Add(context.Background(), instRow, typingTestUUID(2), typingTestUUID(3), EmotionTarget{OpenConversationID: "cid", OpenMsgID: "m"}, stale)

	if len(rec.replies) != 0 {
		t.Fatalf("stale message must not get an emotion, got %d", len(rec.replies))
	}
}

func TestTypingIndicatorSkipsEmptyTarget(t *testing.T) {
	rec, srv := newEmotionAPIServer(t)
	messenger := NewRobotMessenger(srv.URL, srv.URL, srv.Client())
	instRow := testInstallationRow(t, typingTestUUID(1), "client_a")
	mgr := NewTypingIndicatorManager(messenger, plaintextDecrypter, &fakeTypingQueries{}, nil)

	mgr.Add(context.Background(), instRow, typingTestUUID(2), typingTestUUID(3), EmotionTarget{OpenMsgID: "m"}, 0)
	mgr.Add(context.Background(), instRow, typingTestUUID(2), typingTestUUID(3), EmotionTarget{OpenConversationID: "cid"}, 0)

	if len(rec.replies) != 0 {
		t.Fatalf("empty target must not get an emotion, got %d", len(rec.replies))
	}
}

// The pending-indicator store, faked in memory with the DELETE...RETURNING
// semantics the real query has: a take claims the rows and empties the set.
func (f *fakeTypingQueries) AddChannelTypingIndicator(_ context.Context, arg db.AddChannelTypingIndicatorParams) error {
	f.indicators = append(f.indicators, db.ChannelTypingIndicator{
		ChatSessionID:  arg.ChatSessionID,
		ChannelType:    arg.ChannelType,
		InstallationID: arg.InstallationID,
		Target:         arg.Target,
	})
	return nil
}

func (f *fakeTypingQueries) TakeChannelTypingIndicators(_ context.Context, arg db.TakeChannelTypingIndicatorsParams) ([]db.ChannelTypingIndicator, error) {
	var taken, kept []db.ChannelTypingIndicator
	for _, row := range f.indicators {
		if row.ChatSessionID == arg.ChatSessionID && row.ChannelType == arg.ChannelType {
			taken = append(taken, row)
			continue
		}
		kept = append(kept, row)
	}
	f.indicators = kept
	return taken, nil
}

func (f *fakeTypingQueries) TakeChannelTypingIndicatorsByTask(_ context.Context, arg db.TakeChannelTypingIndicatorsByTaskParams) ([]db.ChannelTypingIndicator, error) {
	var taken, kept []db.ChannelTypingIndicator
	for _, row := range f.indicators {
		var target typingIndicatorTarget
		_ = json.Unmarshal(row.Target, &target)
		if row.ChatSessionID == arg.ChatSessionID && row.ChannelType == arg.ChannelType && target.TaskID == arg.TaskID {
			taken = append(taken, row)
			continue
		}
		kept = append(kept, row)
	}
	f.indicators = kept
	return taken, nil
}

func TestTypingIndicatorClearTaskKeepsLaterTurn(t *testing.T) {
	rec, srv := newEmotionAPIServer(t)
	messenger := NewRobotMessenger(srv.URL, srv.URL, srv.Client())
	instID := typingTestUUID(1)
	instRow := testInstallationRow(t, instID, "client_a")
	q := &fakeTypingQueries{
		binding: db.ChannelChatSessionBinding{InstallationID: instID},
		inst:    instRow,
	}
	mgr := NewTypingIndicatorManager(messenger, plaintextDecrypter, q, nil)
	session := typingTestUUID(2)
	firstTask := typingTestUUID(3)
	secondTask := typingTestUUID(4)

	mgr.Add(context.Background(), instRow, session, firstTask,
		EmotionTarget{OpenConversationID: "cid", OpenMsgID: "m1"}, 0)
	mgr.Add(context.Background(), instRow, session, secondTask,
		EmotionTarget{OpenConversationID: "cid", OpenMsgID: "m2"}, 0)
	mgr.ClearTask(context.Background(), session, firstTask)

	if len(rec.recalls) != 1 || rec.recalls[0]["openMsgId"] != "m1" {
		t.Fatalf("first task clear recalled %v, want only m1", rec.recalls)
	}
	if len(q.indicators) != 1 {
		t.Fatalf("later task indicator count = %d, want 1", len(q.indicators))
	}
	var remaining typingIndicatorTarget
	if err := json.Unmarshal(q.indicators[0].Target, &remaining); err != nil {
		t.Fatalf("decode remaining indicator: %v", err)
	}
	if remaining.OpenMsgID != "m2" || remaining.TaskID != util.UUIDToString(secondTask) {
		t.Fatalf("remaining indicator = %+v, want second turn", remaining)
	}
}

// The regression this table exists for: the emotion is added by the replica the
// WS lease pinned the ingest to, but it is cleared by whichever replica served
// the daemon's completion POST — a plain load-balanced call. Two managers over
// one store stand in for the two replicas.
func TestTypingIndicatorClearsFromAnotherReplica(t *testing.T) {
	rec, srv := newEmotionAPIServer(t)
	messenger := NewRobotMessenger(srv.URL, srv.URL, srv.Client())

	instID := typingTestUUID(1)
	instRow := testInstallationRow(t, instID, "client_a")
	// One store, two managers: the shared DB both replicas talk to.
	shared := &fakeTypingQueries{
		binding: db.ChannelChatSessionBinding{InstallationID: instID},
		inst:    instRow,
	}
	replicaA := NewTypingIndicatorManager(messenger, plaintextDecrypter, shared, nil)
	replicaB := NewTypingIndicatorManager(messenger, plaintextDecrypter, shared, nil)

	session := typingTestUUID(2)
	task := typingTestUUID(3)
	replicaA.Add(context.Background(), instRow, session, task,
		EmotionTarget{OpenConversationID: "cid_1", OpenMsgID: "msg_1"}, 0)
	rec.mu.Lock()
	adds := len(rec.replies)
	rec.mu.Unlock()
	if adds != 1 {
		t.Fatalf("replica A should have added the emotion, got %d adds", adds)
	}

	// The run completes; the daemon's POST lands on the other replica.
	replicaB.ClearTask(context.Background(), session, task)

	rec.mu.Lock()
	recalls := append([]map[string]any(nil), rec.recalls...)
	rec.mu.Unlock()
	if len(recalls) != 1 {
		t.Fatalf("replica B must recall the emotion replica A added, got %d recalls", len(recalls))
	}
	if recalls[0]["openMsgId"] != "msg_1" {
		t.Errorf("recalled the wrong message: %v", recalls[0])
	}

	// The take is the claim: a racing clear on replica A finds nothing left.
	replicaA.ClearTask(context.Background(), session, task)
	rec.mu.Lock()
	total := len(rec.recalls)
	rec.mu.Unlock()
	if total != 1 {
		t.Errorf("a second clear must not recall again, got %d recalls", total)
	}
}

func TestHistoricalStreamTypingUsesCallbackRobotCodeAcrossReplicas(t *testing.T) {
	rec, srv := newEmotionAPIServer(t)
	messenger := NewRobotMessenger(srv.URL, srv.URL, srv.Client())

	instID := typingTestUUID(11)
	config, err := json.Marshal(dingtalkInstallConfig{
		AppID:              "legacy-stream-client",
		AppSecretEncrypted: base64.StdEncoding.EncodeToString([]byte("legacy-stream-secret")),
	})
	if err != nil {
		t.Fatal(err)
	}
	instRow := db.ChannelInstallation{
		ID: instID, ChannelType: string(TypeDingtalk), Status: "active", Config: config,
	}
	shared := &fakeTypingQueries{
		binding: db.ChannelChatSessionBinding{InstallationID: instID},
		inst:    instRow,
	}
	replicaA := NewTypingIndicatorManager(messenger, plaintextDecrypter, shared, nil)
	replicaB := NewTypingIndicatorManager(messenger, plaintextDecrypter, shared, nil)

	session := typingTestUUID(12)
	task := typingTestUUID(13)
	replicaA.Add(context.Background(), instRow, session, task, EmotionTarget{
		OpenConversationID: "legacy-cid",
		OpenMsgID:          "legacy-msg",
		RobotCode:          "robot-from-stream-callback",
	}, time.Now().UnixMilli())

	if len(rec.replies) != 1 || rec.replies[0]["robotCode"] != "robot-from-stream-callback" {
		t.Fatalf("stream emotion reply = %#v", rec.replies)
	}
	if len(shared.indicators) != 1 {
		t.Fatalf("persisted indicators = %d", len(shared.indicators))
	}
	var persisted typingIndicatorTarget
	if err := json.Unmarshal(shared.indicators[0].Target, &persisted); err != nil {
		t.Fatalf("decode persisted target: %v", err)
	}
	if persisted.RobotCode != "robot-from-stream-callback" {
		t.Fatalf("persisted robot code = %q", persisted.RobotCode)
	}

	replicaB.ClearTask(context.Background(), session, task)
	if len(rec.recalls) != 1 || rec.recalls[0]["robotCode"] != "robot-from-stream-callback" {
		t.Fatalf("stream emotion recall = %#v", rec.recalls)
	}
}
