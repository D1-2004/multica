package inboundcoord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/multica-ai/multica/server/internal/langfuse"
	"unicode/utf8"

	"github.com/multica-ai/multica/server/internal/service/userdecision"
	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/shared"
)

// UserDecisionSnapshot is immutable evidence for the single question belonging
// to this inbound run. It is not a committed execution checkpoint.
type UserDecisionSnapshot struct {
	TraceRootSpanID          string                                   `json:"trace_root_span_id,omitempty"`
	ProposalPromptHash       string                                   `json:"proposal_prompt_hash,omitempty"`
	ProposalReviewPromptHash string                                   `json:"proposal_review_prompt_hash,omitempty"`
	DecisionID               string                                   `json:"decision_id,omitempty"`
	Turn                     Turn                                     `json:"turn"`
	Messages                 []openai.ChatCompletionMessageParamUnion `json:"messages"`
	Recalls                  []recallCall                             `json:"recalls"`
	RecalledIDs              []string                                 `json:"recalled_ids"`
	Recommended              Decision                                 `json:"recommended"`
	ProposalPolicy           PolicyModuleManifest                     `json:"proposal_policy"`
	Policy                   PolicyManifest                           `json:"policy"`
	ProposalReviews          []json.RawMessage                        `json:"proposal_reviews,omitempty"`
	ProposalAttempts         []string                                 `json:"proposal_attempts,omitempty"`
	Proposal                 userdecision.Proposal                    `json:"proposal"`
}

func (c *Coordinator) proposeUserDecision(ctx context.Context, turn Turn, messages []openai.ChatCompletionMessageParamUnion, recalls []recallCall, recalled map[string]struct{}, recommended Decision) (Decision, error) {
	// Dispatch context carries callback credentials and is not model or dataset evidence.
	turn.IssueDispatchContext = nil
	// Tool transcripts can end in an unresolved finish call. Start a separate
	// request with the entire transcript as immutable evidence, not tool replies.
	evidence, err := json.Marshal(struct {
		Turn        Turn
		Messages    []openai.ChatCompletionMessageParamUnion
		Recommended Decision
	}{turn, messages, recommended})
	if err != nil {
		return Decision{}, err
	}
	params := windowPlanToolFor(turn, true).OfFunction.Function.Parameters
	option := map[string]any{"type": "object", "additionalProperties": false, "required": []string{"id", "label", "kind", "plan"}, "properties": map[string]any{"id": map[string]any{"type": "string"}, "label": map[string]any{"type": "string"}, "kind": map[string]any{"type": "string", "enum": []string{"continue_work", "start_work", "reply"}}, "plan": params}}
	schema := shared.FunctionParameters{"type": "object", "additionalProperties": false, "required": []string{"question", "options", "recommended_id"}, "properties": map[string]any{"question": map[string]any{"type": "string"}, "options": map[string]any{"type": "array", "minItems": 2, "maxItems": 5, "items": option}, "recommended_id": map[string]any{"type": "string"}}}
	proposalMessages := []openai.ChatCompletionMessageParamUnion{openai.SystemMessage(buildSystemPromptForStage(turn, true) + "\n" + userDecisionPolicy()), openai.UserMessage(string(evidence))}
	var p userdecision.Proposal
	var attempts []string
	var reviews []json.RawMessage
	var validationErr error
	for attempt := 0; attempt < 3; attempt++ {
		generation := startUserDecisionGeneration(ctx, "coordinator.propose_choices", c.configuredModel(), proposalMessages, 4096)
		response, callErr := c.completeWithLimit(ctx, proposalMessages, []openai.ChatCompletionToolUnionParam{openai.ChatCompletionFunctionTool(shared.FunctionDefinitionParam{Name: "propose_choices", Parameters: schema})}, 4096, temperature)
		endRoundGeneration(generation, response, callErr)
		if callErr != nil {
			return Decision{}, callErr
		}
		if response == nil || len(response.Choices) != 1 || len(response.Choices[0].Message.ToolCalls) != 1 {
			return Decision{}, errors.New("missing user decision proposal")
		}
		call := response.Choices[0].Message.ToolCalls[0].Function
		if call.Name != "propose_choices" {
			return Decision{}, errors.New("unexpected user decision tool")
		}
		attempts = append(attempts, call.Arguments)
		p = userdecision.Proposal{}
		validationErr = json.Unmarshal([]byte(call.Arguments), &p)
		if validationErr == nil {
			validationErr = p.Validate()
		}
		if validationErr == nil {
			for i := range p.Options {
				o := p.Options[i]
				d, parseErr := parseValidatedWindowPlan(string(o.Plan), turn, recalls, recalled)
				if parseErr != nil {
					validationErr = fmt.Errorf("candidate %s: %w", o.ID, parseErr)
					break
				}
				// The exact model-authored reply is the visible choice, never a
				// potentially misleading model-authored summary of that reply.
				if o.Kind == "reply" && d.Action == ActionReply && d.UserText != "" {
					o.Label = "直接回复：“" + d.UserText + "”"
					p.Options[i] = o
				}
				if directionErr := validateChoiceDirection(o, d); directionErr != nil {
					validationErr = fmt.Errorf("candidate %s kind=%s: %w; plan action kinds=%v", o.ID, o.Kind, directionErr, decisionOptionActionKinds(d))
					break
				}
			}
		}
		if validationErr == nil {
			validationErr = p.Validate()
		}
		if validationErr == nil {
			review, reviewErr := c.reviewUserDecisionProposal(ctx, turn, evidence, p)
			if len(review) > 0 {
				reviews = append(reviews, review)
			}
			validationErr = reviewErr
		}
		if validationErr == nil {
			break
		}
		feedback, _ := json.Marshal(map[string]any{"proposal_validation_error": validationErr.Error(), "previous_proposal": call.Arguments})
		proposalMessages = append(proposalMessages, openai.UserMessage(string(feedback)))
	}
	if validationErr != nil {
		return Decision{}, validationErr
	}
	p.Model = c.configuredModel()
	p.RawOutput = attempts[len(attempts)-1]
	snapshot := &UserDecisionSnapshot{TraceRootSpanID: langfuse.TraceFromContext(ctx).RootSpanID(), ProposalPromptHash: policyHash(proposalMessages[0].OfSystem.Content.OfString.Value), ProposalReviewPromptHash: policyHash(userDecisionPolicy()), Turn: turn, Messages: messages, Recalls: recalls, RecalledIDs: sortedCopy(turn.recalledIssueIDs), Recommended: recommended, Policy: policyManifestForStage(turn, len(recalled) > 0), Proposal: p, ProposalAttempts: attempts, ProposalReviews: reviews}
	for _, module := range coordinatorPolicy.Modules {
		if module.ID == "user_decision" {
			snapshot.ProposalPolicy = PolicyModuleManifest{ID: module.ID, Version: module.Version, Hash: module.ContentHash}
		}
	}
	return Decision{Action: ActionAwaitUser, UserText: p.Question, Reason: "awaiting_initiator", UserDecision: snapshot}, nil
}

// Review the frozen alternatives before display; this does not choose for the user.
func (c *Coordinator) reviewUserDecisionProposal(ctx context.Context, turn Turn, evidence []byte, proposal userdecision.Proposal) (json.RawMessage, error) {
	replyReview, err := c.reviewUserDecisionProposalPart(ctx, evidence, proposal, false)
	if err != nil {
		return replyReview, err
	}
	for _, option := range proposal.Options {
		if option.Kind == "continue_work" {
			labels, labelErr := c.reviewUserDecisionProposalPart(ctx, evidence, proposal, true)
			var combined map[string]any
			if json.Unmarshal(labels, &combined) == nil {
				combined["reply_review"] = json.RawMessage(replyReview)
				labels, _ = json.Marshal(combined)
			}
			return labels, labelErr
		}
	}
	return replyReview, nil
}

func (c *Coordinator) reviewUserDecisionProposalPart(ctx context.Context, evidence []byte, proposal userdecision.Proposal, taskLabels bool) (json.RawMessage, error) {
	var reply *userdecision.Option
	for i := range proposal.Options {
		if proposal.Options[i].Kind == "reply" {
			reply = &proposal.Options[i]
			break
		}
	}
	if reply == nil {
		return nil, errors.New("reply candidate missing")
	}
	inputData := map[string]any{"evidence": json.RawMessage(evidence), "reply_candidate": reply}
	toolName := "review_choices"
	if taskLabels {
		options := []userdecision.Option{}
		for _, option := range proposal.Options {
			if option.Kind == "continue_work" {
				options = append(options, option)
			}
		}
		inputData = map[string]any{"evidence": json.RawMessage(evidence), "continuation_options": options}
		toolName = "review_task_labels"
	}
	input, err := json.Marshal(inputData)
	if err != nil {
		return nil, err
	}
	messages := []openai.ChatCompletionMessageParamUnion{openai.SystemMessage(userDecisionPolicy()), openai.UserMessage(string(input))}
	required := []string{"allowed", "reason"}
	if taskLabels {
		required = append(required, "labels_match_targets")
	}
	tool := openai.ChatCompletionFunctionTool(shared.FunctionDefinitionParam{Name: toolName, Parameters: shared.FunctionParameters{
		"type": "object", "additionalProperties": false, "required": required, "properties": map[string]any{
			"allowed": map[string]any{"type": "boolean"}, "reason": map[string]any{"type": "string"},
			"labels_match_targets": map[string]any{"type": "boolean", "description": "False if any visible option misidentifies its actual task, even when issue_id exists. A label naming task B while targeting task A is false. An explicit change of purpose that names task A correctly is valid. True only when every label is grounded in the mapped task and candidate action."},
		},
	}})
	generation := startUserDecisionGeneration(ctx, "coordinator."+toolName, c.configuredModel(), messages, 1024)
	response, err := c.completeWithLimit(ctx, messages, []openai.ChatCompletionToolUnionParam{tool}, 1024, temperature)
	endRoundGeneration(generation, response, err)
	if err != nil {
		return nil, err
	}
	if response == nil || len(response.Choices) != 1 || len(response.Choices[0].Message.ToolCalls) != 1 || response.Choices[0].Message.ToolCalls[0].Function.Name != toolName {
		return nil, errors.New("missing candidate semantic review")
	}
	raw := json.RawMessage(response.Choices[0].Message.ToolCalls[0].Function.Arguments)
	var review struct {
		Allowed            *bool  `json:"allowed"`
		LabelsMatchTargets *bool  `json:"labels_match_targets"`
		Reason             string `json:"reason"`
	}
	if json.Unmarshal(raw, &review) != nil || review.Allowed == nil || (taskLabels && review.LabelsMatchTargets == nil) || review.Reason == "" {
		return raw, errors.New("invalid candidate semantic review")
	}
	if !*review.Allowed || (taskLabels && !*review.LabelsMatchTargets) {
		return raw, fmt.Errorf("candidate semantic review: %s", review.Reason)
	}
	return raw, nil
}

func validateChoiceDirection(o userdecision.Option, d Decision) error {
	work := 0
	for _, a := range d.CoordinationActions {
		if a.Kind == "start_work" || a.Kind == "continue_work" {
			work++
			if a.Kind != o.Kind {
				return errors.New("candidate direction differs from displayed option")
			}
		}
	}
	if o.Kind == "reply" {
		for _, a := range d.CoordinationActions {
			if a.Kind == "acknowledge" && a.AckKind == "receipt" {
				return errors.New("reply choice cannot be a work receipt; supply an evidence-supported standalone answer without committing to execution")
			}
		}
		if work != 0 || d.Action != ActionReply || d.UserText == "" {
			return errors.New("reply candidate must contain a concrete non-work reply")
		}
	} else if work != 1 {
		return errors.New("work candidate must have exactly one work target")
	}
	return nil
}

func userDecisionPolicy() string {
	for _, m := range coordinatorPolicy.Modules {
		if m.ID == "user_decision" {
			return policyModuleBody(m)
		}
	}
	panic("user decision policy is not registered")
}

// ResolveUserDecision interprets supplementary text once and rechecks the
// frozen candidate. It never regenerates the displayed choices or commits a
// checkpoint; the durable Host commits this result with the original job.
type UserDecisionResolutionAudit struct {
	InterpretationPolicy   *PolicyManifest `json:"interpretation_policy,omitempty"`
	TraceID                string          `json:"trace_id,omitempty"`
	Model                  string          `json:"model"`
	RawInterpretation      string          `json:"raw_interpretation,omitempty"`
	InterpretationAttempts []string        `json:"interpretation_attempts,omitempty"`
	Review                 any             `json:"review,omitempty"`
	ReviewAttempts         []any           `json:"review_attempts,omitempty"`
}

func (c *Coordinator) ResolveUserDecision(ctx context.Context, s UserDecisionSnapshot, submission userdecision.Submission) (Decision, error) {
	return c.resolveUserDecision(ctx, s, submission, &UserDecisionResolutionAudit{})
}
func (c *Coordinator) ResolveUserDecisionWithAudit(ctx context.Context, s UserDecisionSnapshot, submission userdecision.Submission) (Decision, UserDecisionResolutionAudit, error) {
	var audit UserDecisionResolutionAudit
	audit.TraceID = s.Turn.TraceID
	if langfuse.TraceIDHex(audit.TraceID) == "" {
		audit.TraceID = s.DecisionID
	}
	trace := c.startUserDecisionResolutionTrace(ctx, s, submission)
	ctx = langfuse.ContextWithTrace(ctx, trace)
	d, err := c.resolveUserDecision(ctx, s, submission, &audit)
	trace.End(langfuse.EndOptions{Output: map[string]any{"action": d.Action, "reason": d.Reason}, Err: err})
	return d, audit, err
}
func (c *Coordinator) resolveUserDecision(ctx context.Context, s UserDecisionSnapshot, submission userdecision.Submission, audit *UserDecisionResolutionAudit) (Decision, error) {
	snapshotCoordinator := *c
	snapshotCoordinator.model = c.configuredModel()
	audit.Model = snapshotCoordinator.model
	snapshotCoordinator.ModelProvider = nil
	c = &snapshotCoordinator
	turn := s.Turn
	turn.UserDecisionEnabled = false
	turn.UserDecisionSubmission = &submission
	recalled := map[string]struct{}{}
	for _, id := range s.RecalledIDs {
		recalled[id] = struct{}{}
	}
	turn.recalledIssueIDs = append([]string(nil), s.RecalledIDs...)
	var selected *userdecision.Option
	for _, o := range s.Proposal.Options {
		if o.ID == submission.OptionID {
			copy := o
			selected = &copy
			break
		}
	}
	if submission.OptionID != "" && selected == nil {
		return Decision{}, errors.New("unknown frozen option")
	}
	reviewPlan := func(raw json.RawMessage) (Decision, error) {
		decision, err := parseValidatedWindowPlan(string(raw), turn, s.Recalls, recalled)
		if err != nil {
			return Decision{}, err
		}
		if selected != nil {
			if err = validateChoiceDirection(*selected, decision); err != nil {
				return Decision{}, err
			}
			frozen, err := parseValidatedWindowPlan(string(selected.Plan), turn, s.Recalls, recalled)
			if err != nil {
				return Decision{}, err
			}
			if workTarget(frozen) != workTarget(decision) {
				return Decision{}, errors.New("supplementary text changed selected target")
			}
		}
		check, err := c.checkFinish(ctx, turn, decision, s.Messages, 0, map[string]finishCheckResult{})
		if err != nil {
			return Decision{}, err
		}
		audit.Review = check
		audit.ReviewAttempts = append(audit.ReviewAttempts, check)
		if check.Verdict != "allow" {
			return Decision{}, fmt.Errorf("submitted plan rejected: %s", check.Reason)
		}
		return decision, nil
	}
	var raw json.RawMessage
	if selected != nil {
		raw = selected.Plan
	}
	if submission.Custom != "" {
		evidence, err := json.Marshal(struct {
			Snapshot   UserDecisionSnapshot
			Submission userdecision.Submission
		}{s, submission})
		if err != nil {
			return Decision{}, err
		}
		planSchema := windowPlanToolFor(turn, true).OfFunction.Function.Parameters
		tool := openai.ChatCompletionFunctionTool(shared.FunctionDefinitionParam{Name: "interpret_submission", Parameters: shared.FunctionParameters{"type": "object", "required": []string{"executable", "reason", "plan"}, "properties": map[string]any{"executable": map[string]any{"type": "boolean"}, "reason": map[string]any{"type": "string"}, "plan": planSchema}}})
		latest, _ := json.Marshal(map[string]any{"user_decision_submission": submission})
		interpretMessages := []openai.ChatCompletionMessageParamUnion{openai.SystemMessage(userDecisionPolicy()), openai.UserMessage(string(evidence)), openai.UserMessage(string(latest))}
		policy := PolicyManifest{PolicyVersion: coordinatorPolicy.Version, AssemblyVersion: coordinatorPolicy.AssemblyVersion}
		for _, module := range coordinatorPolicy.Modules {
			if module.ID == "user_decision" {
				policy.Modules = []PolicyModuleManifest{{ID: module.ID, Version: module.Version, Hash: policyHash(policyModuleBody(module))}}
				policy.ActiveRuleIDs = append([]string(nil), module.OwnsRuleIDs...)
			}
		}
		actualPrompt := interpretMessages[0].OfSystem.Content.OfString.Value
		policy.PromptHash = policyHash(actualPrompt)
		policy.Characters = utf8.RuneCountInString(actualPrompt)
		audit.InterpretationPolicy = &policy
		for attempt := 0; attempt < 3; attempt++ {
			generation := startUserDecisionGeneration(ctx, "coordinator.interpret_submission", c.configuredModel(), interpretMessages, 2048)
			response, err := c.completeWithLimit(ctx, interpretMessages, []openai.ChatCompletionToolUnionParam{tool}, 2048, temperature)
			endRoundGeneration(generation, response, err)
			if err != nil {
				return Decision{}, err
			}
			if response == nil || len(response.Choices) != 1 || len(response.Choices[0].Message.ToolCalls) != 1 || response.Choices[0].Message.ToolCalls[0].Function.Name != "interpret_submission" {
				return Decision{}, errors.New("missing submission interpretation")
			}
			audit.RawInterpretation = response.Choices[0].Message.ToolCalls[0].Function.Arguments
			audit.InterpretationAttempts = append(audit.InterpretationAttempts, audit.RawInterpretation)
			var parsed struct {
				Executable bool            `json:"executable"`
				Reason     string          `json:"reason"`
				Plan       json.RawMessage `json:"plan"`
			}
			if err = json.Unmarshal([]byte(response.Choices[0].Message.ToolCalls[0].Function.Arguments), &parsed); err != nil {
				return Decision{}, err
			}
			if !parsed.Executable {
				return Decision{}, &userdecision.NonExecutableSubmissionError{Reason: parsed.Reason}
			}
			raw = parsed.Plan
			if decision, validationErr := reviewPlan(raw); validationErr == nil {
				return decision, nil
			} else {
				if attempt == 2 {
					return Decision{}, validationErr
				}
				feedback, _ := json.Marshal(map[string]any{"interpretation_validation_error": validationErr.Error(), "previous_interpretation": audit.RawInterpretation, "allowed_issue_ids": s.RecalledIDs})
				interpretMessages = append(interpretMessages, openai.UserMessage(string(feedback)))
			}
		}
	}
	if len(raw) == 0 {
		return Decision{}, errors.New("empty submission")
	}
	return reviewPlan(raw)
}
func workTarget(d Decision) string {
	for _, a := range d.CoordinationActions {
		if a.Kind == "continue_work" || a.Kind == "start_work" {
			return a.Kind + ":" + a.IssueID
		}
	}
	return "reply"
}

func decisionOptionActionKinds(d Decision) []string {
	kinds := make([]string, 0, len(d.CoordinationActions))
	for _, a := range d.CoordinationActions {
		kinds = append(kinds, a.Kind)
	}
	return kinds
}

func startUserDecisionGeneration(ctx context.Context, name, model string, input any, tokens int) *langfuse.Observation {
	trace := langfuse.TraceFromContext(ctx)
	if trace == nil {
		return nil
	}
	return trace.StartObservation(langfuse.ObservationOptions{Type: langfuse.TypeGeneration, Name: name, Model: model, Input: input, ModelParameters: map[string]any{"max_completion_tokens": tokens, "temperature": temperature}})
}
