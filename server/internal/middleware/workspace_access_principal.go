package middleware

import (
	"context"
)

const WorkspaceAccessActorSource = "workspace_access_token"

type workspaceAccessPrincipalContextKey struct{}

// WorkspaceAccessPrincipal carries the token lifecycle and audit identity for
// a DTA workspace credential. Business authorization is provided by the
// subject's native workspace member row, not by a parallel capability policy.
type WorkspaceAccessPrincipal struct {
	TokenID     string
	UserID      string
	WorkspaceID string
	Name        string
	Version     int32
}

func WithWorkspaceAccessPrincipal(ctx context.Context, principal WorkspaceAccessPrincipal) context.Context {
	return context.WithValue(ctx, workspaceAccessPrincipalContextKey{}, principal)
}

func WorkspaceAccessPrincipalFromContext(ctx context.Context) (WorkspaceAccessPrincipal, bool) {
	principal, ok := ctx.Value(workspaceAccessPrincipalContextKey{}).(WorkspaceAccessPrincipal)
	return principal, ok
}
