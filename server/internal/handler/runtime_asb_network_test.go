package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/multica-ai/multica/server/internal/service"
)

func TestASBNetworkPolicyAdminSaveReadAndValidation(t *testing.T) {
	if testHandler == nil {
		t.Skip("database unavailable")
	}
	runtimeID, runtimeOwnerID, memberID := runtimeVisibilityFixture(t)
	_, err := testPool.Exec(context.Background(), `UPDATE agent_runtime SET runtime_mode='cloud', provider='hermes', metadata = '{"kind":"cloud-sandbox","sandbox_backend":"asb","artifact_kind":"oci_image","artifact_ref":"registry.example/image@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","preserved":"yes"}' WHERE id=$1`, runtimeID)
	if err != nil {
		t.Fatal(err)
	}
	h := *testHandler
	h.cfg.ASB = service.ASBConfig{Enabled: true, ServerURL: "https://multica.example", LLMBaseURL: "https://llm.example"}
	h.ASBLauncher = &service.ASBLauncher{Config: h.cfg.ASB, Queries: h.Queries, Pool: testPool}
	put := func(actor string, body any) *httptest.ResponseRecorder {
		req := withURLParam(newRequestAs(actor, http.MethodPut, "/api/runtimes/"+runtimeID+"/asb-network-policy", body), "runtimeId", runtimeID)
		w := httptest.NewRecorder()
		h.UpdateASBRuntimeNetworkPolicy(w, req)
		return w
	}
	for _, actor := range []string{memberID, runtimeOwnerID} {
		if w := put(actor, map[string]any{"custom_targets": []string{"unauthorized.example"}}); w.Code != http.StatusForbidden {
			t.Fatalf("non-admin status=%d body=%s", w.Code, w.Body.String())
		}
	}
	for _, body := range []any{map[string]any{}, map[string]any{"custom_targets": nil}, map[string]any{"custom_targets": []string{"*.alibaba-inc.com"}}, map[string]any{"custom_targets": []string{}, "default_action": "allow"}} {
		if w := put(testUserID, body); w.Code != http.StatusBadRequest {
			t.Fatalf("invalid body status=%d body=%s", w.Code, w.Body.String())
		}
	}
	w := put(testUserID, map[string]any{"custom_targets": []string{" CUSTOM.example. ", "custom.example"}})
	if w.Code != http.StatusOK {
		t.Fatalf("save status=%d body=%s", w.Code, w.Body.String())
	}
	var settings service.ASBNetworkPolicySettings
	if err = json.Unmarshal(w.Body.Bytes(), &settings); err != nil {
		t.Fatal(err)
	}
	if settings.DefaultAction != "deny" || len(settings.CustomTargets) != 1 || settings.CustomTargets[0] != "custom.example" {
		t.Fatalf("settings=%+v", settings)
	}
	var preserved string
	if err = testPool.QueryRow(context.Background(), `SELECT metadata->>'preserved' FROM agent_runtime WHERE id=$1`, runtimeID).Scan(&preserved); err != nil || preserved != "yes" {
		t.Fatalf("other metadata lost: %s %v", preserved, err)
	}
	req := withURLParam(newRequestAs(testUserID, http.MethodGet, "/api/runtimes/"+runtimeID+"/asb-network-policy", nil), "runtimeId", runtimeID)
	read := httptest.NewRecorder()
	h.GetASBRuntimeNetworkPolicy(read, req)
	if read.Code != http.StatusOK || read.Body.String() != w.Body.String() {
		t.Fatalf("saved policy did not persist: %d %s", read.Code, read.Body.String())
	}
	if w = put(testUserID, map[string]any{"custom_targets": []string{}}); w.Code != http.StatusOK {
		t.Fatalf("clear status=%d %s", w.Code, w.Body.String())
	}
}
