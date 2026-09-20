package middleware

import (
	"context"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"net/http"
	"strings"
)

const WorkspaceAccessActorSource = "workspace_access_token"

type workspaceAccessPrincipalContextKey struct{}

// WorkspaceAccessPrincipal carries the token lifecycle and audit identity for
// a DTA workspace credential. Permission is loaded fresh on every request.
type WorkspaceAccessPrincipal struct {
	TokenID     string
	UserID      string
	WorkspaceID string
	Name        string
	Permission  string
	Version     int32
}

func WithWorkspaceAccessPrincipal(ctx context.Context, principal WorkspaceAccessPrincipal) context.Context {
	return context.WithValue(ctx, workspaceAccessPrincipalContextKey{}, principal)
}

func WorkspaceAccessPrincipalFromContext(ctx context.Context) (WorkspaceAccessPrincipal, bool) {
	principal, ok := ctx.Value(workspaceAccessPrincipalContextKey{}).(WorkspaceAccessPrincipal)
	return principal, ok
}

const (
	WorkspaceAccessAll       = "all"
	WorkspaceAccessDSHConfig = "dsh_config"
)

func ValidWorkspaceAccessPermission(permission string) bool {
	return permission == WorkspaceAccessAll || permission == WorkspaceAccessDSHConfig
}

// IsDSHConfigurationRequest matches the public contract by exact method/path.
func IsDSHConfigurationRequest(r *http.Request) bool {
	p := strings.Split(strings.TrimSuffix(r.URL.Path, "/"), "/")
	if len(p) == 4 && p[1] == "api" && p[2] == "dsh-plugins" && p[3] == "upload" {
		return r.Method == http.MethodPost
	}
	if len(p) != 5 || p[1] != "api" || p[2] != "agents" || p[3] == "" {
		return false
	}
	switch p[4] {
	case "dsh-plugins":
		return r.Method == http.MethodGet || r.Method == http.MethodPut
	case "dsh-profile":
		return r.Method == http.MethodGet || r.Method == http.MethodPost
	}
	return false
}
func WorkspaceAccessRequestAllowed(permission string, r *http.Request) bool {
	if isWorkspaceFilesystemRequest(r) {
		return false
	}
	if permission == WorkspaceAccessAll {
		return true
	}
	if permission != WorkspaceAccessDSHConfig {
		return false
	}
	return (r.Method == http.MethodGet && r.URL.Path == "/api/workspace-access/self") || IsDSHConfigurationRequest(r)
}

func isWorkspaceFilesystemRequest(r *http.Request) bool {
	return strings.HasPrefix(r.URL.Path, "/api/filesystem")
}

// WorkspaceAccessMember supplies request-local authority without turning the
// service subject into a persistent owner. Auth restricts dsh_config routes first.
func WorkspaceAccessMember(ctx context.Context, member db.Member) db.Member {
	p, ok := WorkspaceAccessPrincipalFromContext(ctx)
	if ok && ValidWorkspaceAccessPermission(p.Permission) && uuidToString(member.WorkspaceID) == p.WorkspaceID && uuidToString(member.UserID) == p.UserID {
		member.Role = "owner"
	}
	return member
}
