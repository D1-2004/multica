package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/service/employeeloop"
	"github.com/multica-ai/multica/server/internal/taskinput"
)

// The native path has two generations and no background Run: a current read
// and one atomic cancellation receipt, unlike the real COL-03 dispatch mistake.
func (c *collectionHarness) nativeCancel(text string, beforeStop ...func()) string {
	c.t.Helper()
	id := "cancel-" + uuid.NewString()
	source := c.send(collectionMessage{conversation: "cid-origin-dm", kind: "single", name: "Requester", openID: "requester-open-id", messageID: id, text: text})
	rounds := 0
	c.model.set(func(request string) (string, map[string]any) {
		rounds++
		if rounds == 1 {
			return collectionCall("cancel-read", "read_task", map[string]any{"source_ref": source, "task_ref": "t1"})
		}
		if rounds != 2 {
			c.t.Fatalf("cancel used %d model calls", rounds)
		}
		if !strings.Contains(request, "collections") || !strings.Contains(request, "has_active_run") {
			c.t.Fatal("read lacked current collection/run facts")
		}
		for _, fn := range beforeStop {
			fn()
		}
		return collectionCall("cancel-native", "cancel_collection", map[string]any{"source_ref": source, "task_ref": "t1", "read_ref": "cancel-read", "instruction_quote": text})
	})
	c.process()
	c.model.set(collectionQuiet)
	if rounds != 2 {
		c.t.Fatalf("cancel model calls=%d", rounds)
	}
	return id
}

func TestCollectionNativeCancelWaitingAndLateAnswer(t *testing.T) {
	for _, partial := range []bool{false, true} {
		name := "open"
		if partial {
			name = "partial_ready"
		}
		t.Run(name, func(t *testing.T) {
			c := newCollectionHarness(t)
			ctx := context.Background()
			c.seed()
			col := c.origin("问Carol和Dave本周签了几单", collectionParticipants[0], collectionParticipants[1])
			c.deliver(col.ID, map[string]string{"Carol": "cid-carol-dm", "Dave": "cid-team-group"})
			var ready *taskinput.ReadyIntent
			if partial {
				_, readyErr, err := taskinput.NewStore(testPool).CloseCollectionTx(ctx, c.scope(), taskinput.CloseParams{CollectionID: col.ID, Mode: taskinput.ClosePartial, Reason: "先总结已有回复", Source: taskinput.Source{Namespace: "test", Key: "partial"}, Authority: taskinput.Authority{ActorRef: col.RequesterRef, SceneID: col.OriginSceneID, ReceiptRef: "partial", VerifiedAt: time.Now()}, ExpectedRevision: col.Revision})
				if err != nil {
					t.Fatal(err)
				}
				ready = readyErr
			}
			msg := c.nativeCancel("这次收集取消，不用再问，也不要继续汇总。")
			got, err := taskinput.NewStore(testPool).GetCollection(ctx, c.scope(), col.ID)
			if err != nil || got.State != taskinput.CollectionCancelled {
				t.Fatalf("collection=%+v err=%v", got, err)
			}
			var state string
			var runs, openWaits int
			if err := testPool.QueryRow(ctx, `SELECT state,(SELECT count(*) FROM employee_task_run WHERE task_id=t.id),(SELECT count(*) FROM employee_task_wait WHERE task_id=t.id AND state='open') FROM employee_task t WHERE id=$1::uuid`, col.TaskID).Scan(&state, &runs, &openWaits); err != nil {
				t.Fatal(err)
			}
			if state != "cancelled" || runs != 0 || openWaits != 0 {
				t.Fatalf("goal=%s runs=%d waits=%d", state, runs, openWaits)
			}
			var raw []byte
			var jobID string
			if err := testPool.QueryRow(ctx, `SELECT j.id::text,j.tool_journal->'cancel-native'->'result' FROM employee_scene_job j JOIN employee_event_consumption c ON c.job_id=j.id WHERE c.agent_id=$1::uuid AND c.payload#>>'{command,event,data,messages,0,openMsgId}'=$2`, c.f.agentID, msg).Scan(&jobID, &raw); err != nil {
				t.Fatal(err)
			}
			var record employeeToolRecord
			if json.Unmarshal(raw, &record) != nil || record.Result.Terminal == nil {
				t.Fatalf("no receipt=%s", raw)
			}
			var receipt employeeStopReceipt
			if err := json.Unmarshal([]byte(record.Result.Content), &receipt); err != nil {
				t.Fatal(err)
			}
			if receipt.TaskState != "cancelled" || len(receipt.CancelledCollections) != 1 || receipt.CancelledCollections[0] != col.ID || !receipt.ExitConfirmed || receipt.RunID != "" || !strings.Contains(record.Result.Terminal.Reply, "已取消") {
				t.Fatalf("unproven cancellation=%+v result=%+v", receipt, record.Result)
			}
			before := c.model.count()
			if _, err := testPool.Exec(ctx, `UPDATE employee_scene_job SET state='pending',available_at=now(),outcome=NULL WHERE id=$1::uuid`, jobID); err != nil {
				t.Fatal(err)
			}
			c.process()
			if c.model.count() != before {
				t.Fatal("replay generated again")
			}
			if n := c.count(`SELECT count(*) FROM employee_task_entry WHERE task_id=$1::uuid AND payload->>'operation'='stop'`, col.TaskID); n != 1 {
				t.Fatalf("stop entries=%d", n)
			}
			if ready != nil {
				intent, err := taskinput.NewStore(testPool).GetReadyIntent(ctx, c.scope(), col.ID, ready.CollectionRevision)
				if err != nil || intent.State != taskinput.ReadyIntentSuperseded {
					t.Fatalf("intent=%+v err=%v", intent, err)
				}
				if _, err := taskinput.NewStore(testPool).CompleteCollectionTx(ctx, c.scope(), taskinput.CompleteParams{CollectionID: col.ID, SummaryRef: "late-summary", Source: taskinput.Source{Namespace: "test", Key: "late"}, ExpectedRevision: ready.CollectionRevision}); !errors.Is(err, taskinput.ErrClosed) {
					t.Fatal("stale summary accepted", err)
				}
			}
			c.send(collectionMessage{conversation: "cid-carol-dm", kind: "single", name: "Carol", openID: "carol-open", messageID: "late-carol", text: "5单", quoted: "pm-Carol"})
			c.send(collectionMessage{conversation: "cid-team-group", kind: "group", title: "B组", name: "Dave", openID: "dave-open", messageID: "late-dave", text: "6单", quoted: "pm-Dave"})
			c.process()
			if _, err := c.f.h.EmployeeSceneWorker.ReconcileEmployeeCollections(ctx, 50); err != nil {
				t.Fatal(err)
			}
			c.process()
			if n := c.count(`SELECT count(*) FROM employee_task_input WHERE collection_id=$1::uuid`, col.ID); n != 0 {
				t.Fatalf("late inputs=%d", n)
			}
			if n := c.count(`SELECT count(*) FROM employee_task_ready_intent WHERE collection_id=$1::uuid AND state IN ('pending','admitted')`, col.ID); n != 0 {
				t.Fatalf("ready intents=%d", n)
			}
			if n := c.count(`SELECT count(*) FROM employee_task WHERE agent_id=$1::uuid`, c.f.agentID); n != 1 {
				t.Fatalf("cancel spawned background task=%d", n)
			}
		})
	}
}

func TestCollectionCancelRejectsInvalidReadAndNonCollection(t *testing.T) {
	for _, fault := range []string{"no_read", "wrong_quote", "wrong_task_ref", "revoked_principal", "stale_version", "non_collection"} {
		t.Run(fault, func(t *testing.T) {
			f, host, id, source := employeeCurrentTaskHost(t, "running", "停止这个任务")
			ctx := context.Background()
			if fault != "no_read" {
				if _, err := host.Execute(ctx, id, employeeReadCall(source)); err != nil {
					t.Fatal(err)
				}
			}
			call := employeeStopCall(source)
			call.Name = "cancel_collection"
			switch fault {
			case "wrong_quote":
				call.Arguments["instruction_quote"] = "未在原句里"
			case "wrong_task_ref":
				call.Arguments["task_ref"] = "t9"
			case "revoked_principal":
				if _, err := testPool.Exec(ctx, `UPDATE employee_event_consumption SET principal_id=$2::uuid WHERE job_id=$1::uuid`, host.job.ID, uuid.NewString()); err != nil {
					t.Fatal(err)
				}
			case "stale_version":
				if _, err := testPool.Exec(ctx, `UPDATE employee_task SET version=version+1 WHERE agent_id=$1`, f.agentID); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := host.Execute(ctx, id, call); err == nil {
				t.Fatal("invalid cancellation accepted")
			}
			if n := cancellationCount(t, f.agentID); n != 0 {
				t.Fatalf("refused cancel changed goal: %d", n)
			}
		})
	}
}
func cancellationCount(t *testing.T, agent string) int {
	t.Helper()
	var n int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM employee_task_entry WHERE agent_id=$1::uuid AND payload->>'operation'='stop'`, agent).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestCollectionCancelToolContract(t *testing.T) {
	registry := employeeloop.NewToolRegistry()
	registry.Register(employeeCancelCollectionTool())
	if valid, _ := registry.Validate("cancel_collection", map[string]any{"source_ref": "s", "task_ref": "t1", "read_ref": "r"}); valid {
		t.Fatal("missing current instruction accepted")
	}
	var epoch int
	if _, err := fmt.Sscanf(EmployeeLoopReplicaMarker, "[employee-loop:%d]", &epoch); err != nil || epoch < 16 {
		t.Fatal("new frozen tool has no reader gate")
	}
}

func TestCollectionNativeCancelNoRunRejectsChangedAuthorityAndRead(t *testing.T) {
	for _, fault := range []string{"no_read", "stale_version", "changed_principal", "wrong_requester"} {
		t.Run(fault, func(t *testing.T) {
			c := newCollectionHarness(t)
			ctx := context.Background()
			c.seed()
			col := c.origin("问Carol签了几单", collectionParticipants[0])
			source := c.send(collectionMessage{conversation: "cid-origin-dm", kind: "single", name: "Requester", openID: "requester-open-id", messageID: "refused-cancel", text: "取消这个收集"})
			round := 0
			c.model.set(func(string) (string, map[string]any) {
				round++
				if round == 1 && fault != "no_read" {
					return collectionCall("cancel-read", "read_task", map[string]any{"source_ref": source, "task_ref": "t1"})
				}
				if round > 2 || (fault == "no_read" && round > 1) {
					return collectionQuiet("")
				}
				switch fault {
				case "stale_version":
					if _, err := testPool.Exec(ctx, `UPDATE employee_task SET version=version+1 WHERE id=$1::uuid`, col.TaskID); err != nil {
						t.Fatal(err)
					}
				case "changed_principal":
					if _, err := testPool.Exec(ctx, `UPDATE employee_event_consumption SET principal_id=$3::uuid WHERE agent_id=$1::uuid AND payload#>>'{command,event,data,messages,0,openMsgId}'=$2`, c.f.agentID, "refused-cancel", uuid.NewString()); err != nil {
						t.Fatal(err)
					}
				case "wrong_requester":
					if _, err := testPool.Exec(ctx, `UPDATE employee_task SET requester_ref='other' WHERE id=$1::uuid`, col.TaskID); err != nil {
						t.Fatal(err)
					}
				}
				return collectionCall("refused-native", "cancel_collection", map[string]any{"source_ref": source, "task_ref": "t1", "read_ref": "cancel-read", "instruction_quote": "取消这个收集"})
			})
			c.process()
			c.model.set(collectionQuiet)
			got, err := taskinput.NewStore(testPool).GetCollection(ctx, c.scope(), col.ID)
			if err != nil || got.State != taskinput.CollectionOpen {
				t.Fatalf("refusal changed collection=%+v %v", got, err)
			}
			if n := cancellationCount(t, c.f.agentID); n != 0 {
				t.Fatal("refusal committed stop", n)
			}
			var raw []byte
			if err := testPool.QueryRow(ctx, `SELECT j.tool_journal->'refused-native'->'result' FROM employee_scene_job j JOIN employee_event_consumption c ON c.job_id=j.id WHERE c.agent_id=$1::uuid AND c.payload#>>'{command,event,data,messages,0,openMsgId}'=$2`, c.f.agentID, "refused-cancel").Scan(&raw); err != nil {
				t.Fatal(err)
			}
			var record employeeToolRecord
			if json.Unmarshal(raw, &record) != nil || record.Failure == "" || record.Result.Receipt != "" {
				t.Fatalf("refusal claimed receipt=%s", raw)
			}
		})
	}
}

func TestCollectionNativeCancelVersusLastAnswerReady(t *testing.T) {
	for _, ordered := range []bool{false, true} {
		name := "concurrent"
		if ordered {
			name = "ready_first"
		}
		t.Run(name, func(t *testing.T) {
			c := newCollectionHarness(t)
			ctx := context.Background()
			c.seed()
			col := c.origin("问Carol签了几单", collectionParticipants[0])
			c.deliver(col.ID, map[string]string{"Carol": "cid-carol-dm"})
			inv := c.invitations(col.ID)["Carol"]
			start := make(chan struct{})
			done := make(chan error, 1)
			go func() {
				<-start
				bound, cancel := context.WithTimeout(ctx, 5*time.Second)
				defer cancel()
				_, err := taskinput.NewStore(testPool).AcceptInputTx(bound, c.scope(), taskinput.AcceptInputParams{CollectionID: col.ID, InvitationID: inv.ID, Source: taskinput.Source{Namespace: "native-race", Key: "last-answer"}, Authority: taskinput.Authority{ActorRef: inv.ParticipantRef, SceneID: inv.TargetSceneID, ReceiptRef: "last-answer", VerifiedAt: time.Now()}, SenderKind: taskinput.SenderPerson, MessageKind: taskinput.MessageText, Binding: taskinput.BindReplyChain, ProviderMessageID: "last-answer", OccurredAt: time.Now(), Body: "6单", ExpectedRevision: col.Revision})
				done <- err
			}()
			var acceptedErr error
			c.nativeCancel("取消这个收集，不用再汇总。", func() {
				close(start)
				if ordered {
					acceptedErr = <-done
				}
			})
			if !ordered {
				acceptedErr = <-done
			}
			if acceptedErr != nil && !errors.Is(acceptedErr, taskinput.ErrClosed) && !errors.Is(acceptedErr, taskinput.ErrStaleRevision) && !errors.Is(acceptedErr, taskinput.ErrConflict) {
				t.Fatal("cancel/ready race failed", acceptedErr)
			}
			got, err := taskinput.NewStore(testPool).GetCollection(ctx, c.scope(), col.ID)
			if err != nil || got.State != taskinput.CollectionCancelled {
				t.Fatalf("collection=%+v err=%v", got, err)
			}
			if n := c.count(`SELECT count(*) FROM employee_task_ready_intent WHERE collection_id=$1::uuid AND state IN ('pending','admitted')`, col.ID); n != 0 {
				t.Fatal("cancel retained ready intent", n)
			}
			if _, err := c.f.h.EmployeeSceneWorker.ReconcileEmployeeCollections(ctx, 50); err != nil {
				t.Fatal(err)
			}
			c.process()
			var state string
			if err := testPool.QueryRow(ctx, `SELECT state FROM employee_task WHERE id=$1::uuid`, col.TaskID).Scan(&state); err != nil || state != "cancelled" {
				t.Fatal("ready revived goal", state, err)
			}
		})
	}
}
