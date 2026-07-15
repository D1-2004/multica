package managedagent

import (
	"testing"
	"time"
)

func TestConfigValidateAcceptsOnlyPublicGiteeRepository(t *testing.T) {
	valid := Config{
		SourceKey: DefaultSourceKey, RepositoryURL: "https://gitee.com/acme/fde-agent.git",
		Ref: "main", SyncInterval: 30 * time.Minute, BatchSize: 50,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	for _, raw := range []string{
		"http://gitee.com/acme/fde-agent.git",
		"https://github.com/acme/fde-agent.git",
		"https://token@gitee.com/acme/fde-agent.git",
		"https://gitee.com:444/acme/fde-agent.git",
		"https://gitee.com/acme",
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
