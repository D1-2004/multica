package protocol

import "testing"

func TestRuntimeStartFailureMessageRequiresAllowedStageCodePair(t *testing.T) {
	message, ok := RuntimeStartFailureMessage(
		RuntimeStartFailureStageAgentIdentityRedeem,
		RuntimeStartFailureCodeAgentIdentityRedeemTimeout,
	)
	if !ok {
		t.Fatal("expected allowed Agent Identity timeout pair")
	}
	if message != "Agent Identity credential redemption timed out" {
		t.Fatalf("message = %q", message)
	}

	for _, test := range []struct {
		name  string
		stage string
		code  string
	}{
		{
			name:  "unknown stage",
			stage: "arbitrary",
			code:  RuntimeStartFailureCodeAgentIdentityRedeemTimeout,
		},
		{
			name:  "mismatched stage",
			stage: RuntimeStartFailureStageDWSAuthExchange,
			code:  RuntimeStartFailureCodeAgentIdentityRedeemTimeout,
		},
		{
			name:  "unknown code",
			stage: RuntimeStartFailureStageAgentIdentityRedeem,
			code:  "arbitrary",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if message, ok := RuntimeStartFailureMessage(test.stage, test.code); ok || message != "" {
				t.Fatalf("RuntimeStartFailureMessage(%q, %q) = %q, %v; want rejected", test.stage, test.code, message, ok)
			}
		})
	}
}
