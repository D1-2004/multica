package main

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/multica-ai/multica/server/internal/dshhost"
	"github.com/multica-ai/multica/server/pkg/runtimeconfig"
)

func TestDSHStorageQuotaFollowsDiamondWithoutChangingPlacement(t *testing.T) {
	raw, err := os.ReadFile("../../../docs/runtime-config.example.json")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := runtimeconfig.ParseStrict(raw, true)
	if err != nil {
		t.Fatal(err)
	}
	remote, err := runtimeconfig.NewStatic(cfg)
	if err != nil {
		t.Fatal(err)
	}
	app := &appRuntimeConfig{remote: remote}
	base := dshhost.ProvisionSpec{AccountID: "account", Region: "cn-beijing", Zone: "cn-beijing-k", TeamID: "team", FileSystemID: "fs", VPCID: "vpc", SecurityGroupID: "sg", VSwitchIDs: []string{"vsw"}, SizeLimit: 10 << 30, FileCountLimit: 10000}
	if got := dshStoragePlacement(base, nil); !reflect.DeepEqual(got, base) {
		t.Fatal("environment-only placement changed")
	}
	check := func(size, files int64) {
		t.Helper()
		want := base
		want.SizeLimit, want.FileCountLimit = size, files
		if got := dshStoragePlacement(base, app); !reflect.DeepEqual(got, want) {
			t.Fatalf("got %+v; want %+v", got, want)
		}
	}
	check(100<<30, 1000000000)
	cfg.Runtime.AgenticFS = runtimeconfig.AgenticFSConfig{SizeLimit: 120 << 30, FileCountLimit: 900000000}
	updated, _ := json.Marshal(cfg)
	if _, err := remote.ApplyJSON(updated); err != nil {
		t.Fatal(err)
	}
	check(120<<30, 900000000)
	cfg.Runtime.AgenticFS.FileCountLimit = 1000000001
	invalid, _ := json.Marshal(cfg)
	if _, err := remote.ApplyJSON(invalid); err == nil {
		t.Fatal("invalid quota accepted")
	}
	check(120<<30, 900000000)
}
