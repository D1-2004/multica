package middleware

import (
	"context"
	"net/http"
	"strings"
)

const WorkspaceAccessActorSource = "workspace_access_token"

type workspaceAccessPrincipalContextKey struct{}

// WorkspaceAccessPrincipal is the authenticated, database-backed policy for a
// DTA workspace credential. It is deliberately separate from workspace roles:
// a Token subject is not a member and never becomes an owner or admin.
type WorkspaceAccessPrincipal struct {
	TokenID      string
	UserID       string
	WorkspaceID  string
	Name         string
	Capabilities []string
	Version      int32
}

func WithWorkspaceAccessPrincipal(ctx context.Context, principal WorkspaceAccessPrincipal) context.Context {
	return context.WithValue(ctx, workspaceAccessPrincipalContextKey{}, principal)
}

func WorkspaceAccessPrincipalFromContext(ctx context.Context) (WorkspaceAccessPrincipal, bool) {
	principal, ok := ctx.Value(workspaceAccessPrincipalContextKey{}).(WorkspaceAccessPrincipal)
	return principal, ok
}

func (p WorkspaceAccessPrincipal) HasCapability(capability string) bool {
	for _, candidate := range p.Capabilities {
		if candidate == capability {
			return true
		}
	}
	return false
}

// workspaceAccessCapabilityForRequest is the fail-closed operation registry.
// A newly-added API route is inaccessible to a DTA Token until it is explicitly
// mapped here and receives resource-level checks in its handler.
func workspaceAccessCapabilityForRequest(r *http.Request) (string, bool) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) == 3 && parts[0] == "api" && parts[1] == "workspace-access" && parts[2] == "self" && r.Method == http.MethodGet {
		return "", true
	}

	if len(parts) == 2 && parts[0] == "api" && parts[1] == "agents" {
		if r.Method == http.MethodGet || r.Method == http.MethodPost {
			return "deployment.manage", true
		}
	}
	if len(parts) >= 3 && parts[0] == "api" && parts[1] == "agents" {
		switch {
		case len(parts) == 3 && (r.Method == http.MethodGet || r.Method == http.MethodPut):
			return "deployment.manage", true
		case len(parts) == 4 && parts[3] == "archive" && r.Method == http.MethodPost:
			return "deployment.retire", true
		case len(parts) == 4 && parts[3] == "tasks" && r.Method == http.MethodGet:
			return "trace.read", true
		case len(parts) == 4 && parts[3] == "restore" && r.Method == http.MethodPost:
			return "deployment.manage", true
		case len(parts) == 4 && parts[3] == "source" && r.Method == http.MethodGet:
			return "deployment.manage", true
		case len(parts) == 5 && parts[3] == "source" && parts[4] == "sync" && r.Method == http.MethodPost:
			return "deployment.manage", true
		case len(parts) == 4 && parts[3] == "skills" && (r.Method == http.MethodGet || r.Method == http.MethodPut):
			return "deployment.manage", true
		case len(parts) == 5 && parts[3] == "skills" && parts[4] == "add" && r.Method == http.MethodPost:
			return "deployment.manage", true
		case len(parts) == 5 && parts[3] == "skills" && r.Method == http.MethodDelete:
			return "deployment.manage", true
		case len(parts) == 6 && parts[3] == "skills" && parts[5] == "enabled" && r.Method == http.MethodPut:
			return "deployment.manage", true
		}
	}

	if len(parts) >= 2 && parts[0] == "api" && parts[1] == "skills" {
		if len(parts) == 2 && (r.Method == http.MethodGet || r.Method == http.MethodPost) {
			return "deployment.manage", true
		}
		if len(parts) == 3 && parts[2] == "search" && r.Method == http.MethodGet {
			return "deployment.manage", true
		}
		if len(parts) == 3 && parts[2] != "import" && (r.Method == http.MethodGet || r.Method == http.MethodPut || r.Method == http.MethodDelete) {
			return "deployment.manage", true
		}
		if len(parts) == 4 && parts[3] == "files" && (r.Method == http.MethodGet || r.Method == http.MethodPut) {
			return "deployment.manage", true
		}
		if len(parts) == 5 && parts[3] == "files" && r.Method == http.MethodDelete {
			return "deployment.manage", true
		}
	}

	if len(parts) == 2 && parts[0] == "api" && parts[1] == "runtimes" && r.Method == http.MethodGet {
		return "deployment.manage", true
	}

	// GitHub Agent Source delivery reuses only installations that a human
	// workspace manager has already connected. DTA Tokens may discover those
	// bindings, preview an immutable source revision, and create an Agent from
	// that preview; they cannot connect, reuse, or delete installations.
	if len(parts) == 5 && parts[0] == "api" && parts[1] == "workspaces" && parts[3] == "github" {
		switch {
		case parts[4] == "installations" && r.Method == http.MethodGet:
			return "deployment.manage", true
		case parts[4] == "repositories" && r.Method == http.MethodGet:
			return "deployment.manage", true
		case parts[4] == "agent-preview" && r.Method == http.MethodPost:
			return "deployment.manage", true
		case parts[4] == "agents" && r.Method == http.MethodPost:
			return "deployment.manage", true
		}
	}

	// Purpose-built deployment verification surface. It can only create and
	// inspect server-stamped load-smoke Issues; generic Issue/Chat APIs remain
	// outside the workspace Token surface.
	if len(parts) >= 3 && parts[0] == "api" && parts[1] == "dta" && parts[2] == "load-smokes" {
		switch {
		case len(parts) == 3 && (r.Method == http.MethodGet || r.Method == http.MethodPost):
			return "deployment.manage", true
		case len(parts) == 5 && parts[4] == "runs" && r.Method == http.MethodGet:
			return "deployment.manage", true
		case len(parts) == 5 && parts[4] == "comments" && r.Method == http.MethodGet:
			return "deployment.manage", true
		case len(parts) == 5 && parts[4] == "retry" && r.Method == http.MethodPost:
			return "deployment.manage", true
		case len(parts) == 7 && parts[4] == "runs" && parts[6] == "messages" && r.Method == http.MethodGet:
			return "deployment.manage", true
		}
	}

	if len(parts) == 4 && parts[0] == "api" && parts[1] == "tasks" && parts[3] == "messages" && r.Method == http.MethodGet {
		return "trace.read", true
	}

	return "", false
}
