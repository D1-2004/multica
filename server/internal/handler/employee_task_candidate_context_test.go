package handler

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/employeeentry"
	"github.com/multica-ai/multica/server/internal/employeetask"
)

func TestCurrentTaskSourceLinksDisambiguateIdenticalGoals(t *testing.T) {
	c := newCollectionHarness(t)
	ctx := context.Background()
	goal := "用Python实际执行sleep(12)并计算1到5的平方"
	sources := map[string]string{}
	sendSource := func(id, text string) string {
		command := c.f.command
		command.Event.Data.Conversation = DispatchConversation{OpenConversationID: "cid-origin-dm", Type: "single"}
		command.Event.Data.Sender = DispatchSender{DisplayName: "Requester", OpenDingTalkID: "requester-open-id"}
		command.Event.Data.Messages = []DispatchMessage{{OpenMsgID: id, Text: text, OccurredAt: time.Now().UnixMilli(), SenderDisplayName: "Requester", SenderOpenDingTalkID: "requester-open-id"}}
		c.f.command = command
		if response := employeeHTTP(t, c.f, c.dc, uuid.NewString()); response.Code != 202 {
			t.Fatal(response.Body.String())
		}
		var receipt string
		if err := testPool.QueryRow(ctx, `SELECT receipt_id::text FROM employee_event_consumption WHERE agent_id=$1::uuid AND payload#>>'{command,event,data,messages,0,openMsgId}'=$2`, c.f.agentID, id).Scan(&receipt); err != nil {
			t.Fatal(err)
		}
		return receipt + "/" + id
	}

	makeTask := func(topic string) string {
		source := sendSource(topic+"-request", topic+"验收：用Python实际sleep12再算平方")
		c.model.set(func(string) (string, map[string]any) {
			return collectionCall("dispatch-"+topic, "dispatch_task", map[string]any{"source_ref": source, "goal": goal, "prompt": "实际执行Python测试，" + topic + "验收", "reply": "我来实际执行，跑完告诉你。"})
		})
		c.process()
		c.model.set(collectionQuiet)
		var id string
		if err := testPool.QueryRow(ctx, `SELECT id::text FROM employee_task WHERE agent_id=$1::uuid ORDER BY created_at DESC LIMIT 1`, c.f.agentID).Scan(&id); err != nil {
			t.Fatal(err)
		}
		sources[id] = source
		return id
	}
	old := makeTask("旧杉")
	fresh := makeTask("蓝杉")
	// The old task's bookkeeping is newer: updated_at sorting must not decide
	// which identically summarized goal the human named.
	if _, err := testPool.Exec(ctx, `UPDATE employee_task SET updated_at=now() WHERE id=$1::uuid`, old); err != nil {
		t.Fatal(err)
	}
	sendSource("continue-blue", "继续蓝杉验收：把刚才的平方加起来")
	c.process()
	var raw []byte
	if err := testPool.QueryRow(ctx, `SELECT j.input_snapshot FROM employee_scene_job j JOIN employee_event_consumption c ON c.job_id=j.id WHERE c.agent_id=$1::uuid AND c.payload#>>'{command,event,data,messages,0,openMsgId}'=$2`, c.f.agentID, "continue-blue").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var saved employeeSavedInput
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	var brief struct {
		Sources []struct {
			Candidates []map[string]any `json:"candidates"`
		} `json:"sources"`
	}
	if err := json.Unmarshal([]byte(saved.Input.TaskBrief), &brief); err != nil {
		t.Fatal(err)
	}
	byRef := map[string]string{}
	for _, b := range saved.CurrentTasks {
		byRef[b.Ref] = b.TaskID
	}
	seen := map[string]map[string]any{}
	for _, source := range brief.Sources {
		for _, candidate := range source.Candidates {
			seen[byRef[candidate["task_ref"].(string)]] = candidate
		}
	}
	if len(seen) != 2 {
		t.Fatalf("candidate count=%d brief=%s", len(seen), saved.Input.TaskBrief)
	}
	for id, topic := range map[string]string{old: "旧杉", fresh: "蓝杉"} {
		candidate := seen[id]
		links, ok := candidate["human_source_links"].([]any)
		if !ok || len(links) != 1 || !strings.Contains(links[0].(map[string]any)["text"].(string), topic) || links[0].(map[string]any)["source_ref"] == "" {
			t.Fatalf("original human alias lost for %s: %+v", id, candidate)
		}
		if candidate["created_at"] == nil || candidate["latest_run_at_snapshot"] == nil || candidate["has_active_run_at_snapshot"] != true {
			t.Fatalf("execution/source time evidence lost: %+v", candidate)
		}
		if candidate["goal"] != goal || candidate["result_report"] != nil {
			t.Fatal("test needs identical goal and no duplicated report", candidate)
		}
	}
	// A source remains in the Task ledger after an authorized memory withdrawal.
	// It must not re-enter the brief through this new provenance helper.
	parts := strings.SplitN(sources[fresh], "/", 2)
	record, _ := json.Marshal(map[string]string{"source_id": "employee-message:" + parts[0], "evidence_id": parts[1]})
	if _, err := testPool.Exec(ctx, `INSERT INTO employee_learning(id,workspace_id,agent_id,tenant_org_id,scene_id,scope_kind,principal_id,replay_key,record,forgotten_at) SELECT $2::uuid,workspace_id,agent_id,tenant_org_id,scene_id,'private',requester_ref,$3,$4::jsonb,now() FROM employee_task WHERE id=$1::uuid`, fresh, uuid.NewString(), strings.Repeat("s", 64), string(record)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM employee_learning WHERE agent_id=$1::uuid`, c.f.agentID)
	})
	sendSource("after-forget", "蓝杉进度如何")
	c.process()
	var filtered string
	if err := testPool.QueryRow(ctx, `SELECT j.input_snapshot#>>'{input,TaskBrief}' FROM employee_scene_job j JOIN employee_event_consumption c ON c.job_id=j.id WHERE c.agent_id=$1::uuid AND c.payload#>>'{command,event,data,messages,0,openMsgId}'='after-forget'`, c.f.agentID).Scan(&filtered); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(filtered, "蓝杉") {
		t.Fatal("filtered source revived via task brief", filtered)
	}
}

func TestTaskSourceHintsNeverRestoreFilteredOrForgedDialogue(t *testing.T) {
	requester := "dingtalk:org:uid:owner"
	receipt := uuid.NewString()
	ref := receipt + "/m1"
	at := time.Now()
	source := employeeSourceMessage{SourceRef: ref, ReceiptID: receipt, RequesterRef: requester, Message: DispatchMessage{OpenMsgID: "m1", Text: "RAW-SECRET-do-not-copy"}}
	raw, _ := json.Marshal(source)
	task := employeetask.Task{RequesterRef: requester, Scope: employeetask.Scope{TenantOrgID: "org"}}
	entry := employeetask.Entry{Kind: "request", ActorRef: requester, Source: employeetask.Source{Namespace: "employee_scene", Key: receipt + "/call/definition"}, Body: string(raw)}
	visible := map[string]employeeentry.RecentConversationMessage{ref: {Role: "user", SpeakerRef: "uid:owner", ReceiptID: receipt, MessageID: "m1", At: at, Text: "已过滤的可见原请求"}}
	hints := employeeTaskSourceHints(task, []employeetask.Entry{entry}, visible)
	if len(hints) != 1 || hints[0]["text"] != "已过滤的可见原请求" {
		t.Fatal("raw evidence bypassed visible history", hints)
	}
	if hints := employeeTaskSourceHints(task, []employeetask.Entry{entry}, nil); len(hints) != 0 {
		t.Fatal("filtered-out source restored", hints)
	}
	entry.ActorRef = "other"
	if len(employeeTaskSourceHints(task, []employeetask.Entry{entry}, visible)) != 0 {
		t.Fatal("foreign actor source leaked")
	}
	entry.ActorRef = requester
	entry.Source.Key = uuid.NewString() + "/call/definition"
	if len(employeeTaskSourceHints(task, []employeetask.Entry{entry}, visible)) != 0 {
		t.Fatal("unbound receipt source linked")
	}
	entry.Source.Key = receipt + "/call/definition"
	line := visible[ref]
	line.SpeakerRef = "other"
	visible[ref] = line
	if len(employeeTaskSourceHints(task, []employeetask.Entry{entry}, visible)) != 0 {
		t.Fatal("foreign visible dialogue source linked")
	}
}
