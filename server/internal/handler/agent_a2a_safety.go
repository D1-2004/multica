package handler

import (
	"log/slog"
	"net"
	"net/url"
	"os"
	"strings"
)

const (
	agentA2AAllowUnsafeLocalRuntimeEnv      = "MULTICA_A2A_ALLOW_UNSAFE_LOCAL_RUNTIME"
	agentA2AAllowUnsafePrereleaseRuntimeEnv = "MULTICA_A2A_ALLOW_UNSAFE_PRERELEASE_RUNTIME"
	agentA2AUnsafePrereleasePublicURLEnv    = "MULTICA_A2A_UNSAFE_PRERELEASE_PUBLIC_URL"
	agentA2AUnsafeRuntimeForbidden          = "A2A inbound cannot be enabled because runtime credential and tool-shell isolation are not verified; explicitly unsafe local or prerelease E2E may opt in outside production with the corresponding runtime override and URL safety checks"
)

type agentA2ARuntimeSafetyMode string

const (
	agentA2ARuntimeSafetyModeDenied           agentA2ARuntimeSafetyMode = "denied"
	agentA2ARuntimeSafetyModeUnsafeLocal      agentA2ARuntimeSafetyMode = "unsafe_local"
	agentA2ARuntimeSafetyModeUnsafePrerelease agentA2ARuntimeSafetyMode = "unsafe_prerelease"
)

type agentA2ARuntimeSafetyDecision struct {
	Allowed       bool
	Mode          agentA2ARuntimeSafetyMode
	PublicBaseURL string
}

type agentA2AEnvironmentMarkers struct {
	Production bool
	Prerelease bool
}

// evaluateAgentA2ARuntimeSafety centralizes the temporary fail-closed gate for
// hosted inbound A2A. The current runtime launch contract does not yet expose
// an artifact proving that provider credentials and the tool shell are
// isolated from caller-controlled work. Until it does, only explicitly opted
// in, non-production local or prerelease E2E is allowed to execute. The local
// mode remains loopback-only. The prerelease mode additionally pins the
// configured public URL to an explicit HTTPS expectation.
//
// PublicBaseURL is still returned for a syntactically valid deployment URL so
// the owner-only management response can render/export the configured card
// while reporting the endpoint as effectively disabled.
func evaluateAgentA2ARuntimeSafety(publicURL string) agentA2ARuntimeSafetyDecision {
	decision := agentA2ARuntimeSafetyDecision{Mode: agentA2ARuntimeSafetyModeDenied}
	baseURL, err := normalizeAgentA2APublicBaseURL(publicURL)
	if err != nil {
		return decision
	}

	decision.PublicBaseURL = baseURL
	// Production is an unconditional deny across every deployment marker. A
	// simultaneous prerelease marker is a conflict and therefore also lands in
	// this branch; no precedence rule may weaken a production signal.
	environment := currentAgentA2AEnvironmentMarkers()
	if environment.Production {
		return decision
	}

	host, ok := agentA2APublicURLHost(baseURL)
	if ok && isAgentA2AUnsafeOverrideEnabled(agentA2AAllowUnsafeLocalRuntimeEnv) && isExactAgentA2ALocalE2EHost(host) {
		decision.Allowed = true
		decision.Mode = agentA2ARuntimeSafetyModeUnsafeLocal
		return decision
	}

	if environment.Prerelease && isAgentA2AUnsafePrereleaseRuntimeAllowed(baseURL, host, ok) {
		decision.Allowed = true
		decision.Mode = agentA2ARuntimeSafetyModeUnsafePrerelease
	}
	return decision
}

func currentAgentA2AEnvironmentMarkers() agentA2AEnvironmentMarkers {
	markers := agentA2AEnvironmentMarkers{}
	// Inspect every trusted marker rather than applying precedence. Aone injects
	// AONE_ENV_TYPE from its Docker ENV_TYPE build argument, while the remaining
	// names cover runtime conventions used by other deployment shapes.
	for _, name := range []string{"AONE_ENV_TYPE", "ENV_TYPE", "GO_ENV", "APP_ENV"} {
		switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
		case "prod", "production", "online":
			markers.Production = true
		case "pre", "prepub", "prepublish", "staging", "stage":
			markers.Prerelease = true
		}
	}
	return markers
}

// evaluateAgentA2ARequestSafety adds the mode-specific socket-peer requirement
// used by public Card/RPC routes. Local mode is only for clients connecting
// directly over loopback: forwarding headers are intentionally ignored because
// a reverse proxy would collapse an untrusted caller into a trusted peer.
// Prerelease mode expects a load balancer and therefore does not constrain the
// socket peer after its explicit HTTPS public URL pin has passed.
func evaluateAgentA2ARequestSafety(publicURL, remoteAddr string) agentA2ARuntimeSafetyDecision {
	decision := evaluateAgentA2ARuntimeSafety(publicURL)
	if decision.Allowed && decision.Mode == agentA2ARuntimeSafetyModeUnsafeLocal && !isExactAgentA2ALoopbackPeer(remoteAddr) {
		decision.Allowed = false
	}
	return decision
}

func isAgentA2AUnsafeOverrideEnabled(name string) bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv(name)), "true")
}

func isAgentA2AUnsafePrereleaseRuntimeAllowed(baseURL, host string, validHost bool) bool {
	if !validHost || !isAgentA2AUnsafeOverrideEnabled(agentA2AAllowUnsafePrereleaseRuntimeEnv) {
		return false
	}
	expectedBaseURL, err := normalizeAgentA2APublicBaseURL(os.Getenv(agentA2AUnsafePrereleasePublicURLEnv))
	if err != nil || expectedBaseURL != baseURL {
		return false
	}
	parsed, err := url.Parse(baseURL)
	return err == nil && parsed.Scheme == "https" && !isAgentA2ALoopbackHostForPrerelease(host)
}

func isAgentA2ALoopbackHostForPrerelease(host string) bool {
	normalizedHost := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if normalizedHost == "localhost" || strings.HasSuffix(normalizedHost, ".localhost") {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
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

// warnAgentA2AUnsafeRuntimeAccepted records only the selected safety mode and
// stable identifiers. Raw credentials and JSON-RPC bodies must never be
// attached to this warning.
func warnAgentA2AUnsafeRuntimeAccepted(mode agentA2ARuntimeSafetyMode, operation, workspaceID, agentID, publicAgentID, clientID string) {
	slog.Warn("unsafe A2A runtime override accepted",
		"safety_mode", mode,
		"operation", operation,
		"workspace_id", workspaceID,
		"agent_id", agentID,
		"public_agent_id", publicAgentID,
		"client_id", clientID,
	)
}
