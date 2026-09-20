package dshhost

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"
)

func TestEmployeeCreateSpecIsSingleMulticaMount(t *testing.T) {
	h := Host{
		Key: Key{WorkspaceID: uuid.New(), AgentID: uuid.New()}, Storage: Storage{VolumeName: "vol-employee", AccessPointARN: "ap", RoleARN: "acs:ram::1:role/employee"},
		State: "creating", Generation: 1, CreateIntent: uuid.New(), TemplateID: "template-1",
	}
	spec, err := employeeCreateSpec(h)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Scope != "employee" || len(spec.Mounts) != 1 || spec.Mounts[0] != (VolumeMountSpec{Name: "vol-employee", Path: MountPath}) {
		t.Fatalf("employee spec changed: %+v", spec)
	}
	if spec.valid() != nil {
		t.Fatal(spec.valid())
	}
}

func TestEmployeeCreateSpecRejectsWorkspaceMount(t *testing.T) {
	h := Host{
		Key: Key{WorkspaceID: uuid.New(), AgentID: uuid.New()}, Storage: Storage{VolumeName: "vol-employee", AccessPointARN: "ap", RoleARN: "role"},
		State: "creating", Generation: 1, CreateIntent: uuid.New(), TemplateID: "template-1",
	}
	spec, err := employeeCreateSpec(h)
	if err != nil {
		t.Fatal(err)
	}
	spec.Mounts = append(spec.Mounts, VolumeMountSpec{Name: "vol-shared", Path: WorkspaceMountPath})
	if spec.valid() == nil {
		t.Fatal("employee spec accepted a second mount")
	}
}

func TestMatchesSpecAcceptsTwoMountsAndRejectsListingRW(t *testing.T) {
	intent := uuid.New()
	ro := VolumeMountSpec{Name: "vol-ro", Path: WorkspaceMountPath}
	rw := VolumeMountSpec{Name: "vol-rw", Path: WorkspaceMountPath}
	private := VolumeMountSpec{Name: "vol-employee", Path: MountPath}
	readSpec := SandboxCreateSpec{
		WorkspaceID: uuid.New(), Scope: "wsfs-read", Generation: 1, CreateIntent: intent,
		TemplateID: "template-1", RoleARN: "role-ro", Mounts: []VolumeMountSpec{ro},
		Labels: map[string]string{"multica.wsfs.intent": intent.String(), "multica.wsfs.mode": "read"},
	}
	info := sandboxInfo{ID: "sbx-1", Template: "alias", Metadata: readSpec.Labels, Mounts: []volumeMount{{ro.Name, ro.Path}}}
	if !matchesSpec(info, readSpec) {
		t.Fatal("listing host did not match its RO volume")
	}
	info.Mounts = []volumeMount{{rw.Name, rw.Path}}
	if matchesSpec(info, readSpec) {
		t.Fatal("listing spec matched a RW volume name")
	}
	taskSpec := SandboxCreateSpec{
		WorkspaceID: uuid.New(), AgentID: uuid.New(), Scope: "task", Generation: 2, CreateIntent: uuid.New(),
		TemplateID: "template-1", RoleARN: "role-composite", Mounts: []VolumeMountSpec{private, VolumeMountSpec{Name: "vol-rw", Path: WorkspaceMountPath}},
		Labels: map[string]string{"multica.dsh.intent": "x"},
	}
	two := sandboxInfo{ID: "sbx-2", Template: "alias", Metadata: taskSpec.Labels, Mounts: []volumeMount{{"vol-rw", WorkspaceMountPath}, {private.Name, MountPath}}}
	if !matchesSpec(two, taskSpec) {
		t.Fatal("dual-mount task did not match unordered mounts")
	}
	two.Mounts = []volumeMount{{private.Name, MountPath}}
	if matchesSpec(two, taskSpec) {
		t.Fatal("dual-mount spec matched a single employee mount")
	}
}

func TestFCCreateEmployeePayloadStaysSingleMount(t *testing.T) {
	a, _ := stores(t)
	h := bind(t, a)
	h.State, h.Generation, h.CreateIntent, h.TemplateID = "creating", 1, uuid.New(), "template-1"
	p := fakeFC(t, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Mounts    []volumeMount     `json:"volumeMounts"`
			Metadata  map[string]string `json:"metadata"`
			AutoPause bool              `json:"autoPause"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if len(body.Mounts) != 1 || body.Mounts[0] != (volumeMount{h.VolumeName, MountPath}) || body.AutoPause {
			t.Errorf("DSH employee create payload changed: %+v", body)
		}
		if body.Metadata["fc.sandbox.auth.role"] != h.RoleARN || body.Metadata["multica.dsh.agent"] != h.AgentID.String() {
			t.Error("DSH identity labels changed")
		}
		_ = json.NewEncoder(w).Encode(sandboxInfo{ID: "sandbox-keep", Template: "alias", State: "running"})
	})
	id, err := p.Create(context.Background(), h)
	if err != nil || id != "sandbox-keep" {
		t.Fatalf("id=%s err=%v", id, err)
	}
}
