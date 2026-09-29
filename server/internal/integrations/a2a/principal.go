package a2aintegration

import "context"

const (
	// AgentIdentityExtensionURI is the optional extension a caller declares
	// before supplying a task-local Agent Identity ContextToken.
	AgentIdentityExtensionURI = "urn:multica:a2a:agent-identity:v1"
	AgentIdentityTokenHeader  = "X-Multica-Agent-Identity-Context-Token"
	AgentIdentityExpiryHeader = "X-Multica-Agent-Identity-Context-Token-Expires-At"
	DEAPDWSTokenHeader        = "X-DWS-Token"
	// DingTalkEventExtensionURI is DEAP's optional message extension carrying
	// the DingTalk conversation, sender, thread and quoted-message context in
	// message.metadata[URI].
	DingTalkEventExtensionURI = "https://api-deap.dingtalk.com/a2a/extensions/dingtalk-event/v1"
)

// Principal identifies the authenticated external caller and its target endpoint.
// IDs are opaque to the protocol layer and are intentionally carried as strings.
type Principal struct {
	WorkspaceID   string
	AgentID       string
	EndpointID    string
	PublicAgentID string
	ClientID      string
	CredentialID  string
	// OwnerID is the current Agent owner/delegator. Protocol adapters only set
	// it from the credential admission row; callers can never supply it.
	OwnerID         string
	Scopes          []string
	EndpointEnabled bool
	// AllowDisabledEndpoint is set only by the authenticated MCP adapter. It
	// keeps the A2A publication switch independent from an active MCP link while
	// preserving the same owner, runtime, client and credential admission checks.
	AllowDisabledEndpoint bool
}

type principalContextKey struct{}

type invocationIdentityContextKey struct{}

// InvocationIdentity is private request metadata. It is intentionally kept
// outside the A2A Message so it cannot be copied into task history or events.
type InvocationIdentity struct {
	ExtensionDeclared bool
	ContextToken      string
	ExpiresAtUnixMS   int64
	// DEAPDWSToken is a request-scoped DWS credential supplied by DEAP. It must
	// never be copied into an A2A message, task context, event, or durable queue.
	DEAPDWSToken string
	// RequestBound permits an internal immediate SendMessage only while the
	// surrounding streaming request remains open.
	RequestBound bool
}

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

// WithInvocationIdentity attaches one request's optional external identity.
func WithInvocationIdentity(ctx context.Context, identity InvocationIdentity) context.Context {
	return context.WithValue(ctx, invocationIdentityContextKey{}, identity)
}

// InvocationIdentityFromContext returns the external identity headers for the
// current turn. Callers must never log the returned token.
func InvocationIdentityFromContext(ctx context.Context) (InvocationIdentity, bool) {
	identity, ok := ctx.Value(invocationIdentityContextKey{}).(InvocationIdentity)
	return identity, ok
}
