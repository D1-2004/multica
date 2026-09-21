package userdecision

import (
	"encoding/json"
	"errors"
	"strings"
)

type Option struct {
	ID    string          `json:"id"`
	Label string          `json:"label"`
	Kind  string          `json:"kind"`
	Plan  json.RawMessage `json:"plan"`
}
type Proposal struct {
	Question      string   `json:"question"`
	Options       []Option `json:"options"`
	RecommendedID string   `json:"recommended_id"`
	Model         string   `json:"model"`
	RawOutput     string   `json:"raw_output"`
}

// Card renders only the question, choices and optional free text. Candidate
// plans and model recommendations never enter the client payload.
func Card(id string, p Proposal) []string {
	const catalog = "https://a2ui.org/specification/v1_0/catalogs/basic/catalog.json"
	component := func(id, kind string, fields map[string]any) map[string]any {
		fields["id"] = id
		fields["component"] = kind
		fields["catalogId"] = catalog
		return fields
	}
	options := []map[string]any{}
	questions := []map[string]any{}
	for _, o := range p.Options {
		options = append(options, map[string]any{"label": o.Label, "value": o.ID})
		questions = append(questions, map[string]any{"id": o.ID, "label": o.Label})
	}
	model := map[string]any{"clarification": map[string]any{"sourceTurnId": id, "sourceProjectionVersion": Version, "questions": []any{map[string]any{"id": "q0", "prompt": p.Question, "selection": "single", "allowCustom": true, "options": questions}}, "answers": map[string]any{"q0": map[string]any{"selected": []string{}, "custom": ""}}}}
	path := func(s string) map[string]string { return map[string]string{"path": "/clarification/" + s} }
	action := map[string]any{"event": map[string]any{"name": "runtime.clarification.submit", "context": map[string]any{"outcome": "answered", "sourceTurnId": path("sourceTurnId"), "sourceProjectionVersion": path("sourceProjectionVersion"), "questions": path("questions"), "answers": path("answers")}}}
	required := func(value any) map[string]any {
		return map[string]any{"call": "required", "args": map[string]any{"value": value}}
	}
	checks := []any{map[string]any{"condition": map[string]any{"call": "or", "args": map[string]any{"values": []any{required(path("answers/q0/selected")), required(path("answers/q0/custom"))}}}, "message": "请选择一项或填写补充说明"}}
	components := []any{
		component("root", "Column", map[string]any{"children": []string{"question", "choices", "extra", "submit"}}),
		component("question", "Text", map[string]any{"text": p.Question}),
		component("choices", "ChoicePicker", map[string]any{"options": options, "value": path("answers/q0/selected"), "variant": "mutuallyExclusive", "displayStyle": "checkbox"}),
		component("extra", "TextField", map[string]any{"label": "补充说明（可选，也可以直接写你的想法）", "value": path("answers/q0/custom")}),
		component("submitLabel", "Text", map[string]any{"text": "提交选择"}),
		component("submit", "Button", map[string]any{"child": "submitLabel", "variant": "primary", "action": action, "checks": checks}),
	}
	return encodeMessages(map[string]any{"version": "v1.0", "createSurface": map[string]any{"surfaceId": id, "catalogId": "https://dingtalk.com/card/a2ui/catalogs/public/catalog.json", "dataModel": model}}, map[string]any{"version": "v1.0", "updateComponents": map[string]any{"surfaceId": id, "components": components}})
}
func StatusCard(id, text string) []string {
	const catalog = "https://a2ui.org/specification/v1_0/catalogs/basic/catalog.json"
	return encodeMessages(map[string]any{"version": "v1.0", "updateComponents": map[string]any{"surfaceId": id, "components": []any{map[string]any{"id": "root", "component": "Column", "catalogId": catalog, "children": []string{"status"}}, map[string]any{"id": "status", "component": "Text", "catalogId": catalog, "text": text}}}})
}
func encodeMessages(messages ...any) []string {
	out := make([]string, 0, len(messages))
	for _, m := range messages {
		b, _ := json.Marshal(m)
		out = append(out, string(b))
	}
	return out
}

// Validate checks the transport-independent choice contract. Actual task
// existence, authorization and plan semantics remain Coordinator Host checks.
func (p Proposal) Validate() error {
	if strings.TrimSpace(p.Question) == "" || len([]rune(p.Question)) > 500 || len(p.Options) < 2 || len(p.Options) > 5 {
		return errors.New("invalid decision question or option count")
	}
	seen := map[string]bool{}
	counts := map[string]int{}
	for _, o := range p.Options {
		if o.ID == "" || seen[o.ID] || strings.TrimSpace(o.Label) == "" || len([]rune(o.Label)) > 1500 || !json.Valid(o.Plan) {
			return errors.New("invalid decision option")
		}
		seen[o.ID] = true
		counts[o.Kind]++
		if o.Kind != "continue_work" && o.Kind != "start_work" && o.Kind != "reply" {
			return errors.New("unsupported decision option kind")
		}
	}
	if counts["continue_work"] > 3 || counts["start_work"] != 1 || counts["reply"] != 1 {
		return errors.New("invalid decision option mix")
	}
	if p.RecommendedID != "" && !seen[p.RecommendedID] {
		return errors.New("unknown recommended option")
	}
	return nil
}
