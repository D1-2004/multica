package handler

import (
	"context"
	"encoding/json"
	"testing"
)

func TestEmployeeRunNoticeSuccessPreservesSanitizedOutput(t *testing.T) {
	for _, tc := range []struct{ name, output, want string }{
		{"program_line", "EXEC|run-123|3.11.2", "EXEC|run-123|3.11.2"},
		{"formatting", "  first line\n    indented second line\n", "  first line\n    indented second line\n"},
		{"redaction", "result sk-123456789012345678901234\n", "result [REDACTED API KEY]\n"},
		{"empty", "", "本次执行已完成，未返回文本结果。"},
		{"whitespace", " \n\t", "本次执行已完成，未返回文本结果。"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := employeeNoticeBody(employeeNoticeBinding{ResultState: "succeeded", Result: tc.output}); got != tc.want {
				t.Fatalf("successful notice changed output: got %q want %q", got, tc.want)
			}
		})
	}
}

func TestEmployeeRunNoticeSuccessOutboxKeepsExactRunOutput(t *testing.T) {
	f := employeeNoticeDatabase(t, "succeeded", false, false)
	if _, err := reconcileEmployeeNotice(t, f.h); err != nil {
		t.Fatal(err)
	}
	var result, body string
	var raw []byte
	if err := testPool.QueryRow(context.Background(), `SELECT r.result,n.body,a.input FROM employee_task_run r JOIN employee_run_notice n ON n.run_id=r.id JOIN response_action a ON a.id=n.action_id WHERE r.id=$1`, f.runID).Scan(&result, &body, &raw); err != nil {
		t.Fatal(err)
	}
	var input struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &input); err != nil {
		t.Fatal(err)
	}
	if body != result || input.Text != result {
		t.Fatalf("Host added prose to executor output: result=%q notice=%q outbox=%q", result, body, input.Text)
	}
	if f.model.calls != 1 {
		t.Fatalf("notice added model calls: %d", f.model.calls)
	}
}
