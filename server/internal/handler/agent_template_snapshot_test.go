package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/multica-ai/multica/server/internal/agentsource"
	"github.com/multica-ai/multica/server/internal/agenttemplate"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestSelectTemplateSkills(t *testing.T) {
	available := []agentsource.Skill{
		{SourcePath: "skills/factory", Name: "Factory"},
		{SourcePath: "skills/audit", Name: "Audit"},
	}
	all, err := selectTemplateSkills(available, nil, false)
	if err != nil || len(all) != 2 {
		t.Fatalf("unspecified selection = %#v, %v", all, err)
	}
	none, err := selectTemplateSkills(available, []string{}, true)
	if err != nil || len(none) != 0 {
		t.Fatalf("explicit empty selection = %#v, %v", none, err)
	}
	one, err := selectTemplateSkills(available, []string{"skills/audit"}, true)
	if err != nil || len(one) != 1 || one[0].SourcePath != "skills/audit" {
		t.Fatalf("single selection = %#v, %v", one, err)
	}
	if _, err := selectTemplateSkills(available, []string{"skills/missing"}, true); err == nil {
		t.Fatal("unknown template skill path was accepted")
	}
}

func TestCreateAgentFromTemplateMaterializesIndependentSnapshot(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	workspaceID := parseUUID(testWorkspaceID)
	userID := parseUUID(testUserID)
	_, err := testHandler.Queries.CreateWorkspaceTemplateFromSeed(ctx, db.CreateWorkspaceTemplateFromSeedParams{
		WorkspaceID: workspaceID,
		Slug:        agenttemplate.DefaultSlug,
		CreatedBy:   userID,
		SystemKey:   agenttemplate.DefaultSystemKey,
	})
	if err != nil {
		t.Fatalf("CreateWorkspaceTemplateFromSeed: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_template WHERE workspace_id = $1 AND slug = $2`, testWorkspaceID, agenttemplate.DefaultSlug)
	})

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/workspaces/"+testWorkspaceID+"/agent-templates/"+agenttemplate.DefaultSlug+"/agents", map[string]any{
		"runtime_id": handlerTestRuntimeID(t),
		"name":       "Snapshot Materialization Test",
	})
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("id", testWorkspaceID)
	routeContext.URLParams.Add("slug", agenttemplate.DefaultSlug)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeContext))
	testHandler.CreateAgentFromTemplate(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateAgentFromTemplate: got %d: %s", w.Code, w.Body.String())
	}
	var result struct {
		AgentID      string `json:"agent_id"`
		TemplateSlug string `json:"template_slug"`
	}
	if err := json.NewDecoder(w.Body).Decode(&result); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if result.AgentID == "" || result.TemplateSlug != agenttemplate.DefaultSlug {
		t.Fatalf("unexpected response: %#v", result)
	}
	t.Cleanup(func() { _, _ = testPool.Exec(context.Background(), `DELETE FROM agent WHERE id = $1`, result.AgentID) })

	var sourceCount, skillCount, fileCount int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM agent_source WHERE agent_id = $1`, result.AgentID).Scan(&sourceCount); err != nil {
		t.Fatal(err)
	}
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM agent_skill WHERE agent_id = $1`, result.AgentID).Scan(&skillCount); err != nil {
		t.Fatal(err)
	}
	if err := testPool.QueryRow(ctx, `
		SELECT count(*) FROM skill_file f
		JOIN agent_skill a ON a.skill_id = f.skill_id
		WHERE a.agent_id = $1
	`, result.AgentID).Scan(&fileCount); err != nil {
		t.Fatal(err)
	}
	if sourceCount != 0 || skillCount != 2 || fileCount < 2 {
		t.Fatalf("materialized source=%d skills=%d files=%d", sourceCount, skillCount, fileCount)
	}

	if _, err := testPool.Exec(ctx, `DELETE FROM agent_template WHERE workspace_id = $1 AND slug = $2`, testWorkspaceID, agenttemplate.DefaultSlug); err != nil {
		t.Fatalf("delete template: %v", err)
	}
	var remaining int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM agent WHERE id = $1`, result.AgentID).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 1 {
		t.Fatalf("agent was coupled to deleted template: count=%d", remaining)
	}
}

func TestAgentTemplateGitHubSourceEnforcesWorkspaceAndDisconnectsInPlace(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	tx, err := testPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	queries := db.New(tx)
	seed, err := agenttemplate.LoadDefaultSeed(ctx)
	if err != nil {
		t.Fatal(err)
	}
	created, err := queries.CreateGitHubAgentTemplate(ctx, db.CreateGitHubAgentTemplateParams{
		WorkspaceID: parseUUID(testWorkspaceID), Slug: "github-isolation-test",
		DisplayName: seed.DisplayName, Description: seed.Description,
		BundleSchemaVersion: agenttemplate.BundleSchemaVersion,
		BundleSizeBytes:     int32(len(seed.BundleJSON)), Bundle: seed.BundleJSON,
		ContentHash: seed.Bundle.Hash, CreatedBy: parseUUID(testUserID),
	})
	if err != nil {
		t.Fatal(err)
	}
	var otherWorkspaceID, otherInstallationID, localInstallationID string
	if err := tx.QueryRow(ctx, `INSERT INTO workspace (name, slug, issue_prefix) VALUES ('Other', $1, 'OTH') RETURNING id`, "template-isolation-"+time.Now().UTC().Format("150405000000000")).Scan(&otherWorkspaceID); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO github_installation (workspace_id, installation_id, account_login) VALUES ($1, $2, 'other') RETURNING id`, otherWorkspaceID, time.Now().UnixNano()).Scan(&otherInstallationID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `SAVEPOINT cross_workspace_check`); err != nil {
		t.Fatal(err)
	}
	_, err = queries.CreateAgentTemplateGitHubSource(ctx, db.CreateAgentTemplateGitHubSourceParams{
		TemplateID: created.ID, WorkspaceID: parseUUID(testWorkspaceID), GithubInstallationID: parseUUID(otherInstallationID),
		RepoOwner: "owner", RepoName: "repo", Ref: "main", SyncedCommitSha: strings.Repeat("a", 40),
	})
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23503" {
		t.Fatalf("cross-workspace source error = %v, want foreign-key violation", err)
	}
	if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT cross_workspace_check`); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO github_installation (workspace_id, installation_id, account_login) VALUES ($1, $2, 'local') RETURNING id`, testWorkspaceID, time.Now().UnixNano()+1).Scan(&localInstallationID); err != nil {
		t.Fatal(err)
	}
	if _, err := queries.CreateAgentTemplateGitHubSource(ctx, db.CreateAgentTemplateGitHubSourceParams{
		TemplateID: created.ID, WorkspaceID: parseUUID(testWorkspaceID), GithubInstallationID: parseUUID(localInstallationID),
		RepoOwner: "owner", RepoName: "repo", Ref: "main", SyncedCommitSha: strings.Repeat("b", 40),
	}); err != nil {
		t.Fatalf("same-workspace source: %v", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM github_installation WHERE id = $1`, localInstallationID); err != nil {
		t.Fatalf("disconnect installation: %v", err)
	}
	source, err := queries.GetAgentTemplateGitHubSource(ctx, db.GetAgentTemplateGitHubSourceParams{WorkspaceID: parseUUID(testWorkspaceID), TemplateID: created.ID})
	if err != nil {
		t.Fatal(err)
	}
	if source.GithubInstallationID.Valid || source.WorkspaceID != parseUUID(testWorkspaceID) {
		t.Fatalf("disconnected source changed ownership: %#v", source)
	}
}
