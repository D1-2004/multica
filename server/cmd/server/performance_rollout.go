package main

import (
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/pkg/runtimeconfig"
)

// quickWins translates the one public Diamond rollout into the internal
// feature switches. Old snapshots keep their individual values only until
// the document is migrated; a present rollout always takes precedence.
func (c *appRuntimeConfig) quickWins() service.RuntimeStartRecoveryConfig {
	return quickWinsForRuntime(c.current().Runtime)
}

func quickWinsForRuntime(raw runtimeconfig.RuntimeConfig) service.RuntimeStartRecoveryConfig {
	if rollout := raw.PerformanceOptimization; rollout != nil {
		on := rollout.Enabled && len(rollout.AgentIDs) > 0
		return service.RuntimeStartRecoveryConfig{
			Scoped: true, RolloutAgentIDs: append([]string(nil), rollout.AgentIDs...),
			DSHEventWakeup: on, BoundDSHHostWait: on,
			RecoverAbandonedLaunches: on, DingTalkReplyCommand: on,
			ASBEventWakeup: on, StartupObservability: on,
			BoundedReadyExec: on, CoalescedHotExec: on,
			BatchSkillResolve: on,
		}
	}
	fc := raw.FCE2B
	return service.RuntimeStartRecoveryConfig{
		RecoverAbandonedLaunches: fc.RecoverAbandonedLaunches,
		BoundDSHHostWait:         fc.BoundDSHHostWait,
		DSHEventWakeup:           fc.DSHEventWakeup,
		DingTalkReplyCommand:     fc.DingTalkReplyCommand,
		ASBEventWakeup:           fc.ASBEventWakeup,
		StartupObservability:     fc.StartupObservability,
		BoundedReadyExec:         fc.BoundedReadyExec,
		CoalescedHotExec:         fc.CoalescedHotExec,
		BatchSkillResolve:        fc.BatchSkillResolve,
	}
}
