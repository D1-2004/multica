package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestTaskDispatchBroadcastOnlyCarriesSafeDispatchMetadata(t *testing.T) {
	task := db.AgentTaskQueue{
		ID:            typingIndependentUUID(31),
		AgentID:       typingIndependentUUID(32),
		RuntimeID:     typingIndependentUUID(33),
		IssueID:       typingIndependentUUID(34),
		ChatSessionID: typingIndependentUUID(35),
			Context: []byte(`{
				"agent_identity_context_token":"ctx-secret",
				"agent_identity_context_token_expires_at":4102444800000,
				"agent_identity_context_token_source":"external",
				"dingtalk_session_reply":{"webhook":"https://secret.example/session"},
			"dispatch_runtime_prompt":"private prompt",
			"other_private_state":"must not broadcast",
			"dispatch_idempotency_key":"dispatch-window:window-1",
			"dispatch_source":{"platform":"dingtalk","type":"robot"},
			"dispatch_outbound":{"mode":"robot_sdk","replyTo":"latest_message"},
			"dispatch_event_data":{"conversation":{"openConversationId":"cid","type":"group","title":"private title"},"sender":{"staffId":"staff","displayName":"private name"},"messages":[{"openMsgId":"msg","occurredAt":10,"text":"private message","attachments":[{"downloadUrl":"https://secret.example/file"}]}]}
		}`),
	}

	payload := taskDispatchBroadcastPayload(task)
	for _, forbidden := range []string{"agent_identity_context_token", "agent_identity_context_token_expires_at", "agent_identity_context_token_source", "dingtalk_session_reply", "dispatch_runtime_prompt", "other_private_state"} {
		if _, present := payload[forbidden]; present {
			t.Fatalf("task:dispatch leaked private key %q: %#v", forbidden, payload)
		}
	}
	for _, required := range []string{"task_id", "runtime_id", "agent_id", "dispatch_idempotency_key", "dispatch_source", "dispatch_outbound", "dispatch_event_data"} {
		if _, present := payload[required]; !present {
			t.Errorf("task:dispatch missing safe key %q: %#v", required, payload)
		}
	}
	if payload["task_id"] != util.UUIDToString(task.ID) {
		t.Fatalf("task id = %#v", payload["task_id"])
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"ctx-secret", "https://secret.example/session", "private prompt", "must not broadcast", "private title", "private name", "private message", "https://secret.example/file"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("task:dispatch leaked %q: %s", secret, encoded)
		}
	}
}

func TestDispatchRobotLifecycleClaimsAreDurableAndOneShot(t *testing.T) {
	fixture := newDurableChannelTaskFixture(t)
	queries := db.New(fixture.pool)
	task, err := queries.CreateAgentTask(context.Background(), db.CreateAgentTaskParams{
		AgentID:         fixture.agentID,
		RuntimeID:       fixture.runtimeID,
		IssueID:         pgtype.UUID{},
		Priority:        0,
		DispatchContext: []byte(`{"dispatch_idempotency_key":"dispatch-window:claim-test"}`),
	})
	if err != nil {
		t.Fatalf("create dispatch task: %v", err)
	}

	claims := []struct {
		name string
		call func(context.Context, pgtype.UUID) (bool, error)
	}{
		{name: "processing", call: queries.ClaimDispatchProcessingReaction},
		{name: "recall", call: queries.ClaimDispatchProcessingRecall},
		{name: "outbound", call: queries.ClaimDispatchOutbound},
	}
	for _, claim := range claims {
		t.Run(claim.name, func(t *testing.T) {
			first, err := claim.call(context.Background(), task.ID)
			if err != nil {
				t.Fatalf("first claim: %v", err)
			}
			second, err := claim.call(context.Background(), task.ID)
			if err != nil {
				t.Fatalf("second claim: %v", err)
			}
			if !first || second {
				t.Fatalf("claims = first:%v second:%v, want true then false", first, second)
			}
		})
	}
}

func typingIndependentUUID(last byte) (id pgtype.UUID) {
	id.Valid = true
	id.Bytes[15] = last
	return id
}
