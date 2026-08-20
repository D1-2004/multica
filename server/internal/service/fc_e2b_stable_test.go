package service

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type recordingStableReleaseQueryer struct {
	query string
	row   pgx.Row
}

func (q *recordingStableReleaseQueryer) QueryRow(
	_ context.Context,
	query string,
	_ ...any,
) pgx.Row {
	q.query = query
	return q.row
}

type completeStableReleaseRow struct {
	at time.Time
}

type stableRolloutProgressRow struct {
	values []int
}

func (row stableRolloutProgressRow) Scan(dest ...any) error {
	if len(dest) != len(row.values) {
		return fmt.Errorf("stable rollout progress scan has %d destinations, want %d", len(dest), len(row.values))
	}
	for index, target := range dest {
		value, ok := target.(*int)
		if !ok {
			return fmt.Errorf("unsupported stable rollout progress scan target %T", target)
		}
		*value = row.values[index]
	}
	return nil
}

func (row completeStableReleaseRow) Scan(dest ...any) error {
	for _, target := range dest {
		switch value := target.(type) {
		case *string:
			*value = "loaded"
		case *bool:
			*value = false
		case *int:
			*value = 1
		case *[]byte:
			*value = []byte(`{}`)
		case *pgtype.UUID:
			*value = util.MustParseUUID("410d0a06-a026-449b-b7ab-64c9d92481bd")
		case *pgtype.Timestamptz:
			*value = pgtype.Timestamptz{Time: row.at, Valid: true}
		case *time.Time:
			*value = row.at
		default:
			return fmt.Errorf("unsupported stable release scan target %T", target)
		}
	}
	return nil
}

func TestClaimStableRolloutForAdvanceLoadsCanonicalReleaseProjection(t *testing.T) {
	at := time.Date(2026, 7, 30, 15, 4, 0, 0, time.FixedZone("CST", 8*60*60))
	queryer := &recordingStableReleaseQueryer{row: completeStableReleaseRow{at: at}}
	service := &FCE2BStableService{}

	release, err := service.claimStableRolloutForAdvance(
		context.Background(),
		queryer,
		util.MustParseUUID("ca188cff-8c85-45ff-bd3f-4fd92f1a7f2e"),
		uuid.MustParse("ea57d7c1-0000-4000-8000-000000000000"),
	)
	if err != nil {
		t.Fatalf("claimStableRolloutForAdvance() error = %v", err)
	}
	if !strings.Contains(queryer.query, "RETURNING "+stableReleaseColumns) {
		t.Fatal("advance claim did not use the canonical stable release projection")
	}
	if release.RolloutStartedAt == nil || !release.RolloutStartedAt.Equal(at) {
		t.Fatalf("rollout_started_at = %v, want %s", release.RolloutStartedAt, at)
	}
	if release.BatchStartedAt == nil || !release.BatchStartedAt.Equal(at) {
		t.Fatalf("batch_started_at = %v, want %s", release.BatchStartedAt, at)
	}
	// The multi-backend release adds SandboxBackend to the canonical
	// projection. Keep this assertion reflective so this fix can originate on
	// develop and still prove the field is populated when both CRs are merged.
	if backend := reflect.ValueOf(release).FieldByName("SandboxBackend"); backend.IsValid() && backend.String() == "" {
		t.Fatal("sandbox backend was omitted from the manual advance claim")
	}
}

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

func TestReleaseTemplateUsesPublishedFiveRunnerManifest(t *testing.T) {
	release := FCE2BStableRelease{
		TemplateID:      "template-five-runner",
		TemplateBuildID: "build-five-runner",
		TemplateAlias:   "multica-m7-v3904c2e827d58ad7-r1-abcdef",
		SourceRevision:  "abcdef",
		Manifest: map[string]any{
			"schema_version": 7,
			"providers": []string{
				"hermes", "opencode", "pi", "dsh", "opencode-v2",
			},
			"capabilities_by_backend": map[string][]string{
				string(SandboxBackendAliyunFC): {
					"dws", "dws.im_event", "mcp", RuntimeStartCapabilityEventsV1,
					LLMTraceCapability, A2AInboundOpenCodeCapability,
					A2AInvocationV2Capability, DSHTrajectoryCapability,
				},
			},
			"component_versions": map[string]any{
				"hermes":      "0.19.0",
				"opencode":    "v1.18.11",
				"pi":          "0.83.0",
				"dsh":         "0.1.0-rc.6",
				"opencode-v2": "0.0.0-beta-202608110357",
			},
			"runner_protocol": string(fcE2BRunnerLaunchRootLog),
		},
	}

	template := releaseTemplate(release)
	if template.ManifestVersion != 7 || template.SourceRevision != release.SourceRevision {
		t.Fatalf("release template identity = %#v", template)
	}
	if !reflect.DeepEqual(template.Providers, FCE2BSupportedProviders) {
		t.Fatalf("release providers = %#v, want %#v", template.Providers, FCE2BSupportedProviders)
	}
	if !reflect.DeepEqual(
		template.Capabilities,
		release.Manifest["capabilities_by_backend"].(map[string][]string)[string(SandboxBackendAliyunFC)],
	) {
		t.Fatalf("release capabilities = %#v", template.Capabilities)
	}
	if template.ComponentVersions["dsh"] != "0.1.0-rc.6" ||
		template.ComponentVersions["opencode-v2"] != "0.0.0-beta-202608110357" {
		t.Fatalf("release component versions = %#v", template.ComponentVersions)
	}
	if template.RunnerProtocol != string(fcE2BRunnerLaunchRootLog) {
		t.Fatalf("release runner protocol = %q", template.RunnerProtocol)
	}
}

func TestStableRolloutCoverageUsesCumulativeUpdatedTargets(t *testing.T) {
	tests := []struct {
		name    string
		total   int
		updated int
		batch   int
		target  int
		covered bool
	}{
		{name: "developer coverage skips 5 percent work", total: 61, updated: 35, batch: 1, target: 4, covered: true},
		{name: "developer coverage skips 25 percent work", total: 61, updated: 35, batch: 2, target: 16, covered: true},
		{name: "developer coverage skips 50 percent work", total: 61, updated: 35, batch: 3, target: 31, covered: true},
		{name: "100 percent still requires every runtime", total: 61, updated: 35, batch: 4, target: 61, covered: false},
		{name: "live cohort computes the next fifty percent target", total: 80, updated: 35, batch: 3, target: 40, covered: false},
		{name: "new runtime during final stage must be updated", total: 62, updated: 61, batch: 4, target: 62, covered: false},
		{name: "new runtime completes final stage after update", total: 62, updated: 62, batch: 4, target: 62, covered: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := stableBatchTarget(test.total, test.batch); got != test.target {
				t.Fatalf("stableBatchTarget(%d, %d) = %d, want %d", test.total, test.batch, got, test.target)
			}
			if got := stableBatchCovered(test.total, test.updated, test.batch); got != test.covered {
				t.Fatalf(
					"stableBatchCovered(%d, %d, %d) = %t, want %t",
					test.total,
					test.updated,
					test.batch,
					got,
					test.covered,
				)
			}
		})
	}
}

func TestLoadStableRolloutProgressFreezesCurrentStageCohort(t *testing.T) {
	queryer := &recordingStableReleaseQueryer{row: stableRolloutProgressRow{
		values: []int{
			61, // live Runtime targets after one Runtime was added
			60, // Runtime targets that existed when the 50% stage started
			31, // all updated targets, including one Runtime added after stage start
			30, // updated targets from the frozen current-stage cohort
			0,
			0,
			0,
		},
	}}
	progress, err := loadStableRolloutProgress(
		context.Background(),
		queryer,
		"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
		3,
	)
	if err != nil {
		t.Fatalf("loadStableRolloutProgress() error = %v", err)
	}
	if progress.Total != 61 || progress.StageTotal != 60 || progress.Updated != 31 || progress.StageUpdated != 30 {
		t.Fatalf("stable rollout progress = %#v, want live=61 stage=60 updated=31 stageUpdated=30", progress)
	}
	if target := stableBatchTarget(progress.StageTotal, 3); target != 30 {
		t.Fatalf("frozen 50%% stage target = %d, want 30", target)
	}
	if !stableBatchCovered(progress.StageTotal, progress.StageUpdated, 3) {
		t.Fatal("a 50% stage that completed at 30/60 was invalidated by a later Runtime")
	}
	if nextTarget := stableBatchTarget(progress.Total, 4); nextTarget != 61 {
		t.Fatalf("next 100%% stage target = %d, want all 61 live Runtimes", nextTarget)
	}
	if !strings.Contains(queryer.query, "runtime.created_at AS runtime_created_at") ||
		!strings.Contains(queryer.query, "runtime_created_at <= (SELECT batch_started_at FROM release_state)") {
		t.Fatal("stage cohort is not anchored to the persisted batch start time")
	}
}

func TestStableRolloutSkipsCoveredStagesBeforeHealthGate(t *testing.T) {
	tests := []struct {
		name         string
		total        int
		updated      int
		nextBatch    int
		gateRequired bool
	}{
		{
			name:         "current five percent only",
			total:        52,
			updated:      3,
			nextBatch:    2,
			gateRequired: true,
		},
		{
			name:         "already covers twenty five percent",
			total:        52,
			updated:      13,
			nextBatch:    2,
			gateRequired: false,
		},
		{
			name:         "developer rollout already covers fifty percent",
			total:        52,
			updated:      26,
			nextBatch:    3,
			gateRequired: false,
		},
		{
			name:         "fifty percent still needs one runtime",
			total:        52,
			updated:      25,
			nextBatch:    3,
			gateRequired: true,
		},
		{
			name:         "new runtime makes final stage incomplete",
			total:        53,
			updated:      52,
			nextBatch:    4,
			gateRequired: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := stableCurrentStageHealthGateRequired(
				test.total,
				test.updated,
				test.nextBatch,
			); got != test.gateRequired {
				t.Fatalf(
					"stableCurrentStageHealthGateRequired(%d, %d, %d) = %t, want %t",
					test.total,
					test.updated,
					test.nextBatch,
					got,
					test.gateRequired,
				)
			}
		})
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

func TestStableRuntimeMetadataForBackendUsesCanonicalRuntimeParser(t *testing.T) {
	validASBDigest := strings.Repeat("a", 64)
	tests := []struct {
		name     string
		runtime  db.AgentRuntime
		backend  SandboxBackendKind
		eligible bool
	}{
		{
			name: "legacy FC Runtime",
			runtime: db.AgentRuntime{
				RuntimeMode: "cloud",
				Provider:    "hermes",
				Metadata: []byte(`{
					"kind":"fc-e2b",
					"template_id":"template-id",
					"template_build_id":"build-id",
					"template_channel":"stable"
				}`),
			},
			backend:  SandboxBackendAliyunFC,
			eligible: true,
		},
		{
			name: "ASB Runtime is excluded from FC channel",
			runtime: db.AgentRuntime{
				RuntimeMode: "cloud",
				Provider:    "hermes",
				Metadata: []byte(`{
					"kind":"cloud-sandbox",
					"sandbox_backend":"asb",
					"provider":"hermes",
					"artifact_kind":"oci_image",
					"artifact_channel":"stable",
					"artifact_ref":"registry.example/runtime@sha256:` + validASBDigest + `"
				}`),
			},
			backend:  SandboxBackendAliyunFC,
			eligible: false,
		},
		{
			name: "malformed FC metadata is excluded",
			runtime: db.AgentRuntime{
				RuntimeMode: "cloud",
				Provider:    "hermes",
				Metadata: []byte(`{
					"kind":"cloud-sandbox",
					"sandbox_backend":"aliyun_fc",
					"provider":"hermes",
					"artifact_kind":"oci_image",
					"artifact_channel":"stable",
					"artifact_ref":"template-id"
				}`),
			},
			backend:  SandboxBackendAliyunFC,
			eligible: false,
		},
		{
			name: "candidate Runtime is excluded",
			runtime: db.AgentRuntime{
				RuntimeMode: "cloud",
				Provider:    "pi",
				Metadata: []byte(`{
					"kind":"cloud-sandbox",
					"sandbox_backend":"aliyun_fc",
					"provider":"pi",
					"artifact_kind":"e2b_template",
					"artifact_channel":"candidate",
					"artifact_ref":"template-id"
				}`),
			},
			backend:  SandboxBackendAliyunFC,
			eligible: false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, eligible := stableRuntimeMetadataForBackend(test.runtime, test.backend)
			if eligible != test.eligible {
				t.Fatalf("eligible = %t, want %t", eligible, test.eligible)
			}
		})
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

func TestFCE2BStableServiceReadsCurrentDeveloperUserIDs(t *testing.T) {
	initial := "410d0a06-a026-449b-b7ab-64c9d92481bd"
	updated := "2c508db5-5410-41f9-ad69-d3d527529c20"
	service := NewFCE2BStableService(nil, nil, map[string]struct{}{initial: {}})

	current := map[string]struct{}{updated: {}}
	service.DeveloperUserIDsProvider = func() map[string]struct{} {
		return current
	}
	if _, ok := service.currentDeveloperUserIDs()[updated]; !ok {
		t.Fatal("current Diamond developer list was not used")
	}
	if _, ok := service.currentDeveloperUserIDs()[initial]; ok {
		t.Fatal("startup developer list remained active after installing the provider")
	}

	replacement := "bc780d5f-3cf2-4bc5-99be-86fe204f193d"
	current = map[string]struct{}{replacement: {}}
	if _, ok := service.currentDeveloperUserIDs()[replacement]; !ok {
		t.Fatal("updated Diamond developer list was not observed")
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

func TestStableProviderProbeCoverageDoesNotRequireInsufficientSamples(t *testing.T) {
	for _, completed := range []int{0, 1} {
		if stableProviderProbeCoverageIncomplete(completed, 0, 0) {
			t.Fatalf("completed=%d was treated as incomplete provider session coverage", completed)
		}
	}
}

func TestStableProviderProbeCoverageReportsMissingSessionKindsOnceSampled(t *testing.T) {
	tests := []struct {
		name                   string
		rotatedExistingSession int
		newSession             int
		wantIncomplete         bool
	}{
		{name: "neither session kind", wantIncomplete: true},
		{name: "existing session only", rotatedExistingSession: 1, wantIncomplete: true},
		{name: "new conversation only", newSession: 1, wantIncomplete: true},
		{name: "both session kinds", rotatedExistingSession: 1, newSession: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			incomplete := stableProviderProbeCoverageIncomplete(
				2,
				test.rotatedExistingSession,
				test.newSession,
			)
			if incomplete != test.wantIncomplete {
				t.Fatalf(
					"stableProviderProbeCoverageIncomplete() = %t, want %t",
					incomplete,
					test.wantIncomplete,
				)
			}
		})
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

func TestStableObservationRestartsFinalCatchUpForNewRuntimes(t *testing.T) {
	missing, failed := stableObservationTargetCounts(stableRolloutProgress{
		Total:   5,
		Updated: 5,
	})
	if missing != 0 || failed != 0 {
		t.Fatalf("fully updated reconciled targets = missing %d, failed %d", missing, failed)
	}
	missing, failed = stableObservationTargetCounts(stableRolloutProgress{
		Total:   6,
		Updated: 5,
	})
	if missing != 1 || failed != 0 {
		t.Fatalf("new reconciled target = missing %d, failed %d", missing, failed)
	}
	missing, failed = stableObservationTargetCounts(stableRolloutProgress{
		Total:   6,
		Updated: 5,
		Failed:  1,
	})
	if missing != 1 || failed != 1 {
		t.Fatalf("failed reconciled target = missing %d, failed %d", missing, failed)
	}

	if err := stableObservationFailedTargetsError(0); err != nil {
		t.Fatalf("fully updated observation was blocked: %v", err)
	}
	if !stableObservationNeedsCatchUp(2, 0) {
		t.Fatal("new Runtime targets did not restart the final 100% catch-up")
	}
	if stableObservationNeedsCatchUp(2, 1) {
		t.Fatal("failed Runtime targets were treated as ordinary catch-up work")
	}
	if err := stableObservationFailedTargetsError(1); err == nil ||
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

func TestStableReleaseFingerprintIncludesArtifactBuildTime(t *testing.T) {
	firstBuiltAt := time.Date(2026, 7, 30, 20, 34, 18, 0, time.FixedZone("CST", 8*60*60))
	secondBuiltAt := firstBuiltAt.Add(time.Second)
	input := CreateFCE2BStableReleaseInput{
		SandboxBackend:  SandboxBackendASB,
		ArtifactRef:     "registry.example/runtime@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		ExpectedBuildID: "build-1",
		ArtifactBuiltAt: &firstBuiltAt,
		ArtifactDigest:  "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		GitCommit:       "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	}
	first := stableReleaseFingerprint(input)
	input.ArtifactBuiltAt = &secondBuiltAt
	if got := stableReleaseFingerprint(input); got == first {
		t.Fatal("artifact build time did not change the request fingerprint")
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

func TestReleaseTemplatePreservesVerifiedFCManifest(t *testing.T) {
	release := FCE2BStableRelease{
		TemplateID:      "template-v6",
		TemplateBuildID: "build-v6",
		TemplateAlias:   "multica-m6",
		SourceRevision:  "029b33",
		Manifest: map[string]any{
			"schema_version": float64(6),
			"providers":      []any{"hermes", "opencode", "pi"},
			"capabilities_by_backend": map[string]any{
				"aliyun_fc": []any{
					"dws",
					"dws.im_event",
					"mcp",
					RuntimeStartCapabilityEventsV1,
					LLMTraceCapability,
					A2AInboundOpenCodeCapability,
					A2AInvocationV2Capability,
				},
			},
			"component_versions": map[string]any{
				"opencode": "v1.18.11",
				"dws":      "v1.0.58-beta.4",
			},
			"runner_protocol": "root-log-v1",
		},
	}

	template := releaseTemplate(release)
	if template.ManifestVersion != 6 {
		t.Fatalf("manifest version = %d, want 6", template.ManifestVersion)
	}
	if template.SourceRevision != release.SourceRevision {
		t.Fatalf("source revision = %q, want %q", template.SourceRevision, release.SourceRevision)
	}
	if want := []string{"hermes", "opencode", "pi"}; !reflect.DeepEqual(template.Providers, want) {
		t.Fatalf("providers = %#v, want %#v", template.Providers, want)
	}
	if !containsAllStrings(template.Capabilities, A2AInvocationV2Capability) {
		t.Fatalf("capabilities = %#v, want %q", template.Capabilities, A2AInvocationV2Capability)
	}
	if template.RunnerProtocol != "root-log-v1" {
		t.Fatalf("runner protocol = %q, want root-log-v1", template.RunnerProtocol)
	}
	if template.ComponentVersions["opencode"] != "v1.18.11" ||
		template.ComponentVersions["dws"] != "v1.0.58-beta.4" {
		t.Fatalf("component versions = %#v", template.ComponentVersions)
	}
}

func TestRuntimeUsesStableReleaseRequiresVerifiedManifestMetadata(t *testing.T) {
	manifest := map[string]any{
		"schema_version": float64(6),
		"capabilities_by_backend": map[string]any{
			"aliyun_fc": []any{"dws", RuntimeStartCapabilityEventsV1, A2AInvocationV2Capability},
			"asb":       []any{"dws", RuntimeStartCapabilityEventsV1, A2AInvocationV2Capability},
		},
		"runner_protocol": "root-log-v1",
	}
	fcRelease := FCE2BStableRelease{
		SandboxBackend:  string(SandboxBackendAliyunFC),
		TemplateID:      "template-v6",
		TemplateBuildID: "build-v6",
		Manifest:        manifest,
	}
	fcRuntime := db.AgentRuntime{
		RuntimeMode: "cloud",
		Provider:    "opencode",
		Metadata: []byte(`{
			"kind":"fc-e2b",
			"template_id":"template-v6",
			"template_build_id":"build-v6",
			"manifest_version":6,
			"runner_protocol":"root-log-v1",
			"capabilities":["opencode","dws","runtime_start_events_v1","a2a-invocation-v2"]
		}`),
	}
	if !runtimeUsesStableRelease(fcRuntime, fcRelease) {
		t.Fatal("FC Runtime with the verified manifest metadata was rejected")
	}
	fcRuntime.Metadata = []byte(`{
		"kind":"fc-e2b",
		"template_id":"template-v6",
		"template_build_id":"build-v6",
		"manifest_version":6,
		"runner_protocol":"root-log-v1",
		"capabilities":["opencode","dws","runtime_start_events_v1"]
	}`)
	if runtimeUsesStableRelease(fcRuntime, fcRelease) {
		t.Fatal("FC Runtime missing a2a-invocation-v2 passed stable release readback")
	}

	fcRelease.Manifest = map[string]any{
		"schema_version": float64(6),
		"capabilities_by_backend": map[string]any{
			"aliyun_fc": []any{
				"dws",
				RuntimeStartCapabilityEventsV1,
				A2AInboundOpenCodeCapability,
				A2AInvocationV2Capability,
			},
		},
		"runner_protocol": "root-log-v1",
	}
	fcRuntime.Provider = "hermes"
	fcRuntime.Metadata = []byte(`{
		"kind":"fc-e2b",
		"template_id":"template-v6",
		"template_build_id":"build-v6",
		"manifest_version":6,
		"runner_protocol":"root-log-v1",
		"capabilities":["hermes","dws","runtime_start_events_v1","a2a-invocation-v2"]
	}`)
	if !runtimeUsesStableRelease(fcRuntime, fcRelease) {
		t.Fatal("Hermes Runtime was rejected for omitting the sibling OpenCode A2A capability")
	}

	digest := strings.Repeat("a", 64)
	asbRelease := FCE2BStableRelease{
		SandboxBackend:  string(SandboxBackendASB),
		ArtifactRef:     "registry.example/runtime@sha256:" + digest,
		ArtifactBuildID: "build-v6",
		ArtifactDigest:  "sha256:" + digest,
		Manifest:        manifest,
	}
	asbRuntime := db.AgentRuntime{
		RuntimeMode: "cloud",
		Provider:    "opencode",
		Metadata: []byte(`{
			"kind":"cloud-sandbox",
			"sandbox_backend":"asb",
			"provider":"opencode",
			"artifact_kind":"oci_image",
			"artifact_ref":"registry.example/runtime@sha256:` + digest + `",
			"artifact_build_id":"build-v6",
			"artifact_digest":"sha256:` + digest + `",
			"manifest_version":6,
			"runner_protocol":"root-log-v1",
			"capabilities":["dws","runtime_start_events_v1","a2a-invocation-v2"]
		}`),
	}
	if !runtimeUsesStableRelease(asbRuntime, asbRelease) {
		t.Fatal("ASB Runtime with the verified manifest metadata was rejected")
	}
}

func TestStableReleaseCapabilitiesForProvider(t *testing.T) {
	capabilities := []string{
		"dws",
		A2AInboundHermesCapability,
		A2AInboundOpenCodeCapability,
		A2AInboundPiCapability,
		"dsh_trajectory_v1",
	}
	tests := []struct {
		provider string
		want     []string
	}{
		{"hermes", []string{"dws", A2AInboundHermesCapability}},
		{"opencode", []string{"dws", A2AInboundOpenCodeCapability}},
		{"pi", []string{"dws", A2AInboundPiCapability}},
		{"dsh", []string{"dws", A2AInboundOpenCodeCapability, "dsh_trajectory_v1"}},
		{"opencode-v2", []string{"dws", A2AInboundOpenCodeCapability}},
	}
	for _, tt := range tests {
		t.Run(tt.provider, func(t *testing.T) {
			if got := stableReleaseCapabilitiesForProvider(tt.provider, capabilities); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("capabilities = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestVerifyStableTemplateRunsNativeSmokeAndChecksManifest(t *testing.T) {
	runner := &fakeCommandRunner{out: []string{
		"Sandbox created with ID sbx_stable123 using template multica-stable\n",
		"",
		"",
		`{
			"schema_version":6,
			"sandbox_backends":["aliyun_fc","asb"],
			"providers":["hermes","opencode","pi"],
			"capabilities":["dws","dws.im_event","mcp","runtime_start_events_v1","llm_trace_v1","a2a_inbound_opencode_v1","a2a-invocation-v2"],
			"capabilities_by_backend":{
				"aliyun_fc":["dws","dws.im_event","mcp","runtime_start_events_v1","llm_trace_v1","a2a_inbound_opencode_v1","a2a-invocation-v2"],
				"asb":["dws","dws.im_event","mcp","runtime_start_events_v1","llm_trace_v1","a1","mw","buc","a2a-invocation-v2"]
			},
			"identity_modes_by_backend":{
				"aliyun_fc":["agent_identity"],
				"asb":["agent_identity","spiffe","buc_wireguard"]
			},
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
		ManifestVersion: 6,
		Providers:       []string{"hermes", "opencode", "pi"},
		Capabilities: []string{
			"dws",
			"dws.im_event",
			"mcp",
			RuntimeStartCapabilityEventsV1,
			LLMTraceCapability,
			A2AInboundOpenCodeCapability,
			A2AInvocationV2Capability,
		},
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
	if got := runner.timeouts[2]; got < fcE2BStableValidationTimeout-time.Second || got > fcE2BStableValidationTimeout {
		t.Fatalf("smoke timeout = %s, want approximately %s", got, fcE2BStableValidationTimeout)
	}
	if got := runner.timeouts[1]; got <= 0 || got > time.Second {
		t.Fatalf("ready probe timeout = %s, want configured sandbox-ready timeout", got)
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
		ManifestVersion: 3,
		Providers:       []string{"hermes", "opencode", "pi"},
		Capabilities:    []string{"dws", "dws.im_event", "mcp", RuntimeStartCapabilityEventsV1},
		RunnerProtocol:  "root-log-v1",
	})
	if err == nil || !strings.Contains(err.Error(), "runtime-smoke-test failed") {
		t.Fatalf("smoke failure error = %v", err)
	}
}

func testStableUUID(value int) string {
	return fmt.Sprintf("00000000-0000-4000-8000-%012d", value)
}
