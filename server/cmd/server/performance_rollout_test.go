package main

import (
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
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

func TestFinishSchemaExperimentFollowsThePerformanceSwitch(t *testing.T) {
	const target = "e2293e9e-1e79-4926-b0e6-da4cb693add0"
	const other = "5b000000-0000-0000-0000-000000000000"
	build := func(parent, experiment bool) *appRuntimeConfig {
		t.Helper()
		raw, err := os.ReadFile("../../../docs/runtime-config.example.json")
		if err != nil {
			t.Fatal(err)
		}
		cfg, err := runtimeconfig.ParseStrict(raw, true)
		if err != nil {
			t.Fatal(err)
		}
		cfg.Runtime.PerformanceOptimization = &runtimeconfig.PerformanceOptimizationConfig{
			Enabled:                parent,
			AgentIDs:               []string{target},
			FinishSchemaExperiment: &runtimeconfig.FinishSchemaExperimentConfig{Enabled: experiment, Salt: "pri47"},
		}
		remote, err := runtimeconfig.NewStatic(cfg)
		if err != nil {
			t.Fatal(err)
		}
		return &appRuntimeConfig{remote: remote}
	}
	got := build(true, true).finishSchemaExperiment(target)
	if !got.Enabled || got.Salt != "pri47" || got.ConfigSHA256 == "" {
		t.Fatalf("the experiment runs for an agent under the performance switch: %+v", got)
	}
	for name, experiment := range map[string]bool{
		"parent off":        build(false, true).finishSchemaExperiment(target).Enabled,
		"agent not allowed": build(true, true).finishSchemaExperiment(other).Enabled,
		"experiment off":    build(true, false).finishSchemaExperiment(target).Enabled,
	} {
		if experiment {
			t.Fatalf("%s must keep the experiment off", name)
		}
	}
}
