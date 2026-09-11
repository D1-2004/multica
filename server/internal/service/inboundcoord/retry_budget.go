package inboundcoord

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	openai "github.com/openai/openai-go/v3"
)

// Retry budgets stop a run from spending its eight rounds on the same failing
// step. A read that failed twice with the same arguments is withdrawn; a plan
// that Host rejected three times for the same defect, or that review sent back
// three times with the same reason, ends the run early as deferred instead of
// producing the same rejection until the round cap. Ending early is not a
// reply, but it is cheaper and faster than eight identical rejections.
const (
	// repeatedCallBudget is how many times the same tool call may fail before
	// a further identical call is refused and the tool is withdrawn.
	repeatedCallBudget = 2
	// repeatedFinishErrorBudget is how many times Host may reject a plan for
	// the same defect before the run stops.
	repeatedFinishErrorBudget = 3
	// repeatedReviewReasonBudget is how many times review may return the same
	// reason before the run stops.
	repeatedReviewReasonBudget = 3
)

const (
	loopStopRepeatedInvalidPlan = "repeated_invalid_plan"
	loopStopReviewDeadlock      = "review_deadlock"
	loopStopRoundsExhausted     = "rounds_exhausted"
)

type retryLedger struct {
	failedCalls   map[string]int
	withdrawn     map[string]bool
	finishErrors  map[string]int
	reviewReasons map[string]int
}

func newRetryLedger() *retryLedger {
	return &retryLedger{failedCalls: map[string]int{}, withdrawn: map[string]bool{}, finishErrors: map[string]int{}, reviewReasons: map[string]int{}}
}

// callKey identifies a tool call by name and canonical arguments so that
// whitespace or key order cannot disguise a repeat.
func callKey(name, arguments string) string {
	var decoded any
	if json.Unmarshal([]byte(strings.TrimSpace(arguments)), &decoded) == nil {
		if canonical, err := json.Marshal(decoded); err == nil {
			return name + ":" + string(canonical)
		}
	}
	return name + ":" + strings.TrimSpace(arguments)
}

// refuseRepeat reports whether an identical call already used its budget. The
// returned hint tells the model what to do instead of repeating.
func (l *retryLedger) refuseRepeat(name, arguments string) error {
	if l == nil || name == toolFinish {
		// finish is judged by its Host defect and review reason counters, so an
		// identical proposal still reaches the validator that names the defect.
		return nil
	}
	if l.withdrawn[name] {
		return hintErr(fmt.Sprintf("%s is withdrawn for this run after %d identical failures", name, repeatedCallBudget),
			"Do not call it again. Decide from the evidence already shown: submit finish({actions:[...]}) with clarify, report_status, acknowledge or ignore, or use a different available read.")
	}
	if count := l.failedCalls[callKey(name, arguments)]; count >= repeatedCallBudget {
		return hintErr(fmt.Sprintf("retry budget exhausted: %s failed %d times with the same arguments", name, count),
			"Do not repeat it. Decide from the evidence already shown or use a different available read; then submit finish({actions:[...]}).")
	}
	return nil
}

// recordFailure counts a failed call and withdraws a read tool once the same
// arguments have failed repeatedly. finish is never withdrawn.
func (l *retryLedger) recordFailure(name, arguments string) (count int, withdrawn bool) {
	if l == nil {
		return 0, false
	}
	key := callKey(name, arguments)
	l.failedCalls[key]++
	count = l.failedCalls[key]
	if name != toolFinish && count >= repeatedCallBudget && !l.withdrawn[name] {
		l.withdrawn[name] = true
		withdrawn = true
	}
	return count, withdrawn
}

// recordFinishError counts a Host validation defect repeated for the same
// proposal shape. A materially changed plan (different kinds, refs, purposes
// or targets) restarts the count even when the defect text is the same.
func (l *retryLedger) recordFinishError(err error, arguments string) int {
	if l == nil || err == nil {
		return 0
	}
	key := normalizeRepeatKey(err.Error()) + "|" + proposalShape(arguments)
	l.finishErrors[key]++
	return l.finishErrors[key]
}

// recordReviewReason counts a review reason repeated for the same proposal
// shape; a repaired plan that still draws the same reason starts over.
func (l *retryLedger) recordReviewReason(reason, arguments string) int {
	if l == nil {
		return 0
	}
	key := normalizeRepeatKey(reason) + "|" + proposalShape(arguments)
	l.reviewReasons[key]++
	return l.reviewReasons[key]
}

// proposalShape reduces a finish proposal to what the plan does: kinds, refs,
// purposes, targets and fields. Reply wording is excluded so that rewording
// the same plan still counts as the same plan.
func proposalShape(arguments string) string {
	var input struct {
		Actions []struct {
			Kind          string   `json:"kind"`
			SourceRefs    []string `json:"source_refs"`
			Purpose       string   `json:"purpose"`
			IssueID       string   `json:"issue_id"`
			Basis         string   `json:"basis"`
			Intent        string   `json:"intent"`
			MissingFields []string `json:"missing_fields"`
			StateRefs     []string `json:"state_refs"`
			AckKind       string   `json:"ack_kind"`
			ReasonCode    string   `json:"reason_code"`
			Quote         string   `json:"constraint_quote"`
		} `json:"actions"`
	}
	if json.Unmarshal([]byte(strings.TrimSpace(arguments)), &input) != nil || len(input.Actions) == 0 {
		return policyHash(strings.TrimSpace(arguments))
	}
	parts := make([]string, 0, len(input.Actions))
	for _, a := range input.Actions {
		refs := append([]string(nil), a.SourceRefs...)
		sort.Strings(refs)
		fields := append([]string(nil), a.MissingFields...)
		sort.Strings(fields)
		states := append([]string(nil), a.StateRefs...)
		sort.Strings(states)
		parts = append(parts, strings.Join([]string{a.Kind, strings.Join(refs, ","), normalizeRepeatKey(a.Purpose), a.IssueID, a.Basis, a.Intent, strings.Join(fields, ","), strings.Join(states, ","), a.AckKind, a.ReasonCode, normalizeRepeatKey(a.Quote)}, "\x1f"))
	}
	sort.Strings(parts)
	return policyHash(strings.Join(parts, "\x1e"))
}

func (l *retryLedger) withdrawnTools() []string {
	if l == nil {
		return nil
	}
	out := make([]string, 0, len(l.withdrawn))
	for name := range l.withdrawn {
		out = append(out, name)
	}
	return out
}

// normalizeRepeatKey collapses whitespace and case so that a reason rendered
// twice with cosmetic differences still counts as the same defect.
func normalizeRepeatKey(text string) string {
	return strings.ToLower(strings.Join(strings.Fields(text), " "))
}

// withoutWithdrawnTools removes tools the ledger withdrew; finish always stays.
func withoutWithdrawnTools(tools []openai.ChatCompletionToolUnionParam, ledger *retryLedger) []openai.ChatCompletionToolUnionParam {
	if ledger == nil || len(ledger.withdrawn) == 0 {
		return tools
	}
	out := make([]openai.ChatCompletionToolUnionParam, 0, len(tools))
	for _, tool := range tools {
		names := toolParamNames([]openai.ChatCompletionToolUnionParam{tool})
		if len(names) == 1 && ledger.withdrawn[names[0]] && names[0] != toolFinish {
			continue
		}
		out = append(out, tool)
	}
	return out
}

// repeatHint appends the repeat count to a failure hint so the next round sees
// that it is looping, not merely failing.
func repeatHint(err error, count int, instruction string) error {
	if count < 2 || err == nil {
		return err
	}
	msg, hint := err.Error(), ""
	var hinted *toolHintError
	if errors.As(err, &hinted) && hinted != nil {
		msg, hint = hinted.Error(), hinted.hint
	}
	msg = fmt.Sprintf("%s (identical failure #%d)", msg, count)
	hint = strings.TrimSpace(hint + " " + instruction)
	// A typed Host prerequisite must survive the repeat note so the loop still
	// recognizes when the required history read has satisfied it.
	if isHistoryPrerequisiteError(err) {
		return historyPrerequisiteHint(msg, hint)
	}
	return hintErr(msg, hint)
}
