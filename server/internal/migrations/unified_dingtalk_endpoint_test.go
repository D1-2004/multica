package migrations

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUnifiedDingTalkEndpointMigrationIsReversibleAndFailClosed(t *testing.T) {
	dir := realMigrationsDir(t)
	up := readMigrationForTest(t, filepath.Join(dir, "196_unified_dingtalk_router_registration.up.sql"))
	down := readMigrationForTest(t, filepath.Join(dir, "196_unified_dingtalk_router_registration.down.sql"))

	for _, fragment := range []string{
		"create table if not exists agent_dispatch_endpoint",
		"unique (agent_id)",
		"unique (endpoint_id)",
		"dispatch_url text not null",
		"from channel_installation ci",
		"ci.channel_type = 'dingtalk_account'",
		"router_registration_status",
		"ingress_cutover_state",
		"router_pending",
		"then 'pending'",
	} {
		if !strings.Contains(up, fragment) {
			t.Errorf("up migration missing %q", fragment)
		}
	}
	for _, fragment := range []string{
		"dispatch_endpoint_id",
		"dispatch_url",
		"drop table if exists agent_dispatch_endpoint",
		"then 'active'",
		"then 'revoked'",
	} {
		if !strings.Contains(down, fragment) {
			t.Errorf("down migration missing %q", fragment)
		}
	}
}

func TestGatewayCallbackInstallationsAreExcludedFromLegacyStream(t *testing.T) {
	queryPath := filepath.Join(realMigrationsDir(t), "..", "pkg", "db", "queries", "channel.sql")
	body, err := os.ReadFile(queryPath)
	if err != nil {
		t.Fatal(err)
	}
	queries := strings.ToLower(string(body))
	if got := strings.Count(queries, "ingress_cutover_state', 'legacy_stream'"); got < 2 {
		t.Fatalf("legacy Stream guards = %d, want list and lease acquisition guards", got)
	}
}

func TestRouterStateUpdatesFenceConcurrentLifecycleTransitions(t *testing.T) {
	queryPath := filepath.Join(realMigrationsDir(t), "..", "pkg", "db", "queries", "dingtalk_router_registration.sql")
	body, err := os.ReadFile(queryPath)
	if err != nil {
		t.Fatal(err)
	}
	query := strings.ToLower(string(body))
	for _, fragment := range []string{
		"expected_status",
		"expected_router_registration_status",
		"expected_ingress_cutover_state",
	} {
		if !strings.Contains(query, fragment) {
			t.Fatalf("Router state CAS query missing %q", fragment)
		}
	}
}

func readMigrationForTest(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.ToLower(string(body))
}
