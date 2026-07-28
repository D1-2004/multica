package protocol

const (
	// SandboxRelayTokenHeader carries the signed routing assertion used by a
	// pre-release sandbox when it reaches Multica through the production
	// ingress. The production relay removes it before forwarding upstream.
	SandboxRelayTokenHeader = "X-Multica-Sandbox-Relay-Token"

	// SandboxRelayTokenEnvKey is injected only by a Multica deployment that
	// has a sandbox relay signing key. Production launches omit it.
	SandboxRelayTokenEnvKey = "MULTICA_SANDBOX_RELAY_TOKEN"
)
