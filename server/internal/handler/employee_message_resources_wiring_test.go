package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	openai "github.com/openai/openai-go/v3"
)

// A new snapshot carries the bounded ResourceContext, so the first and only
// model call already sees the file text: no extra model call, no task. While
// a replica cannot replay Input.Resources the context is not produced.
func TestEmployeeResourceSnapshotWiringFeedsFirstModelCall(t *testing.T) {
	for _, ready := range []bool{true, false} {
		t.Run(map[bool]string{true: "ready", false: "older replica live"}[ready], func(t *testing.T) {
			f, _, dc := employeeFixture(t)
			ctx := context.Background()
			if _, err := testPool.Exec(ctx, `INSERT INTO agent_dingtalk_identity(agent_id,workspace_id,dws_uid,org_id,bound_by) VALUES($1,$2,'123','456',$3)`, f.agentID, testWorkspaceID, testUserID); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_dingtalk_identity WHERE agent_id=$1`, f.agentID)
				_, _ = testPool.Exec(context.Background(), `DELETE FROM employee_message_resource WHERE agent_id=$1`, f.agentID)
			})
			f.command.CompletionCallback = nil
			f.command.ResponsePolicy = nil
			message := resourceMessage("msg-file", "[文件] code.txt fileId: F-CODE")
			message.Attachments = []DispatchAttachment{{Type: "file", Name: "code.txt", DownloadURL: "https://router.example/code.txt?" + resourceSecret}}
			f.command.Event.Data.Sender = DispatchSender{DisplayName: "Requester", OpenDingTalkID: resourceRequester}
			f.command.Event.Data.Messages = []DispatchMessage{message}
			dws := newFakeResourceDWS()
			dws.messages["msg-file"] = providerMessage(f.command.Event.Data.Conversation.OpenConversationID, "msg-file", resourceRequester, "", fileRes("F-CODE"))
			dws.files["F-CODE"] = textFile("code.txt", "THE-CODE-IS-7351")
			worker := f.h.EmployeeSceneWorker
			worker.ResourceProvider = dws
			calls := 0
			worker.model = employeeReplyModelFunc(func(_ context.Context, p openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
				calls++
				raw, _ := json.Marshal(p.Messages)
				request := string(raw)
				if strings.Contains(request, "RESOURCE-SECRET-SIG") || strings.Contains(request, "router.example") {
					t.Fatalf("model request leaks a signed URL: %s", request)
				}
				if got := strings.Contains(request, "THE-CODE-IS-7351") && strings.Contains(request, "ATTACHED RESOURCES"); got != ready {
					t.Fatalf("resource text in request = %v, want %v", got, ready)
				}
				var completion openai.ChatCompletion
				body, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": "code.txt 里的码是 THE-CODE-IS-7351"}}}})
				if err := json.Unmarshal(body, &completion); err != nil {
					t.Fatal(err)
				}
				return &completion, nil
			})
			if r := employeeHTTP(t, f, dc, uuid.NewString()); r.Code != http.StatusAccepted {
				t.Fatal(r.Body.String())
			}
			// Admitted while ready; an older replica then joins the rollout.
			worker.ReplicaReady = func(context.Context) error {
				if ready {
					return nil
				}
				return errors.New("an older replica is live")
			}
			if worked, err := worker.ProcessNext(ctx); !worked || err != nil {
				t.Fatal(worked, err)
			}
			if calls != 1 {
				t.Fatalf("model calls = %d", calls)
			}
			var snapshot string
			if err := testPool.QueryRow(ctx, `SELECT input_snapshot::text FROM employee_scene_job WHERE agent_id=$1`, f.agentID).Scan(&snapshot); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(snapshot, `"Resources"`) != ready || strings.Contains(snapshot, "RESOURCE-SECRET-SIG") {
				t.Fatalf("snapshot resources=%v want %v: %s", strings.Contains(snapshot, `"Resources"`), ready, snapshot)
			}
			for _, table := range []string{"employee_task"} {
				var count int
				if err := testPool.QueryRow(ctx, `SELECT count(*) FROM `+table+` WHERE agent_id=$1`, f.agentID).Scan(&count); err != nil || count != 0 {
					t.Fatalf("%s rows = %d %v", table, count, err)
				}
			}
		})
	}
}
