package handler

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/agentsource"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestAgentPackageExportUpdatePreservesWorkspaceSkillIdentity(t *testing.T) {
	for _, sourceManaged := range []bool{false, true} {
		t.Run(map[bool]string{false:"manual Agent", true:"source Agent"}[sourceManaged], func(t *testing.T) {
			f := newGitSourceFixture(t)
			id := createHandlerTestAgent(t, "Skill round trip", nil)
			if sourceManaged { id = f.create(t) }
			if _, err := testPool.Exec(t.Context(), `UPDATE agent SET runtime_id=$2 WHERE id=$1`, id, testRuntimeID); err != nil { t.Fatal(err) }
			skill, err := testHandler.Queries.CreateSkill(t.Context(), db.CreateSkillParams{WorkspaceID:parseUUID(testWorkspaceID), CreatedBy:parseUUID(testUserID), Name:"shared-"+id, Description:"Shared review", Content:"Review the changes", Config:[]byte(`{"origin":{"type":"manual"}}`)})
			if err != nil { t.Fatal(err) }
			file, err := testHandler.Queries.UpsertSkillFile(t.Context(), db.UpsertSkillFileParams{SkillID:skill.ID, Path:"references/check.md", Content:"Check the result"}); if err != nil { t.Fatal(err) }
			if err := testHandler.Queries.AddAgentSkill(t.Context(), db.AddAgentSkillParams{AgentID:parseUUID(id), SkillID:skill.ID}); err != nil { t.Fatal(err) }
			if _, err := testHandler.Queries.SetAgentSkillEnabled(t.Context(), db.SetAgentSkillEnabledParams{AgentID:parseUUID(id), SkillID:skill.ID, Enabled:false}); err != nil { t.Fatal(err) }
			before, err := testHandler.Queries.ListAgentSkillSummaries(t.Context(), parseUUID(id)); if err != nil { t.Fatal(err) }
			files := exportAgentFiles(t, id)
			var exported agentsource.PortableManifest
			if err := json.Unmarshal([]byte(files[agentsource.PortableManifestPath]),&exported); err != nil { t.Fatal(err) }
			identified := false
			for _, entry := range exported.Skills { if entry.SkillID == uuidToString(skill.ID) && entry.Scope != nil && entry.Scope.Type == "workspace" && entry.Scope.ID == testWorkspaceID { identified = true } }
			if !identified { t.Fatal("export omitted the workspace scope and skill_id") }
			preview := packagePublishPreview(t, f.handler, id, files, http.StatusOK)
			var changes []agentsource.FileChange
			if err := json.Unmarshal(preview["configuration_changes"], &changes); err != nil { t.Fatal(err) }
			for _, change := range changes { if strings.HasPrefix(change.Path, "skills/") { t.Errorf("unchanged exported skill produced a diff: %s", change.Path) } }
			confirmExportedPackagePreview(t, f, id, preview, http.StatusOK)
			after, err := testHandler.Queries.ListAgentSkillSummaries(t.Context(), parseUUID(id)); if err != nil { t.Fatal(err) }
			if len(after) != len(before) { t.Fatalf("export/update duplicated skills: before=%d after=%d", len(before), len(after)) }
			for _, want := range before {
				found := false
				for _, got := range after { if got.ID == want.ID && got.Enabled == want.Enabled { found = true } }
				if !found { t.Fatal("export/update changed a skill identity or enabled state") }
			}
			unchanged, err := testHandler.Queries.GetSkill(t.Context(), skill.ID); if err != nil { t.Fatal(err) }
			if unchanged.UpdatedAt != skill.UpdatedAt || string(unchanged.Config) != string(skill.Config) { t.Fatal("unchanged shared skill was rewritten or made exclusive") }
			unchangedFiles, err := testHandler.Queries.ListSkillFiles(t.Context(), skill.ID); if err != nil || len(unchangedFiles) != 1 || unchangedFiles[0].ID != file.ID { t.Fatal("unchanged supporting file was recreated") }
			// Older platform exports encoded the ID only in workspace-skills/<id>.
			legacyFiles := exportAgentFiles(t,id)
			var legacyManifest map[string]any
			if err := json.Unmarshal([]byte(legacyFiles[agentsource.PortableManifestPath]),&legacyManifest); err != nil { t.Fatal(err) }
			for _, raw := range legacyManifest["skills"].([]any) { entry := raw.(map[string]any); delete(entry,"scope"); delete(entry,"skill_id") }
			legacyJSON, err := json.Marshal(legacyManifest); if err != nil { t.Fatal(err) }; legacyFiles[agentsource.PortableManifestPath] = string(legacyJSON)
			preview = packagePublishPreview(t,f.handler,id,legacyFiles,http.StatusOK)
			confirmExportedPackagePreview(t,f,id,preview,http.StatusOK)
			after, err = testHandler.Queries.ListAgentSkillSummaries(t.Context(),parseUUID(id)); if err != nil || len(after) != len(before) { t.Fatal("previously exported ZIP duplicated existing skills") }

			path := "workspace-skills/" + uuidToString(skill.ID)
			files[path+"/SKILL.md"] = "Review the updated changes"
			files[path+"/references/check.md"] = "Check the updated result"
			preview = packagePublishPreview(t, f.handler, id, files, http.StatusOK)
			if !strings.Contains(string(preview["configuration_changes"]), "Review the changes") || !strings.Contains(string(preview["configuration_changes"]), "Review the updated changes") { t.Fatal("skill diff did not compare the existing and imported content") }
			confirmExportedPackagePreview(t, f, id, preview, http.StatusOK)
			updated, err := testHandler.Queries.GetSkill(t.Context(), skill.ID); if err != nil || updated.Content != "Review the updated changes" { t.Fatal("publication did not update the original skill") }
			after, err = testHandler.Queries.ListAgentSkillSummaries(t.Context(), parseUUID(id)); if err != nil || len(after) != len(before) { t.Fatal("edited publication duplicated skills") }
			if string(updated.Config) != string(skill.Config) { t.Fatal("publication changed the shared skill ownership or origin") }
			preview = packagePublishPreview(t, f.handler, id, exportAgentFiles(t,id), http.StatusOK)
			confirmExportedPackagePreview(t, f, id, preview, http.StatusOK)
		})
	}
}

func TestAgentPackageSkillIdentitySurvivesDirectoryRename(t *testing.T) {
	f := newGitSourceFixture(t)
	id := f.create(t)
	before, err := testHandler.Queries.ListAgentSkills(t.Context(),parseUUID(id)); if err != nil || len(before) != 1 { t.Fatal("missing source skill") }
	files := exportAgentFiles(t,id)
	var manifest map[string]any
	if err := json.Unmarshal([]byte(files[agentsource.PortableManifestPath]),&manifest); err != nil { t.Fatal(err) }
	entry := manifest["skills"].([]any)[0].(map[string]any)
	old := entry["path"].(string)
	entry["path"] = "renamed/review"
	for path, content := range files {
		if strings.HasPrefix(path,old+"/") { files["renamed/review"+strings.TrimPrefix(path,old)] = content; delete(files,path) }
	}
	encoded, err := json.Marshal(manifest); if err != nil { t.Fatal(err) }; files[agentsource.PortableManifestPath] = string(encoded)
	preview := packagePublishPreview(t,f.handler,id,files,http.StatusOK)
	confirmExportedPackagePreview(t,f,id,preview,http.StatusOK)
	after, err := testHandler.Queries.ListAgentSkills(t.Context(),parseUUID(id)); if err != nil || len(after) != 1 || after[0].ID != before[0].ID { t.Fatal("directory rename replaced the skill identity") }
	if _,ok := exportAgentFiles(t,id)["renamed/review/SKILL.md"]; !ok { t.Fatal("export lost the new directory mapping") }
}

func TestAgentPackageReferencedSkillPublicationChecksCurrentStateAndPermission(t *testing.T) {
	f := newGitSourceFixture(t)
	id := createHandlerTestAgent(t,"Referenced skill checks",nil)
	if _, err := testPool.Exec(t.Context(),`UPDATE agent SET runtime_id=$2 WHERE id=$1`,id,testRuntimeID); err != nil { t.Fatal(err) }
	skill, err := testHandler.Queries.CreateSkill(t.Context(),db.CreateSkillParams{WorkspaceID:parseUUID(testWorkspaceID),CreatedBy:parseUUID(testUserID),Name:"guarded-"+id,Content:"Existing content",Config:[]byte(`{}`)}); if err != nil { t.Fatal(err) }
	if err := testHandler.Queries.AddAgentSkill(t.Context(),db.AddAgentSkillParams{AgentID:parseUUID(id),SkillID:skill.ID}); err != nil { t.Fatal(err) }
	files := exportAgentFiles(t,id)
	files["workspace-skills/"+uuidToString(skill.ID)+"/SKILL.md"] = "Imported change"
	preview := packagePublishPreview(t,f.handler,id,files,http.StatusOK)
	if _, err := testPool.Exec(t.Context(),`UPDATE skill SET content='Concurrent change' WHERE id=$1`,skill.ID); err != nil { t.Fatal(err) }
	confirmExportedPackagePreview(t,f,id,preview,http.StatusConflict)
	preview = packagePublishPreview(t,f.handler,id,files,http.StatusOK)
	if err := testHandler.Queries.RemoveAgentSkill(t.Context(),db.RemoveAgentSkillParams{AgentID:parseUUID(id),SkillID:skill.ID}); err != nil { t.Fatal(err) }
	confirmExportedPackagePreview(t,f,id,preview,http.StatusConflict)
	packagePublishPreview(t,f.handler,id,files,http.StatusConflict)
	if err := testHandler.Queries.AddAgentSkill(t.Context(),db.AddAgentSkillParams{AgentID:parseUUID(id),SkillID:skill.ID}); err != nil { t.Fatal(err) }
	memberID := createPlainMember(t,"skill-package-"+id+"@example.test")
	if _, err := testPool.Exec(t.Context(),`UPDATE agent SET owner_id=$2 WHERE id=$1`,id,memberID); err != nil { t.Fatal(err) }
	r := withURLParam(newRequestAs(memberID,http.MethodPost,"/",nil),"id",id)
	r.Body = io.NopCloser(bytes.NewReader(zipPackageFiles(t,files)))
	r.Header.Set("Content-Type","application/zip")
	w := httptest.NewRecorder(); f.handler.PreviewAgentSourceSync(w,r)
	if w.Code != http.StatusOK { t.Fatalf("member preview: %d %s",w.Code,w.Body.String()) }
	if err := json.Unmarshal(w.Body.Bytes(),&preview); err != nil { t.Fatal(err) }
	var requirements PackageRequirements
	if err := json.Unmarshal(preview["requirements"],&requirements); err != nil { t.Fatal(err) }
	w = httptest.NewRecorder()
	f.handler.SyncAgentSource(w,withURLParam(newRequestAs(memberID,http.MethodPost,"/",map[string]any{"preview_id":rawString(t,preview["preview_id"]),"deferred_bindings":requirements.DeferredBindings}),"id",id))
	if w.Code != http.StatusForbidden { t.Fatalf("member replaced another creator's shared skill: %d %s",w.Code,w.Body.String()) }
	current, err := testHandler.Queries.GetSkill(t.Context(),skill.ID); if err != nil || current.Content != "Concurrent change" { t.Fatal("rejected publication modified the original skill") }
}

func confirmExportedPackagePreview(t *testing.T, f *gitSourceFixture, id string, preview map[string]json.RawMessage, status int) {
	t.Helper()
	var requirements PackageRequirements
	if err := json.Unmarshal(preview["requirements"], &requirements); err != nil { t.Fatal(err) }
	f.request(t, f.handler.SyncAgentSource, id, map[string]any{"preview_id":rawString(t,preview["preview_id"]), "deferred_bindings":requirements.DeferredBindings}, status)
}
