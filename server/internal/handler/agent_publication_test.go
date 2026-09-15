package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/agentsource"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestGitPublicationKeepsRestorableConfiguration(t *testing.T) {
	f := newGitSourceFixture(t)
	id := f.create(t)
	publication, err := f.handler.Queries.LatestAppliedAgentSourcePreview(t.Context(), db.LatestAppliedAgentSourcePreviewParams{AgentID:parseUUID(id), WorkspaceID:parseUUID(testWorkspaceID)})
	if err != nil { t.Fatal(err) }
	var snapshot map[string]json.RawMessage
	if err := json.Unmarshal(publication.Snapshot, &snapshot); err != nil { t.Fatal(err) }
	if len(snapshot["published_definition"]) == 0 { t.Fatal("publication does not preserve materialized configuration for rollback") }
}

func TestGitPublicationRollbackUsesHistoryInsteadOfMovedBranch(t *testing.T) {
	f := newGitSourceFixture(t)
	f.files[gitSourceSHA2]["agent.json"] = `{"$schema":"agent.schema.json","version":"multica.agent/v2","name":"New configuration","instructions":"agent/AGENTS.md","skills":[],"configuration":{"persona":"Version two persona","custom_args":["--new-option"]}}`
	id := f.create(t)
	publication, err := f.handler.Queries.LatestAppliedAgentSourcePreview(t.Context(), db.LatestAppliedAgentSourcePreviewParams{AgentID:parseUUID(id), WorkspaceID:parseUUID(testWorkspaceID)})
	if err != nil { t.Fatal(err) }
	preview := f.request(t, f.handler.PreviewAgentSourceSync, id, map[string]any{"ref":"release/v2"}, http.StatusOK)
	f.request(t, f.handler.SyncAgentSource, id, map[string]any{"preview_id":rawString(t,preview["preview_id"])}, http.StatusOK)
	f.refs["main"] = gitSourceSHA2
	rollback := f.request(t, f.handler.PreviewAgentSourceSync, id, map[string]any{"publication_id":uuidToString(publication.ID)}, http.StatusOK)
	if rawString(t,rollback["resolved_sha"]) != gitSourceSHA1 { t.Fatal("rollback resolved the current branch instead of the recorded release") }
	stored, err := f.handler.Queries.GetAgentSourcePreview(t.Context(), db.GetAgentSourcePreviewParams{ID:parseUUID(rawString(t,rollback["preview_id"])), WorkspaceID:publication.WorkspaceID, CreatedBy:publication.CreatedBy})
	if err != nil { t.Fatal(err) }
	// During rolling deployment an older server decodes only RepositorySnapshot.
	var legacy agentsource.RepositorySnapshot
	if err := json.Unmarshal(stored.Snapshot,&legacy); err != nil { t.Fatal(err) }
	var legacyConfiguration map[string]any
	if err := json.Unmarshal(legacy.Definition.Definition["configuration"],&legacyConfiguration); err != nil { t.Fatal(err) }
	if value,present := legacyConfiguration["persona"]; !present || value != "" { t.Fatal("older confirmation server would lose the complete rollback configuration") }
	var gitChanges []agentsource.FileChange
	if err := json.Unmarshal(rollback["git_changes"],&gitChanges); err != nil { t.Fatal(err) }
	for _, change := range gitChanges {
		if change.Path == "agent.json" && change.After != nil && strings.Contains(*change.After, `"persona"`) { t.Fatal("Git diff includes materialized platform configuration absent from the repository") }
	}
	delete(f.files,gitSourceSHA1)
	delete(f.refs,"main")
	confirmExportedPackagePreview(t,f,id,rollback,http.StatusOK)
	confirmExportedPackagePreview(t,f,id,rollback,http.StatusOK)
	agent, err := f.handler.Queries.GetAgent(t.Context(),parseUUID(id))
	if err != nil || agent.Instructions != "Review code v1" || string(agent.CustomArgs) != "[]" { t.Fatalf("incomplete rollback: instructions=%q args=%s err=%v",agent.Instructions,agent.CustomArgs,err) }
	var persona string
	if err := testPool.QueryRow(t.Context(),`SELECT persona FROM agent WHERE id=$1`,id).Scan(&persona); err != nil || persona != "" { t.Fatalf("rollback retained new persona: %q %v",persona,err) }
	skills, err := f.handler.Queries.ListAgentSkills(t.Context(),agent.ID)
	if err != nil || len(skills) != 1 { t.Fatalf("rollback did not restore deleted skill: %v",err) }
	history := f.request(t,f.handler.ListAgentPublications,id,nil,http.StatusOK)
	var entries []map[string]json.RawMessage
	if err := json.Unmarshal(history["publications"],&entries); err != nil { t.Fatal(err) }
	if len(entries) != 3 || rawString(t,entries[0]["rollback_of"]) != uuidToString(publication.ID) { t.Fatalf("missing or duplicated rollback history: %s",history["publications"]) }
	if strings.Contains(string(history["publications"]),"published_definition") { t.Fatal("history leaked the configuration snapshot") }
}

func TestGitPublicationRollbackPreservesExportedManifest(t *testing.T) {
	f := newGitSourceFixture(t)
	id := f.create(t)
	original := exportAgentFiles(t,id)["agent.json"]
	publication, err := f.handler.Queries.LatestAppliedAgentSourcePreview(t.Context(),db.LatestAppliedAgentSourcePreviewParams{AgentID:parseUUID(id),WorkspaceID:parseUUID(testWorkspaceID)})
	if err != nil { t.Fatal(err) }
	preview := f.request(t,f.handler.PreviewAgentSourceSync,id,map[string]any{"ref":"release/v2"},http.StatusOK)
	f.request(t,f.handler.SyncAgentSource,id,map[string]any{"preview_id":rawString(t,preview["preview_id"])},http.StatusOK)
	rollback := f.request(t,f.handler.PreviewAgentSourceSync,id,map[string]any{"publication_id":uuidToString(publication.ID)},http.StatusOK)
	confirmExportedPackagePreview(t,f,id,rollback,http.StatusOK)
	if current := exportAgentFiles(t,id)["agent.json"]; packageValueHash(json.RawMessage(current)) != packageValueHash(json.RawMessage(original)) { t.Fatalf("rollback changed exported configuration:\nbefore: %s\nafter: %s",original,current) }
}

func TestGitAgentRejectsZIPPublication(t *testing.T) {
	f := newGitSourceFixture(t)
	packagePublishPreview(t, f.handler, f.create(t), f.files[gitSourceSHA2], http.StatusConflict)
}

func TestGitPublicationSupportsBranchesTagsAndCommits(t *testing.T) {
	f := newGitSourceFixture(t)
	f.refs["heads/release/v2"] = gitSourceSHA2
	f.refs["tags/release/v2"] = gitSourceSHA1
	id := f.create(t)
	for ref,want := range map[string]string{"refs/heads/release/v2":gitSourceSHA2,"refs/tags/release/v2":gitSourceSHA1,gitSourceSHA2:gitSourceSHA2} {
		preview := f.request(t,f.handler.PreviewAgentSourceSync,id,map[string]any{"ref":ref},http.StatusOK)
		if rawString(t,preview["resolved_sha"]) != want { t.Fatalf("wrong commit for %s",ref) }
		f.request(t,f.handler.SyncAgentSource,id,map[string]any{"preview_id":rawString(t,preview["preview_id"])},http.StatusOK)
		source,err := f.handler.Queries.GetAgentSourceByAgentID(t.Context(),parseUUID(id))
		if err != nil || source.Ref != ref || source.SyncedCommitSha != want || source.RepoOwner != "acme" || source.RepoName != "reviewer" { t.Fatalf("lost Git origin/ref: %v",err) }
	}
	listing := f.request(t,f.handler.ListAgentSourceBranches,id,nil,http.StatusOK)
	if !strings.Contains(string(listing["tags"]),"v1.0") { t.Fatal("tag selection list is missing") }
}

func TestGitPublicationHistoryPaginationDoesNotExposeOtherAgents(t *testing.T) {
	f := newGitSourceFixture(t)
	id := f.create(t)
	if _,err := testPool.Exec(t.Context(),`INSERT INTO agent_source_preview (workspace_id,created_by,agent_id,agent_source_id,git_connection_id,repository,ref,resolved_sha,snapshot,applied_at,applied_source)
SELECT workspace_id,created_by,agent_id,agent_source_id,git_connection_id,repository,ref,resolved_sha,snapshot,clock_timestamp() + n * interval '1 microsecond',applied_source
FROM agent_source_preview CROSS JOIN generate_series(1,51) AS n WHERE agent_id=$1 AND applied_at IS NOT NULL`,id); err != nil { t.Fatal(err) }
	first := f.request(t,f.handler.ListAgentPublications,id,nil,http.StatusOK)
	var rows []map[string]json.RawMessage
	if err := json.Unmarshal(first["publications"],&rows); err != nil || len(rows) != 50 { t.Fatalf("first page: %d %v",len(rows),err) }
	cursor := rawString(t,first["next_cursor"])
	w := httptest.NewRecorder()
	f.handler.ListAgentPublications(w,withURLParam(newRequest(http.MethodGet,"/?before="+cursor,nil),"id",id))
	if w.Code != http.StatusOK { t.Fatalf("history page: %d %s",w.Code,w.Body.String()) }
	var second map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(),&second); err != nil { t.Fatal(err) }
	if err := json.Unmarshal(second["publications"],&rows); err != nil || len(rows) != 2 || string(second["next_cursor"]) != "null" { t.Fatalf("second page: %s",w.Body.String()) }
	other := f.create(t)
	w = httptest.NewRecorder()
	f.handler.ListAgentPublications(w,withURLParam(newRequest(http.MethodGet,"/?before="+cursor,nil),"id",other))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(),`"publications":[]`) { t.Fatalf("foreign cursor exposed history: %s",w.Body.String()) }
}

func TestGitPublicationRollbackChecksAgentAndCurrentState(t *testing.T) {
	f := newGitSourceFixture(t)
	id := f.create(t)
	publication, err := f.handler.Queries.LatestAppliedAgentSourcePreview(t.Context(),db.LatestAppliedAgentSourcePreviewParams{AgentID:parseUUID(id),WorkspaceID:parseUUID(testWorkspaceID)})
	if err != nil { t.Fatal(err) }
	body := map[string]any{"publication_id":uuidToString(publication.ID)}
	f.request(t,f.handler.PreviewAgentSourceSync,f.create(t),body,http.StatusNotFound)
	rollback := f.request(t,f.handler.PreviewAgentSourceSync,id,body,http.StatusOK)
	if _,err := testPool.Exec(t.Context(),`UPDATE agent SET instructions='Concurrent edit' WHERE id=$1`,id); err != nil { t.Fatal(err) }
	confirmExportedPackagePreview(t,f,id,rollback,http.StatusConflict)
	rollback = f.request(t,f.handler.PreviewAgentSourceSync,id,body,http.StatusOK)
	f.accessible = false
	confirmExportedPackagePreview(t,f,id,rollback,http.StatusForbidden)
	member := createPlainMember(t,"publication-member-"+id+"@example.test")
	w := httptest.NewRecorder()
	f.handler.ListAgentPublications(w,withURLParam(newRequestAs(member,http.MethodGet,"/",nil),"id",id))
	if w.Code != http.StatusForbidden && w.Code != http.StatusNotFound { t.Fatalf("member read publication history: %d",w.Code) }
}
