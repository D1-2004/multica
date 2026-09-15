package gitrepo

import "testing"

func TestParseAddressInfersProviderAndCanonicalRepository(t *testing.T) {
	for _, tt := range []struct { input, provider, repository, canonical, ref, directory string }{
		{"https://github.com/acme/agent.git", "github", "acme/agent", "https://github.com/acme/agent", "", ""},
		{"git@github.com:acme/agent.git", "github", "acme/agent", "https://github.com/acme/agent", "", ""},
		{"https://code.alibaba-inc.com/team/sub/agent", "alibaba_code", "team/sub/agent", "https://code.alibaba-inc.com/team/sub/agent", "", ""},
		{"git@gitlab.alibaba-inc.com:team/agent.git", "alibaba_code", "team/agent", "https://code.alibaba-inc.com/team/agent", "", ""},
		{"ssh://git@code.aone.alibaba-inc.com/team/agent.git", "alibaba_code", "team/agent", "https://code.alibaba-inc.com/team/agent", "", ""},
	} {
		t.Run(tt.input, func(t *testing.T) {
			got, err := ParseAddress(tt.input)
			if err != nil { t.Fatal(err) }
			if got.Provider != tt.provider || got.Repository != tt.repository || got.URL != tt.canonical { t.Fatalf("unexpected address: %#v", got) }
		})
	}
}

func TestParseAddressRejectsUntrustedOrAmbiguousAddresses(t *testing.T) {
	for _, input := range []string{"acme/agent", "https://github.com.evil.test/acme/agent", "https://user:secret@github.com/acme/agent", "https://code.alibaba-inc.com/team/../agent", "https://code.alibaba-inc.com/team/%2e%2e/agent", "https://code.alibaba-inc.com/team/agent?token=secret", "http://code.alibaba-inc.com/team/agent", "https://github.com:8443/acme/agent", "file:///tmp/agent", "ssh://bad@github.com/acme/agent"} {
		if _, err := ParseAddress(input); err == nil { t.Errorf("accepted %q", input) }
	}
}
