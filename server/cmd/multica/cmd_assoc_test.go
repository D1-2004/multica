package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/spf13/cobra"
)

func newAssocRecallTestCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "recall"}
	cmd.Flags().String("server-url", "", "")
	cmd.Flags().String("workspace-id", "", "")
	cmd.Flags().String("profile", "", "")
	cmd.Flags().String("since", "", "")
	cmd.Flags().String("until", "", "")
	cmd.Flags().String("conversation", "", "")
	cmd.Flags().String("person", "", "")
	cmd.Flags().String("issue", "", "")
	cmd.Flags().Bool("current-issue", false, "")
	cmd.Flags().String("agent-id", "", "")
	cmd.Flags().String("q", "", "")
	cmd.Flags().String("intent", "", "")
	cmd.Flags().Int("limit", 20, "")
	cmd.Flags().String("output", "json", "")
	return cmd
}

func TestRunAssocRecallRequiresSince(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MULTICA_TOKEN", "test-token")
	t.Setenv("MULTICA_WORKSPACE_ID", "ws-123")
	t.Setenv("MULTICA_SERVER_URL", "http://127.0.0.1:1")
	cmd := newAssocRecallTestCmd()
	if err := runAssocRecall(cmd, nil); err == nil {
		t.Fatal("expected --since error")
	}
}

func TestRunAssocRecallSendsQuery(t *testing.T) {
	var gotPath, gotQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		_ = json.NewEncoder(w).Encode(map[string]any{
			"since": "2026-01-01T00:00:00Z",
			"until": "2026-01-02T00:00:00Z",
			"items": []any{},
		})
	}))
	defer server.Close()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MULTICA_TOKEN", "test-token")
	t.Setenv("MULTICA_WORKSPACE_ID", "ws-123")
	t.Setenv("MULTICA_SERVER_URL", server.URL)

	cmd := newAssocRecallTestCmd()
	_ = cmd.Flags().Set("since", "48h")
	_ = cmd.Flags().Set("conversation", "cid-a")
	_ = cmd.Flags().Set("agent-id", "11111111-1111-1111-1111-111111111111")
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	if err := runAssocRecall(cmd, nil); err != nil {
		t.Fatalf("runAssocRecall: %v", err)
	}
	if gotPath != "/api/assoc/recall" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotQuery == "" || !containsAll(gotQuery, "since=48h", "conversation_id=cid-a") {
		t.Fatalf("query = %q", gotQuery)
	}
}

func TestRunAssocRecallCurrentIssue(t *testing.T) {
	var gotQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{}})
	}))
	defer server.Close()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MULTICA_TOKEN", "test-token")
	t.Setenv("MULTICA_WORKSPACE_ID", "ws-123")
	t.Setenv("MULTICA_SERVER_URL", server.URL)
	t.Setenv("MULTICA_ISSUE_ID", "33333333-3333-3333-3333-333333333333")

	cmd := newAssocRecallTestCmd()
	_ = cmd.Flags().Set("since", "24h")
	_ = cmd.Flags().Set("current-issue", "true")
	cmd.SetOut(&bytes.Buffer{})
	if err := runAssocRecall(cmd, nil); err != nil {
		t.Fatalf("runAssocRecall: %v", err)
	}
	if !containsAll(gotQuery, "issue=33333333-3333-3333-3333-333333333333") {
		t.Fatalf("query = %q", gotQuery)
	}
}

func TestRunAssocRecallQOnly(t *testing.T) {
	var gotQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{}})
	}))
	defer server.Close()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MULTICA_TOKEN", "test-token")
	t.Setenv("MULTICA_WORKSPACE_ID", "ws-123")
	t.Setenv("MULTICA_SERVER_URL", server.URL)

	cmd := newAssocRecallTestCmd()
	_ = cmd.Flags().Set("since", "48h")
	_ = cmd.Flags().Set("q", "预约周五")
	_ = cmd.Flags().Set("agent-id", "11111111-1111-1111-1111-111111111111")
	cmd.SetOut(&bytes.Buffer{})
	if err := runAssocRecall(cmd, nil); err != nil {
		t.Fatalf("runAssocRecall: %v", err)
	}
	if !containsAll(gotQuery, "q=") || !containsAll(gotQuery, "since=48h") {
		t.Fatalf("query = %q", gotQuery)
	}
}

func TestRunAssocBind(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_ = json.NewEncoder(w).Encode(map[string]any{"conversation_id": "cid-a", "linked": false})
	}))
	defer server.Close()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MULTICA_TOKEN", "mat_test")
	t.Setenv("MULTICA_WORKSPACE_ID", "ws-123")
	t.Setenv("MULTICA_SERVER_URL", server.URL)

	cmd := &cobra.Command{Use: "bind"}
	cmd.Flags().String("server-url", "", "")
	cmd.Flags().String("workspace-id", "", "")
	cmd.Flags().String("profile", "", "")
	cmd.Flags().String("conversation", "", "")
	cmd.Flags().String("evidence", "", "")
	cmd.Flags().String("person", "", "")
	cmd.Flags().String("kind", "", "")
	cmd.Flags().String("output", "json", "")
	_ = cmd.Flags().Set("conversation", "cid-a")
	_ = cmd.Flags().Set("evidence", "msg-1")
	cmd.SetOut(&bytes.Buffer{})
	if err := runAssocBind(cmd, nil); err != nil {
		t.Fatalf("runAssocBind: %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/api/assoc/bind-outbound" {
		t.Fatalf("request %s %s", gotMethod, gotPath)
	}
	// No kind is sent unless the caller states one: the server keeps a known
	// conversation's kind and refuses to guess a new one.
	if _, sent := gotBody["kind"]; gotBody["conversation_id"] != "cid-a" || gotBody["evidence_id"] != "msg-1" || sent {
		t.Fatalf("body=%#v", gotBody)
	}
}

func containsAll(haystack string, needles ...string) bool {
	for _, n := range needles {
		if !bytes.Contains([]byte(haystack), []byte(n)) {
			return false
		}
	}
	return true
}
