package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/agentsource"
	"github.com/multica-ai/multica/server/internal/coordinatorcontract"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestPortableContractSourcePreviewRejectsConcurrentChangeAndClearsOnSync(t *testing.T) {
	f := newGitSourceFixture(t)
	f.files[gitSourceSHA1] = map[string]string{
		"agent.json": `{"$schema":"agent.schema.json","version":"multica.agent/v2","name":"Before contract","instructions":"AGENTS.md","skills":[],"coordinator_contract":{"version":1,"scope":"Coordinate work","constraints":["Draft only"]}}`,
		"AGENTS.md":  " \nExecutor SOP.\n ",
	}
	f.files[gitSourceSHA2] = map[string]string{
		"agent.json": `{"$schema":"agent.schema.json","version":"multica.agent/v2","name":"After contract","description":"Published profile","instructions":"AGENTS.md","skills":[]}`,
		"AGENTS.md":  " \nExecutor SOP.\n ",
	}
	preview := f.request(t, f.handler.PreviewGitAgent, testWorkspaceID, map[string]any{"connection_id": f.installationID, "repository": "https://github.com/acme/reviewer", "ref": "main"}, http.StatusOK)
	if _, state := coordinatorcontract.Resolve(preview["coordinator_contract"], f.files[gitSourceSHA1]["AGENTS.md"]); state != coordinatorcontract.StateLoaded {
		t.Fatalf("Git preview lost bound contract: %s", state)
	}
	id := f.create(t)
	agent, err := testHandler.Queries.GetAgent(t.Context(), parseUUID(id))
	if err != nil {
		t.Fatal(err)
	}
	if _, state := coordinatorcontract.Resolve(agent.CoordinatorContract, agent.Instructions); state != coordinatorcontract.StateLoaded {
		t.Fatalf("created contract state=%s", state)
	}
	syncPreview := f.request(t, f.handler.PreviewAgentSourceSync, id, map[string]any{"ref": "release/v2"}, http.StatusOK)
	if !strings.Contains(string(syncPreview["configuration_changes"]), `coordinator_contract`) {
		t.Fatal("source deletion is absent from reviewable diff")
	}
	changed, err := coordinatorcontract.Bind(&coordinatorcontract.Contract{Version: 1, Scope: "A concurrent contract change"}, agent.Instructions)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testHandler.Queries.UpdateAgent(t.Context(), db.UpdateAgentParams{ID: agent.ID, CoordinatorContract: coordinatorcontract.Marshal(changed)}); err != nil {
		t.Fatal(err)
	}
	f.request(t, f.handler.SyncAgentSource, id, map[string]any{"preview_id": rawString(t, syncPreview["preview_id"])}, http.StatusConflict)
	syncPreview = f.request(t, f.handler.PreviewAgentSourceSync, id, map[string]any{"ref": "release/v2"}, http.StatusOK)
	f.request(t, f.handler.SyncAgentSource, id, map[string]any{"preview_id": rawString(t, syncPreview["preview_id"])}, http.StatusOK)
	agent, err = testHandler.Queries.GetAgent(t.Context(), agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(agent.CoordinatorContract) != 0 || agent.Name != "After contract" || agent.Description != "Published profile" {
		t.Fatal("sync lost contract deletion or release v2 profile publication")
	}
}

func TestPortableContractLocalPreviewCreateAndExportPreserveStaleHash(t *testing.T) {
	f := newGitSourceFixture(t)
	old, err := coordinatorcontract.Bind(&coordinatorcontract.Contract{Version: 1, Scope: "Coordinate only", Constraints: []string{"Draft only"}}, "Older SOP")
	if err != nil {
		t.Fatal(err)
	}
	definition := map[string]any{"$schema": agentsource.PortableSchemaPath, "version": "multica.agent/v2", "name": "Local stale source", "instructions": "AGENTS.md", "skills": []any{}, "coordinator_contract": old}
	encoded, _ := json.Marshal(definition)
	files := map[string]string{"agent.json": string(encoded), "AGENTS.md": "Current SOP"}
	response := httptest.NewRecorder()
	f.handler.PreviewAgentPackage(response, agentPackageRequest(t, zipPackageFiles(t, files), false))
	if response.Code != http.StatusOK {
		t.Fatalf("preview: %d %s", response.Code, response.Body.String())
	}
	var preview map[string]json.RawMessage
	if err := json.Unmarshal(response.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	if _, state := coordinatorcontract.Resolve(preview["coordinator_contract"], "Current SOP"); state != coordinatorcontract.StateStale {
		t.Fatalf("preview recertified stale contract: %s", state)
	}
	body := map[string]any{"preview_id": rawString(t, preview["preview_id"]), "runtime_id": testRuntimeID, "name": "Local contract " + f.installationID}
	created := f.request(t, f.handler.CreateAgentFromPackage, testWorkspaceID, body, http.StatusCreated)
	var agent AgentResponse
	if err := json.Unmarshal(created["agent"], &agent); err != nil {
		t.Fatal(err)
	}
	if agent.CoordinatorContractState != coordinatorcontract.StateStale || agent.CoordinatorContract.SourceInstructionsSHA256 != old.SourceInstructionsSHA256 {
		t.Fatal("import changed the copied version reference")
	}
	replayed := f.request(t, f.handler.CreateAgentFromPackage, testWorkspaceID, body, http.StatusOK)
	var again AgentResponse
	if err := json.Unmarshal(replayed["agent"], &again); err != nil {
		t.Fatal(err)
	}
	if again.ID != agent.ID || again.CoordinatorContractState != coordinatorcontract.StateStale {
		t.Fatal("idempotent import lost the original contract")
	}
	exported := exportAgentFiles(t, agent.ID)
	parsed, err := agentsource.ParseAgentPackage(t.Context(), zipPackageFiles(t, exported))
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := parsed.Bundle()
	if err != nil {
		t.Fatal(err)
	}
	if _, state := coordinatorcontract.Resolve(coordinatorcontract.Marshal(bundle.CoordinatorContract), bundle.Instructions); state != coordinatorcontract.StateStale {
		t.Fatal("export/import recertified stale contract")
	}
	if bundle.CoordinatorContract.SourceInstructionsSHA256 != old.SourceInstructionsSHA256 {
		t.Fatal("export lost the original source hash")
	}
}
