package main

import (
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service/inboundcoord"
	"github.com/multica-ai/multica/server/pkg/runtimeconfig"
)

func TestPerformanceRolloutSingleSwitchSelectsAllQuickWinsForOneAgent(t *testing.T) {
	const target = "e2293e9e-1e79-4926-b0e6-da4cb693add0"
	id := pgtype.UUID{Bytes: [16]byte{0xe2, 0x29, 0x3e, 0x9e, 0x1e, 0x79, 0x49, 0x26, 0xb0, 0xe6, 0xda, 0x4c, 0xb6, 0x93, 0xad, 0xd0}, Valid: true}
	other := pgtype.UUID{Bytes: [16]byte{91}, Valid: true}
	for _, enabled := range []bool{true, false, true} {
		cfg := quickWinsForRuntime(runtimeconfig.RuntimeConfig{PerformanceOptimization: &runtimeconfig.PerformanceOptimizationConfig{Enabled: enabled, AgentIDs: []string{target}}})
		if cfg.AllowsAgent(id) != enabled {
			t.Fatalf("rollout eligibility ignored master enabled=%v", enabled)
		}
		selected := cfg.ForAgent(id)
		outside := cfg.ForAgent(other)
		for _, flag := range []bool{selected.DSHEventWakeup, selected.BoundDSHHostWait, selected.RecoverAbandonedLaunches, selected.DingTalkReplyCommand, selected.ASBEventWakeup, selected.StartupObservability, selected.BoundedReadyExec, selected.CoalescedHotExec, selected.BatchSkillResolve} {
			if flag != enabled {
				t.Fatalf("selected agent has mixed rollout after enabled=%v", enabled)
			}
		}
		if outside.DSHEventWakeup || outside.RecoverAbandonedLaunches || outside.BatchSkillResolve || outside.DingTalkReplyCommand {
			t.Fatal("non-target agent selected by performance rollout")
		}
	}
	if quickWinsForRuntime(runtimeconfig.RuntimeConfig{PerformanceOptimization: &runtimeconfig.PerformanceOptimizationConfig{Enabled: true}}).DSHEventWakeup {
		t.Fatal("empty allowlist must fail closed")
	}
}

func TestPerformanceRolloutObjectOverridesLegacyFields(t *testing.T) {
	cfg := quickWinsForRuntime(runtimeconfig.RuntimeConfig{
		FCE2B:                   runtimeconfig.FCE2BConfig{DSHEventWakeup: true, BatchSkillResolve: true},
		PerformanceOptimization: &runtimeconfig.PerformanceOptimizationConfig{Enabled: false},
	})
	if cfg.DSHEventWakeup || cfg.BatchSkillResolve {
		t.Fatal("old Diamond fields escaped the unified off switch")
	}
	cfg = quickWinsForRuntime(runtimeconfig.RuntimeConfig{FCE2B: runtimeconfig.FCE2BConfig{DSHEventWakeup: true, BatchSkillResolve: true}})
	if cfg.DSHEventWakeup || cfg.BatchSkillResolve {
		t.Fatal("missing unified rollout must fail closed even with old fields")
	}
}

func TestCoordinatorDecisionConfigComesFromOneSnapshot(t *testing.T) {
	const target = "e2293e9e-1e79-4926-b0e6-da4cb693add0"
	const other = "5b000000-0000-0000-0000-000000000000"
	build := func(parent, experiment bool, mode string) *appRuntimeConfig {
		t.Helper()
		raw, err := os.ReadFile("../../../docs/runtime-config.example.json")
		if err != nil {
			t.Fatal(err)
		}
		cfg, err := runtimeconfig.ParseStrict(raw, true)
		if err != nil {
			t.Fatal(err)
		}
		cfg.Runtime.LLM.CoordinatorModel = "qwen3.7-plus"
		cfg.Runtime.PerformanceOptimization = &runtimeconfig.PerformanceOptimizationConfig{
			Enabled:                parent,
			AgentIDs:               []string{target},
			FinishSchemaExperiment: &runtimeconfig.FinishSchemaExperimentConfig{Enabled: experiment, Mode: mode, Salt: "pri47"},
		}
		remote, err := runtimeconfig.NewStatic(cfg)
		if err != nil {
			t.Fatal(err)
		}
		return &appRuntimeConfig{remote: remote}
	}
	app := build(true, true, "")
	got := app.coordinatorDecisionConfig(target)
	snapshot := app.snapshot()
	if !got.Performance || !got.FinishSchema.Enabled || got.FinishSchema.Salt != "pri47" || got.Model != "qwen3.7-plus" || got.ConfigSHA256 != snapshot.SHA256 || got.ConfigGeneration != snapshot.Generation {
		t.Fatalf("the decision config is one snapshot of the switch, experiment, model and identity: %+v", got)
	}
	if stop := build(true, true, runtimeconfig.FinishSchemaModeExpanded).coordinatorDecisionConfig(target); !stop.Performance || stop.FinishSchema.Mode != runtimeconfig.FinishSchemaModeExpanded {
		t.Fatalf("the stop-loss keeps the switch on: %+v", stop)
	}
	for name, cfg := range map[string]inboundcoord.DecisionConfig{
		"parent off":        build(false, true, "").coordinatorDecisionConfig(target),
		"agent not allowed": app.coordinatorDecisionConfig(other),
	} {
		if cfg.Performance || cfg.FinishSchema.Enabled {
			t.Fatalf("%s must keep the switch and the experiment off: %+v", name, cfg)
		}
	}
	if off := build(true, false, "").coordinatorDecisionConfig(target); !off.Performance || off.FinishSchema.Enabled {
		t.Fatalf("experiment off keeps the switch only: %+v", off)
	}
}

func TestCoordinatorCollectQuietFollowsThePerformanceSwitch(t *testing.T) {
	const target = "e2293e9e-1e79-4926-b0e6-da4cb693add0"
	const other = "5b000000-0000-0000-0000-000000000000"
	build := func(parent bool, quiet int) *appRuntimeConfig {
		t.Helper()
		raw, err := os.ReadFile("../../../docs/runtime-config.example.json")
		if err != nil {
			t.Fatal(err)
		}
		cfg, err := runtimeconfig.ParseStrict(raw, true)
		if err != nil {
			t.Fatal(err)
		}
		cfg.Runtime.PerformanceOptimization = &runtimeconfig.PerformanceOptimizationConfig{Enabled: parent, AgentIDs: []string{target}, CollectQuietMS: quiet}
		remote, err := runtimeconfig.NewStatic(cfg)
		if err != nil {
			t.Fatal(err)
		}
		return &appRuntimeConfig{remote: remote}
	}
	if got := build(true, 1000).coordinatorCollectQuiet(target); got != time.Second {
		t.Fatalf("selected agent collects for 1 s, got %v", got)
	}
	for name, got := range map[string]time.Duration{
		"other agent": build(true, 1000).coordinatorCollectQuiet(other),
		"parent off":  build(false, 1000).coordinatorCollectQuiet(target),
		"unset":       build(true, 0).coordinatorCollectQuiet(target),
	} {
		if got != 0 {
			t.Fatalf("%s keeps the default window, got %v", name, got)
		}
	}
}
