package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestDSHProfileRejectsMachineAndCallerDescriptors(t *testing.T) {
	h := &Handler{}
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		for _, actor := range []string{"task_token", "cloud_pat", "workspace_access_token"} {
			r := httptest.NewRequest(method, "/api/agents/id/dsh-profile", nil)
			r.Header.Set("X-Actor-Source", actor)
			w := httptest.NewRecorder()
			fn := h.GetDSHProfile
			if method == http.MethodPost {
				fn = h.PrepareDSHProfile
			}
			RequireHumanActor(http.HandlerFunc(fn)).ServeHTTP(w, r)
			if w.Code != http.StatusForbidden {
				t.Fatal("machine actor admitted", method, actor, w.Code)
			}
		}
	}
	for _, body := range []string{`{"revision":"1"}`, `{"plugins":[]}`, `{"descriptor":{}}`, `{} {}`, `[]`, strings.Repeat(" ", 1025)} {
		r := httptest.NewRequest(http.MethodPost, "/api/agents/id/dsh-profile", strings.NewReader(body))
		w := httptest.NewRecorder()
		h.PrepareDSHProfile(w, r)
		if w.Code != http.StatusBadRequest {
			t.Fatal("caller descriptor accepted", w.Code)
		}
	}
}

func TestDSHBuildRetryRejectsMachineAndMalformedInput(t *testing.T) {
	h := &Handler{}
	for _, actor := range []string{"task_token", "cloud_pat", "workspace_access_token"} {
		r := httptest.NewRequest(http.MethodPost, "/api/agents/id/dsh-profile/retry", strings.NewReader(`{}`))
		r.Header.Set("X-Actor-Source", actor)
		w := httptest.NewRecorder()
		RequireHumanActor(http.HandlerFunc(h.RetryDSHProfileBuild)).ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Fatal("machine retry admitted", actor, w.Code)
		}
	}
	for _, body := range []string{`{}`, `null`, `[]`, `{"revision":"01","build_id":"bad"}`, `{"revision":"1","build_id":"bad"}`, `{"revision":"1","descriptor":{}}`, `{} {}`, strings.Repeat(" ", 1025)} {
		w := httptest.NewRecorder()
		h.RetryDSHProfileBuild(w, httptest.NewRequest(http.MethodPost, "/api/agents/id/dsh-profile/retry", strings.NewReader(body)))
		if w.Code != http.StatusBadRequest {
			t.Fatal("invalid retry input admitted", w.Code)
		}
	}
}

func TestDSHProfileUsesEmployeeOverrideAndChecksActiveRows(t *testing.T) {
	row := db.ListDshPluginsForAgentRow{ID: pgtype.UUID{Bytes: uuid.New(), Valid: true}, PackageName: "test-plugin", ResolvedVersion: "1.0.0", Integrity: "sha256-" + strings.Repeat("a", 64), Enabled: true, ConfigRevision: 7, BundleRows: []byte(`["declared"]`), ConfigRow: "declared", Config: []byte(`{"value":"workspace"}`), ConfigOverride: []byte(`{"row_id":"declared","config":{"value":"employee"}}`)}
	source, err := dshProfileSource("template", []db.ListDshPluginsForAgentRow{row})
	if err != nil {
		t.Fatal(err)
	}
	if source.Plugins[0].Config["value"] != "employee" || source.Plugins[0].ConfigRevision != 7 {
		t.Fatal("lost employee override/revision")
	}
	row.BundleRows = []byte(`["changed"]`)
	if _, err = dshProfileSource("template", []db.ListDshPluginsForAgentRow{row}); err == nil {
		t.Fatal("active obsolete row admitted")
	}
	row.Enabled = false
	if _, err = dshProfileSource("template", []db.ListDshPluginsForAgentRow{row}); err != nil {
		t.Fatal("disabled stale row blocked other plugins", err)
	}
}
