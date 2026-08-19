package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	a2aintegration "github.com/multica-ai/multica/server/internal/integrations/a2a"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestDEAPA2AOpenConversationID(t *testing.T) {
	t.Parallel()

	deapMetadata := map[string]any{
		"context": map[string]any{
			"source": "AI_AGENT",
			"attributes": map[string]any{
				"sessionInfo": map[string]any{
					"openConversationId": " cid-deap ",
					"history":            []any{},
				},
			},
		},
	}
	if got := deapA2AOpenConversationID(nil, deapMetadata); got != "cid-deap" {
		t.Fatalf("DEAP request metadata = %q", got)
	}
	if got := deapA2AOpenConversationID(map[string]any{
		"sessionInfo": map[string]any{"openConversationId": "cid-message"},
	}); got != "cid-message" {
		t.Fatalf("message metadata = %q", got)
	}
	if got := deapA2AOpenConversationID(map[string]any{"openConversationId": "cid-flat"}); got != "cid-flat" {
		t.Fatalf("flat metadata = %q", got)
	}
	if got := deapA2AOpenConversationID(nil, map[string]any{"context": "x"}); got != "" {
		t.Fatalf("invalid metadata = %q", got)
	}
}

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
	if ShouldInjectA2ARunnerMCP(newA2ATaskContext()) {
		t.Fatal("ordinary A2A must not receive the owner's Runner MCP")
	}
	if !ShouldInjectA2ARunnerMCP(newA2ATaskContext(a2aintegration.InvocationIdentity{DEAPDWSToken: "deap-token"})) {
		t.Fatal("DEAP A2A must receive the Agent-bound Runner MCP")
	}
	if ShouldInjectA2ARunnerMCP(nil) {
		t.Fatal("ordinary tasks do not use the A2A Runner MCP gate")
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
	service.broadcastChatDone(context.Background(), task, nil, false)
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

func TestVisibleA2AChatLifecyclePublishesOnlyToOwner(t *testing.T) {
	bus := events.New()
	published := make([]events.Event, 0)
	bus.SubscribeAll(func(event events.Event) { published = append(published, event) })
	chatSessionID := util.MustParseUUID("33333333-3333-3333-3333-333333333333")
	service := &TaskService{
		Bus: bus,
		a2aHumanRealtimeRoute: func(_ context.Context, got pgtype.UUID) (string, string, error) {
			if got != chatSessionID {
				t.Fatalf("chat session id = %s, want %s", util.UUIDToString(got), util.UUIDToString(chatSessionID))
			}
			return "workspace-secret", "owner-1", nil
		},
	}
	task := db.AgentTaskQueue{
		ID:            util.MustParseUUID("44444444-4444-4444-4444-444444444444"),
		ChatSessionID: chatSessionID,
		Context:       []byte(`{"multica_origin":"a2a"}`),
		Status:        "running",
	}

	service.NotifyA2ATaskEnqueued(context.Background(), task)
	service.broadcastTaskDispatch(context.Background(), task)
	service.broadcastTaskEvent(context.Background(), protocol.EventTaskRunning, task)
	service.broadcastChatDone(context.Background(), task, nil, false)
	service.ReportProgress(context.Background(), task, "workspace-secret", "private tool output", 1, 2)

	wantTypes := []string{
		protocol.EventTaskQueued,
		protocol.EventTaskDispatch,
		protocol.EventTaskRunning,
		protocol.EventChatDone,
	}
	if len(published) != len(wantTypes) {
		t.Fatalf("published events = %+v, want types %v", published, wantTypes)
	}
	for i, event := range published {
		if event.Type != wantTypes[i] {
			t.Fatalf("event[%d].Type = %q, want %q", i, event.Type, wantTypes[i])
		}
		if event.WorkspaceID != "workspace-secret" || event.RecipientUserID != "owner-1" {
			t.Fatalf("event[%d] route = workspace %q recipient %q", i, event.WorkspaceID, event.RecipientUserID)
		}
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

func TestA2ADEAPDWSTokenStaysOutOfDurableTaskContext(t *testing.T) {
	const token = "deap-request-token-secret"
	taskContext := newA2ATaskContext(a2aintegration.InvocationIdentity{DEAPDWSToken: token})
	if bytes.Contains(taskContext, []byte(token)) {
		t.Fatalf("DEAP DWS token leaked into durable task context: %s", taskContext)
	}
	if !requiresA2ADEAPDWSToken(taskContext) {
		t.Fatalf("durable request requirement is missing: %s", taskContext)
	}

	launcher := &FCE2BLauncher{}
	runtime := db.AgentRuntime{
		RuntimeMode: "cloud",
		Metadata:    []byte(`{"kind":"fc-e2b","capabilities":["dws"]}`),
	}
	requestContext := a2aintegration.WithInvocationIdentity(
		context.Background(),
		a2aintegration.InvocationIdentity{DEAPDWSToken: token},
	)
	env, err := launcher.identityEnvForTask(
		requestContext,
		db.AgentTaskQueue{Context: taskContext},
		runtime,
		"sandbox-deap",
		db.Agent{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(env) != 1 || env[protocol.DEAPDWSTokenEnvKey] != token {
		t.Fatalf("DEAP DWS environment = %#v", env)
	}
	if _, err = launcher.identityEnvForTask(
		context.Background(),
		db.AgentTaskQueue{Context: taskContext},
		runtime,
		"sandbox-deap",
		db.Agent{},
	); err == nil {
		t.Fatal("request-scoped DEAP DWS identity was accepted without its request context")
	}
}

func TestA2ADEAPDWSTokenSupportsASBDWSCapability(t *testing.T) {
	const token = "deap-request-token-secret"
	const digest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	runtime := db.AgentRuntime{
		RuntimeMode: "cloud",
		Provider:    "hermes",
		Metadata: []byte(fmt.Sprintf(
			`{"kind":"cloud-sandbox","sandbox_backend":"asb","provider":"hermes","artifact_kind":"oci_image","artifact_ref":"registry.example/runtime@sha256:%s","artifact_digest":"sha256:%s","capabilities":["dws"]}`,
			digest,
			digest,
		)),
	}
	taskContext := newA2ATaskContext(a2aintegration.InvocationIdentity{DEAPDWSToken: token})
	requestContext := a2aintegration.WithInvocationIdentity(
		context.Background(),
		a2aintegration.InvocationIdentity{DEAPDWSToken: token},
	)

	env, err := (&FCE2BLauncher{}).identityEnvForTask(
		requestContext,
		db.AgentTaskQueue{Context: taskContext},
		runtime,
		"sandbox-asb-deap",
		db.Agent{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(env) != 1 || env[protocol.DEAPDWSTokenEnvKey] != token {
		t.Fatalf("ASB DEAP DWS environment = %#v", env)
	}
}
