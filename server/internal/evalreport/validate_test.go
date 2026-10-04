package evalreport

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func exampleSubmission() Submission {
	return Submission{
		SchemaVersion: 1, RunID: "7e853a4a-7317-47f4-a618-f28e4d1fe12a",
		Title: "本地群聊回归", ExecutionKind: "real_e2e", Environment: "isolated-test",
		TargetRevision: strings.Repeat("a", 40), Runner: Runner{Name: "local-evals", Version: "1"},
		StartedAt:  time.Date(2026, 10, 4, 10, 0, 0, 0, time.FixedZone("UTC+8", 8*3600)),
		FinishedAt: time.Date(2026, 10, 4, 10, 2, 0, 0, time.FixedZone("UTC+8", 8*3600)),
		Catalog:    Catalog{Revision: strings.Repeat("b", 40), Dirty: true, SHA256: strings.Repeat("c", 64)},
		SelectedCases: []CaseDefinition{
			{ID: "new-office-case", Title: "本地新增的办公用例", Kind: "office", ScenarioID: "new-local-scene", Roles: []string{"主持人", "员工"}, Verifies: []string{"只响应有效请求"}, Method: []string{"比对回读与预期断言"}},
			{ID: "G01", Title: "黄金用例快照", Kind: "p0", ScenarioID: "", Roles: []string{"员工"}, Verifies: []string{"处理当前任务"}, Method: []string{"核对最终观察"}},
		},
		Results: []CaseResult{
			{CaseID: "new-office-case", Status: "blocked", Summary: "独立环境尚未完成准备", Evidence: []Evidence{}},
			{CaseID: "G01", Status: "pass", Summary: "实际观察与断言一致", Evidence: []Evidence{{Kind: "artifact", Reference: "evidence/golden-observation.json"}}},
		},
	}
}

func encoded(t *testing.T, in Submission) []byte {
	t.Helper()
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestNormalizeFreezesLocalDefinitionsAndCountsUnfinishedCases(t *testing.T) {
	in := exampleSubmission()
	normalized, summary, contentHash, definitionHash, err := Normalize(in)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Total != 2 || summary.Pass != 1 || summary.Blocked != 1 || summary.PassRate != 0.5 || summary.Conclusion != "incomplete" || summary.P0Selected != 1 || summary.P0Total != 20 || summary.RealE2EPass != 1 {
		t.Fatalf("summary: %+v", summary)
	}
	if normalized.SelectedCases[0].ID != "G01" || normalized.SelectedCases[1].ScenarioID != "new-local-scene" || normalized.StartedAt.Location() != time.UTC || len(contentHash) != 64 || len(definitionHash) != 64 {
		t.Fatalf("normalization: %+v", normalized)
	}
	normalized.SelectedCases[0].Roles[0] = "Changed"
	if in.SelectedCases[1].Roles[0] == "Changed" {
		t.Fatal("normalization mutated caller snapshot")
	}
}

func TestEquivalentTimesFieldOrderAndArrayOrderReplaySameHash(t *testing.T) {
	in := exampleSubmission()
	_, _, hash, definitionHash, err := Normalize(in)
	if err != nil {
		t.Fatal(err)
	}
	in.SelectedCases[0], in.SelectedCases[1] = in.SelectedCases[1], in.SelectedCases[0]
	in.Results[0], in.Results[1] = in.Results[1], in.Results[0]
	in.StartedAt = in.StartedAt.UTC()
	in.FinishedAt = in.FinishedAt.UTC()
	in.RunID = strings.ToUpper(in.RunID)
	in.TargetRevision = strings.ToUpper(in.TargetRevision)
	in.Catalog.Revision = strings.ToUpper(in.Catalog.Revision)
	in.Catalog.SHA256 = strings.ToUpper(in.Catalog.SHA256)
	var object map[string]any
	if err := json.Unmarshal(encoded(t, in), &object); err != nil {
		t.Fatal(err)
	}
	data, err := json.MarshalIndent(object, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	reordered, err := Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	_, _, other, otherDefinition, err := Normalize(reordered)
	if err != nil || hash != other || definitionHash != otherDefinition {
		t.Fatalf("equivalent replay hashes differ: %v", err)
	}
	in.Results[0].Summary = "另一种实际观察"
	_, _, changed, _, err := Normalize(in)
	if err != nil || changed == hash {
		t.Fatalf("changed observation reused content hash: %v", err)
	}
}

func TestSummaryDoesNotCountMocksAsRealE2EOrDropUnexecutedCases(t *testing.T) {
	for _, kind := range []string{"mock", "definition_check", "real_e2e"} {
		for _, status := range []string{"pass", "fail", "blocked", "incomplete", "skipped"} {
			t.Run(kind+"/"+status, func(t *testing.T) {
				in := exampleSubmission()
				in.ExecutionKind = kind
				in.Results[0].Status = status
				in.Results[0].Evidence = []Evidence{{Kind: "log", Reference: "evidence/redacted-run.txt"}}
				_, summary, _, _, err := Normalize(in)
				if err != nil {
					t.Fatal(err)
				}
				conclusion := "incomplete"
				if status == "pass" {
					conclusion = "pass"
				}
				if status == "fail" {
					conclusion = "fail"
				}
				if summary.Total != 2 || summary.Conclusion != conclusion {
					t.Fatalf("incorrect denominator/conclusion: %+v", summary)
				}
				if kind != "real_e2e" && summary.RealE2EPass != 0 {
					t.Fatalf("mock counted as real: %+v", summary)
				}
			})
		}
	}
}

func TestInvalidSubmissionsFailWithoutReturningSubmittedContent(t *testing.T) {
	mutations := map[string]func(*Submission){
		"missing-result":        func(in *Submission) { in.Results = in.Results[:1] },
		"extra-result":          func(in *Submission) { in.Results = append(in.Results, in.Results[0]) },
		"duplicate-result":      func(in *Submission) { in.Results[0].CaseID = "G01" },
		"unselected-result":     func(in *Submission) { in.Results[0].CaseID = "unknown-local-case" },
		"duplicate-selection":   func(in *Submission) { in.SelectedCases[0] = in.SelectedCases[1] },
		"missing-pass-evidence": func(in *Submission) { in.Results[1].Evidence = []Evidence{} },
		"missing-fail-evidence": func(in *Submission) { in.Results[0].Status = "fail" },
		"backward-time":         func(in *Submission) { in.FinishedAt = in.StartedAt.Add(-time.Second) },
		"bad-p0-id":             func(in *Submission) { in.SelectedCases[1].ID = "G21" },
		"p0-scenario":           func(in *Submission) { in.SelectedCases[1].ScenarioID = "new-local-scene" },
		"missing-roles":         func(in *Submission) { in.SelectedCases[0].Roles = []string{} },
		"blank-assertion":       func(in *Submission) { in.SelectedCases[0].Verifies = []string{"   "} },
		"short-target-revision": func(in *Submission) { in.TargetRevision = "aabbcc" },
		"nil-evidence":          func(in *Submission) { in.Results[0].Evidence = nil },
		"credential-in-summary": func(in *Submission) { in.Results[0].Summary = "password=do-not-return-secret" },
		"credential-in-method": func(in *Submission) {
			in.SelectedCases[0].Method = []string{"Authorization: Bearer do-not-return-secret-token"}
		},
	}
	for name, mutation := range mutations {
		t.Run(name, func(t *testing.T) {
			in := exampleSubmission()
			mutation(&in)
			_, err := Decode(encoded(t, in))
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("expected invalid, got %v", err)
			}
			if strings.Contains(err.Error(), "do-not-return") {
				t.Fatalf("error exposed submitted secret: %s", err)
			}
		})
	}
}

func TestDecodeRejectsUnknownDuplicateAndTrailingFields(t *testing.T) {
	data := encoded(t, exampleSubmission())
	for name, mutated := range map[string][]byte{
		"unknown-authority": append([]byte(`{"submitted_by":"do-not-return-secret",`), data[1:]...),
		"duplicate-run":     append([]byte(`{"run_id":"do-not-return-secret",`), data[1:]...),
		"escaped-duplicate": append([]byte(`{"\u0072un_id":"do-not-return-secret",`), data[1:]...),
		"second-object":     append(bytes.Clone(data), []byte(` {}`)...),
		"malformed":         []byte(`{"schema_version":`),
		"oversize":          bytes.Repeat([]byte(" "), MaxSubmissionBytes+1),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Decode(mutated)
			if !errors.Is(err, ErrInvalid) || strings.Contains(err.Error(), "do-not-return-secret") {
				t.Fatalf("unsafe decode error: %v", err)
			}
		})
	}
	missing := bytes.Replace(data, []byte(`"dirty":true,`), nil, 1)
	if _, err := Decode(missing); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing required false-capable field accepted: %v", err)
	}
}

func TestEvidenceReferencesAreLinksOrRedactedRelativePaths(t *testing.T) {
	for _, reference := range []string{"https://trace.example.com/runs/redacted?view=summary", "evidence/redacted-round.json", ".context/eval-observation.txt"} {
		in := exampleSubmission()
		in.Results[1].Evidence[0].Reference = reference
		if err := Validate(in); err != nil {
			t.Fatalf("valid reference rejected %s: %v", reference, err)
		}
	}
	for _, reference := range []string{"http://example.com/report", "file:///tmp/report", "/Users/private/report", "C:\\reports\\report.json", "../report.json", "evidence/%2e%2e/report.json", "%2freport.json", "//example.com/report", "https://user:password@example.com/run", "https://example.com/run?X-Amz-Signature=do-not-return-secret", "https://example.com/run?%74oken=do-not-return-secret", "https://example.com/run?X-Goog-Credential=secret", "https://example.com/run?sig=secret", "https://example.com/run?token=%zz", "https://example.com/run?view=summary;token=secret", "evidence/report.json?token=secret", "https://example.com/run?password=secret", "~/.context/report", "evidence/./report.json", "evidence/a\\b"} {
		in := exampleSubmission()
		in.Results[1].Evidence[0].Reference = reference
		err := Validate(in)
		if !errors.Is(err, ErrInvalid) || strings.Contains(err.Error(), "do-not-return-secret") {
			t.Fatalf("unsafe reference accepted/error exposed: %v", err)
		}
	}
	if !IsHTTPSReference("https://example.com/report") || IsHTTPSReference("evidence/report.json") || IsHTTPSReference("https://example.com/report?token=secret") {
		t.Fatal("link classification bypassed reference checks")
	}
}

func TestContractUsesSubmissionSchemaAndSeparateResponseShapes(t *testing.T) {
	contract := Contract()
	var document map[string]any
	if json.Unmarshal(contract, &document) != nil || document["openapi"] != "3.1.0" {
		t.Fatal("missing machine-readable OpenAPI contract")
	}
	contract[0] = ' '
	if bytes.Equal(contract, Contract()) {
		t.Fatal("contract exposes shared mutable storage")
	}
	if _, err := submissionSchema(); err != nil {
		t.Fatal(err)
	}
	schemas := document["components"].(map[string]any)["schemas"].(map[string]any)
	metadata := schemas["ReportMetadata"].(map[string]any)["properties"].(map[string]any)
	if metadata["selected_cases"] != nil || metadata["results"] != nil || metadata["submission"] != nil || metadata["title"] == nil || schemas["Receipt"] == nil {
		t.Fatal("list and receipt must stay distinct from full detail snapshots")
	}
}
