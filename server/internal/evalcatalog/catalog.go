// Package evalcatalog serves the repository-linked evaluation documentation.
package evalcatalog

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
)

//go:embed p0-golden.json office-scenarios.json document-index.json documents/*.md page.gohtml style.css
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

type Document struct {
	Path         string        `json:"path"`
	Title        string        `json:"title"`
	Category     string        `json:"category"`
	Kind         string        `json:"kind"`
	Historical   bool          `json:"historical"`
	File         string        `json:"file"`
	SourceSHA256 string        `json:"sourceSHA256"`
	Body         template.HTML `json:"-"`
}

type page struct {
	Tab           string
	Golden        []TestCase
	Scenarios     []Scenario
	Scenario      *Scenario
	Documents     []Document
	Document      *Document
	Guide         template.HTML
	CaseCount     int
	DocumentCount int
}

type handler struct {
	data      page
	documents map[string]Document
	scenarios map[string]Scenario
	tmpl      *template.Template
	css       []byte
}

func readJSON(name string, target any) {
	data, err := assets.ReadFile(name)
	if err != nil {
		panic(fmt.Errorf("read evaluation asset: %w", err))
	}
	if err := json.Unmarshal(data, target); err != nil {
		panic(fmt.Errorf("decode %s: %w", name, err))
	}
}

func renderMarkdown(data []byte) (template.HTML, error) {
	var out bytes.Buffer
	renderer := goldmark.New(goldmark.WithExtensions(extension.GFM), goldmark.WithParserOptions(parser.WithAutoHeadingID()))
	if err := renderer.Convert(data, &out); err != nil {
		return "", err
	}
	// Goldmark's default renderer rejects raw HTML and dangerous URL schemes.
	return template.HTML(out.String()), nil
}

// NewHandler loads only checked-in definitions and Markdown snapshots.
func NewHandler() http.Handler {
	var golden struct {
		Cases []TestCase `json:"cases"`
	}
	var office struct {
		Scenarios []Scenario `json:"scenarios"`
	}
	var docs struct {
		Assets []Document `json:"assets"`
	}
	readJSON("p0-golden.json", &golden)
	readJSON("office-scenarios.json", &office)
	readJSON("document-index.json", &docs)
	if len(golden.Cases) != 20 {
		panic("P0 catalog must contain exactly 20 golden cases")
	}
	h := &handler{data: page{Golden: golden.Cases, Scenarios: office.Scenarios, DocumentCount: len(docs.Assets)}, documents: map[string]Document{}, scenarios: map[string]Scenario{}}
	labels := map[string]string{}
	for _, s := range office.Scenarios {
		h.scenarios[s.ID] = s
		labels[s.ID] = s.Title
		h.data.CaseCount += len(s.Cases)
	}
	if h.data.CaseCount < 100 {
		panic("office scenario inventory must contain at least 100 cases")
	}
	for _, d := range docs.Assets {
		content, err := assets.ReadFile("documents/" + d.File)
		if err != nil {
			panic(err)
		}
		body, err := renderMarkdown(content)
		if err != nil {
			panic(err)
		}
		d.Body = body
		h.documents[d.Path] = d
		h.data.Documents = append(h.data.Documents, d)
	}
	h.tmpl = template.Must(template.New("page.gohtml").Funcs(template.FuncMap{
		"scenarioTitle": func(id string) string { return labels[id] },
		"docURL": func(path, tab string) string {
			return "/evals?tab=" + url.QueryEscape(tab) + "&doc=" + url.QueryEscape(path)
		},
	}).ParseFS(assets, "page.gohtml"))
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
	data := h.data
	data.Tab = r.URL.Query().Get("tab")
	if data.Tab == "" {
		data.Tab = "evals"
	}
	if data.Tab != "evals" && data.Tab != "tools" && data.Tab != "runtime" {
		http.Error(w, "unknown tab", http.StatusBadRequest)
		return
	}
	if id := r.URL.Query().Get("scenario"); id != "" {
		s, ok := h.scenarios[id]
		if !ok || data.Tab != "evals" {
			http.NotFound(w, r)
			return
		}
		data.Scenario = &s
	}
	if path := r.URL.Query().Get("doc"); path != "" {
		d, ok := h.documents[path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		data.Document = &d
		data.Tab = d.Category
	}
	if data.Document == nil && data.Tab != "evals" {
		guide := map[string]string{"tools": "docs/evals/evaluation-tools.md", "runtime": "docs/evals/evaluation-runtime.md"}[data.Tab]
		data.Guide = h.documents[guide].Body
	}
	var page bytes.Buffer
	if err := h.tmpl.Execute(&page, data); err != nil {
		http.Error(w, "could not render evaluation page", http.StatusInternalServerError)
		return
	}
	send(w, r, page.Bytes(), "text/html; charset=utf-8")
}

func send(w http.ResponseWriter, r *http.Request, body []byte, contentType string) {
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
}
