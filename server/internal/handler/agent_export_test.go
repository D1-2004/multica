package handler

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

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

func TestAgentExportManualSourceOmitsWorkspaceConfiguration(t *testing.T) {
	agentID := createHandlerTestAgent(t, "Export manual", nil)
	if _, err := testPool.Exec(t.Context(), `UPDATE agent SET instructions = 'Exported instructions', custom_env = '{"EXPORT_TEST_SECRET":"do-not-export"}' WHERE id = $1`, agentID); err != nil { t.Fatal(err) }
	files := exportAgentFiles(t, agentID)
	if len(files) != 3 || files["AGENTS.md"] != "Exported instructions" { t.Fatalf("unexpected source files: %v", files) }
	var manifest agentsource.PortableManifest
	if err := json.Unmarshal([]byte(files[agentsource.PortableManifestPath]), &manifest); err != nil { t.Fatal(err) }
	if len(manifest.Skills) != 0 || manifest.Name != "Export manual" { t.Fatalf("manifest = %#v", manifest) }
}

func TestAgentExportGitSourceRoundTripPreservesDisabledSkills(t *testing.T) {
	f := newGitSourceFixture(t)
	agentID := f.create(t)
	skills, err := testHandler.Queries.ListAgentSkills(t.Context(), parseUUID(agentID))
	if err != nil || len(skills) != 1 { t.Fatalf("skills = %#v: %v", skills, err) }
	if _, err := testHandler.Queries.SetAgentSkillEnabled(t.Context(), db.SetAgentSkillEnabledParams{AgentID:parseUUID(agentID), SkillID:skills[0].ID, Enabled:false}); err != nil { t.Fatal(err) }
	files := exportAgentFiles(t, agentID)
	f.files[gitSourceSHA2] = files
	preview := f.request(t, f.handler.PreviewGitHubAgent, testWorkspaceID, map[string]any{"installation_id":f.installationID, "repository":"acme/reviewer", "ref":"release/v2"}, http.StatusOK)
	created := f.request(t, f.handler.CreateGitHubAgent, testWorkspaceID, map[string]any{"preview_id":rawString(t, preview["preview_id"]), "runtime_id":testRuntimeID, "name":"Roundtrip " + f.installationID}, http.StatusCreated)
	var result struct { ID string `json:"id"` }; json.Unmarshal(created["agent"], &result)
	assigned, err := testHandler.Queries.ListAgentSkillSummaries(t.Context(), parseUUID(result.ID))
	if err != nil || len(assigned) != 1 || assigned[0].Enabled { t.Fatalf("disabled skill not preserved: %#v %v", assigned, err) }
	source, err := testHandler.Queries.GetAgentSourceByAgentID(t.Context(), parseUUID(result.ID))
	if err != nil || source.ManifestPath != agentsource.PortableManifestPath { t.Fatalf("source path = %q: %v", source.ManifestPath, err) }
	// Switching the original DTA agent to the exported format uses the same
	// reviewed publish flow and updates the recorded manifest path.
	syncPreview := f.request(t, f.handler.PreviewAgentSourceSync, agentID, map[string]any{"ref":"release/v2"}, http.StatusOK)
	f.request(t, f.handler.SyncAgentSource, agentID, map[string]any{"preview_id":rawString(t, syncPreview["preview_id"])}, http.StatusOK)
	source, err = testHandler.Queries.GetAgentSourceByAgentID(t.Context(), parseUUID(agentID))
	if err != nil || source.ManifestPath != agentsource.PortableManifestPath { t.Fatal("publish did not update manifest path") }
	assigned, err = testHandler.Queries.ListAgentSkillSummaries(t.Context(), parseUUID(agentID))
	if err != nil || len(assigned) != 1 || assigned[0].Enabled { t.Fatal("publish changed disabled state") }
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
