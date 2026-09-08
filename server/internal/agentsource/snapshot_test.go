package agentsource

import (
	"testing"

	"github.com/multica-ai/multica/server/internal/githubapp"
)

func TestRepositoryDiffComparesExactTreesIncludingRemovalsAndModes(t *testing.T) {
	base := RepositorySnapshot{Files:map[string]string{"agent/AGENTS.md":"old", "removed.md":"removed"}, Tree:[]githubapp.TreeEntry{
		{Path:"agent/AGENTS.md", Type:"blob", Mode:"100644", SHA:"a"},
		{Path:"removed.md", Type:"blob", Mode:"100644", SHA:"b"},
		{Path:"script.sh", Type:"blob", Mode:"100644", SHA:"c"},
	}}
	target := RepositorySnapshot{Files:map[string]string{"agent/AGENTS.md":"new"}, Tree:[]githubapp.TreeEntry{
		{Path:"agent/AGENTS.md", Type:"blob", Mode:"100644", SHA:"d"},
		{Path:"script.sh", Type:"blob", Mode:"100755", SHA:"c"},
		{Path:"image.png", Type:"blob", Mode:"100644", SHA:"e"},
	}}
	changes := DiffRepository(base, target)
	if len(changes) != 4 { t.Fatalf("changes = %#v", changes) }
	if changes[0].Path != "agent/AGENTS.md" || *changes[0].Before != "old" || *changes[0].After != "new" {
		t.Fatalf("instruction diff = %#v", changes[0])
	}
	if changes[1].Status != "added" || changes[1].After != nil { t.Fatalf("binary diff = %#v", changes[1]) }
	if changes[2].Status != "deleted" || changes[2].After != nil { t.Fatalf("removal diff = %#v", changes[2]) }
	if changes[3].BeforeMode != "100644" || changes[3].AfterMode != "100755" { t.Fatalf("mode diff = %#v", changes[3]) }
}

func TestConfigurationDiffDistinguishesEmptyContentFromDeletion(t *testing.T) {
	changes := DiffFiles(map[string]string{"empty":"", "same":"kept", "deleted":"value"}, map[string]string{"empty":"value", "same":"kept", "added":""})
	if len(changes) != 3 || changes[0].After == nil || *changes[0].After != "" || changes[1].After != nil || changes[2].Before == nil {
		t.Fatalf("changes = %#v", changes)
	}
}
