package handler

import "testing"

func TestIsAgentOwnedSkill(t *testing.T) {
	t.Parallel()
	if isAgentOwnedSkill("builtin", "anything") {
		t.Fatal("builtin source must be ignored")
	}
	if isAgentOwnedSkill("", "builtin:multica-working-on-issues") {
		t.Fatal("builtin-prefixed ids must be ignored")
	}
	if !isAgentOwnedSkill("workspace", "skill-1") {
		t.Fatal("attached workspace skills must count")
	}
}

func TestIsDirectChatForWarmResume(t *testing.T) {
	t.Parallel()

	tests := []struct {
		chatType string
		want     bool
	}{
		{chatType: "", want: true},
		{chatType: "p2p", want: true},
		{chatType: " group ", want: false},
		{chatType: "group", want: false},
		{chatType: "channel", want: false},
	}
	for _, tc := range tests {
		if got := isDirectChatForWarmResume(tc.chatType); got != tc.want {
			t.Errorf("isDirectChatForWarmResume(%q) = %v, want %v", tc.chatType, got, tc.want)
		}
	}
}

func TestCloudChatResumeIdentityChangesWithAgentInputs(t *testing.T) {
	t.Parallel()

	base := cloudChatResumeIdentity("runtime-1", "artifact-a", "be helpful", []string{"skill-a:h1"}, []string{"provider/tool"})
	if base == "" {
		t.Fatal("identity must not be empty")
	}
	if cloudChatResumeIdentity("runtime-1", "artifact-a", "be helpful", []string{"skill-a:h1"}, []string{"provider/tool"}) != base {
		t.Fatal("identical inputs must produce the same identity")
	}
	if cloudChatResumeIdentity("runtime-2", "artifact-a", "be helpful", []string{"skill-a:h1"}, []string{"provider/tool"}) == base {
		t.Fatal("runtime change must change identity")
	}
	if cloudChatResumeIdentity("runtime-1", "artifact-b", "be helpful", []string{"skill-a:h1"}, []string{"provider/tool"}) == base {
		t.Fatal("artifact change must change identity")
	}
	if cloudChatResumeIdentity("runtime-1", "artifact-a", "be terse", []string{"skill-a:h1"}, []string{"provider/tool"}) == base {
		t.Fatal("instructions change must change identity")
	}
	if cloudChatResumeIdentity("runtime-1", "artifact-a", "be helpful", []string{"skill-b:h1"}, []string{"provider/tool"}) == base {
		t.Fatal("skill change must change identity")
	}
	if cloudChatResumeIdentity("runtime-1", "artifact-a", "be helpful", []string{"skill-a:h2"}, []string{"provider/tool"}) == base {
		t.Fatal("skill content hash change must change identity")
	}
	if cloudChatResumeIdentity("runtime-1", "artifact-a", "be helpful", []string{"skill-a:h1"}, nil) == base {
		t.Fatal("disabled-skill change must change identity")
	}
	// Skill token order must not matter.
	left := cloudChatResumeIdentity("runtime-1", "", "", []string{"b:1", "a:1"}, nil)
	right := cloudChatResumeIdentity("runtime-1", "", "", []string{"a:1", "b:1"}, nil)
	if left != right {
		t.Fatal("skill token order must not change identity")
	}
}
