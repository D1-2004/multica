package humanquestion

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDecodeDoesNotExtractControlFromTextOrEnvelopes(t *testing.T) {
	valid := `{"version":"tag-round-result/v1","summary":"Draft ready."}`
	quoted, err := json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		"Draft ready; tell me whom to send it to.",
		"```json\n" + valid + "\n```",
		"stdout: " + valid,
		string(quoted),
		`{"payload":` + valid + `}`,
		`[` + valid + `]`,
		`{"summary":"Ready."}`,
		`{"version":"tag-round-result/v2","summary":"Ready.","scene_id":"other"}`,
		`{"Version":"tag-round-result/v1","summary":"Ready."}`,
		`{"summary":"version=tag-round-result/v1"}`,
	} {
		t.Run(raw, func(t *testing.T) {
			_, recognized, err := Decode(raw)
			if recognized || err != nil {
				t.Fatalf("untrusted text promoted into control: recognized=%v err=%v", recognized, err)
			}
		})
	}
}

func TestDecodeRejectsMalformedOrCompetingControl(t *testing.T) {
	valid := `{"version":"tag-round-result/v1","summary":"Ready."}`
	for _, raw := range []string{
		`{"version":"tag-round-result/v1",`,
		`{"version":"tag-round-result/v1","summary":`,
		valid + "\n" + valid,
		valid + "\nstdout: task finished",
		`{"version":"tag-round-result/v1","summary":"A","summary":"B"}`,
		`{"version":"tag-round-result/v2","version":"tag-round-result/v1","summary":"Ready."}`,
		`{"version":"tag-round-result/v1","version":"tag-round-result/v2","summary":"Ready."}`,
		`{"version":"tag-round-result/v1","summary":null}`,
		`{"version":"tag-round-result/v1","summary":" "}`,
		`{"version":"tag-round-result/v1","summary":"Ready.","scene_id":"elsewhere"}`,
		`{"version":"tag-round-result/v1","Summary":"Ready."}`,
	} {
		t.Run(raw, func(t *testing.T) {
			_, recognized, err := Decode(raw)
			if !recognized || err == nil {
				t.Fatalf("claimed malformed control was not rejected: recognized=%v err=%v", recognized, err)
			}
		})
	}
}

func TestDecodeRejectsInvalidQuestions(t *testing.T) {
	base := `{"intent":"clarify","kind":"single","question":"Which person?","options":[{"id":"a","label":"Design"},{"id":"b","label":"Engineering"}]}`
	tests := map[string]string{
		"duplicate ids":          strings.Replace(base, `"id":"b"`, `"id":"a"`, 1),
		"empty id":               strings.Replace(base, `"id":"b"`, `"id":" "`, 1),
		"padded id":              strings.Replace(base, `"id":"b"`, `"id":" b"`, 1),
		"missing label":          strings.Replace(base, `"label":"Engineering"`, `"label":" "`, 1),
		"one option":             strings.Replace(base, `,{"id":"b","label":"Engineering"}`, "", 1),
		"single max two":         strings.TrimSuffix(base, "}") + `,"max":2}`,
		"negative bound":         strings.TrimSuffix(base, "}") + `,"min":-1}`,
		"fractional bound":       strings.TrimSuffix(base, "}") + `,"max":1.5}`,
		"range exceeds options":  strings.Replace(strings.TrimSuffix(base, "}"), `"kind":"single"`, `"kind":"multiple"`, 1) + `,"min":1,"max":3}`,
		"reversed range":         strings.Replace(strings.TrimSuffix(base, "}"), `"kind":"single"`, `"kind":"multiple"`, 1) + `,"min":2,"max":1}`,
		"arbitrary intent":       strings.Replace(base, `"intent":"clarify"`, `"intent":"approved"`, 1),
		"arbitrary card kind":    strings.Replace(base, `"kind":"single"`, `"kind":"approval"`, 1),
		"blank question":         strings.Replace(base, `"question":"Which person?"`, `"question":" "`, 1),
		"wrong bool type":        strings.TrimSuffix(base, "}") + `,"allow_custom":"true"}`,
		"null bool":              strings.TrimSuffix(base, "}") + `,"allow_custom":null}`,
		"null description":       strings.Replace(base, `"label":"Engineering"`, `"label":"Engineering","description":null`, 1),
		"embedded authorization": strings.Replace(base, `"label":"Engineering"`, `"label":"Engineering","permission":"send"`, 1),
		"task routing":           strings.TrimSuffix(base, "}") + `,"task_id":"another-task"}`,
		"case-insensitive field": strings.TrimSuffix(base, "}") + `,"Max":1}`,
		"duplicate nested key":   strings.Replace(base, `"id":"b"`, `"id":"b","id":"c"`, 1),
		"native person identity": strings.Replace(strings.Replace(base, `"kind":"single"`, `"kind":"person"`, 1), `"id":"b"`, `"id":"b","user_id":"unverified"`, 1),
	}
	for name, choice := range tests {
		t.Run(name, func(t *testing.T) {
			raw := `{"version":"tag-round-result/v1","summary":"Draft ready; not sent.","choice":` + choice + `}`
			_, recognized, err := Decode(raw)
			if !recognized || err == nil {
				t.Fatalf("invalid question accepted: recognized=%v err=%v", recognized, err)
			}
		})
	}
}

func TestDecodePreservesCompletedWorkAndDefaultsChoiceBounds(t *testing.T) {
	for _, kind := range []string{"single", "multiple", "person"} {
		t.Run(kind, func(t *testing.T) {
			raw := `{"version":"tag-round-result/v1","summary":"Draft ready; not sent. Keep it short.","choice":{"intent":"clarify","kind":"` + kind + `","question":"Which candidate?","options":[{"id":"design","label":"Design","description":"Verified candidate from search"},{"id":"engineering","label":"Engineering"}],"allow_custom":true,"min":0,"max":0}}`
			result, recognized, err := Decode(raw)
			if err != nil || !recognized {
				t.Fatalf("recognized=%v err=%v", recognized, err)
			}
			max := 1
			if kind == "multiple" {
				max = 2
			}
			if result.Summary != "Draft ready; not sent. Keep it short." || result.Choice.Min != 1 || result.Choice.Max != max || !result.Choice.AllowCustom {
				t.Fatalf("lost work/constraints or incorrect defaults: %#v %#v", result, result.Choice)
			}
		})
	}
	for _, suffix := range []string{"", `,"choice":null`} {
		result, recognized, err := Decode(`{"version":"tag-round-result/v1","summary":"Work complete."` + suffix + `}`)
		if err != nil || !recognized || result.Choice != nil {
			t.Fatalf("normal completion required an invented question: recognized=%v err=%v result=%#v", recognized, err, result)
		}
	}
}

func TestChoiceEmphasisIsBoundedPresentation(t *testing.T) {
	for _, emphasis := range []string{"primary", "secondary", "none", "danger", "permission"} {
		raw := `{"version":"tag-round-result/v1","summary":"未执行，待确认","choice":{"intent":"clarify","kind":"single","question":"确认修改？","options":[{"id":"confirm","label":"确认","emphasis":"` + emphasis + `"},{"id":"no","label":"暂不修改"}],"allow_custom":true}}`
		got, recognized, err := Decode(raw)
		if !recognized {
			t.Fatal("protocol not recognized")
		}
		valid := emphasis == "primary" || emphasis == "secondary" || emphasis == "none"
		if valid && (err != nil || got.Choice.Options[0].Emphasis != emphasis || !got.Choice.AllowCustom) {
			t.Fatal(got, err)
		}
		if !valid && err == nil {
			t.Fatal("unbounded emphasis accepted", emphasis)
		}
	}
}
