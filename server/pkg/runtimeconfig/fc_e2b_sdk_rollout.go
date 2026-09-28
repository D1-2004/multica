package runtimeconfig

import (
	"fmt"

	"github.com/google/uuid"
)

// FCE2BSDKRollout is runtime.fc_e2b_sdk_rollout, the single switch of the
// FC/E2B SDK change. It sits beside runtime.performance_optimization and
// shares nothing with it. A scope it selects sends FC/E2B commands through
// the Go SDK instead of the e2b CLI, marks the task's runner with
// FC_E2B_TASK_ID, and ends a cancelled task's processes in its sandboxes.
// Absent, or with enabled false, every operation keeps the CLI and a
// cancelled task's processes are left to the sandbox release, as before.
//
// The runtime document rejects unknown fields, so a binary that predates
// this key refuses a document that carries it: publish the key only after
// every replica runs a binary that knows it.
type FCE2BSDKRollout struct {
	// Enabled is the master switch; false selects nothing whatever the lists
	// say.
	Enabled      bool     `json:"enabled"`
	WorkspaceIDs []string `json:"workspace_ids,omitempty"`
	AgentIDs     []string `json:"agent_ids,omitempty"`
	RuntimeIDs   []string `json:"runtime_ids,omitempty"`
	// Percent buckets scoped operations by agent, then runtime, then
	// workspace. 100 also selects operations that carry no scope.
	Percent int `json:"percent,omitempty"`
}

func (r FCE2BSDKRollout) validate() error {
	if r.Percent < 0 || r.Percent > 100 {
		return fmt.Errorf("fc_e2b_sdk_rollout.percent must be between 0 and 100")
	}
	for _, list := range []struct {
		name string
		ids  []string
	}{{"workspace_ids", r.WorkspaceIDs}, {"agent_ids", r.AgentIDs}, {"runtime_ids", r.RuntimeIDs}} {
		if err := validateUnique("fc_e2b_sdk_rollout."+list.name, list.ids, false); err != nil {
			return err
		}
		for _, id := range list.ids {
			if parsed, err := uuid.Parse(id); err != nil || parsed == uuid.Nil || parsed.String() != id {
				return fmt.Errorf("fc_e2b_sdk_rollout.%s contains invalid canonical UUID %q", list.name, id)
			}
		}
	}
	return nil
}

func (r *FCE2BSDKRollout) clone() *FCE2BSDKRollout {
	if r == nil {
		return nil
	}
	out := *r
	out.WorkspaceIDs = append([]string(nil), r.WorkspaceIDs...)
	out.AgentIDs = append([]string(nil), r.AgentIDs...)
	out.RuntimeIDs = append([]string(nil), r.RuntimeIDs...)
	return &out
}
