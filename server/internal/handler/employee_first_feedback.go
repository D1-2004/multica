package handler

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/employeeentry"
	"github.com/multica-ai/multica/server/internal/scene"
	"github.com/multica-ai/multica/server/internal/service/dingtalkresponse"
	"github.com/multica-ai/multica/server/internal/service/employeeloop"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

var firstFeedbackNamespace = uuid.MustParse("28e0f78f-5856-49de-aef6-d8ea326edbdb")

// Use the already chosen discovery tool version, not a second readiness read.
// Old inputs, automation and ambiguous multi-source windows keep their wire bytes.
func employeeConfigureFirstFeedback(cfg *employeeloop.Config, job employeeentry.Job, messages []employeeSourceMessage, envelopes []employeeDispatchEnvelope) {
	if job.Kind != employeeentry.KindMessage || len(messages) != 1 || messages[0].RequesterRef == "" || messages[0].SourceRef == "" || messages[0].Message.Reaction != nil || strings.TrimSpace(messages[0].Message.Text) == "" {
		return
	}
	addressed := false
	for i, item := range job.Items {
		if i < len(envelopes) && item.ReceiptID == messages[0].ReceiptID {
			addressed = employeeParticipationAddressed(envelopes[i], messages[0])
		}
	}
	if !addressed {
		return
	}
	for _, env := range envelopes {
		if env.Command.Continuation != nil {
			return
		}
	}
	discovery := false
	for _, tool := range cfg.Tools {
		discovery = discovery || tool.Name == "find_tasks"
	}
	if !discovery {
		return
	}
	cfg.StreamedFeedback = true
	cfg.Tools = append(cfg.Tools, employeeFirstFeedbackTool())
	cfg.Persona.DecisionRules += "\nPUBLIC FIRST-REQUEST FEEDBACK: Only when this current human request needs an earlier Task lookup, emit first_feedback first with one short natural public sentence describing what you are about to check, then the necessary find_tasks/read_task calls in the same response. This uses the same model request, never an extra round. Do not claim work was executed, accepted or finished. Direct answers, strict number/JSON formats, dispatch/stop/steer acknowledgments and quiet decisions need no feedback. Never expose reasoning, private material, code or tool syntax. The feedback is nonterminal and never replaces the eventual answer or failure."
}

func employeeFirstFeedbackTool() employeeloop.Tool {
	return employeeloop.Tool{Name: "first_feedback", Effect: true, Description: "On the first model request only, publish one short natural sentence about your intended lookup of this human's Task before a necessary find_tasks/read_task plan. This is public intent, never a work execution receipt, completed result or acceptance ACK. Use exactly one source. Do not use for direct answers, dispatch/stop/steer ACKs, strict number/JSON output, quiet, reactions or automation. No tool names, JSON, code or private details. Continue the same model response with the necessary read plan; this never finishes the request.", Schema: map[string]any{"type": "object", "properties": map[string]any{"source_ref": map[string]any{"type": "string"}, "text": map[string]any{"type": "string", "maxLength": 80}, "intent": map[string]any{"type": "string", "enum": []string{"lookup"}}}, "required": []string{"source_ref", "text", "intent"}, "additionalProperties": false}}
}
func firstFeedbackRefused(reason string) error {
	return errors.Join(employeeloop.ErrToolRefused, errors.New(reason))
}
func firstFeedbackText(args map[string]any) (string, error) {
	text, err := argument(args, "text")
	if err != nil {
		return "", firstFeedbackRefused("public feedback text required")
	}
	text = strings.TrimSpace(text)
	if len(args) != 3 || args["intent"] != "lookup" || !utf8.ValidString(text) || utf8.RuneCountInString(text) > 80 || strings.ContainsAny(text, "{}[]`\r\n") || strings.Contains(text, "<tool") {
		return "", firstFeedbackRefused("feedback must be one bounded public sentence, not tool syntax or code")
	}
	for _, name := range []string{"first_feedback", "find_tasks", "read_task", "dispatch_task", "tool_call", "source_ref"} {
		if strings.Contains(strings.ToLower(text), name) {
			return "", firstFeedbackRefused("feedback must not expose tool syntax")
		}
	}
	for _, r := range text {
		if unicode.IsControl(r) {
			return "", firstFeedbackRefused("feedback contains control characters")
		}
	}
	return text, nil
}

// The persisted ordinal-zero request enables a streamed frame. A pending
// response is expected; ordinary tools still wait for its completion.
func (h *employeeSceneHost) firstFeedbackEligible(ctx context.Context, tx pgx.Tx, call employeeloop.ToolCall) error {
	if h.job.Kind != employeeentry.KindMessage {
		return firstFeedbackRefused("feedback requires a human message wake")
	}
	count := 0
	for i, item := range h.job.Items {
		for _, s := range employeeSourceMessages(item, h.envelopes[i]) {
			if s.RequesterRef != "" && s.SourceRef != "" {
				count++
			}
		}
	}
	if count != 1 {
		return firstFeedbackRefused("feedback requires exactly one frozen source")
	}
	var journal []employeeentry.ModelTurn
	var attempts int
	var state string
	var unfinished bool
	if err := tx.QueryRow(ctx, `SELECT state,outcome IS NULL,model_journal,model_attempts FROM employee_scene_job WHERE id=$1::uuid`, h.job.ID).Scan(&state, &unfinished, &journal, &attempts); err != nil {
		return err
	}
	if state != "running" || !unfinished || len(journal) != 1 || attempts != 1 || journal[0].Failure != "" {
		return firstFeedbackRefused("feedback is only allowed in the first unfinished model request")
	}
	var request struct {
		Tools []struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
	}
	if json.Unmarshal(journal[0].Request, &request) != nil {
		return firstFeedbackRefused("first model request is invalid")
	}
	offered := false
	for _, t := range request.Tools {
		offered = offered || t.Function.Name == "first_feedback"
	}
	if !offered {
		return firstFeedbackRefused("feedback was not offered in the frozen first request")
	}
	if len(journal[0].Response) > 0 {
		var response struct {
			Choices []struct {
				Message struct {
					Calls []struct {
						ID       string `json:"id"`
						Function struct {
							Name string `json:"name"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"message"`
			} `json:"choices"`
		}
		if json.Unmarshal(journal[0].Response, &response) != nil {
			return firstFeedbackRefused("first response is invalid")
		}
		matched := false
		for _, choice := range response.Choices {
			for _, c := range choice.Message.Calls {
				matched = matched || (c.ID == call.NativeToolCallID && c.Function.Name == call.Name)
			}
		}
		if !matched {
			return firstFeedbackRefused("feedback native call is not from first model response")
		}
	}
	return nil
}

func (h *employeeSceneHost) firstFeedback(ctx context.Context, tx pgx.Tx, source employeeSourceMessage, env employeeDispatchEnvelope, call employeeloop.ToolCall) (employeeloop.ToolResult, error) {
	text, err := firstFeedbackText(call.Arguments)
	if err != nil {
		return employeeloop.ToolResult{}, err
	}
	if err = h.firstFeedbackEligible(ctx, tx, call); err != nil {
		return employeeloop.ToolResult{}, err
	}
	if source.Message.Reaction != nil || strings.TrimSpace(source.Message.Text) == "" || env.Command.Continuation != nil || !employeeParticipationAddressed(env, source) {
		return employeeloop.ToolResult{}, firstFeedbackRefused("feedback requires a current addressed human request")
	}
	env, err = h.currentTaskSource(ctx, tx, source)
	if err != nil {
		return employeeloop.ToolResult{}, err
	}
	registered, err := employeeSceneFence(ctx, &Handler{Queries: db.New(tx)}, h.job)
	if err != nil {
		return employeeloop.ToolResult{}, err
	}
	if h.worker.handler.DingTalkResponses == nil || env.Command.ExternalIdentity.DWS == nil {
		return employeeloop.ToolResult{}, firstFeedbackRefused("feedback outbox unavailable")
	}
	id := uuid.NewSHA1(firstFeedbackNamespace, []byte(h.job.ID+"\x00"+source.ReceiptID)).String()
	var oldText, oldCall string
	err = tx.QueryRow(ctx, `SELECT text,native_call_id FROM employee_first_feedback WHERE job_id=$1::uuid AND receipt_id=$2::uuid`, h.job.ID, source.ReceiptID).Scan(&oldText, &oldCall)
	if err == nil {
		if text != oldText || oldCall != call.NativeToolCallID {
			return employeeloop.ToolResult{}, firstFeedbackRefused("first feedback key conflicts with its accepted content or call")
		}
		return firstFeedbackResult(id), nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return employeeloop.ToolResult{}, errors.Join(employeeloop.ErrToolRefused, err)
	}
	command := env.Command
	in := dingtalkresponse.ActionInput{WorkspaceID: h.job.Scope.WorkspaceID, AgentID: h.job.Scope.AgentID, DWSUID: command.ExternalIdentity.DWS.UID, DWSOrgID: h.job.Scope.TenantOrgID, SceneID: h.job.Scope.SceneID, ConversationID: registered.ExternalSceneID, IsGroup: registered.SceneKind == scene.KindGroup, SenderOpenDingTalkID: firstNonEmpty(source.Message.SenderOpenDingTalkID, command.Event.Data.Sender.OpenDingTalkID, command.Event.Data.Sender.SenderOpenDingTalkID), DWSEnvironment: commandDWSEnvironment(command), Text: text}
	if command.ResponsePolicy != nil {
		in.ShowAITag = command.ResponsePolicy.ShowAITag
	}
	actionID, err := h.worker.handler.DingTalkResponses.EnqueueFirstFeedback(ctx, tx, in, h.job.ID, source.ReceiptID, source.SourceRef)
	if err != nil {
		return employeeloop.ToolResult{}, errors.Join(employeeloop.ErrToolRefused, err)
	}
	var actionInput []byte
	if err = tx.QueryRow(ctx, `SELECT input FROM response_action WHERE id=$1`, actionID).Scan(&actionInput); err != nil {
		return employeeloop.ToolResult{}, errors.Join(employeeloop.ErrToolRefused, err)
	}
	_, err = tx.Exec(ctx, `INSERT INTO employee_first_feedback(id,workspace_id,agent_id,tenant_org_id,scene_id,job_id,receipt_id,source_ref,requester_ref,principal_id,native_call_id,text,action_id,action_input) VALUES($1::uuid,$2::uuid,$3::uuid,$4,$5::uuid,$6::uuid,$7::uuid,$8,$9,$10::uuid,$11,$12,$13,$14::jsonb)`, id, h.job.Scope.WorkspaceID, h.job.Scope.AgentID, h.job.Scope.TenantOrgID, h.job.Scope.SceneID, h.job.ID, source.ReceiptID, source.SourceRef, source.RequesterRef, env.PrincipalID, call.NativeToolCallID, text, actionID, actionInput)
	if err != nil {
		return employeeloop.ToolResult{}, errors.Join(employeeloop.ErrToolRefused, err)
	}
	return firstFeedbackResult(id), nil
}
func firstFeedbackResult(id string) employeeloop.ToolResult {
	return employeeloop.ToolResult{Content: `{"state":"enqueued","meaning":"public_lookup_intent_only"}`, Receipt: "first-feedback:" + id}
}
func (h *employeeSceneHost) firstFeedbackReplay(ctx context.Context, tx pgx.Tx, source employeeSourceMessage, call employeeloop.ToolCall, raw json.RawMessage) (json.RawMessage, error) {
	if _, err := h.currentTaskSource(ctx, tx, source); err != nil {
		return nil, err
	}
	var matches bool
	text, err := firstFeedbackText(call.Arguments)
	if err != nil {
		return nil, err
	}
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM employee_first_feedback WHERE job_id=$1::uuid AND receipt_id=$2::uuid AND source_ref=$3 AND requester_ref=$4 AND native_call_id=$5 AND text=$6)`, h.job.ID, source.ReceiptID, source.SourceRef, source.RequesterRef, call.NativeToolCallID, text).Scan(&matches)
	if err != nil {
		return nil, err
	}
	if !matches {
		return nil, firstFeedbackRefused("feedback replay binding removed or mismatched")
	}
	return raw, nil
}

func supersedeEmployeeFirstFeedback(ctx context.Context, tx pgx.Tx, jobID string) error {
	// Only unsubmitted actions are cancelled. Unknown/accepted stay auditable.
	_, err := tx.Exec(ctx, `UPDATE employee_first_feedback f SET state='suppressed',reason='final_resolved',updated_at=now() FROM response_action a WHERE f.job_id=$1::uuid AND f.action_id=a.id AND a.state='pending'`, jobID)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE response_action a SET state='cancelled',error_code='host_send_suppressed:final_resolved',updated_at=now() FROM employee_first_feedback f WHERE f.job_id=$1::uuid AND f.action_id=a.id AND a.state='pending'`, jobID)
	return err
}

// Running feedback has its own gate. The ordinary completed-only gate stays.
func (h *Handler) beforeEmployeeFirstFeedbackSend(ctx context.Context, in dingtalkresponse.ActionInput) error {
	suppress := func(reason string) error { return &dingtalkresponse.SuppressSendError{Reason: reason} }
	database, ok := employeeEntryDB(h)
	if !ok {
		return errors.New("feedback authority unavailable")
	}
	tx, err := database.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	var job employeeentry.Job
	var sourceRef, receipt, requester, principal, state string
	var same, unfinished bool
	raw, err := json.Marshal(in)
	if err != nil {
		return err
	}
	err = tx.QueryRow(ctx, `SELECT j.id::text,j.kind,j.workspace_id::text,j.agent_id::text,j.tenant_org_id,j.scene_id::text,j.items,j.state,j.outcome IS NULL,f.source_ref,f.receipt_id::text,f.requester_ref,f.principal_id::text,f.state,f.action_input=$2::jsonb FROM employee_first_feedback f JOIN employee_scene_job j ON j.id=f.job_id WHERE f.action_id=$1`, in.ActionID, raw).Scan(&job.ID, &job.Kind, &job.Scope.WorkspaceID, &job.Scope.AgentID, &job.Scope.TenantOrgID, &job.Scope.SceneID, &job.Items, &job.State, &unfinished, &sourceRef, &receipt, &requester, &principal, &state, &same)
	if errors.Is(err, pgx.ErrNoRows) {
		return suppress("feedback_binding_removed")
	}
	if err != nil {
		return err
	}
	if !same || job.ID != in.EmployeeFirstFeedbackJobID || receipt != in.EmployeeFirstFeedbackReceiptID || sourceRef != in.EmployeeFirstFeedbackSourceRef || in.WorkspaceID != job.Scope.WorkspaceID || in.AgentID != job.Scope.AgentID || in.SceneID != job.Scope.SceneID || in.DWSOrgID != job.Scope.TenantOrgID {
		return suppress("feedback_action_mismatch")
	}
	if state != "enqueued" || job.Kind != employeeentry.KindMessage || job.State != "running" || !unfinished {
		return suppress("feedback_job_resolved")
	}
	envs := make([]employeeDispatchEnvelope, len(job.Items))
	for i, item := range job.Items {
		if json.Unmarshal(item.Payload, &envs[i]) != nil {
			return suppress("feedback_source_invalid")
		}
	}
	host := employeeSceneHost{worker: &EmployeeSceneWorker{handler: h}, job: job, envelopes: envs}
	source, env, err := host.source(sourceRef)
	if err != nil {
		return suppress("feedback_source_removed")
	}
	if source.ReceiptID != receipt || source.RequesterRef != requester || env.PrincipalID != principal || source.Message.Reaction != nil || !employeeParticipationAddressed(env, source) {
		return suppress("feedback_source_mismatch")
	}
	if _, err = host.currentTaskSource(ctx, tx, source); err != nil {
		return suppress("feedback_authority_revoked")
	}
	p, err := employeeReadParticipation(ctx, tx, job.Scope)
	if err != nil {
		return err
	}
	if p.Mode == "quiet" {
		return suppress("scene_participation_quiet")
	}
	return tx.Commit(ctx)
}
