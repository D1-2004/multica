package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/agentsource"
)

func TestBuilderPackagePreparesDownloadsAndCreatesThroughSharedConfirmation(t *testing.T) {
	f := newGitSourceFixture(t)
	manifest := json.RawMessage(`{"$schema":"agent.schema.json","version":"multica.agent/v2","name":"Complete Builder package","instructions":"AGENTS.md","skills":[{"path":"skills/review","name":"review","enabled":false}],"configuration":{"persona":"Builder persona","max_concurrent_tasks":3},"okrs":[{"objective":"Builder quality","key_results":["Builder reviews"]}]}`)
	body := map[string]any{"manifest":manifest, "files":map[string]string{"AGENTS.md":"Builder instructions", "skills/review/SKILL.md":"Review", "skills/review/references/check.md":"Check details"}}
	preview := f.request(t, f.handler.PrepareAgentPackage, testWorkspaceID, body, http.StatusOK)
	id := rawString(t, preview["preview_id"])
	r := withURLParam(newRequest(http.MethodGet, "/", nil), "id", testWorkspaceID)
	chi.RouteContext(r.Context()).URLParams.Add("previewId", id)
	w := httptest.NewRecorder(); f.handler.DownloadPreparedAgentPackage(w, r)
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/zip" { t.Fatalf("download: %d %s", w.Code, w.Body.String()) }
	parsed, err := agentsource.ParseAgentPackage(t.Context(), w.Body.Bytes()); if err != nil { t.Fatal(err) }
	bundle, err := parsed.Bundle(); if err != nil || bundle.Hash != rawString(t, preview["package_hash"]) { t.Fatalf("download differs from preview: %v", err) }
	created := f.request(t, f.handler.CreateAgentFromPackage, testWorkspaceID, map[string]any{"preview_id":id, "runtime_id":testRuntimeID}, http.StatusCreated)
	var agent AgentResponse; _ = json.Unmarshal(created["agent"], &agent)
	if agent.Persona != "Builder persona" || agent.MaxConcurrentTasks != 3 || agent.Instructions != "Builder instructions" { t.Fatal("Builder created a partial form draft") }
	files := exportAgentFiles(t, agent.ID)
	if files["skills/review/references/check.md"] != "Check details" { t.Fatal("Builder supporting file was lost") }
	other := createPlainMember(t, "builder-package-other@example.test")
	r.Header.Set("X-User-ID", other)
	w = httptest.NewRecorder(); f.handler.DownloadPreparedAgentPackage(w, r)
	if w.Code != http.StatusForbidden { t.Fatalf("another user downloaded the package: %d", w.Code) }
}

func TestBuilderPackageContractUpgradeOnlyTouchesOwnedSystemBuilder(t *testing.T) {
	created := newBuilderSession(t)
	if _, err := testPool.Exec(t.Context(), `UPDATE agent SET instructions = 'Old Builder prompt' WHERE id = $1`, created.BuilderAgentID); err != nil { t.Fatal(err) }
	agent, err := testHandler.Queries.GetAgent(t.Context(), parseUUID(created.BuilderAgentID)); if err != nil { t.Fatal(err) }
	r := newRequest(http.MethodPost, "/", nil)
	input := "MULTICA_AGENT_BUILDER_INPUT\n" + `{"protocol":"multica.agent-package/v1"}`
	updated, err := testHandler.refreshBuilderPackageContract(r, agent, input)
	if err != nil || !strings.Contains(updated.Instructions, "<agent_package>") || !strings.Contains(updated.Instructions, string(agentsource.PortableSchema)) { t.Fatalf("old Builder contract was not refreshed: %v", err) }
	regularID := createHandlerTestAgent(t, "User instructions stay", nil)
	regular, err := testHandler.Queries.GetAgent(t.Context(), parseUUID(regularID)); if err != nil { t.Fatal(err) }
	unchanged, err := testHandler.refreshBuilderPackageContract(r, regular, input)
	if err != nil || unchanged.Instructions != regular.Instructions { t.Fatal("Builder upgrade changed a user Agent") }
}
