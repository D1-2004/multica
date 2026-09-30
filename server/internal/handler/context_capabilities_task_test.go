package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/contextcap"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util/secretbox"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	ctxcapOrg        = "org-ctxcap"
	ctxcapScene      = "cidCtxcapScene=="
	ctxcapOtherScene = "cidCtxcapOther=="
	ctxcapStaff      = "staff-ctxcap-1"
	ctxcapOtherStaff = "staff-ctxcap-2"
)

// ctxcapFixture is one isolated agent with global, scene and personal
// connectors/skills. Rows are removed in t.Cleanup.
type ctxcapFixture struct {
	h          *Handler
	box        *secretbox.Box
	upstream   *ctxcapUpstream
	ws         pgtype.UUID
	agent      pgtype.UUID
	global     string // globally granted bearer connector with a workspace credential
	scene      string // offered, scene-bound bearer connector without a workspace credential
	person     string // offered, person-bound auth_mode=none connector
	notOffered string // scene-bound but never offered
	skillAgent string // agent_skill
	skillScene string // offered, scene-bound skill
	skillFree  string // scene-bound but not offered
}

type ctxcapUpstream struct {
	mu   sync.Mutex
	auth map[string]string // upstream path -> Authorization header of the last call
}

func (u *ctxcapUpstream) last(path string) string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.auth[path]
}

func newCtxcapFixture(t *testing.T) *ctxcapFixture {
	t.Helper()
	if testPool == nil {
		t.Skip("database not available")
	}
	t.Setenv("MULTICA_INTERNAL_MCP_ALLOWED_HOST_SUFFIXES", "safe.example.test")
	ctx := context.Background()
	box, err := secretbox.New(bytes.Repeat([]byte("x"), secretbox.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	upstream := &ctxcapUpstream{auth: map[string]string{}}
	client := &http.Client{Transport: connectorTestRoundTrip(func(r *http.Request) (*http.Response, error) {
		upstream.mu.Lock()
		upstream.auth[r.URL.Path] = r.Header.Get("Authorization")
		upstream.mu.Unlock()
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"upstream ok"}]}}`))}, nil
	})}
	f := &ctxcapFixture{
		h: &Handler{
			Queries: db.New(testPool), DB: testPool, TxStarter: testPool, TaskService: testHandler.TaskService,
			InternalConnectorSecretBox: box, InternalConnectorClient: client,
			InternalConnectorRedis: &semanticaTestRates{n: 1}, cfg: Config{PublicURL: "https://multica.example"},
		},
		box: box, upstream: upstream, ws: parseUUID(testWorkspaceID),
	}

	agentID := uuid.NewString()
	if _, err := testPool.Exec(ctx, `INSERT INTO agent (id, workspace_id, name, runtime_mode, runtime_id, owner_id)
		VALUES ($1, $2, $3, 'cloud', $4, $5)`, agentID, testWorkspaceID, "Context capability agent "+agentID[:8], testRuntimeID, testUserID); err != nil {
		t.Fatal(err)
	}
	f.agent = parseUUID(agentID)
	t.Cleanup(func() {
		bg := context.Background()
		for _, statement := range []string{
			`DELETE FROM context_capability_binding WHERE agent_id = $1`,
			`DELETE FROM context_connector_credential WHERE agent_id = $1`,
			`DELETE FROM internal_connector_call_audit WHERE agent_id = $1`,
			`DELETE FROM internal_connector_agent WHERE agent_id = $1`,
			`DELETE FROM agent_task_queue WHERE agent_id = $1`,
			`DELETE FROM agent_skill WHERE agent_id = $1`,
			`DELETE FROM agent_dingtalk_identity WHERE agent_id = $1`,
			`DELETE FROM agent WHERE id = $1`,
		} {
			_, _ = testPool.Exec(bg, statement, agentID)
		}
		for _, id := range []string{f.global, f.scene, f.person, f.notOffered} {
			_, _ = testPool.Exec(bg, `DELETE FROM internal_connector WHERE id = $1`, id)
		}
		for _, id := range []string{f.skillAgent, f.skillScene, f.skillFree} {
			_, _ = testPool.Exec(bg, `DELETE FROM skill WHERE id = $1`, id)
		}
	})
	if _, err := testPool.Exec(ctx, `INSERT INTO agent_dingtalk_identity (agent_id, workspace_id, dws_uid, org_id, bound_by)
		VALUES ($1, $2, $3, $4, $5)`, agentID, testWorkspaceID, "dws-"+agentID, ctxcapOrg, testUserID); err != nil {
		t.Fatal(err)
	}

	f.global = f.insertConnector(t, "bearer", "workspace-secret")
	f.scene = f.insertConnector(t, "bearer", "")
	f.person = f.insertConnector(t, "none", "")
	f.notOffered = f.insertConnector(t, "bearer", "")
	if _, err := testPool.Exec(ctx, `INSERT INTO internal_connector_agent (connector_id, workspace_id, agent_id) VALUES ($1, $2, $3)`,
		f.global, testWorkspaceID, agentID); err != nil {
		t.Fatal(err)
	}
	f.skillAgent = f.insertSkill(t, "ctxcap-agent-skill")
	f.skillScene = f.insertSkill(t, "ctxcap-scene-skill")
	f.skillFree = f.insertSkill(t, "ctxcap-unoffered-skill")
	if _, err := testPool.Exec(ctx, `INSERT INTO agent_skill (agent_id, skill_id) VALUES ($1, $2)`, agentID, f.skillAgent); err != nil {
		t.Fatal(err)
	}

	tx, err := testPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err := contextcap.ReplaceOffers(ctx, tx, testWorkspaceID, agentID, []string{f.scene, f.person}, []string{f.skillScene}, testUserID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	// Offered bindings are written through the store; unoffered ones are
	// planted directly, as a stale row left after an offer removal would be.
	f.bind(t, contextcap.ScopeScene, ctxcapScene, contextcap.ResourceConnector, f.scene)
	f.bind(t, contextcap.ScopePerson, ctxcapStaff, contextcap.ResourceConnector, f.person)
	f.bind(t, contextcap.ScopeScene, ctxcapScene, contextcap.ResourceSkill, f.skillScene)
	for _, planted := range []struct{ resourceType, id string }{{contextcap.ResourceConnector, f.notOffered}, {contextcap.ResourceSkill, f.skillFree}} {
		if _, err := testPool.Exec(ctx, `INSERT INTO context_capability_binding
			(workspace_id, agent_id, scope_type, org_id, scope_key, resource_type, resource_id, enabled)
			VALUES ($1, $2, 'scene', $3, $4, $5, $6, true)`, testWorkspaceID, agentID, ctxcapOrg, ctxcapScene, planted.resourceType, planted.id); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func (f *ctxcapFixture) insertConnector(t *testing.T, authMode, workspaceBearer string) string {
	t.Helper()
	id := uuid.NewString()
	var sealed []byte
	if workspaceBearer != "" {
		payload, _ := json.Marshal(connectorSealedCredential{WorkspaceID: testWorkspaceID, ConnectorID: id, Bearer: workspaceBearer})
		var err error
		if sealed, err = f.box.Seal(payload); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := testPool.Exec(context.Background(), `INSERT INTO internal_connector
		(id, workspace_id, name, upstream_url, credential_ref, auth_mode, allowed_tools, enabled, credential_ciphertext)
		VALUES ($1, $2, $3, $4, $5, $6, '["read"]'::jsonb, true, $7)`,
		id, testWorkspaceID, "ctxcap-"+id[:8], "https://safe.example.test/"+id, connectorCredentialRef(id), authMode, sealed); err != nil {
		t.Fatal(err)
	}
	return id
}

func (f *ctxcapFixture) insertSkill(t *testing.T, name string) string {
	t.Helper()
	var id string
	if err := testPool.QueryRow(context.Background(), `INSERT INTO skill (workspace_id, name, description, content)
		VALUES ($1, $2, 'context capability test skill', 'body') RETURNING id::text`, testWorkspaceID, name+"-"+uuid.NewString()[:8]).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func (f *ctxcapFixture) bind(t *testing.T, scopeType, key, resourceType, resourceID string) {
	t.Helper()
	if _, err := contextcap.UpsertBinding(context.Background(), testPool, contextcap.BindingWrite{
		WorkspaceID: testWorkspaceID, AgentID: uuidToString(f.agent), ScopeType: scopeType, OrgID: ctxcapOrg, ScopeKey: key,
		ResourceType: resourceType, ResourceID: resourceID, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
}

// offer replaces the agent's offer catalog with connectorIDs plus the scene
// skill.
func (f *ctxcapFixture) offer(t *testing.T, connectorIDs ...string) {
	t.Helper()
	ctx := context.Background()
	tx, err := testPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err := contextcap.ReplaceOffers(ctx, tx, testWorkspaceID, uuidToString(f.agent), connectorIDs, []string{f.skillScene}, testUserID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func (f *ctxcapFixture) setCredential(t *testing.T, connectorID, scopeType, key, bearer string) {
	t.Helper()
	binding := contextcap.CredentialBinding{WorkspaceID: testWorkspaceID, AgentID: uuidToString(f.agent), ConnectorID: connectorID,
		ScopeType: scopeType, OrgID: ctxcapOrg, ScopeKey: key}
	sealed, err := contextcap.SealCredential(f.box, binding, bearer)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := contextcap.UpsertCredential(context.Background(), testPool, binding, sealed, contextcap.Hint(bearer), testUserID); err != nil {
		t.Fatal(err)
	}
}

// task inserts a running task whose context is a DingTalk dispatch envelope.
func (f *ctxcapFixture) task(t *testing.T, taskContext []byte) db.AgentTaskQueue {
	t.Helper()
	var id string
	if err := testPool.QueryRow(context.Background(), `INSERT INTO agent_task_queue (agent_id, runtime_id, status, priority, context)
		VALUES ($1, $2, 'running', 0, $3) RETURNING id::text`, uuidToString(f.agent), testRuntimeID, taskContext).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return db.AgentTaskQueue{ID: parseUUID(id), AgentID: f.agent, Status: "running", Context: taskContext}
}

// rerunTask inserts a running manual rerun (rerun_of_task_id set) whose
// context is a copy of a DingTalk dispatch, as TaskService.RerunIssue writes
// it for a member who reran someone else's run.
func (f *ctxcapFixture) rerunTask(t *testing.T, taskContext []byte) db.AgentTaskQueue {
	t.Helper()
	source := f.task(t, taskContext)
	var id string
	if err := testPool.QueryRow(context.Background(), `INSERT INTO agent_task_queue (agent_id, runtime_id, status, priority, context, rerun_of_task_id)
		VALUES ($1, $2, 'running', 0, $3, $4) RETURNING id::text`, uuidToString(f.agent), testRuntimeID, taskContext, uuidToString(source.ID)).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return db.AgentTaskQueue{ID: parseUUID(id), AgentID: f.agent, Status: "running", Context: taskContext, RerunOfTaskID: source.ID}
}

// ctxcapReplayed marks a dispatch context the way rerunDispatchContext does.
func ctxcapReplayed(taskContext []byte) []byte {
	var payload map[string]json.RawMessage
	_ = json.Unmarshal(taskContext, &payload)
	payload[contextcap.ReplayedDispatchContextKey] = json.RawMessage("true")
	raw, _ := json.Marshal(payload)
	return raw
}

// ctxcapFailingDB fails every query that touches one of the context
// capability tables, as a transient error or a replica running before
// migrations 9400+ would.
type ctxcapFailingDB struct{ dbExecutor }

func (d ctxcapFailingDB) fails(sql string) bool {
	return strings.Contains(sql, "context_capability_binding") || strings.Contains(sql, "context_connector_credential")
}

func (d ctxcapFailingDB) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	if d.fails(sql) {
		return nil, errors.New("relation context_capability_binding does not exist")
	}
	return d.dbExecutor.Query(ctx, sql, args...)
}

func (d ctxcapFailingDB) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	if d.fails(sql) {
		return ctxcapErrRow{}
	}
	return d.dbExecutor.QueryRow(ctx, sql, args...)
}

type ctxcapErrRow struct{}

func (ctxcapErrRow) Scan(...any) error {
	return errors.New("relation context_capability_binding does not exist")
}

func ctxcapDispatch(conversationType, cid, sender string, messageSenders ...string) []byte {
	messages := make([]map[string]any, 0, len(messageSenders))
	for i, staff := range messageSenders {
		messages = append(messages, map[string]any{"openMsgId": "m" + string(rune('a'+i)), "senderStaffId": staff})
	}
	raw, _ := json.Marshal(map[string]any{
		"dispatch_source": map[string]any{"platform": "dingtalk"},
		"dispatch_event_data": map[string]any{
			"conversation": map[string]any{"openConversationId": cid, "type": conversationType, "title": "Ctxcap group"},
			"sender":       map[string]any{"staffId": sender, "displayName": "Alice"},
			"messages":     messages,
		},
	})
	return raw
}

type ctxcapResolved struct{ binding, credential, bearer string }

func (f *ctxcapFixture) resolve(t *testing.T, task db.AgentTaskQueue) map[string]ctxcapResolved {
	t.Helper()
	connectors, err := f.h.authorizedTaskConnectors(context.Background(), f.ws, task)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]ctxcapResolved{}
	for _, c := range connectors {
		bearer, err := f.h.connectorBearer(c)
		if err != nil {
			t.Fatalf("resolved connector %s has no usable bearer: %v", c.ID, err)
		}
		out[c.ID] = ctxcapResolved{binding: c.bindingLayer, credential: c.credentialLayer, bearer: bearer}
	}
	return out
}

func TestContextCapabilitiesTaskScopeFromRealTaskContext(t *testing.T) {
	f := newCtxcapFixture(t)
	ctx := context.Background()
	group := f.task(t, ctxcapDispatch("group", ctxcapScene, ctxcapStaff, ctxcapStaff))
	scope := f.h.taskContextScope(ctx, f.ws, group)
	if scope.OrgID != ctxcapOrg || scope.SceneKey != ctxcapScene || scope.PersonKey != ctxcapStaff || scope.SceneTitle != "Ctxcap group" || scope.ConversationType != "group" {
		t.Fatalf("group scope = %#v", scope)
	}
	a2aContext, _ := json.Marshal(map[string]any{
		"multica_origin":      "a2a",
		"dispatch_event_data": map[string]any{"conversation": map[string]any{"openConversationId": ctxcapScene, "type": "group"}, "sender": map[string]any{"staffId": ctxcapStaff}},
	})
	if !service.IsA2ATaskOrigin(a2aContext) {
		t.Fatal("test fixture is not an A2A-origin context")
	}
	if scope := f.h.taskContextScope(ctx, f.ws, f.task(t, a2aContext)); scope != (contextcap.Scope{}) {
		t.Fatalf("A2A task got a context scope: %#v", scope)
	}
}

func TestContextCapabilitiesConnectorLayeringAndCredentialPrecedence(t *testing.T) {
	f := newCtxcapFixture(t)
	f.setCredential(t, f.global, contextcap.ScopeScene, ctxcapScene, "scene-global-secret")
	f.setCredential(t, f.global, contextcap.ScopePerson, ctxcapStaff, "person-global-secret")
	f.setCredential(t, f.scene, contextcap.ScopeScene, ctxcapScene, "scene-connector-secret")
	f.setCredential(t, f.notOffered, contextcap.ScopeScene, ctxcapScene, "unoffered-secret")

	for _, tc := range []struct {
		name    string
		context []byte
		want    map[string]ctxcapResolved
	}{
		{
			name:    "group single sender gets scene and personal layers",
			context: ctxcapDispatch("group", ctxcapScene, ctxcapStaff, ctxcapStaff, ctxcapStaff),
			want: map[string]ctxcapResolved{
				f.global: {connectorBindingGlobal, connectorCredentialPerson, "person-global-secret"},
				f.scene:  {connectorBindingScene, connectorCredentialScene, "scene-connector-secret"},
				f.person: {connectorBindingPerson, connectorCredentialNone, ""},
			},
		},
		{
			// f.global is granted but never offered, so its scene credential
			// (which would serve every member of the group) does not apply.
			name:    "multi-sender run gets no personal layer",
			context: ctxcapDispatch("group", ctxcapScene, ctxcapStaff, ctxcapStaff, ctxcapOtherStaff),
			want: map[string]ctxcapResolved{
				f.global: {connectorBindingGlobal, connectorCredentialWorkspace, "workspace-secret"},
				f.scene:  {connectorBindingScene, connectorCredentialScene, "scene-connector-secret"},
			},
		},
		{
			name:    "unstamped messages of a merged window get no personal layer",
			context: []byte(`{"dispatch_event_data":{"conversation":{"openConversationId":"` + ctxcapScene + `","type":"group"},"sender":{"staffId":"` + ctxcapStaff + `"},"messages":[{"openMsgId":"x"},{"openMsgId":"y"}]}}`),
			want: map[string]ctxcapResolved{
				f.global: {connectorBindingGlobal, connectorCredentialWorkspace, "workspace-secret"},
				f.scene:  {connectorBindingScene, connectorCredentialScene, "scene-connector-secret"},
			},
		},
		{
			name:    "manual rerun of a single-sender run gets the global layer only",
			context: ctxcapReplayed(ctxcapDispatch("group", ctxcapScene, ctxcapStaff, ctxcapStaff)),
			want: map[string]ctxcapResolved{
				f.global: {connectorBindingGlobal, connectorCredentialWorkspace, "workspace-secret"},
			},
		},
		{
			name:    "direct message gets the personal layer only",
			context: ctxcapDispatch("single", "cidCtxcapDirect", ctxcapStaff, ctxcapStaff),
			want: map[string]ctxcapResolved{
				f.global: {connectorBindingGlobal, connectorCredentialPerson, "person-global-secret"},
				f.person: {connectorBindingPerson, connectorCredentialNone, ""},
			},
		},
		{
			name:    "another group and person get the global layer only",
			context: ctxcapDispatch("group", ctxcapOtherScene, ctxcapOtherStaff),
			want: map[string]ctxcapResolved{
				f.global: {connectorBindingGlobal, connectorCredentialWorkspace, "workspace-secret"},
			},
		},
		{
			name:    "task without dispatch context gets the global layer only",
			context: []byte(`{"issue_id":"x"}`),
			want: map[string]ctxcapResolved{
				f.global: {connectorBindingGlobal, connectorCredentialWorkspace, "workspace-secret"},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := f.resolve(t, f.task(t, tc.context))
			if len(got) != len(tc.want) {
				t.Fatalf("resolved %d connectors, want %d: %#v", len(got), len(tc.want), got)
			}
			for id, want := range tc.want {
				if got[id] != want {
					t.Errorf("connector %s = %#v, want %#v", id, got[id], want)
				}
			}
			if _, leaked := got[f.notOffered]; leaked {
				t.Fatal("a binding whose resource is not offered was mounted")
			}
		})
	}

	t.Run("rerun lineage alone drops the scene and personal layers", func(t *testing.T) {
		got := f.resolve(t, f.rerunTask(t, ctxcapDispatch("group", ctxcapScene, ctxcapStaff, ctxcapStaff)))
		if len(got) != 1 || got[f.global] != (ctxcapResolved{connectorBindingGlobal, connectorCredentialWorkspace, "workspace-secret"}) {
			t.Fatalf("rerun resolution = %#v", got)
		}
	})

	t.Run("scene credential of a granted connector applies once it is offered", func(t *testing.T) {
		f.offer(t, f.scene, f.person, f.global)
		defer f.offer(t, f.scene, f.person)
		got := f.resolve(t, f.task(t, ctxcapDispatch("group", ctxcapScene, ctxcapStaff, ctxcapStaff, ctxcapOtherStaff)))
		if got[f.global] != (ctxcapResolved{connectorBindingGlobal, connectorCredentialScene, "scene-global-secret"}) {
			t.Fatalf("offered global connector = %#v", got[f.global])
		}
	})

	t.Run("context layer failure keeps global connectors", func(t *testing.T) {
		original := f.h.DB
		f.h.DB = ctxcapFailingDB{original}
		defer func() { f.h.DB = original }()
		got := f.resolve(t, f.task(t, ctxcapDispatch("group", ctxcapScene, ctxcapStaff, ctxcapStaff)))
		if len(got) != 1 || got[f.global] != (ctxcapResolved{connectorBindingGlobal, connectorCredentialWorkspace, "workspace-secret"}) {
			t.Fatalf("resolution with failing context layers = %#v", got)
		}
	})

	t.Run("A2A task gets the global layer only", func(t *testing.T) {
		raw, _ := json.Marshal(map[string]any{
			"multica_origin":      "a2a",
			"dispatch_event_data": map[string]any{"conversation": map[string]any{"openConversationId": ctxcapScene, "type": "group"}, "sender": map[string]any{"staffId": ctxcapStaff}},
		})
		got := f.resolve(t, f.task(t, raw))
		if len(got) != 1 || got[f.global] != (ctxcapResolved{connectorBindingGlobal, connectorCredentialWorkspace, "workspace-secret"}) {
			t.Fatalf("A2A resolution = %#v", got)
		}
	})

	t.Run("bearer connector without any credential is not mounted", func(t *testing.T) {
		got := f.resolve(t, f.task(t, ctxcapDispatch("group", ctxcapScene, ctxcapOtherStaff)))
		if _, ok := got[f.scene]; !ok {
			t.Fatal("scene connector with a scene credential was not mounted")
		}
		if _, err := contextcap.DeleteCredential(context.Background(), testPool, contextcap.CredentialBinding{
			WorkspaceID: testWorkspaceID, AgentID: uuidToString(f.agent), ConnectorID: f.scene,
			ScopeType: contextcap.ScopeScene, OrgID: ctxcapOrg, ScopeKey: ctxcapScene,
		}); err != nil {
			t.Fatal(err)
		}
		got = f.resolve(t, f.task(t, ctxcapDispatch("group", ctxcapScene, ctxcapOtherStaff)))
		if _, ok := got[f.scene]; ok {
			t.Fatal("bearer connector without any credential was mounted")
		}
	})
}

func TestContextCapabilitiesClaimInjectionMountsOnlyMatchingTasks(t *testing.T) {
	f := newCtxcapFixture(t)
	f.setCredential(t, f.scene, contextcap.ScopeScene, ctxcapScene, "scene-connector-secret")
	runtime := db.AgentRuntime{WorkspaceID: f.ws, RuntimeMode: "cloud", Metadata: []byte(`{"kind":"fc-e2b","provider":"opencode"}`)}
	inject := func(task db.AgentTaskQueue) *TaskAgentData {
		t.Helper()
		data := &TaskAgentData{}
		if err := f.h.injectRunnerMCP(context.Background(), runtime, task, "task-token", data, true, true); err != nil {
			t.Fatal(err)
		}
		return data
	}

	matching := inject(f.task(t, ctxcapDispatch("group", ctxcapScene, ctxcapStaff, ctxcapStaff)))
	for _, id := range []string{f.global, f.scene, f.person} {
		if _, ok := matching.McpRelayRoutes[connectorServerName(id)]; !ok {
			t.Errorf("matching task is missing connector %s: %#v", id, matching.McpRelayRoutes)
		}
	}
	if _, ok := matching.McpRelayRoutes[connectorServerName(f.notOffered)]; ok {
		t.Error("unoffered connector was mounted")
	}
	for _, secret := range []string{"scene-connector-secret", "workspace-secret"} {
		if strings.Contains(string(matching.McpConfig), secret) {
			t.Fatalf("claim payload leaked a connector credential: %s", matching.McpConfig)
		}
	}

	other := inject(f.task(t, ctxcapDispatch("group", ctxcapOtherScene, ctxcapOtherStaff)))
	if _, ok := other.McpRelayRoutes[connectorServerName(f.global)]; !ok {
		t.Fatal("global connector missing for a non-matching task")
	}
	for _, id := range []string{f.scene, f.person} {
		if _, ok := other.McpRelayRoutes[connectorServerName(id)]; ok {
			t.Errorf("non-matching task mounted context connector %s", id)
		}
	}
}

func TestContextCapabilitiesClaimSkillsLayering(t *testing.T) {
	f := newCtxcapFixture(t)
	ctx := context.Background()
	runtime := db.AgentRuntime{WorkspaceID: f.ws, RuntimeMode: "local"}
	skillIDs := func(skills []service.AgentSkillData) map[string]bool {
		out := map[string]bool{}
		for _, skill := range skills {
			out[skill.ID] = true
		}
		return out
	}

	group := f.task(t, ctxcapDispatch("group", ctxcapScene, ctxcapStaff, ctxcapStaff))
	extra := f.h.taskContextSkillIDs(ctx, f.ws, group)
	if len(extra) != 1 || uuidToString(extra[0]) != f.skillScene {
		t.Fatalf("context skill ids = %v, want only the offered scene skill", extra)
	}
	loaded := skillIDs(f.h.TaskService.LoadTaskExecutionSkills(ctx, f.agent, append(extra, extra[0], parseUUID(uuid.NewString())), runtime, ""))
	if !loaded[f.skillAgent] || !loaded[f.skillScene] || loaded[f.skillFree] {
		t.Fatalf("group task skills = %v", loaded)
	}
	_, refs := f.h.TaskService.LoadTaskSkillBundles(ctx, f.agent, extra, runtime, "")
	sceneRefs := 0
	for _, ref := range refs {
		if ref.ID == f.skillScene {
			sceneRefs++
		}
	}
	if sceneRefs != 1 {
		t.Fatalf("scene skill ref count = %d, want 1 (deduplicated)", sceneRefs)
	}

	other := f.task(t, ctxcapDispatch("group", ctxcapOtherScene, ctxcapOtherStaff))
	if ids := f.h.taskContextSkillIDs(ctx, f.ws, other); len(ids) != 0 {
		t.Fatalf("non-matching task got context skills %v", ids)
	}
	baseline := skillIDs(f.h.TaskService.LoadAgentExecutionSkills(ctx, f.agent, runtime, ""))
	if !baseline[f.skillAgent] || baseline[f.skillScene] {
		t.Fatalf("agent-only skills = %v", baseline)
	}
	// Bundle resolution accepts any requested enabled offered skill, even for
	// a task whose scene does not bind it, so a toggle after claim cannot 404.
	requested := []string{strings.ToUpper(f.skillScene), f.skillFree, f.skillAgent, "builtin-skill"}
	resolvable := f.h.resolvableContextSkillIDs(ctx, f.ws, other, requested)
	if len(resolvable) != 1 || uuidToString(resolvable[0]) != f.skillScene {
		t.Fatalf("resolvable context skills = %v", resolvable)
	}
	// Only requested refs are loaded, and tasks without a scene or personal
	// scope never load offered skills at all.
	if ids := f.h.resolvableContextSkillIDs(ctx, f.ws, other, []string{f.skillAgent}); len(ids) != 0 {
		t.Fatalf("unrequested offered skills were resolved: %v", ids)
	}
	for _, task := range []db.AgentTaskQueue{
		f.task(t, []byte(`{"issue_id":"x"}`)),
		f.rerunTask(t, ctxcapDispatch("group", ctxcapScene, ctxcapStaff, ctxcapStaff)),
	} {
		if ids := f.h.resolvableContextSkillIDs(ctx, f.ws, task, requested); len(ids) != 0 {
			t.Fatalf("task without a context scope resolved offered skills: %v", ids)
		}
	}
}

func (f *ctxcapFixture) relay(t *testing.T, task db.AgentTaskQueue, connectorID string) *httptest.ResponseRecorder {
	t.Helper()
	body := `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"read","arguments":{}}}`
	req := httptest.NewRequest(http.MethodPost, "/api/internal-connectors/"+connectorID+"/mcp", strings.NewReader(body))
	req.Header.Set("X-Actor-Source", "task_token")
	req.Header.Set("X-Workspace-ID", testWorkspaceID)
	req.Header.Set("X-Agent-ID", uuidToString(f.agent))
	req.Header.Set("X-Task-ID", uuidToString(task.ID))
	req = withURLParam(req, "connectorId", connectorID)
	rec := httptest.NewRecorder()
	f.h.CallInternalConnector(rec, req)
	return rec
}

func TestContextCapabilitiesRelayResolvesPerCallAndRevokes(t *testing.T) {
	f := newCtxcapFixture(t)
	ctx := context.Background()
	// Offering the granted connector opts it into group configuration, so its
	// scene credential takes part in person > scene > workspace below.
	f.offer(t, f.scene, f.person, f.global)
	f.setCredential(t, f.global, contextcap.ScopeScene, ctxcapScene, "scene-global-secret")
	f.setCredential(t, f.global, contextcap.ScopePerson, ctxcapStaff, "person-global-secret")
	f.setCredential(t, f.scene, contextcap.ScopeScene, ctxcapScene, "scene-connector-secret")
	group := f.task(t, ctxcapDispatch("group", ctxcapScene, ctxcapStaff, ctxcapStaff))

	call := func(task db.AgentTaskQueue, connectorID string, wantStatus int) string {
		t.Helper()
		rec := f.relay(t, task, connectorID)
		if rec.Code != wantStatus {
			t.Fatalf("relay %s: status=%d body=%s", connectorID, rec.Code, rec.Body.String())
		}
		for _, secret := range []string{"scene-global-secret", "person-global-secret", "scene-connector-secret", "workspace-secret"} {
			if strings.Contains(rec.Body.String(), secret) {
				t.Fatalf("relay response leaked a credential: %s", rec.Body.String())
			}
		}
		return f.upstream.last("/" + connectorID)
	}

	if got := call(group, f.global, http.StatusOK); got != "Bearer person-global-secret" {
		t.Fatalf("global connector sent %q, want the personal credential", got)
	}
	if got := call(group, f.scene, http.StatusOK); got != "Bearer scene-connector-secret" {
		t.Fatalf("scene connector sent %q", got)
	}
	if got := call(group, f.person, http.StatusOK); got != "" {
		t.Fatalf("auth_mode=none connector sent Authorization %q", got)
	}
	var layers []string
	rows, err := testPool.Query(ctx, `SELECT connector_id::text || ':' || binding_layer || ':' || credential_layer
		FROM internal_connector_call_audit WHERE task_id = $1 AND outcome = 'ok' ORDER BY connector_id`, uuidToString(group.ID))
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var layer string
		if err := rows.Scan(&layer); err != nil {
			t.Fatal(err)
		}
		layers = append(layers, layer)
	}
	rows.Close()
	want := map[string]bool{
		f.global + ":global:person": true,
		f.scene + ":scene:scene":    true,
		f.person + ":person:none":   true,
	}
	if len(layers) != len(want) {
		t.Fatalf("audit layers = %v", layers)
	}
	for _, layer := range layers {
		if !want[layer] {
			t.Fatalf("unexpected audit layer %q in %v", layer, layers)
		}
	}

	// Revoking the personal credential falls back to the scene credential on
	// the very next call of the same running task.
	if _, err := contextcap.DeleteCredential(ctx, testPool, contextcap.CredentialBinding{WorkspaceID: testWorkspaceID, AgentID: uuidToString(f.agent),
		ConnectorID: f.global, ScopeType: contextcap.ScopePerson, OrgID: ctxcapOrg, ScopeKey: ctxcapStaff}); err != nil {
		t.Fatal(err)
	}
	if got := call(group, f.global, http.StatusOK); got != "Bearer scene-global-secret" {
		t.Fatalf("after personal revoke the global connector sent %q", got)
	}

	// Disabling the scene binding revokes the scene connector for the running task.
	if _, err := contextcap.UpsertBinding(ctx, testPool, contextcap.BindingWrite{WorkspaceID: testWorkspaceID, AgentID: uuidToString(f.agent),
		ScopeType: contextcap.ScopeScene, OrgID: ctxcapOrg, ScopeKey: ctxcapScene, ResourceType: contextcap.ResourceConnector,
		ResourceID: f.scene, Enabled: false}); err != nil {
		t.Fatal(err)
	}
	call(group, f.scene, http.StatusForbidden)

	// Removing the offer revokes the personal connector at once.
	tx, err := testPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := contextcap.ReplaceOffers(ctx, tx, testWorkspaceID, uuidToString(f.agent), []string{f.scene}, nil, ""); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	call(group, f.person, http.StatusForbidden)

	// The library kill switch still wins over every layer.
	if _, err := testPool.Exec(ctx, `UPDATE internal_connector SET enabled = false WHERE id = $1`, f.global); err != nil {
		t.Fatal(err)
	}
	call(group, f.global, http.StatusForbidden)
}

func TestContextCapabilitiesRelayRejectsForeignScopes(t *testing.T) {
	f := newCtxcapFixture(t)
	f.setCredential(t, f.scene, contextcap.ScopeScene, ctxcapScene, "scene-connector-secret")

	multi := f.task(t, ctxcapDispatch("group", ctxcapScene, ctxcapStaff, ctxcapStaff, ctxcapOtherStaff))
	if rec := f.relay(t, multi, f.person); rec.Code != http.StatusForbidden {
		t.Fatalf("multi-sender task reached the personal connector: %d %s", rec.Code, rec.Body.String())
	}
	if rec := f.relay(t, multi, f.scene); rec.Code != http.StatusOK {
		t.Fatalf("multi-sender task lost the scene connector: %d %s", rec.Code, rec.Body.String())
	}

	a2aContext, _ := json.Marshal(map[string]any{
		"multica_origin":      "a2a",
		"dispatch_event_data": map[string]any{"conversation": map[string]any{"openConversationId": ctxcapScene, "type": "group"}, "sender": map[string]any{"staffId": ctxcapStaff}},
	})
	a2a := f.task(t, a2aContext)
	for _, id := range []string{f.scene, f.person} {
		if rec := f.relay(t, a2a, id); rec.Code != http.StatusForbidden {
			t.Fatalf("A2A task reached context connector %s: %d", id, rec.Code)
		}
	}
	if rec := f.relay(t, f.task(t, ctxcapDispatch("group", ctxcapOtherScene, ctxcapStaff)), f.scene); rec.Code != http.StatusForbidden {
		t.Fatalf("another group's task reached the scene connector: %d", rec.Code)
	}
}

func TestContextCapabilitiesSkillDeleteSweepsBindings(t *testing.T) {
	f := newCtxcapFixture(t)
	ctx := context.Background()
	countBindings := func(resourceID string) int {
		t.Helper()
		var n int
		if err := testPool.QueryRow(ctx, `SELECT count(*) FROM context_capability_binding WHERE agent_id = $1 AND resource_id = $2`,
			uuidToString(f.agent), resourceID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if countBindings(f.skillScene) != 2 { // offer + scene binding
		t.Fatalf("fixture bindings for the scene skill = %d", countBindings(f.skillScene))
	}
	req := withURLParam(newRequest(http.MethodDelete, "/api/skills/"+f.skillScene, nil), "id", f.skillScene)
	rec := httptest.NewRecorder()
	testHandler.DeleteSkill(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete skill: %d %s", rec.Code, rec.Body.String())
	}
	if n := countBindings(f.skillScene); n != 0 {
		t.Fatalf("skill delete left %d context bindings", n)
	}
	if countBindings(f.scene) == 0 {
		t.Fatal("skill delete removed unrelated connector bindings")
	}
}

func TestContextCapabilitiesWorkspaceDeleteSweepsTables(t *testing.T) {
	if testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	slug := "ctxcap-delete-" + uuid.NewString()[:8]
	var wsID string
	if err := testPool.QueryRow(ctx, `INSERT INTO workspace (name, slug, description) VALUES ('Ctxcap delete', $1, '') RETURNING id::text`, slug).Scan(&wsID); err != nil {
		t.Fatal(err)
	}
	agentID, keepAgentID := uuid.NewString(), uuid.NewString()
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = testPool.Exec(bg, `DELETE FROM workspace WHERE id = $1`, wsID)
		for _, table := range []string{"context_capability_binding", "context_connector_credential", "context_config_grant", "context_config_link"} {
			_, _ = testPool.Exec(bg, `DELETE FROM `+table+` WHERE agent_id = ANY($1::uuid[])`, []string{agentID, keepAgentID})
		}
	})
	if _, err := testPool.Exec(ctx, `INSERT INTO member (workspace_id, user_id, role) VALUES ($1, $2, 'owner')`, wsID, testUserID); err != nil {
		t.Fatal(err)
	}
	insertRows := func(workspaceID, agent string) {
		t.Helper()
		for _, statement := range []string{
			`INSERT INTO context_capability_binding (workspace_id, agent_id, scope_type, resource_type, resource_id) VALUES ($1, $2, 'offer', 'skill', gen_random_uuid())`,
			`INSERT INTO context_connector_credential (workspace_id, agent_id, connector_id, scope_type, scope_key, ciphertext) VALUES ($1, $2, gen_random_uuid(), 'person', 'staff', '\x00'::bytea)`,
			`INSERT INTO context_config_grant (user_id, workspace_id, agent_id, scope_type, scope_key, source, expires_at) VALUES (gen_random_uuid(), $1, $2, 'person', 'staff', 'agent_link', now() + interval '1 day')`,
			`INSERT INTO context_config_link (token_hash, workspace_id, agent_id, scope_type, scope_key, expires_at) VALUES (md5(random()::text) || md5(random()::text), $1, $2, 'scene', 'cidX', now() + interval '1 hour')`,
		} {
			if _, err := testPool.Exec(ctx, statement, workspaceID, agent); err != nil {
				t.Fatal(err)
			}
		}
	}
	insertRows(wsID, agentID)
	insertRows(testWorkspaceID, keepAgentID)

	rec := httptest.NewRecorder()
	testHandler.DeleteWorkspace(rec, withURLParam(newRequest(http.MethodDelete, "/api/workspaces/"+wsID, nil), "id", wsID))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete workspace: %d %s", rec.Code, rec.Body.String())
	}
	for _, table := range []string{"context_capability_binding", "context_connector_credential", "context_config_grant", "context_config_link"} {
		var deleted, kept int
		if err := testPool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE agent_id = $1), count(*) FILTER (WHERE agent_id = $2) FROM `+table,
			agentID, keepAgentID).Scan(&deleted, &kept); err != nil {
			t.Fatal(err)
		}
		if deleted != 0 || kept != 1 {
			t.Errorf("%s: deleted-workspace rows=%d other-workspace rows=%d", table, deleted, kept)
		}
	}
}

// A Bearer connector without a shared workspace credential can be enabled
// while context capabilities is on, so people and groups can bring their own
// token; it mounts only where a scene or personal credential exists.
func TestContextCapabilitiesBearerConnectorEnablesWithoutWorkspaceCredential(t *testing.T) {
	f := newCtxcapFixture(t)
	ctx := context.Background()
	if _, err := testPool.Exec(ctx, `UPDATE internal_connector SET enabled = false WHERE id = $1`, f.scene); err != nil {
		t.Fatal(err)
	}
	enable := func() *httptest.ResponseRecorder {
		req := newRequest(http.MethodPatch, "/api/workspaces/"+testWorkspaceID+"/internal-connectors/"+f.scene, map[string]any{
			"name": "ctxcap-byo-token", "upstream_url": "https://safe.example.test/" + f.scene, "auth_mode": "bearer",
			"allowed_tools": []string{"read"}, "agent_ids": []string{}, "enabled": true,
		})
		req = withURLParams(req, "id", testWorkspaceID, "connectorId", f.scene)
		w := httptest.NewRecorder()
		f.h.UpdateInternalConnector(w, req)
		return w
	}

	credentialOptional := func() bool {
		req := withURLParams(newRequest(http.MethodGet, "/api/workspaces/"+testWorkspaceID+"/internal-connectors", nil), "id", testWorkspaceID)
		w := httptest.NewRecorder()
		f.h.ListInternalConnectors(w, req)
		var items []struct {
			ID                 string `json:"id"`
			CredentialOptional bool   `json:"credential_optional"`
		}
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &items) != nil {
			t.Fatalf("list connectors = %d %s", w.Code, w.Body.String())
		}
		for _, item := range items {
			if item.ID == f.scene {
				return item.CredentialOptional
			}
		}
		t.Fatalf("connector %s missing from list", f.scene)
		return false
	}

	if !credentialOptional() {
		t.Fatal("bearer connector should report credential_optional")
	}
	if w := enable(); w.Code != http.StatusOK {
		t.Fatalf("enable without workspace credential = %d %s", w.Code, w.Body.String())
	}

	group := ctxcapDispatch("group", ctxcapScene, ctxcapStaff, ctxcapStaff)
	if _, ok := f.resolve(t, f.task(t, group))[f.scene]; ok {
		t.Fatal("connector without any credential was mounted")
	}
	f.setCredential(t, f.scene, contextcap.ScopePerson, ctxcapStaff, "alice-own-token")
	if got := f.resolve(t, f.task(t, group))[f.scene]; got.credential != connectorCredentialPerson || got.bearer != "alice-own-token" {
		t.Fatalf("personal token resolution = %#v", got)
	}
}

// ctxcapWithDispatchOrg records the agent's own org on a dispatch context, as
// persistedDispatchContext stores external_identity.dws for Digital Employee
// dispatches.
func ctxcapWithDispatchOrg(taskContext []byte, orgID string) []byte {
	var payload map[string]json.RawMessage
	_ = json.Unmarshal(taskContext, &payload)
	identity, _ := json.Marshal(map[string]any{"dws": map[string]string{"uid": "agent-dws-uid", "orgId": orgID}})
	payload["external_identity"] = identity
	raw, _ := json.Marshal(payload)
	return raw
}

func TestContextCapabilitiesSkipsTasksDispatchedUnderEarlierBinding(t *testing.T) {
	f := newCtxcapFixture(t)
	f.setCredential(t, f.global, contextcap.ScopePerson, ctxcapStaff, "person-global-secret")
	group := ctxcapDispatch("group", ctxcapScene, ctxcapStaff, ctxcapStaff)
	expectLayers := func(name string, task db.AgentTaskQueue, want bool) {
		t.Helper()
		got := f.resolve(t, task)
		_, personBound := got[f.person]
		personCredential := got[f.global].credential == connectorCredentialPerson
		if personBound != want || personCredential != want {
			t.Fatalf("%s: personal layer applied=%v/%v, want %v (%+v)", name, personBound, personCredential, want, got)
		}
		if !want && got[f.global].credential != connectorCredentialWorkspace {
			t.Fatalf("%s: global connector should fall back to the workspace credential, got %+v", name, got[f.global])
		}
	}

	expectLayers("dispatched under the current org", f.task(t, ctxcapWithDispatchOrg(group, ctxcapOrg)), true)
	// The same staffId under another org is a different person.
	expectLayers("dispatched under a previous org", f.task(t, ctxcapWithDispatchOrg(group, ctxcapOrg+"-previous")), false)

	// Without a recorded org, a task created before the agent was (re)bound
	// may belong to the previous binding.
	if _, err := testPool.Exec(context.Background(), `UPDATE agent_dingtalk_identity SET bound_at = now() WHERE agent_id = $1`, uuidToString(f.agent)); err != nil {
		t.Fatal(err)
	}
	older := f.task(t, group)
	older.CreatedAt = pgtype.Timestamptz{Time: time.Now().Add(-time.Hour), Valid: true}
	expectLayers("created before the rebind", older, false)
	newer := f.task(t, group)
	newer.CreatedAt = pgtype.Timestamptz{Time: time.Now().Add(time.Minute), Valid: true}
	expectLayers("created after the rebind", newer, true)
}
