package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/dshhost"
)

func TestDSHHomeRejectsMachineCredentialsAndClientPlacement(t *testing.T) {
	h := &Handler{}
	for _, source := range []string{"task_token", "cloud_pat", "workspace_access_token"} {
		r := httptest.NewRequest(http.MethodPost, "/api/agents/id/dsh-home", nil)
		r.Header.Set("X-Actor-Source", source)
		w := httptest.NewRecorder()
		RequireHumanActor(http.HandlerFunc(h.EnsureDSHHome)).ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Fatalf("machine identity admitted: %s %d", source, w.Code)
		}
	}
	for _, body := range []string{`{"space_id":"someone-else"}`, `{"credential_resource":"other-account"}`, `{} {}`, `{"placement":{}}`} {
		r := httptest.NewRequest(http.MethodPost, "/api/agents/id/dsh-home", strings.NewReader(body))
		w := httptest.NewRecorder()
		h.EnsureDSHHome(w, r)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("client placement accepted: %d", w.Code)
		}
	}
}

func TestDSHHomeOwnerWorkflowAndProviderIsolation(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("requires the real preproduction database fixture")
	}
	runtimeID, ownerID, agentID := fce2bTemplateRotationFixture(t)
	h := *testHandler
	calls := 0
	h.ProvisionDSHStorage = func(ctx context.Context, key dshhost.Key) (dshhost.Host, error) {
		calls++
		return (dshhost.PostgresStore{DB: testPool}).BindStorage(ctx, key, dshhost.Storage{FileSystemID: "test-fs", SpaceID: "test-space-" + agentID, AccessPointARN: "test-ap-" + agentID, RoleARN: "test-role", VolumeName: "test-volume-" + agentID, VPCID: "test-vpc", SecurityGroupID: "test-sg", VSwitchIDs: []string{"test-vsw"}})
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM dsh_employee_host WHERE agent_id=$1`, agentID)
	})
	request := func(actor string) *httptest.ResponseRecorder {
		r := withURLParam(newRequestAs(actor, http.MethodPost, "/api/agents/"+agentID+"/dsh-home", nil), "id", agentID)
		w := httptest.NewRecorder()
		h.EnsureDSHHome(w, r)
		return w
	}
	if w := request(ownerID); w.Code != http.StatusConflict || calls != 0 {
		t.Fatal("non-DSH runtime triggered provisioning", w.Code)
	}
	if _, err := testPool.Exec(context.Background(), `UPDATE agent_runtime SET provider='dsh' WHERE id=$1`, runtimeID); err != nil {
		t.Fatal(err)
	}
	if w := request(ownerID); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"provisioned":true`) || calls != 1 {
		t.Fatalf("owner did not bind Home: %d %s", w.Code, w.Body.String())
	}
	if w := request(testUserID); w.Code != http.StatusOK || calls != 1 {
		t.Fatal("existing binding provisioned again", w.Code)
	}
	// A workspace member who does not own the employee must not create storage.
	_, _, memberID := runtimeVisibilityFixture(t)
	if w := request(memberID); w.Code != http.StatusForbidden || calls != 1 {
		t.Fatal("unrelated member was admitted", w.Code)
	}
}
