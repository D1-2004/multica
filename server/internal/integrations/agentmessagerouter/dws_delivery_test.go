package agentmessagerouter

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/dwsclient"
)

type replySessionStub struct {
	sends     int
	statuses  int
	closes    int
	requests  []dwsclient.SendRequest
	status    replyReceipt
	statusErr error
}

func (s *replySessionStub) Send(_ context.Context, r dwsclient.SendRequest) (replyReceipt, error) {
	s.sends++
	s.requests = append(s.requests, r)
	return replyReceipt{OpenTaskID: "send-1"}, nil
}
func (s *replySessionStub) Status(_ context.Context, id string) (replyReceipt, error) {
	s.statuses++
	if id != "send-1" {
		return replyReceipt{}, errors.New("wrong task")
	}
	return s.status, s.statusErr
}
func (s *replySessionStub) Close() { s.closes++ }

type replySenderStub struct {
	session *replySessionStub
	opened  []DWSDelivery
}

func (s *replySenderStub) Open(_ context.Context, d DWSDelivery) (DWSReplySession, error) {
	s.opened = append(s.opened, d)
	return s.session, nil
}
func testDWSDelivery(agentID string) *DWSDelivery {
	return &DWSDelivery{IdempotencyKey: "router-reply:dispatch-1", AgentID: agentID, Environment: "staging", SenderUID: "1001", SenderOrgID: "2001", OpenConversationID: "cid-1", RecipientOpenDingTalkID: "sender-open", Text: "在的。", Title: "在的。"}
}

func TestDWSReplyResumesAcceptedSendWithoutResending(t *testing.T) {
	d := testDWSDelivery("agent-1")
	raw, err := freezeDWSDelivery(d, d.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	session := &replySessionStub{statusErr: errors.New("temporary status failure")}
	sender := &replySenderStub{session: session}
	w := &CompletionWorker{dwsSender: sender}
	save := func(value []byte) error { raw = append([]byte(nil), value...); return nil }
	if err := w.resumeDWSDelivery(context.Background(), raw, save); err == nil {
		t.Fatal("expected status failure")
	}
	var state dwsDeliveryState
	_ = json.Unmarshal(raw, &state)
	if state.OpenTaskID != "send-1" || state.Status != "accepted" {
		t.Fatalf("acceptance not durable: %+v", state)
	}
	session.statusErr = nil
	session.status = replyReceipt{SendStatus: "delivered", OpenConversationID: "cid-1", OpenMessageID: "msg-1"}
	if err := w.resumeDWSDelivery(context.Background(), raw, save); err != nil {
		t.Fatal(err)
	}
	if session.sends != 1 || session.statuses != 2 || session.closes != 2 {
		t.Fatalf("send/status/close counts: %+v", session)
	}
	if got := session.requests[0]; got.Content != d.Text || got.RecipientOpenDingTalkID != d.RecipientOpenDingTalkID || got.IdempotencyKey != d.IdempotencyKey {
		t.Fatalf("changed reply: %+v", got)
	}
	if err := w.resumeDWSDelivery(context.Background(), raw, save); err != nil {
		t.Fatal(err)
	}
	if session.statuses != 2 {
		t.Fatal("delivered reply was queried or sent again")
	}
}

func TestDWSReplyRejectsIdentityMismatchExpiredSendAndWrongConversation(t *testing.T) {
	d := testDWSDelivery("agent-1")
	if _, err := freezeDWSDelivery(d, "agent-2"); err == nil {
		t.Fatal("wrong callback agent accepted")
	}
	session := &replySessionStub{status: replyReceipt{SendStatus: "delivered", OpenConversationID: "other", OpenMessageID: "msg-1"}}
	sender := &replySenderStub{session: session}
	w := &CompletionWorker{dwsSender: sender}
	raw, _ := json.Marshal(dwsDeliveryState{Delivery: *d, StartedAt: time.Now().Add(-24 * time.Hour), Status: "pending"})
	err := w.resumeDWSDelivery(context.Background(), raw, func([]byte) error { return nil })
	var permanent *dwsDeliveryPermanentError
	if !errors.As(err, &permanent) || len(sender.opened) != 0 {
		t.Fatalf("expired ambiguous send retried: %v", err)
	}
	raw, _ = freezeDWSDelivery(d, d.AgentID)
	err = w.resumeDWSDelivery(context.Background(), raw, func([]byte) error { return nil })
	if !errors.As(err, &permanent) || permanent.code != "delivered_conversation_mismatch" {
		t.Fatalf("wrong conversation accepted: %v", err)
	}
}

func TestCallbackOutboxesResumeDWSAfterWorkerRestart(t *testing.T) {
	for _, kind := range []string{"completion", "update"} {
		t.Run(kind, func(t *testing.T) {
			pool := taskCompletionTestPool(t)
			queries := db.New(pool)
			requests := 0
			var agentID string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				data := map[string]any{"dispatchTaskId": "router-dws-restart", "dwsDelivery": testDWSDelivery(agentID)}
				if kind == "completion" {
					data["executionStatus"] = "completed"
					data["executionReportId"] = "report-1"
				} else {
					var req ExecutionUpdateRequest
					_ = json.NewDecoder(r.Body).Decode(&req)
					data["requestId"] = req.RequestID
					data["updateType"] = req.UpdateType
					data["occurredAt"] = req.OccurredAt
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "code": "success", "data": data})
			}))
			defer server.Close()
			client, err := NewClient(ClientConfig{BaseURL: server.URL, ServiceCredential: "secret"})
			if err != nil {
				t.Fatal(err)
			}
			var id any
			table := "task_completion_outbox"
			if kind == "completion" {
				row := enqueueWorkerTestCompletion(t, queries, client.TargetIdentity(), "dws-restart")
				id = row.ID
				agentID = util.UUIDToString(row.AgentID)
			} else {
				row := enqueueWorkerTestExecutionUpdate(t, queries, client.TargetIdentity(), "dws-restart")
				id = row.ID
				agentID = util.UUIDToString(row.AgentID)
				table = "task_execution_update_outbox"
			}
			t.Cleanup(func() { pool.Exec(context.Background(), "DELETE FROM "+table+" WHERE id=$1", id) })
			session := &replySessionStub{statusErr: errors.New("temporary status failure")}
			sender := &replySenderStub{session: session}
			worker := NewCompletionWorker(queries, client, nil)
			worker.SetDWSReplySender(sender)
			if worked, err := worker.ProcessNext(context.Background()); !worked || err != nil {
				t.Fatalf("worked=%v err=%v", worked, err)
			}
			var raw []byte
			var status string
			if err := pool.QueryRow(context.Background(), "SELECT status,dws_delivery FROM "+table+" WHERE id=$1", id).Scan(&status, &raw); err != nil {
				t.Fatal(err)
			}
			var state dwsDeliveryState
			_ = json.Unmarshal(raw, &state)
			if status != "queued" || state.Status != "accepted" || state.OpenTaskID != "send-1" {
				t.Fatalf("state=%s %s", status, raw)
			}
			if _, err := pool.Exec(context.Background(), "UPDATE "+table+" SET available_at=now() WHERE id=$1", id); err != nil {
				t.Fatal(err)
			}
			session.statusErr = nil
			session.status = replyReceipt{SendStatus: "delivered", OpenConversationID: "cid-1", OpenMessageID: "msg-1"}
			restarted := NewCompletionWorker(queries, client, nil)
			restarted.SetDWSReplySender(sender)
			if worked, err := restarted.ProcessNext(context.Background()); !worked || err != nil {
				t.Fatalf("worked=%v err=%v", worked, err)
			}
			if err := pool.QueryRow(context.Background(), "SELECT status,dws_delivery FROM "+table+" WHERE id=$1", id).Scan(&status, &raw); err != nil {
				t.Fatal(err)
			}
			_ = json.Unmarshal(raw, &state)
			if status != "delivered" || state.OpenMessageID != "msg-1" || requests != 1 || session.sends != 1 {
				t.Fatalf("status=%s callbacks=%d sends=%d state=%s", status, requests, session.sends, raw)
			}
		})
	}
}
