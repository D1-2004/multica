package handler

import (
	"context"
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
