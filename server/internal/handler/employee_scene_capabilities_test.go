package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/contextcap"
	"github.com/multica-ai/multica/server/internal/employeeentry"
	"github.com/multica-ai/multica/server/internal/scene"
	"github.com/multica-ai/multica/server/internal/service/employeeloop"
	openai "github.com/openai/openai-go/v3"
)

func employeeCapabilityInput(t *testing.T, f *ctxcapFixture, messages []DispatchMessage) (employeeSavedInput, error) {
	t.Helper()
	envs := []employeeDispatchEnvelope{{PrincipalID: testUserID, Command: DispatchCommand{Event: DispatchEvent{Data: DispatchEventData{Messages: messages}}}}}
	job := employeeentry.Job{CreatedAt: time.Now(), Scope: employeeentry.Scope{WorkspaceID: testWorkspaceID, AgentID: uuidToString(f.agent), TenantOrgID: ctxcapOrg, SceneID: ctxcapScene}, Items: []employeeentry.Item{{ReceiptID: uuid.NewString(), PrincipalID: testUserID}}}
	return NewEmployeeSceneWorker(f.h, &employeeTestModel{}).buildInput(context.Background(), job, envs, envs)
}

func TestEmployeeSceneCapabilitiesEffectivePromptsAndSkills(t *testing.T) {
	f := newCtxcapFixture(t)
	for _, p := range []struct{ scope, key, text string }{{"org", ctxcapOrg, "ORG-BOUNDARY"}, {"scene", ctxcapScene, "SCENE-BOUNDARY"}, {"person", ctxcapStaff, "PERSON-BOUNDARY"}} {
		if _, err := contextcap.ReplacePromptComponents(context.Background(), testPool, contextcap.PromptComponentsWrite{WorkspaceID: testWorkspaceID, AgentID: uuidToString(f.agent), ScopeType: p.scope, OrgID: ctxcapOrg, ScopeKey: p.key, Components: []contextcap.PromptComponentInput{{Name: p.scope, Text: p.text}}}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := testPool.Exec(context.Background(), `UPDATE skill SET description='Managed by dingtalk-agent',content=$2 WHERE id=$1`, f.skillScene, "---\ndescription: Scene analysis expertise\n---\nDo not load this workflow body"); err != nil {
		t.Fatal(err)
	}
	input, err := employeeCapabilityInput(t, f, []DispatchMessage{{OpenMsgID: "one", SenderUID: "alice", SenderStaffID: ctxcapStaff, Text: "What can you do?"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ORG-BOUNDARY", "SCENE-BOUNDARY", "PERSON-BOUNDARY"} {
		if !strings.Contains(input.Config.Persona.Instructions, want) {
			t.Errorf("effective prompt missing %s", want)
		}
	}
	catalog := strings.Join(input.Config.Persona.Expertise, "\n")
	for _, want := range []string{"ctxcap-scene-skill", "Scene analysis expertise", "config-qwen-tag-scene", "connector"} {
		if !strings.Contains(catalog, want) {
			t.Errorf("effective capability missing %s: %s", want, catalog)
		}
	}
	if strings.Contains(catalog, "Do not load this workflow body") {
		t.Fatal("workflow body entered catalog")
	}
	mixed, err := employeeCapabilityInput(t, f, []DispatchMessage{{OpenMsgID: "one", SenderUID: "alice", SenderStaffID: ctxcapStaff}, {OpenMsgID: "two", SenderUID: "bob", SenderStaffID: "staff-bob"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(mixed.Config.Persona.Instructions, "PERSON-BOUNDARY") {
		t.Fatal("mixed window borrowed personal scope")
	}
}

func TestEmployeeSceneCapabilitiesToolsHaveHostOwnedLinks(t *testing.T) {
	found := map[string]bool{}
	for _, tool := range employeeSceneTools() {
		found[tool.Name] = true
	}
	for _, name := range []string{"describe_capabilities", "scene_config_get"} {
		if !found[name] {
			t.Errorf("missing scene capability tool %s", name)
		}
	}
}

func TestSceneConfigGlobalCatalogMatchesEnabledGrants(t *testing.T) {
	f := newCtxcapFixture(t)
	target, ok, err := f.h.taskConfigScene(context.Background(), f.ws, f.task(t, ctxcapDispatch("group", ctxcapScene, ctxcapStaff)))
	if err != nil || !ok {
		t.Fatalf("target %v %v", ok, err)
	}
	read := func() map[string]any {
		t.Helper()
		result, e := f.h.sceneConfigGet(context.Background(), target)
		if e != nil {
			t.Fatal(e)
		}
		raw, _ := json.Marshal(result)
		var value map[string]any
		if e = json.Unmarshal(raw, &value); e != nil {
			t.Fatal(e)
		}
		return value["capabilities"].(map[string]any)
	}
	always := read()["always_on"].(map[string]any)
	for _, kind := range []string{"skills", "connectors"} {
		for _, item := range always[kind].([]any) {
			if item.(map[string]any)["enabled"] != true {
				t.Errorf("always_on %s must be enabled: %v", kind, item)
			}
		}
	}
	if _, err = testPool.Exec(context.Background(), `UPDATE agent_skill SET enabled=false WHERE agent_id=$1`, f.agent); err != nil {
		t.Fatal(err)
	}
	if _, err = testPool.Exec(context.Background(), `UPDATE internal_connector SET enabled=false WHERE id=$1`, f.global); err != nil {
		t.Fatal(err)
	}
	always = read()["always_on"].(map[string]any)
	for _, kind := range []string{"skills", "connectors"} {
		if len(always[kind].([]any)) != 0 {
			t.Errorf("disabled %s remains always_on: %v", kind, always[kind])
		}
	}
}

func TestEmployeeSceneCapabilitiesUnavailableNeverMeansEmpty(t *testing.T) {
	f := newCtxcapFixture(t)
	f.h.DB = ctxcapFailingDB{dbExecutor: testPool}
	input, err := employeeCapabilityInput(t, f, []DispatchMessage{{OpenMsgID: "one", SenderUID: "alice"}})
	if err == nil {
		t.Fatalf("failed context load became a usable empty catalog: %+v", input)
	}
}

func TestEmployeeSceneCapabilitiesRejectReboundTenant(t *testing.T) {
	f := newCtxcapFixture(t)
	if _, err := testPool.Exec(context.Background(), `UPDATE agent_dingtalk_identity SET org_id='rebound-org' WHERE agent_id=$1`, f.agent); err != nil {
		t.Fatal(err)
	}
	if _, err := employeeCapabilityInput(t, f, []DispatchMessage{{OpenMsgID: "one", SenderUID: "alice"}}); err == nil {
		t.Fatal("old tenant context was read after rebinding")
	}
}

func TestEmployeeSceneCapabilityLinkHostCheckpointAndReplay(t *testing.T) {
	for _, kind := range []string{"group", "p2p"} {
		t.Run(kind, func(t *testing.T) {
			f, _, dc := employeeFixture(t)
			f.command.CompletionCallback = nil
			f.command.ResponsePolicy = nil
			f.command.Event.Data.Conversation.Type = kind
			f.command.Event.Data.Messages[0].Text = "你能做什么？给我当前会话的能力配置链接。"
			f.h.cfg.AppURL = "https://app.multica.example"
			if r := employeeHTTP(t, f, dc, uuid.NewString()); r.Code != 202 {
				t.Fatal(r.Body.String())
			}
			var receipt, sceneID string
			if err := testPool.QueryRow(context.Background(), `SELECT receipt_id::text,scene_id::text FROM employee_event_consumption WHERE agent_id=$1`, f.agentID).Scan(&receipt, &sceneID); err != nil {
				t.Fatal(err)
			}
			calls := 0
			f.h.EmployeeSceneWorker.model = employeeReplyModelFunc(func(_ context.Context, p openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
				calls++
				raw, _ := json.Marshal(p)
				if strings.Contains(string(raw), "dingtalkclient/page/link") || strings.Contains(string(raw), "configure?link=") {
					t.Fatal("bearer entered model request")
				}
				return employeeReplyCompletion(t, employeeReplyCall(t, "capability-reply", "describe_capabilities", map[string]any{"source_ref": receipt + "/message-1", "reply": "我可以协助分析，也能通过执行任务管理本会话配置。"}), employeeReplyCall(t, "additional-reply", "reply", map[string]any{"source_ref": receipt + "/message-1", "reply": "补充说明保留。"}), employeeReplyCall(t, "configuration-read", "scene_config_get", map[string]any{"source_ref": receipt + "/message-1"})), nil
			})
			if worked, err := f.h.EmployeeSceneWorker.ProcessNext(context.Background()); err != nil || !worked {
				t.Fatalf("worked=%v err=%v", worked, err)
			}
			verify := func() string {
				t.Helper()
				var count int
				var scope, key, sourceTask, extraScene string
				if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM context_config_link WHERE agent_id=$1`, f.agentID).Scan(&count); err != nil {
					t.Fatal(err)
				}
				if count != 1 {
					t.Fatalf("links=%d want exactly one", count)
				}
				if err := testPool.QueryRow(context.Background(), `SELECT scope_type,scope_key,COALESCE(source_task_id::text,''),extra_scene_key FROM context_config_link WHERE agent_id=$1`, f.agentID).Scan(&scope, &key, &sourceTask, &extraScene); err != nil {
					t.Fatal(err)
				}
				// A 1:1 chat's link also carries its person (the requester).
				wantScope, wantKey, wantExtra := "scene", sceneID, ""
				if kind == "p2p" {
					wantScope, wantKey, wantExtra = "person", "odt:requester-open-id", sceneID
				}
				if scope != wantScope || key != wantKey || extraScene != wantExtra || sourceTask != "" {
					t.Fatalf("wrong scope or fictitious task: %s %s %s %s", scope, key, extraScene, sourceTask)
				}
				var outcome, journal, toolJournal []byte
				if err := testPool.QueryRow(context.Background(), `SELECT outcome,model_journal,tool_journal FROM employee_scene_job WHERE agent_id=$1`, f.agentID).Scan(&outcome, &journal, &toolJournal); err != nil {
					t.Fatal(err)
				}
				var saved employeeSavedOutcome
				if err := json.Unmarshal(outcome, &saved); err != nil {
					t.Fatal(err)
				}
				label := "本群能力配置"
				if kind == "p2p" {
					label = "本单聊能力配置"
				}
				if !strings.Contains(saved.Outcome.Reply, "补充说明保留。") {
					t.Fatal("another reply in batch was lost")
				}
				if !strings.Contains(saved.Outcome.Reply, label) || !strings.Contains(saved.Outcome.Reply, "dingtalkclient/page/link") {
					t.Fatalf("missing Host link: %s", saved.Outcome.Reply)
				}
				if strings.Contains(string(journal), "dingtalkclient/page/link") || strings.Contains(string(journal), "configure?link=") {
					t.Fatal("link entered model journal")
				}
				for _, tool := range saved.Outcome.ToolOutcomes {
					raw, _ := json.Marshal(tool.Result)
					if strings.Contains(string(raw), "dingtalkclient/page/link") {
						t.Fatal("link entered tool result")
					}
				}
				safe, _ := json.Marshal(employeeTraceSafe(saved))
				if strings.Contains(string(safe), "dingtalkclient/page/link") {
					t.Fatal("link entered trace payload")
				}
				if !strings.Contains(string(toolJournal), "delivery_reply") {
					t.Fatal("Host reply checkpoint missing")
				}
				assertEmployeeReplyNoTasks(t, f)
				return saved.Outcome.Reply
			}
			first := verify()
			for _, reset := range []string{"state='pending',available_at=now()", "state='pending',outcome=NULL,available_at=now()"} {
				if _, err := testPool.Exec(context.Background(), `UPDATE employee_scene_job SET `+reset+` WHERE agent_id=$1`, f.agentID); err != nil {
					t.Fatal(err)
				}
				if _, err := f.h.EmployeeSceneWorker.ProcessNext(context.Background()); err != nil {
					t.Fatal(err)
				}
				if got := verify(); got != first {
					t.Fatal("replay minted or changed reply")
				}
			}
			if calls != 1 {
				t.Fatalf("replay invoked model %d times", calls)
			}
			t.Cleanup(func() {
				_, _ = testPool.Exec(context.Background(), `DELETE FROM context_config_link WHERE agent_id=$1`, f.agentID)
			})
		})
	}
}

func TestEmployeeSceneCapabilityLinkFailurePreservesAnswer(t *testing.T) {
	for _, failure := range []string{"missing_origin", "storage_error", "storage_timeout"} {
		t.Run(failure, func(t *testing.T) {
			f, _, dc := employeeFixture(t)
			f.command.CompletionCallback = nil
			f.command.ResponsePolicy = nil
			f.h.cfg.AppURL = ""
			f.h.cfg.FrontendOrigin = ""
			f.h.cfg.ForwardPublicBaseURL = ""
			if failure == "storage_error" || failure == "storage_timeout" {
				f.h.cfg.AppURL = "https://app.multica.example"
				body := "RAISE EXCEPTION 'injected link storage failure';"
				if failure == "storage_timeout" {
					body = "PERFORM pg_sleep(3); RETURN NEW;"
				}
				if _, err := testPool.Exec(context.Background(), `CREATE FUNCTION employee_capability_test_reject_link() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN `+body+` END $$; CREATE TRIGGER employee_capability_test_reject_link BEFORE INSERT ON context_config_link FOR EACH ROW EXECUTE FUNCTION employee_capability_test_reject_link()`); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					_, _ = testPool.Exec(context.Background(), `DROP TRIGGER employee_capability_test_reject_link ON context_config_link; DROP FUNCTION employee_capability_test_reject_link()`)
				})
			}
			if r := employeeHTTP(t, f, dc, uuid.NewString()); r.Code != 202 {
				t.Fatal(r.Body.String())
			}
			var receipt string
			if err := testPool.QueryRow(context.Background(), `SELECT receipt_id::text FROM employee_event_consumption WHERE agent_id=$1`, f.agentID).Scan(&receipt); err != nil {
				t.Fatal(err)
			}
			const answer = "这是能力介绍原答。"
			f.h.EmployeeSceneWorker.model = employeeReplyModelFunc(func(context.Context, openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
				return employeeReplyCompletion(t, employeeReplyCall(t, "capability-failure", "describe_capabilities", map[string]any{"source_ref": receipt + "/message-1", "reply": answer})), nil
			})
			if _, err := f.h.EmployeeSceneWorker.ProcessNext(context.Background()); err != nil {
				t.Fatal(err)
			}
			var raw []byte
			if err := testPool.QueryRow(context.Background(), `SELECT outcome FROM employee_scene_job WHERE agent_id=$1`, f.agentID).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			var saved employeeSavedOutcome
			if err := json.Unmarshal(raw, &saved); err != nil {
				t.Fatal(err)
			}
			if saved.Outcome.Reply != answer {
				t.Fatalf("lost answer on link error: %s", saved.Outcome.Reply)
			}
		})
	}
}

func TestEmployeeSceneCapabilitiesReadUsesDirectoryAndRejectsModelScope(t *testing.T) {
	f, _, dc := employeeFixture(t)
	f.command.CompletionCallback = nil
	f.command.ResponsePolicy = nil
	if r := employeeHTTP(t, f, dc, uuid.NewString()); r.Code != 202 {
		t.Fatal(r.Body.String())
	}
	job, err := f.h.EmployeeSceneWorker.store.Claim(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var env employeeDispatchEnvelope
	if err = json.Unmarshal(job.Items[0].Payload, &env); err != nil {
		t.Fatal(err)
	}
	host := &employeeSceneHost{worker: f.h.EmployeeSceneWorker, job: job, envelopes: []employeeDispatchEnvelope{env}}
	source := employeeSourceMessages(job.Items[0], env)[0]
	identity := employeeloop.Identity{WorkspaceID: job.Scope.WorkspaceID, AgentID: job.Scope.AgentID, TenantOrgID: job.Scope.TenantOrgID, Scene: scene.Ref{SceneID: job.Scope.SceneID}, ReceiptID: job.Items[0].ReceiptID}
	result, err := host.Execute(context.Background(), identity, employeeloop.ToolCall{Name: "scene_config_get", NativeToolCallID: "read-scene", Arguments: map[string]any{"source_ref": source.SourceRef}})
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err = json.Unmarshal([]byte(result.Content), &value); err != nil {
		t.Fatal(err)
	}
	if value["scene"].(map[string]any)["scene_id"] != job.Scope.SceneID || value["read_only"] != nil || !strings.Contains(fmt.Sprint(value["how_to_change"]), "dispatch_task") {
		t.Fatalf("read is not the Host directory scene: %s", result.Content)
	}
	if _, err = host.Execute(context.Background(), identity, employeeloop.ToolCall{Name: "scene_config_get", NativeToolCallID: "read-forged", Arguments: map[string]any{"source_ref": source.SourceRef, "scene_id": uuid.NewString()}}); err == nil {
		t.Fatal("model chose a scene")
	}
	if _, err = testPool.Exec(context.Background(), `UPDATE agent SET archived_at=now() WHERE id=$1`, f.agentID); err != nil {
		t.Fatal(err)
	}
	if _, err = host.Execute(context.Background(), identity, employeeloop.ToolCall{Name: "describe_capabilities", NativeToolCallID: "revoked-link", Arguments: map[string]any{"source_ref": source.SourceRef, "reply": "Here is the link"}}); err == nil {
		t.Fatal("archived employee minted a scene link")
	}
}

func TestSceneConfigGlobalAndOfferedDisabledSwitchKeepsUnion(t *testing.T) {
	f := newCtxcapFixture(t)
	if _, err := testPool.Exec(context.Background(), `INSERT INTO context_capability_binding (workspace_id,agent_id,scope_type,org_id,scope_key,resource_type,resource_id,enabled) VALUES ($1,$2,'offer','','','skill',$3,true),($1,$2,'scene',$4,$5,'skill',$3,false)`, testWorkspaceID, f.agent, f.skillAgent, ctxcapOrg, ctxcapScene); err != nil {
		t.Fatal(err)
	}
	target, ok, err := f.h.taskConfigScene(context.Background(), f.ws, f.task(t, ctxcapDispatch("group", ctxcapScene, ctxcapStaff)))
	if err != nil || !ok {
		t.Fatalf("target %v %v", ok, err)
	}
	result, err := f.h.sceneConfigGet(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	caps := result.(map[string]any)["capabilities"].(map[string]any)
	always := caps["always_on"].(map[string]any)["skills"].([]sceneConfigNamedItem)
	offered := caps["offered"].(map[string]any)["skills"].([]sceneConfigNamedItem)
	if len(always) != 1 || always[0].ID != f.skillAgent || !always[0].Enabled {
		t.Fatalf("global grant lost: %v", always)
	}
	found := false
	for _, skill := range offered {
		if skill.ID == f.skillAgent {
			found = true
			if skill.Enabled {
				t.Fatal("offered disabled switch became globally enabled")
			}
		}
	}
	if !found {
		t.Fatal("global and offered projection lost overlap")
	}
	input, err := employeeCapabilityInput(t, f, []DispatchMessage{{OpenMsgID: "one", SenderUID: "alice"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(input.Config.Persona.Expertise, "\n"), "ctxcap-agent-skill") {
		t.Fatal("scene off revoked global union grant")
	}
}

func TestEmployeeSceneCapabilitiesRejectDepartedPrincipal(t *testing.T) {
	f := newCtxcapFixture(t)
	user := uuid.NewString()
	if _, err := testPool.Exec(context.Background(), `INSERT INTO "user"(id,name,email) VALUES($1,'Capability operator',$2)`, user, user+"@capability.test"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM member WHERE user_id=$1`, user)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM "user" WHERE id=$1`, user)
	})
	if _, err := testPool.Exec(context.Background(), `INSERT INTO member(workspace_id,user_id,role) VALUES($1,$2,'member')`, testWorkspaceID, user); err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(context.Background(), `UPDATE agent SET owner_id=$2 WHERE id=$1`, f.agent, user); err != nil {
		t.Fatal(err)
	}
	job := employeeentry.Job{Scope: employeeentry.Scope{WorkspaceID: testWorkspaceID, AgentID: uuidToString(f.agent), TenantOrgID: ctxcapOrg, SceneID: ctxcapScene}}
	envs := []employeeDispatchEnvelope{{PrincipalID: user, Command: DispatchCommand{Event: DispatchEvent{Data: DispatchEventData{Messages: []DispatchMessage{{OpenMsgID: "one", SenderUID: "alice"}}}}}}}
	if _, err := employeeSceneCapabilities(context.Background(), f.h, job, envs); err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(context.Background(), `DELETE FROM member WHERE workspace_id=$1 AND user_id=$2`, testWorkspaceID, user); err != nil {
		t.Fatal(err)
	}
	if _, err := employeeSceneCapabilities(context.Background(), f.h, job, envs); err == nil {
		t.Fatal("departed owner read capability prompts")
	}
}

func TestSceneConfigGlobalConnectorRequiresMatchingWorkspace(t *testing.T) {
	f := newCtxcapFixture(t)
	if _, err := testPool.Exec(context.Background(), `UPDATE internal_connector SET workspace_id=$2 WHERE id=$1`, f.global, uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	target, ok, err := f.h.taskConfigScene(context.Background(), f.ws, f.task(t, ctxcapDispatch("group", ctxcapScene, ctxcapStaff)))
	if err != nil || !ok {
		t.Fatalf("target %v %v", ok, err)
	}
	result, err := f.h.sceneConfigGet(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	always := result.(map[string]any)["capabilities"].(map[string]any)["always_on"].(map[string]any)["connectors"].([]sceneConfigNamedItem)
	if len(always) != 0 {
		t.Fatalf("other workspace connector leaked: %v", always)
	}
}

func TestEmployeeSceneCapabilitiesGlobalMCPDisabledIsNotEnabled(t *testing.T) {
	f := newCtxcapFixture(t)
	if _, err := testPool.Exec(context.Background(), `UPDATE agent SET mcp_config='{"mcpServers":{"disabled-remote":{"url":"https://example.test/mcp","disabled":true}}}'::jsonb WHERE id=$1`, f.agent); err != nil {
		t.Fatal(err)
	}
	input, err := employeeCapabilityInput(t, f, []DispatchMessage{{OpenMsgID: "one", SenderUID: "alice"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, label := range input.Config.Persona.Expertise {
		if strings.Contains(label, "disabled-remote") && !strings.Contains(label, "disabled in configuration") {
			t.Fatal("disabled global MCP was not identified as disabled")
		}
	}
}

func TestEmployeeSceneCapabilityMintRestoresTimeoutAndJournal(t *testing.T) {
	for _, tc := range []struct{ name, prior, trigger string }{{"success", "5s", ""}, {"timeout", "5s", "PERFORM pg_sleep(3); RETURN NEW;"}, {"tighter_timeout", "100ms", "PERFORM pg_sleep(3); RETURN NEW;"}, {"storage_error", "5s", "RAISE EXCEPTION 'mint rejected';"}} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			f, _, dc := employeeFixture(t)
			f.command.CompletionCallback = nil
			f.command.ResponsePolicy = nil
			f.h.cfg.AppURL = "https://app.multica.example"
			t.Cleanup(func() { _, _ = testPool.Exec(ctx, `DELETE FROM context_config_link WHERE agent_id=$1`, f.agentID) })
			if tc.trigger != "" {
				if _, err := testPool.Exec(ctx, `CREATE FUNCTION employee_capability_test_slow_link() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN `+tc.trigger+` END $$; CREATE TRIGGER employee_capability_test_slow_link BEFORE INSERT ON context_config_link FOR EACH ROW EXECUTE FUNCTION employee_capability_test_slow_link()`); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					_, _ = testPool.Exec(ctx, `DROP TRIGGER employee_capability_test_slow_link ON context_config_link; DROP FUNCTION employee_capability_test_slow_link()`)
				})
			}
			if r := employeeHTTP(t, f, dc, uuid.NewString()); r.Code != 202 {
				t.Fatal(r.Body.String())
			}
			cfg := testPool.Config().Copy()
			cfg.MaxConns = 1
			pool, err := pgxpool.NewWithConfig(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer pool.Close()
			if _, err = pool.Exec(ctx, `SET statement_timeout='7500ms'`); err != nil {
				t.Fatal(err)
			}
			store := employeeentry.NewStore(pool)
			job, err := store.Claim(ctx)
			if err != nil {
				t.Fatal(err)
			}
			host := &employeeSceneHost{worker: f.h.EmployeeSceneWorker, job: job}
			const answer = "原答保留，后续工具日志仍可提交。"
			raw, err := store.ExecuteTool(ctx, job, "mint-timeout-probe", json.RawMessage(`{"name":"describe_capabilities"}`), nil, func(tx pgx.Tx) (json.RawMessage, error) {
				var before, after string
				if e := tx.QueryRow(ctx, `SELECT set_config('statement_timeout',$1,true)`, tc.prior).Scan(&before); e != nil {
					return nil, e
				}
				reply := host.capabilityReply(ctx, tx, answer)
				if e := tx.QueryRow(ctx, `SHOW statement_timeout`).Scan(&after); e != nil {
					return nil, e
				}
				if after != before {
					return nil, fmt.Errorf("mint leaked statement_timeout: before=%s after=%s", before, after)
				}
				if tc.trigger != "" && reply != answer {
					return nil, fmt.Errorf("failed mint changed original answer")
				}
				if tc.trigger == "" && !strings.Contains(reply, "dingtalkclient/page/link") {
					return nil, fmt.Errorf("success mint did not attach link")
				}
				return json.Marshal(employeeToolRecord{Result: employeeloop.ToolResult{Content: "recorded", Terminal: &employeeloop.Decision{Kind: employeeloop.Reply, Reply: answer}}, DeliveryReply: reply})
			})
			if err != nil {
				t.Fatalf("mint broke outer tool journal transaction: %v", err)
			}
			var stored []byte
			var timeout string
			if err = pool.QueryRow(ctx, `SELECT tool_journal->'mint-timeout-probe'->'result' FROM employee_scene_job WHERE id=$1`, job.ID).Scan(&stored); err != nil {
				t.Fatal(err)
			}
			var got, want employeeToolRecord
			if err = json.Unmarshal(stored, &got); err != nil {
				t.Fatal(err)
			}
			if err = json.Unmarshal(raw, &want); err != nil {
				t.Fatal(err)
			}
			if got.DeliveryReply != want.DeliveryReply || got.Result.Terminal.Reply != answer {
				t.Fatal("tool journal did not commit original answer")
			}
			if err = pool.QueryRow(ctx, `SHOW statement_timeout`).Scan(&timeout); err != nil {
				t.Fatal(err)
			}
			if timeout != "7500ms" {
				t.Fatalf("mint leaked pooled session setting: %s", timeout)
			}
		})
	}
}

// These are model-input contracts, not evidence that a real model will obey
// the requested route or voice; the real capability traces need a new canary.
func TestEmployeeCapabilityIntroductionContract(t *testing.T) {
	f := newCtxcapFixture(t)
	input, err := employeeCapabilityInput(t, f, []DispatchMessage{{OpenMsgID: "capabilities", SenderUID: "alice", Text: "请简短介绍能力并给配置入口，只介绍。"}})
	if err != nil {
		t.Fatal(err)
	}
	prompt := employeeloop.BuildPrompt(input.Config.Persona)
	for _, rule := range []string{"describe_capabilities on the first model call", "Only use scene_config_get when the user explicitly asks for configuration details", "1–3 short sentences", "not internal tool names", "DWS/dws-shortcuts", "Foreground boundary:", "Scene self-management is background work you arrange with dispatch_task"} {
		if !strings.Contains(prompt, rule) {
			t.Errorf("capability introduction contract missing %q", rule)
		}
	}
	for _, label := range input.Config.Persona.Expertise {
		if !strings.HasPrefix(label, "skill ") && !strings.HasPrefix(label, "connector ") && !strings.HasPrefix(label, "remote MCP ") {
			continue
		}
		for _, field := range []string{"configuration_enabled=", "runtime_availability=", "layer="} {
			if strings.Contains(label, field) {
				t.Errorf("executor label invites field echo: %s", label)
			}
		}
	}
	for _, tool := range input.Config.Tools {
		switch tool.Name {
		case "describe_capabilities":
			if !strings.Contains(tool.Description, "first model call") || !strings.Contains(tool.Description, "brief") {
				t.Error("introduction tool does not prefer a brief first-call answer")
			}
		case "scene_config_get":
			if !strings.Contains(tool.Description, "explicit configuration-detail") || !strings.Contains(tool.Description, "not a prerequisite") {
				t.Error("config read tool still invites unconditional inspection")
			}
		}
	}
	if !strings.Contains(prompt, "at most three model calls") {
		t.Fatal("foreground budget contract disappeared")
	}
}

// A routine, prompt, switch or MCP server change is executor work. The
// foreground once read "configuration only, do not promise" and a read_only
// flag as "this cannot be done" and refused to dispatch (2026-10-03 Qwen-DWS).
func TestEmployeeSceneSelfManagementDispatchContract(t *testing.T) {
	f := newCtxcapFixture(t)
	input, err := employeeCapabilityInput(t, f, []DispatchMessage{{OpenMsgID: "routine", SenderUID: "alice", Text: "每隔15分钟给我讲个笑话，建个例行任务。"}})
	if err != nil {
		t.Fatal(err)
	}
	prompt := employeeloop.BuildPrompt(input.Config.Persona)
	for _, rule := range []string{"create, change, pause, resume, delete or run now a routine", "config-qwen-tag-scene", "never answer that you cannot do it", "Only a task result can show that something is impossible", "DWS lookups (contacts, managers"} {
		if !strings.Contains(prompt, rule) {
			t.Errorf("self-management boundary missing %q", rule)
		}
	}
	for _, refusal := range []string{"do not advertise or promise", "Employee-triggered execution is unverified", "grants no write permission"} {
		if strings.Contains(prompt, refusal) {
			t.Errorf("prompt still tells the model to refuse: %q", refusal)
		}
	}
	found := map[string]bool{}
	for _, tool := range input.Config.Tools {
		found[tool.Name] = true
		switch tool.Name {
		case "dispatch_task":
			if !strings.Contains(tool.Description, "routine/定时任务") || !strings.Contains(tool.Description, "instead of declining") {
				t.Error("dispatch_task does not cover scene self-management")
			}
		case "scene_config_get":
			if strings.Contains(tool.Description, "no write permission") || !strings.Contains(tool.Description, "goes straight to dispatch_task") {
				t.Error("scene_config_get still reads as a refusal")
			}
		case "describe_capabilities":
			if !strings.Contains(tool.Description, "go through dispatch_task") {
				t.Error("describe_capabilities does not route changes to dispatch_task")
			}
		}
	}
	if !found["dispatch_task"] || !found["scene_config_get"] || !found["describe_capabilities"] {
		t.Fatalf("frozen tools missing: %v", found)
	}
}

func TestEmployeeCapabilityExplicitConfigurationContract(t *testing.T) {
	f := newCtxcapFixture(t)
	input, err := employeeCapabilityInput(t, f, []DispatchMessage{{OpenMsgID: "config-details", SenderUID: "alice", Text: "列出技能准确名称、开关和提示词原文，并给配置链接。"}})
	if err != nil {
		t.Fatal(err)
	}
	prompt := employeeloop.BuildPrompt(input.Config.Persona)
	for _, rule := range []string{"For ordinary introductions,", "For explicit configuration questions, preserve exact skill names, switch states, and stored prompt text", "link-only requests"} {
		if !strings.Contains(prompt, rule) {
			t.Errorf("explicit detail route missing %q", rule)
		}
	}
	if strings.Contains(prompt, "never quote its labels or fields") {
		t.Fatal("unconditional catalog hiding contradicts requested exact configuration")
	}
	for _, tool := range input.Config.Tools {
		if tool.Name == "describe_capabilities" {
			field := tool.Schema["properties"].(map[string]any)["reply"].(map[string]any)["description"].(string)
			if !strings.Contains(tool.Description, "configuration details plus a link") || !strings.Contains(field, "requested exact names, states, and prompt text") {
				t.Fatal("terminal link tool prevents detailed configuration answers")
			}
		}
	}
}
