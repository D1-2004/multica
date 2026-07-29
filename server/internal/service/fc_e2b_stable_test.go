package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
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

func TestStableRuntimeOwnedByDeveloperUsesOnlyConfiguredOwnerUUID(t *testing.T) {
	allowed := util.MustParseUUID("410d0a06-a026-449b-b7ab-64c9d92481bd")
	other := util.MustParseUUID("2c508db5-5410-41f9-ad69-d3d527529c20")
	developers := map[string]struct{}{
		util.UUIDToString(allowed): {},
	}
	if !stableRuntimeOwnedByDeveloper(allowed, developers) {
		t.Fatal("configured runtime owner was not included in developer rollout")
	}
	if stableRuntimeOwnedByDeveloper(other, developers) {
		t.Fatal("unconfigured runtime owner was included in developer rollout")
	}
	if stableRuntimeOwnedByDeveloper(pgtype.UUID{}, developers) {
		t.Fatal("runtime without an owner was included in developer rollout")
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

func TestStableRolloutScheduleKeepsOriginalAnchor(t *testing.T) {
	started := time.Date(2026, 7, 29, 10, 0, 0, 0, time.UTC)
	schedule := stableRolloutSchedule(started)
	want := []FCE2BStableRolloutMilestone{
		{Batch: 1, Percentage: 5, ScheduledAt: started, Kind: "rollout"},
		{Batch: 2, Percentage: 25, ScheduledAt: started.Add(2 * time.Hour), Kind: "rollout"},
		{Batch: 3, Percentage: 50, ScheduledAt: started.Add(8 * time.Hour), Kind: "rollout"},
		{Batch: 4, Percentage: 100, ScheduledAt: started.Add(20 * time.Hour), Kind: "rollout"},
		{Batch: 5, Percentage: 100, ScheduledAt: started.Add(24 * time.Hour), Kind: "complete"},
	}
	if len(schedule) != len(want) {
		t.Fatalf("stableRolloutSchedule() returned %d milestones, want %d", len(schedule), len(want))
	}
	for index := range want {
		if schedule[index] != want[index] {
			t.Fatalf("milestone %d = %#v, want %#v", index, schedule[index], want[index])
		}
	}

	nextBatch, percentage, due := stableNextBatch(2, started)
	if nextBatch != 3 || percentage != 50 || !due.Equal(want[2].ScheduledAt) {
		t.Fatalf(
			"manual stage progression changed the fixed schedule: got (%d, %d, %s)",
			nextBatch,
			percentage,
			due,
		)
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

func TestStableBatchHealthWindowExcludesDeveloperPreRolloutCutovers(t *testing.T) {
	batchStartedAt := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name        string
		completedAt time.Time
		want        bool
	}{
		{
			name:        "developer cutover before percentage stage",
			completedAt: batchStartedAt.Add(-time.Minute),
			want:        false,
		},
		{
			name:        "cutover at percentage stage boundary",
			completedAt: batchStartedAt,
			want:        true,
		},
		{
			name:        "cutover during percentage stage",
			completedAt: batchStartedAt.Add(time.Minute),
			want:        true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := stableTargetNeedsBatchHealthGate(test.completedAt, batchStartedAt); got != test.want {
				t.Fatalf(
					"stableTargetNeedsBatchHealthGate(%s, %s) = %t, want %t",
					test.completedAt,
					batchStartedAt,
					got,
					test.want,
				)
			}
		})
	}
}

func TestStableObservationTargetsError(t *testing.T) {
	if err := stableObservationTargetsError(0, 0); err != nil {
		t.Fatalf("fully updated observation was blocked: %v", err)
	}
	if err := stableObservationTargetsError(2, 0); err == nil ||
		!strings.Contains(err.Error(), "2 runtime targets are not updated") {
		t.Fatalf("missing targets error = %v", err)
	}
	if err := stableObservationTargetsError(2, 1); err == nil ||
		!strings.Contains(err.Error(), "1 runtime targets failed") {
		t.Fatalf("failed targets error = %v", err)
	}
}

func TestStableSourceRevisionRequiresCanonicalAliasRevision(t *testing.T) {
	for _, valid := range []string{"000000", "b90849", "abcdef"} {
		if !isStableSourceRevision(valid) {
			t.Fatalf("valid source revision %q was rejected", valid)
		}
	}
	for _, invalid := range []string{"", "b9084", "b908490", "B90849", "zzzzzz"} {
		if isStableSourceRevision(invalid) {
			t.Fatalf("invalid source revision %q was accepted", invalid)
		}
	}
}

func TestStableReleaseFingerprintDoesNotDependOnDerivedBootstrapState(t *testing.T) {
	input := CreateFCE2BStableReleaseInput{
		TemplateID:      "template-1",
		ExpectedBuildID: "build-1",
		Note:            "release note",
	}
	first := stableReleaseFingerprint(input)
	input.Bootstrap = true
	if got := stableReleaseFingerprint(input); got != first {
		t.Fatalf("server-derived bootstrap state changed request fingerprint: %q != %q", got, first)
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
