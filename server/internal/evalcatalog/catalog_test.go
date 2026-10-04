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

func TestP0AndOfficeDefinitions(t *testing.T) {
	h := NewHandler().(*handler)
	r := request(h, "/api/evals")
	if r.Code != 200 {
		t.Fatal(r.Code)
	}
	body := r.Body.String()
	if n := strings.Count(body, `class="test-case"`); n != 20 {
		t.Fatalf("P0 count=%d", n)
	}
	previous := -1
	for i := 1; i <= 20; i++ {
		id := fmt.Sprintf(`id="G%02d"`, i)
		at := strings.Index(body, id)
		if at <= previous || strings.Count(body, id) != 1 {
			t.Fatal("unordered P0", id)
		}
		previous = at
	}
	if h.data.CaseCount < 100 || len(h.data.Scenarios) < 10 {
		t.Fatal("missing office scenarios")
	}
	for _, s := range h.data.Scenarios {
		r := request(h, "/evals?scenario="+s.ID)
		if r.Code != 200 || strings.Count(r.Body.String(), `class="test-case"`) != len(s.Cases) {
			t.Fatal("scenario missing cases", s.ID)
		}
		for _, field := range []string{"需要什么角色", "测试验证的是什么", "怎么验证"} {
			if !strings.Contains(r.Body.String(), field) {
				t.Fatal("missing field", field)
			}
		}
	}
	for _, bad := range []string{"为什么", "<script", "<form", "<button", "<iframe", "<no value>", "ZgotmplZ"} {
		if strings.Contains(body, bad) {
			t.Fatal("unexpected content", bad)
		}
	}
}

func TestToolsRuntimeAndMarkdownAllowlist(t *testing.T) {
	h := NewHandler().(*handler)
	for _, tab := range []string{"evals", "tools", "runtime"} {
		r := request(h, "/api/evals?tab="+tab)
		if r.Code != 200 || !strings.Contains(r.Body.String(), `aria-current="page"`) {
			t.Fatal("missing tab", tab)
		}
	}
	if len(h.documents) < 100 {
		t.Fatal("missing repository Markdown")
	}
	r := request(h, "/api/evals?doc=docs%2Fevals%2Fp0-golden.md")
	if r.Code != 200 || !strings.Contains(r.Body.String(), "来源 SHA-256") || !strings.Contains(r.Body.String(), "G20") {
		t.Fatal("missing Markdown body")
	}
	for path, status := range map[string]int{
		"/api/evals?doc=../../.env": 404, "/api/evals?scenario=unknown": 404, "/api/evals?tab=execute": 400,
		"/api/evals/golden.json": 404, "/api/evals/../../CLAUDE.md": 404,
	} {
		if r := request(h, path); r.Code != status {
			t.Errorf("%s=%d", path, r.Code)
		}
	}
}

func TestMethodsAndSafeMarkdown(t *testing.T) {
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
	body, err := renderMarkdown([]byte("<script>alert(1)</script>\n\n[bad](javascript:alert%281%29)\n"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "<script") || strings.Contains(string(body), `href="javascript:`) {
		t.Fatal("unsafe Markdown", body)
	}
}
