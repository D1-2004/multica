package inboundcoord

// coordinatorFallbackReply is the Host-owned reply sent when the loop stops
// for a deterministic reason on a turn that addressed the employee. It is
// fixed text, not a model proposal, so it needs no finish_check and grants
// nothing: no work is stored, no answer is given, the person is only told
// the message was not handled.
const coordinatorFallbackReply = "这条我没接住，麻烦再说一遍或者换个说法，我再看。"

// deterministicLoopStop reports whether a loop stop reason would repeat on
// redelivery. A round cap, the same Host defect three times, or the same
// review reason three times depends on the window and the policy, not on
// the moment; retrying the job six times replays the identical failure and
// still ends in silence. Model, review, or storage errors stay deferred so
// the job worker retries them.
func deterministicLoopStop(reason string) bool {
	switch reason {
	case loopStopRoundsExhausted, loopStopRepeatedInvalidPlan, loopStopReviewDeadlock:
		return true
	}
	return false
}

// loopStopFallback converts a deterministic loop stop into a terminal
// decision. An addressed inbound turn gets the fixed fallback reply; an
// unaddressed one is silenced (the same end state the six job retries used
// to reach, without the retries). The stop reason, steps and rounds are
// kept so SLS and the trace still show why the loop gave up, and the plan
// version marks it as a checkpointable window plan so a redelivered job
// restores this verdict instead of reasoning again. The task_finished loop
// keeps its deferred verdict: its caller owns the retry.
func loopStopFallback(turn Turn, decision Decision) (Decision, bool) {
	if turn.Loop == LoopTaskFinished || !deterministicLoopStop(decision.Reason) {
		return decision, false
	}
	decision.UserText = ""
	decision.Items = nil
	decision.IssueComment = nil
	decision.CoordinationActions = nil
	decision.NonWorkRefs = nil
	decision.IssueID = ""
	decision.Purpose = ""
	decision.Intent = ""
	decision.LookInto = ""
	decision.PlanVersion = WindowPlanVersion
	if !turn.Addressed {
		decision.Action = ActionSilence
		return decision, true
	}
	decision.Action = ActionReply
	decision.UserText = coordinatorFallbackReply
	return decision, true
}

// LoopStopFallback reports whether this verdict is the Host fallback for a
// deterministic loop stop rather than a model plan. Callers without a reply
// channel must not turn it into work.
func (d Decision) LoopStopFallback() bool {
	return (d.Action == ActionReply || d.Action == ActionSilence) && deterministicLoopStop(d.Reason)
}
