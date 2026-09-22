package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGlobalModelManagementRequiresStablePublisher(t *testing.T) {
	h := &Handler{cfg: Config{StableRuntimePublisherUserIDs: map[string]struct{}{"00000000-0000-4000-8000-000000000001": {}}}}
	for _, fn := range []http.HandlerFunc{h.GetGlobalModels, h.SaveGlobalModels, h.DiscoverProviderModels} {
		r := httptest.NewRequest("GET", "/api/developer/models", nil)
		r.Header.Set("X-User-ID", "00000000-0000-4000-8000-000000000002")
		w := httptest.NewRecorder()
		fn(w, r)
		if w.Code != 403 {
			t.Fatalf("status %d", w.Code)
		}
	}
}
