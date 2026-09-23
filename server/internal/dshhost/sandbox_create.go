package dshhost

import (
	"errors"
	"sort"
	"strconv"

	"github.com/google/uuid"
)

// WorkspaceMountPath is the listing/write-host mount. Employee Homes stay at
// MountPath (/mnt/multica). A DSH sandbox without a populated task_role_arn
// must never receive a second mount.
const WorkspaceMountPath = "/mnt/workspace"

// WorkspaceSharedRoot is the task dual-mount path and MULTICA_WORKSPACE_FS_ROOT.
// Shared Access Points root at the AgenticSpace root (AgenticFS cannot set an
// AP root directory), so this path is the team file tree.
const WorkspaceSharedRoot = "/mnt/workspace/shared"

// VolumeMountSpec is one FC volumeMounts entry. RoleARN and ReadOnly are
// unused on today's single fc.sandbox.auth.role create path.
type VolumeMountSpec struct {
	Name     string
	Path     string
	RoleARN  string
	ReadOnly bool
}

// SandboxCreateSpec is the generalized FC create input. Employee Create is
// the single-mount /mnt/multica specialization and must stay byte-compatible.
type SandboxCreateSpec struct {
	WorkspaceID  uuid.UUID
	AgentID      uuid.UUID
	Scope        string
	Generation   int64
	CreateIntent uuid.UUID
	TemplateID   string
	RoleARN      string
	Mounts       []VolumeMountSpec
	Labels       map[string]string
}

func employeeCreateSpec(h Host) (SandboxCreateSpec, error) {
	if h.CreateIntent == uuid.Nil || h.WorkspaceID == uuid.Nil || h.AgentID == uuid.Nil || h.Generation < 1 ||
		h.VolumeName == "" || h.AccessPointARN == "" || h.RoleARN == "" || !sandboxIDPattern.MatchString(h.TemplateID) {
		return SandboxCreateSpec{}, errors.New("invalid persisted DSH FC create intent")
	}
	return SandboxCreateSpec{
		WorkspaceID:  h.WorkspaceID,
		AgentID:      h.AgentID,
		Scope:        "employee",
		Generation:   h.Generation,
		CreateIntent: h.CreateIntent,
		TemplateID:   h.TemplateID,
		RoleARN:      h.RoleARN,
		Mounts:       []VolumeMountSpec{{Name: h.VolumeName, Path: MountPath}},
		Labels:       identity(h),
	}, nil
}

func (s SandboxCreateSpec) valid() error {
	if s.WorkspaceID == uuid.Nil || s.CreateIntent == uuid.Nil || s.Generation < 1 || s.RoleARN == "" ||
		!sandboxIDPattern.MatchString(s.TemplateID) || len(s.Mounts) == 0 || len(s.Mounts) > 2 {
		return errors.New("invalid FC sandbox create spec")
	}
	if s.Scope == "employee" && s.AgentID == uuid.Nil {
		return errors.New("employee FC create requires an agent")
	}
	seenPath := map[string]bool{}
	for _, m := range s.Mounts {
		if m.Name == "" || (m.Path != MountPath && m.Path != WorkspaceMountPath && m.Path != WorkspaceSharedRoot) || seenPath[m.Path] {
			return errors.New("invalid FC volume mount spec")
		}
		seenPath[m.Path] = true
	}
	if s.Scope == "employee" && (len(s.Mounts) != 1 || s.Mounts[0].Path != MountPath) {
		return errors.New("employee FC create must mount only /mnt/multica")
	}
	if s.Scope == "task" {
		if s.AgentID == uuid.Nil || len(s.Mounts) != 2 {
			return errors.New("dual-mount task create requires employee and shared volumes")
		}
		hasPrivate, hasShared := false, false
		for _, m := range s.Mounts {
			if m.Path == MountPath {
				hasPrivate = true
			}
			if m.Path == WorkspaceSharedRoot {
				hasShared = true
			}
		}
		if !hasPrivate || !hasShared {
			return errors.New("dual-mount task create must mount /mnt/multica and /mnt/workspace/shared")
		}
	}
	if (s.Scope == "wsfs-read" || s.Scope == "wsfs-write") && (s.AgentID != uuid.Nil || len(s.Mounts) != 1 || (s.Mounts[0].Path != WorkspaceMountPath && s.Mounts[0].Path != WorkspaceSharedRoot)) {
		return errors.New("workspace filesystem host must mount only the shared workspace path")
	}
	return nil
}

// WorkspaceWriteSpec is the wsfs-write host: one read-write shared volume and
// the workspace RW role. It is not an employee sandbox and must not mount
// /mnt/multica.
func WorkspaceWriteSpec(workspaceID, intent uuid.UUID, generation int64, template, volume, role string) (SandboxCreateSpec, error) {
	spec := SandboxCreateSpec{
		WorkspaceID: workspaceID, Scope: "wsfs-write", Generation: generation, CreateIntent: intent,
		TemplateID: template, RoleARN: role,
		Mounts: []VolumeMountSpec{{Name: volume, Path: WorkspaceSharedRoot}},
		Labels: map[string]string{
			"multica.wsfs.intent":     intent.String(),
			"multica.wsfs.workspace":  workspaceID.String(),
			"multica.wsfs.scope":      "wsfs-write",
			"multica.wsfs.generation": strconv.FormatInt(generation, 10),
			"multica.wsfs.volume":     volume,
			"multica.wsfs.mount":      WorkspaceSharedRoot,
			"multica.wsfs.role-arn":   role,
		},
	}
	return spec, spec.valid()
}

func DualCreateSpec(h Host, shared VolumeMountSpec, authRole string) (SandboxCreateSpec, error) {
	spec, err := employeeCreateSpec(h)
	if err != nil {
		return SandboxCreateSpec{}, err
	}
	shared.Path = WorkspaceSharedRoot
	if shared.Name == "" || shared.Name == h.VolumeName || authRole == "" {
		return SandboxCreateSpec{}, errors.New("invalid shared volume for dual-mount create")
	}
	spec.Scope = "task"
	spec.Mounts = []VolumeMountSpec{spec.Mounts[0], shared}
	spec.RoleARN = authRole
	spec.Labels["multica.wsfs.volume"] = shared.Name
	spec.Labels["multica.wsfs.mount"] = shared.Path
	spec.Labels["multica.wsfs.role-arn"] = authRole
	return spec, spec.valid()
}

func matchesSpec(info sandboxInfo, spec SandboxCreateSpec) bool {
	if spec.valid() != nil || !sandboxIDPattern.MatchString(info.ID) || info.Template == "" {
		return false
	}
	for key, value := range spec.Labels {
		if info.Metadata[key] != value {
			return false
		}
	}
	if len(info.Mounts) != len(spec.Mounts) {
		return false
	}
	want := make([]volumeMount, len(spec.Mounts))
	for i, m := range spec.Mounts {
		want[i] = volumeMount{Name: m.Name, Path: m.Path}
	}
	got := append([]volumeMount(nil), info.Mounts...)
	sortMounts(want)
	sortMounts(got)
	for i := range want {
		if want[i] != got[i] {
			return false
		}
	}
	if spec.Scope == "wsfs-read" && spec.RoleARN == "" {
		return false
	}
	return true
}

func sortMounts(mounts []volumeMount) {
	sort.Slice(mounts, func(i, j int) bool {
		if mounts[i].Path == mounts[j].Path {
			return mounts[i].Name < mounts[j].Name
		}
		return mounts[i].Path < mounts[j].Path
	})
}
