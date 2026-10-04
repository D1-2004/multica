package evalcatalog

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func request(h http.Handler, path string) *httptest.ResponseRecorder {
	r := httptest.NewRecorder()
	h.ServeHTTP(r, httptest.NewRequest(http.MethodGet, path, nil))
	return r
}

func TestSinglePageHasGoldenAndExpandableScenarios(t *testing.T) {
	h := NewHandler().(*handler)
	r := request(h, "/api/evals")
	if r.Code != 200 {
		t.Fatal(r.Code)
	}
	body := r.Body.String()
	if strings.Count(body, `class="golden-item"`) != 20 || strings.Count(body, `class="scenario-item"`) != len(h.data.Scenarios) {
		t.Fatal("missing overview entries")
	}
	if strings.Count(body, `class="scenario-case"`) != h.data.CaseCount {
		t.Fatal("cases are not available on the same page")
	}
	previous := -1
	for i := 1; i <= 20; i++ {
		id := fmt.Sprintf(`id="G%02d"`, i)
		at := strings.Index(body, id)
		if at <= previous || strings.Count(body, id) != 1 {
			t.Fatal("unordered golden definitions", id)
		}
		previous = at
	}
	if strings.Contains(body, ` open>`) {
		t.Fatal("overview should be collapsed by default")
	}
	for _, field := range []string{"需要什么角色", "测试验证的是什么", "怎么验证"} {
		if !strings.Contains(body, field) {
			t.Fatal("missing case field", field)
		}
	}
	for _, bad := range []string{"Markdown", "仓库资产", "评测工具", "评测运行时", "为什么", "<script", "<form", "<button", "<iframe", "<no value>", "ZgotmplZ"} {
		if strings.Contains(body, bad) {
			t.Fatal("unexpected overview content", bad)
		}
	}
}

func TestDeepLinksExpandInPlaceAndRemovedReadersStayRemoved(t *testing.T) {
	h := NewHandler().(*handler)
	for _, s := range h.data.Scenarios {
		r := request(h, "/evals?scenario="+s.ID)
		if r.Code != 200 || !strings.Contains(r.Body.String(), `id="`+s.ID+`" open>`) {
			t.Fatal("scenario does not expand in place", s.ID)
		}
		if strings.Count(r.Body.String(), `class="scenario-item"`) != len(h.data.Scenarios) {
			t.Fatal("deep link should retain the single-page overview")
		}
	}
	if r := request(h, "/evals?golden=G01"); r.Code != 200 || !strings.Contains(r.Body.String(), `id="G01" open>`) {
		t.Fatal("golden deep link does not expand")
	}
	for _, path := range []string{"/api/evals?doc=docs%2Fevals%2Fp0-golden.md", "/api/evals?tab=tools", "/api/evals?scenario=unknown", "/api/evals?golden=unknown", "/api/evals/p0-golden.json", "/api/evals/../../CLAUDE.md"} {
		if r := request(h, path); r.Code != 404 {
			t.Errorf("%s=%d", path, r.Code)
		}
	}
}

func TestReadOnlyMethodsAndPrivateHeaders(t *testing.T) {
	h := NewHandler()
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		r := httptest.NewRecorder()
		h.ServeHTTP(r, httptest.NewRequest(method, "/api/evals", nil))
		if r.Code != 405 {
			t.Fatal(method, r.Code)
		}
	}
	r := httptest.NewRecorder()
	h.ServeHTTP(r, httptest.NewRequest(http.MethodHead, "/api/evals", nil))
	if r.Code != 200 || r.Body.Len() != 0 || r.Header().Get("Content-Length") == "" {
		t.Fatal("invalid HEAD")
	}
	r = request(h, "/api/evals/style.css")
	if r.Code != 200 || r.Header().Get("Content-Type") != "text/css; charset=utf-8" {
		t.Fatal("invalid CSS")
	}
	r = request(h, "/api/evals")
	if r.Header().Get("Cache-Control") != "private, no-cache" || !strings.Contains(r.Header().Get("Content-Security-Policy"), "default-src 'none'") {
		t.Fatal("missing reader protection")
	}
}
