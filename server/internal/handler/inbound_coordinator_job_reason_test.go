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

func TestIsCoordinatorBusyParkReason(t *testing.T) {
	t.Parallel()
	cases := []struct {
		reason string
		park   bool
	}{
		{"dispatch rejected with HTTP 409: recalled issue already has a pending agent task", true},
		{"dispatch rejected with HTTP 409: issue already has an active task", true},
		{"issue_busy", true},
		{"dispatch rejected with HTTP 409: active duplicate issue exists", true},
		{"dispatch rejected with HTTP 422: attachment download URL has expired", false},
	}
	for _, tc := range cases {
		if got := isCoordinatorBusyParkReason(tc.reason); got != tc.park {
			t.Fatalf("reason %q park=%v want %v", tc.reason, got, tc.park)
		}
		silence := shouldSilenceCoordinatorBusyPark(http.StatusConflict, tc.reason)
		if silence != tc.park {
			t.Fatalf("reason %q silence=%v want %v", tc.reason, silence, tc.park)
		}
	}
	if shouldSilenceCoordinatorBusyPark(http.StatusInternalServerError, "recalled issue already has a pending agent task") {
		t.Fatal("chat 500 must not be treated as a silent park")
	}
}

func TestDispatchIdempotencyEndpointIDPrefersCommandNamespace(t *testing.T) {
	t.Parallel()
	ns := parseUUID("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	got := dispatchIdempotencyEndpointID(
		DispatchCommand{DispatchEndpointID: uuidToString(ns)},
		agentDispatchContext{EndpointID: "public-endpoint", EndpointNamespaceID: ns},
	)
	if got != uuidToString(ns) {
		t.Fatalf("got %q want namespace uuid", got)
	}
	empty := dispatchIdempotencyEndpointID(
		DispatchCommand{},
		agentDispatchContext{EndpointID: "public-endpoint", EndpointNamespaceID: ns},
	)
	if empty != uuidToString(ns) {
		t.Fatalf("empty command must fall back to namespace, got %q", empty)
	}
}
