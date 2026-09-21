package inboundcoord

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

//go:embed policy/*.md policy/registry.json
var policyAssets embed.FS

type policyModule struct {
	ID               string   `json:"id"`
	Version          string   `json:"version"`
	File             string   `json:"file"`
	AppliesWhen      string   `json:"applies_when"`
	Requires         []string `json:"requires"`
	OwnsRuleIDs      []string `json:"owns_rule_ids"`
	BudgetCharacters int      `json:"budget_characters"`
	ContentHash      string   `json:"content_sha256"`
}

type policyRegistry struct {
	Version            string              `json:"policy_version"`
	AssemblyVersion    string              `json:"assembly_version"`
	Modules            []policyModule      `json:"modules"`
	EffectDependencies map[string][]string `json:"effect_dependencies"`
}

// PolicyModuleManifest identifies the exact rule fragment visible to a model.
type PolicyModuleManifest struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	Hash    string `json:"hash"`
}

// PolicyManifest describes the actual system prompt, not a planned policy set.
// It is structural provenance; it does not assert behavioral verification.
type PolicyManifest struct {
	PolicyVersion   string                 `json:"policy_version"`
	AssemblyVersion string                 `json:"assembly_version"`
	PromptHash      string                 `json:"prompt_hash"`
	Modules         []PolicyModuleManifest `json:"modules"`
	ActiveRuleIDs   []string               `json:"active_rule_ids"`
	Characters      int                    `json:"characters"`
}

var coordinatorPolicy = mustLoadPolicyRegistry()

func mustLoadPolicyRegistry() policyRegistry {
	body, err := policyAssets.ReadFile("policy/registry.json")
	if err != nil {
		panic(fmt.Sprintf("read coordinator policy registry: %v", err))
	}
	var registry policyRegistry
	if err := json.Unmarshal(body, &registry); err != nil {
		panic(fmt.Sprintf("decode coordinator policy registry: %v", err))
	}
	return registry
}

func policyModuleBody(module policyModule) string {
	body, err := policyAssets.ReadFile("policy/" + module.File)
	if err != nil {
		panic(fmt.Sprintf("read coordinator policy module %s: %v", module.ID, err))
	}
	return strings.TrimSpace(string(body))
}

func buildSystemPrompt(turn Turn) string {
	return buildSystemPromptForStage(turn, false)
}

// buildSystemPromptForStage replaces the system message when a successful
// recall unlocks issue tools. It never appends duplicate policy fragments.
func buildSystemPromptForStage(turn Turn, recalled bool) string {
	var parts []string
	for _, module := range selectedPolicyModules(turn, recalled) {
		parts = append(parts, "[policy:"+module.ID+"@"+module.Version+"]\n"+policyModuleBody(module))
	}
	return strings.Join(parts, "\n\n")
}

func policyManifest(turn Turn) PolicyManifest {
	return policyManifestForStage(turn, false)
}

func policyManifestForStage(turn Turn, recalled bool) PolicyManifest {
	prompt := buildSystemPromptForStage(turn, recalled)
	manifest := PolicyManifest{
		PolicyVersion: coordinatorPolicy.Version, AssemblyVersion: coordinatorPolicy.AssemblyVersion,
		PromptHash: policyHash(prompt), Characters: utf8.RuneCountInString(prompt),
		Modules: []PolicyModuleManifest{}, ActiveRuleIDs: []string{},
	}
	seen := map[string]bool{}
	for _, module := range selectedPolicyModules(turn, recalled) {
		manifest.Modules = append(manifest.Modules, PolicyModuleManifest{
			ID: module.ID, Version: module.Version, Hash: policyHash(policyModuleBody(module)),
		})
		for _, id := range module.OwnsRuleIDs {
			if !seen[id] {
				manifest.ActiveRuleIDs = append(manifest.ActiveRuleIDs, id)
				seen[id] = true
			}
		}
	}
	return manifest
}

func policyHash(text string) string {
	hash := sha256.Sum256([]byte(text))
	return hex.EncodeToString(hash[:])
}

func selectedPolicyModules(turn Turn, recalled bool) []policyModule {
	finished := turn.Loop == LoopTaskFinished
	selected := map[string]bool{"core": true}
	if turn.Loop == LoopConversationReply {
		selected = map[string]bool{"conversation_reply": true}
	} else if turn.Loop == LoopFinishCheck {
		// Final review shares the same group participation boundary as routing.
		if turn.Source != SourceWeb && strings.EqualFold(turn.ChatType, "group") {
			selected["channel"], selected["group"] = true, true
		}
		if turn.FinishCheckAction == ActionIssue || turn.FinishCheckMixedActions {
			selected["finish_check_work"] = true
		}
		if turn.FinishCheckAction != ActionIssue || turn.FinishCheckMixedActions {
			selected["finish_check"] = true
		}
	} else if finished {
		selected["voice"] = true
		selected["completion"] = true
	} else {
		selected["voice"] = true
		selected["inbound"] = true
		if turn.Source == SourceWeb {
			selected["web"] = true
		} else {
			selected["channel"] = true
			selected["group"] = strings.EqualFold(turn.ChatType, "group")
		}
		selected["window"] = len(windowUtterances(turn)) > 1
		selected["memory"] = turn.SceneMemoryRevision > 0 || strings.TrimSpace(turn.SceneMemory) != ""
		selected["skills"] = strings.TrimSpace(formatSkillSnapshots(turn.Skills)) != ""
		selected["dialogue"] = len(turn.History) > 0 || len(turn.DingTalkHistory) > 0
		selected["recall_match"] = recalled
	}
	if turn.UserDecisionSubmission != nil {
		selected["user_decision"] = true
	}
	modules := make([]policyModule, 0, len(selected))
	for _, module := range coordinatorPolicy.Modules {
		if selected[module.ID] {
			modules = append(modules, module)
		}
	}
	return modules
}
