package handler

import "testing"

func TestApplyProviderSessionContractDSHStartsFresh(t *testing.T) {
	resp := AgentTaskResponse{PriorSessionID: "ses_previous"}
	applyProviderSessionContract(&resp, " DSH ")
	if resp.PriorSessionID != "" {
		t.Fatalf("DSH PriorSessionID = %q, want empty", resp.PriorSessionID)
	}
}

func TestApplyProviderSessionContractKeepsNativeResumeForOtherProviders(t *testing.T) {
	for _, provider := range []string{"hermes", "opencode", "pi", "opencode-v2"} {
		resp := AgentTaskResponse{PriorSessionID: "ses_previous"}
		applyProviderSessionContract(&resp, provider)
		if resp.PriorSessionID != "ses_previous" {
			t.Fatalf("%s PriorSessionID = %q, want native resume preserved", provider, resp.PriorSessionID)
		}
	}
}
