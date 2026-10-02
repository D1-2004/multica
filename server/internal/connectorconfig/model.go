// Package connectorconfig stores workspace OAuth application registrations
// and the authorization instances bound to a call context.
//
// An application (connector_app) is one pre-registered OAuth client. It has
// many authorization instances (accounts, orgs, or installations). Each
// instance has many bindings to a workspace, agent, project, or environment.
//
// Selection rank, most specific first: agent, project, environment,
// workspace. A disabled instance is skipped. An enabled instance that
// matches is the one that takes effect, even when it has no token yet —
// the call does not fall through to another account. No match keeps the
// legacy credential (environment variables and the existing scope layers).
// Dynamic client registration does not use this table.
package connectorconfig

import "strings"

const (
	CallbackSelf              = "self"
	CallbackProductionForward = "production_forward"

	ScopeWorkspace   = "workspace"
	ScopeAgent       = "agent"
	ScopeProject     = "project"
	ScopeEnvironment = "environment"

	StatusPending     = "pending"
	StatusActive      = "active"
	StatusDisabled    = "disabled"
	StatusNeedsReauth = "needs_reauth"

	SourceWorkspace = "workspace"
	SourceEnv       = "env"
)

// Priority is the selection rank, most specific first.
var Priority = []string{ScopeAgent, ScopeProject, ScopeEnvironment, ScopeWorkspace}

// Binding attaches an authorization instance to one scope target.
// Workspace bindings use an empty ScopeID.
type Binding struct {
	ScopeKind string
	ScopeID   string
}

// Instance is one authorization of an application.
type Instance struct {
	ID              string
	AppID           string
	Label           string
	ExternalSubject string
	ExternalLogin   string
	Status          string
	Enabled         bool
	Bindings        []Binding
}

// CallContext is the scope of one agent invocation.
type CallContext struct {
	AgentID     string
	ProjectID   string
	Environment string
}

// NormalizeProvider returns a catalog-style provider key.
func NormalizeProvider(raw string) (string, bool) {
	provider := strings.ToLower(strings.TrimSpace(raw))
	if len(provider) < 1 || len(provider) > 32 {
		return "", false
	}
	for i, r := range provider {
		switch {
		case r >= 'a' && r <= 'z':
		case i > 0 && (r == '-' || (r >= '0' && r <= '9')):
		default:
			return "", false
		}
	}
	return provider, true
}

// NormalizeCallbackMode accepts the two stored modes.
func NormalizeCallbackMode(raw string) (string, bool) {
	switch strings.TrimSpace(raw) {
	case "", CallbackProductionForward:
		return CallbackProductionForward, true
	case CallbackSelf:
		return CallbackSelf, true
	default:
		return "", false
	}
}

// NormalizeEnvironment folds a deployment or named environment.
func NormalizeEnvironment(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}
