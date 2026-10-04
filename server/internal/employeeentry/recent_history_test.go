package employeeentry

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func recentHistoryDatabase(t *testing.T) (fixture, time.Time) {
	t.Helper()
	f := database(t)
	_, self, _, _ := runtime.Caller(0)
	apply(t, f.pool, filepath.Join(filepath.Dir(self), "..", "..", "migrations", "9144_response_action.up.sql"))
	apply(t, f.pool, filepath.Join(filepath.Dir(self), "..", "..", "migrations", "9620_employee_memory.up.sql"))
	return f, time.Now().UTC().Truncate(time.Microsecond)
}

func recentHistoryInput(t *testing.T, f fixture, scope Scope, principal, text, messageID string, at time.Time) (string, string) {
	t.Helper()
	ctx := context.Background()
	receipt := uuid.NewString()
	_, err := f.pool.Exec(ctx, `INSERT INTO scene_event_receipt SELECT $1::uuid,$3::uuid,$4::uuid,$5::uuid,$6,source,$1::text,fingerprint,envelope,$7::uuid,route,state,reason,config_version,$8 FROM scene_event_receipt WHERE id=$2::uuid`, receipt, f.admission.Item.ReceiptID, scope.WorkspaceID, scope.AgentID, principal, scope.TenantOrgID, scope.SceneID, at)
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]any{"principal_id": principal, "command": map[string]any{"event_receipt_id": receipt, "agent_scene": map[string]any{"scene_id": scope.SceneID}, "externalIdentity": map[string]any{"dws": map[string]any{"uid": "123", "orgId": scope.TenantOrgID}}, "event": map[string]any{"data": map[string]any{"messages": []any{map[string]any{"openMsgId": messageID, "text": text, "senderDisplayName": "colleague", "senderOpenDingTalkId": "source-open"}}}}}})
	job := uuid.NewString()
	if _, err := f.pool.Exec(ctx, `INSERT INTO employee_event_consumption(workspace_id,agent_id,tenant_org_id,scene_id,receipt_id,principal_id,owner_loop,config_revision,payload,job_id,state,created_at) VALUES($1::uuid,$2::uuid,$3,$4::uuid,$5::uuid,$6::uuid,'employee','test',$7::jsonb,$8::uuid,'completed',$9)`, scope.WorkspaceID, scope.AgentID, scope.TenantOrgID, scope.SceneID, receipt, principal, payload, job, at); err != nil {
		t.Fatal(err)
	}
	return receipt, job
}

func recentHistoryMemoryEvidence(t *testing.T, f fixture, scope Scope, requester, receipt, message, state string) {
	t.Helper()
	record, _ := json.Marshal(map[string]string{"source_id": "employee-message:" + receipt, "evidence_id": message, "insight": "not used for matching"})
	if _, err := f.pool.Exec(context.Background(), `INSERT INTO employee_learning(id,workspace_id,agent_id,tenant_org_id,scene_id,scope_kind,principal_id,replay_key,record,superseded_by,forgotten_at) VALUES($1::uuid,$2::uuid,$3::uuid,$4,$5::uuid,'private',$6,$7,$8::jsonb,CASE WHEN $9='superseded' THEN $1::uuid ELSE NULL END,CASE WHEN $9='forgotten' THEN now() ELSE NULL END)`, uuid.NewString(), scope.WorkspaceID, scope.AgentID, scope.TenantOrgID, scope.SceneID, requester, strings.Repeat("f", 64), record, state); err != nil {
		t.Fatal(err)
	}
}

func TestRecentConversationOmitsWithdrawnMemoryEvidenceWithoutErasingDialogue(t *testing.T) {
	for _, state := range []string{"active", "superseded", "forgotten"} {
		t.Run(state, func(t *testing.T) {
			f, before := recentHistoryDatabase(t)
			scope, principal := f.admission.Scope, f.admission.Item.PrincipalID
			receipt, job := recentHistoryInput(t, f, scope, principal, "请记住 WITHDRAWN_MEMORY_VALUE", "remember", before.Add(-3*time.Minute))
			if _, err := f.pool.Exec(context.Background(), `UPDATE employee_event_consumption SET payload=jsonb_set(payload,'{command,event,data,messages}',(payload#>'{command,event,data,messages}') || '[{"openMsgId":"ordinary-same-window","text":"同窗的普通事项仍须保留","senderOpenDingTalkId":"source-open"}]'::jsonb) WHERE receipt_id=$1::uuid`, receipt); err != nil {
				t.Fatal(err)
			}
			recentHistoryReply(t, f, scope, job, "delivered", "已记住 WITHDRAWN_MEMORY_VALUE", "capture-reply", "cid-test", before.Add(-150*time.Second))
			otherReceipt, _ := recentHistoryInput(t, f, scope, principal, "另一条同窗问题也须保留", "second-receipt", before.Add(-160*time.Second))
			if _, err := f.pool.Exec(context.Background(), `UPDATE employee_event_consumption SET job_id=$2::uuid WHERE receipt_id=$1::uuid`, otherReceipt, job); err != nil {
				t.Fatal(err)
			}
			recentHistoryReply(t, f, scope, uuid.NewSHA1(uuid.MustParse(job), []byte("receipt:"+otherReceipt)).String(), "delivered", "整窗答复也含 WITHDRAWN_MEMORY_VALUE", "same-job-other-reply", "cid-test", before.Add(-140*time.Second))
			recentHistoryMemoryEvidence(t, f, scope, "dingtalk:org-a:open_id:source-open", receipt, "remember", state)
			recentHistoryInput(t, f, scope, principal, "临时对话，小林蓝、小周绿", "temporary", before.Add(-2*time.Minute))
			_, ordinaryJob := recentHistoryInput(t, f, scope, principal, "只把小周改为紫色", "temporary-correction", before.Add(-time.Minute))
			recentHistoryReply(t, f, scope, ordinaryJob, "delivered", "小林蓝，小周紫", "ordinary-reply", "cid-test", before.Add(-30*time.Second))
			got, err := f.store.RecentConversation(context.Background(), RecentConversationRequest{Scope: scope, PrincipalID: principal, Before: before})
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(got)
			if strings.Contains(string(raw), "WITHDRAWN_MEMORY_VALUE") != (state == "active") {
				t.Fatalf("withdrawn evidence resurrected or active dialogue erased: %s", raw)
			}
			for _, want := range []string{"同窗的普通事项仍须保留", "另一条同窗问题也须保留", "临时对话，小林蓝、小周绿", "只把小周改为紫色", "小林蓝，小周紫"} {
				if !strings.Contains(string(raw), want) {
					t.Errorf("unrelated dialogue erased: %q", want)
				}
			}
			var metadata map[string]any
			_ = json.Unmarshal(raw, &metadata)
			if state != "active" && metadata["withdrawn_memory_evidence_omitted"] != true {
				t.Fatal("withdrawal omission presented as full dialogue", string(raw))
			}
			var originalUser, originalReply string
			if err := f.pool.QueryRow(context.Background(), `SELECT payload#>>'{command,event,data,messages,0,text}' FROM employee_event_consumption WHERE receipt_id=$1::uuid`, receipt).Scan(&originalUser); err != nil {
				t.Fatal(err)
			}
			if err := f.pool.QueryRow(context.Background(), `SELECT input->>'text' FROM response_action WHERE provider_message_id='capture-reply'`).Scan(&originalReply); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(originalUser, "WITHDRAWN_MEMORY_VALUE") || !strings.Contains(originalReply, "WITHDRAWN_MEMORY_VALUE") {
				t.Fatal("projection destroyed audit evidence")
			}
		})
	}
}

func TestRecentConversationIgnoresForeignMemoryWithdrawal(t *testing.T) {
	f, before := recentHistoryDatabase(t)
	scope, principal := f.admission.Scope, f.admission.Item.PrincipalID
	receipt, job := recentHistoryInput(t, f, scope, principal, "KEEP_REAL_SOURCE", "original", before.Add(-time.Minute))
	recentHistoryReply(t, f, scope, job, "delivered", "KEEP_REAL_REPLY", "original-reply", "cid-test", before.Add(-30*time.Second))
	for _, dimension := range []string{"workspace", "agent", "org", "scene", "requester", "evidence"} {
		other, requester, message := scope, "dingtalk:org-a:open_id:source-open", "original"
		switch dimension {
		case "workspace":
			other.WorkspaceID = uuid.NewString()
		case "agent":
			other.AgentID = uuid.NewString()
		case "org":
			other.TenantOrgID = "other-org"
		case "scene":
			other.SceneID = uuid.NewString()
		case "requester":
			requester = "dingtalk:org-a:open_id:another-person"
		case "evidence":
			message = "another-message"
		}
		recentHistoryMemoryEvidence(t, f, other, requester, receipt, message, "forgotten")
	}
	got, err := f.store.RecentConversation(context.Background(), RecentConversationRequest{Scope: scope, PrincipalID: principal, Before: before})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Messages) != 2 || got.Messages[0].Text != "KEEP_REAL_SOURCE" || got.Messages[1].Text != "KEEP_REAL_REPLY" {
		t.Fatal("unrelated withdrawal erased dialogue", got)
	}
}

func TestRecentConversationCallbackNeedsExactRequestAndIndependentRunProof(t *testing.T) {
	for _, state := range []string{"active", "forgotten"} {
		t.Run(state, func(t *testing.T) {
			f, before := recentHistoryDatabase(t)
			scope, principal := f.admission.Scope, f.admission.Item.PrincipalID
			receipt, job := recentHistoryInput(t, f, scope, principal, "memory source", "remember", before.Add(-2*time.Minute))
			const callback = "/api/v1/dispatch-tasks/shared-request/response-receipt"
			if _, err := f.pool.Exec(context.Background(), `UPDATE employee_event_consumption SET payload=jsonb_set(payload,'{command,completionCallback}',jsonb_build_object('responseUrl',$2::text)) WHERE receipt_id=$1::uuid`, receipt, callback); err != nil {
				t.Fatal(err)
			}
			recentHistoryMemoryEvidence(t, f, scope, "dingtalk:org-a:open_id:source-open", receipt, "remember", state)
			_, otherJob := recentHistoryInput(t, f, scope, principal, "independent user source", "another", before.Add(-time.Minute))
			for _, reply := range []struct{ id, request, text string }{
				{"sync", "multica-terminal:sync-completed:shared-request", "EXACT_SYNC_REPLY"},
				{"unproven", "multica-terminal:sync-wrapup:unproven:shared-request", "UNPROVEN_REPLY"},
				{"independent", "employee-run:independent-request", "INDEPENDENT_PROVEN_REPLY"},
			} {
				recentHistoryReply(t, f, scope, job, "delivered", reply.text, reply.id, "cid-test", before.Add(-30*time.Second))
				if _, err := f.pool.Exec(context.Background(), `UPDATE response_action SET request_id=$2,input=(input-'scene_notice_id')||jsonb_build_object('callback_url',$3::text,'request_id',$2::text) WHERE provider_message_id=$1`, reply.id, reply.request, callback); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := f.pool.Exec(context.Background(), `INSERT INTO employee_run_notice(run_id,workspace_id,agent_id,tenant_org_id,scene_id,task_id,queue_task_id,job_id,source_ref,requester_ref,result_state,state,action_id) SELECT $1::uuid,workspace_id,agent_id,$2,$3::uuid,$4::uuid,$5::uuid,$6::uuid,'independent-source','requester','succeeded','enqueued',id FROM response_action WHERE provider_message_id='independent'`, uuid.NewString(), scope.TenantOrgID, scope.SceneID, uuid.NewString(), uuid.NewString(), otherJob); err != nil {
				t.Fatal(err)
			}
			got, err := f.store.RecentConversation(context.Background(), RecentConversationRequest{Scope: scope, PrincipalID: principal, Before: before})
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(got)
			if strings.Contains(string(raw), "UNPROVEN_REPLY") {
				t.Error("callback URL alone admitted another request")
			}
			if strings.Contains(string(raw), "EXACT_SYNC_REPLY") != (state == "active") {
				t.Error("exact source reply withdrawal incorrect")
			}
			if !strings.Contains(string(raw), "INDEPENDENT_PROVEN_REPLY") {
				t.Error("withdrawal erased an independently proven request on the same callback")
			}
		})
	}
}

func recentHistoryReply(t *testing.T, f fixture, scope Scope, job, state, text, messageID, conversation string, at time.Time) {
	t.Helper()
	input, _ := json.Marshal(map[string]any{"workspace_id": scope.WorkspaceID, "agent_id": scope.AgentID, "scene_id": scope.SceneID, "dws_org_id": scope.TenantOrgID, "dws_uid": "123", "conversation_id": "cid-test", "scene_notice_id": job, "text": text})
	if _, err := f.pool.Exec(context.Background(), `INSERT INTO response_action(id,workspace_id,agent_id,request_id,kind,input,state,provider_message_id,provider_conversation_id,created_at,updated_at) VALUES($1,$2::uuid,$3::uuid,$1,'message.send',$4,$5,$6,$7,$8,$8)`, uuid.NewString(), scope.WorkspaceID, scope.AgentID, input, state, messageID, conversation, at); err != nil {
		t.Fatal(err)
	}
}

func TestRecentConversationReadsOnlyScopedDeliveredEvidence(t *testing.T) {
	f, before := recentHistoryDatabase(t)
	scope, principal := f.admission.Scope, f.admission.Item.PrincipalID
	original, job := recentHistoryInput(t, f, scope, principal, "小林蓝、小周绿", "original", before.Add(-time.Minute))
	if _, err := f.pool.Exec(context.Background(), `UPDATE scene_event_receipt SET route='legacy',state='legacy' WHERE id=$1::uuid`, original); err != nil {
		t.Fatal(err)
	}
	recentHistoryReply(t, f, scope, job, "delivered", "想修改哪一个？", "sent", "cid-test", before.Add(-30*time.Second))
	for _, state := range []string{"pending", "provider_accepted", "unknown", "failed", "cancelled", "silent"} {
		recentHistoryReply(t, f, scope, job, state, "DO_NOT_READ_"+state, "unverified", "cid-test", before.Add(-20*time.Second))
	}
	recentHistoryReply(t, f, scope, job, "delivered", "DO_NOT_READ_wrong_target", "wrong", "other-cid", before.Add(-20*time.Second))
	recentHistoryReply(t, f, scope, job, "delivered", "DO_NOT_READ_missing_receipt", "", "cid-test", before.Add(-20*time.Second))
	recentHistoryReply(t, f, scope, job, "delivered", "DO_NOT_READ_future", "future", "cid-test", before.Add(time.Second))
	recentHistoryReply(t, f, scope, job, "delivered", "DO_NOT_READ_expired", "old", "cid-test", before.Add(-25*time.Hour))
	current, _ := recentHistoryInput(t, f, scope, principal, "DO_NOT_READ_current", "current", before.Add(-time.Second))
	recentHistoryInput(t, f, scope, principal, "DO_NOT_READ_duplicate", "current", before.Add(-2*time.Second))
	recentHistoryInput(t, f, scope, principal, "DO_NOT_READ_expired_user", "expired", before.Add(-25*time.Hour))
	unresolved, _ := recentHistoryInput(t, f, scope, principal, "DO_NOT_READ_unresolved", "unresolved", before.Add(-time.Minute))
	if _, err := f.pool.Exec(context.Background(), `UPDATE scene_event_receipt SET reason='unresolved_source' WHERE id=$1::uuid`, unresolved); err != nil {
		t.Fatal(err)
	}
	held, _ := recentHistoryInput(t, f, scope, principal, "DO_NOT_READ_held", "held", before.Add(-time.Minute))
	if _, err := f.pool.Exec(context.Background(), `UPDATE employee_event_consumption SET state='held' WHERE receipt_id=$1::uuid`, held); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"workspace", "agent", "org", "scene", "principal"} {
		other, p := scope, principal
		switch field {
		case "workspace":
			other.WorkspaceID = uuid.NewString()
			_, err := f.pool.Exec(context.Background(), `INSERT INTO workspace VALUES($1::uuid)`, other.WorkspaceID)
			if err != nil {
				t.Fatal(err)
			}
		case "agent":
			other.AgentID = uuid.NewString()
		case "org":
			other.TenantOrgID = "other-org"
		case "scene":
			other.SceneID = uuid.NewString()
		case "principal":
			p = uuid.NewString()
		}
		_, otherJob := recentHistoryInput(t, f, other, p, "DO_NOT_READ_"+field, "other-"+field, before.Add(-time.Minute))
		recentHistoryReply(t, f, other, otherJob, "delivered", "DO_NOT_READ_reply_"+field, "other-sent-"+field, "cid-test", before.Add(-20*time.Second))
	}
	got, err := f.store.RecentConversation(context.Background(), RecentConversationRequest{Scope: scope, PrincipalID: principal, Before: before, ExcludeReceipts: []string{current}, ExcludeMessages: []string{"current"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Messages) != 2 || got.Messages[0].Role != "user" || got.Messages[0].Text != "小林蓝、小周绿" || got.Messages[1].Role != "assistant" || got.Messages[1].Text != "想修改哪一个？" {
		t.Fatalf("missing or false dialogue evidence: %+v", got)
	}
	wire, _ := json.Marshal(got)
	if strings.Contains(string(wire), "DO_NOT_READ") {
		t.Fatal("unverified/foreign dialogue leaked", string(wire))
	}
	if !got.Before.Equal(before) || !got.Since.Equal(before.Add(-24*time.Hour)) || got.Truncated {
		t.Fatal("wrong fixed history window", got)
	}
}

func TestRecentConversationReadFailureIsNotAnEmptyHistory(t *testing.T) {
	f, before := recentHistoryDatabase(t)
	request := RecentConversationRequest{Scope: f.admission.Scope, PrincipalID: f.admission.Item.PrincipalID, Before: before}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.store.RecentConversation(ctx, request); !errors.Is(err, context.Canceled) {
		t.Fatal("failed read became available empty history", err)
	}
	request.PrincipalID = ""
	if _, err := f.store.RecentConversation(context.Background(), request); !errors.Is(err, ErrInvalid) {
		t.Fatal("unscoped history accepted", err)
	}
}

func TestRecentConversationBoundsNewestWholeMessagesAndMarksTruncation(t *testing.T) {
	f, before := recentHistoryDatabase(t)
	for i := 0; i < 30; i++ {
		recentHistoryInput(t, f, f.admission.Scope, f.admission.Item.PrincipalID, strings.Repeat("原文", 1800)+"TAIL", uuid.NewString(), before.Add(-time.Duration(30-i)*time.Minute))
	}
	got, err := f.store.RecentConversation(context.Background(), RecentConversationRequest{Scope: f.admission.Scope, PrincipalID: f.admission.Item.PrincipalID, Before: before})
	if err != nil {
		t.Fatal(err)
	}
	wire, _ := json.Marshal(got)
	if !got.Truncated || len(got.Messages) == 0 || len(got.Messages) > 20 || len(wire) > 16<<10 {
		t.Fatalf("unbounded/false-complete history: messages=%d bytes=%d %+v", len(got.Messages), len(wire), got)
	}
	last := got.Messages[len(got.Messages)-1]
	if !last.At.Equal(before.Add(-time.Minute)) || !last.Truncated || last.OriginalBytes <= len(last.Text) || !json.Valid(wire) {
		t.Fatal("newest evidence or explicit truncation lost", last)
	}
}
