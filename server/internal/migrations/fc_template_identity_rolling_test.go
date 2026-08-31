package migrations

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readMigrationForContract(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(realMigrationsDir(t), name)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read migration %s: %v", name, err)
	}
	return strings.ToUpper(string(raw))
}

func TestFCTemplateIdentityCompatibilityMigrationIsOldBinarySafe(t *testing.T) {
	sql := readMigrationForContract(t, "9090_fc_template_id_identity_compat.up.sql")
	for _, forbidden := range []string{"DROP COLUMN", "UPDATE AGENT_RUNTIME", "ARTIFACT_BUILD_ID ="} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("compatibility migration contains old-binary-incompatible SQL %q", forbidden)
		}
	}
	if !strings.Contains(sql, "DROP CONSTRAINT IF EXISTS FC_E2B_STABLE_RELEASE_TARGET_PROVIDER_CHECK") {
		t.Fatal("compatibility migration does not remove the legacy provider check")
	}
}

func TestFCTemplateBuildCompatibilityColumnsAreIdempotent(t *testing.T) {
	sql := readMigrationForContract(t, "9092_restore_fc_template_build_columns_for_rolling_compat.up.sql")
	for _, column := range []string{
		"TEMPLATE_BUILD_ID",
		"PREVIOUS_TEMPLATE_BUILD_ID",
		"CURRENT_TEMPLATE_BUILD_ID",
	} {
		if !strings.Contains(sql, "ADD COLUMN IF NOT EXISTS "+column) {
			t.Fatalf("compatibility migration does not idempotently add %s", column)
		}
		if !strings.Contains(sql, "ALTER COLUMN "+column+" SET DEFAULT ''") {
			t.Fatalf("compatibility migration does not set a default for %s", column)
		}
	}
}
