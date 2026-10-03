package employeeverification

import (
	"encoding/json"
	"testing"
)

func describe(checks []Check) []string {
	out := make([]string, 0, len(checks))
	for _, c := range checks {
		c.ID = ""
		raw, _ := json.Marshal(c)
		out = append(out, string(raw))
	}
	return out
}

func TestDeriveFromHumanTextPatternTable(t *testing.T) {
	cases := []struct {
		name, text string
		want       []string
	}{
		{"zh rows and columns", "验收标准：sales.csv 共 3 行数据，sales.csv 的列为 区域、金额",
			[]string{`{"id":"","kind":"artifact_contents","file":"sales.csv","columns":["区域","金额"]}`, `{"id":"","kind":"artifact_contents","file":"sales.csv","data_rows":3}`}},
		{"zh quoted chinese file contains", "完成标准：「日报.md」里要包含「研发部」",
			[]string{`{"id":"","kind":"artifact_contents","file":"日报.md","contains":["研发部"]}`}},
		{"zh value bound to file", "把 1 到 100 的和写到 sum.txt，sum.txt 的内容应为 5050，没通过检查别说做完了",
			[]string{`{"id":"","kind":"execution_output","file":"sum.txt","expect":"5050"}`}},
		{"zh bare result binds to the only file", "结果写入 sum.txt。完成条件：结果应为 5050",
			[]string{`{"id":"","kind":"execution_output","file":"sum.txt","expect":"5050"}`}},
		{"zh only-then cue with produce", "只有生成 report.xlsx 才算完成",
			[]string{`{"id":"","kind":"artifact_contents","file":"report.xlsx"}`}},
		{"zh full-width digits and records", "交付标准：orders.csv 里要有 １２ 条记录",
			[]string{`{"id":"","kind":"artifact_contents","file":"orders.csv","data_rows":12}`}},
		{"en gawkbot phrasing", "Definition of done: file landing/index.html exists and contains \"Sign up\"",
			[]string{`{"id":"","kind":"artifact_contents","file":"index.html","contains":["Sign up"]}`}},
		{"en exists", "Don't tell me it's done unless file out.json exists",
			[]string{`{"id":"","kind":"artifact_contents","file":"out.json"}`}},
		{"en command after cue", "DoD: `go test ./internal/... -count=1` passes",
			[]string{`{"id":"","kind":"execution_output","command":"go test ./internal/... -count=1"}`}},
		{"en rows", "Acceptance criteria: users.csv has exactly 40 data rows",
			[]string{`{"id":"","kind":"artifact_contents","file":"users.csv","data_rows":40}`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := describe(DeriveFromHumanText(tc.text))
			if len(got) != len(tc.want) {
				t.Fatalf("got %v want %v", got, tc.want)
			}
			seen := map[string]bool{}
			for _, g := range got {
				seen[g] = true
			}
			for _, w := range tc.want {
				if !seen[w] {
					t.Fatalf("missing %s in %v", w, got)
				}
			}
		})
	}
}

// False positives are worse than misses: each of these must derive nothing.
func TestDeriveFromHumanTextStaysConservative(t *testing.T) {
	for _, text := range []string{
		"帮我整理 sales.csv 共 3 行数据，sales.csv 里要包含「研发部」", // checkable, but no done-criteria cue
		"完成标准还没定，先看看文件系统存在什么问题",                      // cue without any check
		"完成标准：报表要好看一点",                               // vague criterion
		"完成标准：sales.csv 的列为 区域、金额等",                  // open-ended column list
		"完成标准：sales.txt 共 3 行数据",                     // rows only for csv/tsv
		"完成标准：../secret.csv 共 3 行数据",                 // traversal
		"完成标准：sales.csv 为 3 行",                       // number followed by a unit is not a value
		"完成标准：sales.csv 里包含 研发部",                     // unquoted literal is ambiguous
		"DoD: `report.csv`", // a backticked path is not a command
		"完成标准：`区域 金额`",      // non-ASCII backtick text is not a command
		"结果应为 5050，写到 a.txt 或 b.txt，完成标准见上", // a bare value with two candidate files
		"the file system exists; definition of done pending",
	} {
		if got := DeriveFromHumanText(text); len(got) != 0 {
			t.Errorf("%q derived %v", text, describe(got))
		}
	}
}

func TestDeriveFromModelCriteriaNeedsNoCueButNoCommands(t *testing.T) {
	got := describe(DeriveFromModelCriteria([]string{"report.csv 共 3 行数据", "`rm -rf /` passes", "Definition of done: `make test`"}))
	if len(got) != 1 || got[0] != `{"id":"","kind":"artifact_contents","file":"report.csv","data_rows":3}` {
		t.Fatalf("model criteria %v", got)
	}
}

func TestCheckIDsAreStableAndOptionalAgnostic(t *testing.T) {
	a, _, err := NormalizeChecks([]Check{{Kind: KindArtifactContents, File: "a.csv", DataRows: intPtr(3)}}, "")
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := NormalizeChecks([]Check{{Kind: KindArtifactContents, File: "dir/a.csv", DataRows: intPtr(3), Optional: true}}, "")
	if err != nil {
		t.Fatal(err)
	}
	if a[0].ID != b[0].ID || !checkIDFormat.MatchString(a[0].ID) {
		t.Fatalf("ids %s %s", a[0].ID, b[0].ID)
	}
	for _, bad := range []Check{
		{Kind: KindExecutionOutput, Expect: "5050"}, // value without produced output
		{Kind: KindArtifactContents, File: "/etc/passwd"},
		{Kind: KindArtifactContents, File: "a.txt", Columns: []string{"x"}},
		{Kind: KindDeliveryReceipt, Delivery: "any_chat"},
		{Kind: KindExecutionOutput, File: "a.txt", Expect: "1", Field: "total"},
	} {
		if _, _, err := NormalizeChecks([]Check{bad}, ""); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
}
