package protocol

const (
	// SandboxRelayTokenHeader carries the signed routing assertion used by a
	// pre-release sandbox when it reaches Multica through production ingress.
	SandboxRelayTokenHeader = "X-Multica-Sandbox-Relay-Token"

	// SandboxRelayTokenEnvKey is injected only into a pre-release sandbox.
	SandboxRelayTokenEnvKey = "MULTICA_SANDBOX_RELAY_TOKEN"
)
