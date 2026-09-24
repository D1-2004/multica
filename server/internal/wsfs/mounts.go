// Package wsfs implements workspace-shared filesystem grants and mount
// selection. Employee DSH sandboxes stay on a single /mnt/multica mount
// unless a grant generation has a task_role_arn (composite Role).
package wsfs

import (
	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/dshhost"
)

const (
	AccessNone  = "none"
	AccessRead  = "read"
	AccessWrite = "write"
)

// Binding is the immutable workspace filesystem cloud identity.
type Binding struct {
	WorkspaceID   uuid.UUID
	ROVolumeName  string
	RWVolumeName  string
	RORoleARN     string
	RWRoleARN     string
	ROAccessPoint string
	RWAccessPoint string
}

// Grant is the current shared-disk permission for one agent. A missing row
// is AccessNone with empty TaskRoleARN, which must not add a shared mount
// to an employee sandbox.
type Grant struct {
	WorkspaceID uuid.UUID `json:"workspace_id"`
	AgentID     uuid.UUID `json:"agent_id"`
	Access      string    `json:"access"`
	Generation  int64     `json:"generation"`
	TaskRoleARN string    `json:"task_role_arn"`
	// Persisted is false when no grant row exists. That is not a revoke.
	Persisted bool `json:"-"`
}

// Revoked reports an explicit none grant. A missing row is not a revoke.
func (g Grant) Revoked() bool {
	return g.Persisted && g.effectiveAccess() == AccessNone
}

func (g Grant) effectiveAccess() string {
	if g.Access == AccessRead || g.Access == AccessWrite {
		return g.Access
	}
	return AccessNone
}

// MountDecision is what a launcher may pass to FC. Manager.Ensure and
// EnsurePrivate use only the employee Host; EnsureWithShared adds Shared with
// the composite role.
type MountDecision struct {
	Private *dshhost.Host
	Shared  *dshhost.VolumeMountSpec
	RoleARN string
	Access  string
	// Revoked is an explicit none grant. Launch must drop a shared mount the
	// sandbox still carries. A missing grant is not revoked.
	Revoked bool
}

// SelectVolumeMounts implements the launch matrix. Empty TaskRoleARN on an
// employee host is a hard gate: only /mnt/multica, employee Role. This keeps
// existing DSH users on the historical single-mount create path.
func SelectVolumeMounts(employee *dshhost.Host, grant Grant, binding *Binding) MountDecision {
	access := grant.effectiveAccess()
	if employee != nil {
		out := MountDecision{Private: employee, RoleARN: employee.RoleARN}
		if binding == nil || access == AccessNone || grant.TaskRoleARN == "" {
			return out
		}
		shared := sharedMount(*binding, access)
		if shared.Name == "" || shared.Name == employee.VolumeName {
			return out
		}
		out.Shared = &shared
		out.RoleARN = grant.TaskRoleARN
		out.Access = access
		return out
	}
	if binding == nil || access == AccessNone {
		return MountDecision{}
	}
	shared := sharedMount(*binding, access)
	role := binding.RORoleARN
	if access == AccessWrite {
		role = binding.RWRoleARN
	}
	return MountDecision{Shared: &shared, RoleARN: role, Access: access}
}

func sharedMount(binding Binding, access string) dshhost.VolumeMountSpec {
	name := binding.ROVolumeName
	if access == AccessWrite {
		name = binding.RWVolumeName
	}
	return dshhost.VolumeMountSpec{Name: name, Path: dshhost.WorkspaceSharedRoot}
}
