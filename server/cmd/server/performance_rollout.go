package main

import (
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/pkg/runtimeconfig"
)

// quickWins translates the one public Diamond rollout into the internal
// feature switches. Old fields remain decode-only for rolling deployment;
// their values never enable this binary without an explicit rollout.
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
	return service.RuntimeStartRecoveryConfig{}
}
