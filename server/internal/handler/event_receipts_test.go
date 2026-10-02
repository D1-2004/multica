package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/eventrouter"
)

func TestEventReceiptReadsAreManageAndOwnerScopedDatabase(t *testing.T) {
	f := newCoordinatorPlanFixture(t, "receipt read fixture")
	f.h.DB = testPool
	raw, _ := json.Marshal(f.command)
	req := newRequest(http.MethodPost, "/dispatch", nil)
	req.Header.Set("Idempotency-Key", f.baseKey)
	f.h.EventRouteConfig = func(string, string, string) (string, string) { return eventrouter.Unified, "read-test" }
	response := newBufferedDispatchResponse()
	f.h.admitDispatchEvent(response, req, raw, &f.command, f.dc)
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM scene_event_receipt WHERE agent_id=$1`, f.agent.ID)
	})
	router := chi.NewRouter()
	router.Get("/api/agents/{id}/event-receipts", f.h.ListAgentEventReceipts)
	path := "/api/agents/" + uuidToString(f.agent.ID) + "/event-receipts?org_id=org-plan&source_event_id=" + f.baseKey
	w := scenesAs(t, router, "", http.MethodGet, path, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("owner read: %d %s", w.Code, w.Body.String())
	}
	var metadata struct {
		Receipts []eventReceiptView `json:"receipts"`
	}
	ctxcapDecode(t, w, &metadata)
	if len(metadata.Receipts) != 1 || metadata.Receipts[0].Event != nil || metadata.Receipts[0].AgentScene == nil || metadata.Receipts[0].AgentScene.SceneID != uuidToString(f.scene.ID) {
		t.Fatal("wrong metadata read")
	}
	w = scenesAs(t, router, "", http.MethodGet, path+"&include_payload=true", nil)
	ctxcapDecode(t, w, &metadata)
	if len(metadata.Receipts) != 1 || metadata.Receipts[0].Event == nil {
		t.Fatal("explicit payload missing")
	}
	member := createPermissionTestMember(t, "receipt-peer-"+uuid.NewString()+"@example.test")
	w = scenesAs(t, router, member, http.MethodGet, path, nil)
	if w.Code != http.StatusForbidden {
		t.Fatalf("peer accessed private receipt: %d", w.Code)
	}
	other := newCoordinatorPlanFixture(t, "other owner")
	w = scenesAs(t, router, "", http.MethodGet, "/api/agents/"+uuidToString(other.agent.ID)+"/event-receipts?org_id=org-plan&receipt_id="+f.command.EventReceiptID, nil)
	if w.Code != http.StatusOK {
		t.Fatal("other read failed")
	}
	ctxcapDecode(t, w, &metadata)
	if len(metadata.Receipts) != 0 {
		t.Fatal("receipt crossed agent boundary")
	}
}
