package migrations

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAoneStartupRunsDatabaseMigrationsBeforeProcesses(t *testing.T) {
	repoRoot := filepath.Clean(filepath.Join(realMigrationsDir(t), "..", ".."))
	path := filepath.Join(repoRoot, "src", "main.sh")
	bodyBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	body := string(bodyBytes)
	stopIndex := strings.LastIndex(body, "\nstop_existing_processes\n")
	migrateIndex := strings.LastIndex(body, "\n\"$APP_ROOT/bin/migrate\" up\n")
	startIndex := strings.LastIndex(body, "\nstart_processes\n")
	if stopIndex < 0 || migrateIndex < 0 || startIndex < 0 {
		t.Fatalf("Aone startup script %s must stop old processes, run migrations, and start new processes", path)
	}
	if !(stopIndex < migrateIndex && migrateIndex < startIndex) {
		t.Fatalf("Aone startup script %s must run migrations after stopping old processes and before starting new processes", path)
	}
}

func TestDockerEntrypointDoesNotRunDatabaseMigrations(t *testing.T) {
	repoRoot := filepath.Clean(filepath.Join(realMigrationsDir(t), "..", ".."))
	path := filepath.Join(repoRoot, "docker", "entrypoint.sh")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(string(body)), "migrate") {
		t.Fatalf("generic container startup script %s must not run database migrations", path)
	}
}
