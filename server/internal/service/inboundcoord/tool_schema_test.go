package inboundcoord

import (
	"encoding/json"
	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/shared"
	"reflect"
	"strings"
	"testing"
)

func TestCoordinatorWireSchemaPreservesRequiredChoiceAndBranchConstraints(t *testing.T) {
	schema := shared.FunctionParameters{"type": "object", "properties": map[string]any{"actions": map[string]any{"type": "array", "items": map[string]any{
		"type": "object", "additionalProperties": false, "required": []string{"kind"},
		"properties": map[string]any{"kind": map[string]any{"type": "string", "enum": []string{"acknowledge", "ignore"}}, "source_refs": map[string]any{"type": "array", "minItems": 1, "description": "References must be unique.", "items": map[string]any{"type": "string", "enum": []string{"u1"}}}, "reply": map[string]any{"type": "string"}, "reason": map[string]any{"type": "string"}},
		"oneOf":      []any{map[string]any{"properties": map[string]any{"kind": map[string]any{"enum": []string{"acknowledge"}}}, "required": []string{"source_refs", "reply"}}, map[string]any{"properties": map[string]any{"kind": map[string]any{"enum": []string{"ignore"}}}, "required": []string{"source_refs", "reason"}}},
	}}}}
	params := openai.ChatCompletionNewParams{Tools: []openai.ChatCompletionToolUnionParam{openai.ChatCompletionFunctionTool(shared.FunctionDefinitionParam{Name: "finish", Parameters: schema})}}
	params.SetExtraFields(map[string]any{"tool_choice": "required", "enable_thinking": false})
	before, _ := json.Marshal(params)
	got, err := coordinatorWireParams(params)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(params)
	var beforeValue, afterValue any
	_ = json.Unmarshal(before, &beforeValue)
	_ = json.Unmarshal(after, &afterValue)
	if !reflect.DeepEqual(beforeValue, afterValue) {
		t.Fatal("mutated shared input")
	}
	raw, _ := json.Marshal(got)
	var wire map[string]any
	_ = json.Unmarshal(raw, &wire)
	if wire["tool_choice"] != "required" || wire["enable_thinking"] != false {
		t.Fatal("changed model call policy")
	}
	if strings.Contains(string(raw), `"uniqueItems"`) {
		t.Fatal("unsupported keyword on wire")
	}
	p := got.Tools[0].OfFunction.Function.Parameters
	item := p["properties"].(map[string]any)["actions"].(map[string]any)["items"].(map[string]any)
	for i, v := range item["oneOf"].([]any) {
		branch := v.(map[string]any)
		required := branch["required"].([]any)
		if len(required) != 3 || required[0] != "kind" || required[1] != "source_refs" {
			t.Fatalf("lost required fields: %v", required)
		}
		if branch["additionalProperties"] != false || branch["type"] != "object" {
			t.Fatal("lost closed object")
		}
		props := branch["properties"].(map[string]any)
		kind := props["kind"].(map[string]any)
		want := []string{"acknowledge", "ignore"}[i]
		if kind["enum"].([]any)[0] != want || kind["type"] != "string" {
			t.Fatal("lost branch discriminator")
		}
		refs := props["source_refs"].(map[string]any)
		if refs["minItems"] != float64(1) || !strings.Contains(refs["description"].(string), "unique") {
			t.Fatal("lost reference requirements")
		}
	}
}
func TestToolSchemaPreservesPropertyNames(t *testing.T) {
	schema := map[string]any{"type": "object", "properties": map[string]any{"uniqueItems": map[string]any{"type": "string"}}}
	expandToolSchemaBranches(schema)
	if schema["properties"].(map[string]any)["uniqueItems"] == nil {
		t.Fatal("removed a property name")
	}
}
