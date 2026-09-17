package handler

import "testing"

func TestGitHubSettingsURL(t *testing.T) {
	for _, tt := range []struct {
		name     string
		returnTo string
		want     string
	}{
		{"identity", githubReturnToGitHub, "https://example.com/acme/settings?tab=repositories&section=connections"},
		{"repository picker", githubReturnToRepositories, "https://example.com/acme/settings?tab=repositories"},
		{"invalid destination", "https://untrusted.example", "https://example.com/acme/settings?tab=repositories&section=connections"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := githubSettingsURL("https://example.com/acme/", tt.returnTo); got != tt.want {
				t.Fatalf("githubSettingsURL() = %q, want %q", got, tt.want)
			}
		})
	}
}
