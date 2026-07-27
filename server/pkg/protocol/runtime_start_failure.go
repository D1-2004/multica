package protocol

// RuntimeStartFailureReport is sent by an FC/E2B runner that exits before the
// daemon can claim its task. The launch lease token fences delayed runners from
// a previous launch generation; stage/code are a closed vocabulary so no
// runner-controlled error text reaches the task record or chat transcript.
type RuntimeStartFailureReport struct {
	LaunchLeaseToken string `json:"launch_lease_token"`
	Stage            string `json:"stage"`
	Code             string `json:"code"`
}

type RuntimeStartFailureReportResponse struct {
	Status   string `json:"status"`
	Accepted bool   `json:"accepted"`
}

const (
	RuntimeStartFailureStageValidation             = "validation"
	RuntimeStartFailureStageDWSAuthValidation      = "dws_auth_validation"
	RuntimeStartFailureStageProviderProxyConfigure = "provider_proxy_configure"
	RuntimeStartFailureStageDWSSkillSetup          = "dws_skill_setup"
	RuntimeStartFailureStageAgentIdentityRedeem    = "agent_identity_redeem"
	RuntimeStartFailureStageDWSAuthExchange        = "dws_auth_exchange"

	RuntimeStartFailureCodeRunnerValidationFailed             = "runner_validation_failed"
	RuntimeStartFailureCodeDWSAuthValidationFailed            = "dws_auth_validation_failed"
	RuntimeStartFailureCodeProviderProxyConfigureFailed       = "provider_proxy_configure_failed"
	RuntimeStartFailureCodeDWSSkillSetupFailed                = "dws_skill_setup_failed"
	RuntimeStartFailureCodeAgentIdentityRedeemFailed          = "agent_identity_redeem_failed"
	RuntimeStartFailureCodeAgentIdentityRedeemTimeout         = "agent_identity_redeem_timeout"
	RuntimeStartFailureCodeAgentIdentityRedeemNetworkError    = "agent_identity_redeem_network_error"
	RuntimeStartFailureCodeAgentIdentityRedeemHTTPError       = "agent_identity_redeem_http_error"
	RuntimeStartFailureCodeAgentIdentityRedeemInvalidResponse = "agent_identity_redeem_invalid_response"
	RuntimeStartFailureCodeDWSAuthExchangeFailed              = "dws_auth_exchange_failed"
)

type runtimeStartFailureDefinition struct {
	stage   string
	message string
}

var runtimeStartFailureDefinitions = map[string]runtimeStartFailureDefinition{
	RuntimeStartFailureCodeRunnerValidationFailed: {
		stage:   RuntimeStartFailureStageValidation,
		message: "runner environment validation failed",
	},
	RuntimeStartFailureCodeDWSAuthValidationFailed: {
		stage:   RuntimeStartFailureStageDWSAuthValidation,
		message: "DWS authentication input validation failed",
	},
	RuntimeStartFailureCodeProviderProxyConfigureFailed: {
		stage:   RuntimeStartFailureStageProviderProxyConfigure,
		message: "provider proxy configuration failed",
	},
	RuntimeStartFailureCodeDWSSkillSetupFailed: {
		stage:   RuntimeStartFailureStageDWSSkillSetup,
		message: "DWS skill setup failed",
	},
	RuntimeStartFailureCodeAgentIdentityRedeemFailed: {
		stage:   RuntimeStartFailureStageAgentIdentityRedeem,
		message: "Agent Identity credential redemption failed",
	},
	RuntimeStartFailureCodeAgentIdentityRedeemTimeout: {
		stage:   RuntimeStartFailureStageAgentIdentityRedeem,
		message: "Agent Identity credential redemption timed out",
	},
	RuntimeStartFailureCodeAgentIdentityRedeemNetworkError: {
		stage:   RuntimeStartFailureStageAgentIdentityRedeem,
		message: "Agent Identity credential redemption could not reach the service",
	},
	RuntimeStartFailureCodeAgentIdentityRedeemHTTPError: {
		stage:   RuntimeStartFailureStageAgentIdentityRedeem,
		message: "Agent Identity credential redemption was rejected by the service",
	},
	RuntimeStartFailureCodeAgentIdentityRedeemInvalidResponse: {
		stage:   RuntimeStartFailureStageAgentIdentityRedeem,
		message: "Agent Identity credential redemption returned an invalid response",
	},
	RuntimeStartFailureCodeDWSAuthExchangeFailed: {
		stage:   RuntimeStartFailureStageDWSAuthExchange,
		message: "DWS AuthCode exchange failed",
	},
}

// RuntimeStartFailureMessage validates a fixed stage/code pair and returns the
// server-owned reader-facing message for it.
func RuntimeStartFailureMessage(stage, code string) (string, bool) {
	definition, ok := runtimeStartFailureDefinitions[code]
	if !ok || definition.stage != stage {
		return "", false
	}
	return definition.message, true
}
