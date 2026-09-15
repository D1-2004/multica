package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func scheduleRequest(body string) *http.Request {
	task, workspace, agent := uuid.NewString(), uuid.NewString(), uuid.NewString()
	r := withURLParam(httptest.NewRequest(http.MethodPost, "/api/tasks/"+task+"/dsh/schedules", strings.NewReader(body)), "taskId", task)
	r.Header.Set("X-Actor-Source", "task_token")
	r.Header.Set("X-Task-ID", task)
	r.Header.Set("X-Workspace-ID", workspace)
	r.Header.Set("X-Agent-ID", agent)
	return r.WithContext(middleware.SetMemberContext(r.Context(), workspace, db.Member{}))
}

func TestDSHScheduleRejectsWrongAuthenticationBeforeDatabase(t *testing.T) {
	for _, alter := range []func(*http.Request){
		func(r *http.Request) { r.Header.Del("X-Actor-Source") },
		func(r *http.Request) { r.Header.Set("X-Actor-Source", "cloud_pat") },
		func(r *http.Request) { r.Header.Set("X-Actor-Source", "workspace_access_token") },
		func(r *http.Request) { r.Header.Set("X-Actor-Source", "native_access") },
		func(r *http.Request) { r.Header.Set("X-Task-ID", uuid.NewString()) },
		func(r *http.Request) { r.Header.Set("X-Workspace-ID", uuid.NewString()) },
	} {
		r := scheduleRequest(`{}`)
		alter(r)
		for _, handle := range []http.HandlerFunc{(&Handler{}).DSHSchedules, (&Handler{}).DeleteDSHSchedule} {
			w := httptest.NewRecorder()
			handle(w, r)
			if w.Code != http.StatusForbidden {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}
		}
	}
}

func TestDSHScheduleRejectsMalformedOrCallerSuppliedAuthority(t *testing.T) {
	h := &Handler{TaskService: &service.TaskService{}}
	for _, body := range []string{`{`, `{} {}`, `{"first_due_at":"tomorrow"}`,
		`{"owner_member_id":"fake"}`, `{"source_task_id":"fake"}`, `{"workspace_id":"fake"}`,
		`{"agent_id":"fake"}`, `{"native_access":"fake"}`, `{"token":"fake"}`,
		`{"prompt":"` + strings.Repeat("x", 65536) + `"}`} {
		w := httptest.NewRecorder()
		h.DSHSchedules(w, scheduleRequest(body))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("malformed payload status %d", w.Code)
		}
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("private response is cacheable")
		}
	}
}

func TestDSHScheduleActorUsesOnlyAuthenticatedCoordinates(t *testing.T) {
	r := scheduleRequest(`{}`)
	r.URL.RawQuery = "workspace_id=" + uuid.NewString() + "&agent_id=" + uuid.NewString() + "&task_id=" + uuid.NewString()
	w := httptest.NewRecorder()
	a, ok := taskScheduleActor(w, r)
	if !ok || a.WorkspaceID.String() != r.Header.Get("X-Workspace-ID") || a.AgentID.String() != r.Header.Get("X-Agent-ID") || a.TaskID.String() != r.Header.Get("X-Task-ID") {
		t.Fatal("query parameters changed the authenticated actor")
	}
}
