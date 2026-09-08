package handler

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func zipPackageFiles(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var content bytes.Buffer
	archive := zip.NewWriter(&content)
	for name, body := range files { writer, err := archive.Create(name); if err != nil { t.Fatal(err) }; if _, err := writer.Write([]byte(body)); err != nil { t.Fatal(err) } }
	if err := archive.Close(); err != nil { t.Fatal(err) }
	return content.Bytes()
}

func TestLocalAndGitPackagesCreateTheSameConfiguration(t *testing.T) {
	f := newGitSourceFixture(t)
	files := map[string]string{
		"agent.json":`{"$schema":"agent.schema.json","version":"multica.agent/v2","name":"Package parity","instructions":"AGENTS.md","skills":[{"path":"skills/review","name":"review","enabled":false}],"configuration":{"persona":"Careful reviewer","reply_tone":"Be concise","avatar_url":"emoji:🧪","inbound_coordinator":true,"chat_session_resume":true,"dingtalk_response_enabled":false,"dingtalk_show_ai_tag":false,"dispatch_always_new_issue":true,"task_finished_loop_enabled":true,"scene_memory_write_enabled":true,"scene_memory_recall_enabled":true,"scene_memory_ui_enabled":true,"scene_memory_bootstrap_enabled":true,"dispatch_prompt_overrides":{"policy":"Inspect safely"},"model":"test-model","max_concurrent_tasks":3,"custom_args":["--test-flag"],"custom_env":{"PACKAGE_TOKEN":{"secret_ref":"test-token"}},"runtime_config":{"llm_trace":{"enabled":false}},"mcp_config":{"mcpServers":{}}},"access":{"permission_mode":"private","invocation_targets":[]},"a2a":{"enabled":false,"card_name":"Review","card_version":"1.2.3","card_skills":[],"clients":[{"key":"review","name":"Review client","status":"disabled","scopes":["send","read"],"rate_limit_per_minute":7}]}}`,
		"AGENTS.md":"Review package contents.", "skills/review/SKILL.md":"Review carefully.", "skills/review/references/check.md":"Preserve this reference.",
	}
	f.files[gitSourceSHA1] = files
	git := f.request(t, f.handler.PreviewGitHubAgent, testWorkspaceID, map[string]any{"installation_id":f.installationID,"repository":"acme/reviewer","ref":"main"}, http.StatusOK)
	localResponse := httptest.NewRecorder()
	f.handler.PreviewAgentPackage(localResponse, agentPackageRequest(t, zipPackageFiles(t,files), true))
	if localResponse.Code != http.StatusOK { t.Fatalf("local preview: %d %s",localResponse.Code,localResponse.Body.String()) }
	var local map[string]json.RawMessage
	if err := json.Unmarshal(localResponse.Body.Bytes(), &local); err != nil { t.Fatal(err) }
	for index, preview := range []map[string]json.RawMessage{local,git} {
		id := rawString(t,preview["preview_id"])
		body := map[string]any{"preview_id":id,"runtime_id":testRuntimeID,"name":"Parity " + id,"secrets":map[string]string{"test-token":"test-value"}}
		result := f.request(t,f.handler.CreateAgentFromPackage,testWorkspaceID,body,http.StatusCreated)
		var response AgentResponse
		if err := json.Unmarshal(result["agent"],&response); err != nil { t.Fatal(err) }
		if response.Persona != "Careful reviewer" || response.ReplyTone != "Be concise" || !response.InboundCoordinator || !response.SceneMemoryBootstrapEnabled || response.DingTalkResponseEnabled || response.MaxConcurrentTasks != 3 || response.Model != "test-model" { t.Fatalf("configuration missing: %#v",response) }
		row, err := testHandler.Queries.GetAgent(t.Context(),parseUUID(response.ID)); if err != nil { t.Fatal(err) }
		if !strings.Contains(string(row.CustomEnv),"test-value") || !row.DispatchAlwaysNewIssue || !strings.Contains(string(row.DispatchPromptOverrides),"Inspect safely") { t.Fatal("runtime configuration lost") }
		skills, err := testHandler.Queries.ListAgentSkillSummaries(t.Context(),row.ID); if err != nil || len(skills) != 1 || skills[0].Enabled { t.Fatalf("disabled skill lost: %v",err) }
		origin, err := testHandler.Queries.GetAgentSourceByAgentID(t.Context(),row.ID); if err != nil { t.Fatal(err) }
		if index == 0 && (origin.SourceType != "local" || origin.GithubInstallationID.Valid) { t.Fatal("local upload acquired Git provenance") }
		files, err := testHandler.Queries.ListSkillFiles(t.Context(),skills[0].ID); if err != nil || len(files) != 1 || files[0].Content != "Preserve this reference." { t.Fatal("supporting file lost") }
		other := createHandlerTestAgent(t,"Cannot reassign " + id,nil)
		f.request(t,f.handler.AddAgentSkills,other,map[string]any{"skill_ids":[]string{uuidToString(skills[0].ID)}},http.StatusBadRequest)
		clients, err := testHandler.Queries.ListAgentA2AClientsForOwner(t.Context(),db.ListAgentA2AClientsForOwnerParams{WorkspaceID:row.WorkspaceID,AgentID:row.ID,OwnerUserID:row.OwnerID}); if err != nil || len(clients) != 1 || clients[0].Status != "disabled" { t.Fatalf("A2A policy lost: %v",err) }
		replayed := f.request(t,f.handler.CreateAgentFromPackage,testWorkspaceID,body,http.StatusOK)
		var again AgentResponse; _ = json.Unmarshal(replayed["agent"],&again)
		if again.ID != response.ID { t.Fatal("confirmation was not idempotent") }
	}
}

func TestLocalPackageRequiresExplicitSecretsAndBindingChoices(t *testing.T) {
	f := newGitSourceFixture(t)
	content := agentPackageFixture(t,`{"$schema":"agent.schema.json","version":"multica.agent/v2","name":"References","instructions":"AGENTS.md","skills":[],"configuration":{"custom_env":{"TOKEN":{"secret_ref":"token"}}},"bindings":{"enterprise_identity":{"ref":"employee"}}}`)
	preview := httptest.NewRecorder(); f.handler.PreviewAgentPackage(preview,agentPackageRequest(t,content,false))
	if preview.Code != http.StatusOK { t.Fatalf("preview: %d %s",preview.Code,preview.Body.String()) }
	var data map[string]json.RawMessage; _ = json.Unmarshal(preview.Body.Bytes(),&data)
	id := rawString(t,data["preview_id"])
	body := map[string]any{"preview_id":id,"runtime_id":testRuntimeID,"name":"Reference " + id}
	f.request(t,f.handler.CreateAgentFromPackage,testWorkspaceID,body,http.StatusUnprocessableEntity)
	body["deferred_bindings"] = []string{"/bindings/enterprise_identity"}
	f.request(t,f.handler.CreateAgentFromPackage,testWorkspaceID,body,http.StatusUnprocessableEntity)
	body["secrets"] = map[string]string{"token":"fixture-value"}
	f.request(t,f.handler.CreateAgentFromPackage,testWorkspaceID,body,http.StatusCreated)
}

func TestCompleteExamplePackageUploadAndExport(t *testing.T) {
	f := newGitSourceFixture(t)
	root := "../../../docs/examples/package-inspector-agent"
	files := map[string]string{}
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() { return err }
		content, err := os.ReadFile(path); if err != nil { return err }
		name, err := filepath.Rel(root,path); if err != nil { return err }; files[filepath.ToSlash(name)] = string(content); return nil
	}); err != nil { t.Fatal(err) }
	preview := httptest.NewRecorder(); f.handler.PreviewAgentPackage(preview,agentPackageRequest(t,zipPackageFiles(t,files),true))
	if preview.Code != http.StatusOK { t.Fatalf("example upload: %d %s",preview.Code,preview.Body.String()) }
	var data map[string]json.RawMessage; _ = json.Unmarshal(preview.Body.Bytes(),&data)
	id := rawString(t,data["preview_id"])
	created := f.request(t,f.handler.CreateAgentFromPackage,testWorkspaceID,map[string]any{"preview_id":id,"runtime_id":testRuntimeID,"name":"Example " + id,"deferred_bindings":[]string{"/bindings/runner","/bindings/enterprise_identity","/bindings/github_identity","/bindings/dingtalk_account"}},http.StatusCreated)
	var agent AgentResponse; _ = json.Unmarshal(created["agent"],&agent)
	skills, err := testHandler.Queries.ListAgentSkillSummaries(t.Context(),parseUUID(agent.ID)); if err != nil || len(skills) != 2 { t.Fatal("example skills missing") }
	enabled := 0; for _, skill := range skills { if skill.Enabled { enabled++ } }; if enabled != 1 { t.Fatal("example skill states were not preserved") }
	okrs, err := testHandler.Queries.ListAgentOKRs(t.Context(),db.ListAgentOKRsParams{WorkspaceID:parseUUID(testWorkspaceID),AgentID:parseUUID(agent.ID)}); if err != nil || len(okrs) != 3 { t.Fatalf("example OKRs missing: %v",err) }
	exported := exportAgentFiles(t,agent.ID)
	for name, content := range files { if name == "AGENTS.md" || strings.HasPrefix(name,"skills/") { if exported[name] != content { t.Fatalf("export lost %s",name) } } }
	if strings.Contains(exported["agent.json"],`"AGENT_PACKAGE_TEST": "enabled"`) { t.Fatal("export should use a secret reference for environment values") }
	// Existing workspace labels cannot be taken over by a second import.
	another := httptest.NewRecorder(); f.handler.PreviewAgentPackage(another,agentPackageRequest(t,zipPackageFiles(t,files),false))
	_ = json.Unmarshal(another.Body.Bytes(),&data)
	newID := rawString(t,data["preview_id"])
	name := "Conflicting OKR " + newID
	f.request(t,f.handler.CreateAgentFromPackage,testWorkspaceID,map[string]any{"preview_id":newID,"runtime_id":testRuntimeID,"name":name,"deferred_bindings":[]string{"/bindings/runner","/bindings/enterprise_identity","/bindings/github_identity","/bindings/dingtalk_account"}},http.StatusConflict)
	exists, err := testHandler.Queries.AgentNameExistsInWorkspace(t.Context(),db.AgentNameExistsInWorkspaceParams{WorkspaceID:parseUUID(testWorkspaceID),Name:name}); if err != nil || exists { t.Fatal("failed import left a partial Agent") }
}

func TestV2PackagePublicationAppliesConfigurationAndRejectsStalePreview(t *testing.T) {
	f := newGitSourceFixture(t)
	for sha, persona := range map[string]string{gitSourceSHA1:"First",gitSourceSHA2:"Second"} {
		f.files[sha] = map[string]string{"agent.json":`{"$schema":"agent.schema.json","version":"multica.agent/v2","name":"Publication","instructions":"AGENTS.md","skills":[],"configuration":{"persona":"` + persona + `","chat_session_resume":false,"max_concurrent_tasks":2,"mcp_config":null}}`,"AGENTS.md":"Package instructions"}
	}
	id := f.create(t)
	preview := f.request(t,f.handler.PreviewAgentSourceSync,id,map[string]any{"ref":"release/v2"},http.StatusOK)
	if !strings.Contains(string(preview["configuration_changes"]),"Second") { t.Fatal("preview omitted configuration changes") }
	if _, err := testPool.Exec(t.Context(),`UPDATE agent SET persona = 'Local change' WHERE id = $1`,id); err != nil { t.Fatal(err) }
	f.request(t,f.handler.SyncAgentSource,id,map[string]any{"preview_id":rawString(t,preview["preview_id"])},http.StatusConflict)
	preview = f.request(t,f.handler.PreviewAgentSourceSync,id,map[string]any{"ref":"release/v2"},http.StatusOK)
	f.request(t,f.handler.SyncAgentSource,id,map[string]any{"preview_id":rawString(t,preview["preview_id"])},http.StatusOK)
	voice, err := testHandler.Queries.GetAgentVoice(t.Context(),parseUUID(id)); if err != nil || voice.Persona != "Second" { t.Fatalf("published configuration missing: %v",err) }
	row, err := testHandler.Queries.GetAgent(t.Context(),parseUUID(id)); if err != nil || row.MaxConcurrentTasks != 2 || len(row.McpConfig) != 0 { t.Fatal("published execution settings missing") }
}

func TestPackageA2APublicationUsesStableKeysAndPreservesManualClients(t *testing.T) {
	f := newGitSourceFixture(t)
	for sha, name := range map[string]string{gitSourceSHA1:"Before", gitSourceSHA2:"After"} {
		f.files[sha] = map[string]string{"agent.json":`{"$schema":"agent.schema.json","version":"multica.agent/v2","name":"A2A package ` + name + `","description":"` + name + `","instructions":"AGENTS.md","skills":[],"a2a":{"clients":[{"key":"stable","name":"` + name + `","status":"disabled","scopes":["send","read"]}]}}`, "AGENTS.md":"Instructions"}
	}
	id := f.create(t)
	agent, err := testHandler.Queries.GetAgent(t.Context(),parseUUID(id)); if err != nil { t.Fatal(err) }
	mappings, err := readPackageClientMappings(t.Context(),testHandler.Queries,agent.ID); if err != nil || mappings["stable"] == "" { t.Fatal("missing source client mapping") }
	clientID := mappings["stable"]
	manual, err := testHandler.Queries.CreateAgentA2AClientForOwner(t.Context(),db.CreateAgentA2AClientForOwnerParams{WorkspaceID:agent.WorkspaceID,AgentID:agent.ID,OwnerUserID:agent.OwnerID,ActorUserID:agent.OwnerID,Name:"Manual",Scopes:[]string{"read"}}); if err != nil { t.Fatal(err) }
	preview := f.request(t,f.handler.PreviewAgentSourceSync,id,map[string]any{"ref":"release/v2"},http.StatusOK)
	f.request(t,f.handler.SyncAgentSource,id,map[string]any{"preview_id":rawString(t,preview["preview_id"])},http.StatusOK)
	mappings, err = readPackageClientMappings(t.Context(),testHandler.Queries,agent.ID); if err != nil || mappings["stable"] != clientID { t.Fatal("renamed source client changed identity") }
	clients, err := testHandler.Queries.ListAgentA2AClientsForOwner(t.Context(),db.ListAgentA2AClientsForOwnerParams{WorkspaceID:agent.WorkspaceID,AgentID:agent.ID,OwnerUserID:agent.OwnerID}); if err != nil || len(clients) != 2 { t.Fatal("client duplicated or manual client removed") }
	for _, client := range clients { if client.ID == manual.ID && client.Status != manual.Status { t.Fatal("manual client was modified") }; if uuidToString(client.ID) == clientID && client.Name != "After" { t.Fatal("source policy did not follow its stable key") } }
	agent, err = testHandler.Queries.GetAgent(t.Context(),agent.ID); if err != nil || agent.Name != "A2A package After" || agent.Description != "After" { t.Fatal("v2 profile was not published") }
	manifest, _, err := buildAgentExportManifest(t.Context(),testHandler.Queries,agent,"agent.json"); if err != nil { t.Fatal(err) }
	encoded, _ := json.Marshal(manifest["a2a"]); if !strings.Contains(string(encoded),`"key":"stable"`) { t.Fatal("export lost stable source client key") }
	f.files[gitSourceSHA2]["agent.json"] = strings.Replace(f.files[gitSourceSHA2]["agent.json"],`"clients":[{"key":"stable","name":"After","status":"disabled","scopes":["send","read"]}]`,`"clients":[]`,1)
	preview = f.request(t,f.handler.PreviewAgentSourceSync,id,map[string]any{"ref":"release/v2"},http.StatusOK)
	f.request(t,f.handler.SyncAgentSource,id,map[string]any{"preview_id":rawString(t,preview["preview_id"])},http.StatusOK)
	clients, err = testHandler.Queries.ListAgentA2AClientsForOwner(t.Context(),db.ListAgentA2AClientsForOwnerParams{WorkspaceID:agent.WorkspaceID,AgentID:agent.ID,OwnerUserID:agent.OwnerID}); if err != nil { t.Fatal(err) }
	for _, client := range clients { if client.ID == manual.ID && client.Status != manual.Status { t.Fatal("empty source list revoked manual client") }; if uuidToString(client.ID) == clientID && client.Status != "revoked" { t.Fatal("removed source client was not revoked") } }
}
