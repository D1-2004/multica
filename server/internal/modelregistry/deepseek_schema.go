package modelregistry

import (
	"encoding/json"
	"fmt"
	"slices"

	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/shared"
)

// deepSeekCoordinatorParams adapts the Coordinator's tool grammar for Bailian
// DeepSeek V4.1. The service rejects the uniqueItems keyword (even false) and
// loses inherited required fields when oneOf branches only specify deltas.
// Host validation remains authoritative for duplicate source/state references.
// Keep required tool choice, branch discriminators and all other constraints.
func deepSeekCoordinatorParams(params openai.ChatCompletionNewParams) (openai.ChatCompletionNewParams, error) {
	params.Tools = append([]openai.ChatCompletionToolUnionParam(nil), params.Tools...)
	for i, tool := range params.Tools {
		if tool.OfFunction == nil {
			continue
		}
		functionTool := *tool.OfFunction
		raw, err := json.Marshal(functionTool.Function.Parameters)
		if err != nil {
			return params, fmt.Errorf("encode Coordinator tool schema: %w", err)
		}
		var schema map[string]any
		if err = json.Unmarshal(raw, &schema); err != nil {
			return params, fmt.Errorf("decode Coordinator tool schema: %w", err)
		}
		normalizeDeepSeekSchema(schema)
		functionTool.Function.Parameters = shared.FunctionParameters(schema)
		params.Tools[i].OfFunction = &functionTool
	}
	return params, nil
}

func normalizeDeepSeekSchema(value any) {
	switch schema := value.(type) {
	case []any:
		for _, v := range schema {
			normalizeDeepSeekSchema(v)
		}
	case map[string]any:
		if _, exists := schema["uniqueItems"]; exists {
			delete(schema, "uniqueItems")
			description, _ := schema["description"].(string)
			schema["description"] = description + " Entries must be unique; Host rejects duplicate references."
		}
		// Only expand the Coordinator's object-branch pattern. Copying each parent
		// constraint into every mutually exclusive branch preserves the contract.
		branches, hasBranches := schema["oneOf"].([]any)
		properties, hasProperties := schema["properties"].(map[string]any)
		canExpand := hasBranches && hasProperties && schema["type"] == "object"
		for _, v := range branches {
			branch, ok := v.(map[string]any)
			if !ok {
				canExpand = false
				break
			}
			for key := range branch {
				if key != "properties" && key != "required" {
					canExpand = false
				}
			}
		}
		if canExpand {
			expanded := make([]any, 0, len(branches))
			for _, v := range branches {
				branch := v.(map[string]any)
				merged := map[string]any{}
				for key, value := range schema {
					if key != "oneOf" {
						merged[key] = value
					}
				}
				props := map[string]any{}
				for key, value := range properties {
					props[key] = value
				}
				overrides, _ := branch["properties"].(map[string]any)
				for key, value := range overrides {
					base, _ := props[key].(map[string]any)
					overlay, _ := value.(map[string]any)
					if base != nil && overlay != nil {
						combined := map[string]any{}
						for k, v := range base {
							combined[k] = v
						}
						for k, v := range overlay {
							combined[k] = v
						}
						props[key] = combined
					} else {
						props[key] = value
					}
				}
				merged["properties"] = props
				required := []any{}
				for _, source := range []map[string]any{schema, branch} {
					names, _ := source["required"].([]any)
					for _, name := range names {
						if !slices.Contains(required, name) {
							required = append(required, name)
						}
					}
				}
				merged["required"] = required
				// Deep copy prevents sibling branches sharing rewritten property maps.
				independent := cloneSchemaValue(merged).(map[string]any)
				normalizeDeepSeekSchema(independent)
				expanded = append(expanded, independent)
			}
			for key := range schema {
				delete(schema, key)
			}
			schema["type"] = "object"
			schema["oneOf"] = expanded
			return
		}
		for key, value := range schema {
			// Property names are data, even if one is named uniqueItems or oneOf.
			if key == "properties" {
				if properties, ok := value.(map[string]any); ok {
					for _, property := range properties {
						normalizeDeepSeekSchema(property)
					}
				}
			} else if key == "items" || key == "oneOf" || key == "anyOf" || key == "allOf" {
				normalizeDeepSeekSchema(value)
			}
		}
	}
}

func cloneSchemaValue(value any) any {
	switch v := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(v))
		for key, entry := range v {
			result[key] = cloneSchemaValue(entry)
		}
		return result
	case []any:
		result := make([]any, len(v))
		for i, entry := range v {
			result[i] = cloneSchemaValue(entry)
		}
		return result
	default:
		return value
	}
}
