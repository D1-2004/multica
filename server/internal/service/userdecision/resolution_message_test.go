package userdecision

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestResolutionFailureMessageExposesOnlyExplicitNonexecution(t *testing.T) {
	for _, err := range []error{errors.New("provider endpoint credential details"), errors.New("constraint_quote internal protocol"), &NonExecutableSubmissionError{}} {
		if got := resolutionFailureMessage(err); got != "无法依据本次选择和补充说明形成明确、合法的处理计划，本次未执行。" {
			t.Fatalf("internal error exposed: %s", got)
		}
	}
	err := fmt.Errorf("wrapped: %w", &NonExecutableSubmissionError{Reason: "用户明确取消本次请求。"})
	if got := resolutionFailureMessage(err); got != "本次未执行：用户明确取消本次请求。" {
		t.Fatal(got)
	}
	got := resolutionFailureMessage(&NonExecutableSubmissionError{Reason: strings.Repeat("说明", 500)})
	if !utf8.ValidString(got) || len([]rune(got)) > 410 {
		t.Fatal("unbounded or invalid message")
	}
}
