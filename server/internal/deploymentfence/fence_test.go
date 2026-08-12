package deploymentfence

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMiddlewareHonorsFenceState(t *testing.T) {
	tests := []struct {
		name       string
		state      State
		method     string
		path       string
		wantStatus int
	}{
		{name: "normal write", state: StateNormal, method: http.MethodPost, path: "/api/issues", wantStatus: http.StatusNoContent},
		{name: "draining read", state: StateDraining, method: http.MethodGet, path: "/api/issues", wantStatus: http.StatusServiceUnavailable},
		{name: "draining health", state: StateDraining, method: http.MethodGet, path: "/healthz", wantStatus: http.StatusNoContent},
		{name: "draining write", state: StateDraining, method: http.MethodPost, path: "/api/issues", wantStatus: http.StatusServiceUnavailable},
		{name: "draining daemon completion", state: StateDraining, method: http.MethodPost, path: "/api/daemon/tasks/task-1/complete", wantStatus: http.StatusNoContent},
		{name: "frozen daemon completion", state: StateFrozen, method: http.MethodPost, path: "/api/daemon/tasks/task-1/complete", wantStatus: http.StatusServiceUnavailable},
		{name: "frozen control", state: StateFrozen, method: http.MethodPut, path: "/api/internal/deployment-fence", wantStatus: http.StatusNoContent},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &Service{}
			service.snapshot.Store(&Snapshot{State: test.state, Revision: 7})
			handler := service.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNoContent)
			}))
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(test.method, test.path, nil))
			if recorder.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d", recorder.Code, test.wantStatus)
			}
		})
	}
}

func TestValidTransition(t *testing.T) {
	allowed := [][2]State{
		{StateNormal, StateDraining},
		{StateDraining, StateNormal},
		{StateDraining, StateFrozen},
		{StateFrozen, StateNormal},
	}
	for _, pair := range allowed {
		if !validTransition(pair[0], pair[1]) {
			t.Fatalf("expected transition %s -> %s to be valid", pair[0], pair[1])
		}
	}
	if validTransition(StateNormal, StateFrozen) {
		t.Fatal("normal -> frozen must require an explicit draining phase")
	}
}
