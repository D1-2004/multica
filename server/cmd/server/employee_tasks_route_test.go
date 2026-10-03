package main

import (
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/analytics"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/realtime"
)

// The read-only EmployeeTask API is GET-only; nothing else is registered
// under it except the existing human steer control.
func TestEmployeeTaskReadRoutesAreGetOnly(t *testing.T) {
	router := NewRouter(nil, realtime.NewHub(), events.New(), analytics.NoopClient{}, nil)
	want := map[string]bool{
		"GET /api/employee-tasks":              false,
		"GET /api/employee-tasks/{id}":         false,
		"GET /api/employee-tasks/{id}/entries": false,
		"GET /api/employee-tasks/{id}/runs":    false,
		"POST /api/employee-tasks/{id}/steer":  false,
	}
	err := chi.Walk(router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		key := method + " " + route
		if len(route) >= len("/api/employee-tasks") && route[:len("/api/employee-tasks")] == "/api/employee-tasks" {
			if _, ok := want[key]; !ok {
				t.Errorf("unexpected EmployeeTask route %s", key)
			}
			want[key] = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for key, seen := range want {
		if !seen {
			t.Errorf("route %s is not registered", key)
		}
	}
}
