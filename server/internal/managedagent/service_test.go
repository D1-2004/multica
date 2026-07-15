package managedagent

import (
	"testing"
	"time"
)

func TestConfigFromEnvUsesFixedRepositoryByDefault(t *testing.T) {
	t.Setenv("MULTICA_FDE_AGENT_REPOSITORY_URL", "")
	config := ConfigFromEnv()
	if config.RepositoryURL != DefaultRepositoryURL {
		t.Fatalf("repository URL = %q, want %q", config.RepositoryURL, DefaultRepositoryURL)
	}
}

func TestConfigValidateAcceptsCredentialFreePublicHTTPSRepository(t *testing.T) {
	valid := Config{
		SourceKey: DefaultSourceKey, RepositoryURL: "https://gitee.com/acme/fde-agent.git",
		Ref: "main", SyncInterval: 30 * time.Minute, BatchSize: 50,
	}
	for _, raw := range []string{
		"https://gitee.com/acme/fde-agent.git",
		"https://github.com/acme/fde-agent.git",
		"https://gitlab.example.com/fde-agent.git",
		"https://git.example.com:8443/team/fde-agent.git",
	} {
		candidate := valid
		candidate.RepositoryURL = raw
		if err := candidate.Validate(); err != nil {
			t.Errorf("valid public HTTPS repository rejected: %s: %v", raw, err)
		}
	}
	for _, raw := range []string{
		"http://gitee.com/acme/fde-agent.git",
		"https://token@gitee.com/acme/fde-agent.git",
		"https:///acme/fde-agent.git",
		"https://gitee.com/acme/fde-agent.git?token=secret",
		"https://gitee.com/acme/../fde-agent",
	} {
		candidate := valid
		candidate.RepositoryURL = raw
		if err := candidate.Validate(); err == nil {
			t.Errorf("unsafe repository URL accepted: %s", raw)
		}
	}
}

func TestProviderAllowed(t *testing.T) {
	if !providerAllowed(nil, "hermes") || !providerAllowed([]string{"hermes"}, "hermes") {
		t.Fatal("hermes should be allowed")
	}
	if providerAllowed([]string{"codex"}, "hermes") {
		t.Fatal("fixed FC provider should honor manifest compatibility")
	}
}

func TestRepositoryParts(t *testing.T) {
	owner, repo := repositoryParts("https://gitee.com/acme/fde-agent.git")
	if owner != "acme" || repo != "fde-agent" {
		t.Fatalf("repositoryParts = %q/%q", owner, repo)
	}
}
