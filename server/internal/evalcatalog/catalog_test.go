package evalcatalog

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/evalreport"
)

func request(h http.Handler, path string) *httptest.ResponseRecorder {
	r := httptest.NewRecorder()
	h.ServeHTTP(r, httptest.NewRequest(http.MethodGet, path, nil))
	return r
}

func TestSinglePageHasGoldenAndExpandableScenarios(t *testing.T) {
	h := NewHandler(nil).(*handler)
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
	if strings.Count(body, `class="scenario-category"`) != len(h.data.Categories) || !strings.Contains(body, "P0 GoldenCases") || !strings.Contains(body, "通用办公场景用例") {
		t.Fatal("missing categorized evaluation overview")
	}
	if !strings.Contains(body, `href="/evals" aria-current="page"`) || strings.Contains(body, `class="spec-item"`) {
		t.Fatal("EVALS navigation should select only the evaluation content")
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
	if strings.Contains(body, "FD Workbench") || strings.Contains(body, `class="topbar"`) {
		t.Fatal("workbench bar should be gone")
	}
	readyCases := map[string]bool{}
	for _, scenario := range h.data.Scenarios {
		for _, item := range scenario.Cases {
			readyCases[item.ID] = item.LiveReady
		}
	}
	for _, id := range []string{
		"office-at-employee",
		"office-python-real",
		"office-cron-real-due",
		"office-file-native-delivery",
		"office-collection-two-task-person",
		"office-collection-cancel-no-recount",
		"office-questions-stay-separate",
		"office-same-chat-drink-recall",
		"office-scheduled-wait-honesty",
	} {
		if !readyCases[id] {
			t.Fatal("expected a conversation-checkable case", id)
		}
	}
	behaviors := map[string]string{
		"office-collection-cancel-no-recount": "已作废的各方人数",
		"office-questions-stay-separate":      "不要合并",
		"office-same-chat-drink-recall":       "温柠檬水",
	}
	for _, scenario := range h.data.Scenarios {
		for _, item := range scenario.Cases {
			phrase, ok := behaviors[item.ID]
			if !ok {
				continue
			}
			text := strings.Join(item.Verifies, "") + strings.Join(item.Method, "")
			if !strings.Contains(text, phrase) {
				t.Fatal("new behavior missing", item.ID)
			}
			at := strings.Index(body, `id="`+item.ID+`"`)
			if at < 0 {
				t.Fatal("new case missing from page", item.ID)
			}
			window := body[at:]
			if len(window) > 500 {
				window = window[:500]
			}
			if !strings.Contains(window, `class="ready-mark"`) {
				t.Fatal("new case is not marked on the page", item.ID)
			}
		}
	}
	for _, id := range []string{"office-at-other", "office-json-only", "office-hook-signature", "office-memory-capture-private", "office-structured-completion-continue", "office-continue-success", "office-direct-without-issue"} {
		if readyCases[id] {
			t.Fatal("case is not verifiable from the conversation", id)
		}
	}
	readyGolden := map[string]bool{}
	for _, item := range h.data.Golden {
		readyGolden[item.ID] = item.LiveReady
	}
	if !readyGolden["G04"] || !readyGolden["G12"] {
		t.Fatal("expected ready golden combinations")
	}
	for id, ready := range readyGolden {
		if id != "G04" && id != "G12" && ready {
			t.Fatal("unexpected ready golden", id)
		}
	}
	for _, id := range []string{"G04", "G12"} {
		at := strings.Index(body, `id="`+id+`"`)
		if at < 0 {
			t.Fatal("golden missing", id)
		}
		window := body[at:]
		if len(window) > 500 {
			window = window[:500]
		}
		if !strings.Contains(window, `class="ready-mark"`) {
			t.Fatal("golden is not marked on the page", id)
		}
	}
	wantCases := 0
	for _, ready := range readyCases {
		if ready {
			wantCases++
		}
	}
	wantGolden := 0
	for _, ready := range readyGolden {
		if ready {
			wantGolden++
		}
	}
	readyMarks := strings.Count(body, `class="ready-mark"`)
	if h.data.ReadyGolden != wantGolden || h.data.ReadyCases != wantCases || readyMarks != h.data.ReadyGolden+h.data.ReadyCases {
		t.Fatalf("ready marks golden=%d cases=%d rendered=%d", h.data.ReadyGolden, h.data.ReadyCases, readyMarks)
	}
	if !strings.Contains(body, "可跑可验证") {
		t.Fatal("ready mark missing")
	}
	judgeCases, blockedCases := 0, 0
	for _, scenario := range h.data.Scenarios {
		for _, item := range scenario.Cases {
			states := 0
			if item.LiveReady {
				states++
			}
			if item.JudgeFact != "" {
				states++
				judgeCases++
				text := strings.Join(item.Verifies, "") + strings.Join(item.Method, "")
				if !strings.Contains(text, "通过") || !strings.Contains(text, "不通过") {
					t.Fatal("judge case missing pass and fail wording", item.ID)
				}
			}
			if item.BlockedBecause != "" {
				states++
				blockedCases++
			}
			if states != 1 {
				t.Fatal("case is not in exactly one visible state", item.ID)
			}
		}
	}
	if judgeCases+blockedCases+wantCases != h.data.CaseCount || judgeCases < 1 || blockedCases < 1 {
		t.Fatalf("states ready=%d judge=%d blocked=%d total=%d", wantCases, judgeCases, blockedCases, h.data.CaseCount)
	}
	if strings.Count(body, `class="judge-mark"`) != judgeCases || strings.Count(body, `class="blocked-mark"`) != blockedCases {
		t.Fatal("state marks do not match the cases")
	}
	if !strings.Contains(body, "补一条事实可判断") || !strings.Contains(body, "还缺条件") {
		t.Fatal("state labels missing")
	}
	for _, id := range []string{"office-at-other", "office-json-only", "office-memory-capture-private", "office-continue-success", "office-direct-without-issue"} {
		if readyCases[id] {
			t.Fatal("case is not verifiable from the conversation", id)
		}
	}
	for _, bad := range []string{"Markdown", "仓库资产", "评测工具", "评测运行时", "为什么", "<script", "<form", "<button", "<iframe", "<no value>", "ZgotmplZ"} {
		if strings.Contains(body, bad) {
			t.Fatal("unexpected overview content", bad)
		}
	}
}

func TestSpecRequirementsLinkToEvaluationScenarios(t *testing.T) {
	h := NewHandler(nil).(*handler)
	r := request(h, "/evals?tab=spec")
	if r.Code != 200 {
		t.Fatal(r.Code)
	}
	body := r.Body.String()
	if strings.Count(body, `class="spec-item"`) != len(h.data.Requirements) || !strings.Contains(body, `href="/evals?tab=spec" aria-current="page"`) {
		t.Fatal("SPEC requirements or active navigation missing")
	}
	if strings.Contains(body, `class="golden-item"`) || strings.Contains(body, `class="scenario-item"`) || strings.Contains(body, "可跑可验证") || strings.Contains(body, "FD Workbench") {
		t.Fatal("SPEC should show requirements, without duplicating evaluations")
	}
	for _, requirement := range h.data.Requirements {
		if !strings.Contains(body, requirement.Title) {
			t.Fatal("missing requirement", requirement.ID)
		}
		for _, sid := range requirement.ScenarioRefs {
			if !h.scenarios[sid] || !strings.Contains(body, `/evals?scenario=`+sid) {
				t.Fatal("missing evaluation link", requirement.ID, sid)
			}
		}
	}
	for _, bad := range []string{"已通过", "验收通过", "Markdown", "<script", "<form", "<button"} {
		if strings.Contains(body, bad) {
			t.Fatal("SPEC should state requirements only", bad)
		}
	}
}

func TestDeepLinksExpandInPlaceAndRemovedReadersStayRemoved(t *testing.T) {
	h := NewHandler(nil).(*handler)
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
	for _, path := range []string{"/api/evals?doc=docs%2Fevals%2Fp0-golden.md", "/api/evals?tab=tools", "/api/evals?tab=runtime", "/api/evals?tab=spec&scenario=group-participation", "/api/evals?scenario=unknown", "/api/evals?golden=unknown", "/api/evals/p0-golden.json", "/api/evals/../../CLAUDE.md"} {
		if r := request(h, path); r.Code != 404 {
			t.Errorf("%s=%d", path, r.Code)
		}
	}
}

func TestReadOnlyMethodsAndPrivateHeaders(t *testing.T) {
	h := NewHandler(nil)
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

func TestReportsTabSeparatesUnavailableEmptyAndFrozenDetail(t *testing.T) {
	for _, test := range []struct {
		source ReportSource
		want   string
	}{
		{nil, "报告暂不可用"},
		{func(*http.Request) (ReportPage, error) { return ReportPage{}, nil }, "暂无上报报告"},
		{func(*http.Request) (ReportPage, error) { return ReportPage{}, errors.New("database offline") }, "报告暂不可用"},
	} {
		response := request(NewHandler(test.source), "/evals?tab=reports")
		if response.Code != 200 || !strings.Contains(response.Body.String(), test.want) {
			t.Fatal("report availability is misrepresented")
		}
		if strings.Contains(response.Body.String(), "QwenTag 的") || !strings.Contains(response.Body.String(), "QwenTag SPEC &amp; EVALS") || strings.Contains(response.Body.String(), "FD Workbench") || strings.Contains(response.Body.String(), "可跑可验证") {
			t.Fatal("page title not simplified")
		}
	}
	record := evalreport.Record{ID: "fixture-report", WorkspaceName: "隔离测试", ReceivedAt: time.Now(), Submission: evalreport.Submission{Title: "历史快照", ExecutionKind: "mock", SelectedCases: []evalreport.CaseDefinition{{ID: "local-case", Title: "旧标题 <script>alert(1)</script>", Roles: []string{"测试角色"}, Verifies: []string{"冻结标准"}, Method: []string{"独立读回"}}}, Results: []evalreport.CaseResult{{CaseID: "local-case", Status: "pass", Summary: "模拟观察", Evidence: []evalreport.Evidence{{Kind: "artifact", Reference: "evidence/fixture.json"}}}}}, Summary: evalreport.Summary{Total: 1, Pass: 1, Conclusion: "pass", P0Total: 20}}
	response := request(NewHandler(func(*http.Request) (ReportPage, error) { return ReportPage{Detail: &record}, nil }), "/evals?tab=reports")
	body := response.Body.String()
	if response.Code != 200 || !strings.Contains(body, "冻结标准") || !strings.Contains(body, "模拟测试") || strings.Contains(body, "<script>") || strings.Contains(body, `href="evidence/fixture.json"`) {
		t.Fatal("snapshot report is unsafe or uses current definitions")
	}
	missing := request(NewHandler(func(*http.Request) (ReportPage, error) { return ReportPage{}, evalreport.ErrNotFound }), "/evals?tab=reports&report=missing")
	if missing.Code != 404 {
		t.Fatal("invisible report should not render as an empty list")
	}
	contract := request(NewHandler(nil), "/api/evals/report-contract")
	if contract.Code != 200 || !strings.Contains(contract.Body.String(), `"openapi"`) {
		t.Fatal("report contract not available")
	}
}

func TestJudgeFactReturnsPlantedFactWithoutTraceDump(t *testing.T) {
	h := NewHandler(nil).(*handler)
	var caseID, fact string
	for _, scenario := range h.data.Scenarios {
		for _, item := range scenario.Cases {
			if item.ID == "office-at-other" && item.JudgeFact != "" {
				caseID, fact = item.ID, item.JudgeFact
			}
		}
	}
	if caseID == "" || fact == "" {
		t.Fatal("fixture case has no planted fact")
	}
	const dump = "SPAN-DUMP-9f3c log-bucket-should-not-leak"
	payload, err := json.Marshal(map[string]string{"caseId": caseID, "fact": fact, "trace": dump, "log": dump})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	h.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/evals/judge-fact", strings.NewReader(string(payload))))
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), fact) {
		t.Fatal("planted fact missing", response.Body.String())
	}
	if strings.Contains(response.Body.String(), dump) || strings.Contains(response.Body.String(), "SPAN-DUMP") {
		t.Fatal("trace dump leaked", response.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got["fact"] != fact {
		t.Fatal("response is not the one fact", got)
	}
	leaky, err := json.Marshal(map[string]string{"caseId": caseID, "fact": dump, "trace": dump})
	if err != nil {
		t.Fatal(err)
	}
	rejected := httptest.NewRecorder()
	h.ServeHTTP(rejected, httptest.NewRequest(http.MethodPost, "/api/evals/judge-fact", strings.NewReader(string(leaky))))
	if rejected.Code == 200 || strings.Contains(rejected.Body.String(), dump) {
		t.Fatal("unplanted dump was returned", rejected.Code, rejected.Body.String())
	}
}

func TestOfficeVisibleChecksSayWhatChatOrFileShows(t *testing.T) {
	h := NewHandler(nil).(*handler)
	page := request(h, "/api/evals")
	if page.Code != 200 {
		t.Fatal(page.Code)
	}
	body := page.Body.String()
	live := map[string]bool{
		"office-negative-stop":                true,
		"office-two-existing-targets":         true,
		"office-steer-delivery-inheritance":   true,
		"office-forwarded-invite-principal":   true,
		"office-authorized-terminal-followup": true,
		"office-terminal-without-plan":        false,
	}
	found := 0
	for _, scenario := range h.data.Scenarios {
		for _, item := range scenario.Cases {
			wantLive, ok := live[item.ID]
			if !ok {
				continue
			}
			found++
			if len(item.Method) == 0 {
				t.Fatal("missing method", item.ID)
			}
			verdict := item.Method[0]
			if !strings.Contains(verdict, "通过时") || !strings.Contains(verdict, "不通过时") {
				t.Fatal("method does not state pass and fail", item.ID)
			}
			if !strings.Contains(verdict, "群") && !strings.Contains(verdict, "文件") {
				t.Fatal("method does not name the chat or the file", item.ID)
			}
			if item.LiveReady != wantLive {
				t.Fatal("visible state", item.ID, item.LiveReady)
			}
			if !strings.Contains(body, verdict) {
				t.Fatal("page does not show the verdict", item.ID)
			}
			if item.ID == "office-terminal-without-plan" {
				const fact = "这一项结果交完之后，没有再派发新任务。"
				if item.JudgeFact != fact || item.BlockedBecause != "" {
					t.Fatal("judge fact", item.JudgeFact, item.BlockedBecause)
				}
				if !strings.Contains(body, "裁判只看这一条："+fact) {
					t.Fatal("page does not show the one judge fact")
				}
				continue
			}
			if item.JudgeFact != "" || item.BlockedBecause != "" {
				t.Fatal("conversation case also has another state", item.ID)
			}
		}
	}
	if found != len(live) {
		t.Fatal("missing cases", found)
	}
}
