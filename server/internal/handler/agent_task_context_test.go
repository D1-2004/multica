package handler

import (
	"testing"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestTaskToResponseSurfacesDingTalkDWSIdentityUnavailable(t *testing.T) {
	response := taskToResponse(db.AgentTaskQueue{
		Context: []byte(`{"dingtalk_robot_identity_unavailable":{"reason":"missing_organization_identity"}}`),
	}, "")
	if !response.DingTalkDWSIdentityUnavailable {
		t.Fatal("DingTalk DWS identity unavailable marker was not surfaced to the daemon")
	}
}

func TestTaskToResponseOmitsDingTalkDWSIdentityUnavailableForOtherTasks(t *testing.T) {
	response := taskToResponse(db.AgentTaskQueue{Context: []byte(`{"some_other_context":true}`)}, "")
	if response.DingTalkDWSIdentityUnavailable {
		t.Fatal("unrelated task context was classified as DingTalk DWS identity unavailable")
	}
}
