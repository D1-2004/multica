package runtimeconfig

import (
	"strings"
	"testing"
)

func TestConnectionReuseParsesBesideTheConcurrencyCap(t *testing.T) {
	raw := strings.Replace(validJSON(), `"timeout_seconds": 4800,`, `"timeout_seconds": 4800,
      "connection_reuse": {
        "enabled": true,
        "max_concurrent_tasks": 6,
        "workspace_ids": ["019fcfda-70bb-7830-acee-61d95e68668b"],
        "agent_ids": ["11111111-1111-4111-8111-111111111111"]
      },`, 1)
	cfg := mustParseConfig(t, raw)
	reuse := cfg.Runtime.FCE2B.ConnectionReuse
	if reuse == nil || !reuse.Enabled || len(reuse.WorkspaceIDs) != 1 || len(reuse.AgentIDs) != 1 {
		t.Fatalf("reuse = %#v", reuse)
	}
	if reuse.Concurrency() != 6 {
		t.Fatalf("concurrency = %d", reuse.Concurrency())
	}
}

func TestConnectionReuseRejectsABadTargetAndCap(t *testing.T) {
	for _, snippet := range []string{
		`"connection_reuse": {"enabled": true, "workspace_ids": ["019FCFDA-70BB-7830-ACEE-61D95E68668B"]},`,
		`"connection_reuse": {"enabled": true, "agent_ids": ["not-a-uuid"]},`,
		`"connection_reuse": {"enabled": false, "max_concurrent_tasks": 51},`,
		`"connection_reuse": {"enabled": true, "max_concurrent_tasks": 0, "agent_ids": ["11111111-1111-4111-8111-111111111111", "11111111-1111-4111-8111-111111111111"]},`,
	} {
		raw := strings.Replace(validJSON(), `"fc_e2b": {`, `"fc_e2b": {`+snippet, 1)
		if _, err := ParseStrict([]byte(raw), false); err == nil {
			t.Fatalf("accepted %s", snippet)
		}
	}
}

func TestConnectionReuseZeroCapMeansSix(t *testing.T) {
	raw := strings.Replace(validJSON(), `"fc_e2b": {`, `"fc_e2b": {"connection_reuse": {"enabled": false},`, 1)
	cfg := mustParseConfig(t, raw)
	reuse := cfg.Runtime.FCE2B.ConnectionReuse
	if reuse == nil || reuse.Concurrency() != DefaultFCE2BConnectionReuseTasks || reuse.Enabled {
		t.Fatalf("reuse = %#v", reuse)
	}
	if (FCE2BConnectionReuse{}).Concurrency() != DefaultFCE2BConnectionReuseTasks {
		t.Fatal("an absent connection_reuse must keep the default cap")
	}
}

func TestConnectionReuseSnapshotCopiesTargetLists(t *testing.T) {
	raw := strings.Replace(validJSON(), `"fc_e2b": {`, `"fc_e2b": {"connection_reuse": {"enabled": true, "agent_ids": ["11111111-1111-4111-8111-111111111111"]},`, 1)
	service, err := NewStatic(mustParseConfig(t, raw))
	if err != nil {
		t.Fatal(err)
	}
	first := service.Current()
	first.Config.Runtime.FCE2B.ConnectionReuse.AgentIDs[0] = "mutated"
	current := service.Current()
	if current.Config.Runtime.FCE2B.ConnectionReuse.AgentIDs[0] != "11111111-1111-4111-8111-111111111111" {
		t.Fatalf("snapshot shared the agent list: %#v", current.Config.Runtime.FCE2B.ConnectionReuse)
	}
}
