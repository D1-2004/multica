package auth

import (
	"strings"
	"testing"
	"time"
)

func TestSceneTokenRoundTripAndRefusals(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	claims := SceneTokenClaims{WorkspaceID: "ws", AgentID: "agent", TaskID: "task", SceneID: "scene"}
	token, err := IssueSceneToken(claims, now)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(token, SceneTokenPrefix) || strings.ContainsAny(token, "/?#=+ ") {
		t.Fatalf("token is not a path-safe sct_ token: %q", token)
	}
	got, err := VerifySceneToken(token, now.Add(time.Hour))
	if err != nil || got.TaskID != "task" || got.SceneID != "scene" || got.AgentID != "agent" || got.WorkspaceID != "ws" {
		t.Fatalf("verify = %+v %v", got, err)
	}
	other, _ := IssueSceneToken(claims, now)
	if other == token {
		t.Fatal("two issues of the same claims must differ (nonce)")
	}

	body, signature, _ := strings.Cut(strings.TrimPrefix(token, SceneTokenPrefix), ".")
	forgedBody, _ := IssueSceneToken(SceneTokenClaims{WorkspaceID: "ws", AgentID: "agent", TaskID: "task", SceneID: "other-scene"}, now)
	otherBody, _, _ := strings.Cut(strings.TrimPrefix(forgedBody, SceneTokenPrefix), ".")
	for name, candidate := range map[string]string{
		"expired":          token,
		"swapped payload":  SceneTokenPrefix + otherBody + "." + signature,
		"truncated":        SceneTokenPrefix + body,
		"wrong prefix":     "mat_" + body + "." + signature,
		"tampered sig":     SceneTokenPrefix + body + "." + strings.Repeat("A", len(signature)),
		"empty":            "",
		"payload not json": SceneTokenPrefix + "bm90LWpzb24" + "." + signature,
	} {
		at := now.Add(time.Hour)
		if name == "expired" {
			at = now.Add(SceneTokenTTL)
		}
		if _, err := VerifySceneToken(candidate, at); err == nil {
			t.Errorf("%s: verified", name)
		}
	}
}
