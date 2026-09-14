package handler

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/githubapp"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const gitSourceSHA1 = "1111111111111111111111111111111111111111"
const gitSourceSHA2 = "2222222222222222222222222222222222222222"

type gitSourceFixture struct {
	handler *Handler
	installationID string
	refs map[string]string
	files map[string]map[string]string
	accessible bool
}

func newGitSourceFixture(t *testing.T) *gitSourceFixture {
	t.Helper()
	f := &gitSourceFixture{
		refs: map[string]string{"main": gitSourceSHA1, "release/v2": gitSourceSHA2},
		files: map[string]map[string]string{}, accessible: true,
	}
	for index, sha := range []string{gitSourceSHA1, gitSourceSHA2} {
		f.files[sha] = map[string]string{
			"agent.json": `{"$schema":"agent.schema.json","version":"multica.agent/v1","name":"source-reviewer","instructions":"agent/AGENTS.md","skills":[{"path":"agent/skills/dta-basic-behavior","name":"dta-basic-behavior","description":"Base behavior","enabled":true}]}`,
			"agent/AGENTS.md": fmt.Sprintf("Review code v%d", index + 1),
			"agent/skills/dta-basic-behavior/SKILL.md": "---\nname: dta-basic-behavior\ndescription: Base behavior\n---\nBe helpful.",
			"agent/skills/dta-basic-behavior/references/check.md": fmt.Sprintf("Checklist v%d", index + 1),
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		switch {
		case strings.HasSuffix(p, "/access_tokens"):
			writeJSON(w, http.StatusCreated, map[string]any{"token":"fixture-token", "expires_at":time.Now().Add(time.Hour)})
		case p == "/installation/repositories":
			repositories := []githubapp.Repository{}
			if f.accessible {
				repositories = append(repositories, githubapp.Repository{ID: 42, FullName: "acme/reviewer", DefaultBranch: "main", HTMLURL: "https://github.com/acme/reviewer"})
			}
			writeJSON(w, http.StatusOK, map[string]any{"repositories":repositories})
		case p == "/repos/acme/reviewer/branches":
			branches := []map[string]any{}
			for ref, sha := range f.refs {
				branches = append(branches, map[string]any{"name":ref, "commit":map[string]string{"sha":sha}})
			}
			writeJSON(w, http.StatusOK, branches)
		case p == "/repos/acme/reviewer/tags":
			writeJSON(w,http.StatusOK,[]map[string]any{{"name":"v1.0","commit":map[string]string{"sha":gitSourceSHA1}}})
		case strings.HasPrefix(p, "/repos/acme/reviewer/commits/"):
			ref := strings.TrimPrefix(p, "/repos/acme/reviewer/commits/")
			sha := f.refs[ref]
			if f.files[ref] != nil { sha = ref }
			if sha == "" { http.NotFound(w, r); return }
			writeJSON(w, http.StatusOK, map[string]string{"sha":sha})
		case strings.HasPrefix(p, "/repos/acme/reviewer/git/commits/"):
			sha := strings.TrimPrefix(p, "/repos/acme/reviewer/git/commits/")
			writeJSON(w, http.StatusOK, map[string]any{"tree":map[string]string{"sha":sha}})
		case strings.HasPrefix(p, "/repos/acme/reviewer/git/trees/"):
			sha := strings.TrimPrefix(p, "/repos/acme/reviewer/git/trees/")
			entries := []githubapp.TreeEntry{}
			for path, content := range f.files[sha] {
				digest := sha1.Sum([]byte(content))
				entries = append(entries, githubapp.TreeEntry{Path:path, Type:"blob", Mode:"100644", SHA:hex.EncodeToString(digest[:]), Size:int64(len(content))})
			}
			writeJSON(w, http.StatusOK, githubapp.Tree{SHA:sha, Entries:entries})
		case strings.HasPrefix(p, "/repos/acme/reviewer/git/blobs/"):
			sha := strings.TrimPrefix(p, "/repos/acme/reviewer/git/blobs/")
			for _, files := range f.files {
				for _, content := range files {
					digest := sha1.Sum([]byte(content))
					if hex.EncodeToString(digest[:]) == sha {
						writeJSON(w, http.StatusOK, map[string]any{"encoding":"base64", "content":base64.StdEncoding.EncodeToString([]byte(content)), "size":len(content)})
						return
					}
				}
			}
			http.NotFound(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil { t.Fatal(err) }
	client, err := githubapp.New(githubapp.Config{AppID:"123", PrivateKey:string(pem.EncodeToMemory(&pem.Block{Type:"RSA PRIVATE KEY", Bytes:x509.MarshalPKCS1PrivateKey(key)})), APIBase:server.URL})
	if err != nil { t.Fatal(err) }
	copyHandler := *testHandler
	copyHandler.GitHubApp = client
	copyHandler.TaskService = nil
	f.handler = &copyHandler
	installation, err := testHandler.Queries.CreateGitHubInstallation(t.Context(), db.CreateGitHubInstallationParams{
		WorkspaceID:parseUUID(testWorkspaceID), InstallationID:time.Now().UnixNano(), AccountLogin:"acme", AccountType:"Organization", ConnectedByID:parseUUID(testUserID),
	})
	if err != nil { t.Fatal(err) }
	f.installationID = uuidToString(installation.ID)
	return f
}

func (f *gitSourceFixture) request(t *testing.T, handler http.HandlerFunc, id string, body any, wantStatus int) map[string]json.RawMessage {
	t.Helper()
	w := httptest.NewRecorder()
	handler(w, withURLParam(newRequest(http.MethodPost, "/", body), "id", id))
	if w.Code != wantStatus { t.Fatalf("status = %d, want %d: %s", w.Code, wantStatus, w.Body.String()) }
	result := map[string]json.RawMessage{}
	if w.Body.Len() > 0 {
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil { t.Fatal(err) }
	}
	return result
}

func rawString(t *testing.T, value json.RawMessage) string {
	t.Helper()
	var result string
	if err := json.Unmarshal(value, &result); err != nil { t.Fatal(err) }
	return result
}

func TestGitHubSourceCreateFromURLPinsPreviewAndIsIdempotent(t *testing.T) {
	f := newGitSourceFixture(t)
	preview := f.request(t, f.handler.PreviewGitHubAgent, testWorkspaceID, map[string]any{
		"installation_id":f.installationID, "repository":"https://github.com/acme/reviewer", "ref":"main",
	}, http.StatusOK)
	previewID := rawString(t, preview["preview_id"])
	f.refs["main"] = gitSourceSHA2
	body := map[string]any{"preview_id":previewID, "runtime_id":testRuntimeID, "name":"Pinned source " + previewID}
	created := f.request(t, f.handler.CreateGitHubAgent, testWorkspaceID, body, http.StatusCreated)
	var agent struct { ID string `json:"id"`; Instructions string `json:"instructions"` }
	if err := json.Unmarshal(created["agent"], &agent); err != nil { t.Fatal(err) }
	if agent.Instructions != "Review code v1" { t.Fatalf("instructions = %q", agent.Instructions) }
	replayed := f.request(t, f.handler.CreateGitHubAgent, testWorkspaceID, body, http.StatusOK)
	var replayAgent struct { ID string `json:"id"` }
	if err := json.Unmarshal(replayed["agent"], &replayAgent); err != nil { t.Fatal(err) }
	if replayAgent.ID != agent.ID { t.Fatal("confirmation created a second Agent") }
}

func TestGitHubSourceSkillCanBeEditedAndDeletedButNotReassigned(t *testing.T) {
	f := newGitSourceFixture(t)
	created := f.request(t, f.handler.CreateGitHubAgent, testWorkspaceID, map[string]any{
		"installation_id":f.installationID, "repository":"acme/reviewer", "ref":"main", "resolved_sha":gitSourceSHA1,
		"runtime_id":testRuntimeID, "name":"Editable source " + f.installationID,
	}, http.StatusCreated)
	var agent struct { ID string `json:"id"` }
	if err := json.Unmarshal(created["agent"], &agent); err != nil { t.Fatal(err) }
	skills, err := testHandler.Queries.ListAgentSkills(t.Context(), parseUUID(agent.ID))
	if err != nil || len(skills) != 1 { t.Fatalf("skills = %#v, %v", skills, err) }
	skillID := uuidToString(skills[0].ID)
	f.request(t, f.handler.UpdateSkill, skillID, map[string]any{"content":"Local change", "config":map[string]any{"custom":"retained"}}, http.StatusOK)
	updated, err := testHandler.Queries.GetSkill(t.Context(), skills[0].ID)
	if err != nil || !strings.Contains(string(updated.Config), "github_agent_source") { t.Fatal("editing removed the source ownership metadata") }
	otherAgentID := createHandlerTestAgent(t, "Other " + f.installationID, nil)
	f.request(t, f.handler.AddAgentSkills, otherAgentID, map[string]any{"skill_ids":[]string{skillID}}, http.StatusBadRequest)
	f.request(t, f.handler.DeleteSkill, skillID, nil, http.StatusNoContent)
	if _, err := testHandler.Queries.GetSkill(t.Context(), pgtype.UUID{Bytes:skills[0].ID.Bytes, Valid:true}); err == nil { t.Fatal("skill was not deleted") }
}

func (f *gitSourceFixture) create(t *testing.T) string {
	t.Helper()
	created := f.request(t, f.handler.CreateGitHubAgent, testWorkspaceID, map[string]any{
		"installation_id":f.installationID, "repository":"acme/reviewer", "ref":"main", "resolved_sha":gitSourceSHA1,
		"runtime_id":testRuntimeID, "name":fmt.Sprintf("Source %s %d", f.installationID, time.Now().UnixNano()),
	}, http.StatusCreated)
	var agent struct { ID string `json:"id"` }
	if err := json.Unmarshal(created["agent"], &agent); err != nil { t.Fatal(err) }
	return agent.ID
}

func TestGitHubSourceSyncRequiresPreviewAndUsesConfirmedBranchCommit(t *testing.T) {
	f := newGitSourceFixture(t)
	agentID := f.create(t)
	f.request(t, f.handler.SyncAgentSource, agentID, nil, http.StatusPreconditionRequired)
	preview := f.request(t, f.handler.PreviewAgentSourceSync, agentID, map[string]any{"ref":"release/v2"}, http.StatusOK)
	if rawString(t, preview["resolved_sha"]) != gitSourceSHA2 { t.Fatal("preview resolved the wrong branch") }
	var diff []map[string]any
	if err := json.Unmarshal(preview["git_changes"], &diff); err != nil || len(diff) != 2 { t.Fatalf("Git changes = %s, %v", preview["git_changes"], err) }
	f.refs["release/v2"] = gitSourceSHA1
	confirmation := map[string]any{"preview_id":rawString(t, preview["preview_id"])}
	f.request(t, f.handler.SyncAgentSource, agentID, confirmation, http.StatusOK)
	agent, err := testHandler.Queries.GetAgent(t.Context(), parseUUID(agentID))
	if err != nil || agent.Instructions != "Review code v2" { t.Fatalf("published instructions = %q, %v", agent.Instructions, err) }
	source, err := testHandler.Queries.GetAgentSourceByAgentID(t.Context(), agent.ID)
	if err != nil || source.Ref != "release/v2" || source.SyncedCommitSha != gitSourceSHA2 { t.Fatalf("source = %#v, %v", source, err) }
	f.request(t, f.handler.SyncAgentSource, agentID, confirmation, http.StatusOK)
}

func TestGitHubSourceSyncRejectsChangesMadeAfterPreview(t *testing.T) {
	f := newGitSourceFixture(t)
	agentID := f.create(t)
	preview := f.request(t, f.handler.PreviewAgentSourceSync, agentID, map[string]any{"ref":"release/v2"}, http.StatusOK)
	skills, err := testHandler.Queries.ListAgentSkills(t.Context(), parseUUID(agentID))
	if err != nil || len(skills) != 1 { t.Fatalf("skills = %#v, %v", skills, err) }
	f.request(t, f.handler.UpdateSkill, uuidToString(skills[0].ID), map[string]any{"content":"new local edit", "config":map[string]any{"custom":"retained"}}, http.StatusOK)
	f.request(t, f.handler.SyncAgentSource, agentID, map[string]any{"preview_id":rawString(t, preview["preview_id"])}, http.StatusConflict)
	agent, err := testHandler.Queries.GetAgent(t.Context(), parseUUID(agentID))
	if err != nil || agent.Instructions != "Review code v1" { t.Fatal("stale preview changed the Agent") }
	fresh := f.request(t, f.handler.PreviewAgentSourceSync, agentID, map[string]any{"ref":"release/v2"}, http.StatusOK)
	if !strings.Contains(string(fresh["configuration_changes"]), "new local edit") { t.Fatal("preview omitted the local edit") }
	f.request(t, f.handler.SyncAgentSource, agentID, map[string]any{"preview_id":rawString(t, fresh["preview_id"])}, http.StatusOK)
	skill, err := testHandler.Queries.GetSkill(t.Context(), skills[0].ID)
	if err != nil || !strings.Contains(string(skill.Config), "retained") { t.Fatal("sync discarded local skill configuration") }
}

func TestGitHubSourceSyncRestoresDeletedSkillAtSameCommitAfterConfirmation(t *testing.T) {
	f := newGitSourceFixture(t)
	agentID := f.create(t)
	skills, err := testHandler.Queries.ListAgentSkills(t.Context(), parseUUID(agentID))
	if err != nil || len(skills) != 1 { t.Fatalf("skills = %#v, %v", skills, err) }
	f.request(t, f.handler.DeleteSkill, uuidToString(skills[0].ID), nil, http.StatusNoContent)
	preview := f.request(t, f.handler.PreviewAgentSourceSync, agentID, map[string]any{}, http.StatusOK)
	if string(preview["changed"]) != "true" || !strings.Contains(string(preview["configuration_changes"]), "added") { t.Fatal("preview omitted deleted skill restoration") }
	f.request(t, f.handler.SyncAgentSource, agentID, map[string]any{"preview_id":rawString(t, preview["preview_id"])}, http.StatusOK)
	skills, err = testHandler.Queries.ListAgentSkills(t.Context(), parseUUID(agentID))
	if err != nil || len(skills) != 1 { t.Fatalf("restored skills = %#v, %v", skills, err) }
}

func TestGitHubSourceSyncRechecksRepositoryPermission(t *testing.T) {
	f := newGitSourceFixture(t)
	agentID := f.create(t)
	preview := f.request(t, f.handler.PreviewAgentSourceSync, agentID, map[string]any{"ref":"release/v2"}, http.StatusOK)
	f.accessible = false
	f.request(t, f.handler.SyncAgentSource, agentID, map[string]any{"preview_id":rawString(t, preview["preview_id"])}, http.StatusForbidden)
}

func TestGitHubSourceConcurrentConfirmationsCreateOneAgent(t *testing.T) {
	f := newGitSourceFixture(t)
	preview := f.request(t, f.handler.PreviewGitHubAgent, testWorkspaceID, map[string]any{
		"installation_id":f.installationID, "repository":"https://github.com/acme/reviewer",
	}, http.StatusOK)
	previewID := rawString(t, preview["preview_id"])
	start := make(chan struct{})
	results := make(chan *httptest.ResponseRecorder, 2)
	var workers sync.WaitGroup
	for range 2 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			w := httptest.NewRecorder()
			f.handler.CreateGitHubAgent(w, withURLParam(newRequest(http.MethodPost, "/", map[string]any{
				"preview_id":previewID, "runtime_id":testRuntimeID, "name":"Concurrent " + previewID,
			}), "id", testWorkspaceID))
			results <- w
		}()
	}
	close(start)
	workers.Wait()
	close(results)
	ids := map[string]bool{}
	for response := range results {
		if response.Code != http.StatusCreated && response.Code != http.StatusOK { t.Fatalf("confirmation status = %d: %s", response.Code, response.Body.String()) }
		var body struct { Agent struct { ID string `json:"id"` } `json:"agent"` }
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil { t.Fatal(err) }
		ids[body.Agent.ID] = true
	}
	if len(ids) != 1 { t.Fatalf("created Agent IDs = %#v", ids) }
}

func TestGitHubSourceRejectsExpiredAndForeignAgentPreviews(t *testing.T) {
	f := newGitSourceFixture(t)
	agentID := f.create(t)
	preview := f.request(t, f.handler.PreviewAgentSourceSync, agentID, map[string]any{"ref":"release/v2"}, http.StatusOK)
	previewID := rawString(t, preview["preview_id"])
	otherAgentID := f.create(t)
	f.request(t, f.handler.SyncAgentSource, otherAgentID, map[string]any{"preview_id":previewID}, http.StatusBadRequest)
	if _, err := testPool.Exec(t.Context(), "UPDATE agent_source_preview SET expires_at = now() - interval '1 minute' WHERE id = $1", previewID); err != nil { t.Fatal(err) }
	f.request(t, f.handler.SyncAgentSource, agentID, map[string]any{"preview_id":previewID}, http.StatusConflict)
}

func TestGitHubSourceConfirmationRejectsAnotherUsersPreview(t *testing.T) {
	f := newGitSourceFixture(t)
	agentID := f.create(t)
	preview := f.request(t, f.handler.PreviewAgentSourceSync, agentID, map[string]any{"ref":"release/v2"}, http.StatusOK)
	previewID := rawString(t, preview["preview_id"])
	// Keep the caller's Agent management permission, but give the candidate to
	// a different user. Management alone must not authorize their confirmation.
	if _, err := testPool.Exec(t.Context(), "UPDATE agent_source_preview SET created_by = gen_random_uuid() WHERE id = $1", previewID); err != nil { t.Fatal(err) }
	f.request(t, f.handler.SyncAgentSource, agentID, map[string]any{"preview_id":previewID}, http.StatusNotFound)
}

func TestGitHubSourceCompetingPreviewsCannotOverwritePublishedVersion(t *testing.T) {
	f := newGitSourceFixture(t)
	agentID := f.create(t)
	first := f.request(t, f.handler.PreviewAgentSourceSync, agentID, map[string]any{"ref":"release/v2"}, http.StatusOK)
	second := f.request(t, f.handler.PreviewAgentSourceSync, agentID, map[string]any{"ref":"main"}, http.StatusOK)
	f.request(t, f.handler.SyncAgentSource, agentID, map[string]any{"preview_id":rawString(t, first["preview_id"])}, http.StatusOK)
	f.request(t, f.handler.SyncAgentSource, agentID, map[string]any{"preview_id":rawString(t, second["preview_id"])}, http.StatusConflict)
	agent, err := testHandler.Queries.GetAgent(t.Context(), parseUUID(agentID))
	if err != nil || agent.Instructions != "Review code v2" { t.Fatal("competing preview overwrote the published Agent") }
}

func TestGitHubSourceSyncRollsBackAllConfigurationOnSkillFailure(t *testing.T) {
	f := newGitSourceFixture(t)
	agentID := f.create(t)
	preview := f.request(t, f.handler.PreviewAgentSourceSync, agentID, map[string]any{"ref":"release/v2"}, http.StatusOK)
	// A trigger failure occurs after the Agent instruction UPDATE. The release
	// must leave both the Agent and source receipt at the previous version.
	if _, err := testPool.Exec(t.Context(), `CREATE FUNCTION test_source_skill_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture skill write failure'; END $$`); err != nil { t.Fatal(err) }
	if _, err := testPool.Exec(t.Context(), `CREATE TRIGGER test_source_skill_failure BEFORE UPDATE ON skill FOR EACH ROW EXECUTE FUNCTION test_source_skill_failure()`); err != nil { t.Fatal(err) }
	t.Cleanup(func() { _, _ = testPool.Exec(context.Background(), `DROP TRIGGER IF EXISTS test_source_skill_failure ON skill; DROP FUNCTION IF EXISTS test_source_skill_failure()`) })
	f.request(t, f.handler.SyncAgentSource, agentID, map[string]any{"preview_id":rawString(t, preview["preview_id"])}, http.StatusInternalServerError)
	agent, err := testHandler.Queries.GetAgent(t.Context(), parseUUID(agentID))
	if err != nil || agent.Instructions != "Review code v1" { t.Fatal("Agent update escaped rollback") }
	source, err := testHandler.Queries.GetAgentSourceByAgentID(t.Context(), agent.ID)
	if err != nil || source.SyncedCommitSha != gitSourceSHA1 { t.Fatal("source version escaped rollback") }
}

func TestGitHubSourcePublishRenamedSkillDirectory(t *testing.T) {
	f := newGitSourceFixture(t)
	agentID := f.create(t)
	files := f.files[gitSourceSHA2]
	files["agent.json"] = strings.ReplaceAll(files["agent.json"], "agent/skills", "new/skills")
	for p, content := range files {
		if strings.HasPrefix(p, "agent/skills/") { files[strings.Replace(p, "agent/skills/", "new/skills/", 1)] = content; delete(files, p) }
	}
	preview := f.request(t, f.handler.PreviewAgentSourceSync, agentID, map[string]any{"ref":"release/v2"}, http.StatusOK)
	f.request(t, f.handler.SyncAgentSource, agentID, map[string]any{"preview_id":rawString(t, preview["preview_id"])}, http.StatusOK)
	skills, err := testHandler.Queries.ListAgentSkills(t.Context(), parseUUID(agentID))
	if err != nil || len(skills) != 1 { t.Fatalf("renamed skill: %#v %v", skills, err) }
}
