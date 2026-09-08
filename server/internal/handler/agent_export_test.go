package handler

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/multica-ai/multica/server/internal/agentsource"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func exportAgentFiles(t *testing.T, agentID string) map[string]string {
	t.Helper()
	w := httptest.NewRecorder()
	testHandler.ExportAgent(w, withURLParam(newRequest(http.MethodGet, "/", nil), "id", agentID))
	if w.Code != http.StatusOK { t.Fatalf("export status %d: %s", w.Code, w.Body.String()) }
	if w.Header().Get("Content-Type") != "application/zip" || w.Header().Get("Cache-Control") != "no-store" { t.Fatal("invalid download headers") }
	reader, err := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len()))
	if err != nil { t.Fatal(err) }
	files := map[string]string{}
	for _, file := range reader.File {
		r, err := file.Open(); if err != nil { t.Fatal(err) }
		content, err := io.ReadAll(r); r.Close(); if err != nil { t.Fatal(err) }
		files[file.Name] = string(content)
	}
	return files
}

func TestAgentExportIncludesCurrentPlatformConfiguration(t *testing.T) {
	agentID := createHandlerTestAgent(t, "Export manual", nil)
	if _, err := testPool.Exec(t.Context(), `UPDATE agent SET instructions = 'Exported instructions', persona = 'Current persona', model = 'current-model', chat_session_resume = false, custom_env = '{"EXPORT_TEST_SECRET":"do-not-export"}', mcp_config = '{"mcpServers":{"tools":{"url":"https://tools.example.test/mcp","headers":{"Authorization":"private-header"}}}}' WHERE id = $1`, agentID); err != nil { t.Fatal(err) }
	files := exportAgentFiles(t, agentID)
	if files["AGENTS.md"] != "Exported instructions" { t.Fatal("current instructions were not exported") }
	var manifest struct { Version string; Configuration map[string]any; Bindings map[string]any; Access map[string]any }
	if err := json.Unmarshal([]byte(files[agentsource.PortableManifestPath]), &manifest); err != nil { t.Fatal(err) }
	if manifest.Version != "multica.agent/v2" || manifest.Configuration["persona"] != "Current persona" || manifest.Configuration["model"] != "current-model" || manifest.Configuration["chat_session_resume"] != false { t.Fatal("platform configuration missing from exported manifest") }
	if len(manifest.Configuration) != 23 || manifest.Bindings["runtime"] == nil || manifest.Access["permission_mode"] == nil { t.Fatal("export must include all configuration groups") }
	for _, content := range files { if strings.Contains(content, "do-not-export") || strings.Contains(content, "private-header") { t.Fatal("export leaked credentials") } }
	if !strings.Contains(files[agentsource.PortableManifestPath], "secret_ref") { t.Fatal("credentials must be represented as references") }
	if _, err := agentsource.ValidateManifestJSON([]byte(files[agentsource.PortableManifestPath])); err != nil { t.Fatal(err) }
}

func TestAgentExportGitSourceRoundTripPreservesDisabledSkills(t *testing.T) {
	f := newGitSourceFixture(t)
	agentID := f.create(t)
	skills, err := testHandler.Queries.ListAgentSkills(t.Context(), parseUUID(agentID))
	if err != nil || len(skills) != 1 { t.Fatalf("skills = %#v: %v", skills, err) }
	if _, err := testHandler.Queries.SetAgentSkillEnabled(t.Context(), db.SetAgentSkillEnabledParams{AgentID:parseUUID(agentID), SkillID:skills[0].ID, Enabled:false}); err != nil { t.Fatal(err) }
	if _, err := testPool.Exec(t.Context(), `UPDATE agent SET instructions = 'Platform edit' WHERE id = $1`, agentID); err != nil { t.Fatal(err) }
	if _, err := testPool.Exec(t.Context(), `UPDATE skill SET content = 'Current skill content' WHERE id = $1`, skills[0].ID); err != nil { t.Fatal(err) }
	files := exportAgentFiles(t, agentID)
	fs := fstest.MapFS{}
	for name, content := range files { fs[name] = &fstest.MapFile{Data:[]byte(content)} }
	parsed, err := agentsource.ParseAgentPackageFS(t.Context(), fs)
	if err != nil { t.Fatal(err) }
	if parsed.Instructions != "Platform edit" || len(parsed.Skills) != 1 || !parsed.Skills[0].Disabled || parsed.Skills[0].Content != "Current skill content" { t.Fatal("export did not preserve current platform content and disabled skill") }
}

func TestAgentExportRequiresManagePermission(t *testing.T) {
	agentID := createHandlerTestAgent(t, "Export permissions", nil)
	memberID := createPlainMember(t, "agent-export-member@example.test")
	for _, userID := range []string{memberID, "00000000-0000-0000-0000-000000000001"} {
		w := httptest.NewRecorder()
		testHandler.ExportAgent(w, withURLParam(newRequestAs(userID, http.MethodGet, "/", nil), "id", agentID))
		if w.Code != http.StatusForbidden && w.Code != http.StatusNotFound { t.Fatalf("unauthorized export: %d %s", w.Code, w.Body.String()) }
	}
}

func TestAgentExportPreservesOKRsAndA2APolicies(t *testing.T) {
	agentID := createHandlerTestAgent(t, "Export policy", nil)
	response := setAgentOKRsForTest(t, agentID, map[string]any{"okrs":[]any{map[string]any{"objective":"Improve reviews", "key_results":[]string{"Review every release"}}}})
	if response.Code != http.StatusOK { t.Fatalf("create OKRs: %d %s", response.Code, response.Body.String()) }
	response = putAgentA2ATestConfig(t, agentID, testUserID, false)
	if response.Code != http.StatusOK { t.Fatalf("configure A2A: %d %s", response.Code, response.Body.String()) }
	response = createAgentA2ATestClient(t, agentID, testUserID, map[string]any{"name":"Review client", "scopes":[]string{"send", "read"}, "rate_limit_per_minute":int32(15)})
	if response.Code != http.StatusCreated { t.Fatalf("create A2A client: %d %s", response.Code, response.Body.String()) }
	files := exportAgentFiles(t, agentID)
	var manifest struct {
		OKRs []struct { Objective string; KeyResults []string `json:"key_results"` } `json:"okrs"`
		A2A struct { Enabled bool; Clients []struct { Name string; RateLimit int `json:"rate_limit_per_minute"`; MaxConcurrent any `json:"max_concurrent_tasks"` } } `json:"a2a"`
	}
	if err := json.Unmarshal([]byte(files[agentsource.PortableManifestPath]), &manifest); err != nil { t.Fatal(err) }
	if len(manifest.OKRs) != 1 || manifest.OKRs[0].Objective != "Improve reviews" || len(manifest.OKRs[0].KeyResults) != 1 || manifest.OKRs[0].KeyResults[0] != "Review every release" { t.Fatal("OKR definition lost") }
	if manifest.A2A.Enabled || len(manifest.A2A.Clients) != 1 || manifest.A2A.Clients[0].Name != "Review client" || manifest.A2A.Clients[0].RateLimit != 15 || manifest.A2A.Clients[0].MaxConcurrent != nil { t.Fatal("A2A policy or nullable limits lost") }
}

func TestAgentExportReplacesPrivateCommandAndExtensionValues(t *testing.T) {
	agentID := createHandlerTestAgent(t, "Export private config", []byte(`{"mcpServers":{"tool":{"type":"stdio","command":"sh -c 'private-command'","args":["--token","private-argument"],"extension":{"url":"https://example.test?token=private-url","value":"private-extension"}},"array-tool":{"type":"local","command":["node","--token","private-array-argument"]}}}`))
	files := exportAgentFiles(t, agentID)
	for _, content := range files {
		for _, secret := range []string{"private-command", "private-argument", "private-extension", "private-url", "private-array-argument"} {
			if strings.Contains(content, secret) { t.Fatal("private provider config leaked") }
		}
	}
	if _, err := agentsource.ValidateManifestJSON([]byte(files[agentsource.PortableManifestPath])); err != nil { t.Fatal(err) }
}

func TestAgentExportUsesResourceAliasesForRuntimeSkillsAndMembers(t *testing.T) {
	agentID := createHandlerTestAgent(t, "Export resource references", nil)
	agent, err := testHandler.Queries.GetAgent(t.Context(), parseUUID(agentID)); if err != nil { t.Fatal(err) }
	runtimeID := uuidToString(agent.RuntimeID)
	disabled, err := json.Marshal([]DisabledRuntimeSkill{{RuntimeID:runtimeID, Provider:"codex", Root:"plugin", Key:"review", Plugin:"review-tools"}}); if err != nil { t.Fatal(err) }
	if _, err := testPool.Exec(t.Context(), `UPDATE agent SET disabled_runtime_skills = $1 WHERE id = $2`, disabled, agentID); err != nil { t.Fatal(err) }
	if err := testHandler.Queries.CreateAgentInvocationTarget(t.Context(), db.CreateAgentInvocationTargetParams{AgentID:agent.ID, TargetType:"member", TargetID:parseUUID(testUserID), CreatedBy:parseUUID(testUserID)}); err != nil { t.Fatal(err) }
	files := exportAgentFiles(t, agentID)
	var manifest struct {
		Bindings struct { Runtime struct { Ref string } }
		Disabled []struct { RuntimeRef string `json:"runtime_ref"`; Plugin string } `json:"disabled_runtime_skills"`
		Access struct { Targets []struct { Ref string; Type string `json:"target_type"` } `json:"invocation_targets"` }
	}
	if err := json.Unmarshal([]byte(files[agentsource.PortableManifestPath]), &manifest); err != nil { t.Fatal(err) }
	if len(manifest.Disabled) != 1 || manifest.Disabled[0].RuntimeRef != manifest.Bindings.Runtime.Ref || manifest.Disabled[0].Plugin != "review-tools" { t.Fatal("disabled runtime skill lost its runtime binding") }
	if len(manifest.Access.Targets) != 2 { t.Fatal("access targets missing") }
	for _, target := range manifest.Access.Targets { if target.Type == "member" && target.Ref == "" { t.Fatal("member reference missing") } }
	if strings.Contains(files[agentsource.PortableManifestPath], runtimeID) || strings.Contains(files[agentsource.PortableManifestPath], testUserID) { t.Fatal("instance IDs must not be used as portable resource identities") }
}
