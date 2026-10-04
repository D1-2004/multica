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
	if spec.EmployeeCompact && (kind == KindConfirm || kind == KindChoose) {
		return projectEmployeeAsk(publicID, surfaceID, question, spec)
	}
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

// projectEmployeeAsk keeps frozen choices in action literals rather than a
// client-editable answer model. Multiple choice still has an explicit finish.
func projectEmployeeAsk(publicID, surfaceID, question string, spec storedRequest) ([]string, error) {
	children := []string{"question"}
	components := []any{titleText("question", question)}
	model := map[string]any{"clarification": map[string]any{
		"sourceTurnId": publicID, "sourceProjectionVersion": Version,
	}}
	if !spec.Multiple && !spec.AllowCustom {
		for _, option := range spec.Options {
			labelID, buttonID := "label-"+option.ID, "choice-"+option.ID
			children = append(children, buttonID)
			components = append(components,
				basicComponent(labelID, "Text", map[string]any{"text": compactOptionLabel(option)}),
				basicComponent(buttonID, "Button", map[string]any{
					"child": labelID, "variant": optionVariant(option.Emphasis), "action": directChoiceAction(publicID, option.ID),
				}),
			)
		}
	} else {
		pickerOptions := make([]any, 0, len(spec.Options))
		for _, option := range spec.Options {
			pickerOptions = append(pickerOptions, map[string]any{"label": compactOptionLabel(option), "value": option.ID})
		}
		clarification := model["clarification"].(map[string]any)
		clarification["answers"] = map[string]any{"q0": map[string]any{"selected": []string{}, "custom": ""}}
		children = append(children, "choices")
		pickerVariant := "mutuallyExclusive"
		if spec.Multiple {
			pickerVariant = "multipleSelection"
		}
		components = append(components,
			basicComponent("choices", "ChoicePicker", map[string]any{
				"options": pickerOptions, "value": pathOf("/clarification/answers/q0/selected"),
				"variant": pickerVariant, "displayStyle": "checkbox",
			}),
		)
		if spec.AllowCustom {
			children = append(children, "extra")
			components = append(components, basicComponent("extra", "TextField", map[string]any{
				"label": "补充一句（可以不写）", "value": pathOf("/clarification/answers/q0/custom"),
			}))
		}
		children = append(children, "actions")
		components = append(components,
			basicComponent("actions", "Row", map[string]any{"children": []string{"submit"}, "align": "center", "justify": "end"}),
			basicComponent("submitLabel", "Text", map[string]any{"text": "确认"}),
			basicComponent("submit", "Button", map[string]any{
				"child": "submitLabel", "variant": "primary", "action": map[string]any{"event": map[string]any{
					"name": submitEvent, "context": map[string]any{
						"outcome": "answered", "sourceTurnId": publicID, "sourceProjectionVersion": Version,
						"answers": pathOf("/clarification/answers"),
					},
				}}, "checks": submitChecks(spec.AllowCustom),
			}),
		)
	}
	components = append([]any{basicComponent("root", "Column", map[string]any{
		"children": children, "align": "stretch", "justify": "start", "gap": 8,
	})}, components...)
	return encodeMessages(
		map[string]any{"version": "v1.0", "createSurface": map[string]any{
			"surfaceId": surfaceID, "catalogId": catalogPublic, "dataModel": model,
		}},
		map[string]any{"version": "v1.0", "updateComponents": map[string]any{"surfaceId": surfaceID, "components": components}},
	)
}

// optionVariant uses only variants verified by the native basic catalog.
func optionVariant(emphasis string) string {
	switch emphasis {
	case "primary":
		return "primary"
	case "secondary":
		return "default"
	default:
		return "borderless"
	}
}

func compactDescription(description string) string {
	return clip(strings.Join(strings.Fields(description), " "), 80)
}

func compactOptionLabel(option storedOption) string {
	if option.Description == "" {
		return option.Label
	}
	return option.Label + " · " + compactDescription(option.Description)
}

func directChoiceAction(publicID, optionID string) map[string]any {
	return map[string]any{"event": map[string]any{
		"name": submitEvent,
		"context": map[string]any{
			"outcome": "answered", "sourceTurnId": publicID,
			"sourceProjectionVersion": Version,
			"answers":                 map[string]any{"q0": map[string]any{"selected": []string{optionID}, "custom": ""}},
		},
	}}
}

// projectResolvedAsk replaces the surface tree with text only. The previous
// buttons remain unreachable from root even on renderers that retain old ids.
func projectResolvedAsk(surfaceID, question string, labels []string, custom, outcome string) ([]string, error) {
	children := []string{"resolved-question"}
	components := []any{titleText("resolved-question", question)}
	for i, label := range labels {
		id := "resolved-" + strconv.Itoa(i)
		children = append(children, id)
		components = append(components,
			bodyText(id+"-label", label),
			publicComponent(id+"-check", "Text", map[string]any{
				"text": "✓", "variant": "body", "colorToken": colorPrimary, "maxLine": 1,
			}),
			basicComponent(id, "Row", map[string]any{
				"children": []string{id + "-label", id + "-check"}, "justify": "spaceBetween", "align": "center", "gap": 8,
			}),
		)
	}
	if custom != "" {
		children = append(children, "resolved-custom")
		components = append(components, captionText("resolved-custom", clip(custom, 160)))
	}
	if outcome == "disabled" {
		children = append(children, "resolved-disabled")
		components = append(components, captionText("resolved-disabled", "已失效"))
	}
	if outcome == string(StatusSkipped) {
		children = append(children, "resolved-skipped")
		components = append(components, captionText("resolved-skipped", "先不选"))
	}
	components = append([]any{basicComponent("root", "Column", map[string]any{
		"children": children, "align": "stretch", "justify": "start", "gap": 8,
	})}, components...)
	return encodeMessages(map[string]any{"version": "v1.0", "updateComponents": map[string]any{
		"surfaceId": surfaceID, "components": components,
	}})
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
