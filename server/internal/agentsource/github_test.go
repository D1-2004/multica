package agentsource

import "testing"

func TestParseGitHubRepositoryAcceptsRepositoryAndBranchLinks(t *testing.T) {
	for _, test := range []struct {
		input string
		ref string
		repository string
		wantRef string
	}{
		{"https://github.com/acme/reviewer", "", "acme/reviewer", ""},
		{" https://github.com/acme/reviewer.git/ ", "main", "acme/reviewer", "main"},
		{"acme/reviewer", "release/v2", "acme/reviewer", "release/v2"},
		{"https://github.com/acme/reviewer/tree/release%2Fv2", "", "acme/reviewer", "release/v2"},
		{"https://github.com/acme/reviewer/tree/release/v2", "release/v2", "acme/reviewer", "release/v2"},
	} {
		repository, ref, err := ParseGitHubRepository(test.input, test.ref)
		if err != nil || repository != test.repository || ref != test.wantRef {
			t.Errorf("ParseGitHubRepository(%q, %q) = %q, %q, %v", test.input, test.ref, repository, ref, err)
		}
	}
}

func TestParseGitHubRepositoryRejectsUnsafeOrAmbiguousLinks(t *testing.T) {
	for _, input := range []string{
		"http://github.com/acme/reviewer", "https://github.com.evil.test/acme/reviewer",
		"https://user:password@github.com/acme/reviewer", "https://github.com:8443/acme/reviewer",
		"https://github.com/acme/reviewer?token=secret", "https://github.com/acme/reviewer#readme",
		"https://github.com/acme/reviewer/blob/main/dingtalk-agent.json",
		"acme/../reviewer", "acme", "acme/reviewer/tree/main", "https://github.com/acme/reviewer/tree/",
		"https://github.com/acme/reviewer/tree/../main", "https://github.com/acme/reviewer/tree/main%00",
	} {
		if _, _, err := ParseGitHubRepository(input, ""); err == nil {
			t.Errorf("accepted invalid repository %q", input)
		}
	}
	if _, _, err := ParseGitHubRepository("https://github.com/acme/reviewer/tree/main", "release"); err == nil {
		t.Fatal("accepted a branch link that contradicts the explicit ref")
	}
}
