package migrations

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestDeploymentFenceStateRefreshReadsAuthoritativeRow(t *testing.T) {
	dir := filepath.Join("..", "..", "migrations")
	up := readSingleMigrationForTest(t, dir, "9052_deployment_fence_authoritative_state_refresh.up.sql")
	normalized := strings.ToLower(up)

	for _, required := range []string{
		"pg_advisory_xact_lock_shared",
		"from deployment_fence",
		"where fence.singleton_id = 1",
	} {
		if !strings.Contains(normalized, required) {
			t.Errorf("migration is missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"current_setting('multica.deployment_fence_state'",
		"set_config('multica.deployment_fence_state'",
	} {
		if strings.Contains(normalized, forbidden) {
			t.Errorf("migration must not cache deployment fence state with %q", forbidden)
		}
	}
}
