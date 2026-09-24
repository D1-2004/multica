package dshhost

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"
)

func TestWorkspaceWriteSpecIsSingleSharedMount(t *testing.T) {
	ws, intent := uuid.New(), uuid.New()
	spec, err := WorkspaceWriteSpec(ws, intent, 2, "template-1", "vol-rw", "acs:ram::1:role/rw")
	if err != nil {
		t.Fatal(err)
	}
	if spec.Scope != "wsfs-write" || spec.AgentID != uuid.Nil || spec.RoleARN == "" || len(spec.Mounts) != 1 || spec.Mounts[0] != (VolumeMountSpec{Name: "vol-rw", Path: WorkspaceSharedRoot}) {
		t.Fatalf("write spec: %+v", spec)
	}
	if spec.Labels["multica.wsfs.intent"] != intent.String() || spec.Labels["multica.wsfs.scope"] != "wsfs-write" {
		t.Fatalf("labels: %+v", spec.Labels)
	}
	if spec.valid() != nil {
		t.Fatal(spec.valid())
	}
	spec.Mounts = append(spec.Mounts, VolumeMountSpec{Name: "vol-employee", Path: MountPath})
	if spec.valid() == nil {
		t.Fatal("wsfs-write accepted an employee mount")
	}
}

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
		TemplateID: "template-1", RoleARN: "role-composite", Mounts: []VolumeMountSpec{private, VolumeMountSpec{Name: "vol-rw", Path: WorkspaceSharedRoot}},
		Labels: map[string]string{"multica.dsh.intent": "x"},
	}
	two := sandboxInfo{ID: "sbx-2", Template: "alias", Metadata: taskSpec.Labels, Mounts: []volumeMount{{"vol-rw", WorkspaceSharedRoot}, {private.Name, MountPath}}}
	if !matchesSpec(two, taskSpec) {
		t.Fatal("dual-mount task did not match unordered mounts")
	}
	two.Mounts = []volumeMount{{private.Name, MountPath}}
	if matchesSpec(two, taskSpec) {
		t.Fatal("dual-mount spec matched a single employee mount")
	}
}

func TestFCCreateTimeoutFloorsConfigured3600To4800(t *testing.T) {
	h := Host{
		Key: Key{WorkspaceID: uuid.New(), AgentID: uuid.New()}, Storage: Storage{VolumeName: "vol-employee", AccessPointARN: "ap", RoleARN: "acs:ram::1:role/employee"},
		State: "creating", Generation: 1, CreateIntent: uuid.New(), TemplateID: "template-1",
	}
	p := fakeFC(t, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Timeout int `json:"timeout"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.Timeout != DefaultSandboxTaskTimeoutSeconds {
			t.Errorf("employee create timeout=%d want %d", body.Timeout, DefaultSandboxTaskTimeoutSeconds)
		}
		_ = json.NewEncoder(w).Encode(sandboxInfo{ID: "sandbox-ttl", Template: "alias", State: "running"})
	})
	p.config.TimeoutSeconds = 3600
	id, err := p.Create(context.Background(), h)
	if err != nil || id != "sandbox-ttl" {
		t.Fatalf("id=%s err=%v", id, err)
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
			Timeout   int               `json:"timeout"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if len(body.Mounts) != 1 || body.Mounts[0] != (volumeMount{h.VolumeName, MountPath}) || body.AutoPause {
			t.Errorf("DSH employee create payload changed: %+v", body)
		}
		if body.Timeout != DefaultSandboxTaskTimeoutSeconds {
			t.Errorf("employee create timeout=%d", body.Timeout)
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

func TestMatchesAcceptsDualMountSandboxForSameEmployeeIntent(t *testing.T) {
	h := Host{
		Key: Key{WorkspaceID: uuid.New(), AgentID: uuid.New()}, Storage: Storage{VolumeName: "vol-employee", AccessPointARN: "ap", RoleARN: "acs:ram::1:role/employee"},
		State: "creating", Generation: 1, CreateIntent: uuid.New(), TemplateID: "template-1",
	}
	spec, err := DualCreateSpec(h, VolumeMountSpec{Name: "vol-rw"}, "role-composite")
	if err != nil {
		t.Fatal(err)
	}
	meta := map[string]string{}
	for k, v := range spec.Labels {
		meta[k] = v
	}
	meta["fc.sandbox.auth.role"] = spec.RoleARN
	dual := sandboxInfo{ID: "sbx-dual", Template: "alias", Metadata: meta, Mounts: []volumeMount{{h.VolumeName, MountPath}, {"vol-rw", WorkspaceSharedRoot}}}
	if !matches(dual, h) {
		t.Fatal("dual-mount sandbox with the employee create intent must reconcile")
	}
	single := sandboxInfo{ID: "sbx-1", Template: "alias", Metadata: identity(h), Mounts: []volumeMount{{h.VolumeName, MountPath}}}
	if !matches(single, h) {
		t.Fatal("single employee mount must still match")
	}
}

func TestDualCreateSpecMountsSharedRootAndKeepsEmployeeVolume(t *testing.T) {
	h := Host{
		Key: Key{WorkspaceID: uuid.New(), AgentID: uuid.New()}, Storage: Storage{VolumeName: "vol-employee", AccessPointARN: "ap", RoleARN: "acs:ram::1:role/employee"},
		State: "creating", Generation: 1, CreateIntent: uuid.New(), TemplateID: "template-1",
	}
	spec, err := DualCreateSpec(h, VolumeMountSpec{Name: "vol-rw", Path: "/ignored"}, "role-composite")
	if err != nil {
		t.Fatal(err)
	}
	if spec.Scope != "task" || spec.RoleARN != "role-composite" || len(spec.Mounts) != 2 {
		t.Fatalf("dual spec: %+v", spec)
	}
	if spec.Mounts[0].Path != MountPath || spec.Mounts[1] != (VolumeMountSpec{Name: "vol-rw", Path: WorkspaceSharedRoot}) {
		t.Fatalf("mounts: %+v", spec.Mounts)
	}
	employee, err := employeeCreateSpec(h)
	if err != nil || len(employee.Mounts) != 1 || employee.Mounts[0].Path != MountPath {
		t.Fatalf("employee spec changed: %+v", employee)
	}
}

func TestFCCreateDualMountPayload(t *testing.T) {
	a, _ := stores(t)
	h := bind(t, a)
	h.State, h.Generation, h.CreateIntent, h.TemplateID = "creating", 1, uuid.New(), "template-1"
	h.ExtraMounts = []VolumeMountSpec{{Name: "vol-rw", Path: WorkspaceSharedRoot}}
	h.AuthRoleARN = "acs:ram::1:role/composite"
	p := fakeFC(t, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Mounts   []volumeMount     `json:"volumeMounts"`
			Metadata map[string]string `json:"metadata"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if len(body.Mounts) != 2 || body.Mounts[0] != (volumeMount{h.VolumeName, MountPath}) || body.Mounts[1] != (volumeMount{"vol-rw", WorkspaceSharedRoot}) {
			t.Errorf("dual-mount payload: %+v", body.Mounts)
		}
		if body.Metadata["fc.sandbox.auth.role"] != h.AuthRoleARN || body.Metadata["multica.dsh.agent"] != h.AgentID.String() {
			t.Error("composite role or employee identity missing")
		}
		if body.Metadata["multica.wsfs.volume"] != "vol-rw" {
			t.Error("missing shared volume label")
		}
		_ = json.NewEncoder(w).Encode(sandboxInfo{ID: "sandbox-dual", Template: "alias", State: "running"})
	})
	id, err := p.Create(context.Background(), h)
	if err != nil || id != "sandbox-dual" {
		t.Fatalf("id=%s err=%v", id, err)
	}
}
