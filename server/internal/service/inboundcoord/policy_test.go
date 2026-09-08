package inboundcoord

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func policyModuleIDs(turn Turn, recalled bool) map[string]bool {
	ids := map[string]bool{}
	for _, module := range selectedPolicyModules(turn, recalled) {
		ids[module.ID] = true
	}
	return ids
}

func TestPolicySelectsIndependentEntryPoints(t *testing.T) {
	turn := Turn{
		Source: SourceDigitalEmployee, ChatType: "group", Message: "current request",
		Utterances:          []WindowUtterance{{Sender: "A", Text: "first"}, {Sender: "B", Text: "second"}},
		Skills:              []SkillSnapshot{{Name: "Meetings", Description: "Summarize meetings"}},
		SceneMemoryRevision: 1, SceneMemory: "Stable knowledge",
		DingTalkHistory: []HistoryLine{{Role: "assistant", Content: "Earlier question"}},
	}
	initial := policyModuleIDs(turn, false)
	for _, id := range []string{"core", "voice", "inbound", "channel", "group", "window", "skills", "memory", "dialogue"} {
		if !initial[id] {
			t.Errorf("initial policy missing applicable module %s", id)
		}
	}
	if initial["recall_match"] || initial["completion"] || initial["web"] {
		t.Fatalf("unneeded initial policy: %v", initial)
	}
	if !policyModuleIDs(turn, true)["recall_match"] {
		t.Fatal("successful recall must disclose matching rules")
	}
	turn.Loop = LoopTaskFinished
	finished := policyModuleIDs(turn, true)
	if len(finished) != 3 || !finished["core"] || !finished["voice"] || !finished["completion"] {
		t.Fatalf("task-finished policy leaked inbound modules: %v", finished)
	}
}

func TestPolicySelectionDependenciesCloseAcrossConditions(t *testing.T) {
	for _, source := range []Source{SourceWeb, SourceRobot, SourceDigitalEmployee} {
		for _, loop := range []Loop{LoopInbound, LoopTaskFinished} {
			for _, chatType := range []string{"p2p", "group"} {
				for conditions := 0; conditions < 32; conditions++ {
					turn := Turn{Source: source, Loop: loop, ChatType: chatType, Message: "message"}
					if conditions&1 != 0 {
						turn.Skills = []SkillSnapshot{{Name: "Skill"}}
					}
					if conditions&2 != 0 {
						turn.SceneMemoryRevision = 1
					}
					if conditions&4 != 0 {
						turn.DingTalkHistory = []HistoryLine{{Content: "question"}}
					}
					if conditions&8 != 0 {
						turn.Utterances = []WindowUtterance{{Text: "first"}, {Text: "second"}}
					}
					recalled := conditions&16 != 0
					ids := policyModuleIDs(turn, recalled)
					for _, module := range selectedPolicyModules(turn, recalled) {
						for _, dependency := range module.Requires {
							if !ids[dependency] {
								t.Fatalf("%s/%s/%s/%d: module %s missing dependency %s", source, loop, chatType, conditions, module.ID, dependency)
							}
						}
					}
					if loop == LoopInbound && recalled {
						for _, dependency := range coordinatorPolicy.EffectDependencies["work_submission"] {
							if !ids[dependency] {
								t.Fatalf("work effect prerequisite %s not disclosed", dependency)
							}
						}
					}
				}
			}
		}
	}
}

func TestPolicyManifestMatchesActualPromptAndModuleBudgets(t *testing.T) {
	turn := Turn{Source: SourceDigitalEmployee, Message: "capabilities", Skills: []SkillSnapshot{{Name: "Meeting"}}, SceneMemoryRevision: 1}
	prompt := buildSystemPrompt(turn)
	manifest := policyManifest(turn)
	if manifest.PromptHash != policyHash(prompt) || manifest.Characters != utf8.RuneCountInString(prompt) {
		t.Fatal("manifest does not describe the assembled prompt")
	}
	if manifest.PolicyVersion == "" || manifest.AssemblyVersion == "" || len(manifest.ActiveRuleIDs) == 0 {
		t.Fatalf("manifest lacks provenance: %+v", manifest)
	}
	if manifest.Characters > 9500 {
		t.Fatalf("direct-answer system prompt regressed to %d characters", manifest.Characters)
	}
	if !strings.Contains(prompt, "must assoc_recall this scene then finish action=issue") {
		t.Fatal("inbound policy lost the default dispatch-and-associate rule")
	}
	for _, module := range coordinatorPolicy.Modules {
		if policyHash(policyModuleBody(module)) != module.ContentHash {
			t.Errorf("module %s content changed without updating registered provenance", module.ID)
		}
		if n := utf8.RuneCountInString(policyModuleBody(module)); n > module.BudgetCharacters {
			t.Errorf("module %s exceeds registered budget: %d > %d", module.ID, n, module.BudgetCharacters)
		}
	}
	changed := policyManifestForStage(turn, true)
	if changed.PromptHash == manifest.PromptHash {
		t.Fatal("stage transition must identify a new actual prompt")
	}
	if _, err := json.Marshal(changed); err != nil {
		t.Fatal(err)
	}
	finished := policyManifest(Turn{Loop: LoopTaskFinished})
	if finished.Characters > 4000 {
		t.Fatalf("completion policy carries excessive context: %d", finished.Characters)
	}
}

func TestPolicyWindowProjectionPreservesEachSourceOnce(t *testing.T) {
	stamp := time.Date(2026, 9, 7, 11, 14, 1, 0, time.UTC)
	turn := Turn{
		Source: SourceDigitalEmployee, Message: "aggregate text must not be duplicated",
		HistoryBefore: stamp,
		Utterances: []WindowUtterance{
			{Sender: "A", Text: "改到三点，先不要发。", EvidenceID: "message-a", SenderID: "sender-a", Timestamp: stamp, ReplyToEvidenceID: "question-a"},
			{Sender: "B", Text: "再帮我查下一班车。"},
		},
	}
	prompt := buildUserPrompt(turn)
	for _, text := range []string{"改到三点，先不要发。", "再帮我查下一班车。"} {
		if strings.Count(prompt, text) != 1 {
			t.Fatalf("source must be preserved exactly once: %q", text)
		}
	}
	if strings.Contains(prompt, turn.Message) {
		t.Fatal("aggregate message duplicated the authoritative window")
	}
	for _, evidence := range []string{"source_ref=u1", "source_ref=u2", `evidence_id="message-a"`, `reply_to="question-a"`, "history_watermark:"} {
		if !strings.Contains(prompt, evidence) {
			t.Errorf("missing provenance %s", evidence)
		}
	}
	if strings.Contains(prompt, `evidence_id="u2"`) {
		t.Fatal("window-local reference must not pretend to be a platform ID")
	}
}

func TestPolicyContextStatesDoNotTurnMissingIntoEmpty(t *testing.T) {
	missing := buildUserPrompt(Turn{Source: SourceDigitalEmployee, Message: "有哪些记忆", HistoryStatus: "unavailable", HistoryError: "sensitive underlying error"})
	for _, want := range []string{"skills_status: not_loaded", "scene_memory_status: not_loaded", "history_status: unavailable"} {
		if !strings.Contains(missing, want) {
			t.Errorf("missing context status %s", want)
		}
	}
	if strings.Contains(missing, "sensitive underlying error") {
		t.Fatal("raw read error leaked into model context")
	}
	empty := buildUserPrompt(Turn{Source: SourceDigitalEmployee, Message: "有哪些记忆", SceneMemoryRevision: 2, SceneMemoryStatus: "empty", SkillsStatus: "empty", HistoryStatus: "empty"})
	for _, want := range []string{"skills_status: empty", "scene_memory_status: empty", "history_status: empty", "(empty)"} {
		if !strings.Contains(empty, want) {
			t.Errorf("known empty snapshot missing %s", want)
		}
	}
}

func TestPolicyCompletionProjectionOmitsUnrelatedFacts(t *testing.T) {
	turn := Turn{
		Loop: LoopTaskFinished, Source: SourceDigitalEmployee, IssueID: "issue-current", TaskResult: "Current answer",
		TaskDeliveryContext: "Current task delivery unknown",
		Skills:              []SkillSnapshot{{Name: "SECRET_SKILL"}}, SceneMemory: "UNRELATED_MEMORY",
		DingTalkHistory: []HistoryLine{{Content: "UNRELATED_HISTORY"}}, Message: "UNRELATED_INBOUND",
	}
	prompt := buildUserPrompt(turn)
	for _, notNeeded := range []string{"SECRET_SKILL", "UNRELATED_MEMORY", "UNRELATED_HISTORY", "UNRELATED_INBOUND"} {
		if strings.Contains(prompt, notNeeded) {
			t.Fatalf("completion context leaked %s", notNeeded)
		}
	}
	if !strings.Contains(prompt, turn.TaskResult) || !strings.Contains(prompt, turn.TaskDeliveryContext) || !strings.Contains(prompt, turn.IssueID) {
		t.Fatal("completion omitted its actual work, result or delivery context")
	}
}

func TestPolicyKeepsWorkingConstraintAfterLongInstructions(t *testing.T) {
	constraint := "Only draft the message; do not send it until I approve."
	instructions := strings.Repeat("Background context. ", 80) + constraint
	prompt := buildUserPrompt(Turn{Source: SourceDigitalEmployee, Message: "Draft a message", Instructions: instructions})
	if !strings.Contains(prompt, instructions) || !strings.Contains(prompt, constraint) {
		t.Fatal("working-policy projection dropped a trailing execution restriction")
	}
	if !strings.Contains(prompt, "job_policy_status: loaded; truncated=false") {
		t.Fatal("full working-policy state must be explicit")
	}
}
