package inboundcoord

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode/utf8"

	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/shared"
)

const WindowPlanMaxItems = 8
const WindowPlanVersion = "window-plan-v1"
const ReplicaPlanMarker = "[coordinator-plan:window-plan-v1]"

// CoordinationAction is the model-facing operation. Reply is scoped to its kind;
// the internal Decision still drives the durable, idempotent Host submission.
type CoordinationAction struct {
	Kind            string   `json:"kind"`
	SourceRefs      []string `json:"source_refs,omitempty"`
	Reply           string   `json:"reply,omitempty"`
	Purpose         string   `json:"purpose,omitempty"`
	Intent          string   `json:"intent,omitempty"`
	Context         string   `json:"context,omitempty"`
	IssueID         string   `json:"issue_id,omitempty"`
	Basis           string   `json:"basis,omitempty"`
	MissingFields   []string `json:"missing_fields,omitempty"`
	StateRefs       []string `json:"state_refs,omitempty"`
	AckKind         string   `json:"ack_kind,omitempty"`
	MemoryRevision  *int64   `json:"memory_revision,omitempty"`
	ResultRef       string   `json:"result_ref,omitempty"`
	Reason          string   `json:"reason,omitempty"`
	ReasonCode      string   `json:"reason_code,omitempty"`
	ConstraintQuote string   `json:"constraint_quote,omitempty"`
}

func windowPlanTool(canPlanWork bool) openai.ChatCompletionToolUnionParam {
	return coordinationFinishTool(canPlanWork, false)
}
func coordinationFinishTool(canPlanWork, taskFinished bool) openai.ChatCompletionToolUnionParam {
	kinds := []string{"clarify", "report_status", "acknowledge", "describe_capabilities", "report_memory", "decline", "ignore"}
	if canPlanWork {
		kinds = append(kinds, "start_work", "continue_work")
	}
	if taskFinished {
		kinds = []string{"report_result", "ignore"}
	}
	props := map[string]any{
		"kind":        map[string]any{"type": "string", "enum": kinds},
		"source_refs": map[string]any{"type": "array", "minItems": 1, "items": map[string]any{"type": "string"}, "description": "Exact current-window uN refs. Cover every input, including non-work. Multiple intents may share a ref."},
		"reply":       map[string]any{"type": "string", "description": "Required except ignore. Only user-facing communication belonging to this operation, never a business answer or internal routing narration. For work, briefly name what you will do. Host sends it after submission succeeds."},
		"reason":      map[string]any{"type": "string", "description": "Only ignore: why no response/work is needed."},
	}
	if taskFinished {
		delete(props, "source_refs")
		props["result_ref"] = map[string]any{"type": "string", "description": "report_result only: copy current_result_ref. Summarize the supplied current result only; no invented delivery."}
	} else {
		props["reason_code"] = map[string]any{"type": "string", "enum": []string{"scope", "authorization", "privacy"}, "description": "decline only: the explicit boundary preventing the request."}
		props["constraint_quote"] = map[string]any{"type": "string", "maxLength": 300, "description": "decline only: exact quote of the user restriction or loaded coordination/job policy. Explain this boundary; do not answer the business question."}
		props["missing_fields"] = map[string]any{"type": "array", "minItems": 1, "items": map[string]any{"type": "string", "enum": []string{"intent", "recipient", "message_body", "scope", "timing", "authorization", "work_target", "source_material"}}, "description": "clarify only: missing information preventing a safe dispatch."}
		props["state_refs"] = map[string]any{"type": "array", "minItems": 1, "items": map[string]any{"type": "string"}, "description": "report_status only: rN read_ref from current Host assoc_recall/work_state snapshots, including bounded empty or unavailable reads."}
		props["ack_kind"] = map[string]any{"type": "string", "enum": []string{"greeting", "thanks", "correction", "receipt"}, "description": "acknowledge only; never substitute for executable work."}
		props["memory_revision"] = map[string]any{"type": "integer", "description": "report_memory only: copy scene_memory_revision; report only the supplied memory and its availability."}
		if canPlanWork {
			props["purpose"] = map[string]any{"type": "string", "minLength": 8, "maxLength": 240, "description": "Work only: ONE independently executable deliverable with its concrete target. Separate unrelated deliverables into separate actions; never hide them as a numbered list inside one purpose. Amendments or steps toward the same artifact may stay together. Host binds the speaker."}
			props["intent"] = map[string]any{"type": "string", "enum": []string{"ask", "confirm", "notify", "lookup", "wait", "other"}}
			props["context"] = map[string]any{"type": "string", "maxLength": 500, "description": "Work only: necessary context, without copying history or scene memory."}
			props["issue_id"] = recalledIssueIDSchema("continue_work only: exact recalled Issue UUID. start_work must omit it.")
			props["basis"] = map[string]any{"type": "string", "enum": []string{"answer", "change", "retry"}, "description": "continue_work only. answer requires loaded original history; status pings are not retry."}
		}
	}
	variants := make([]any, 0, len(kinds))
	for _, kind := range kinds {
		required := []string{"kind"}
		if !taskFinished {
			required = append(required, "source_refs")
		}
		if kind == "ignore" {
			required = append(required, "reason")
		} else {
			required = append(required, "reply")
		}
		switch kind {
		case "start_work":
			required = append(required, "purpose", "intent")
		case "continue_work":
			required = append(required, "purpose", "intent", "issue_id", "basis")
		case "clarify":
			required = append(required, "missing_fields")
		case "report_status":
			required = append(required, "state_refs")
		case "acknowledge":
			required = append(required, "ack_kind")
		case "report_memory":
			required = append(required, "memory_revision")
		case "decline":
			required = append(required, "reason_code", "constraint_quote")
		case "report_result":
			required = append(required, "result_ref")
		}
		variants = append(variants, map[string]any{"properties": map[string]any{"kind": map[string]any{"enum": []string{kind}}}, "required": required})
	}
	return openai.ChatCompletionFunctionTool(shared.FunctionDefinitionParam{
		Name:        toolFinish,
		Description: openai.String("Finish coordination using 1-8 explicit actions. No generic reply action. Each action owns its reply and only its documented fields; ignore owns reason. Product/professional questions require start_work or continue_work, even if easy. Recall first before work. One work action per independent deliverable; do not bundle unrelated requests into one purpose. Mixed work, clarification and acknowledgements are allowed; cover the full window. Never claim a proposed action already succeeded."),
		Parameters:  shared.FunctionParameters{"type": "object", "additionalProperties": false, "required": []string{"actions"}, "properties": map[string]any{"actions": map[string]any{"type": "array", "minItems": 1, "maxItems": WindowPlanMaxItems, "items": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"kind"}, "properties": props, "oneOf": variants}}}},
	})
}

func currentResultRef(turn Turn) string {
	sum := sha256.Sum256([]byte(turn.IssueID + "\x00" + turn.ConversationID + "\x00" + turn.TaskResult + "\x00" + turn.TaskDeliveryContext))
	return "result_" + hex.EncodeToString(sum[:12])
}

func parseValidatedWindowPlan(raw string, turn Turn, recalls []recallCall, recalled map[string]struct{}) (Decision, error) {
	var input struct {
		Actions []json.RawMessage `json:"actions"`
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return Decision{}, hintErr("invalid finish JSON: "+err.Error(), "Use finish({actions:[...]}) and only the advertised operation fields. Generic reply/text/action is unavailable.")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return Decision{}, fmt.Errorf("finish must contain one JSON object")
	}
	if len(input.Actions) == 0 || len(input.Actions) > WindowPlanMaxItems {
		return Decision{}, fmt.Errorf("finish requires 1-8 actions")
	}
	d := Decision{Action: ActionSilence, PlanVersion: WindowPlanVersion}
	utterances := windowUtterances(turn)
	byRef := map[string]WindowUtterance{}
	for i, u := range utterances {
		byRef[fmt.Sprintf("u%d", i+1)] = u
	}
	covered, workRefs, continued := map[string]bool{}, map[string]bool{}, map[string]bool{}
	replies := []string{}
	for _, entry := range input.Actions {
		var a CoordinationAction
		dec := json.NewDecoder(strings.NewReader(string(entry)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&a); err != nil {
			return Decision{}, fmt.Errorf("invalid coordination action: %w", err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(entry, &fields); err != nil {
			return Decision{}, err
		}
		allowed := map[string]bool{"kind": true}
		if turn.Loop != LoopTaskFinished {
			allowed["source_refs"] = true
		}
		work := false
		switch a.Kind {
		case "start_work", "continue_work":
			work = true
			for _, k := range []string{"purpose", "intent", "context", "reply"} {
				allowed[k] = true
			}
			if a.Kind == "continue_work" {
				allowed["issue_id"] = true
				allowed["basis"] = true
			}
		case "decline":
			allowed["reason_code"] = true
			allowed["constraint_quote"] = true
			allowed["reply"] = true
		case "clarify":
			allowed["missing_fields"] = true
			allowed["reply"] = true
		case "report_status":
			allowed["state_refs"] = true
			allowed["reply"] = true
		case "acknowledge":
			allowed["ack_kind"] = true
			allowed["reply"] = true
		case "describe_capabilities":
			allowed["reply"] = true
		case "report_memory":
			allowed["memory_revision"] = true
			allowed["reply"] = true
		case "report_result":
			allowed["result_ref"] = true
			allowed["reply"] = true
		case "ignore":
			allowed["reason"] = true
		default:
			return Decision{}, fmt.Errorf("unknown coordination kind %q", a.Kind)
		}
		for k := range fields {
			if !allowed[k] {
				return Decision{}, fmt.Errorf("%s cannot contain %s", a.Kind, k)
			}
		}
		if turn.Loop == LoopTaskFinished {
			if a.Kind != "report_result" && a.Kind != "ignore" {
				return Decision{}, fmt.Errorf("task_finished permits only report_result or ignore")
			}
			if len(input.Actions) != 1 {
				return Decision{}, fmt.Errorf("task_finished requires one terminal action")
			}
			if a.Kind == "report_result" && a.ResultRef != currentResultRef(turn) {
				return Decision{}, fmt.Errorf("report_result must reference current_result_ref")
			}
		} else {
			if a.Kind == "report_result" {
				return Decision{}, fmt.Errorf("report_result requires task_finished")
			}
			if len(a.SourceRefs) == 0 {
				return Decision{}, fmt.Errorf("every action requires source_refs")
			}
			seen := map[string]bool{}
			for _, ref := range a.SourceRefs {
				if _, ok := byRef[ref]; !ok || seen[ref] {
					return Decision{}, fmt.Errorf("unknown or duplicate source_ref %q", ref)
				}
				seen[ref] = true
				covered[ref] = true
				if work {
					workRefs[ref] = true
				}
			}
			sort.SliceStable(a.SourceRefs, func(i, j int) bool {
				var x, y int
				_, _ = fmt.Sscanf(a.SourceRefs[i], "u%d", &x)
				_, _ = fmt.Sscanf(a.SourceRefs[j], "u%d", &y)
				return x < y
			})
		}
		a.Reply = strings.TrimSpace(a.Reply)
		if a.Kind == "ignore" {
			if strings.TrimSpace(a.Reason) == "" || utf8.RuneCountInString(a.Reason) > 300 {
				return Decision{}, fmt.Errorf("ignore requires a reason of at most 300 characters")
			}
			d.Reason = a.Reason
		} else {
			limit := 600
			if a.Kind == "report_memory" || a.Kind == "report_result" {
				limit = 1800
			}
			if a.Reply == "" || utf8.RuneCountInString(a.Reply) > limit {
				return Decision{}, fmt.Errorf("%s requires reply of at most %d characters", a.Kind, limit)
			}
			replies = append(replies, a.Reply)
		}
		switch a.Kind {
		case "decline":
			if !oneOf(a.ReasonCode, "scope", "authorization", "privacy") || strings.TrimSpace(a.ConstraintQuote) == "" || utf8.RuneCountInString(a.ConstraintQuote) > 300 {
				return Decision{}, fmt.Errorf("decline requires a reason_code and exact constraint_quote")
			}
			matched := strings.Contains(coordinationConstraintText(turn), a.ConstraintQuote)
			for _, u := range utterances {
				matched = matched || strings.Contains(u.Text, a.ConstraintQuote)
			}
			if !matched {
				return Decision{}, fmt.Errorf("decline constraint_quote must quote an actual supplied boundary")
			}
		case "clarify":
			if !validEnumList(a.MissingFields, []string{"intent", "recipient", "message_body", "scope", "timing", "authorization", "work_target", "source_material"}) {
				return Decision{}, fmt.Errorf("clarify requires explicit valid missing_fields")
			}
		case "acknowledge":
			if !oneOf(a.AckKind, "greeting", "thanks", "correction", "receipt") {
				return Decision{}, fmt.Errorf("acknowledge requires ack_kind")
			}
		case "report_memory":
			if a.MemoryRevision == nil || *a.MemoryRevision != turn.SceneMemoryRevision {
				return Decision{}, fmt.Errorf("report_memory must copy scene_memory_revision")
			}
		case "report_status":
			if len(a.StateRefs) == 0 {
				return Decision{}, fmt.Errorf("report_status requires current Host state_refs")
			}
			seen := map[string]bool{}
			for _, ref := range a.StateRefs {
				found := false
				for _, r := range turn.CoordinationReads {
					if r.ReadRef == ref && (r.Tool == toolAssocRecall || r.Tool == toolWorkState) {
						found = true
					}
				}
				if !found || seen[ref] {
					return Decision{}, fmt.Errorf("unknown or duplicate state_ref %q", ref)
				}
				seen[ref] = true
			}
		}
		if work {
			if strings.TrimSpace(a.Purpose) == "" {
				return Decision{}, hintErr("work action is missing purpose", "Set purpose to the concrete authorized deliverable, at most 240 characters.")
			}
			if !oneOf(a.Intent, "ask", "confirm", "notify", "lookup", "wait", "other") {
				return Decision{}, hintErr("work action has missing or invalid intent", "Every start_work/continue_work requires intent: ask, confirm, notify, lookup, wait, or other. Keep its purpose, reply and source_refs.")
			}
			if !hasRecall(recalls, turn.ConversationID) && turn.ConversationID != "" {
				return Decision{}, hintErr("current scene recall required before work", "Call assoc_recall for this conversation before choosing start_work or continue_work.")
			}
			if utf8.RuneCountInString(a.Purpose) > 240 || utf8.RuneCountInString(a.Context) > 500 {
				return Decision{}, fmt.Errorf("work purpose/context exceeds coordination budget")
			}
			basis := "new_request"
			if a.Kind == "continue_work" {
				if _, ok := recalled[a.IssueID]; !ok || a.IssueID == "" {
					return Decision{}, fmt.Errorf("continue_work requires a recalled issue_id")
				}
				if !oneOf(a.Basis, "answer", "change", "retry") {
					return Decision{}, fmt.Errorf("continue_work requires answer/change/retry basis")
				}
				if a.Basis == "answer" && turn.HistoryStatus != "loaded" {
					return Decision{}, hintErr("original question evidence is required", "Read context_read(kind=history) before interpreting a short answer. If unavailable, clarify.")
				}
				if continued[a.IssueID] {
					return Decision{}, fmt.Errorf("combine all continuation refs for one Issue into one action")
				}
				continued[a.IssueID] = true
				basis = a.Basis
			}
			first := byRef[a.SourceRefs[0]]
			item, ok := newWindowItem(turn, firstNonEmpty(first.Sender, turn.SenderName, "用户"), "", a.Purpose, a.Intent, a.Context)
			if !ok {
				return Decision{}, fmt.Errorf("invalid deliverable purpose or intent")
			}
			content := []string{}
			for _, ref := range a.SourceRefs {
				u := byRef[ref]
				label := " 在钉钉会话中的消息：\n\n"
				if turn.Source == SourceWeb {
					label = " 在当前会话中的消息：\n\n"
				}
				content = append(content, firstNonEmpty(u.Sender, "用户")+label+u.Text)
			}
			item.Reply = a.Reply
			item.SourceRefs = a.SourceRefs
			item.IssueID = a.IssueID
			item.Basis = basis
			item.Content = strings.Join(content, "\n\n")
			item.ActionKey = fmt.Sprintf("item-%d", len(d.Items)+1)
			d.Items = append(d.Items, item)
		}
		d.CoordinationActions = append(d.CoordinationActions, a)
	}
	if turn.Loop != LoopTaskFinished {
		for ref := range byRef {
			if !covered[ref] {
				return Decision{}, hintErr("window has unhandled source: "+ref, "Give each source_ref an explicit coordination action; do not discard any current request.")
			}
			if !workRefs[ref] {
				d.NonWorkRefs = append(d.NonWorkRefs, ref)
			}
		}
		sort.Strings(d.NonWorkRefs)
	}
	d.UserText = strings.Join(replies, "\n\n")
	if utf8.RuneCountInString(d.UserText) > 2400 {
		return Decision{}, fmt.Errorf("combined replies exceed 2400 characters")
	}
	if len(d.Items) > 0 {
		d.Action = ActionIssue
		d.Purpose = d.Items[0].Purpose
		d.Intent = d.Items[0].Intent
		d.LookInto = d.Items[0].LookInto
	} else if d.UserText != "" {
		d.Action = ActionReply
	}
	if d.Action == ActionSilence && turn.Source == SourceWeb && turn.Loop != LoopTaskFinished {
		return Decision{}, fmt.Errorf("web chat requires a visible coordination action")
	}
	if d.Action != ActionSilence && turn.Loop != LoopTaskFinished {
		for _, cid := range extractConversationIDs(turn.Message) {
			if !hasRecall(recalls, cid) {
				return Decision{}, hintErr("named conversation must be recalled", fmt.Sprintf("Call assoc_recall with conversation_id=%q before reporting its recorded matters.", cid))
			}
		}
	}
	return d, nil
}

func hasRecall(recalls []recallCall, cid string) bool {
	for _, r := range recalls {
		if r.ConversationID == cid {
			return true
		}
	}
	return false
}
func oneOf(value string, values ...string) bool {
	for _, v := range values {
		if value == v {
			return true
		}
	}
	return false
}
func validEnumList(values, allowed []string) bool {
	if len(values) == 0 {
		return false
	}
	seen := map[string]bool{}
	for _, v := range values {
		if !oneOf(v, allowed...) || seen[v] {
			return false
		}
		seen[v] = true
	}
	return true
}

func (d Decision) CoordinationKinds() []string {
	kinds := make([]string, 0, len(d.CoordinationActions))
	for _, a := range d.CoordinationActions {
		kinds = append(kinds, a.Kind)
	}
	return kinds
}
