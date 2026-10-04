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

//go:embed spec.json p0-golden.json office-scenarios.json page.gohtml style.css
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
	CategoryRef string     `json:"categoryRef"`
	Cases       []TestCase `json:"cases"`
}

type Requirement struct {
	ID           string   `json:"id"`
	Title        string   `json:"title"`
	Summary      string   `json:"summary"`
	Requirements []string `json:"requirements"`
	ScenarioRefs []string `json:"scenarioRefs"`
}

type Category struct {
	ID          string     `json:"id"`
	Title       string     `json:"title"`
	Description string     `json:"description"`
	Scenarios   []Scenario `json:"-"`
	CaseCount   int        `json:"-"`
}

type page struct {
	ProductTitle string
	Description  string
	Requirements []Requirement
	Golden       []TestCase
	Scenarios    []Scenario
	Categories   []Category
	CaseCount    int
	ActiveTab    string
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
		Categories []Category `json:"categories"`
		Scenarios  []Scenario `json:"scenarios"`
	}
	var spec struct {
		Title        string        `json:"title"`
		Description  string        `json:"description"`
		Requirements []Requirement `json:"requirements"`
	}
	readJSON("spec.json", &spec)
	readJSON("p0-golden.json", &golden)
	readJSON("office-scenarios.json", &office)
	if len(golden.Cases) != 20 {
		panic("P0 catalog must contain exactly 20 golden cases")
	}
	h := &handler{data: page{ProductTitle: spec.Title, Description: spec.Description, Requirements: spec.Requirements, Golden: golden.Cases, Scenarios: office.Scenarios, Categories: office.Categories}, scenarios: map[string]bool{}, golden: map[string]bool{}}
	categoryIndex := map[string]int{}
	for i, category := range h.data.Categories {
		if _, duplicate := categoryIndex[category.ID]; duplicate {
			panic("duplicate office category")
		}
		categoryIndex[category.ID] = i
	}
	titles := map[string]string{}
	for _, s := range office.Scenarios {
		i, exists := categoryIndex[s.CategoryRef]
		if !exists {
			panic("unknown office category")
		}
		h.data.Categories[i].Scenarios = append(h.data.Categories[i].Scenarios, s)
		h.data.Categories[i].CaseCount += len(s.Cases)
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
	if query.Has("doc") {
		http.NotFound(w, r)
		return
	}
	data := h.data
	data.ActiveTab = query.Get("tab")
	if data.ActiveTab == "" {
		data.ActiveTab = "evals"
	}
	if data.ActiveTab != "spec" && data.ActiveTab != "evals" {
		http.NotFound(w, r)
		return
	}
	data.OpenScenario = query.Get("scenario")
	data.OpenGolden = query.Get("golden")
	if data.OpenScenario != "" && !h.scenarios[data.OpenScenario] || data.OpenGolden != "" && !h.golden[data.OpenGolden] || data.ActiveTab == "spec" && (data.OpenScenario != "" || data.OpenGolden != "") {
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
