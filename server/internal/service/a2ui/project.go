package a2ui

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

const (
	catalogBasic   = "https://a2ui.org/specification/v1_0/catalogs/basic/catalog.json"
	catalogPublic  = "https://dingtalk.com/card/a2ui/catalogs/public/catalog.json"
	submitEvent    = "runtime.clarification.submit"
	colorPrimary   = "common_level1_base_color"
	colorSecondary = "common_level2_base_color"
)

func projectCard(publicID, surfaceID string, kind Kind, header, question string, spec storedRequest) ([]string, error) {
	switch kind {
	case KindChart:
		return projectChart(surfaceID, header, spec)
	case KindNote:
		return projectNote(surfaceID, spec)
	default:
		return projectAsk(publicID, surfaceID, kind, header, question, spec)
	}
}

func projectAsk(publicID, surfaceID string, kind Kind, header, question string, spec storedRequest) ([]string, error) {
	selection := "single"
	if spec.Multiple {
		selection = "multiple"
	}
	modelOptions := make([]any, 0, len(spec.Options))
	pickerOptions := make([]any, 0, len(spec.Options))
	for _, option := range spec.Options {
		item := map[string]any{"id": option.ID, "label": option.Label}
		if option.Description != "" {
			item["description"] = option.Description
		}
		modelOptions = append(modelOptions, item)
		pickerOptions = append(pickerOptions, map[string]any{"label": option.Label, "value": option.ID})
	}
	questionModel := map[string]any{
		"id": "q0", "prompt": question, "selection": selection,
		"allowCustom": spec.AllowCustom, "options": modelOptions,
	}
	if kind == KindPerson {
		questionModel["inputKind"] = "person"
	}
	model := map[string]any{
		"questionSummary": question,
		"clarification": map[string]any{
			"sourceTurnId": publicID, "sourceProjectionVersion": Version,
			"questions": []any{questionModel},
			"answers":   map[string]any{"q0": map[string]any{"selected": []string{}, "custom": ""}},
		},
	}
	children := make([]string, 0, 6)
	components := make([]any, 0, 8)
	if header != "" {
		children = append(children, "title")
		components = append(components, titleText("title", header))
	}
	if question != "" && question != header {
		children = append(children, "question")
		components = append(components, bodyText("question", question))
	}
	for _, option := range spec.Options {
		if option.Description == "" {
			continue
		}
		id := "hint-" + option.ID
		children = append(children, id)
		components = append(components, captionText(id, option.Label+" · "+option.Description))
	}
	if kind == KindPerson {
		children = append(children, "person")
		components = append(components, publicComponent("person", "UserPicker", map[string]any{
			"title": "选择同事", "placeholder": "搜一个名字", "maxSelections": 1,
			"value": pathOf("/clarification/answers/q0/selected"),
		}))
	} else {
		children = append(children, "choices")
		variant := "mutuallyExclusive"
		if spec.Multiple {
			variant = "multipleSelection"
		}
		components = append(components, basicComponent("choices", "ChoicePicker", map[string]any{
			"options": pickerOptions, "value": pathOf("/clarification/answers/q0/selected"),
			"variant": variant, "displayStyle": "checkbox",
		}))
	}
	if spec.AllowCustom {
		children = append(children, "extra")
		components = append(components, basicComponent("extra", "TextField", map[string]any{
			"label": "补充一句（可以不写）", "value": pathOf("/clarification/answers/q0/custom"),
		}))
	}
	children = append(children, "actions")
	components = append(components,
		basicComponent("actions", "Row", map[string]any{
			"children": []string{"skip", "submit"}, "align": "center", "justify": "end", "gap": 8,
		}),
		basicComponent("submitLabel", "Text", map[string]any{"text": submitLabel(kind)}),
		basicComponent("submit", "Button", map[string]any{
			"child": "submitLabel", "variant": "primary",
			"action": submitAction("answered"), "checks": submitChecks(spec.AllowCustom),
		}),
		captionText("skipLabel", "先不选"),
		basicComponent("skip", "Button", map[string]any{
			"child": "skipLabel", "variant": "borderless", "action": submitAction("skipped"),
		}),
	)
	root := basicComponent("root", "Column", map[string]any{
		"children": children, "align": "stretch", "justify": "start", "gap": 8,
	})
	return encodeMessages(
		map[string]any{"version": "v1.0", "createSurface": map[string]any{
			"surfaceId": surfaceID, "catalogId": catalogPublic, "dataModel": model,
		}},
		map[string]any{"version": "v1.0", "updateComponents": map[string]any{
			"surfaceId": surfaceID, "components": append([]any{root}, components...),
		}},
	)
}

func projectChart(surfaceID, title string, spec storedRequest) ([]string, error) {
	if spec.Chart == nil {
		return nil, fmt.Errorf("%w: chart", ErrInvalid)
	}
	chartType := map[string]string{"line": "lineChart", "bar": "histogram", "pie": "pieChart"}[spec.Chart.Type]
	points := make([]any, 0, len(spec.Chart.Points))
	fallback := make([]string, 0, len(spec.Chart.Points))
	for _, point := range spec.Chart.Points {
		points = append(points, map[string]any{"x": point.X, "y": point.Y})
		fallback = append(fallback, point.X+" "+strconv.FormatFloat(point.Y, 'f', -1, 64))
	}
	model := map[string]any{"charts": map[string]any{"main": map[string]any{
		"type": chartType, "data": points, "config": map[string]any{},
	}}}
	components := []any{
		basicComponent("root", "Column", map[string]any{
			"children": []string{"title", "chart"}, "align": "stretch", "justify": "start", "gap": 8,
		}),
		titleText("title", title),
		publicComponent("chart", "Chart", map[string]any{
			"data": pathOf("/charts/main"), "aspectRatio": "2:1", "enableDetail": false,
			"fallbackMarkdown": strings.Join(fallback, "，"),
		}),
	}
	return encodeMessages(
		map[string]any{"version": "v1.0", "createSurface": map[string]any{
			"surfaceId": surfaceID, "catalogId": catalogPublic, "dataModel": model,
		}},
		map[string]any{"version": "v1.0", "updateComponents": map[string]any{
			"surfaceId": surfaceID, "components": components,
		}},
	)
}

func projectNote(surfaceID string, spec storedRequest) ([]string, error) {
	return encodeMessages(
		map[string]any{"version": "v1.0", "createSurface": map[string]any{
			"surfaceId": surfaceID, "catalogId": catalogPublic,
			"dataModel": map[string]any{"summary": spec.Markdown},
		}},
		map[string]any{"version": "v1.0", "updateComponents": map[string]any{
			"surfaceId": surfaceID,
			"components": []any{
				basicComponent("root", "Column", map[string]any{"children": []string{"body"}}),
				publicComponent("body", "Markdown", map[string]any{"content": pathOf("/summary")}),
			},
		}},
	)
}

func submitLabel(kind Kind) string {
	switch kind {
	case KindChoose:
		return "就这些"
	case KindPerson:
		return "发给这个人"
	case KindApproval:
		return "提交"
	default:
		return "确认"
	}
}

func submitAction(outcome string) map[string]any {
	return map[string]any{"event": map[string]any{
		"name": submitEvent,
		"context": map[string]any{
			"outcome":                 outcome,
			"sourceTurnId":            pathOf("/clarification/sourceTurnId"),
			"sourceProjectionVersion": pathOf("/clarification/sourceProjectionVersion"),
			"questions":               pathOf("/clarification/questions"),
			"answers":                 pathOf("/clarification/answers"),
		},
	}}
}

func submitChecks(allowCustom bool) []any {
	selected := pathOf("/clarification/answers/q0/selected")
	if !allowCustom {
		return []any{map[string]any{"condition": requiredValue(selected), "message": "请选择一项"}}
	}
	return []any{map[string]any{
		"condition": map[string]any{"call": "or", "catalogId": catalogBasic, "args": map[string]any{"values": []any{
			requiredValue(selected), requiredValue(pathOf("/clarification/answers/q0/custom")),
		}}},
		"message": "请选择一项或填写补充说明",
	}}
}

func requiredValue(value any) map[string]any {
	return map[string]any{"call": "required", "catalogId": catalogBasic, "args": map[string]any{"value": value}}
}

func pathOf(p string) map[string]string { return map[string]string{"path": p} }

// titleText, bodyText and captionText use the public catalog. DingTalk paints
// colorToken; a hardcoded color is not sent with it. level1 is the primary
// copy, level2 is the secondary copy. Markdown has no color field.
func titleText(id, text string) map[string]any {
	return publicComponent(id, "Text", map[string]any{
		"text": text, "variant": "body", "bold": true,
		"colorToken": colorPrimary, "maxLine": 2,
	})
}

func bodyText(id, text string) map[string]any {
	return publicComponent(id, "Text", map[string]any{
		"text": text, "variant": "body", "weight": 1,
		"colorToken": colorPrimary, "maxLine": 8,
	})
}

func captionText(id, text string) map[string]any {
	return publicComponent(id, "Text", map[string]any{
		"text": text, "variant": "caption",
		"colorToken": colorSecondary, "maxLine": 2,
	})
}

func basicComponent(id, kind string, fields map[string]any) map[string]any {
	fields["id"] = id
	fields["component"] = kind
	fields["catalogId"] = catalogBasic
	return fields
}

func publicComponent(id, kind string, fields map[string]any) map[string]any {
	fields["id"] = id
	fields["component"] = kind
	fields["catalogId"] = catalogPublic
	return fields
}

func encodeMessages(messages ...any) ([]string, error) {
	out := make([]string, 0, len(messages))
	for _, message := range messages {
		body, err := json.Marshal(message)
		if err != nil {
			return nil, err
		}
		out = append(out, string(body))
	}
	return out, nil
}
