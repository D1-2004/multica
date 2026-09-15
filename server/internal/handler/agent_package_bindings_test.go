package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"github.com/multica-ai/multica/server/internal/agentsource"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/internal/integrations/agentidentitygithub"
)

func TestPackageExportPreservesUnresolvedResourceAndSecretAliases(t *testing.T) {
	f := newGitSourceFixture(t)
	manifest := `{"$schema":"agent.schema.json","version":"multica.agent/v2","name":"Portable bindings","instructions":"AGENTS.md","skills":[],"bindings":{"github_identity":{"ref":"code-maintainer"},"enterprise_identity":{"ref":"employee"}},"configuration":{"custom_env":{"SERVICE_TOKEN":{"secret_ref":"service-token"}}}}`
	w := httptest.NewRecorder()
	f.handler.PreviewAgentPackage(w, agentPackageRequest(t, agentPackageFixture(t, manifest), false))
	if w.Code != http.StatusOK { t.Fatalf("preview: %d %s", w.Code, w.Body.String()) }
	var preview map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &preview); err != nil { t.Fatal(err) }
	id := rawString(t, preview["preview_id"])
	created := f.request(t, f.handler.CreateAgentFromPackage, testWorkspaceID, map[string]any{
		"preview_id":id, "runtime_id":testRuntimeID, "name":"Bindings " + id,
		"deferred_bindings":[]string{"/bindings/github_identity", "/bindings/enterprise_identity"},
		"secrets":map[string]string{"service-token":"private-fixture-value"},
	}, http.StatusCreated)
	var agent AgentResponse
	if err := json.Unmarshal(created["agent"], &agent); err != nil { t.Fatal(err) }
	// Simulate an Agent imported before the binding-state migration. Its
	// applied package snapshot must recover declarations without any receipts.
	if _, err := testPool.Exec(t.Context(),`UPDATE agent_source SET package_binding_state = '{}' WHERE agent_id = $1`,agent.ID); err != nil { t.Fatal(err) }
	files := exportAgentFiles(t, agent.ID)
	for _, alias := range []string{"code-maintainer", "employee", "service-token"} {
		if !strings.Contains(files["agent.json"], `"` + alias + `"`) { t.Errorf("export lost declared alias %q", alias) }
	}
	for name, content := range files {
		if strings.Contains(content, "private-fixture-value") { t.Fatalf("export leaked a credential into %s", name) }
	}
}

func TestPackageBindingConfirmationRequiresCurrentAgentResource(t *testing.T) {
	f := newGitSourceFixture(t)
	manifest := `{"$schema":"agent.schema.json","version":"multica.agent/v2","name":"Binding confirmation","instructions":"AGENTS.md","skills":[],"bindings":{"github_identity":{"ref":"author"}}}`
	w := httptest.NewRecorder()
	f.handler.PreviewAgentPackage(w, agentPackageRequest(t, agentPackageFixture(t, manifest), false))
	if w.Code != http.StatusOK { t.Fatal(w.Body.String()) }
	var preview map[string]json.RawMessage
	_ = json.Unmarshal(w.Body.Bytes(), &preview)
	created := f.request(t, f.handler.CreateAgentFromPackage, testWorkspaceID, map[string]any{"preview_id":rawString(t,preview["preview_id"]), "runtime_id":testRuntimeID, "deferred_bindings":[]string{"/bindings/github_identity"}}, http.StatusCreated)
	var agent AgentResponse
	_ = json.Unmarshal(created["agent"], &agent)
	status := f.request(t, f.handler.GetAgentPackageBindings, agent.ID, nil, http.StatusOK)
	var report packageBindingReport
	if err := json.Unmarshal(mustPackageJSON(t,status), &report); err != nil { t.Fatal(err) }
	if len(report.Bindings) != 1 || report.Bindings[0].Status != "unavailable" { t.Fatalf("unexpected status: %#v", report) }
	f.request(t, f.handler.ConfirmAgentPackageBinding, agent.ID, map[string]any{"path":"/bindings/github_identity", "revision":report.Revision, "current_fingerprint":"another-agent-identity"}, http.StatusConflict)
}

func mustPackageJSON(t *testing.T, value any) []byte { t.Helper(); data, err := json.Marshal(value); if err != nil { t.Fatal(err) }; return data }

func TestPackageBindingVerifiedReuseAndDrift(t *testing.T) {
	f := newGitSourceFixture(t)
	manifest := `{"$schema":"agent.schema.json","version":"multica.agent/v2","name":"Reusable binding","instructions":"AGENTS.md","skills":[],"bindings":{"github_identity":{"ref":"author"}},"configuration":{"custom_env":{"TOKEN":{"secret_ref":"auth"}},"persona":"Before"}}`
	for _, sha := range []string{gitSourceSHA1,gitSourceSHA2} { f.files[sha] = map[string]string{"agent.json":manifest,"AGENTS.md":"Instructions"} }
	w := httptest.NewRecorder()
	f.handler.PreviewAgentPackage(w,agentPackageRequest(t,agentPackageFixture(t,manifest),false))
	if w.Code != http.StatusOK { t.Fatal(w.Body.String()) }
	var preview map[string]json.RawMessage; _ = json.Unmarshal(w.Body.Bytes(),&preview)
	created := f.request(t,f.handler.CreateAgentFromPackage,testWorkspaceID,map[string]any{"preview_id":rawString(t,preview["preview_id"]),"runtime_id":testRuntimeID,"secrets":map[string]string{"auth":"private-reuse-fixture"},"deferred_bindings":[]string{"/bindings/github_identity"}},http.StatusCreated)
	var response AgentResponse; _ = json.Unmarshal(created["agent"],&response)
	connectionID := "same-agent-connection"
	identity := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request) {
		if r.URL.Query().Get("agentId") != response.ID || r.URL.Query().Get("workspaceId") != testWorkspaceID { t.Error("identity was queried outside the destination Agent") }
		writeJSON(w,http.StatusOK,map[string]any{"ok":true,"connectionId":connectionID,"accountLogin":"owner","status":"ACTIVE"})
	}))
	defer identity.Close()
	f.handler.AgentIdentityGitHub = agentidentitygithub.NewClient(agentidentitygithub.Config{BaseURL:identity.URL})
	status := f.request(t,f.handler.GetAgentPackageBindings,response.ID,nil,http.StatusOK)
	var report packageBindingReport; _ = json.Unmarshal(mustPackageJSON(t,status),&report)
	if len(report.Bindings) != 1 || report.Bindings[0].Status != "pending" { t.Fatalf("expected pending: %#v",report) }
	item := report.Bindings[0]
	var actual portableResource; _ = json.Unmarshal(item.Current,&actual)
	body := map[string]any{"path":item.Path,"revision":report.Revision,"current_fingerprint":item.CurrentFingerprint,"mappings":map[string]string{"author":actual.Ref}}
	f.request(t,f.handler.ConfirmAgentPackageBinding,response.ID,body,http.StatusOK)
	f.request(t,f.handler.ConfirmAgentPackageBinding,response.ID,body,http.StatusConflict)
	agent, err := testHandler.Queries.GetAgent(t.Context(),parseUUID(response.ID)); if err != nil { t.Fatal(err) }
	parsed, err := agentsource.ParseAgentPackage(t.Context(),agentPackageFixture(t,strings.Replace(manifest,"Before","After",1))); if err != nil { t.Fatal(err) }
	bundle, err := parsed.Bundle(); if err != nil { t.Fatal(err) }
	requirements, err := f.handler.packageRequirementsForAgent(t.Context(),f.handler.Queries,agent,testUserID,bundle)
	if err != nil || len(requirements.Secrets) != 0 || len(requirements.DeferredBindings) != 0 { t.Fatalf("verified inputs were not reused: %#v %v",requirements,err) }
	source, err := f.handler.Queries.GetAgentSourceByAgentID(t.Context(),agent.ID); if err != nil { t.Fatal(err) }
	tx, err := f.handler.TxStarter.Begin(t.Context()); if err != nil { t.Fatal(err) }; defer tx.Rollback(t.Context())
	if err := (agentPackageService{handler:f.handler}).Import(t.Context(),tx,agent,source,preparedAgentSource{bundle:bundle},nil,nil,agent.OwnerID,false); err != nil { t.Fatal(err) }
	if err := tx.Commit(t.Context()); err != nil { t.Fatal(err) }
	updated, err := f.handler.Queries.GetAgent(t.Context(),agent.ID); if err != nil || !strings.Contains(string(updated.CustomEnv),"private-reuse-fixture") { t.Fatal("publication lost reused secret") }
	connectionID = "different-connection"
	requirements, err = f.handler.packageRequirementsForAgent(t.Context(),f.handler.Queries,updated,testUserID,bundle)
	if err != nil || len(requirements.DeferredBindings) != 1 { t.Fatal("resource drift was silently accepted") }
	files := exportAgentFiles(t,response.ID)
	if strings.Contains(files["agent.json"],"private-reuse-fixture") || !strings.Contains(files["agent.json"],`"auth"`) { t.Fatal("secret alias export failed") }
}

func TestPackageEventTriggerRoundTripAndRollback(t *testing.T) {
	f := newGitSourceFixture(t)
	f.handler.EventTriggers = service.NewEventTriggerService(testPool,nil)
	manifest := `{"$schema":"agent.schema.json","version":"multica.agent/v2","name":"Events","instructions":"AGENTS.md","skills":[],"configuration":{"event_trigger_enabled":true}}`
	w := httptest.NewRecorder(); f.handler.PreviewAgentPackage(w,agentPackageRequest(t,agentPackageFixture(t,manifest),false))
	if w.Code != http.StatusOK { t.Fatal(w.Body.String()) }
	var preview map[string]json.RawMessage; _ = json.Unmarshal(w.Body.Bytes(),&preview)
	created := f.request(t,f.handler.CreateAgentFromPackage,testWorkspaceID,map[string]any{"preview_id":rawString(t,preview["preview_id"]),"runtime_id":testRuntimeID},http.StatusCreated)
	var response AgentResponse; _ = json.Unmarshal(created["agent"],&response)
	if !response.EventTriggerEnabled || !response.InboundCoordinator { t.Fatal("events did not enable inbound coordination") }
	files := exportAgentFiles(t,response.ID)
	var exported struct { Configuration struct { Enabled bool `json:"event_trigger_enabled"` } `json:"configuration"` }
	if err := json.Unmarshal([]byte(files["agent.json"]),&exported); err != nil || !exported.Configuration.Enabled { t.Fatal("event setting was not exported") }
	agent, err := f.handler.Queries.GetAgent(t.Context(),parseUUID(response.ID)); if err != nil { t.Fatal(err) }
	source, err := f.handler.Queries.GetAgentSourceByAgentID(t.Context(),agent.ID); if err != nil { t.Fatal(err) }
	conflict := strings.Replace(manifest,`"event_trigger_enabled":true`,`"event_trigger_enabled":true,"inbound_coordinator":false`,1)
	parsed, err := agentsource.ParseAgentPackage(t.Context(),agentPackageFixture(t,conflict)); if err != nil { t.Fatal(err) }
	bundle, err := parsed.Bundle(); if err != nil { t.Fatal(err) }
	tx, err := f.handler.TxStarter.Begin(t.Context()); if err != nil { t.Fatal(err) }
	if err := (agentPackageService{handler:f.handler}).Import(t.Context(),tx,agent,source,preparedAgentSource{bundle:bundle},nil,nil,agent.OwnerID,false); err != nil { _ = tx.Rollback(t.Context()); t.Fatal(err) }
	q := f.handler.Queries.WithTx(tx)
	enabled, err := q.GetAgentPackageEventTriggerEnabled(t.Context(),db.GetAgentPackageEventTriggerEnabledParams{AgentID:agent.ID,WorkspaceID:agent.WorkspaceID}); if err != nil || enabled { t.Fatal("explicit inbound disable did not win") }
	if err := tx.Rollback(t.Context()); err != nil { t.Fatal(err) }
	enabled, err = f.handler.EventTriggers.Enabled(t.Context(),agent.ID,agent.WorkspaceID); if err != nil || !enabled { t.Fatal("event configuration escaped the package transaction") }
}

func TestEmptyPluginImportReportsReady(t *testing.T) {
	f := newGitSourceFixture(t)
	var manifest map[string]any
	if err := json.Unmarshal([]byte(f.files[gitSourceSHA1]["agent.json"]),&manifest); err != nil { t.Fatal(err) }
	manifest["dsh_plugins"] = []any{}
	manifest["version"] = "multica.agent/v2"
	encoded,err := json.Marshal(manifest); if err != nil { t.Fatal(err) }; f.files[gitSourceSHA1]["agent.json"] = string(encoded)
	id := f.create(t)
	response := f.request(t,f.handler.GetAgentPackageBindings,id,nil,http.StatusOK)
	var report packageBindingReport
	if err := json.Unmarshal(mustPackageJSON(t,response),&report); err != nil { t.Fatal(err) }
	for _,binding := range report.Bindings { if binding.Path == "/dsh_plugins" { if binding.Status != "ready" { t.Fatalf("empty plugin binding is %s",binding.Status) }; return } }
	t.Fatal("plugin declaration absent from binding report")
}
