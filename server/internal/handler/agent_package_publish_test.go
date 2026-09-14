package handler

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func packagePublishPreview(t *testing.T, h *Handler, id string, files map[string]string, status int) map[string]json.RawMessage {
	t.Helper()
	r := withURLParam(newRequest(http.MethodPost, "/", nil), "id", id)
	r.Body = io.NopCloser(bytes.NewReader(zipPackageFiles(t, files)))
	r.Header.Set("Content-Type", "application/zip")
	w := httptest.NewRecorder()
	h.PreviewAgentSourceSync(w, r)
	if w.Code != status { t.Fatalf("ZIP preview: %d, want %d: %s", w.Code, status, w.Body.String()) }
	var result map[string]json.RawMessage; _ = json.Unmarshal(w.Body.Bytes(), &result)
	return result
}

func (f *gitSourceFixture) createLocal(t *testing.T) string {
	t.Helper()
	w := httptest.NewRecorder()
	f.handler.PreviewAgentPackage(w,agentPackageRequest(t,zipPackageFiles(t,f.files[gitSourceSHA1]),false))
	if w.Code != http.StatusOK { t.Fatalf("local source preview: %d %s",w.Code,w.Body.String()) }
	var preview map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(),&preview); err != nil { t.Fatal(err) }
	created := f.request(t,f.handler.CreateAgentFromPackage,testWorkspaceID,map[string]any{"preview_id":rawString(t,preview["preview_id"]),"runtime_id":testRuntimeID,"name":"Local "+rawString(t,preview["preview_id"])},http.StatusCreated)
	var agent AgentResponse
	if err := json.Unmarshal(created["agent"],&agent); err != nil { t.Fatal(err) }
	return agent.ID
}

func TestZIPPublicationUpdatesExistingLocalAgents(t *testing.T) {
	f := newGitSourceFixture(t)
	manualID := createHandlerTestAgent(t, "ZIP manual target", nil)
	if _, err := testPool.Exec(t.Context(), `UPDATE agent SET runtime_id = $2 WHERE id = $1`, manualID, testRuntimeID); err != nil { t.Fatal(err) }
	for _, id := range []string{f.createLocal(t), manualID} {
		files := map[string]string{"agent.json":`{"$schema":"agent.schema.json","version":"multica.agent/v2","name":"ZIP ` + id + `","instructions":"AGENTS.md","skills":[{"path":"skills/new","name":"new skill","enabled":true}],"configuration":{"persona":"ZIP persona"}}`, "AGENTS.md":"ZIP instructions", "skills/new/SKILL.md":"New skill", "skills/new/references/review.md":"New reference"}
		preview := packagePublishPreview(t, f.handler, id, files, http.StatusOK)
		if !strings.Contains(string(preview["configuration_changes"]), "ZIP persona") { t.Fatal("ZIP diff omitted configuration") }
		if id == manualID {
			var count int
			if err := testPool.QueryRow(t.Context(), `SELECT COUNT(*) FROM agent_source WHERE agent_id = $1`, id).Scan(&count); err != nil || count != 0 { t.Fatal("preview created a source before confirmation") }
		}
		// A publish preview must never be accepted by the create endpoint.
		f.request(t, f.handler.CreateAgentFromPackage, testWorkspaceID, map[string]any{"preview_id":rawString(t, preview["preview_id"]), "runtime_id":testRuntimeID}, http.StatusBadRequest)
		if _, err := testPool.Exec(t.Context(), `UPDATE agent SET persona = 'Concurrent edit' WHERE id = $1`, id); err != nil { t.Fatal(err) }
		f.request(t, f.handler.SyncAgentSource, id, map[string]any{"preview_id":rawString(t, preview["preview_id"])}, http.StatusConflict)
		preview = packagePublishPreview(t, f.handler, id, files, http.StatusOK)
		confirm := map[string]any{"preview_id":rawString(t, preview["preview_id"])}
		f.request(t, f.handler.SyncAgentSource, id, confirm, http.StatusOK)
		f.request(t, f.handler.SyncAgentSource, id, confirm, http.StatusOK)
		row, err := testHandler.Queries.GetAgent(t.Context(), parseUUID(id)); if err != nil || row.Instructions != "ZIP instructions" { t.Fatalf("ZIP publication: %v", err) }
		source, err := testHandler.Queries.GetAgentSourceByAgentID(t.Context(), row.ID); if err != nil { t.Fatal(err) }
		if id == manualID && source.SourceType != "local" { t.Fatal("manual Agent did not acquire local package provenance") }
		mappings, err := testHandler.Queries.ListAgentSourceSkills(t.Context(), source.ID); if err != nil || len(mappings) != 1 { t.Fatalf("source skills: %v", err) }
		files["skills/new/SKILL.md"] = "Updated skill"
		preview = packagePublishPreview(t, f.handler, id, files, http.StatusOK)
		f.request(t, f.handler.SyncAgentSource, id, map[string]any{"preview_id":rawString(t, preview["preview_id"])}, http.StatusOK)
		again, err := testHandler.Queries.ListAgentSourceSkills(t.Context(), source.ID); if err != nil || len(again) != 1 || again[0].SkillID != mappings[0].SkillID { t.Fatal("repeat ZIP update replaced skill identity") }
		assigned, err := testHandler.Queries.ListAgentSkillSummaries(t.Context(), row.ID); if err != nil || len(assigned) == 0 { t.Fatal("ZIP update lost skills") }
	}
}
