package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestStableBatchCutoffs(t *testing.T) {
	tests := []struct {
		total int
		want  [4]int
	}{
		{total: 0, want: [4]int{}},
		{total: 1, want: [4]int{1, 1, 1, 1}},
		{total: 2, want: [4]int{1, 1, 1, 2}},
		{total: 3, want: [4]int{3, 3, 3, 3}},
		{total: 60, want: [4]int{3, 15, 30, 60}},
		{total: 61, want: [4]int{4, 16, 31, 61}},
	}
	for _, test := range tests {
		if got := stableBatchCutoffs(test.total); got != test.want {
			t.Fatalf("stableBatchCutoffs(%d) = %#v, want %#v", test.total, got, test.want)
		}
	}
}

func TestAssignStableBatchesCoversProvidersAndIsDeterministic(t *testing.T) {
	targets := make([]stableRuntimeTarget, 0, 60)
	providers := []string{"hermes", "opencode", "pi"}
	for index := 0; index < 60; index++ {
		targets = append(targets, stableRuntimeTarget{
			RuntimeID:   util.MustParseUUID(testStableUUID(index + 1)),
			WorkspaceID: util.MustParseUUID(testStableUUID((index % 23) + 101)),
			Provider:    providers[index%len(providers)],
		})
	}
	copyTargets := append([]stableRuntimeTarget(nil), targets...)
	assignStableBatches("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", targets)
	assignStableBatches("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", copyTargets)

	firstProviders := map[string]bool{}
	counts := [5]int{}
	byID := make(map[string]int, len(copyTargets))
	for _, target := range copyTargets {
		byID[util.UUIDToString(target.RuntimeID)] = target.BatchIndex
	}
	for _, target := range targets {
		counts[target.BatchIndex]++
		if target.BatchIndex == 1 {
			firstProviders[target.Provider] = true
		}
		if got := byID[util.UUIDToString(target.RuntimeID)]; got != target.BatchIndex {
			t.Fatalf("runtime %s moved from batch %d to %d", util.UUIDToString(target.RuntimeID), target.BatchIndex, got)
		}
	}
	if counts[1] != 3 || counts[2] != 12 || counts[3] != 15 || counts[4] != 30 {
		t.Fatalf("batch counts = %#v, want 3/12/15/30", counts)
	}
	for _, provider := range providers {
		if !firstProviders[provider] {
			t.Fatalf("first batch does not cover provider %q: %#v", provider, firstProviders)
		}
	}
}

func TestStableRuntimeProviderPreservesLegacyHermesDefault(t *testing.T) {
	tests := map[string]string{
		"":            "hermes",
		" Hermes ":    "hermes",
		"OpenCode":    "opencode",
		"pi":          "pi",
		"unsupported": "",
	}
	for input, want := range tests {
		if got := stableRuntimeProvider(input); got != want {
			t.Fatalf("stableRuntimeProvider(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestStableNextBatchSchedule(t *testing.T) {
	started := time.Date(2026, 7, 28, 10, 0, 0, 0, time.UTC)
	tests := []struct {
		current    int
		next       int
		percentage int
		due        time.Time
	}{
		{1, 2, 25, started.Add(2 * time.Hour)},
		{2, 3, 50, started.Add(8 * time.Hour)},
		{3, 4, 100, started.Add(20 * time.Hour)},
		{4, 0, 100, started.Add(24 * time.Hour)},
	}
	for _, test := range tests {
		next, percentage, due := stableNextBatch(test.current, started)
		if next != test.next || percentage != test.percentage || !due.Equal(test.due) {
			t.Fatalf(
				"stableNextBatch(%d) = (%d, %d, %s), want (%d, %d, %s)",
				test.current,
				next,
				percentage,
				due,
				test.next,
				test.percentage,
				test.due,
			)
		}
	}
}

func TestStableFailureRate(t *testing.T) {
	if got := failureRate(0, 0); got != 0 {
		t.Fatalf("failureRate(0, 0) = %v, want 0", got)
	}
	if got := failureRate(1, 20); got != 0.05 {
		t.Fatalf("failureRate(1, 20) = %v, want 0.05", got)
	}
}

func TestRuntimeUsesTemplateRequiresTemplateAndBuild(t *testing.T) {
	runtime := db.AgentRuntime{Metadata: []byte(`{
		"template_id":"template-1",
		"template_build_id":"build-1"
	}`)}
	if !runtimeUsesTemplate(runtime, "template-1", "build-1") {
		t.Fatal("exact template readback was rejected")
	}
	if runtimeUsesTemplate(runtime, "template-1", "build-2") {
		t.Fatal("mismatched build readback was accepted")
	}
	if runtimeUsesTemplate(db.AgentRuntime{Metadata: []byte(`{`)}, "template-1", "build-1") {
		t.Fatal("invalid metadata readback was accepted")
	}
}

func TestVerifyStableTemplateRunsNativeSmokeAndChecksManifest(t *testing.T) {
	runner := &fakeCommandRunner{out: []string{
		"Sandbox created with ID sbx_stable123 using template multica-stable\n",
		"",
		"",
		`{
			"schema_version":1,
			"providers":["hermes","opencode","pi"],
			"capabilities":["dws","dws.im_event","mcp"],
			"component_versions":{
				"hermes":"0.19.0",
				"opencode":"v1.18.4",
				"pi":"0.80.10",
				"dws":"v1.0.53-beta.4",
				"multica_ref":"abcdef"
			},
			"runner_protocol":"root-log-v1"
		}`,
		"",
	}}
	launcher := NewFCE2BLauncher(nil, nil, FCE2BConfig{
		APIKey:              "test-key",
		APIURL:              "https://fc-e2b.test",
		Domain:              "fc-e2b.test",
		CLIPath:             "e2b-test",
		TimeoutSeconds:      3600,
		SandboxReadyTimeout: time.Second,
	}, runner)
	template := FCE2BTemplate{
		ID:              "template-1",
		BuildID:         "build-1",
		Template:        "multica-stable",
		Status:          "READY",
		ManifestVersion: 2,
		Providers:       []string{"hermes", "opencode", "pi"},
		Capabilities:    []string{"dws", "dws.im_event", "mcp"},
		ComponentVersions: map[string]string{
			"hermes":   "0.19.0",
			"opencode": "v1.18.4",
			"pi":       "0.80.10",
			"dws":      "v1.0.53-beta.4",
		},
		RunnerProtocol: "root-log-v1",
	}
	manifest, err := launcher.VerifyStableTemplate(context.Background(), template)
	if err != nil {
		t.Fatalf("VerifyStableTemplate() error = %v", err)
	}
	if manifest["runner_protocol"] != "root-log-v1" {
		t.Fatalf("verified manifest = %#v", manifest)
	}
	if len(runner.calls) != 5 {
		t.Fatalf("E2B call count = %d, want create, ready, smoke, manifest and kill", len(runner.calls))
	}
	if got := runner.calls[2].args; len(got) == 0 || got[len(got)-1] != "/usr/local/bin/runtime-smoke-test" {
		t.Fatalf("smoke command = %#v", got)
	}
	if got := runner.calls[4].args; len(got) != 3 || got[0] != "sandbox" || got[1] != "kill" {
		t.Fatalf("kill command = %#v", got)
	}
}

func TestVerifyStableTemplateSmokeFailureRejectsCandidate(t *testing.T) {
	runner := &fakeCommandRunner{
		out: []string{
			"Sandbox created with ID sbx_stable123 using template multica-stable\n",
			"",
			"",
			"",
		},
		errs: []error{nil, nil, errors.New("smoke failed"), nil},
	}
	launcher := NewFCE2BLauncher(nil, nil, FCE2BConfig{
		APIKey:              "test-key",
		APIURL:              "https://fc-e2b.test",
		Domain:              "fc-e2b.test",
		CLIPath:             "e2b-test",
		TimeoutSeconds:      3600,
		SandboxReadyTimeout: time.Second,
	}, runner)
	_, err := launcher.VerifyStableTemplate(context.Background(), FCE2BTemplate{
		ID:              "template-1",
		BuildID:         "build-1",
		Template:        "multica-stable",
		Status:          "READY",
		ManifestVersion: 2,
		Providers:       []string{"hermes", "opencode", "pi"},
		Capabilities:    []string{"dws", "dws.im_event", "mcp"},
		RunnerProtocol:  "root-log-v1",
	})
	if err == nil || !strings.Contains(err.Error(), "runtime-smoke-test failed") {
		t.Fatalf("smoke failure error = %v", err)
	}
}

func testStableUUID(value int) string {
	return fmt.Sprintf("00000000-0000-4000-8000-%012d", value)
}
