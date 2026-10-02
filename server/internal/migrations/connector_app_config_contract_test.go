package migrations

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestConnectorAppConfigMigrationContract(t *testing.T) {
	t.Parallel()
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve migration test path")
	}
	dir := filepath.Clean(filepath.Join(filepath.Dir(current), "..", "..", "migrations"))
	read := func(name string) string {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		return string(raw)
	}

	table := read("9432_connector_app_config.up.sql")
	for _, required := range []string{
		"CREATE TABLE IF NOT EXISTS connector_app",
		"CREATE TABLE IF NOT EXISTS connector_auth_instance",
		"CREATE TABLE IF NOT EXISTS connector_auth_binding",
	} {
		if !strings.Contains(table, required) {
			t.Errorf("connector app migration missing %q", required)
		}
	}
	if strings.Contains(table, "REFERENCES ") || strings.Contains(table, "CREATE INDEX") || strings.Contains(table, "ON DELETE") {
		t.Fatal("connector app table migration must not add foreign keys or indexes")
	}

	drop := read("9434_connector_app_drop_foreign_keys.up.sql")
	if !strings.Contains(drop, "con.contype = 'f'") || !strings.Contains(drop, "DROP CONSTRAINT") {
		t.Fatal("connector app foreign-key cleanup must drop existing foreign keys")
	}

	for _, name := range []string{
		"9435_connector_app_workspace_provider_client_idx.up.sql",
		"9436_connector_app_workspace_provider_idx.up.sql",
		"9437_connector_auth_instance_app_idx.up.sql",
		"9438_connector_auth_binding_app_scope_idx.up.sql",
		"9439_connector_auth_binding_instance_idx.up.sql",
	} {
		index := strings.TrimSpace(read(name))
		if strings.Count(index, "CREATE ") != 1 || !strings.Contains(index, "INDEX CONCURRENTLY IF NOT EXISTS") || strings.Contains(index, ";") {
			t.Errorf("%s must be one concurrent index statement", name)
		}
	}
}
