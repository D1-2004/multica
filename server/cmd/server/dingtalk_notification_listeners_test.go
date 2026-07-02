package main

import "testing"

func TestShouldSkipDingTalkMemberAddedNotification(t *testing.T) {
	t.Run("skips invitee accepting their own invitation", func(t *testing.T) {
		if !shouldSkipDingTalkMemberAddedNotification("user-1", "user-1") {
			t.Fatal("expected self-authored member:added to be skipped")
		}
	})

	t.Run("allows admin-added members", func(t *testing.T) {
		if shouldSkipDingTalkMemberAddedNotification("admin-1", "user-1") {
			t.Fatal("expected admin-authored member:added to be sent")
		}
	})

	t.Run("allows system-authored members", func(t *testing.T) {
		if shouldSkipDingTalkMemberAddedNotification("", "user-1") {
			t.Fatal("expected system-authored member:added to be sent")
		}
	})
}

func TestDingTalkNotificationURLs(t *testing.T) {
	l := &dingTalkNotificationListener{appURL: "https://fde.example"}

	if got := l.issueURL("team one", "MUL-1"); got != "https://fde.example/team%20one/issues/MUL-1" {
		t.Fatalf("issueURL = %q", got)
	}
	if got := l.invitationURL("invite-1"); got != "https://fde.example/invite/invite-1" {
		t.Fatalf("invitationURL = %q", got)
	}
	if got := l.workspaceURL(""); got != "" {
		t.Fatalf("workspaceURL(empty) = %q", got)
	}
}
