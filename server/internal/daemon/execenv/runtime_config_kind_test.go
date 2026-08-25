package execenv

import (
	"strings"
	"testing"
)

// TestClassifyTask pins the precedence rule on classifyTask. All four
// kinds plus tiebreak cases for safety.
func TestClassifyTask(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		ctx  TaskContextForEnv
		want taskKind
	}{
		{"chat", TaskContextForEnv{ChatSessionID: "c"}, kindChat},
		{"quick-create", TaskContextForEnv{QuickCreatePrompt: "p"}, kindQuickCreate},
		{"autopilot", TaskContextForEnv{AutopilotRunID: "r"}, kindAutopilotRunOnly},
		{"issue-comment-triggered", TaskContextForEnv{IssueID: "i", TriggerCommentID: "c"}, kindIssue},
		{"issue-assignment-triggered", TaskContextForEnv{IssueID: "i"}, kindIssue},
		{"issue-bare", TaskContextForEnv{}, kindIssue},
		{"tiebreak-chat-vs-quick", TaskContextForEnv{ChatSessionID: "c", QuickCreatePrompt: "p"}, kindChat},
		{"tiebreak-quick-vs-autopilot", TaskContextForEnv{QuickCreatePrompt: "p", AutopilotRunID: "r"}, kindQuickCreate},
		{"tiebreak-autopilot-vs-comment", TaskContextForEnv{AutopilotRunID: "r", IssueID: "i", TriggerCommentID: "c"}, kindAutopilotRunOnly},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := classifyTask(tc.ctx); got != tc.want {
				t.Errorf("classifyTask: got %d, want %d", got, tc.want)
			}
		})
	}
}

// TestTaskKindHasIssueContext pins the predicate that gates Project
// Context / Issue Metadata / Sub-issue Creation in the slim dispatcher.
func TestTaskKindHasIssueContext(t *testing.T) {
	t.Parallel()
	cases := []struct {
		kind taskKind
		want bool
	}{
		{kindIssue, true},
		{kindAutopilotRunOnly, false},
		{kindQuickCreate, false},
		{kindChat, false},
	}
	for _, tc := range cases {
		if got := tc.kind.hasIssueContext(); got != tc.want {
			t.Errorf("kind=%d hasIssueContext: got %v, want %v", tc.kind, got, tc.want)
		}
	}
}

// TestBuildMetaSkillContentBriefContent pins that buildMetaSkillContent
// renders the (now sole) brief: the `issue get` one-liner is present and
// the retired legacy verbose description is not.
func TestBuildMetaSkillContentBriefContent(t *testing.T) {
	t.Parallel()

	out := buildMetaSkillContent("claude", TaskContextForEnv{
		IssueID:          "issue-1",
		TriggerCommentID: "comment-1",
		AgentName:        "Eve",
		AgentID:          "eve-1",
	})

	if !strings.Contains(out, "- `multica issue get <id> --output json` — full issue.\n") {
		t.Errorf("brief is missing the `issue get` one-liner\n---\n%s", out)
	}
	if strings.Contains(out, "Get full issue details.") {
		t.Errorf("brief still carries the retired legacy `issue get` description\n---\n%s", out)
	}
}

// TestBuildMetaSkillContentIssueBodyFormatting pins the shared issue-body
// hierarchy rule across every task kind that can author an issue.
func TestBuildMetaSkillContentIssueBodyFormatting(t *testing.T) {
	t.Parallel()

	fixtures := map[string]TaskContextForEnv{
		"issue":        {IssueID: "i-1"},
		"autopilot":    {AutopilotRunID: "r-1"},
		"quick-create": {QuickCreatePrompt: "create an issue"},
		"chat":         {ChatSessionID: "c-1"},
	}

	for name, ctx := range fixtures {
		name, ctx := name, ctx
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			out := buildMetaSkillContent("codex", ctx)
			for _, want := range []string{
				"## Issue Body Formatting",
				"An issue title already serves as its H1.",
				// The rule covers BOTH surfaces: `description` is the CLI/API
				// field name, `body` the UI term — the alias is a cross-surface
				// mapping, not prose (MUL-5442 stage-1 review).
				"do not add a Markdown H1 (`# ...`) to an issue body or description",
				"start with prose or `##` subheadings",
				"Only add an H1 when the user specifically requests one",
			} {
				if !strings.Contains(out, want) {
					t.Errorf("brief is missing issue-body formatting guidance %q\n---\n%s", want, out)
				}
			}
		})
	}
}

// TestBuildMetaSkillContentSlimKindMatrix locks in which sections the
// slim brief emits per task kind, machine-checking the matrix documented
// on `buildMetaSkillContentSlim`. Heading is matched as a discrete line
// (preceded by newline + followed by newline) so inline references like
// "see ## Comment Formatting" do not trip the absence assertions.
func TestBuildMetaSkillContentSlimKindMatrix(t *testing.T) {

	baseRepo := []RepoContextForEnv{{URL: "https://example.com/x.git", Description: "x"}}
	baseSkill := []SkillContextForEnv{{Name: "skill-x", Description: "x"}}

	type sectionCheck struct {
		heading  string
		mustHave map[taskKind]bool
	}
	allKinds := map[taskKind]bool{
		kindIssue: true, kindAutopilotRunOnly: true,
		kindQuickCreate: true, kindChat: true,
	}
	issueKinds := map[taskKind]bool{kindIssue: true}
	noKinds := map[taskKind]bool{}
	checks := []sectionCheck{
		{"# Multica Agent Runtime", allKinds},
		{"## Background Task Safety", allKinds},
		{"## Agent Identity", allKinds},
		{"## Available Commands", allKinds},
		{"## Issue Body Formatting", allKinds},
		{"### Workflow", allKinds},
		{"## Important: Always Use the `multica` CLI", allKinds},
		{"## Output", allKinds},
		{"## Comment Formatting", issueKinds},
		{"## Repositories", map[taskKind]bool{
			kindIssue: true, kindAutopilotRunOnly: true, kindChat: true,
		}},
		{"## Issue Metadata", issueKinds},
		{"## Instruction Precedence", issueKinds},
		{"## Sub-issue Creation", issueKinds},
		// Provider-gated now, not kind-gated: this matrix runs on "claude",
		// which lists the daemon-written SKILL.md files itself, so the brief
		// suppresses its own index. TestBriefSkillsIndexIsProviderGated covers
		// both sides of that gate.
		{"## Skills", noKinds},
		{"## Mentions", issueKinds},
		{"## Attachments", issueKinds},
	}

	fixtures := map[taskKind]TaskContextForEnv{
		kindChat: {ChatSessionID: "c-1", AgentName: "Eve", AgentID: "eve-1",
			Repos: baseRepo, AgentSkills: baseSkill},
		kindQuickCreate: {QuickCreatePrompt: "p", AgentName: "Eve", AgentID: "eve-1",
			Repos: baseRepo, AgentSkills: baseSkill},
		kindAutopilotRunOnly: {AutopilotRunID: "r-1", AgentName: "Eve", AgentID: "eve-1",
			Repos: baseRepo, AgentSkills: baseSkill},
		kindIssue: {IssueID: "i-1", AgentName: "Eve", AgentID: "eve-1",
			Repos: baseRepo, AgentSkills: baseSkill},
	}

	for kind, ctx := range fixtures {
		out := buildMetaSkillContent("claude", ctx)
		for _, c := range checks {
			needle := "\n" + c.heading + "\n"
			firstLine := c.heading + "\n"
			present := strings.HasPrefix(out, firstLine) || strings.Contains(out, needle)
			want := c.mustHave[kind]
			if want && !present {
				t.Errorf("kind=%d: expected heading %q in slim brief", kind, c.heading)
			}
			if !want && present {
				t.Errorf("kind=%d: heading %q should NOT be in slim brief (matrix gating regression)", kind, c.heading)
			}
		}
	}
}

// TestBriefDueDateTeachesCalendarDayFormat pins the --due-date synopsis to
// the calendar-day format the server canonically accepts
// (util.ParseCalendarDate: YYYY-MM-DD; an RFC3339 value passes only at exact
// UTC midnight). MUL-5696 found the brief teaching `<RFC3339>` while the CLI
// help and the projects skill say YYYY-MM-DD, steering agents that computed a
// natural timestamp into 400s.
func TestBriefDueDateTeachesCalendarDayFormat(t *testing.T) {
	for name, ctx := range map[string]TaskContextForEnv{
		"issue":        {IssueID: "issue-1"},
		"quick-create": {QuickCreatePrompt: "create an issue"},
	} {
		out := buildMetaSkillContent("claude", ctx)
		if !strings.Contains(out, "--due-date <YYYY-MM-DD>") {
			t.Errorf("%s brief missing the calendar-day --due-date synopsis", name)
		}
		if strings.Contains(out, "--due-date <RFC3339>") {
			t.Errorf("%s brief still teaches --due-date <RFC3339>, which the server rejects except at UTC midnight (MUL-5696)", name)
		}
	}
}

// TestChatAvailableCommandsDropsIssueScopedSurface locks the chat variant of
// Available Commands.
//
// A chat run has no issue id. Every command that takes one is unreachable from
// it, and the `## Issue Metadata` section that says when to touch the bag is
// already out of scope for the kind — so those bullets shipped on every chat
// turn as commands with no governing policy, led by the longest single line in
// the brief. What a chat turn actually reaches for stays, and `--help` remains
// the route to everything else.
func TestChatAvailableCommandsDropsIssueScopedSurface(t *testing.T) {
	t.Parallel()

	out := buildMetaSkillContent("claude", TaskContextForEnv{
		ChatSessionID: "chat-1", AgentName: "Eve", AgentID: "eve-1",
	})

	for _, want := range []string{
		"## Available Commands",
		"`multica <command> --help`",
		"multica mcp tools --output json",
		"multica mcp call --method <name>",
		"multica issue list",
		"multica issue get <id> --output json",
		"multica issue create --title",
		"multica issue status <id> <status>",
		"multica repo checkout <url>",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("chat Available Commands missing %q\n---\n%s", want, out)
		}
	}

	for _, banned := range []string{
		"multica issue comment list <issue-id>",
		"multica issue comment add <issue-id>",
		"multica issue metadata list <issue-id>",
		"multica issue metadata set <issue-id>",
		"multica issue metadata delete <issue-id>",
		"multica issue children <id>",
	} {
		if strings.Contains(out, banned) {
			t.Errorf("chat brief advertises issue-scoped command %q with no issue to use it on", banned)
		}
	}

	// The delegation policy owns the delegate flags; the command list must not
	// grow a second copy that can drift from the policy governing it.
	if !strings.Contains(out, "## Background Issue Delegation") {
		t.Error("chat brief must carry the delegation policy — it is what keeps the room responsive")
	}
	if strings.Count(out, "multica issue delegate --title") != 1 {
		t.Error("delegate flags must appear exactly once, inside the policy that governs them")
	}
	// Ending the turn after a successful handoff has to be reconciled with
	// Background Task Safety, which is in the same brief and forbids ending a
	// turn while waiting on something.
	if !strings.Contains(out, "not a background process you are waiting on") {
		t.Error("delegation must state why ending the turn is not the forbidden background-and-yield")
	}
}

// An A2A invocation is a chat session by construction, so it lands on kindChat —
// but it runs without a task-scoped token and every task-token endpoint refuses
// it, `multica issue delegate` included. Emitting the delegation policy there
// would be a mandatory instruction that can only 403, and its advice is the
// inverse of the A2A contract (answer the caller directly). Everything else the
// chat brief carries still applies.
func TestA2AChatBriefOmitsDelegationItCannotPerform(t *testing.T) {
	t.Parallel()

	a2a := buildMetaSkillContent("claude", TaskContextForEnv{
		ChatSessionID: "chat-1", A2AInvocation: true, AgentName: "Eve", AgentID: "eve-1",
	})
	if strings.Contains(a2a, "## Background Issue Delegation") {
		t.Error("A2A run told to delegate through an endpoint that refuses it for lack of a task token")
	}
	if strings.Contains(a2a, "multica issue delegate") {
		t.Error("A2A brief still advertises the delegate command")
	}
	if !strings.Contains(a2a, "**You are in chat mode.**") {
		t.Error("A2A is still a chat run and keeps the rest of the chat brief")
	}
	// A pointer to a section the file does not contain is worse than no pointer.
	if strings.Contains(a2a, "see `## Background Issue Delegation`") {
		t.Error("A2A workflow points at a section its own brief omits")
	}

	human := buildMetaSkillContent("claude", TaskContextForEnv{
		ChatSessionID: "chat-1", AgentName: "Eve", AgentID: "eve-1",
	})
	if !strings.Contains(human, "## Background Issue Delegation") {
		t.Error("an ordinary chat run must keep the delegation policy")
	}
	// The workflow used to tell the run to check code out while the delegation
	// policy, in the same brief, sent code and repository work to the background.
	if !strings.Contains(human, "work that CHANGES code or a repository is background work") {
		t.Error("chat workflow must reconcile repo checkout with the delegation boundary")
	}
}

// The issue-bearing kinds keep the full list; this is the other half of the
// scoping decision above.
func TestIssueKindsKeepFullAvailableCommands(t *testing.T) {
	t.Parallel()

	for name, ctx := range map[string]TaskContextForEnv{
		"comment":   {IssueID: "i-1", TriggerCommentID: "tc-1"},
		"assign":    {IssueID: "i-1"},
		"autopilot": {AutopilotRunID: "r-1"},
	} {
		out := buildMetaSkillContent("claude", ctx)
		for _, want := range []string{
			"multica issue comment list <issue-id>",
			"multica issue metadata list <issue-id>",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("%s brief lost full Available Commands entry %q", name, want)
			}
		}
		if strings.Contains(out, "## Background Issue Delegation") {
			t.Errorf("%s brief must not carry the chat-only delegation policy", name)
		}
	}
}

// TestBriefOwnsAutopilotIssueCommandsGuard pins the guard's single emission
// point: the autopilot brief carries AutopilotIssueCommandsGuard, and the
// per-turn prompt defers to it (daemon.TestBuildPromptAutopilotRunOnly pins
// the deferral side). MUL-5696.
func TestBriefOwnsAutopilotIssueCommandsGuard(t *testing.T) {
	out := buildMetaSkillContent("claude", TaskContextForEnv{AutopilotRunID: "run-1"})
	if !strings.Contains(out, AutopilotIssueCommandsGuard) {
		t.Errorf("autopilot brief missing AutopilotIssueCommandsGuard — the per-turn prompt defers to this single emission point")
	}
}

// TestSlimQuickCreateAvailableCommands locks the minimal-variant content
// for quick-create's Available Commands: `issue create` present, every
// other Core command absent (the hard guardrails forbid the call).
func TestSlimQuickCreateAvailableCommands(t *testing.T) {

	out := buildMetaSkillContent("codex", TaskContextForEnv{
		QuickCreatePrompt: "create an issue about flaky tests",
		AgentName:         "Eve", AgentID: "eve-1",
	})

	for _, want := range []string{
		"## Available Commands",
		"multica issue create --title",
		"`multica --help`",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("quick_create slim Available Commands missing %q", want)
		}
	}

	for _, banned := range []string{
		"multica issue get <id>",
		"multica issue comment list <issue-id>",
		"multica issue update <id>",
		"multica issue status <id> <status>",
		"multica issue comment add <issue-id>",
		"multica issue metadata list <issue-id>",
		"multica issue metadata set <issue-id>",
		"multica issue metadata delete <issue-id>",
		"multica issue children <id>",
		"multica repo checkout <url>",
		"### Squad maintenance",
		"multica squad member set-role",
	} {
		if strings.Contains(out, banned) {
			t.Errorf("quick_create slim Available Commands should NOT advertise %q (hard guardrails forbid the call)", banned)
		}
	}
}

// TestBackgroundTaskSafetySlimHardPins asserts the slim brief carries the
// same hardened Background Task Safety pins as the legacy brief (MUL-4140).
// The verbose path is covered by
// TestInjectRuntimeConfigBackgroundTaskSafetyProviderAgnostic; this locks
// the compressed slim path so a future slim-brief trim can't quietly drop
// the no-background-and-yield / no-"standing by" guardrails that address
// the MUL-4091 mechanism.
func TestBackgroundTaskSafetySlimHardPins(t *testing.T) {

	out := buildMetaSkillContent("claude", TaskContextForEnv{
		IssueID: "i-1", TriggerCommentID: "tc-1",
		AgentName: "Eve", AgentID: "eve-1",
	})

	for _, want := range []string{
		"## Background Task Safety",
		// MUL-5442 judgment rewrite (owner-authorized pin renegotiation): the
		// section now states the one platform fact, the external-systems/CI
		// boundary with its single exception, and the review-locked
		// persistent-service contract. Enforcement-detail pins that only
		// restated derivations of the platform fact were retired with the
		// prose. What stays pinned: the fact, each boundary, each exception,
		// and the handoff triple — the things an agent cannot infer.
		"any run-owned work still active is orphaned",
		"no background-completion wakeup",
		"whatever a tool response promises",
		"Never background-and-yield",
		"foreground tool calls that block",
		"run unobservable work synchronously",
		"standing by",
		"are not run-owned: do not wait",
		// The full compound ban, not its first item — MUL-5223 made this a
		// non-derivable boundary, so no member may be silently dropped.
		"do not run `gh pr checks --watch`, `gh run watch`, or sleep/retry polls",
		"GitHub Actions after a successful push",
		"NOT your delivery acceptance criteria",
		"CI running: <PR link>",
		"The one exception",
		"ONE foreground blocking call (`gh pr checks <pr> --watch`)",
		"persistent service handoff",
		"running service itself is the requested deliverable",
		"durable logs",
		"cleanup handle such as PID/profile",
		"verify readiness",
		"URL, logs, and stop instructions",
		"survival as best-effort, not guaranteed",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("slim Background Task Safety missing hardened pin %q\n---\n%s", want, out)
		}
	}
	// Exactly one exception (see the execenv provider-agnostic test for
	// the incident this guards).
	if got := strings.Count(out, "The one exception"); got != 1 {
		t.Errorf("slim brief must state the CI exception exactly once, got %d\n---\n%s", got, out)
	}
	// `gh run watch` may only appear as a banned command, never as the
	// section's example of how to wait properly.
	if strings.Contains(out, "e.g. `gh run watch`") {
		t.Errorf("slim Background Task Safety should not suggest waiting for external GitHub CI\n---\n%s", out)
	}
	// MUL-5274 review: with the persistent-service exception in the list, a
	// "The rules above ..." scoping sentence would sweep in work that is
	// precisely no longer run-owned after handoff.
	if strings.Contains(out, "The rules above") {
		t.Errorf("slim Background Task Safety must not reintroduce the ambiguous \"The rules above\" scoping sentence\n---\n%s", out)
	}
}
