package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWorkspaceAccessCapabilityForRequest(t *testing.T) {
	tests := []struct {
		method     string
		path       string
		capability string
		allowed    bool
	}{
		{http.MethodGet, "/api/workspace-access/self", "", true},
		{http.MethodGet, "/api/agents", "deployment.manage", true},
		{http.MethodPost, "/api/agents", "deployment.manage", true},
		{http.MethodPut, "/api/agents/a", "deployment.manage", true},
		{http.MethodPost, "/api/agents/a/archive", "deployment.retire", true},
		{http.MethodPost, "/api/agents/a/restore", "deployment.manage", true},
		{http.MethodGet, "/api/agents/a/tasks", "trace.read", true},
		{http.MethodDelete, "/api/agents/a/skills/s", "deployment.manage", true},
		{http.MethodPut, "/api/agents/a/skills/s/enabled", "deployment.manage", true},
		{http.MethodPut, "/api/agents/a/skills/s/future-route", "", false},
		{http.MethodDelete, "/api/agents/a/skills/s/future-route", "", false},
		{http.MethodGet, "/api/tasks/t/messages", "trace.read", true},
		{http.MethodGet, "/api/runtimes", "deployment.manage", true},
		{http.MethodGet, "/api/workspaces/w/github/installations", "deployment.manage", true},
		{http.MethodGet, "/api/workspaces/w/github/repositories", "deployment.manage", true},
		{http.MethodPost, "/api/workspaces/w/github/agent-preview", "deployment.manage", true},
		{http.MethodPost, "/api/workspaces/w/github/agents", "deployment.manage", true},
		{http.MethodGet, "/api/workspaces/w/github/connect", "", false},
		{http.MethodPost, "/api/workspaces/w/github/installations/reuse", "", false},
		{http.MethodDelete, "/api/workspaces/w/github/installations/i", "", false},
		{http.MethodPost, "/api/dta/load-smokes", "deployment.manage", true},
		{http.MethodGet, "/api/dta/load-smokes?agent_id=a&marker=m", "deployment.manage", true},
		{http.MethodGet, "/api/dta/load-smokes/i/runs", "deployment.manage", true},
		{http.MethodGet, "/api/dta/load-smokes/i/comments", "deployment.manage", true},
		{http.MethodPost, "/api/dta/load-smokes/i/retry", "deployment.manage", true},
		{http.MethodGet, "/api/dta/load-smokes/i/runs/t/messages", "deployment.manage", true},
		{http.MethodDelete, "/api/dta/load-smokes/i", "", false},
		{http.MethodGet, "/api/skills/s/files", "deployment.manage", true},
		{http.MethodDelete, "/api/skills/s/files/f", "deployment.manage", true},
		{http.MethodPost, "/api/skills/s/labels", "", false},
		{http.MethodGet, "/api/skills/s/labels", "", false},
		{http.MethodPost, "/api/runtimes/fc-e2b", "", false},
		{http.MethodDelete, "/api/runtimes/r", "", false},
		{http.MethodGet, "/api/me", "", false},
		{http.MethodGet, "/api/workspaces/w/members", "", false},
		{http.MethodPost, "/api/workspaces/w/access-tokens", "", false},
		{http.MethodPost, "/api/issues", "", false},
		{http.MethodGet, "/ws", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, nil)
			capability, allowed := workspaceAccessCapabilityForRequest(req)
			if allowed != tt.allowed || capability != tt.capability {
				t.Fatalf("got (%q, %v), want (%q, %v)", capability, allowed, tt.capability, tt.allowed)
			}
		})
	}
}

func TestWorkspaceAccessPrincipalHasCapability(t *testing.T) {
	principal := WorkspaceAccessPrincipal{Capabilities: []string{"deployment.manage", "trace.read"}}
	if !principal.HasCapability("trace.read") {
		t.Fatal("trace.read should be present")
	}
	if principal.HasCapability("deployment.retire") {
		t.Fatal("deployment.retire should be absent")
	}
}
