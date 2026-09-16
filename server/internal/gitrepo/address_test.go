package gitrepo

import "testing"

func TestParseAddressInfersProviderAndCanonicalRepository(t *testing.T) {
	for _, tt := range []struct { input, provider, repository, canonical, ref, directory string }{
		{"https://github.com/acme/agent.git", "github", "acme/agent", "https://github.com/acme/agent", "", ""},
		{"git@github.com:acme/agent.git", "github", "acme/agent", "https://github.com/acme/agent", "", ""},
	} {
		t.Run(tt.input, func(t *testing.T) {
			got, err := ParseAddress(tt.input)
			if err != nil { t.Fatal(err) }
			if got.Provider != tt.provider || got.Repository != tt.repository || got.URL != tt.canonical { t.Fatalf("unexpected address: %#v", got) }
		})
	}
}

func TestParseAddressRejectsUntrustedOrAmbiguousAddresses(t *testing.T) {
	for _, input := range []string{"acme/agent", "https://github.com.evil.test/acme/agent", "https://user:secret@github.com/acme/agent", "https://github.com/team/../agent", "https://github.com/team/%2e%2e/agent", "https://github.com/team/agent?token=secret", "http://github.com/team/agent", "https://github.com:8443/acme/agent", "file:///tmp/agent", "ssh://bad@github.com/acme/agent"} {
		if _, err := ParseAddress(input); err == nil { t.Errorf("accepted %q", input) }
	}
}

func TestParseAddressRejectsInternalCodeHosts(t *testing.T) {
	for _, host := range []string{"code.alibaba-inc.com", "gitlab.alibaba-inc.com", "code.aone.alibaba-inc.com", "code-sc.aone.alibaba-inc.com"} {
		for _, input := range []string{"https://" + host + "/team/repo", "git@" + host + ":team/repo.git", "ssh://git@" + host + "/team/repo.git"} {
			if _, err := ParseAddress(input); err == nil {
				t.Errorf("accepted internal repository %q", input)
			}
		}
	}
}
