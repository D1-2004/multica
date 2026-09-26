package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/pkg/protocol"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTaskReplyCommandRespectsDeliveryAndQuotesLocators(t *testing.T) {
	stored := persistedDispatchContext{Source: DispatchSource{Platform: "dingtalk"}, Domain: "channel", Type: "message.created", Surface: DispatchSurface{Type: "issue"}, Outbound: DispatchOutbound{Mode: protocol.DispatchOutboundModeDWS, ReplyTo: "latest_message"}, EventData: DispatchEventData{Conversation: DispatchConversation{OpenConversationID: "cid+abc=="}, Messages: []DispatchMessage{{OpenMsgID: "msg+abc==", Text: "hello"}}}}
	raw, _ := json.Marshal(stored)
	command := taskDingTalkReplyCommand(raw, "task-1")
	if !strings.Contains(command, "--group 'cid+abc=='") || !strings.Contains(command, "--message-id 'msg+abc=='") {
		t.Fatalf("missing quoted target: %s", command)
	}
	stored.Outbound.Mode = "callback"
	raw, _ = json.Marshal(stored)
	if taskDingTalkReplyCommand(raw, "task-1") != "" {
		t.Fatal("callback gained DWS command")
	}
	stored.Outbound.Mode = protocol.DispatchOutboundModeDWS
	stored.EventData.Conversation.OpenConversationID = "cid'; touch /tmp/pwn; '"
	raw, _ = json.Marshal(stored)
	if taskDingTalkReplyCommand(raw, "task-1") != "" {
		t.Fatal("unsafe locator admitted")
	}
}
func TestSmallSkillBatchRetainsLargeBundleFallback(t *testing.T) {
	small := []service.AgentSkillData{{Content: "small", Files: []service.AgentSkillFileData{{Content: "support"}}}}
	if !inlineSmallSkillSet(small) {
		t.Fatal("small set not batched")
	}
	small[0].Files[0].Content = strings.Repeat("x", 512*1024)
	if inlineSmallSkillSet(small) {
		t.Fatal("supporting files escaped size bound")
	}
	if inlineSmallSkillSet(make([]service.AgentSkillData, 33)) {
		t.Fatal("count bound missing")
	}
	if inlineSmallSkillSet([]service.AgentSkillData{{Description: strings.Repeat("x", 512*1024)}}) {
		t.Fatal("metadata escaped size bound")
	}
	if inlineSmallSkillSet([]service.AgentSkillData{{Content: strings.Repeat("\x00", 100*1024)}}) {
		t.Fatal("JSON escaping escaped size bound")
	}
}

func TestSmallSkillBatchIncludesMandatoryPlatformSkills(t *testing.T) {
	svc := &service.TaskService{}
	skills := append(svc.BuiltinSkills(), service.DWSAgentSkill())
	skills = append(skills, service.AgentSkillData{ID: "test-workspace-skill", Name: "probe", Content: "Run the authorized probe."})
	bundles, refs := service.BuildAgentSkillBundles(skills)
	if len(bundles) != len(skills) || len(refs) != len(skills) {
		t.Fatal("fixture lost mandatory skills")
	}
	if !inlineSmallSkillSet(bundles) {
		t.Fatalf("real minimal agent cannot enter batching: %d bundles", len(bundles))
	}
}

// Exercise the actual claim wire: all mandatory skills must survive both
// directions of a live flag change, including their supporting files/hashes.
func TestSmallSkillBatchClaimOnOffOn(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	previous := testHandler.TaskService.RuntimeStartRecoveryConfig
	t.Cleanup(func() { testHandler.TaskService.RuntimeStartRecoveryConfig = previous })
	for i, enabled := range []bool{true, false, true} {
		t.Run(fmt.Sprintf("%d-enabled-%t", i, enabled), func(t *testing.T) {
			testHandler.TaskService.RuntimeStartRecoveryConfig = func() service.RuntimeStartRecoveryConfig {
				return service.RuntimeStartRecoveryConfig{BatchSkillResolve: enabled}
			}
			runtimeID := createClaimReclaimRuntime(t, ctx, "Batch claim runtime")
			agentID, issueID := createClaimReclaimAgentAndIssue(t, ctx, runtimeID, "Batch claim agent")
			var taskID string
			if err := testPool.QueryRow(ctx, `INSERT INTO agent_task_queue (agent_id,runtime_id,issue_id,status,priority) VALUES ($1,$2,$3,'queued',0) RETURNING id`, agentID, runtimeID, issueID).Scan(&taskID); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE id=$1`, taskID) })
			req := newDaemonTokenRequest("POST", "/api/daemon/runtimes/"+runtimeID+"/tasks/claim", nil, testWorkspaceID, "batch-claim-daemon")
			req.Header.Set("X-Client-Capabilities", protocol.DaemonCapabilitySkillBundlesV1)
			req = withURLParam(req, "runtimeId", runtimeID)
			w := httptest.NewRecorder()
			testHandler.ClaimTaskByRuntime(w, req)
			if w.Code != http.StatusOK {
				t.Fatalf("claim status=%d: %s", w.Code, w.Body.String())
			}
			var response struct {
				Task *AgentTaskResponse `json:"task"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if response.Task == nil || response.Task.Agent == nil {
				t.Fatal("missing claimed agent")
			}
			agent := response.Task.Agent
			builtins := testHandler.TaskService.BuiltinSkills()
			if !enabled {
				if len(agent.Skills) != 0 || len(agent.SkillRefs) != len(builtins) {
					t.Fatal("off did not restore complete refs")
				}
				return
			}
			if len(agent.SkillRefs) != 0 || len(agent.Skills) != len(builtins) {
				t.Fatal("on did not inline complete set")
			}
			for i, original := range builtins {
				actual := agent.Skills[i]
				if actual.Name != original.Name || actual.Content != original.Content || actual.Hash == "" || len(actual.Files) != len(original.Files) {
					t.Fatal("builtin lost in claim")
				}
				for j, file := range original.Files {
					if actual.Files[j].Content != file.Content || actual.Files[j].Path != file.Path || actual.Files[j].SHA256 == "" {
						t.Fatal("supporting file lost in claim")
					}
				}
			}
		})
	}
}
