package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/gitrepo"
	"github.com/multica-ai/multica/server/internal/util/secretbox"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestGitRepositoryIdentityResolutionIncludesSkillLinks(t *testing.T) {
	f := newGitSourceFixture(t)
	for _, input := range []struct { address, provider string; connections int }{
		{"https://github.com/acme/reviewer", "github", 1},
		{"https://skills.sh/acme/reviewer/review", "github", 1},
		{"git@code.alibaba-inc.com:acme/reviewer.git", "alibaba_code", 0},
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

func TestCodeConnectionAgentPublicationAndPermissionBoundary(t *testing.T) {
	f:=newGitSourceFixture(t)
	readable:=true
	srv:=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
		if r.Header.Get("PRIVATE-TOKEN")!="code_pat_fixture" { t.Error("Code request missing scoped credential");w.WriteHeader(401);return }
		if strings.Contains(r.URL.RawQuery,"token") { t.Error("credential in URL") }
		if !readable { w.WriteHeader(403);return }
		switch r.URL.Path {
		case "/api/v3/user": writeJSON(w,200,map[string]any{"id":42,"username":"fixture-code-account"})
		case "/api/v3/projects/acme/reviewer":writeJSON(w,200,map[string]any{"id":42,"path_with_namespace":"acme/reviewer","default_branch":"main"})
		case "/api/v3/projects/42/repository/branches/main", "/api/v3/projects/42/repository/branches/release/v2":
			ref:=strings.TrimPrefix(r.URL.Path,"/api/v3/projects/42/repository/branches/");writeJSON(w,200,map[string]any{"commit":map[string]string{"id":f.refs[ref]}})
		case "/api/v4/projects/42/repository/tree":
			sha:=r.URL.Query().Get("ref_name");if f.files[sha]==nil {t.Error("tree read without pinned commit")}
			entries:=[]map[string]any{};for path,content:=range f.files[sha]{entries=append(entries,map[string]any{"id":sha+":"+path,"path":path,"mode":"100644","type":"blob","size":len(content)})};writeJSON(w,200,entries)
		case "/api/v4/projects/42/repository/blobs":
			sha:=r.URL.Query().Get("ref");if f.files[sha]==nil{t.Error("blob read without pinned commit")};writeJSON(w,200,map[string]any{"content":f.files[sha][r.URL.Query().Get("filepath")]})
		default:http.NotFound(w,r)
		}
	}));defer srv.Close()
	box,err:=secretbox.New(make([]byte,secretbox.KeySize));if err!=nil{t.Fatal(err)}
	f.handler.GitRepoSecrets=box;f.handler.GitRepoCodeConfig=gitrepo.CodeConfig{APIBase:srv.URL}
	connected:=f.request(t,f.handler.ConnectGitRepository,testWorkspaceID,map[string]any{"repository_url":"https://code.alibaba-inc.com/acme/reviewer","token":"code_pat_fixture"},200)
	connectionID:=rawString(t,connected["id"])
	t.Cleanup(func() {
		_ = f.handler.Queries.DeleteCodeGitConnection(context.Background(), db.DeleteCodeGitConnectionParams{ID:parseUUID(connectionID), WorkspaceID:parseUUID(testWorkspaceID)})
	})
	stored,err:=f.handler.Queries.GetGitConnection(t.Context(),db.GetGitConnectionParams{ID:parseUUID(connectionID),WorkspaceID:parseUUID(testWorkspaceID)})
	if err!=nil{t.Fatal(err)}
	if len(stored.TokenCiphertext)==0 || strings.Contains(string(stored.TokenCiphertext),"code_pat_fixture"){t.Fatal("credential was not encrypted")}
	if _,exists:=connected["token_ciphertext"];exists{t.Fatal("connection response exposes credential storage")}
	preview:=f.request(t,f.handler.PreviewGitAgent,testWorkspaceID,map[string]any{"repository":"git@code.alibaba-inc.com:acme/reviewer.git","ref":"refs/heads/main"},200)
	f.refs["main"]=gitSourceSHA2
	created:=f.request(t,f.handler.CreateAgentFromPackage,testWorkspaceID,map[string]any{"preview_id":rawString(t,preview["preview_id"]),"runtime_id":testRuntimeID},201)
	var agent struct{ ID string `json:"id"`;Instructions string `json:"instructions"` };if err:=json.Unmarshal(created["agent"],&agent);err!=nil{t.Fatal(err)}
	if agent.Instructions!="Review code v1"{t.Fatal("creation did not use previewed commit")}
	var source AgentSourceResponse;if err:=json.Unmarshal(created["source"],&source);err!=nil{t.Fatal(err)}
	if source.SourceType!="git" || source.RepositoryURL!="https://code.alibaba-inc.com/acme/reviewer" || source.ConnectionID==nil || *source.ConnectionID!=connectionID{t.Fatalf("wrong source: %#v",source)}
	syncPreview:=f.request(t,f.handler.PreviewAgentSourceSync,agent.ID,map[string]any{"ref":"refs/heads/release/v2"},200)
	if rawString(t,syncPreview["resolved_sha"])!=gitSourceSHA2{t.Fatal("publication did not resolve selected version")}
	readable=false
	f.request(t,f.handler.SyncAgentSource,agent.ID,map[string]any{"preview_id":rawString(t,syncPreview["preview_id"])},403)
	current,err:=f.handler.Queries.GetAgent(t.Context(),parseUUID(agent.ID));if err!=nil{t.Fatal(err)}
	if current.Instructions!="Review code v1"{t.Fatal("unauthorized publication wrote configuration")}
	readable=true
	f.request(t,f.handler.SyncAgentSource,agent.ID,map[string]any{"preview_id":rawString(t,syncPreview["preview_id"])},200)
	history:=f.request(t,f.handler.ListAgentPublications,agent.ID,nil,200)
	var publications []map[string]json.RawMessage;if err:=json.Unmarshal(history["publications"],&publications);err!=nil{t.Fatal(err)}
	if len(publications)!=2{t.Fatalf("publication count=%d",len(publications))}
	rollback:=f.request(t,f.handler.PreviewAgentSourceSync,agent.ID,map[string]any{"publication_id":rawString(t,publications[1]["id"])},200)
	f.request(t,f.handler.SyncAgentSource,agent.ID,map[string]any{"preview_id":rawString(t,rollback["preview_id"])},200)
	current,err=f.handler.Queries.GetAgent(t.Context(),parseUUID(agent.ID));if err!=nil{t.Fatal(err)}
	if current.Instructions!="Review code v1"{t.Fatal("rollback did not restore historical configuration")}
	_,err=f.handler.gitRepositories().Open(t.Context(),parseUUID(testWorkspaceID),"https://github.com/acme/reviewer",connectionID)
	if err==nil{t.Fatal("Code identity accepted for GitHub")}
	_,err=f.handler.gitRepositories().Open(t.Context(),parseUUID("ffffffff-ffff-ffff-ffff-ffffffffffff"),"https://code.alibaba-inc.com/acme/reviewer",connectionID)
	if err==nil{t.Fatal("cross-workspace identity accepted")}
}
