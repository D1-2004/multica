package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/employeeentry"
	"github.com/multica-ai/multica/server/internal/integrations/agentmessagerouter"
	openai "github.com/openai/openai-go/v3"
)

// Each wake must see bounded conversation evidence, independently of long-term
// memory. Provider-confirmed replies are evidence; a saved model outcome is not.
func TestEmployeeRecentContextPreservesReferentsAcrossNewWakes(t *testing.T) {
	f, _, dc := employeeFixture(t)
	ctx := context.Background()
	f.command.CompletionCallback = nil
	f.command.ResponsePolicy = nil
	questions := []string{
		"这只是当前对话：小林蓝、小周绿，不要保存长期记忆。",
		"后者。",
		"只改小周为紫，其他不变；只给当前映射。",
	}
	answers := []string{
		"小林：蓝；小周：绿。你想修改哪一个？",
		"后者是小周，现在是绿色。要改成什么颜色？",
		"小林：蓝；小周：紫。",
	}
	calls := 0
	f.h.EmployeeSceneWorker.model = employeeReplyModelFunc(func(_ context.Context, p openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
		turn := calls
		calls++
		if turn >= len(questions) {
			t.Fatalf("conversation unexpectedly used more than one model call per wake: %d", calls)
		}
		raw, err := json.Marshal(p.Messages)
		if err != nil {
			t.Fatal(err)
		}
		for prior := 0; prior < turn; prior++ {
			for _, want := range []string{questions[prior], answers[prior]} {
				if !strings.Contains(string(raw), want) {
					t.Errorf("wake %d lacks earlier conversation evidence %q", turn+1, want)
				}
			}
		}
		var completion openai.ChatCompletion
		body, err := json.Marshal(map[string]any{"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": answers[turn]}}}})
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(body, &completion); err != nil {
			t.Fatal(err)
		}
		return &completion, nil
	})
	for turn, question := range questions {
		f.command.Event.Data.Messages[0].OpenMsgID = fmt.Sprintf("recent-%d", turn)
		f.command.Event.Data.Messages[0].Text = question
		if response := employeeHTTP(t, f, dc, uuid.NewString()); response.Code != http.StatusAccepted {
			t.Fatal(response.Body.String())
		}
		if worked, err := f.h.EmployeeSceneWorker.ProcessNext(ctx); !worked || err != nil {
			t.Fatal(worked, err)
		}
		// Model the existing response worker's verified provider observation:
		// both provider identifiers match the frozen target, after a real enqueue.
		if tag, err := testPool.Exec(ctx, `UPDATE response_action SET state='delivered',provider_conversation_id=input->>'conversation_id',provider_message_id=$2,updated_at=now() WHERE agent_id=$1::uuid AND state='pending' AND kind='message.send'`, f.agentID, fmt.Sprintf("sent-recent-%d", turn)); err != nil || tag.RowsAffected() != 1 {
			t.Fatal("expected one confirmed reply", tag.RowsAffected(), err)
		}
	}
	if calls != len(questions) {
		t.Fatalf("model calls=%d want %d", calls, len(questions))
	}
	for _, table := range []string{"employee_task", "employee_learning"} {
		var count int
		if err := testPool.QueryRow(ctx, `SELECT count(*) FROM `+table+` WHERE agent_id=$1::uuid`, f.agentID).Scan(&count); err != nil || count != 0 {
			t.Fatalf("temporary dialogue created %s rows: %d (%v)", table, count, err)
		}
	}
}

func TestEmployeeRecentContextHelperFencesAndProjectsConfirmedSources(t *testing.T) {
	for _, callback := range []bool{false, true} {
		t.Run(fmt.Sprint(callback), func(t *testing.T) {
			f, model, dc := employeeFixture(t)
			ctx := context.Background()
			if !callback {
				f.command.CompletionCallback = nil
				f.command.ResponsePolicy = nil
			}
			f.command.Event.Data.Messages[0].Text = "历史原话，小林蓝、小周绿 https://app.test/dingtalk/configure?link=RECENT_LINK_SECRET"
			if response := employeeHTTP(t, f, dc, uuid.NewString()); response.Code != http.StatusAccepted {
				t.Fatal(response.Body.String())
			}
			if _, err := f.h.EmployeeSceneWorker.ProcessNext(ctx); err != nil {
				t.Fatal(err)
			}
			if callback {
				var requestID, reply string
				if err := testPool.QueryRow(ctx, `SELECT request_id,result_message FROM task_completion_outbox WHERE agent_id=$1::uuid`, f.agentID).Scan(&requestID, &reply); err != nil {
					t.Fatal(err)
				}
				if _, err := f.h.PrepareExecutionResult(ctx, f.command.CompletionCallback.URL, agentmessagerouter.ExecutionResultRequest{RequestID: requestID, AgentID: f.agentID, ExecutionStatus: "completed", ResultMessage: reply}); err != nil {
					t.Fatal(err)
				}
			}
			if tag, err := testPool.Exec(ctx, `UPDATE response_action SET state='delivered',provider_conversation_id=input->>'conversation_id',provider_message_id='verified-recent-reply',updated_at=now() WHERE agent_id=$1::uuid AND kind='message.send'`, f.agentID); err != nil || tag.RowsAffected() != 1 {
				t.Fatal("missing delivered reply fixture", tag.RowsAffected(), err)
			}
			// Credentials can exist in other stored envelope fields; this reader
			// only projects message text and never renders the dispatch envelope.
			if _, err := testPool.Exec(ctx, `UPDATE employee_event_consumption SET payload=jsonb_set(payload,'{command,externalIdentity,contextToken}','"HISTORY_ENVELOPE_SECRET"'::jsonb) WHERE agent_id=$1::uuid`, f.agentID); err != nil {
				t.Fatal(err)
			}
			f.command.Event.Data.Messages[0].OpenMsgID = "current-followup"
			f.command.Event.Data.Messages[0].Text = "CURRENT_WINDOW_ONLY 后者"
			if response := employeeHTTP(t, f, dc, uuid.NewString()); response.Code != http.StatusAccepted {
				t.Fatal(response.Body.String())
			}
			job, err := f.h.EmployeeSceneWorker.store.Claim(ctx)
			if err != nil {
				t.Fatal(err)
			}
			text, err := f.h.EmployeeSceneWorker.recentConversation(ctx, job)
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{"历史原话，小林蓝、小周绿", "[configuration link]", "在，需要我帮你做什么？"} {
				if !strings.Contains(text, want) {
					t.Errorf("recent context lacks %q: %s", want, text)
				}
			}
			for _, forbidden := range []string{"RECENT_LINK_SECRET", "HISTORY_ENVELOPE_SECRET", "CURRENT_WINDOW_ONLY"} {
				if strings.Contains(text, forbidden) {
					t.Errorf("recent context leaked %q", forbidden)
				}
			}
			var snapshot employeeentry.RecentConversation
			if err := json.Unmarshal([]byte(text), &snapshot); err != nil || len(snapshot.Messages) != 2 || !snapshot.Before.Equal(job.CreatedAt) {
				t.Fatalf("invalid frozen history: %+v %v", snapshot, err)
			}
			if model.calls != 1 {
				t.Fatal("history lookup invoked a model", model.calls)
			}
			for _, dimension := range []string{"workspace", "agent", "scene", "org", "principal", "bad_envelope"} {
				wrong := job
				switch dimension {
				case "workspace":
					wrong.Scope.WorkspaceID = uuid.NewString()
				case "agent":
					wrong.Scope.AgentID = uuid.NewString()
				case "scene":
					wrong.Scope.SceneID = uuid.NewString()
				case "org":
					wrong.Scope.TenantOrgID = "other-org"
				case "principal":
					wrong.PrincipalID = uuid.NewString()
				case "bad_envelope":
					wrong.Items = append([]employeeentry.Item(nil), job.Items...)
					wrong.Items[0].Payload = json.RawMessage(`{}`)
				}
				if leaked, err := f.h.EmployeeSceneWorker.recentConversation(ctx, wrong); err == nil || leaked != "" {
					t.Errorf("%s bypassed history fence: %q %v", dimension, leaked, err)
				}
			}
		})
	}
}
