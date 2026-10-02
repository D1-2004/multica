package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/runtimeconfig"
)

const (
	reuseWorkspaceID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	reuseAgentID     = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	reuseOtherAgent  = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	reuseSceneID     = "dddddddd-dddd-4ddd-8ddd-dddddddddddd"
)

func reuseRuntime(t *testing.T, capabilities ...string) db.AgentRuntime {
	t.Helper()
	metadata, err := json.Marshal(map[string]any{
		"kind":         "fc-e2b",
		"capabilities": capabilities,
	})
	if err != nil {
		t.Fatal(err)
	}
	return db.AgentRuntime{
		RuntimeMode: "cloud",
		WorkspaceID: util.MustParseUUID(reuseWorkspaceID),
		Metadata:    metadata,
	}
}

func reuseTask(agentID string, contextJSON string) db.AgentTaskQueue {
	return db.AgentTaskQueue{
		AgentID: util.MustParseUUID(agentID),
		Context: []byte(contextJSON),
	}
}

func sceneDispatch(sceneID, staffID string, messages ...string) string {
	type message struct {
		SenderStaffID string `json:"senderStaffId"`
	}
	body := map[string]any{
		"agent_scene": map[string]any{"scene_id": sceneID},
		"dispatch_event_data": map[string]any{
			"conversation": map[string]any{"type": "group", "title": "room"},
			"sender":       map[string]any{"staffId": staffID, "displayName": "Ada"},
		},
	}
	if len(messages) > 0 {
		encoded := make([]message, len(messages))
		for i, staff := range messages {
			encoded[i] = message{SenderStaffID: staff}
		}
		body["dispatch_event_data"].(map[string]any)["messages"] = encoded
	}
	raw, err := json.Marshal(body)
	if err != nil {
		panic(err)
	}
	return string(raw)
}

func TestConnectionReuseScope(t *testing.T) {
	capable := reuseRuntime(t, SandboxConnectionReuseCapability)
	oldImage := reuseRuntime(t)
	personal := sceneDispatch(reuseSceneID, "staff-1")
	public := sceneDispatch(reuseSceneID, "staff-1", "staff-1", "staff-2")
	routine := `{"agent_scene":{"scene_id":"` + reuseSceneID + `"},"scene_routine":{"tenant_org_id":"org-1","kind":"group"}}`
	replayed := `{"replayed_dispatch_context":true,"agent_scene":{"scene_id":"` + reuseSceneID + `"},"dispatch_event_data":{"sender":{"staffId":"staff-1"}}}`
	a2a := `{"multica_origin":"a2a","agent_scene":{"scene_id":"` + reuseSceneID + `"},"dispatch_event_data":{"sender":{"staffId":"staff-1"}}}`

	personalScope, selectedPersonal, reason := connectionReuseScope(reuseTask(reuseAgentID, personal), capable, true)
	if !selectedPersonal || reason != "" || personalScope.typ != fcE2BScopeTypeScene || personalScope.sceneID != reuseSceneID || personalScope.actorKey != "staff-1" {
		t.Fatalf("personal scope = %+v selected=%v reason=%s", personalScope, selectedPersonal, reason)
	}
	again, _, _ := connectionReuseScope(reuseTask(reuseAgentID, personal), capable, true)
	if again.id != personalScope.id {
		t.Fatal("scene+actor scope id is not stable")
	}

	publicScope, ok, reason := connectionReuseScope(reuseTask(reuseAgentID, public), capable, true)
	if !ok || reason != "" || publicScope.actorKey != "" || publicScope.id == personalScope.id {
		t.Fatalf("mixed speakers = %+v selected=%v reason=%s", publicScope, ok, reason)
	}
	routineScope, ok, reason := connectionReuseScope(reuseTask(reuseAgentID, routine), capable, true)
	if !ok || reason != "" || routineScope.actorKey != "" || routineScope.id != publicScope.id {
		t.Fatalf("routine = %+v selected=%v reason=%s public=%v", routineScope, ok, reason, publicScope.id)
	}

	otherScope, ok, reason := connectionReuseScope(reuseTask(reuseOtherAgent, personal), capable, true)
	if !ok || reason != "" || otherScope.id != personalScope.id {
		t.Fatalf("another agent in the same scene = %+v selected=%v reason=%s", otherScope, ok, reason)
	}

	if _, ok, reason := connectionReuseScope(reuseTask(reuseAgentID, personal), capable, false); ok || reason != "disabled" {
		t.Fatalf("switch off selected=%v reason=%s", ok, reason)
	}
	if _, ok, reason := connectionReuseScope(reuseTask(reuseAgentID, a2a), capable, true); ok || reason != "a2a" {
		t.Fatalf("a2a selected=%v reason=%s", ok, reason)
	}
	if _, ok, reason := connectionReuseScope(reuseTask(reuseAgentID, personal), oldImage, true); ok || reason != "capability" {
		t.Fatalf("old image selected=%v reason=%s", ok, reason)
	}
	if _, ok, reason := connectionReuseScope(reuseTask(reuseAgentID, replayed), capable, true); ok || reason != "no_scene" {
		t.Fatalf("replayed selected=%v reason=%s", ok, reason)
	}
	if _, ok, reason := connectionReuseScope(reuseTask(reuseAgentID, `{}`), capable, true); ok || reason != "no_scene" {
		t.Fatalf("empty context selected=%v reason=%s", ok, reason)
	}
}

func TestSceneSandboxOverflowUsesTheConfiguredCap(t *testing.T) {
	if fcE2BSceneSandboxOverflow(5, 6) || !fcE2BSceneSandboxOverflow(6, 6) {
		t.Fatal("default cap of 6 did not overflow on the 7th task")
	}
	if fcE2BSceneSandboxOverflow(0, 1) || !fcE2BSceneSandboxOverflow(1, 1) {
		t.Fatal("cap of 1 did not overflow on the second task")
	}
	if fcE2BSceneSandboxOverflow(5, 0) || !fcE2BSceneSandboxOverflow(6, 0) {
		t.Fatal("a zero limit must use the default cap")
	}
}

func TestSceneScopeIDSeparatesThePublicBucket(t *testing.T) {
	personal := fcE2BSceneScopeID(reuseSceneID, "staff-1")
	public := fcE2BSceneScopeID(reuseSceneID, "")
	otherScene := fcE2BSceneScopeID("eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee", "staff-1")
	if !personal.Valid || personal == public || personal == otherScene || public == otherScene {
		t.Fatalf("personal=%v public=%v other=%v", personal, public, otherScene)
	}
	if fcE2BSceneScopeID(reuseSceneID, "staff-1") != personal {
		t.Fatal("scope id changed for the same scene and actor")
	}
}

func TestConnectTimeoutUsesThePinnedTaskLifetime(t *testing.T) {
	if got := fcE2BConnectTimeoutFromContext(context.Background()); got != fcE2BSDKConnectTimeoutSeconds {
		t.Fatalf("unpinned connect timeout = %d", got)
	}
	ctx := withFCE2BConnectTimeout(context.Background(), 4800)
	if got := fcE2BConnectTimeoutFromContext(ctx); got != 4800 {
		t.Fatalf("pinned connect timeout = %d", got)
	}
}

func TestCataloguedTemplatesDoNotClaimConnectionReuse(t *testing.T) {
	raw := `[{"templateID":"tpl_1","aliases":["multica-m7-v0123456789abcdef-r1-abc123"]}]`
	templates, err := parseFCE2BTemplates(raw, map[string][]string{"0123456789abcdef": {"hermes", "dsh"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(templates) != 1 {
		t.Fatalf("templates = %d", len(templates))
	}
	for _, capability := range templates[0].Capabilities {
		if capability == SandboxConnectionReuseCapability {
			t.Fatal("catalogued images must not be stamped with connection reuse")
		}
	}
	for _, capability := range runtimeconfig.CapabilitiesForProviders([]string{"hermes", "dsh", "opencode", "pi"}) {
		if capability == SandboxConnectionReuseCapability {
			t.Fatal("provider capabilities must not include connection reuse")
		}
	}
}

func TestSandboxReleaseRetainsSceneSandboxWhileAdmissionHoldsTheLock(t *testing.T) {
	f := newReleaseFixture(t)
	issue := pgtype.UUID{Bytes: [16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}, Valid: true}
	task := f.task(t, issue, pgtype.UUID{}, "completed", "sbx-scene")
	sceneScope := fcE2BTaskScope{typ: fcE2BScopeTypeScene, id: pgtype.UUID{Bytes: [16]byte{9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9}, Valid: true}}
	if _, err := f.pool.Exec(context.Background(), `INSERT INTO fc_e2b_sandbox_session(runtime_id,sandbox_id,expires_at,scope_type,scope_id)
VALUES ($1,'sbx-scene',now()+interval '70 minutes',$2,$3)`, f.rt.ID, sceneScope.typ, sceneScope.id); err != nil {
		t.Fatal(err)
	}
	launch := *f.l
	launch.Pool = f.other
	unlock, err := launch.lockSandboxScope(context.Background(), f.rt, sceneScope)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if action, reason := f.release(t, task, "sbx-scene"); action != fcE2BSandboxRetained || reason != "scope_admitting" {
		t.Fatalf("action=%s reason=%s", action, reason)
	}
	if calls := f.fcCalls(); len(calls) != 0 {
		t.Fatalf("trimmed a scene sandbox while admission held its lock: %v", calls)
	}
}
