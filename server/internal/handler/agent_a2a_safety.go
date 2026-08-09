package handler

import (
	"log/slog"
	"net"
	"net/url"
	"os"
	"strings"
)

const (
	agentA2AAllowUnsafeLocalRuntimeEnv = "MULTICA_A2A_ALLOW_UNSAFE_LOCAL_RUNTIME"
	agentA2AUnsafeRuntimeForbidden     = "A2A inbound cannot be enabled because runtime credential and tool-shell isolation are not verified; trusted local E2E may opt in with MULTICA_A2A_ALLOW_UNSAFE_LOCAL_RUNTIME=true and a loopback MULTICA_PUBLIC_URL outside production"
)

type agentA2ARuntimeSafetyDecision struct {
	Allowed       bool
	PublicBaseURL string
}

// evaluateAgentA2ARuntimeSafety centralizes the temporary fail-closed gate for
// hosted inbound A2A. The current runtime launch contract does not yet expose
// an artifact proving that provider credentials and the tool shell are
// isolated from caller-controlled work. Until it does, only explicitly opted
// in, non-production, loopback-only local E2E is allowed to execute.
//
// PublicBaseURL is still returned for a syntactically valid deployment URL so
// the owner-only management response can render/export the configured card
// while reporting the endpoint as effectively disabled.
func evaluateAgentA2ARuntimeSafety(publicURL string) agentA2ARuntimeSafetyDecision {
	baseURL, err := normalizeAgentA2APublicBaseURL(publicURL)
	if err != nil {
		return agentA2ARuntimeSafetyDecision{}
	}

	decision := agentA2ARuntimeSafetyDecision{PublicBaseURL: baseURL}
	if isProductionEnv() || !strings.EqualFold(strings.TrimSpace(os.Getenv(agentA2AAllowUnsafeLocalRuntimeEnv)), "true") {
		return decision
	}

	host, ok := agentA2APublicURLHost(baseURL)
	if !ok || !isExactAgentA2ALocalE2EHost(host) {
		return decision
	}
	decision.Allowed = true
	return decision
}

// evaluateAgentA2ALocalRequestSafety adds the socket-peer requirement used by
// public Card/RPC routes. The unsafe override is only for clients connecting
// directly over loopback: forwarding headers are intentionally ignored because
// a reverse proxy would collapse an untrusted caller into a trusted peer.
func evaluateAgentA2ALocalRequestSafety(publicURL, remoteAddr string) agentA2ARuntimeSafetyDecision {
	decision := evaluateAgentA2ARuntimeSafety(publicURL)
	if !decision.Allowed || !isExactAgentA2ALoopbackPeer(remoteAddr) {
		decision.Allowed = false
	}
	return decision
}

func agentA2APublicURLHost(baseURL string) (string, bool) {
	// normalizeAgentA2APublicBaseURL already parsed and validated baseURL. Keep
	// this helper deliberately small and avoid accepting a host from request
	// headers or other ambient input.
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Hostname() == "" {
		return "", false
	}
	return parsed.Hostname(), true
}

func isExactAgentA2ALocalE2EHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	return isExactAgentA2ALoopbackIP(host)
}

func isExactAgentA2ALoopbackPeer(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(strings.TrimSpace(remoteAddr))
	return err == nil && isExactAgentA2ALoopbackIP(host)
}

func isExactAgentA2ALoopbackIP(host string) bool {
	if host == "::1" {
		return true
	}
	// A colon here is an IPv6 literal. Reject IPv4-mapped IPv6 such as
	// ::ffff:127.0.0.1; the exemption intentionally allows only exact ::1.
	if strings.Contains(host, ":") {
		return false
	}
	address := net.ParseIP(host)
	if address == nil {
		return false
	}
	ipv4 := address.To4()
	return ipv4 != nil && ipv4[0] == 127
}

// warnAgentA2AUnsafeLocalRuntimeAccepted records only stable identifiers. Raw
// credentials and JSON-RPC bodies must never be attached to this warning.
func warnAgentA2AUnsafeLocalRuntimeAccepted(operation, workspaceID, agentID, publicAgentID, clientID string) {
	slog.Warn("unsafe local A2A runtime override accepted",
		"operation", operation,
		"workspace_id", workspaceID,
		"agent_id", agentID,
		"public_agent_id", publicAgentID,
		"client_id", clientID,
	)
}
