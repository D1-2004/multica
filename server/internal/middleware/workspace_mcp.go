package middleware

import (
	"context"
	"strings"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type workspaceMCPContextKey int

const (
	workspaceMCPPrincipalKey workspaceMCPContextKey = iota
	workspaceMCPDispatchKey
)

func WithWorkspaceMCPPrincipal(ctx context.Context, token db.WorkspaceMCPToken) context.Context {
	return context.WithValue(ctx, workspaceMCPPrincipalKey, token)
}

func WorkspaceMCPPrincipalFromContext(ctx context.Context) (db.WorkspaceMCPToken, bool) {
	token, ok := ctx.Value(workspaceMCPPrincipalKey).(db.WorkspaceMCPToken)
	return token, ok
}

// WithWorkspaceMCPDispatch marks a server-created, allowlisted API subrequest.
// The marker cannot be supplied as an HTTP header by a remote client.
func WithWorkspaceMCPDispatch(ctx context.Context) context.Context {
	return context.WithValue(ctx, workspaceMCPDispatchKey, true)
}

func IsWorkspaceMCPDispatch(ctx context.Context) bool {
	allowed, _ := ctx.Value(workspaceMCPDispatchKey).(bool)
	return allowed
}

func WorkspaceMCPHasScope(scopes []string, target string) bool {
	for _, scope := range scopes {
		if strings.EqualFold(scope, target) {
			return true
		}
	}
	return false
}
