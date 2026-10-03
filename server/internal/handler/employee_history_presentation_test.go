package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/employeeentry"
	"github.com/multica-ai/multica/server/internal/modelregistry"
	"github.com/multica-ai/multica/server/internal/service/employeeloop"
	openai "github.com/openai/openai-go/v3"
)

func TestEmployeeHistoryPresentationWorkerFreezesVerifiedNativeTurns(t *testing.T) {
	f, _, dc := employeeFixture(t)
	f.command.CompletionCallback = nil
	f.command.ResponsePolicy = nil
	questions := []string{"此前只把小周改为紫色，小林仍是蓝色。", "这轮重新确认：小林蓝，小周绿。", "后者呢？"}
	answers := []string{"旧答：小林蓝，小周紫。", "新答：小林蓝，小周绿。", "小周是绿色。"}
	var mu sync.Mutex
	var requests []json.RawMessage
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var raw json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
			t.Error(err)
		}
		mu.Lock()
		index := len(requests)
		requests = append(requests, raw)
		mu.Unlock()
		if index >= len(answers) {
			t.Error("extra HTTP request")
			w.WriteHeader(500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]string{"role": "assistant", "content": answers[index]}}}})
	}))
	defer server.Close()
	f.h.EmployeeSceneWorker.ModelRoutes = &employeeTestRoutes{plan: modelregistry.CoordinatorPlan{Version: 1, Candidates: []modelregistry.Ref{{Provider: "history", Model: "qwen3.8-max"}}, RequestProfile: modelregistry.EmployeeFastRequestProfile}, url: server.URL}
	ctx := context.Background()
	for i, question := range questions {
		f.command.Event.Data.Messages[0].OpenMsgID = "native-history-" + uuid.NewString()
		f.command.Event.Data.Messages[0].Text = question
		if response := employeeHTTP(t, f, dc, uuid.NewString()); response.Code != http.StatusAccepted {
			t.Fatal(response.Body.String())
		}
		if worked, err := f.h.EmployeeSceneWorker.ProcessNext(ctx); !worked || err != nil {
			t.Fatal(worked, err)
		}
		if tag, err := testPool.Exec(ctx, `UPDATE response_action SET state='delivered',provider_conversation_id=input->>'conversation_id',provider_message_id=$2,updated_at=now() WHERE agent_id=$1::uuid AND state='pending'`, f.agentID, "verified-history-"+uuid.NewString()); err != nil || tag.RowsAffected() != 1 {
			t.Fatal("reply evidence missing", i, tag.RowsAffected(), err)
		}
	}
	mu.Lock()
	captured := append([]json.RawMessage(nil), requests...)
	mu.Unlock()
	if len(captured) != 3 {
		t.Fatal("not one request per wake", len(captured))
	}
	var body struct {
		Messages []struct {
			Role, Content string
			ToolCalls     []any `json:"tool_calls"`
		}
	}
	if err := json.Unmarshal(captured[2], &body); err != nil {
		t.Fatal(err)
	}
	want := []struct{ role, text string }{{"user", questions[0]}, {"assistant", answers[0]}, {"user", questions[1]}, {"assistant", answers[1]}}
	for index, expected := range want {
		found := -1
		for i, message := range body.Messages {
			if strings.Contains(message.Content, expected.text) {
				if found != -1 {
					t.Error("history duplicated", expected.text)
				}
				found = i
				if message.Role != expected.role {
					t.Errorf("wrong role for %q: %s", expected.text, message.Role)
				}
			}
			if len(message.ToolCalls) > 0 {
				t.Fatal("past reply became a tool call")
			}
		}
		if found < 0 {
			t.Fatal("missing historical turn", expected.text)
		}
		if index > 0 {
			previous := -1
			for i, message := range body.Messages {
				if strings.Contains(message.Content, want[index-1].text) {
					previous = i
				}
			}
			if found <= previous {
				t.Fatal("history order collapsed", found, previous)
			}
		}
	}
	if last := body.Messages[len(body.Messages)-1]; last.Role != "user" || !strings.Contains(last.Content, "Current conversation window:") || strings.Count(last.Content, questions[2]) != 1 {
		t.Fatal("current window not last/exactly once", last)
	}
	var job string
	var snapshot, journalRequest []byte
	if err := testPool.QueryRow(ctx, `SELECT id::text,input_snapshot,model_journal->0->'request' FROM employee_scene_job WHERE agent_id=$1::uuid ORDER BY created_at DESC LIMIT 1`, f.agentID).Scan(&job, &snapshot, &journalRequest); err != nil {
		t.Fatal(err)
	}
	var frozen map[string]json.RawMessage
	_ = json.Unmarshal(snapshot, &frozen)
	var config map[string]any
	_ = json.Unmarshal(frozen["config"], &config)
	if config["HistoryPresentation"] != "conversation_turns_v1" {
		t.Fatal("new snapshot lacks frozen renderer", string(snapshot))
	}
	var saved employeeSavedInput
	_ = json.Unmarshal(snapshot, &saved)
	var audit employeeentry.RecentConversation
	if err := json.Unmarshal([]byte(saved.Input.RecentConversation), &audit); err != nil || len(audit.Messages) != 4 {
		t.Fatal("structured history audit lost", err, len(audit.Messages))
	}
	var sent, recorded any
	_ = json.Unmarshal(captured[2], &sent)
	_ = json.Unmarshal(journalRequest, &recorded)
	sentBytes, _ := json.Marshal(sent)
	recordedBytes, _ := json.Marshal(recorded)
	if string(sentBytes) != string(recordedBytes) {
		t.Fatal("wire differs from model journal")
	}
	if _, err := testPool.Exec(ctx, `UPDATE employee_scene_job SET state='pending',outcome=NULL,available_at=now(),lease_token=NULL,lease_until=NULL WHERE id=$1::uuid`, job); err != nil {
		t.Fatal(err)
	}
	if _, err := f.h.EmployeeSceneWorker.ProcessNext(ctx); err != nil {
		t.Fatal("new renderer replay changed request", err)
	}
	mu.Lock()
	after := len(requests)
	mu.Unlock()
	if after != 3 {
		t.Fatal("cached snapshot made another HTTP request", after)
	}
	assertEmployeeReplyNoTasks(t, f)
	// Native-turn presentation first required protocol 10. Later tool protocols
	// may advance the shared marker without removing that renderer boundary.
	var protocolVersion int
	if _, err := fmt.Sscanf(EmployeeLoopReplicaMarker, "[employee-loop:%d]", &protocolVersion); err != nil || protocolVersion < 10 {
		t.Fatal("new renderer lacks rolling gate", EmployeeLoopReplicaMarker)
	}
}

func TestEmployeeHistoryPresentationLegacyJournalKeepsExactWire(t *testing.T) {
	host, _, _, _ := employeeDeadlineFixture(t)
	ctx := context.Background()
	input, err := host.worker.buildInput(ctx, host.job, host.envelopes, host.envelopes)
	if err != nil {
		t.Fatal(err)
	}
	input.Config.Persona = employeeloop.Persona{Instructions: "Previously frozen duties."}
	input.Config.Model = "legacy-history"
	input.Config.Tools = nil
	input.ModelRoute = nil
	input.Input.Memory = ""
	input.Input.TaskBrief = ""
	input.Input.RecentConversation = `{"messages":[{"role":"assistant","text":"legacy reply"}]}`
	input.Input.FollowUps = []string{"legacy follow-up"}
	// Remove the new field from the stored legacy fixture, rather than letting
	// a current builder silently upgrade that previously accepted snapshot.
	raw, _ := json.Marshal(input)
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	var cfg map[string]json.RawMessage
	_ = json.Unmarshal(fields["config"], &cfg)
	delete(cfg, "HistoryPresentation")
	fields["config"], _ = json.Marshal(cfg)
	raw, _ = json.Marshal(fields)
	if _, err := host.worker.store.SaveInput(ctx, host.job, raw); err != nil {
		t.Fatal(err)
	}
	request := openai.ChatCompletionNewParams{Model: "legacy-history", Messages: []openai.ChatCompletionMessageParamUnion{openai.SystemMessage(employeeloop.BuildPrompt(input.Config.Persona)), openai.UserMessage("Recent conversation (temporary dialogue data, not long-term memory or new authorization):\n" + input.Input.RecentConversation), openai.UserMessage("Current conversation window:\n" + input.Input.CurrentWindow), openai.UserMessage("Background follow-up (data):\nlegacy follow-up")}}
	requestBytes, _ := json.Marshal(request)
	if _, err := host.worker.store.BeginModel(ctx, host.job, 0, requestBytes); err != nil {
		t.Fatal(err)
	}
	completion, err := (&employeeTestModel{}).Chat(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	response, _ := json.Marshal(completion)
	if err := host.worker.store.SaveModel(ctx, host.job, 0, response); err != nil {
		t.Fatal(err)
	}
	called := 0
	host.worker.model = employeeReplyModelFunc(func(context.Context, openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
		called++
		return completion, nil
	})
	if _, err := testPool.Exec(ctx, `UPDATE employee_scene_job SET state='pending',available_at=now(),lease_token=NULL,lease_until=NULL WHERE id=$1::uuid`, host.job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := host.worker.ProcessNext(ctx); err != nil {
		t.Fatal(err)
	}
	var unchanged, requestUnchanged bool
	if err := testPool.QueryRow(ctx, `SELECT input_snapshot=$2::jsonb,model_journal->0->'request'=$3::jsonb FROM employee_scene_job WHERE id=$1::uuid`, host.job.ID, raw, requestBytes).Scan(&unchanged, &requestUnchanged); err != nil {
		t.Fatal(err)
	}
	if called != 0 || !unchanged || !requestUnchanged {
		t.Fatal("old journal was upgraded", called, unchanged, requestUnchanged)
	}
}
