package employeeloop

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPromptDecisionRulesFrontLoadAndLegacyOmission(t *testing.T) {
	persona := testConfig().Persona
	legacy := BuildPrompt(persona)
	raw, err := json.Marshal(persona)
	if err != nil || strings.Contains(string(raw), "DecisionRules") {
		t.Fatal(string(raw), err)
	}
	persona.DecisionRules = "RULE ZERO sentinel"
	prompt := BuildPrompt(persona)
	if strings.Index(prompt, persona.DecisionRules) > strings.Index(prompt, "Core personality") || !strings.Contains(prompt, persona.DecisionRules) {
		t.Fatal("decision comes after lower-priority voice")
	}
	if strings.Replace(prompt, persona.DecisionRules+"\n\n", "", 1) != legacy {
		t.Fatal("optional rule changed legacy prompt bytes")
	}
}
