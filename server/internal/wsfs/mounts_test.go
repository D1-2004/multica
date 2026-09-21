package wsfs

import (
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/dshhost"
)

func testEmployee() *dshhost.Host {
	return &dshhost.Host{
		Key:     dshhost.Key{WorkspaceID: uuid.New(), AgentID: uuid.New()},
		Storage: dshhost.Storage{VolumeName: "vol-employee", RoleARN: "role-employee"},
	}
}

func testBinding() *Binding {
	return &Binding{
		ROVolumeName: "vol-ro", RWVolumeName: "vol-rw",
		RORoleARN: "role-ro", RWRoleARN: "role-rw",
	}
}

func TestSelectVolumeMountsEmployeeEmptyTaskRoleKeepsPrivateOnly(t *testing.T) {
	employee := testEmployee()
	got := SelectVolumeMounts(employee, Grant{Access: AccessWrite, TaskRoleARN: ""}, testBinding())
	if got.Private == nil || got.Private.VolumeName != "vol-employee" || got.Shared != nil || got.RoleARN != "role-employee" {
		t.Fatalf("DSH employee sandbox would change: %+v", got)
	}
}

func TestSelectVolumeMountsEmployeeNoneKeepsPrivateOnly(t *testing.T) {
	employee := testEmployee()
	got := SelectVolumeMounts(employee, Grant{}, testBinding())
	if got.Shared != nil || got.RoleARN != "role-employee" {
		t.Fatalf("default none grant mounted shared: %+v", got)
	}
}

func TestSelectVolumeMountsEmployeeWriteWithTaskRoleAddsShared(t *testing.T) {
	employee := testEmployee()
	got := SelectVolumeMounts(employee, Grant{Access: AccessWrite, TaskRoleARN: "role-composite"}, testBinding())
	if got.Shared == nil || got.Shared.Name != "vol-rw" || got.Shared.Path != dshhost.WorkspaceSharedRoot || got.RoleARN != "role-composite" || got.Access != AccessWrite {
		t.Fatalf("expected dual mount with composite role: %+v", got)
	}
	if got.Private == nil || got.Private.VolumeName != "vol-employee" {
		t.Fatal("lost employee volume")
	}
}

func TestSelectVolumeMountsNeverUsesAnotherAgentsVolume(t *testing.T) {
	employee := testEmployee()
	binding := testBinding()
	binding.RWVolumeName = employee.VolumeName
	got := SelectVolumeMounts(employee, Grant{Access: AccessWrite, TaskRoleARN: "role-composite"}, binding)
	if got.Shared != nil {
		t.Fatal("shared volume reused the employee volume name")
	}
}

func TestSelectVolumeMountsNoPrivateUsesWorkspaceRole(t *testing.T) {
	got := SelectVolumeMounts(nil, Grant{Access: AccessWrite}, testBinding())
	if got.Private != nil || got.Shared == nil || got.Shared.Name != "vol-rw" || got.RoleARN != "role-rw" {
		t.Fatalf("no-private write: %+v", got)
	}
	got = SelectVolumeMounts(nil, Grant{Access: AccessRead}, testBinding())
	if got.Shared == nil || got.Shared.Name != "vol-ro" || got.RoleARN != "role-ro" {
		t.Fatalf("no-private read: %+v", got)
	}
	got = SelectVolumeMounts(nil, Grant{}, testBinding())
	if got.Shared != nil || got.Private != nil {
		t.Fatalf("no-private none: %+v", got)
	}
}
