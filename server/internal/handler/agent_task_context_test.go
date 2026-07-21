package handler

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func taskResponseFixture(context []byte) db.AgentTaskQueue {
	return db.AgentTaskQueue{
		ID:        parseUUID("cc22f8e5-c591-43bb-8757-699bd98f5797"),
		CreatedAt: pgtype.Timestamptz{Time: time.UnixMilli(1_721_000_100_456), Valid: true},
		Context:   context,
	}
}

func TestTaskToResponseSurfacesDingTalkDWSIdentityUnavailable(t *testing.T) {
	response := taskToResponse(taskResponseFixture([]byte(`{"dingtalk_robot_identity_unavailable":{"reason":"missing_organization_identity"}}`)), "")
	if !response.DingTalkDWSIdentityUnavailable {
		t.Fatal("DingTalk DWS identity unavailable marker was not surfaced to the daemon")
	}
}

func TestTaskToResponseOmitsDingTalkDWSIdentityUnavailableForOtherTasks(t *testing.T) {
	response := taskToResponse(taskResponseFixture([]byte(`{"some_other_context":true}`)), "")
	if response.DingTalkDWSIdentityUnavailable {
		t.Fatal("unrelated task context was classified as DingTalk DWS identity unavailable")
	}
}

func TestTaskToResponseSurfacesChatTrace(t *testing.T) {
	response := taskToResponse(taskResponseFixture([]byte(`{"chat_trace":{"trace_id":"37d0871a-3657-4c74-91fa-39e846fa90a0","channel":"web","started_at_unix_ms":1721000000123}}`)), "")
	if response.TraceID != "37d0871a-3657-4c74-91fa-39e846fa90a0" || response.TraceStartedAtUnixMS != 1721000000123 {
		t.Fatalf("chat trace response = id %q started %d", response.TraceID, response.TraceStartedAtUnixMS)
	}
}

func TestTaskToResponseSurfacesTaskTraceForNonChatTask(t *testing.T) {
	response := taskToResponse(taskResponseFixture([]byte(`{"some_other_context":true}`)), "")
	if response.TraceID != "cc22f8e5-c591-43bb-8757-699bd98f5797" || response.TraceStartedAtUnixMS != 1_721_000_100_456 {
		t.Fatalf("non-chat trace response = id %q started %d", response.TraceID, response.TraceStartedAtUnixMS)
	}
}

func TestDispatchRuntimePromptOnlyEntersPrivateClaimResponse(t *testing.T) {
	const runtimePrompt = "private runtime instruction"
	const contextToken = "private context token"
	task := db.AgentTaskQueue{Context: []byte(`{"agent_identity_context_token":"` + contextToken + `","dispatch_runtime_prompt":"` + runtimePrompt + `"}`)}
	response := taskToResponse(task, "")
	if response.DispatchRuntimePrompt != "" {
		t.Fatalf("ordinary task response exposed runtime prompt: %q", response.DispatchRuntimePrompt)
	}
	if response.AgentIdentityContextToken != "" {
		t.Fatalf("ordinary task response exposed context token: %q", response.AgentIdentityContextToken)
	}
	ordinary, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(ordinary), runtimePrompt) || strings.Contains(string(ordinary), contextToken) {
		t.Fatalf("ordinary task response leaked private dispatch context: %s", ordinary)
	}

	populatePrivateTaskClaimContext(&response, task.Context)
	if response.DispatchRuntimePrompt != runtimePrompt {
		t.Fatalf("claim runtime prompt = %q, want %q", response.DispatchRuntimePrompt, runtimePrompt)
	}
	if response.AgentIdentityContextToken != contextToken {
		t.Fatalf("claim context token = %q, want %q", response.AgentIdentityContextToken, contextToken)
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"dispatch_runtime_prompt":"`+runtimePrompt+`"`) {
		t.Fatalf("private claim response did not carry runtime prompt: %s", encoded)
	}
}
