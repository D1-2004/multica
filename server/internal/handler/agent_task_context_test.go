package handler

import (
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

func TestTaskToResponseOmitsServerPrivateAgentIdentityContextToken(t *testing.T) {
	fixture := taskResponseFixture([]byte(`{"agent_identity_context_token":"context-secret"}`))
	response := taskToResponse(fixture, "")
	if response.AgentIdentityContextToken != "" {
		t.Fatal("user-facing task response exposed server-private Agent Identity ContextToken")
	}
	claimResponse := taskToClaimResponse(fixture, "", db.AgentRuntime{})
	if claimResponse.AgentIdentityContextToken != "context-secret" {
		t.Fatal("daemon claim response did not receive server-private Agent Identity ContextToken")
	}
	fcClaimResponse := taskToClaimResponse(fixture, "", db.AgentRuntime{
		RuntimeMode: "cloud",
		Metadata:    []byte(`{"kind":"fc-e2b"}`),
	})
	if fcClaimResponse.AgentIdentityContextToken != "" {
		t.Fatal("FC/E2B daemon claim response exposed already-redeemed ContextToken")
	}
}
