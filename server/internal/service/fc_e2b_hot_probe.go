package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type hotRunnerProbeKey struct{}
type hotRunnerProbe struct {
	sandboxID string
	launch    fcE2BRunnerLaunch
	err       error
}

// One successful fixed capability command proves both exec readiness and the
// executable entrypoint. Only an invocation-local receipt may skip the second
// probe; a replaced sandbox or later task always gets its own proof.
func (l *FCE2BLauncher) probeHotRunner(ctx context.Context, sandboxID string, rt db.AgentRuntime) error {
	hint, err := fcE2BRunnerLaunchForRuntime(rt)
	if err != nil {
		// Invalid metadata must fail at the normal capability boundary, not
		// classify a reachable sandbox as dead and destroy it.
		if receipt, ok := ctx.Value(hotRunnerProbeKey{}).(*hotRunnerProbe); ok {
			*receipt = hotRunnerProbe{sandboxID: sandboxID, err: err}
		}
		return l.checkSandboxReady(ctx, sandboxID)
	}
	root, err := fcE2BRunnerLaunchForMode(FCE2BRuntimeProvider(rt), fcE2BRunnerLaunchRootLog)
	if err != nil {
		return err
	}
	candidates := []fcE2BRunnerLaunch{root}
	if legacy, err := fcE2BRunnerLaunchForMode(FCE2BRuntimeProvider(rt), fcE2BRunnerLaunchLegacyUser); err == nil {
		candidates = append(candidates, legacy)
		if hint.Mode == legacy.Mode {
			candidates[0], candidates[1] = candidates[1], candidates[0]
		}
	}
	var script strings.Builder
	for _, candidate := range candidates {
		fmt.Fprintf(&script, "if /usr/bin/test -x '%s'; then printf '%%s' '%s'; exit 0; fi; ", candidate.Command, candidate.Mode)
	}
	script.WriteString("printf runner-unavailable; exit 0")
	if l.Config.QuickWins.BoundedReadyExec {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.WithValue(ctx, boundedExecKey{}, true), 5*time.Second)
		defer cancel()
	}
	out, err := l.runE2BCommand(ctx, []string{"sandbox", "exec", "--user", "user", sandboxID, "--", "/bin/sh", "-c", script.String()})
	if err != nil {
		return err
	}
	for _, candidate := range candidates {
		if strings.TrimSpace(out) == string(candidate.Mode) {
			if receipt, ok := ctx.Value(hotRunnerProbeKey{}).(*hotRunnerProbe); ok {
				*receipt = hotRunnerProbe{sandboxID: sandboxID, launch: candidate}
			}
			return nil
		}
	}
	if receipt, ok := ctx.Value(hotRunnerProbeKey{}).(*hotRunnerProbe); ok {
		*receipt = hotRunnerProbe{sandboxID: sandboxID, err: fmt.Errorf("FC/E2B runner entrypoint unavailable")}
	}
	return nil
}

func (l *FCE2BLauncher) checkReusedSandboxReady(ctx context.Context, sandboxID string, runtime db.AgentRuntime) error {
	if l.Config.QuickWins.CoalescedHotExec {
		return l.probeHotRunner(ctx, sandboxID, runtime)
	}
	return l.checkSandboxReady(ctx, sandboxID)
}
