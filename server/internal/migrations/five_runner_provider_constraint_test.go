package migrations

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestFiveRunnerStableTargetProviderConstraintMigration(t *testing.T) {
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve migration test path")
	}
	dir := filepath.Clean(filepath.Join(filepath.Dir(current), "..", "..", "migrations"))
	read := func(name string) string {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}

	up := read("9063_fc_e2b_stable_release_target_five_providers.up.sql")
	for _, provider := range []string{"'hermes'", "'opencode'", "'pi'", "'dsh'", "'opencode-v2'"} {
		if !strings.Contains(up, provider) {
			t.Errorf("up migration is missing provider %s", provider)
		}
	}
	if !strings.Contains(up, "VALIDATE CONSTRAINT fc_e2b_stable_release_target_provider_check") {
		t.Fatal("up migration must validate the five-provider constraint")
	}

	down := read("9063_fc_e2b_stable_release_target_five_providers.down.sql")
	if !strings.Contains(down, "NOT VALID") {
		t.Fatal("down migration must preserve existing five-provider rows with NOT VALID")
	}
}
