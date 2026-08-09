package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestA2ATaskOriginMarker(t *testing.T) {
	if !hasA2ATaskOrigin(newA2ATaskContext()) {
		t.Fatal("new A2A task context must carry the durable origin marker")
	}
	if ShouldInjectRuntimeOwnerProfile(newA2ATaskContext()) {
		t.Fatal("A2A tasks must not inherit the runtime owner's personal profile")
	}
	if !ShouldInjectRuntimeOwnerProfile(nil) {
		t.Fatal("ordinary tasks must keep the existing runtime-owner profile behavior")
	}
	if !hasA2ATaskOrigin([]byte(`{"trace":{"id":"trace"},"multica_origin":"a2a"}`)) {
		t.Fatal("origin marker must survive additional task context fields")
	}
	for _, input := range [][]byte{
		nil,
		[]byte(`{}`),
		[]byte(`{"multica_origin":"human"}`),
		[]byte(`{"multica_origin":true}`),
		[]byte(`not-json`),
	} {
		if hasA2ATaskOrigin(input) {
			t.Fatalf("unexpected A2A marker in %q", input)
		}
	}
}

func TestTokenlessClaimFinalizationRejectsOrdinaryTask(t *testing.T) {
	service := &TaskService{}
	if _, err := service.FinalizeTaskClaimWithoutToken(context.Background(), db.AgentTaskQueue{}, nil, false); err == nil {
		t.Fatal("ordinary task was allowed to finalize without a task-scoped token")
	}
}

func TestAgentStatusBroadcastMasksRuntimeGatewayToken(t *testing.T) {
	mapped := agentToMap(db.Agent{RuntimeConfig: []byte(`{"mode":"gateway","gateway":{"host":"gw.internal","token":"secret-bearer"}}`)})
	encoded, err := json.Marshal(mapped)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) == "" || json.Valid(encoded) == false {
		t.Fatalf("invalid agent broadcast payload: %s", encoded)
	}
	var payload map[string]any
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatal(err)
	}
	runtimeConfig := payload["runtime_config"].(map[string]any)
	gateway := runtimeConfig["gateway"].(map[string]any)
	if gateway["token"] != "***" {
		t.Fatalf("gateway token was not masked: %s", encoded)
	}
}

func TestA2ATaskLifecycleDoesNotPublishHumanWorkspaceEvents(t *testing.T) {
	bus := events.New()
	published := make([]events.Event, 0)
	bus.SubscribeAll(func(event events.Event) { published = append(published, event) })
	service := &TaskService{Bus: bus}
	task := db.AgentTaskQueue{
		ID:      util.MustParseUUID("11111111-1111-1111-1111-111111111111"),
		Context: []byte(`{"multica_origin":"a2a","type":"quick_create","workspace_id":"workspace-secret"}`),
	}

	service.broadcastTaskDispatch(context.Background(), task)
	service.broadcastTaskEvent(context.Background(), protocol.EventTaskRunning, task)
	service.broadcastChatDone(context.Background(), task, nil)
	service.ReportProgress(context.Background(), task, "workspace-secret", "tool output", 1, 2)

	if len(published) != 0 {
		t.Fatalf("A2A lifecycle leaked %d workspace events: %+v", len(published), published)
	}

	ordinary := db.AgentTaskQueue{ID: util.MustParseUUID("22222222-2222-2222-2222-222222222222")}
	service.ReportProgress(context.Background(), ordinary, "workspace-visible", "ordinary progress", 1, 1)
	if len(published) != 1 || published[0].Type != protocol.EventTaskProgress || published[0].WorkspaceID != "workspace-visible" {
		t.Fatalf("ordinary progress event was unexpectedly suppressed: %+v", published)
	}
}

func TestA2ATaskDoesNotReceiveFCE2BIdentityEnvironment(t *testing.T) {
	launcher := &FCE2BLauncher{}
	env, err := launcher.identityEnvForTask(
		context.Background(),
		db.AgentTaskQueue{Context: newA2ATaskContext()},
		db.AgentRuntime{},
		"sandbox-external",
		db.Agent{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(env) != 0 {
		t.Fatalf("A2A task received identity environment: %#v", env)
	}
}
