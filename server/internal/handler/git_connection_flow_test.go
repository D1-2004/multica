package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestGitRepositoryIdentityResolutionIncludesSkillLinks(t *testing.T) {
	f := newGitSourceFixture(t)
	for _, input := range []struct { address, provider string; connections int }{
		{"https://github.com/acme/reviewer", "github", 1},
		{"https://skills.sh/acme/reviewer/review", "github", 1},
	} {
		recorder := httptest.NewRecorder()
		request := withURLParam(newRequest(http.MethodGet, "/?repository="+url.QueryEscape(input.address), nil), "id", testWorkspaceID)
		f.handler.ResolveGitRepository(recorder, request)
		var response struct { Provider string `json:"provider"`; Connections []map[string]any `json:"connections"` }
		if recorder.Code != http.StatusOK { t.Fatalf("resolve %s: %d %s", input.address, recorder.Code, recorder.Body.String()) }
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil { t.Fatal(err) }
		if response.Provider != input.provider || len(response.Connections) < input.connections { t.Fatalf("wrong matching identities: %#v", response) }
		found := false
		for _, connection := range response.Connections {
			if connection["provider"] != input.provider { t.Fatal("identity from another platform was included") }
			if connection["id"] == f.installationID { found = true }
		}
		if input.provider == "github" && !found { t.Fatal("connected GitHub identity was not available for the repository") }
		if strings.Contains(recorder.Body.String(), "token") { t.Fatal("identity resolution exposed credentials") }
	}
}

func TestGitEndpointsRejectInternalCodeBeforeAccess(t *testing.T) {
	h := &Handler{}
	for _, host := range []string{"code.alibaba-inc.com", "gitlab.alibaba-inc.com", "code.aone.alibaba-inc.com", "code-sc.aone.alibaba-inc.com"} {
		for _, address := range []string{"https://" + host + "/team/repo", "git@" + host + ":team/repo.git", "ssh://git@" + host + "/team/repo.git"} {
			t.Run(address, func(t *testing.T) {
				if _, _, err := detectImportSource(address); err == nil {
					t.Fatal("skill import accepted an internal repository")
				}
				recorder := httptest.NewRecorder()
				request := withURLParam(newRequest(http.MethodGet, "/?repository="+url.QueryEscape(address), nil), "id", testWorkspaceID)
				h.ResolveGitRepository(recorder, request)
				if recorder.Code != http.StatusBadRequest { t.Fatalf("identity resolution status: %d", recorder.Code) }
				recorder = httptest.NewRecorder()
				request = withURLParam(newRequest(http.MethodPost, "/", GitAgentPreviewRequest{Repository:address}), "id", testWorkspaceID)
				h.PreviewGitAgent(recorder, request)
				if recorder.Code != http.StatusBadRequest { t.Fatalf("agent preview status: %d", recorder.Code) }
			})
		}
	}
}

func TestAgentSourceResponseDisablesUnsupportedRepository(t *testing.T) {
	for _, input := range []struct { repository string; connected bool }{
		{"https://github.com/team/agent", true},
		{"https://code.alibaba-inc.com/team/agent", false},
	} {
		response := agentSourceToResponse(db.AgentSource{SourceType:"git", RepositoryUrl:input.repository, SyncStatus:"ready"})
		if response.Connected != input.connected || response.CanSync != input.connected { t.Fatalf("wrong availability: %#v", response) }
		if !input.connected && response.SyncStatus != "disconnected" { t.Fatalf("unsupported source is still ready: %#v", response) }
	}
}

func TestGitConnectionsExcludeUnsupportedIdentities(t *testing.T) {
	f := newGitSourceFixture(t)
	tx, err := testPool.Begin(t.Context())
	if err != nil { t.Fatal(err) }
	defer tx.Rollback(t.Context())
	var unsupportedID string
	err = tx.QueryRow(t.Context(), `INSERT INTO git_connection (workspace_id, provider, account_login, token_ciphertext)
		VALUES ($1, 'alibaba_code', 'retired-identity', $2) RETURNING id`, parseUUID(testWorkspaceID), []byte("retired-test-ciphertext")).Scan(&unsupportedID)
	if err != nil { t.Fatal(err) }
	queries := db.New(tx)
	_, err = queries.GetGitConnection(t.Context(), db.GetGitConnectionParams{ID:parseUUID(unsupportedID), WorkspaceID:parseUUID(testWorkspaceID)})
	if err != pgx.ErrNoRows { t.Fatalf("unsupported identity lookup: %v", err) }
	h := &Handler{Queries:queries}
	recorder := httptest.NewRecorder()
	h.ListGitConnections(recorder, withURLParam(newRequest(http.MethodGet, "/", nil), "id", testWorkspaceID))
	if recorder.Code != http.StatusOK { t.Fatalf("list status: %d", recorder.Code) }
	var response struct { Connections []map[string]any `json:"connections"` }
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil { t.Fatal(err) }
	found := false
	for _, connection := range response.Connections {
		if connection["provider"] != "github" { t.Fatal("unsupported identity is still exposed") }
		if connection["id"] == f.installationID { found = true }
	}
	if !found { t.Fatal("GitHub identity disappeared") }
	if strings.Contains(recorder.Body.String(), "token") { t.Fatal("connection response exposed token fields") }
}
