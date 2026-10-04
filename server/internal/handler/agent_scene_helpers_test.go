package handler

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/multica-ai/multica/server/internal/assoc"
	"github.com/multica-ai/multica/server/internal/scene"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// registerTestScene registers (or finds) the Agent work scene of a test
// conversation the way an inbound dispatch does, and removes the agent's
// scenes when the test ends.
func registerTestScene(t *testing.T, agentID, orgID, kind, conversationID string) db.AgentScene {
	t.Helper()
	ctx := context.Background()
	owner := scene.Owner{WorkspaceID: parseUUID(testWorkspaceID), AgentID: parseUUID(agentID)}
	sc, err := scene.Resolve(ctx, testHandler.Queries, owner, scene.DingTalkConversation(orgID, kind, conversationID), scene.Observation{})
	if err != nil {
		t.Fatalf("register scene %s: %v", conversationID, err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_scene_memory WHERE agent_id = $1`, owner.AgentID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_scene WHERE agent_id = $1`, owner.AgentID)
	})
	return sc
}

func testSceneRef(sc db.AgentScene) *scene.Ref {
	ref := scene.RefOf(sc)
	return &ref
}

func testSceneNode(sc db.AgentScene) assoc.SceneNode {
	return sceneNodeOf(sc)
}

// registerFixedScene stores an Agent work scene with a fixed scene id for a
// test agent, active age ago, and removes it when the test ends.
func registerFixedScene(t *testing.T, agentID, orgID, kind, sceneID, conversationID, title string, age time.Duration) {
	t.Helper()
	if _, err := testPool.Exec(context.Background(), `INSERT INTO agent_scene
		(id, workspace_id, agent_id, provider, tenant_org_id, source_namespace, scene_kind, external_scene_id, title, last_active_at)
		VALUES ($1, $2, $3, 'dingtalk', $4, 'dingtalk.open_conversation_id', $5, $6, $7, now() - make_interval(secs => $8::double precision))`,
		sceneID, testWorkspaceID, agentID, orgID, kind, conversationID, title, age.Seconds()); err != nil {
		t.Fatalf("register scene %s: %v", sceneID, err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_scene_memory WHERE scene_id = $1`, sceneID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_scene WHERE id = $1`, sceneID)
	})
}

// assocSceneFixture is an agent with a DingTalk identity in its org, so the
// conversation ids its tools name resolve to its scenes, and a task on an
// issue for task-token calls.
type assocSceneFixture struct {
	h       *Handler
	store   *assoc.Memory
	ws      string
	agentID string
	issueID string
	taskID  string
	orgID   string
}

func newAssocSceneFixture(t *testing.T) assocSceneFixture {
	t.Helper()
	if testPool == nil || testHandler == nil {
		t.Skip("database not available")
	}
	f := assocSceneFixture{store: assoc.NewMemory(), ws: testWorkspaceID, orgID: "org-assoc-" + uuid.NewString()[:8]}
	f.agentID = createHandlerTestAgent(t, "assoc-scenes-"+uuid.NewString()[:8], nil)
	if _, err := testPool.Exec(context.Background(), `INSERT INTO agent_dingtalk_identity (agent_id, workspace_id, dws_uid, org_id, bound_by)
		VALUES ($1, $2, $3, $4, $5)`, f.agentID, testWorkspaceID, "dws-"+f.agentID, f.orgID, testUserID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_dingtalk_identity WHERE agent_id = $1`, f.agentID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_scene WHERE agent_id = $1`, f.agentID)
	})
	f.issueID = createTestIssue(t, "assoc scene fixture "+uuid.NewString()[:8], "todo", "medium")
	t.Cleanup(func() { deleteTestIssue(t, f.issueID) })
	f.taskID = createHandlerTestTaskForAgentOnIssue(t, f.agentID, f.issueID)
	f.h = &Handler{Queries: testHandler.Queries, Assoc: assoc.NewService(f.store), cfg: Config{PublicURL: "https://api.multica.test"}}
	return f
}

// scene registers the agent's scene of conversation cid.
func (f assocSceneFixture) scene(t *testing.T, kind, cid string) db.AgentScene {
	t.Helper()
	return registerTestScene(t, f.agentID, f.orgID, kind, cid)
}

// mcp points an MCP request at the fixture's workspace, agent and task.
func (f assocSceneFixture) mcp(r *http.Request) *http.Request {
	r.Header.Set("X-Agent-ID", f.agentID)
	r.Header.Set("X-Task-ID", f.taskID)
	r.Header.Set("X-Workspace-ID", f.ws)
	return r
}

// recallConversation recalls the graph of one conversation id through the
// handler, which resolves it to the agent's scene.
func (f assocSceneFixture) recallConversation(t *testing.T, cid string) assoc.Result {
	t.Helper()
	result, err := f.h.recallAssoc(context.Background(), f.ws, f.agentID, assocRecallParams{Since: "1h", ConversationID: cid})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

// task is the fixture task as the daemon reports its messages.
func (f assocSceneFixture) task() db.AgentTaskQueue {
	return db.AgentTaskQueue{ID: parseUUID(f.taskID), AgentID: parseUUID(f.agentID), IssueID: parseUUID(f.issueID)}
}

// A persisted SceneRef is used only while the agent still serves the
// scene's org: after the agent is re-bound to another org, a job or task
// admitted before reads no scene (docs/agent-scene.md §6).
func TestFenceSceneRefAfterTheAgentIsReBound(t *testing.T) {
	f := newAssocSceneFixture(t)
	ctx := context.Background()
	sc := f.scene(t, "group", "cid-fence-group==")
	owner := scene.Owner{WorkspaceID: parseUUID(f.ws), AgentID: parseUUID(f.agentID)}
	ref := testSceneRef(sc)
	if got := f.h.fenceSceneRef(ctx, ref, owner, f.orgID); got == nil || got.SceneID != ref.SceneID {
		t.Fatalf("current binding: %+v", got)
	}
	if _, err := testPool.Exec(ctx, `UPDATE agent_dingtalk_identity SET org_id = 'org-rebound' WHERE agent_id = $1`, f.agentID); err != nil {
		t.Fatal(err)
	}
	for _, dispatchOrg := range []string{f.orgID, "", "org-rebound"} {
		if got := f.h.fenceSceneRef(ctx, ref, owner, dispatchOrg); got != nil {
			t.Fatalf("dispatch org %q: a scene of the old org passed the fence: %+v", dispatchOrg, got)
		}
	}
	command := DispatchCommand{AgentScene: ref, ExternalIdentity: AgentDispatchExternalIdentity{DWS: &AgentDispatchDWSIdentity{OrgID: f.orgID}}}
	if _, err := dispatchScene(ctx, f.h.Queries, command, agentDispatchContext{WorkspaceID: owner.WorkspaceID, AgentID: owner.AgentID}); !errors.Is(err, scene.ErrStaleTenant) {
		t.Fatalf("dispatchScene after re-binding: %v", err)
	}
	// A tenant created for the old org keeps it served: a dispatch that
	// recorded that org still uses its scenes (context capabilities §2).
	if _, err := testPool.Exec(ctx, `INSERT INTO agent_tenant (workspace_id, agent_id, org_id, name) VALUES ($1, $2, $3, 'Old org')`, f.ws, f.agentID, f.orgID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_tenant WHERE agent_id = $1`, f.agentID)
	})
	if got := f.h.fenceSceneRef(ctx, ref, owner, f.orgID); got == nil {
		t.Fatal("a scene of a served tenant org was fenced")
	}
	if got := f.h.fenceSceneRef(ctx, ref, owner, ""); got != nil {
		t.Fatalf("without a recorded org the identity org applies: %+v", got)
	}
}

func TestCoordinatorChatTypeNeverAssumesADirectChat(t *testing.T) {
	for raw, want := range map[string]string{"group": "group", "2": "group", "single": "p2p", "p2p": "p2p", "1": "p2p", "": "unknown", "channel": "unknown"} {
		if got := coordinatorChatType(raw); got != want {
			t.Errorf("coordinatorChatType(%q) = %q, want %q", raw, got, want)
		}
	}
}

// Inside a task a scene_id names a scene only through the fence for the
// task's org: a task of an org the agent does not serve reads nothing, a
// member reading the agent's scenes is not a task.
func TestRecallBySceneIDInsideATaskPassesTheFence(t *testing.T) {
	f := newAssocSceneFixture(t)
	ctx := context.Background()
	sc := f.scene(t, "group", "cid-fenced-recall==")
	if _, err := f.store.InsertEvent(ctx, assoc.Event{WorkspaceID: f.ws, AgentID: f.agentID, Source: "inbound_im", Direction: assoc.DirInbound,
		EvidenceID: "msg-fenced-recall", SceneID: uuidToString(sc.ID), OccurredAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	foreign := parseUUID(createHandlerTestTaskForAgentOnIssue(t, f.agentID, f.issueID))
	if _, err := testPool.Exec(ctx, `UPDATE agent_task_queue SET context = '{"external_identity":{"dws":{"orgId":"org-not-served"}}}'::jsonb WHERE id = $1`, foreign); err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(ctx, `UPDATE agent_task_queue SET context = jsonb_build_object('external_identity', jsonb_build_object('dws', jsonb_build_object('orgId', $2::text))) WHERE id = $1`, parseUUID(f.taskID), f.orgID); err != nil {
		t.Fatal(err)
	}
	recall := func(taskID string) assoc.Result {
		t.Helper()
		got, err := f.h.recallAssoc(ctx, f.ws, f.agentID, assocRecallParams{Since: "1h", SceneID: uuidToString(sc.ID), TaskID: taskID})
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if got := recall(uuidToString(foreign)); got.SceneID != "" || len(got.Events) != 0 {
		t.Fatalf("a task of an org the agent does not serve read the scene: %+v", got)
	}
	if got := recall(f.taskID); got.SceneID != uuidToString(sc.ID) {
		t.Fatalf("the task's own org: %+v", got)
	}
	if got := recall(""); got.SceneID != uuidToString(sc.ID) {
		t.Fatalf("a member's read: %+v", got)
	}
}
