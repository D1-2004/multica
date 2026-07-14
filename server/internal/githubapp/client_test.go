package githubapp

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestClientCachesInstallationToken(t *testing.T) {
	var tokenRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/installations/42/access_tokens":
			tokenRequests.Add(1)
			if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
				t.Fatal("token exchange did not use an App JWT")
			}
			w.WriteHeader(http.StatusCreated)
			fmt.Fprintf(w, `{"token":"installation-token","expires_at":%q}`, time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
		case "/installation/repositories":
			if got := r.Header.Get("Authorization"); got != "Bearer installation-token" {
				t.Fatalf("repository auth = %q", got)
			}
			fmt.Fprint(w, `{"repositories":[{"id":1,"name":"agent","full_name":"acme/agent","private":true,"default_branch":"main"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := newTestClient(t, server.URL)
	for range 2 {
		repositories, err := client.ListRepositories(context.Background(), 42)
		if err != nil {
			t.Fatal(err)
		}
		if len(repositories) != 1 || repositories[0].FullName != "acme/agent" {
			t.Fatalf("unexpected repositories: %+v", repositories)
		}
	}
	if got := tokenRequests.Load(); got != 1 {
		t.Fatalf("token requests = %d, want 1", got)
	}
}

func TestClientEvictsTokenAndRetriesOnceOnUnauthorized(t *testing.T) {
	var tokenRequests atomic.Int32
	var repositoryRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/installations/7/access_tokens":
			n := tokenRequests.Add(1)
			w.WriteHeader(http.StatusCreated)
			fmt.Fprintf(w, `{"token":"token-%d","expires_at":%q}`, n, time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
		case "/installation/repositories":
			n := repositoryRequests.Add(1)
			if n == 1 {
				w.WriteHeader(http.StatusUnauthorized)
				fmt.Fprint(w, `{"message":"Bad credentials"}`)
				return
			}
			if got := r.Header.Get("Authorization"); got != "Bearer token-2" {
				t.Fatalf("retry auth = %q", got)
			}
			fmt.Fprint(w, `{"repositories":[]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := newTestClient(t, server.URL)
	if _, err := client.ListRepositories(context.Background(), 7); err != nil {
		t.Fatal(err)
	}
	if tokenRequests.Load() != 2 || repositoryRequests.Load() != 2 {
		t.Fatalf("requests: tokens=%d repositories=%d", tokenRequests.Load(), repositoryRequests.Load())
	}
}

func TestClientPaginatesRepositories(t *testing.T) {
	var repositoryRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/installations/8/access_tokens":
			w.WriteHeader(http.StatusCreated)
			fmt.Fprintf(w, `{"token":"token","expires_at":%q}`, time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
		case "/installation/repositories":
			repositoryRequests.Add(1)
			if r.URL.Query().Get("page") == "1" {
				fmt.Fprint(w, `{"repositories":[`)
				for index := range 100 {
					if index > 0 {
						fmt.Fprint(w, ",")
					}
					fmt.Fprintf(w, `{"id":%d,"full_name":"acme/repo-%d"}`, index+1, index+1)
				}
				fmt.Fprint(w, `]}`)
				return
			}
			fmt.Fprint(w, `{"repositories":[{"id":101,"full_name":"acme/repo-101"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := newTestClient(t, server.URL)
	repositories, err := client.ListRepositories(context.Background(), 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(repositories) != 101 || repositoryRequests.Load() != 2 {
		t.Fatalf("repositories=%d requests=%d", len(repositories), repositoryRequests.Load())
	}
}

func TestClientReturnsRateLimitError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/installations/10/access_tokens":
			w.WriteHeader(http.StatusCreated)
			fmt.Fprintf(w, `{"token":"token","expires_at":%q}`, time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
		case "/installation/repositories":
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, `{"message":"rate limit exceeded"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := newTestClient(t, server.URL)
	_, err := client.ListRepositories(context.Background(), 10)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("error = %v, want APIError 429", err)
	}
}

func TestClientRefusesCrossOriginRedirectWithoutLeakingToken(t *testing.T) {
	var leaked atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			leaked.Store(true)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/installations/9/access_tokens":
			w.WriteHeader(http.StatusCreated)
			fmt.Fprintf(w, `{"token":"secret-token","expires_at":%q}`, time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
		case "/installation/repositories":
			http.Redirect(w, r, target.URL+"/steal", http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := newTestClient(t, server.URL)
	if _, err := client.ListRepositories(context.Background(), 9); err == nil {
		t.Fatal("expected redirect refusal")
	}
	if leaked.Load() {
		t.Fatal("authorization header leaked to redirected origin")
	}
}

func TestNewRejectsMissingCredentials(t *testing.T) {
	if _, err := New(Config{}); err != ErrUnavailable {
		t.Fatalf("New error = %v, want ErrUnavailable", err)
	}
}

func TestResolveCommitPreservesSlashBearingRef(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/installations/5/access_tokens":
			w.WriteHeader(http.StatusCreated)
			fmt.Fprintf(w, `{"token":"token","expires_at":%q}`, time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
		case "/repos/acme/agent/commits/release/v2":
			if r.URL.EscapedPath() != "/repos/acme/agent/commits/release%2Fv2" {
				t.Fatalf("escaped path = %q", r.URL.EscapedPath())
			}
			fmt.Fprint(w, `{"sha":"0123456789012345678901234567890123456789"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := newTestClient(t, server.URL)
	sha, err := client.ResolveCommit(context.Background(), 5, "acme", "agent", "release/v2")
	if err != nil {
		t.Fatal(err)
	}
	if sha == "" {
		t.Fatal("expected commit SHA")
	}
}

func TestGetTreeResolvesCommitToTreeSHA(t *testing.T) {
	const (
		commitSHA = "0123456789012345678901234567890123456789"
		treeSHA   = "abcdefabcdefabcdefabcdefabcdefabcdefabcd"
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/installations/6/access_tokens":
			w.WriteHeader(http.StatusCreated)
			fmt.Fprintf(w, `{"token":"token","expires_at":%q}`, time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
		case "/repos/acme/agent/git/commits/" + commitSHA:
			fmt.Fprintf(w, `{"sha":%q,"tree":{"sha":%q}}`, commitSHA, treeSHA)
		case "/repos/acme/agent/git/trees/" + treeSHA:
			if r.URL.Query().Get("recursive") != "1" {
				t.Fatalf("recursive query = %q", r.URL.RawQuery)
			}
			fmt.Fprintf(w, `{"sha":%q,"tree":[{"path":"multica-agent.yaml","mode":"100644","type":"blob","sha":"blob-sha","size":42}]}`, treeSHA)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := newTestClient(t, server.URL)
	tree, err := client.GetTree(context.Background(), 6, "acme", "agent", commitSHA)
	if err != nil {
		t.Fatal(err)
	}
	if tree.SHA != treeSHA || len(tree.Entries) != 1 {
		t.Fatalf("unexpected tree: %+v", tree)
	}
}

func newTestClient(t *testing.T, baseURL string) *Client {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pemKey := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	client, err := New(Config{
		AppID:      "123",
		PrivateKey: string(pemKey),
		APIBase:    baseURL,
		HTTPClient: &http.Client{Timeout: time.Second},
	})
	if err != nil {
		t.Fatal(err)
	}
	return client
}
