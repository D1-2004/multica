package migrations

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestInboundCoordinatorJobMigrationContract(t *testing.T) {
	t.Parallel()
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve migration test path")
	}
	dir := filepath.Clean(filepath.Join(filepath.Dir(current), "..", "..", "migrations"))
	read := func(name string) string {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		return string(raw)
	}

	table := read("9114_inbound_coordinator_job.up.sql")
	for _, required := range []string{
		"CREATE TABLE IF NOT EXISTS inbound_coordinator_job",
		"acceptance_id UUID NOT NULL",
		"command JSONB NOT NULL",
		"status IN ('pending', 'running', 'completed', 'failed')",
		"lease_expires_at TIMESTAMPTZ",
	} {
		if !strings.Contains(table, required) {
			t.Errorf("job migration missing %q", required)
		}
	}
	if strings.Contains(table, "REFERENCES ") || strings.Contains(table, "CREATE INDEX") {
		t.Fatal("job table migration must not add foreign keys or non-concurrent indexes")
	}

	for _, name := range []string{
		"9115_inbound_coordinator_job_acceptance_idx.up.sql",
		"9116_inbound_coordinator_job_claim_idx.up.sql",
		"9117_inbound_coordinator_job_chat_session_idx.up.sql",
		"9118_inbound_coordinator_job_id_idx.up.sql",
	} {
		index := read(name)
		if !strings.Contains(index, "INDEX CONCURRENTLY IF NOT EXISTS") {
			t.Errorf("%s must build its index concurrently", name)
		}
	}
}
