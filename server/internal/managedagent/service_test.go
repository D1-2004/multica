package managedagent

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/agentsource"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
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

func TestSkillConfigUsesGitHubAgentSourceWireContract(t *testing.T) {
	config := Config{
		SourceKey: "fde-agent", RepositoryURL: "https://gitee.com/keeperqaq/fde-agent.git",
		Ref: "master",
	}
	encoded, err := json.Marshal(skillConfig(
		config,
		"0123456789abcdef0123456789abcdef01234567",
		"agent/skills/multica-development-manager",
	))
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"origin":{"commit_sha":"0123456789abcdef0123456789abcdef01234567","path":"agent/skills/multica-development-manager","ref":"master","repository":"keeperqaq/fde-agent","type":"github_agent_source"}}`
	if string(encoded) != want {
		t.Fatalf("skill config = %s, want %s", encoded, want)
	}
}

func TestManagedSourceIdentityUsesManagedSourceKey(t *testing.T) {
	source := db.AgentSource{
		SourceType:       "github",
		ManagedSourceKey: pgtype.Text{String: "fde-agent", Valid: true},
	}
	if !isManagedSource(source, "fde-agent") {
		t.Fatal("github source with matching managed_source_key should be platform managed")
	}
	source.ManagedSourceKey.String = "other"
	if isManagedSource(source, "fde-agent") {
		t.Fatal("different managed_source_key should not be reconciled")
	}
	source.SourceType = "managed_git"
	source.ManagedSourceKey.String = "fde-agent"
	if !isManagedSource(source, "fde-agent") {
		t.Fatal("platform source identity should remain readable during source type migration")
	}
}

func TestLegacyManagedSkillPath(t *testing.T) {
	if got := legacyManagedSkillPath("agent/skills/multica-development-manager"); got != "skills/multica-development-manager" {
		t.Fatalf("legacy path = %q", got)
	}
	if got := legacyManagedSkillPath("skills/multica-development-manager"); got != "" {
		t.Fatalf("non-DTA path produced fallback = %q", got)
	}
}

func TestManagedSnapshotRequiresDTAProjectBundle(t *testing.T) {
	valid := agentsource.Bundle{Skills: []agentsource.Skill{
		{Name: agentsource.DTABasicSkill, SourcePath: "agent/skills/dta-basic-behavior"},
		{Name: "multica-development-manager", SourcePath: "agent/skills/multica-development-manager"},
	}}
	if !isManagedDTAProjectBundle(valid) {
		t.Fatal("DTA Project bundle should be accepted")
	}
	valid.Skills[1].SourcePath = "skills/multica-development-manager"
	if isManagedDTAProjectBundle(valid) {
		t.Fatal("legacy YAML bundle should not be used for new provisioning")
	}
}
