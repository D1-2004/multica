package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/multica-ai/multica/server/pkg/featureflag"
)

// The claim-time task instruction is not one blob. It is an ordered set of
// independent segments with different owners: deployment configuration, the
// Router's per-dispatch facts, and product constants. This file is the single
// place that knows the set and the order.
//
// Both the real claim path and the settings preview compose through
// composeDispatchInstructionSegments, so a preview cannot drift from what an
// agent actually receives. That is the whole point of the indirection — a
// preview assembled by separate code would be a second implementation to keep
// in sync, and would silently start lying.
const (
	// DispatchSegmentPolicy is the fixed behavioral policy: the managed
	// common section plus the section for the current dispatch mode.
	DispatchSegmentPolicy = "policy"
	// DispatchSegmentContext is the Router's resolved delivery facts for this
	// one dispatch — conversation, message and sender locators, outbound
	// ownership. Not authored policy, and therefore never overridable.
	DispatchSegmentContext = "context"
	// DispatchSegmentDingTalkConversation carries what the visible conversation
	// text cannot hold: that the DingTalk conversation is the source of truth
	// rather than Multica's mirror of it, and the identifiers that make the
	// conversation and any quoted message re-readable.
	DispatchSegmentDingTalkConversation = "dingtalk_conversation"
	// DispatchSegmentReplyFormatting constrains Markdown that survives
	// DingTalk delivery.
	DispatchSegmentReplyFormatting = "reply_formatting"
	// DispatchSegmentEnterpriseIdentity tells the agent how to surface a BUC
	// authorization failure instead of retrying or asking for credentials.
	DispatchSegmentEnterpriseIdentity = "enterprise_identity"
	// DispatchSegmentSceneGraph tells the agent how to recall and bind DingTalk
	// conversations to the current Issue.
	DispatchSegmentSceneGraph = "scene_graph"
)

// Segment sources, as reported to the settings UI.
const (
	dispatchSegmentSourceManaged = "managed" // deployment configuration (Diamond)
	dispatchSegmentSourceBuiltin = "builtin" // product constant in the binary
	dispatchSegmentSourceRouter  = "router"  // per-dispatch, resolved upstream
)

// DispatchPromptSegment is one row of the composed instruction, carrying enough
// for the settings UI to render the real structure: where the text comes from,
// whether this agent replaced it, and whether it applies at all to the previewed
// scenario.
type DispatchPromptSegment struct {
	ID     string `json:"id"`
	Source string `json:"source"`
	// Delivery is where an included segment travels: the runtime brief (system
	// prompt, once per session) or the per-turn message. See the constants.
	Delivery string `json:"delivery"`
	// Customizable is false for segments an agent must not replace. Today only
	// the Router context, which carries facts rather than policy.
	Customizable bool `json:"customizable"`
	// Overridden reports whether this agent authored a replacement.
	Overridden bool `json:"overridden"`
	// Condition names the gate that decides whether this segment is injected at
	// all. It is always present, not only when the segment is excluded: an owner
	// editing a prompt needs to know when it will reach the agent, and "it is
	// showing right now" does not answer that. The UI maps the key to copy.
	Condition string `json:"condition"`
	// Included reports whether the segment reaches the agent in the previewed
	// scenario; ExcludedReason names the gate that dropped it.
	Included       bool   `json:"included"`
	ExcludedReason string `json:"excluded_reason,omitempty"`
	// ManagedText is what the segment would contain without this agent's
	// override; EffectiveText is what the agent actually receives.
	ManagedText   string `json:"managed_text"`
	EffectiveText string `json:"effective_text"`
}

// customizableDispatchSegments is the allow-list for override keys. An unknown
// key is rejected at the API boundary rather than silently stored, so a typo
// cannot look like a saved customization that never takes effect.
var customizableDispatchSegments = map[string]bool{
	DispatchSegmentPolicy:               true,
	DispatchSegmentReplyFormatting:      true,
	DispatchSegmentEnterpriseIdentity:   true,
	DispatchSegmentSceneGraph:           true,
	DispatchSegmentContext:              false,
	DispatchSegmentDingTalkConversation: false,
}

// dispatchSegmentOrder is the order the agent reads the segments in.
var dispatchSegmentOrder = []string{
	DispatchSegmentPolicy,
	DispatchSegmentContext,
	DispatchSegmentDingTalkConversation,
	DispatchSegmentSceneGraph,
	DispatchSegmentReplyFormatting,
	DispatchSegmentEnterpriseIdentity,
}

type dispatchInstructionInputs struct {
	// Stored is the persisted dispatch context; Present reports whether the
	// task carried one at all.
	Stored  persistedDispatchContext
	Present bool
	// DingTalkContext is broader than Present: a DingTalk stream task has no
	// dispatch envelope but still gets reply formatting.
	DingTalkContext bool
	Flags           *featureflag.Service
	Overrides       map[string]string
	// EnterpriseAuthorizationURL is empty unless the run is on an ASB runtime
	// with a resolvable workspace and agent.
	EnterpriseAuthorizationURL string
	// ResumedSession is true when the claim keeps a provider session for the
	// daemon to resume — a warm 1:1 cloud chat the agent opted into. It only
	// changes the wording of the conversation segment: the run already holds
	// its earlier turns, so it is told which message is the unanswered one
	// rather than to re-read a room it is looking at.
	ResumedSession bool
}

// Where an included segment travels. The split is by how often the text
// changes, and it is the whole reason the per-turn message got shorter.
//
// A segment whose text is the same on every turn of a session — the managed
// policy, reply formatting, the BUC authorization rule — rides in the runtime
// brief: it is appended to the agent instructions the claim carries, which the
// daemon renders into AGENTS.md / the system prompt. The provider then sees it
// once per request, ahead of the conversation, instead of once per turn inside
// it — a resumed session used to accumulate a copy per turn (production trace
// b60a1060… carried three), and the title-generation call was fed 3k tokens of
// policy to name a "HI".
//
// A segment that changes per dispatch — the Router's delivery facts and the
// conversation locators — stays in task.instruction, at the head of the
// per-turn user message, where it is next to the message it is about.
const (
	dispatchDeliveryRuntimeBrief = "runtime_brief"
	dispatchDeliveryPerTurn      = "per_turn"
)

// dispatchInstructionByDelivery splits what the agent receives into the text
// that rides in the runtime brief and the text that heads the per-turn message.
// Order within each half is dispatchSegmentOrder.
func dispatchInstructionByDelivery(segments []DispatchPromptSegment) (brief, perTurn string) {
	briefParts := make([]string, 0, len(segments))
	turnParts := make([]string, 0, len(segments))
	for _, segment := range segments {
		if !segment.Included {
			continue
		}
		if segment.Delivery == dispatchDeliveryRuntimeBrief {
			briefParts = append(briefParts, segment.EffectiveText)
		} else {
			turnParts = append(turnParts, segment.EffectiveText)
		}
	}
	return joinDispatchPromptSections(briefParts...), joinDispatchPromptSections(turnParts...)
}

func parseDispatchPromptOverrides(raw []byte) map[string]string {
	if len(raw) == 0 {
		return map[string]string{}
	}
	var parsed map[string]string
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return map[string]string{}
	}
	out := make(map[string]string, len(parsed))
	for key, value := range parsed {
		if !customizableDispatchSegments[key] {
			continue
		}
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			out[key] = trimmed
		}
	}
	return out
}

// composeDispatchInstructionSegments builds every segment, including the ones
// that do not apply. Excluded segments are retained (Included=false) so the
// settings UI can explain why a segment the user configured is not showing up,
// instead of silently omitting it. instructionFromSegments drops them.
func composeDispatchInstructionSegments(in dispatchInstructionInputs) []DispatchPromptSegment {
	segments := make([]DispatchPromptSegment, 0, len(dispatchSegmentOrder))

	// Policy is scoped to dispatch runs. A DingTalk stream task with no dispatch
	// envelope has no mode, and inflating it with the common section would give
	// it policy it does not receive today.
	managedPolicy := ""
	if in.Present {
		managedPolicy = joinDispatchPromptSections(
			resolveDispatchRuntimePrompt(in.Flags, featureflag.DispatchCommonRuntimePromptFlagKey),
			resolveSurfaceRuntimePrompt(in.Flags, in.Stored.Surface.Type),
		)
	}
	segments = append(segments, dispatchSegment(
		DispatchSegmentPolicy, dispatchSegmentSourceManaged, dispatchDeliveryRuntimeBrief,
		managedPolicy, in.Overrides,
		in.Present, "dingtalk_dispatch", "no_dispatch_context",
	))

	segments = append(segments, DispatchPromptSegment{
		ID:             DispatchSegmentContext,
		Source:         dispatchSegmentSourceRouter,
		Delivery:       dispatchDeliveryPerTurn,
		Customizable:   false,
		Condition:      "per_dispatch",
		Included:       in.Present && strings.TrimSpace(in.Stored.ContextPrompt) != "",
		ExcludedReason: dispatchExcludedReason(in.Present, "no_dispatch_context", "not_supplied"),
		ManagedText:    in.Stored.ContextPrompt,
		EffectiveText:  in.Stored.ContextPrompt,
	})

	// Facts, not policy: the locators come from the dispatch envelope, so this
	// segment is composed here rather than configured, and cannot be overridden.
	conversation := ""
	if in.Present {
		conversation = buildDispatchConversationInstruction(in.Stored, in.ResumedSession)
	}
	segments = append(segments, DispatchPromptSegment{
		ID:             DispatchSegmentDingTalkConversation,
		Source:         dispatchSegmentSourceBuiltin,
		Delivery:       dispatchDeliveryPerTurn,
		Customizable:   false,
		Condition:      "dingtalk_conversation",
		Included:       conversation != "",
		ExcludedReason: dispatchExcludedReason(in.Present, "no_dispatch_context", "no_conversation_context"),
		ManagedText:    conversation,
		EffectiveText:  conversation,
	})

	assocApplies := in.Present && in.Stored.Source.Platform == "dingtalk" && in.Stored.Domain == "channel"
	segments = append(segments, dispatchSegment(
		DispatchSegmentSceneGraph, dispatchSegmentSourceBuiltin,
		dispatchSceneGraphInstruction, in.Overrides,
		assocApplies, "dingtalk_channel", "not_a_dingtalk_channel",
	))

	segments = append(segments, dispatchSegment(
		DispatchSegmentReplyFormatting, dispatchSegmentSourceBuiltin, dispatchDeliveryRuntimeBrief,
		dingTalkReplyFormattingInstruction, in.Overrides,
		in.DingTalkContext, "any_dingtalk_task", "not_a_dingtalk_task",
	))

	enterpriseManaged := ""
	if url := strings.TrimSpace(in.EnterpriseAuthorizationURL); url != "" {
		enterpriseManaged = strings.Replace(enterpriseIdentityAuthorizationInstruction, "%s", url, 1)
	}
	segments = append(segments, dispatchSegment(
		DispatchSegmentEnterpriseIdentity, dispatchSegmentSourceBuiltin, dispatchDeliveryRuntimeBrief,
		enterpriseManaged, in.Overrides,
		strings.TrimSpace(in.EnterpriseAuthorizationURL) != "", "enterprise_runtime", "not_an_enterprise_identity_runtime",
	))

	return segments
}

func dispatchSegment(
	id, source, delivery, managed string,
	overrides map[string]string,
	applies bool,
	condition string,
	excludedReason string,
) DispatchPromptSegment {
	// A blank override is "restore the managed text", never "make this segment
	// empty". The API boundary normalizes blanks away, but the composer must
	// agree independently: a stored blank arriving from an older write would
	// otherwise silently delete a whole policy section.
	effective := managed
	overridden := false
	if authored, present := overrides[id]; present && strings.TrimSpace(authored) != "" {
		effective = authored
		overridden = true
	}
	included := applies && strings.TrimSpace(effective) != ""
	reason := ""
	if !applies {
		reason = excludedReason
	} else if !included {
		reason = "empty"
	}
	return DispatchPromptSegment{
		ID:             id,
		Source:         source,
		Delivery:       delivery,
		Customizable:   true,
		Condition:      condition,
		Overridden:     overridden,
		Included:       included,
		ExcludedReason: reason,
		ManagedText:    managed,
		EffectiveText:  effective,
	}
}

func dispatchExcludedReason(applies bool, gateReason, emptyReason string) string {
	if !applies {
		return gateReason
	}
	return emptyReason
}

// instructionFromSegments joins what the agent actually receives.
func instructionFromSegments(segments []DispatchPromptSegment) string {
	parts := make([]string, 0, len(segments))
	for _, segment := range segments {
		if !segment.Included {
			continue
		}
		parts = append(parts, segment.EffectiveText)
	}
	return joinDispatchPromptSections(parts...)
}

// encodeDispatchPromptOverrides validates an override map from the API
// boundary and encodes it for storage. An unknown segment id is a 400 rather
// than a silent drop: a typo that stores cleanly but never takes effect is the
// worst outcome for someone editing a prompt they cannot otherwise observe.
func encodeDispatchPromptOverrides(w http.ResponseWriter, overrides map[string]string) ([]byte, bool) {
	cleaned := make(map[string]string, len(overrides))
	for id, text := range overrides {
		customizable, known := customizableDispatchSegments[id]
		if !known {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("unknown dispatch prompt segment %q", id))
			return nil, false
		}
		if !customizable {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("dispatch prompt segment %q cannot be customized", id))
			return nil, false
		}
		if utf8.RuneCountInString(text) > maxAgentDispatchPromptLength {
			writeError(w, http.StatusBadRequest, fmt.Sprintf(
				"dispatch prompt segment %q must be %d characters or fewer", id, maxAgentDispatchPromptLength))
			return nil, false
		}
		// A blank override is the same as no override. Normalizing here keeps
		// "restore managed" from depending on whether the client omitted the
		// key or sent an empty string.
		if trimmed := strings.TrimSpace(text); trimmed != "" {
			cleaned[id] = trimmed
		}
	}
	encoded, err := json.Marshal(cleaned)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to encode dispatch prompt overrides")
		return nil, false
	}
	return encoded, true
}
