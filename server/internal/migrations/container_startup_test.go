package migrations

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestContainerStartupDoesNotRunDatabaseMigrations(t *testing.T) {
	repoRoot := filepath.Clean(filepath.Join(realMigrationsDir(t), "..", ".."))
	startupScripts := []string{
		filepath.Join(repoRoot, "docker", "entrypoint.sh"),
		filepath.Join(repoRoot, "src", "main.sh"),
	}

	for _, path := range startupScripts {
		path := path
		t.Run(filepath.Base(filepath.Dir(path))+"/"+filepath.Base(path), func(t *testing.T) {
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(strings.ToLower(string(body)), "migrate") {
				t.Fatalf("container startup script %s must not run database migrations", path)
			}
		})
	}
}
