package a2aintegration

import "context"

// Principal identifies the authenticated external caller and its target endpoint.
// IDs are opaque to the protocol layer and are intentionally carried as strings.
type Principal struct {
	WorkspaceID     string
	AgentID         string
	EndpointID      string
	PublicAgentID   string
	ClientID        string
	CredentialID    string
	Scopes          []string
	EndpointEnabled bool
}

type principalContextKey struct{}

// WithPrincipal attaches an authenticated A2A principal to a request context.
func WithPrincipal(ctx context.Context, principal Principal) context.Context {
	principal.Scopes = append([]string(nil), principal.Scopes...)
	return context.WithValue(ctx, principalContextKey{}, principal)
}

// PrincipalFromContext returns the authenticated A2A principal, when present.
func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	principal, ok := ctx.Value(principalContextKey{}).(Principal)
	return principal, ok
}
