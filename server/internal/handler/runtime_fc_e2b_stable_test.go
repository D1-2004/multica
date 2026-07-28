package handler

import (
	"net/http/httptest"
	"testing"
)

func TestCanPublishFCE2BStableUsesOnlyAuthenticatedUserUUID(t *testing.T) {
	const allowed = "410d0a06-a026-449b-b7ab-64c9d92481bd"
	h := &Handler{cfg: Config{
		StableRuntimePublisherUserIDs: map[string]struct{}{allowed: {}},
	}}

	request := httptest.NewRequest("GET", "/api/runtimes/fc-e2b/stable-channel", nil)
	request.Header.Set("X-User-ID", allowed)
	request.Header.Set("X-DingTalk-Corp-ID", "untrusted-corp")
	request.Header.Set("X-Auth-Method", "untrusted-method")
	if !h.canPublishFCE2BStable(request) {
		t.Fatal("exact authenticated Multica UUID was rejected")
	}

	request.Header.Set("X-User-ID", "2c508db5-5410-41f9-ad69-d3d527529c20")
	request.Header.Set("X-DingTalk-Corp-ID", "")
	if h.canPublishFCE2BStable(request) {
		t.Fatal("non-allow-listed UUID was accepted")
	}

	request.Header.Set("X-User-ID", "")
	request.Header.Set("X-Requested-User-ID", allowed)
	if h.canPublishFCE2BStable(request) {
		t.Fatal("client-supplied alternate user ID was accepted")
	}
}
