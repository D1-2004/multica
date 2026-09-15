package gitrepo

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCodeRepositoryReadsPinnedObjectsWithPATHeader(t *testing.T) {
	const sha = "0123456789012345678901234567890123456789"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request) {
		if r.Header.Get("PRIVATE-TOKEN") != "code_pat_fixture" || strings.Contains(r.URL.String(),"fixture") { t.Error("PAT must only appear in its header") }
		w.Header().Set("Content-Type","application/json")
		switch r.URL.Path {
		case "/api/v3/user": w.Write([]byte(`{"id":42,"username":"alice"}`))
		case "/api/v3/projects/team/agent": w.Write([]byte(`{"id":7,"path_with_namespace":"team/agent","default_branch":"main"}`))
		case "/api/v3/projects/7/repository/branches/main": json.NewEncoder(w).Encode(map[string]any{"name":"main","commit":map[string]string{"id":sha}})
		case "/api/v4/projects/7/repository/tree":
			if r.URL.Query().Get("ref_name") != sha || r.URL.Query().Get("type") != "RECURSIVE" { t.Error("tree must use a pinned commit") }
			w.Write([]byte(`[{"id":"blob-1","path":"agent.json","mode":"100644","type":"blob","size":2}]`))
		case "/api/v4/projects/7/repository/blobs":
			if r.URL.Query().Get("ref") != sha || r.URL.Query().Get("filepath") != "agent.json" { t.Error("blob must use pinned path and commit") }
			w.Write([]byte(`{"content":"{}","total_lines":1}`))
		default: t.Errorf("unexpected request: %s",r.URL); http.NotFound(w,r)
		}
	}))
	defer server.Close()
	client,err := NewCodeClient(CodeConfig{APIBase:server.URL,HTTPClient:server.Client()},"code_pat_fixture")
	if err != nil { t.Fatal(err) }
	account,err := client.Account(context.Background()); if err != nil || account.Username != "alice" { t.Fatalf("account: %#v %v",account,err) }
	address,_ := ParseAddress("https://code.alibaba-inc.com/team/agent")
	remote,err := client.Open(context.Background(),address); if err != nil { t.Fatal(err) }
	commit,err := remote.ResolveCommit(context.Background(),"refs/heads/main"); if err != nil || commit != sha { t.Fatalf("commit %s: %v",commit,err) }
	tree,err := remote.GetTree(context.Background(),commit); if err != nil || len(tree.Entries) != 1 { t.Fatalf("tree: %#v %v",tree,err) }
	content,err := remote.GetBlob(context.Background(),tree.Entries[0].SHA); if err != nil || string(content) != "{}" { t.Fatalf("blob: %q %v",content,err) }
}

func TestCodePATNeverFollowsRedirectOrLeaksInErrors(t *testing.T) {
	visited := false
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request) { visited=true }))
	defer other.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request) { http.Redirect(w,r,other.URL+"/?token=code_pat_fixture",http.StatusFound) }))
	defer server.Close()
	client,err := NewCodeClient(CodeConfig{APIBase:server.URL},"code_pat_fixture"); if err != nil { t.Fatal(err) }
	_,err=client.Account(context.Background())
	if err == nil || visited || strings.Contains(err.Error(),"code_pat_fixture") { t.Fatalf("unsafe redirect handling: visited=%v err=%v",visited,err) }
}

func TestCodeRefsUseDocumentedPageEnvelope(t *testing.T) {
	srv:=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
		if strings.HasSuffix(r.URL.Path,"/projects/team/repo") { json.NewEncoder(w).Encode(map[string]any{"id":7,"path_with_namespace":"team/repo","default_branch":"main"});return }
		if r.URL.Path!="/api/v3/projects/7/repository/branches" && r.URL.Path!="/api/v4/projects/7/repository/tags" { t.Errorf("unexpected endpoint %s",r.URL.Path);http.NotFound(w,r);return }
		name:="first";if r.URL.Query().Get("page")=="2" {name="second"}
		json.NewEncoder(w).Encode(map[string]any{"amount":2,"list":[]map[string]any{{"name":name,"commit":map[string]string{"id":strings.Repeat("a",40)}}}})
	}));defer srv.Close()
	client,err:=NewCodeClient(CodeConfig{APIBase:srv.URL},"code_pat_fixture");if err!=nil{t.Fatal(err)}
	address,_:=ParseAddress("https://code.alibaba-inc.com/team/repo")
	remote,err:=client.Open(t.Context(),address);if err!=nil{t.Fatal(err)}
	branches,err:=remote.ListBranches(t.Context());if err!=nil || len(branches)!=2{t.Fatalf("branch pagination: %#v, %v",branches,err)}
	tags,err:=remote.ListTags(t.Context());if err!=nil || len(tags)!=2{t.Fatalf("tag pagination: %#v, %v",tags,err)}
	sha,err:=remote.ResolveCommit(t.Context(),"refs/tags/second")
	if err!=nil || sha!=strings.Repeat("a",40){t.Fatalf("resolve paginated tag: %s, %v",sha,err)}
}
