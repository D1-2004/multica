package agenttemplate

import (
	"context"
	"strings"
	"testing"
)

func TestDefaultSeedContainsCompleteOfflineBundle(t *testing.T) {
	seed, err := LoadDefaultSeed(context.Background())
	if err != nil {
		t.Fatalf("LoadDefaultSeed: %v", err)
	}
	if seed.SystemKey != DefaultSystemKey || seed.Slug != DefaultSlug || seed.ReleaseVersion != 2 {
		t.Fatalf("unexpected seed metadata: %#v", seed)
	}
	if len(seed.Bundle.Skills) != 2 {
		t.Fatalf("skills = %d, want 2", len(seed.Bundle.Skills))
	}
	foundFactoryHelper := false
	for _, skill := range seed.Bundle.Skills {
		for _, file := range skill.Files {
			if skill.SourcePath == "skills/multica-agent-factory" && file.Path == "scripts/multica_factory_api.py" {
				foundFactoryHelper = true
				for _, forbidden := range []string{"git-agent-templates", "local_cli_snapshot", "/api/agents/from-template"} {
					if strings.Contains(file.Content, forbidden) {
						t.Fatalf("factory helper still contains legacy path %q", forbidden)
					}
				}
			}
		}
	}
	if !foundFactoryHelper {
		t.Fatal("factory helper was not materialized into the seed bundle")
	}
}
