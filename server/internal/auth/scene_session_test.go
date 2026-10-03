package auth

import (
	"strings"
	"testing"
	"time"
)

func TestSceneSessionRoundTripAndRejection(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	sess := SceneSession{
		WorkspaceID: "11111111-1111-4111-8111-111111111111",
		AgentID:     "22222222-2222-4222-8222-222222222222",
		OrgID:       "439446171",
		ScopeType:   "scene",
		ScopeKey:    "f6daee25-f5fe-4bfe-a50a-e6ff5707f65b",
		UserID:      "33333333-3333-4333-8333-333333333333",
		ExpiresAt:   now.Add(SceneSessionTTL).Unix(),
	}
	token, err := SignSceneSession(sess, now)
	if err != nil || !strings.Contains(token, ".") {
		t.Fatalf("sign %q %v", token, err)
	}
	got, err := OpenSceneSession(token, now)
	if err != nil || got != sess {
		t.Fatalf("open %+v %v", got, err)
	}
	if _, err := OpenSceneSession(token, now.Add(SceneSessionTTL+time.Second)); err == nil {
		t.Fatal("expired token accepted")
	}
	tampered := token[:len(token)-2] + "aa"
	if _, err := OpenSceneSession(tampered, now); err == nil {
		t.Fatal("tampered token accepted")
	}
	sess.ScopeType = "org"
	if _, err := SignSceneSession(sess, now); err == nil {
		t.Fatal("org session signed")
	}
}
