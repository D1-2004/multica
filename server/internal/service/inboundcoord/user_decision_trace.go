package inboundcoord

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/multica-ai/multica/server/internal/langfuse"
	"github.com/multica-ai/multica/server/internal/service/userdecision"
)

func userDecisionChoiceSpanID(id string) string {
	return langfuse.DeterministicSpanID("coordinator.user_decision.choice:" + id)
}

// ObserveUserDecision exports only committed state, never untrusted callbacks
// or a model's recommendation as an accepted human choice.
func (c *Coordinator) ObserveUserDecision(ctx context.Context, r userdecision.Request) error {
	var snapshot UserDecisionSnapshot
	if err := json.Unmarshal(r.Snapshot, &snapshot); err != nil {
		return err
	}
	turn := snapshot.Turn
	if langfuse.TraceIDHex(turn.TraceID) == "" {
		turn.TraceID = r.JobID
	}
	if langfuse.TraceIDHex(turn.TraceID) == "" {
		return errors.New("missing decision trace identity")
	}
	opts := coordinatorTraceOptions(turn, r.CreatedAt)
	// Original trace identity/tags are repeated; only decision-prefixed metadata
	// may change, so late projections cannot replace the coordinator's verdict.
	opts.Metadata = map[string]any{"decision_id": r.ID, "decision_state": r.State, "coord_trace_id": turn.TraceID}
	options := make([]any, 0, len(r.Proposal.Options))
	for _, o := range r.Proposal.Options {
		options = append(options, map[string]any{"id": o.ID, "label": o.Label, "kind": o.Kind})
	}
	input := map[string]any{"decision_id": r.ID, "version": r.Version, "question": r.Proposal.Question, "options": options, "recommended_id": r.Proposal.RecommendedID}
	output := map[string]any{"state": r.State, "accepted": r.Submission != nil, "card_biz_id": r.CardID, "updated_at": r.UpdatedAt}
	if r.Submission != nil {
		label := ""
		for _, o := range r.Proposal.Options {
			if o.ID == r.Submission.OptionID {
				label = o.Label
				break
			}
		}
		output["submission"] = map[string]any{"event_id": r.Submission.EventID, "operator_id": r.Submission.OperatorID, "option_id": r.Submission.OptionID, "option_label": label, "custom": r.Submission.Custom, "received_at": r.Submission.ReceivedAt}
	}
	if len(r.ExecutionResult) > 0 {
		var execution map[string]any
		if json.Unmarshal(r.ExecutionResult, &execution) == nil {
			output["execution_state"] = execution["state"]
		}
	}
	parentID := snapshot.TraceRootSpanID
	if parentID == "" {
		// Historical snapshots predate root IDs. A content-free grouping span
		// keeps their choice child from replacing the original trace input/output.
		parentID = langfuse.DeterministicSpanID("coordinator.user_decision:" + r.ID)
		if err := c.Langfuse.ExportObservation(ctx, opts, langfuse.ObservationOptions{
			Name: "coordinator.user_decision", Type: langfuse.TypeSpan,
			SpanID: parentID, StartTime: r.CreatedAt,
		}, langfuse.EndOptions{EndTime: r.UpdatedAt}); err != nil {
			return err
		}
	}
	return c.Langfuse.ExportObservation(ctx, opts, langfuse.ObservationOptions{
		Name: "coordinator.user_decision.choice", Type: langfuse.TypeSpan,
		SpanID: userDecisionChoiceSpanID(r.ID), ParentSpanID: parentID, StartTime: r.CreatedAt,
		Input:    userdecision.ExportValue(input),
		Metadata: map[string]any{"decision_id": r.ID, "question_id": "q0"},
	}, langfuse.EndOptions{EndTime: r.UpdatedAt, Output: userdecision.ExportValue(output)})
}

func (c *Coordinator) startUserDecisionResolutionTrace(ctx context.Context, s UserDecisionSnapshot, submission userdecision.Submission) *langfuse.Trace {
	turn := s.Turn
	if langfuse.TraceIDHex(turn.TraceID) == "" {
		turn.TraceID = s.DecisionID
	}
	opts := coordinatorTraceOptions(turn, time.Now())
	opts.Metadata = map[string]any{"decision_id": s.DecisionID, "coord_trace_id": turn.TraceID}
	return c.Langfuse.ContinueTrace(ctx, opts, langfuse.ObservationOptions{
		Name: "coordinator.user_decision.resolve", Type: langfuse.TypeChain,
		ParentSpanID: userDecisionChoiceSpanID(s.DecisionID),
		Input:        userdecision.ExportValue(map[string]any{"option_id": submission.OptionID, "custom": submission.Custom, "event_id": submission.EventID}),
		Metadata:     map[string]any{"decision_id": s.DecisionID},
	})
}
