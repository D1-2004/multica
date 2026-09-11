package scenememory

import (
	"errors"
	"fmt"
	"testing"

	"github.com/multica-ai/multica/server/internal/dwsclient"
)

func notInConversation() error {
	return fmt.Errorf("history page: %w", dwsclient.NewHistoryError(map[string]string{
		"category": "api", "reason": "business_error", "server_error_code": dwsclient.ServerErrorNotInConversation, "trace_id": "2136268017890559202955757e04bb",
	}))
}

func otherBusinessError() error {
	return fmt.Errorf("history page: %w", dwsclient.NewHistoryError(map[string]string{
		"category": "api", "reason": "business_error", "server_error_code": "1001", "trace_id": "t",
	}))
}

// 口香糖小队 on 2026-09-10: DingTalk 130003 "OpenId is not in conversation"
// was retried to attempt 130 on the 15-minute backoff. Membership loss is
// terminal; the dirty upsert unblocks when the scene sends again.
func TestClassifyHistoryMapsMembershipLossToTerminalCode(t *testing.T) {
	err := classifyHistory(notInConversation())
	if code := FlushErrorCode(err); code != ErrorNotInConversation {
		t.Fatalf("code = %q, want %q", code, ErrorNotInConversation)
	}
	if !TerminalFlushCode(ErrorNotInConversation) {
		t.Fatal("membership loss must block, not retry")
	}
	var cliErr *dwsclient.HistoryError
	if !errors.As(err, &cliErr) || cliErr.ServerErrorCode() != "130003" {
		t.Fatalf("diagnostic fields lost: %v", err)
	}
	if code := FlushErrorCode(classifyHistory(otherBusinessError())); code != ErrorHistoryUnavailable {
		t.Fatalf("other business errors stay HISTORY_UNAVAILABLE, got %q", code)
	}
}

func TestBlockAfterFailureCapsRepeatingBusinessErrors(t *testing.T) {
	business := classifyHistory(otherBusinessError())
	if !HistoryBusinessError(business) {
		t.Fatal("business rejection not recognised")
	}
	if BlockAfterFailure(ErrorHistoryUnavailable, business, MaxHistoryBusinessErrorAttempts-1) {
		t.Fatal("below the ceiling the scene keeps retrying")
	}
	if !BlockAfterFailure(ErrorHistoryUnavailable, business, MaxHistoryBusinessErrorAttempts) {
		t.Fatal("at the ceiling a repeating business rejection must block")
	}
	// Timeouts and transport errors never hit the ceiling.
	timeout := classifyHistory(errors.New("dws timeout"))
	if HistoryBusinessError(timeout) || BlockAfterFailure(ErrorHistoryUnavailable, timeout, 500) {
		t.Fatal("transient history failures keep the unbounded backoff")
	}
	if BlockAfterFailure(ErrorLLMTimeout, errors.New("llm"), 500) {
		t.Fatal("LLM timeouts are not history business errors")
	}
	if !BlockAfterFailure(ErrorAuth, errors.New("binding gone"), 1) || !BlockAfterFailure(ErrorNotInConversation, notInConversation(), 1) {
		t.Fatal("terminal codes block on the first attempt")
	}
}
