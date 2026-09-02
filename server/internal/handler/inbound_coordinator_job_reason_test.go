package handler

import (
	"net/http"
	"strings"
	"testing"
)

func TestDispatchRejectReasonIncludesBody(t *testing.T) {
	t.Parallel()
	resp := newBufferedDispatchResponse()
	writeError(resp, http.StatusUnprocessableEntity, "attachment download URL has expired")
	got := dispatchRejectReason(resp)
	if !strings.Contains(got, "422") || !strings.Contains(got, "expired") {
		t.Fatalf("got %q", got)
	}
}

func TestDispatchRejectReasonEmptyBody(t *testing.T) {
	t.Parallel()
	resp := newBufferedDispatchResponse()
	resp.WriteHeader(http.StatusForbidden)
	got := dispatchRejectReason(resp)
	if got != "dispatch rejected with HTTP 403" {
		t.Fatalf("got %q", got)
	}
}
