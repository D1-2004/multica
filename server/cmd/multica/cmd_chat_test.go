package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

const (
	chatTestSessionID = "abcd1234-1111-4222-8333-1234567890ab"
	chatTestAgentID   = "aabbccdd-1111-4222-8333-1234567890ab"
)

func newChatTestCommand(use, output string) *cobra.Command {
	cmd := &cobra.Command{Use: use}
	cmd.Flags().String("output", output, "")
	cmd.Flags().String("agent", "", "")
	cmd.Flags().String("title", "", "")
	return cmd
}

func setupChatCLIEnv(t *testing.T, serverURL string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MULTICA_SERVER_URL", serverURL)
	t.Setenv("MULTICA_WORKSPACE_ID", "workspace-123")
	t.Setenv("MULTICA_TOKEN", "mul_test")
	t.Setenv("MULTICA_AGENT_ID", "")
	t.Setenv("MULTICA_TASK_ID", "")
	t.Setenv("MULTICA_DAEMON_PORT", "")
}

func TestChatListGetMessages(t *testing.T) {
	var requests []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.RequestURI())
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/chat/sessions":
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"id": chatTestSessionID, "agent_id": chatTestAgentID,
				"status": "active", "title": "CLI session", "updated_at": "2026-07-12T12:00:00Z",
			}})
		case "/api/chat/sessions/" + chatTestSessionID:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": chatTestSessionID, "agent_id": chatTestAgentID,
				"status": "active", "title": "CLI session",
			})
		case "/api/chat/sessions/" + chatTestSessionID + "/messages":
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"id": "message-1", "role": "user", "content": "hello",
				"task_id": "task-1", "created_at": "2026-07-12T12:01:00Z",
			}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	setupChatCLIEnv(t, srv.URL)

	listOut, err := captureStdout(t, func() error {
		return runChatList(newChatTestCommand("list", "table"), nil)
	})
	if err != nil {
		t.Fatalf("chat list: %v", err)
	}
	for _, want := range []string{"ID", "AGENT", "STATUS", "TITLE", "CLI session"} {
		if !strings.Contains(listOut, want) {
			t.Fatalf("chat list table missing %q:\n%s", want, listOut)
		}
	}

	getOut, err := captureStdout(t, func() error {
		return runChatGet(newChatTestCommand("get", "json"), []string{"abcd"})
	})
	if err != nil {
		t.Fatalf("chat get: %v", err)
	}
	if !strings.Contains(getOut, chatTestSessionID) {
		t.Fatalf("chat get output missing canonical session id: %s", getOut)
	}

	messagesOut, err := captureStdout(t, func() error {
		return runChatMessages(newChatTestCommand("messages", "table"), []string{chatTestSessionID})
	})
	if err != nil {
		t.Fatalf("chat messages: %v", err)
	}
	for _, want := range []string{"ROLE", "CONTENT", "user", "hello"} {
		if !strings.Contains(messagesOut, want) {
			t.Fatalf("chat messages table missing %q:\n%s", want, messagesOut)
		}
	}

	joined := strings.Join(requests, "\n")
	for _, want := range []string{
		"GET /api/chat/sessions",
		"GET /api/chat/sessions/" + chatTestSessionID,
		"GET /api/chat/sessions/" + chatTestSessionID + "/messages",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("requests missing %q:\n%s", want, joined)
		}
	}
}

func TestChatSendReadsMultilineStdin(t *testing.T) {
	var gotContent string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/chat/sessions/"+chatTestSessionID+"/messages" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		gotContent = fmt.Sprint(body["content"])
		_ = json.NewEncoder(w).Encode(map[string]any{
			"message_id": "message-1", "task_id": "task-1", "collected": true,
		})
	}))
	defer srv.Close()
	setupChatCLIEnv(t, srv.URL)

	cmd := newChatTestCommand("send", "json")
	cmd.SetIn(strings.NewReader("line one\nline two\n"))
	out, err := captureStdout(t, func() error {
		return runChatSend(cmd, []string{chatTestSessionID, "-"})
	})
	if err != nil {
		t.Fatalf("chat send: %v", err)
	}
	if gotContent != "line one\nline two" {
		t.Fatalf("content = %q, want multiline stdin", gotContent)
	}
	for _, want := range []string{chatTestSessionID, "message-1", "task-1", `"collected": true`} {
		if !strings.Contains(out, want) {
			t.Fatalf("chat send output missing %q: %s", want, out)
		}
	}
}

func TestChatStartResolvesAgentCreatesSessionThenSends(t *testing.T) {
	var order []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		order = append(order, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/agents":
			_ = json.NewEncoder(w).Encode([]map[string]any{{"id": chatTestAgentID, "name": "Lambda"}})
		case r.Method == http.MethodPost && r.URL.Path == "/api/chat/sessions":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["agent_id"] != chatTestAgentID || body["title"] != "CLI managed" {
				t.Fatalf("create body = %#v", body)
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": chatTestSessionID})
		case r.Method == http.MethodPost && r.URL.Path == "/api/chat/sessions/"+chatTestSessionID+"/messages":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["content"] != "hello world" {
				t.Fatalf("send body = %#v", body)
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"message_id": "message-1", "task_id": "task-1", "collected": false})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	setupChatCLIEnv(t, srv.URL)

	cmd := newChatTestCommand("start", "json")
	_ = cmd.Flags().Set("agent", "Lambda")
	_ = cmd.Flags().Set("title", "CLI managed")
	out, err := captureStdout(t, func() error {
		return runChatStart(cmd, []string{"hello", "world"})
	})
	if err != nil {
		t.Fatalf("chat start: %v", err)
	}
	wantOrder := []string{"GET /api/agents", "POST /api/chat/sessions", "POST /api/chat/sessions/" + chatTestSessionID + "/messages"}
	if strings.Join(order, "|") != strings.Join(wantOrder, "|") {
		t.Fatalf("request order = %#v, want %#v", order, wantOrder)
	}
	if !strings.Contains(out, chatTestSessionID) {
		t.Fatalf("chat start output missing session id: %s", out)
	}
}

func TestChatStartSendFailureIncludesCreatedSessionID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/chat/sessions":
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": chatTestSessionID})
		case r.Method == http.MethodPost && r.URL.Path == "/api/chat/sessions/"+chatTestSessionID+"/messages":
			http.Error(w, "runtime unavailable", http.StatusServiceUnavailable)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	setupChatCLIEnv(t, srv.URL)

	cmd := newChatTestCommand("start", "json")
	_ = cmd.Flags().Set("agent", chatTestAgentID)
	_, err := captureStdout(t, func() error {
		return runChatStart(cmd, []string{"hello"})
	})
	if err == nil || !strings.Contains(err.Error(), chatTestSessionID) {
		t.Fatalf("error = %v, want created session id %s", err, chatTestSessionID)
	}
}

func TestChatSendRejectsEmptyStdinLocally(t *testing.T) {
	setupChatCLIEnv(t, "http://127.0.0.1:1")
	cmd := newChatTestCommand("send", "json")
	cmd.SetIn(strings.NewReader(" \n\t"))
	err := runChatSend(cmd, []string{chatTestSessionID, "-"})
	if err == nil || !strings.Contains(err.Error(), "cannot be empty") {
		t.Fatalf("error = %v, want local empty-message validation", err)
	}
}

func TestChatManageCommandsRegistered(t *testing.T) {
	for _, name := range []string{"list", "get", "messages", "start", "send"} {
		if cmd, _, err := chatCmd.Find([]string{name}); err != nil || cmd == chatCmd || cmd.Name() != name {
			t.Fatalf("chat %s command is not registered", name)
		}
	}
}
