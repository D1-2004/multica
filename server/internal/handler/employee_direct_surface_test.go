package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/employeetask"
	"github.com/multica-ai/multica/server/internal/scene"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestEmployeeDirectExecutionSurfacePreservesContext(t *testing.T) {
	b := newCtxBuilder(t)
	b.configureIdentityTenant(t)
	b.h.TaskService = &service.TaskService{Queries: b.h.Queries, TxStarter: testPool, Bus: testHandler.Bus}
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := testPool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`UPDATE agent SET system_key=$2,instructions=$3 WHERE id=$1`, b.agent, service.MikaSystemKey, "ROLE_SENTINEL: user-owned multica issue instructions")
	// The platform skill prefix is legal in an explicitly bound workspace skill.
	exec(`UPDATE skill SET name='multica-user-owned' WHERE id=$1`, b.skillAgent)
	w := setAgentOKRsForTest(t, b.agentID, map[string]any{"okrs": []map[string]any{{"objective": "PLATFORM_OKR_SENTINEL", "key_results": []string{}}}})
	if w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `DELETE FROM agent_okr WHERE agent_id=$1`, b.agent)
		_, _ = testPool.Exec(ctx, `DELETE FROM issue_label WHERE description=$1`, agentOKRLabelDescription)
	})
	machineID := uuid.NewString()
	exec(`INSERT INTO runner_machine(id,owner_id,name,os,arch,public_key,client_version,last_seen_at,connection_id) VALUES($1,$2,'direct-surface-runner','linux','amd64',$3,'test',now(),$4)`, machineID, testUserID, []byte(strings.ReplaceAll(uuid.NewString(), "-", "")), uuid.NewString())
	exec(`INSERT INTO agent_runner_binding(workspace_id,agent_id,machine_id,bound_by,roots,enabled_mcp_servers) VALUES($1,$2,$3,$4,'[]','{"runner-tool":"fingerprint"}')`, testWorkspaceID, b.agentID, machineID, testUserID)
	exec(`INSERT INTO runner_mcp_config(machine_id,config,revision) VALUES($1,$2,'rev-1')`, machineID, []byte(`{"mcpServers":{"runner-tool":{"command":"test-runner-tool"}}}`))
	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `DELETE FROM agent_runner_binding WHERE agent_id=$1`, b.agentID)
		_, _ = testPool.Exec(ctx, `DELETE FROM runner_mcp_config WHERE machine_id=$1`, machineID)
		_, _ = testPool.Exec(ctx, `DELETE FROM runner_machine WHERE id=$1`, machineID)
	})
	original, err := b.h.Queries.GetAgentRuntime(ctx, parseUUID(testRuntimeID))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `UPDATE agent_runtime SET runtime_mode=$2,provider=$3,daemon_id=$4,metadata=$5 WHERE id=$1`, original.ID, original.RuntimeMode, original.Provider, original.DaemonID, original.Metadata)
	})
	exec(`UPDATE agent_runtime SET runtime_mode='local',provider='codex',daemon_id='direct-surface',metadata='{"client_capabilities":["employee-direct-v1"]}' WHERE id=$1`, original.ID)
	runtime, err := b.h.Queries.GetAgentRuntime(ctx, original.ID)
	if err != nil {
		t.Fatal(err)
	}
	et, err := employeetask.NewStore(testPool).Create(ctx, employeetask.CreateParams{
		Scope:     employeetask.Scope{WorkspaceID: testWorkspaceID, AgentID: b.agentID, TenantOrgID: ctxcapOrg, Kind: employeetask.ScopeScene, Scene: scene.Ref{SceneID: ctxcapScene}},
		OwnerLoop: employeetask.LoopEmployee, DispatchMode: employeetask.DispatchDirect, RequesterRef: testUserID,
		Definition: employeetask.Definition{Goal: "DIRECT_SURFACE"}, Source: employeetask.Source{Namespace: "surface", Key: uuid.NewString()}, Input: "DIRECT_SURFACE",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, table := range []string{"employee_task_entry", "employee_task_run", "employee_task"} {
			_, _ = testPool.Exec(ctx, `DELETE FROM `+table+` WHERE agent_id=$1`, b.agent)
		}
	})
	admitted, err := b.h.TaskService.EnqueueDirectTask(ctx, service.DirectTaskRequest{Task: et, Source: employeetask.Source{Namespace: "surface", Key: "run"}, Prompt: "DIRECT_SURFACE", PrincipalID: parseUUID(testUserID), OriginatorUserID: parseUUID(testUserID), Context: ctxBuilderGroupTask(ctxcapStaff, ctxcapStaff)})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := b.h.TaskService.ClaimTaskForRuntime(ctx, runtime.ID, service.TaskClaimAuthorization{EmployeeDirectRuntimeIDs: []pgtype.UUID{runtime.ID}})
	if err != nil || claimed == nil || claimed.ID != admitted.Task.ID {
		t.Fatal("claim", claimed, err)
	}
	ordinary := b.task(t, ctxBuilderGroupTask(ctxcapStaff, ctxcapStaff))
	ordinary, err = b.h.Queries.GetAgentTask(ctx, ordinary.ID)
	if err != nil {
		t.Fatal(err)
	}
	var inline []service.AgentSkillData
	for _, tc := range []struct {
		name   string
		task   db.AgentTaskQueue
		refs   bool
		direct bool
	}{{"direct inline", *claimed, false, true}, {"direct bundles", *claimed, true, true}, {"ordinary", ordinary, false, false}} {
		t.Run(tc.name, func(t *testing.T) {
			req := newDaemonTokenRequest(http.MethodPost, "/", nil, testWorkspaceID, runtime.DaemonID.String)
			caps := protocol.DaemonCapabilityEmployeeDirectV1
			if tc.refs {
				caps += "," + protocol.DaemonCapabilitySkillBundlesV1
			}
			req.Header.Set("X-Client-Capabilities", caps)
			resp, _, _, _, failure := b.h.buildClaimedTaskResponse(req, &tc.task, runtime, "", testRuntimeID, testWorkspaceID)
			if failure != nil || resp.Agent == nil {
				t.Fatal("build claim", failure)
			}
			instructions := resp.Agent.Instructions
			for _, want := range []string{"ROLE_SENTINEL: user-owned multica issue instructions", "org rules", "group tone", "person note"} {
				if !strings.Contains(instructions, want) {
					t.Errorf("missing context %q", want)
				}
			}
			if tc.direct {
				if strings.Contains(instructions, "PLATFORM_OKR_SENTINEL") {
					t.Error("Direct received platform OKR")
				}
				if !strings.HasPrefix(instructions, "ROLE_SENTINEL:") {
					t.Error("Direct received platform Mika preamble")
				}
				if strings.Contains(instructions, "org tone") {
					t.Error("scene override lost")
				}
			} else if !strings.Contains(instructions, "PLATFORM_OKR_SENTINEL") || strings.HasPrefix(instructions, "ROLE_SENTINEL:") {
				t.Error("ordinary platform instructions lost")
			}
			skills := resp.Agent.Skills
			if tc.refs {
				request := newDaemonTokenRequest(http.MethodPost, "/", map[string]any{"skills": resp.Agent.SkillRefs}, testWorkspaceID, runtime.DaemonID.String)
				request = withURLParams(request, "runtimeId", testRuntimeID, "taskId", uuidToString(claimed.ID))
				result := httptest.NewRecorder()
				b.h.ResolveTaskSkillBundles(result, request)
				if result.Code != http.StatusOK {
					t.Fatal(result.Code, result.Body.String())
				}
				var resolved struct {
					Bundles []service.AgentSkillData `json:"bundles"`
				}
				if err := json.Unmarshal(result.Body.Bytes(), &resolved); err != nil {
					t.Fatal(err)
				}
				skills = resolved.Bundles
				bundled, _ := service.BuildAgentSkillBundles(inline)
				wantJSON, err := json.Marshal(bundled)
				if err != nil {
					t.Fatal(err)
				}
				gotJSON, err := json.Marshal(skills)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(wantJSON, gotJSON) {
					t.Error("inline and resolved bundle wire content differ")
				}
			} else if tc.direct {
				inline = skills
			}
			ids := map[string]bool{}
			for _, skill := range skills {
				ids[skill.ID] = true
			}
			for _, id := range []string{b.skillAgent, b.skillScene, b.orgSkill} {
				if !ids[id] {
					t.Errorf("missing bound/context skill %s", id)
				}
			}
			names := map[string]bool{}
			for _, skill := range skills {
				names[skill.Name] = true
			}
			for _, want := range []string{"multica-user-owned", service.SceneConfigSkillName} {
				if !names[want] {
					t.Errorf("skill missing %s: %v", want, names)
				}
			}
			for _, builtin := range b.h.TaskService.BuiltinSkills() {
				if names[builtin.Name] == tc.direct {
					t.Errorf("built-in %s wrong for direct=%v", builtin.Name, tc.direct)
				}
			}
			if err := b.h.injectRunnerMCP(ctx, runtime, tc.task, "task-token", resp.Agent, true, true); err != nil {
				t.Fatal(err)
			}
			if route := resp.Agent.McpRelayRoutes["runner-tool"]; !strings.HasPrefix(route.Path, "/api/runner-mcp/mounts/") || route.Authorization != "Bearer task-token" {
				t.Errorf("Runner route lost: %#v", route)
			}
			servers := ctxBuilderMCPServers(t, resp.Agent.McpConfig)
			_, hasPlatform := resp.Agent.McpRelayRoutes["multica"]
			if hasPlatform == tc.direct || (servers["multica"] != "") == tc.direct {
				t.Error("managed multica MCP wrong", servers)
			}
			for _, want := range []string{sceneConfigMCPServerName, connectorServerName(b.global), connectorServerName(b.scene), connectorServerName(b.orgConn), "base", "docs", "crm", "notes", "runner-tool"} {
				if servers[want] == "" {
					t.Errorf("MCP missing %s", want)
				}
			}
		})
	}
}
