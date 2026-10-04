// Package evalcatalog serves the repository-backed golden sets and scenarios.
package evalcatalog

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"strings"
)

//go:embed p0-golden.json office-scenarios.json page.gohtml style.css
var assets embed.FS

type TestCase struct {
	ID           string   `json:"id"`
	Title        string   `json:"title"`
	Priority     string   `json:"priority"`
	Roles        []string `json:"roles"`
	Verifies     []string `json:"verifies"`
	Method       []string `json:"method"`
	ScenarioRefs []string `json:"scenarioRefs"`
	CaseRefs     []string `json:"caseRefs"`
	Sources      []string `json:"sources"`
	Origin       string   `json:"origin"`
}

type Scenario struct {
	ID          string     `json:"id"`
	Title       string     `json:"title"`
	Description string     `json:"description"`
	Cases       []TestCase `json:"cases"`
}

type page struct {
	Golden       []TestCase
	Scenarios    []Scenario
	CaseCount    int
	OpenScenario string
	OpenGolden   string
}

type handler struct {
	data      page
	scenarios map[string]bool
	golden    map[string]bool
	tmpl      *template.Template
	css       []byte
}

func readJSON(name string, target any) {
	data, err := assets.ReadFile(name)
	if err != nil {
		panic(fmt.Errorf("read evaluation definition: %w", err))
	}
	if err := json.Unmarshal(data, target); err != nil {
		panic(fmt.Errorf("decode %s: %w", name, err))
	}
}

// NewHandler reads only the canonical JSON definitions, without runtime state.
func NewHandler() http.Handler {
	var golden struct {
		Cases []TestCase `json:"cases"`
	}
	var office struct {
		Scenarios []Scenario `json:"scenarios"`
	}
	readJSON("p0-golden.json", &golden)
	readJSON("office-scenarios.json", &office)
	if len(golden.Cases) != 20 {
		panic("P0 catalog must contain exactly 20 golden cases")
	}
	h := &handler{data: page{Golden: golden.Cases, Scenarios: office.Scenarios}, scenarios: map[string]bool{}, golden: map[string]bool{}}
	titles := map[string]string{}
	for _, s := range office.Scenarios {
		h.scenarios[s.ID] = true
		titles[s.ID] = s.Title
		h.data.CaseCount += len(s.Cases)
	}
	for _, c := range golden.Cases {
		h.golden[c.ID] = true
	}
	h.tmpl = template.Must(template.New("page.gohtml").Funcs(template.FuncMap{"scenarioTitle": func(id string) string { return titles[id] }}).ParseFS(assets, "page.gohtml"))
	var err error
	h.css, err = assets.ReadFile("style.css")
	if err != nil {
		panic(err)
	}
	return h
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "private, no-cache")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	path := strings.TrimSuffix(r.URL.Path, "/")
	if path == "/api/evals/style.css" {
		send(w, r, h.css, "text/css; charset=utf-8")
		return
	}
	if path != "/api/evals" && path != "/evals" {
		http.NotFound(w, r)
		return
	}
	query := r.URL.Query()
	if query.Has("doc") || query.Has("tab") {
		http.NotFound(w, r)
		return
	}
	data := h.data
	data.OpenScenario = query.Get("scenario")
	data.OpenGolden = query.Get("golden")
	if data.OpenScenario != "" && !h.scenarios[data.OpenScenario] || data.OpenGolden != "" && !h.golden[data.OpenGolden] {
		http.NotFound(w, r)
		return
	}
	var body bytes.Buffer
	if err := h.tmpl.Execute(&body, data); err != nil {
		http.Error(w, "could not render evaluation page", http.StatusInternalServerError)
		return
	}
	send(w, r, body.Bytes(), "text/html; charset=utf-8")
}

func send(w http.ResponseWriter, r *http.Request, body []byte, contentType string) {
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
}
