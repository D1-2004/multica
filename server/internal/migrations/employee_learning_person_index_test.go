package migrations

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The person-view index lands on the hot employee_learning table: it must be
// one CONCURRENTLY statement per file (PostgreSQL rejects concurrent builds in
// a multi-command string) and replay-safe under a renamed-stem re-run.
func TestMigration9872ConcurrentSingleStatement(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	read := func(name string) string {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join(root, "migrations", name))
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(raw))
	}
	up := read("9872_employee_learning_person_idx.up.sql")
	want := "CREATE INDEX CONCURRENTLY IF NOT EXISTS employee_learning_person_idx ON employee_learning (workspace_id, agent_id, tenant_org_id, principal_id, created_at DESC) WHERE scope_kind = 'private' AND forgotten_at IS NULL AND superseded_by IS NULL;"
	if up != want {
		t.Fatalf("9872 up changed:\n%s", up)
	}
	down := read("9872_employee_learning_person_idx.down.sql")
	if down != "DROP INDEX CONCURRENTLY IF EXISTS employee_learning_person_idx;" {
		t.Fatalf("9872 down changed:\n%s", down)
	}
	for _, sql := range []string{up, down} {
		if strings.Count(sql, ";") != 1 || !strings.HasSuffix(sql, ";") {
			t.Fatalf("9872 must be a single statement: %s", sql)
		}
		upper := strings.ToUpper(sql)
		for _, forbidden := range []string{"REFERENCES", "FOREIGN KEY", "CASCADE", "CREATE EXTENSION", "BEGIN"} {
			if strings.Contains(upper, forbidden) {
				t.Fatalf("9872 contains %s", forbidden)
			}
		}
	}
	matches, err := filepath.Glob(filepath.Join(root, "migrations", "9872_*.sql"))
	if err != nil || len(matches) != 2 {
		t.Fatalf("9872 prefix must be owned by exactly one stem: %v %v", matches, err)
	}
}
