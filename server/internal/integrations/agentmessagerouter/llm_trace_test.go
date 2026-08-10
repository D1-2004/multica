package agentmessagerouter

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientSubmitLLMTraceUsesTrustedBaseAndTaskCapability(t *testing.T) {
	wantBody := []byte(`{"sequence":1,"request":{"body":"request"},"response":{"body":"response"}}`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/dispatch-tasks/router-task-1/llm-traces" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer task-capability" {
			t.Fatalf("Authorization = %q", got)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		if string(body) != string(wantBody) {
			t.Fatalf("body = %s", body)
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	client, err := NewClient(ClientConfig{BaseURL: server.URL, ServiceCredential: "service-secret"})
	if err != nil {
		t.Fatal(err)
	}
	status, err := client.SubmitLLMTrace(
		context.Background(),
		"/api/v1/dispatch-tasks/router-task-1/llm-traces",
		"task-capability",
		wantBody,
	)
	if err != nil || status != http.StatusCreated {
		t.Fatalf("status=%d err=%v", status, err)
	}
}
func TestClientSubmitLLMTraceRejectsUntrustedCallbackOrCapability(t *testing.T) {
	client, err := NewClient(ClientConfig{BaseURL: "https://router.example.test", ServiceCredential: "service-secret"})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name       string
		path       string
		capability string
	}{
		{name: "absolute callback", path: "https://evil.example.test/api/v1/dispatch-tasks/task/llm-traces", capability: "task-capability"},
		{name: "wrong callback", path: "/api/v1/dispatch-tasks/task/execution-result", capability: "task-capability"},
		{name: "empty capability", path: "/api/v1/dispatch-tasks/task/llm-traces", capability: ""},
		{name: "capability whitespace", path: "/api/v1/dispatch-tasks/task/llm-traces", capability: "task capability"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := client.SubmitLLMTrace(context.Background(), test.path, test.capability, []byte(`{}`)); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}
