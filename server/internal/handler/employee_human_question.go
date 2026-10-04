package handler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/employeeentry"
	"github.com/multica-ai/multica/server/internal/eventrouter"
	"github.com/multica-ai/multica/server/internal/humanquestion"
	"github.com/multica-ai/multica/server/internal/scene"
	"github.com/multica-ai/multica/server/internal/service/a2ui"
	"github.com/multica-ai/multica/server/internal/service/dingtalkresponse"
	"github.com/multica-ai/multica/server/internal/service/employeeloop"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const EmployeeHumanReplicaMarker = "[employee-human:1]"

var humanQuestionNamespace = uuid.MustParse("8f2e441c-37bd-4b92-a54d-bc10e4197031")

func (w *EmployeeSceneWorker) humanQuestionsReady(ctx context.Context) bool {
	if w == nil || w.HumanQuestionsReady == nil {
		return false
	}
	ready, err := w.HumanQuestionsReady(ctx)
	return err == nil && ready
}
func employeeHumanTools() []employeeloop.Tool {
	s := func() map[string]any { return map[string]any{"type": "string"} }
	option := map[string]any{"type": "object", "properties": map[string]any{"id": s(), "label": s(), "description": s()}, "required": []string{"id", "label"}, "additionalProperties": false}
	choice := map[string]any{"type": "object", "properties": map[string]any{"intent": map[string]any{"type": "string", "enum": []string{"clarify", "suggest"}}, "kind": map[string]any{"type": "string", "enum": []string{"single", "multiple", "person"}}, "question": s(), "options": map[string]any{"type": "array", "items": option, "minItems": 2, "maxItems": 20}, "allow_custom": map[string]any{"type": "boolean"}, "min": map[string]any{"type": "integer", "minimum": 0}, "max": map[string]any{"type": "integer", "minimum": 0}}, "required": []string{"intent", "kind", "question", "options"}, "additionalProperties": false}
	return []employeeloop.Tool{
		{Name: "a2ui_ask", Effect: true, Terminal: employeeloop.Waiting, Description: "Ask the current requester a focused single/multiple choice or frozen candidate clarification using an A2UI card, then end this round without blocking. Use only for an actual ambiguity or useful optional next step; do not dispatch the unresolved work. The Host owns all routing. People may ignore buttons and type ordinary words. No task is created merely to ask.", Schema: map[string]any{"type": "object", "properties": map[string]any{"source_ref": s(), "summary": s(), "choice": choice}, "required": []string{"source_ref", "summary", "choice"}, "additionalProperties": false}},
		{Name: "accept_human_response", Effect: true, Terminal: employeeloop.Quiet, Description: "Accept this current human message as an answer, new information or explicit change to one pending_human_questions entry. Bind the exact question_ref and answer_quote from the source's outer text. Preserve the full words and added constraints. With several plausible questions ask which; ordinary thanks, a question back or a new topic does not answer or cancel all old questions. The same Employee Loop will process the accepted response; do not also dispatch or continue in this round.", Schema: map[string]any{"type": "object", "properties": map[string]any{"source_ref": s(), "question_ref": s(), "intent": map[string]any{"type": "string", "enum": []string{"answer", "provide_info", "amend"}}, "selected": map[string]any{"type": "array", "items": s()}, "answer_quote": s()}, "required": []string{"source_ref", "question_ref", "intent", "answer_quote"}, "additionalProperties": false}},
	}
}

func (h *employeeSceneHost) stageHumanQuestion(ctx context.Context, tx pgx.Tx, source employeeSourceMessage, env employeeDispatchEnvelope, call employeeloop.ToolCall) (employeeloop.ToolResult, error) {
	if !h.worker.humanQuestionsReady(ctx) {
		return employeeloop.ToolResult{}, employeeloop.ErrToolRefused
	}
	var args struct {
		SourceRef string               `json:"source_ref"`
		Summary   string               `json:"summary"`
		Choice    humanquestion.Choice `json:"choice"`
	}
	raw, _ := json.Marshal(call.Arguments)
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&args) != nil || args.Choice.Validate() != nil || strings.TrimSpace(args.Summary) == "" {
		return employeeloop.ToolResult{}, fmt.Errorf("%w: invalid human question", employeeloop.ErrToolRefused)
	}
	if source.Message.Reaction != nil || source.Message.SenderOpenDingTalkID == "" || env.Command.ExternalIdentity.DWS == nil {
		return employeeloop.ToolResult{}, employeeloop.ErrToolRefused
	}
	registered, err := employeeSceneFence(ctx, &Handler{Queries: db.New(tx)}, h.job)
	if err != nil {
		return employeeloop.ToolResult{}, err
	}
	id := uuid.NewSHA1(humanQuestionNamespace, []byte(source.SourceRef+"/"+call.NativeToolCallID))
	q := humanquestion.Question{ID: id.String(), Scope: h.job.Scope, PrincipalID: env.PrincipalID, SourceJobID: h.job.ID, SourceReceiptID: source.ReceiptID, SourceRef: source.SourceRef, RequesterRef: source.RequesterRef, OperatorOpenID: source.Message.SenderOpenDingTalkID, Version: 1, Summary: args.Summary, Choice: args.Choice.Normalized()}
	in := dingtalkresponse.ActionInput{WorkspaceID: q.Scope.WorkspaceID, AgentID: q.Scope.AgentID, DWSUID: env.Command.ExternalIdentity.DWS.UID, DWSOrgID: q.Scope.TenantOrgID, SceneID: q.Scope.SceneID, ConversationID: registered.ExternalSceneID, SenderOpenDingTalkID: q.OperatorOpenID, IsGroup: registered.SceneKind == scene.KindGroup, DWSEnvironment: commandDWSEnvironment(env.Command), Text: q.Summary}
	if _, err = h.worker.handler.stageEmployeeHumanQuestion(ctx, tx, &q, in); err != nil {
		return employeeloop.ToolResult{}, err
	}
	return employeeloop.ToolResult{Content: "Question and card delivery intent recorded; this round may finish. No background work was started.", Receipt: "human-question:" + q.ID, Terminal: &employeeloop.Decision{Kind: employeeloop.Waiting}}, nil
}

func (h *Handler) stageEmployeeHumanQuestion(ctx context.Context, tx pgx.Tx, q *humanquestion.Question, in dingtalkresponse.ActionInput) (string, error) {
	kind := a2ui.KindConfirm
	if q.Choice.Kind == "multiple" {
		kind = a2ui.KindChoose
	}
	options := make([]a2ui.Option, 0, len(q.Choice.Options))
	for _, o := range q.Choice.Options {
		options = append(options, a2ui.Option{Label: o.Label, Description: o.Description})
	}
	custom := q.Choice.AllowCustom
	binding, err := readEmployeeQuestionSource(ctx, tx, *q)
	if err != nil {
		return "", err
	}
	row, messages, err := a2ui.New(db.New(tx)).Stage(ctx, uuid.MustParse(q.ID), a2ui.OpenRequest{WorkspaceID: uuid.MustParse(q.Scope.WorkspaceID), AgentID: uuid.MustParse(q.Scope.AgentID), SenderUID: in.DWSUID, SenderOrgID: q.Scope.TenantOrgID, SceneID: q.Scope.SceneID, ConversationID: in.ConversationID, SourceRef: q.SourceRef, Kind: kind, EmployeeCompact: true, SourceQuote: binding.Source.Message.Text, Header: humanQuestionHeader(q.Summary), Question: q.Choice.Question, Options: options, AllowCustom: &custom, OperatorUID: q.OperatorOpenID, IdempotencyKey: "employee-human:" + q.ID})
	if err != nil {
		return "", err
	}
	q.PublicID = row.PublicID
	action, err := h.DingTalkResponses.EnqueueA2UIQuestion(ctx, tx, in, q.ID, q.PublicID, messages)
	if err != nil {
		return "", err
	}
	q.ActionID = action
	if _, err = humanquestion.StageTx(ctx, tx, *q); err != nil {
		return "", err
	}
	return action, nil
}

func (h *employeeSceneHost) acceptHumanText(ctx context.Context, tx pgx.Tx, source employeeSourceMessage, call employeeloop.ToolCall) (employeeloop.ToolResult, error) {
	if !h.worker.humanQuestionsReady(ctx) || source.Message.Reaction != nil {
		return employeeloop.ToolResult{}, employeeloop.ErrToolRefused
	}
	ref, err := argument(call.Arguments, "question_ref")
	if err != nil {
		return employeeloop.ToolResult{}, err
	}
	intent, err := argument(call.Arguments, "intent")
	if err != nil {
		return employeeloop.ToolResult{}, err
	}
	if intent == "cancel" {
		return employeeloop.ToolResult{}, fmt.Errorf("%w: cancellation must use the current source-bound stop_task tool; do not consume this question", employeeloop.ErrToolRefused)
	}
	quote, err := argument(call.Arguments, "answer_quote")
	if err != nil {
		return employeeloop.ToolResult{}, err
	}
	selected, err := optionalStringArray(call.Arguments, "selected")
	if err != nil {
		return employeeloop.ToolResult{}, err
	}
	eventID := source.SourceRef + "/" + ref
	response := humanquestion.Response{ID: uuid.NewSHA1(humanQuestionNamespace, []byte("response/text/"+eventID)).String(), QuestionID: ref, EventID: eventID, Surface: "chat_text", RequesterRef: source.RequesterRef, Intent: intent, Selected: selected, RawText: source.Message.Text, EvidenceQuote: quote}
	_, r, err := humanquestion.AcceptTx(ctx, tx, h.job.Scope, response, h.worker.handler.admitEmployeeHumanResponseTx)
	if err != nil {
		return employeeloop.ToolResult{}, fmt.Errorf("%w: %v", employeeloop.ErrToolRefused, err)
	}
	return employeeloop.ToolResult{Content: "Human words and a unique response wake recorded.", Receipt: "human-response:" + r.ID, Terminal: &employeeloop.Decision{Kind: employeeloop.Quiet}}, nil
}

func (h *Handler) admitEmployeeHumanResponseTx(ctx context.Context, tx pgx.Tx, q humanquestion.Question, r *humanquestion.Response) error {
	database, ok := employeeEntryDB(&Handler{TxStarter: tx})
	if !ok {
		return humanquestion.ErrInvalid
	}
	b, err := readEmployeeQuestionSource(ctx, database, q)
	if err != nil {
		return err
	}
	owner := scene.Owner{WorkspaceID: parseUUID(q.Scope.WorkspaceID), AgentID: parseUUID(q.Scope.AgentID)}
	raw, _ := json.Marshal(r)
	sum := sha256.Sum256(raw)
	host := eventrouter.Host{Owner: owner, PrincipalID: parseUUID(q.PrincipalID), TenantOrgID: q.Scope.TenantOrgID, Locator: scene.Locator{Provider: b.Registered.Provider, TenantOrgID: q.Scope.TenantOrgID, Namespace: b.Registered.SourceNamespace, Kind: b.Registered.SceneKind, ExternalID: b.Registered.ExternalSceneID}, Route: eventrouter.Unified, ConfigVersion: employeeentry.HumanResponsePayloadSchema, Fingerprint: hex.EncodeToString(sum[:]), ExistingSceneOnly: true}
	event := eventrouter.Event{Version: 1, ID: r.EventID, Source: "employee.human_response/" + r.Surface, Type: "human.response", Category: eventrouter.Control, PayloadSchema: employeeentry.HumanResponsePayloadSchema, Payload: raw}
	receipt, _, err := eventrouter.Admit(ctx, tx, event, host)
	if err != nil {
		return err
	}
	job, _, err := h.EmployeeSceneWorker.store.AdmitHumanResponseTx(ctx, tx, employeeentry.HumanResponseAdmission{Scope: q.Scope, PrincipalID: q.PrincipalID, ReceiptID: uuidToString(receipt.ID), Response: employeeentry.HumanResponse{QuestionRef: q.ID, ResponseRef: r.ID, Version: q.Version}})
	if err != nil {
		return err
	}
	r.ReceiptID, r.JobID = uuidToString(receipt.ID), job.ID
	return h.stageEmployeeHumanCardProjectionTx(ctx, tx, q, r)
}

const employeeHumanQuestionFraming = `HUMAN INTERACTION: Ask one short, direct question (prefer no more than 24 Chinese characters), with 2–4 brief parallel choices when appropriate. Do not restate the question in summary or enumerate option descriptions again. The card renders a Host-bound source quote and choices; ordinary chat is available for extra words, so do not ask for content and format together unless both block this next step. When the current human asks to choose options or clarify a recipient before work starts, or required input is missing before any authorized preparation can begin, use a2ui_ask here in Employee and end this round. Do not dispatch that unresolved work to Pi just to ask the human. dispatch_task.follow_up_steps are automatically runnable authorized steps, never a human-input wait; never put "ask, wait for human, then assume they answered" into a plan. No Task is needed merely to ask. A current question about the options does not accept them: explain or clarify without dispatch. The selected scope, synthetic materials, method and no-send/no-contact constraints come from the actual source, never add real-document searches or file sending that it did not request. If preparation itself is authorized before the missing answer, its completed Pi round may later emit the strict clarification result; no live sandbox session waits for the human.
HUMAN ANSWERS: Buttons are shortcuts. A current message may answer, provide new information, change the request, ask back, or start another topic. Use accept_human_response only when the actual outer wording clearly addresses one listed pending_human_questions entry; preserve all words and added constraints. Explicit quote identifies the question; otherwise resolve from meaning and conversation, never choose the latest Task by default. If ambiguous ask which. New topics/thanks do not satisfy or cancel all pending questions. An old question never overrides a newer explicit request.`

func (w *EmployeeSceneWorker) appendHumanQuestions(ctx context.Context, job employeeentry.Job, envelopes []employeeDispatchEnvelope, input *employeeSavedInput) error {
	if !w.humanQuestionsReady(ctx) {
		return nil
	}
	input.Config.Persona.Instructions += "\n\n" + employeeHumanQuestionFraming
	database, ok := employeeEntryDB(w.handler)
	if !ok {
		return humanquestion.ErrInvalid
	}
	all := map[string]humanquestion.Question{}
	for i, env := range envelopes {
		for _, source := range employeeSourceMessages(job.Items[i], env) {
			questions, err := humanquestion.NewStore(database).Pending(ctx, job.Scope, source.RequesterRef)
			if err != nil {
				return err
			}
			for _, q := range questions {
				all[q.ID] = q
			}
		}
	}
	if len(all) == 0 {
		return nil
	}
	questions := []humanquestion.Question{}
	for _, q := range all {
		questions = append(questions, q)
	}
	raw, _ := json.Marshal(map[string]any{"pending_human_questions": questions})
	input.Input.TaskBrief += "\n\n" + string(raw)
	return nil
}

// BeforeEmployeeHumanQuestionSend rechecks the frozen source immediately before
// a new submission; terminal questions suppress an obsolete pending card.
func (h *Handler) BeforeEmployeeHumanQuestionSend(ctx context.Context, in dingtalkresponse.ActionInput) error {
	if in.A2UICard == nil {
		return nil
	}
	if !h.EmployeeSceneWorker.humanQuestionsReady(ctx) {
		return errors.New("human question readers are not ready")
	}
	database, ok := employeeEntryDB(h)
	if !ok {
		return humanquestion.ErrInvalid
	}
	scope := employeeentry.Scope{WorkspaceID: in.WorkspaceID, AgentID: in.AgentID, TenantOrgID: in.DWSOrgID, SceneID: in.SceneID}
	tx, err := database.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = humanquestion.LockScope(ctx, tx, scope); err != nil {
		return err
	}
	q, err := humanquestion.NewStore(tx).Get(ctx, scope, in.A2UICard.QuestionID)
	if err != nil {
		return err
	}
	if q.ActionID != in.ActionID || q.PublicID != in.A2UICard.PublicID {
		return humanquestion.ErrForbidden
	}
	if q.State != "open" && q.State != "deferred" {
		return &dingtalkresponse.SuppressSendError{Reason: "human_question_closed"}
	}
	if err = humanquestion.CurrentTargetTx(ctx, tx, q); errors.Is(err, humanquestion.ErrStale) {
		return &dingtalkresponse.SuppressSendError{Reason: "human_question_target_changed"}
	} else if err != nil {
		return err
	}
	// Round-end questions are existing authorized background notices; a
	// foreground question must obey a subsequently accepted scene pause.
	if q.TaskID == "" {
		quiet, e := employeeHumanQuiet(ctx, tx, scope)
		if e != nil {
			return e
		}
		if quiet {
			return &dingtalkresponse.SuppressSendError{Reason: "scene_participation_quiet"}
		}
	}
	if _, err = readEmployeeQuestionSource(ctx, tx, q); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func mapNativeHumanAnswer(q humanquestion.Question, got a2ui.NativeAnswer) (humanquestion.Response, error) {
	r := humanquestion.Response{ID: uuid.NewSHA1(humanQuestionNamespace, []byte("response/card/"+got.EventID)).String(), QuestionID: q.ID, EventID: got.EventID, Surface: "a2ui_action", RequesterRef: q.RequesterRef, Intent: "answer", RawText: got.Custom}
	if got.Outcome == "skipped" {
		r.Intent = "skip"
	}
	for _, id := range got.Selected {
		if !strings.HasPrefix(id, "o") {
			return r, humanquestion.ErrInvalid
		}
		i, err := strconv.Atoi(strings.TrimPrefix(id, "o"))
		if err != nil || i < 0 || i >= len(q.Choice.Options) || id != "o"+strconv.Itoa(i) {
			return r, humanquestion.ErrInvalid
		}
		r.Selected = append(r.Selected, q.Choice.Options[i].ID)
	}
	return r, q.ValidateResponse(r)
}

func humanQuestionHeader(summary string) string {
	if len([]rune(summary)) <= 120 {
		return summary
	}
	return "请确认下一步"
}
