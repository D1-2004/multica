package inboundcoord

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/shared"
)

const WindowPlanMaxItems = 8
const WindowPlanVersion = "window-plan-v1"
const ReplicaPlanMarker = "[coordinator-plan:window-plan-v1]"

func windowPlanTool(canPlanWork bool) openai.ChatCompletionToolUnionParam {
	actions := []string{"reply", "silence"}
	if canPlanWork {
		actions = append(actions, "issue")
	}
	fn := shared.FunctionDefinitionParam{
		Name:        toolFinish,
		Description: openai.String("Finish conversation routing, not the business work. Product behavior, professional questions, research and execution require action=issue; never put their answer in text. reply is reserved for greetings, capability explanations, necessary clarification, memory acknowledgement/inventory, or evidenced progress. issue needs acknowledgement text and a complete items plan after recall: new items omit issue_id; continuations use a recalled ID with substantive current input. Cover every source_ref in items or non_work_refs; never execute a request still awaiting clarification. At most 8 items, submitted in batches of 2. Host independently checks terminal decisions before saving; its rejection is not new authorization."),
		Parameters: shared.FunctionParameters{"type": "object", "additionalProperties": false, "required": []string{"action"}, "properties": map[string]any{
			"action":        map[string]any{"type": "string", "enum": actions},
			"text":          map[string]any{"type": "string", "description": "Required for reply and issue; complete work items without text cannot submit. Communication or acknowledgement for the whole window, not a product/business answer or unsupported completion claim."},
			"reason":        map[string]any{"type": "string"},
			"non_work_refs": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Source refs requiring no execution now: greetings or requests explicitly addressed by a necessary clarification in text. Do not silently discard requests."},
			"items": map[string]any{"type": "array", "maxItems": WindowPlanMaxItems, "description": "Required for issue. One item per executable deliverable with sufficient intent and payload; no item for work still awaiting clarification; source_refs copy u1 etc. from current_message.", "items": map[string]any{
				"type": "object", "additionalProperties": false, "required": []string{"source_refs", "purpose", "intent", "basis"}, "properties": map[string]any{
					"source_refs": map[string]any{"type": "array", "minItems": 1, "items": map[string]any{"type": "string"}},
					"issue_id":    map[string]any{"type": "string", "description": "Existing recalled Issue UUID for continuation; omit for a new deliverable."},
					"purpose":     map[string]any{"type": "string", "minLength": 8, "description": "The concrete event, target and deliverable, including needed return recipient. Product names such as DWS身份, MCP or Skills are allowed; do not paste CLI, data-auth or openConversationId. Host binds the actual speaker."},
					"intent":      map[string]any{"type": "string", "enum": []string{"ask", "confirm", "notify", "lookup", "wait", "other"}},
					"basis":       map[string]any{"type": "string", "enum": []string{"new_request", "answer", "change", "retry"}, "description": "Answer requires the original question; status pings/repeated accepted asks are NOT retry."},
					"look_into":   map[string]any{"type": "string", "description": "Only necessary working context; not the whole scene memory/history."},
				},
			}},
		}},
	}
	if !canPlanWork {
		fn.Description = openai.String("Only conversation can finish before recall: greeting, capability explanation, necessary clarification, memory acknowledgement/inventory or verified progress. A product or professional question requires assoc_recall then action=issue, even when it sounds easy. Never substitute an answer or promise for dispatch. Read history when a short answer depends on your previous question. No items at this stage.")
		props := fn.Parameters["properties"].(map[string]any)
		delete(props, "items")
		delete(props, "non_work_refs")
	}
	return openai.ChatCompletionFunctionTool(fn)
}

func parseValidatedWindowPlan(raw string, turn Turn, recalls []recallCall, recalled map[string]struct{}) (Decision, error) {
	var input struct {
		Action      string   `json:"action"`
		Text        string   `json:"text"`
		Reason      string   `json:"reason"`
		IssueID     string   `json:"issue_id"`
		NonWorkRefs []string `json:"non_work_refs"`
		Items       []struct {
			SourceRefs []string `json:"source_refs"`
			IssueID    string   `json:"issue_id"`
			Purpose    string   `json:"purpose"`
			Intent     string   `json:"intent"`
			Basis      string   `json:"basis"`
			LookInto   string   `json:"look_into"`
		} `json:"items"`
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return Decision{}, hintErr("invalid finish JSON: "+err.Error(), "Use exactly the advertised finish schema; source identity comes from source_refs.")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return Decision{}, fmt.Errorf("finish must contain one JSON object")
	}
	if input.IssueID != "" {
		return Decision{}, hintErr("top-level issue_id is invalid", "Put a recalled issue_id on its finish.items entry, together with source_refs, purpose, intent and basis.")
	}
	d := Decision{Action: Action(input.Action), UserText: strings.TrimSpace(input.Text), Reason: input.Reason, PlanVersion: "window-plan-v1", NonWorkRefs: input.NonWorkRefs}
	if d.Action != ActionSilence {
		for _, cid := range extractConversationIDs(turn.Message) {
			found := false
			for _, r := range recalls {
				if r.ConversationID == cid {
					found = true
				}
			}
			if !found {
				return Decision{}, hintErr("named conversation must be recalled", fmt.Sprintf("Call assoc_recall with conversation_id=%q before answering this named scene's recorded matters.", cid))
			}
		}
	}
	if d.Action == ActionReply || d.Action == ActionSilence {
		if len(input.Items) > 0 {
			return Decision{}, fmt.Errorf("work items require action=issue")
		}
		if d.Action == ActionReply && d.UserText == "" {
			return Decision{}, fmt.Errorf("reply needs text")
		}
		if d.Action == ActionSilence && turn.Source == SourceWeb {
			return Decision{}, fmt.Errorf("web chat requires a reply")
		}
		return d, nil
	}
	if d.Action != ActionIssue {
		return Decision{}, hintErr("unknown finish action", "Use action=reply, silence, or issue as advertised.")
	}
	if len(input.Items) == 0 {
		return Decision{}, hintErr("issue needs 1-8 complete work items", hintIssueWorkItems)
	}
	if len(input.Items) > WindowPlanMaxItems {
		return Decision{}, hintErr("finish items is at most 8", hintIssueItemLimit)
	}
	if d.UserText == "" {
		return Decision{}, hintErr("issue needs spoken text", hintIssueSpokenText)
	}
	if turn.ConversationID != "" {
		found := false
		for _, r := range recalls {
			if r.ConversationID == turn.ConversationID {
				found = true
			}
		}
		if !found {
			return Decision{}, hintErr("current scene recall required before work", "Call assoc_recall for this conversation_id before deciding new versus existing work.")
		}
	}
	utterances := windowUtterances(turn)
	byRef := map[string]WindowUtterance{}
	for i, u := range utterances {
		byRef[fmt.Sprintf("u%d", i+1)] = u
	}
	covered := map[string]bool{}
	nonWork := map[string]bool{}
	for _, ref := range input.NonWorkRefs {
		if _, ok := byRef[ref]; !ok {
			return Decision{}, fmt.Errorf("unknown non_work_ref %q", ref)
		}
		covered[ref] = true
		nonWork[ref] = true
	}
	continued := map[string]bool{}
	for i, wire := range input.Items {
		if len(wire.SourceRefs) == 0 {
			return Decision{}, fmt.Errorf("each item requires source_refs")
		}
		seen := map[string]bool{}
		for _, ref := range wire.SourceRefs {
			if seen[ref] || nonWork[ref] {
				return Decision{}, fmt.Errorf("duplicate or contradictory source_ref %q", ref)
			}
			seen[ref] = true
		}
		sort.SliceStable(wire.SourceRefs, func(a, b int) bool {
			var x, y int
			_, _ = fmt.Sscanf(wire.SourceRefs[a], "u%d", &x)
			_, _ = fmt.Sscanf(wire.SourceRefs[b], "u%d", &y)
			return x < y
		})
		var first WindowUtterance
		var content []string
		for j, ref := range wire.SourceRefs {
			u, ok := byRef[ref]
			if !ok {
				return Decision{}, fmt.Errorf("unknown source_ref %q", ref)
			}
			if j == 0 {
				first = u
			}
			label := " 在钉钉会话中的消息：\n\n"
			if turn.Source == SourceWeb {
				label = " 在当前会话中的消息：\n\n"
			}
			content = append(content, firstNonEmpty(u.Sender, "用户")+label+u.Text)
			covered[ref] = true
		}
		switch wire.Basis {
		case "new_request", "answer", "change", "retry":
		default:
			return Decision{}, fmt.Errorf("invalid work basis")
		}
		if wire.Basis == "answer" && turn.HistoryStatus != "loaded" {
			return Decision{}, hintErr("original question evidence is required", "Read context_read(kind=history) before treating a short reply as an answer. If unavailable, clarify instead of guessing consent.")
		}
		if wire.IssueID != "" {
			if _, ok := recalled[wire.IssueID]; !ok {
				return Decision{}, fmt.Errorf("continuation target was not recalled")
			}
			if wire.Basis == "new_request" {
				return Decision{}, fmt.Errorf("a new deliverable cannot continue an existing Issue")
			}
			if continued[wire.IssueID] {
				return Decision{}, hintErr("one continuation per Issue per window", "Combine all substantive source_refs for the same Issue into one item.")
			}
			continued[wire.IssueID] = true
		}
		item, err := composeWindowItem(turn, firstNonEmpty(first.Sender, turn.SenderName, "用户"), "", wire.Purpose, wire.Intent, wire.LookInto)
		if err != nil {
			return Decision{}, err
		}
		item.SourceRefs = wire.SourceRefs
		item.IssueID = wire.IssueID
		item.Basis = wire.Basis
		item.Content = strings.Join(content, "\n\n")
		item.ActionKey = fmt.Sprintf("item-%d", i+1)
		d.Items = append(d.Items, item)
	}
	for ref := range byRef {
		if !covered[ref] {
			return Decision{}, hintErr("window has an unhandled source: "+ref, "Include every request in items; only genuinely non-work messages belong in non_work_refs.")
		}
	}
	d.Purpose = d.Items[0].Purpose
	d.Intent = d.Items[0].Intent
	d.LookInto = d.Items[0].LookInto
	return d, nil
}
