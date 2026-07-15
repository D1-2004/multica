package workspaceprovision

import "testing"

func TestGenerateIssuePrefix(t *testing.T) {
	for input, want := range map[string]string{
		"Jiayuan's Workspace": "JIA",
		"My Team":             "MYT",
		"AB":                  "AB",
		"123":                 "WS",
	} {
		if got := GenerateIssuePrefix(input); got != want {
			t.Errorf("GenerateIssuePrefix(%q) = %q, want %q", input, got, want)
		}
	}
}
