package runtimeconfig

import (
	"fmt"

	"github.com/google/uuid"
)

const (
	// DefaultFCE2BConnectionReuseTasks is the per-sandbox concurrency when
	// max_concurrent_tasks is omitted or zero.
	DefaultFCE2BConnectionReuseTasks = 6
	maxFCE2BConnectionReuseTasks     = 50
)

// FCE2BConnectionReuse is runtime.fc_e2b.connection_reuse. It lets tasks that
// share a scene and trigger reuse one sandbox. Enabled with empty target
// lists selects nobody: a gray target must be named. Absent, or enabled
// false, keeps one sandbox per chat or issue.
//
// The runtime document rejects unknown fields, so a binary that predates
// this key refuses a document that carries it. Publish the key only after
// every replica runs a binary that knows it.
type FCE2BConnectionReuse struct {
	Enabled bool `json:"enabled"`
	// MaxConcurrentTasks is how many tasks may share the sandbox. Zero uses
	// DefaultFCE2BConnectionReuseTasks. Otherwise it must be 1–50.
	MaxConcurrentTasks int      `json:"max_concurrent_tasks,omitempty"`
	WorkspaceIDs       []string `json:"workspace_ids,omitempty"`
	AgentIDs           []string `json:"agent_ids,omitempty"`
}

// Allows reports whether this workspace or agent is in the gray set.
func (r FCE2BConnectionReuse) Allows(workspaceID, agentID string) bool {
	if !r.Enabled {
		return false
	}
	if agentID != "" {
		for _, id := range r.AgentIDs {
			if id == agentID {
				return true
			}
		}
	}
	if workspaceID != "" {
		for _, id := range r.WorkspaceIDs {
			if id == workspaceID {
				return true
			}
		}
	}
	return false
}

// Concurrency is the per-sandbox task cap. Zero means the default.
func (r FCE2BConnectionReuse) Concurrency() int {
	if r.MaxConcurrentTasks <= 0 {
		return DefaultFCE2BConnectionReuseTasks
	}
	return r.MaxConcurrentTasks
}

func (r FCE2BConnectionReuse) validate() error {
	if r.MaxConcurrentTasks != 0 && (r.MaxConcurrentTasks < 1 || r.MaxConcurrentTasks > maxFCE2BConnectionReuseTasks) {
		return fmt.Errorf("fc_e2b.connection_reuse.max_concurrent_tasks must be between 1 and %d", maxFCE2BConnectionReuseTasks)
	}
	for _, list := range []struct {
		name string
		ids  []string
	}{{"workspace_ids", r.WorkspaceIDs}, {"agent_ids", r.AgentIDs}} {
		if err := validateUnique("fc_e2b.connection_reuse."+list.name, list.ids, false); err != nil {
			return err
		}
		for _, id := range list.ids {
			parsed, err := uuid.Parse(id)
			if err != nil || parsed == uuid.Nil || parsed.String() != id {
				return fmt.Errorf("fc_e2b.connection_reuse.%s contains invalid canonical UUID %q", list.name, id)
			}
		}
	}
	return nil
}

func (r *FCE2BConnectionReuse) clone() *FCE2BConnectionReuse {
	if r == nil {
		return nil
	}
	out := *r
	out.WorkspaceIDs = append([]string(nil), r.WorkspaceIDs...)
	out.AgentIDs = append([]string(nil), r.AgentIDs...)
	return &out
}
