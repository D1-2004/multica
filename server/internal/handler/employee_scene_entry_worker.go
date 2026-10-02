package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/employeeentry"
	"github.com/multica-ai/multica/server/internal/langfuse"
	"github.com/multica-ai/multica/server/internal/scene"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/service/dingtalkresponse"
	"github.com/multica-ai/multica/server/internal/service/employeeloop"
	"github.com/multica-ai/multica/server/internal/service/employeememory"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	openai "github.com/openai/openai-go/v3"
)

const EmployeeLoopReplicaMarker = "[employee-loop:5]"

var errEmployeeWindowTooLarge = errors.New("employee window exceeds context bounds")

type EmployeeSceneWorker struct {
	Langfuse     *langfuse.Client
	ReplicaReady func(context.Context) error
	handler      *Handler
	store        *employeeentry.Store
	model        employeeloop.Model
	wake         chan struct{}
	done         chan struct{}
}

func NewEmployeeSceneWorker(h *Handler, model employeeloop.Model) *EmployeeSceneWorker {
	database, _ := employeeEntryDB(h)
	return &EmployeeSceneWorker{handler: h, store: employeeentry.NewStore(database), model: model, wake: make(chan struct{}, 1), done: make(chan struct{})}
}
func (w *EmployeeSceneWorker) Notify() {
	if w == nil {
		return
	}
	select {
	case w.wake <- struct{}{}:
	default:
	}
}
func (w *EmployeeSceneWorker) Run(ctx context.Context) {
	defer close(w.done)
	var group sync.WaitGroup
	group.Go(func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			if w.handler.TaskService != nil {
				reconcileCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
				_, err := w.handler.TaskService.ReconcileEmployeeRuns(reconcileCtx, 100)
				cancel()
				if err != nil && !errors.Is(err, context.Canceled) {
					slog.WarnContext(ctx, "employee run reconciliation failed", "error", err)
				}
			}
			learningCtx, learningCancel := context.WithTimeout(ctx, 10*time.Second)
			if _, err := w.handler.ReconcileEmployeeLearnings(learningCtx, 100); err != nil && !errors.Is(err, context.Canceled) {
				slog.WarnContext(ctx, "employee learning capture failed", "error", err)
			}
			learningCancel()
			// The notice protocol requires every response worker to understand
			// its before-send fence, including while new admission is disabled.
			if w.ReplicaReady != nil {
				noticeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
				if readyErr := w.ReplicaReady(noticeCtx); readyErr == nil {
					if _, err := w.handler.ReconcileEmployeeRunNotices(noticeCtx, 100); err != nil && !errors.Is(err, context.Canceled) {
						slog.WarnContext(ctx, "employee run notice reconciliation failed", "error", err)
					}
				}
				cancel()
			}
			cleanupCtx, cleanupCancel := context.WithTimeout(ctx, 10*time.Second)
			if _, err := w.handler.ReconcileEmployeeTaskArtifacts(cleanupCtx, 100); err != nil && !errors.Is(err, context.Canceled) {
				slog.WarnContext(ctx, "employee artifact cleanup failed", "error", err)
			}
			cleanupCancel()
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	})
	for range 4 {
		group.Go(func() {
			ticker := time.NewTicker(500 * time.Millisecond)
			defer ticker.Stop()
			for {
				worked, err := w.ProcessNext(ctx)
				if err != nil && !errors.Is(err, context.Canceled) {
					slog.WarnContext(ctx, "employee scene job failed", "error", err)
				}
				if worked && err == nil {
					continue
				}
				select {
				case <-ctx.Done():
					return
				case <-w.wake:
				case <-ticker.C:
				}
			}
		})
	}
	group.Wait()
}
func (w *EmployeeSceneWorker) WaitWithTimeout(timeout time.Duration) bool {
	if w == nil {
		return true
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-w.done:
		return true
	case <-timer.C:
		return false
	}
}

type employeeSavedInput struct {
	Input  employeeloop.Input  `json:"input"`
	Config employeeloop.Config `json:"config"`
}
type employeeSavedOutcome struct {
	Outcome        employeeloop.Outcome `json:"outcome"`
	Failure        string               `json:"failure,omitempty"`
	SourceReplies  map[string]string    `json:"source_replies,omitempty"`
	ReplyReceiptID string               `json:"reply_receipt_id,omitempty"`
}

func (w *EmployeeSceneWorker) ProcessNext(ctx context.Context) (worked bool, returnErr error) {
	if w == nil || w.handler == nil || w.model == nil {
		return false, nil
	}
	job, err := w.store.Claim(ctx)
	if errors.Is(err, employeeentry.ErrNoJob) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var saved employeeSavedOutcome
	committed := false
	trace := employeeTraceStart(ctx, w.Langfuse, job)
	ctx = langfuse.ContextWithTrace(ctx, trace)
	defer func() { employeeTraceFinish(trace, saved, committed, returnErr) }()
	h := w.handler
	agent, agentErr := h.Queries.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{ID: parseUUID(job.Scope.AgentID), WorkspaceID: parseUUID(job.Scope.WorkspaceID)})
	if errors.Is(agentErr, pgx.ErrNoRows) || agent.ArchivedAt.Valid {
		return true, w.store.Hold(ctx, job, "agent_archived")
	}
	if agentErr != nil {
		return true, w.store.Retry(ctx, job, agentErr.Error())
	}
	if h.EmployeeLoopReady == nil {
		return true, w.store.Retry(ctx, job, "employee service is not ready")
	}
	if err = h.EmployeeLoopReady(ctx, parseUUID(job.Scope.WorkspaceID), parseUUID(job.Scope.AgentID)); err != nil {
		return true, w.store.Retry(ctx, job, err.Error())
	}
	if _, err = employeeSceneFence(ctx, h, job); err != nil {
		// A rebound tenant must not read old input or send into the old scene.
		raw, _ := json.Marshal(employeeSavedOutcome{Outcome: employeeloop.Outcome{Decision: employeeloop.Decision{Kind: employeeloop.Quiet}}, Failure: "scene tenant is no longer served"})
		if len(job.Outcome) == 0 {
			if saveErr := w.store.SaveOutcome(ctx, job, raw); saveErr != nil {
				return true, saveErr
			}
		}
		return true, w.store.Complete(ctx, job, nil)
	}
	runCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	envelopes := make([]employeeDispatchEnvelope, len(job.Items))
	for i, item := range job.Items {
		if err = json.Unmarshal(item.Payload, &envelopes[i]); err != nil {
			return true, w.store.Retry(ctx, job, "invalid persisted employee command")
		}
		env := &envelopes[i]
		if env.PrincipalID != item.PrincipalID || env.Command.EventReceiptID != item.ReceiptID || dispatchSceneID(env.Command) != job.Scope.SceneID || dispatchRecordedOrg(env.Command) != job.Scope.TenantOrgID {
			return true, w.store.Retry(ctx, job, "persisted employee scope mismatch")
		}
		env.Command.DispatchEndpointID = env.EndpointNamespaceID
		if err = employeePrincipalAllowed(runCtx, h, job.Scope, env.PrincipalID); err != nil {
			return true, w.store.Hold(ctx, job, "admission_principal_revoked")
		}
		env.Command, err = bindDispatchCompletionTarget(env.Command, env.TargetIdentity)
		if err != nil {
			return true, w.store.Retry(ctx, job, "invalid persisted callback target")
		}
	}
	if len(job.Outcome) > 0 {
		if err = json.Unmarshal(job.Outcome, &saved); err != nil {
			return true, err
		}
	} else {
		workEnvelopes, replies, replyReceipt, commandErr := w.memoryCommands(runCtx, job, envelopes)
		if commandErr != nil {
			return true, w.store.Retry(ctx, job, commandErr.Error())
		}
		saved.SourceReplies, saved.ReplyReceiptID = replies, replyReceipt
		if replyReceipt == "" {
			saved.Outcome.Kind = employeeloop.Quiet
		} else {
			var input employeeSavedInput
			if len(job.InputSnapshot) > 0 {
				err = json.Unmarshal(job.InputSnapshot, &input)
			} else {
				input, err = w.buildInput(runCtx, job, workEnvelopes, envelopes)
				if err == nil {
					var raw []byte
					raw, err = json.Marshal(input)
					if err == nil {
						_, err = w.store.SaveInput(runCtx, job, raw)
					}
				}
			}
			if errors.Is(err, errEmployeeWindowTooLarge) {
				saved.Outcome = employeeloop.Outcome{Decision: employeeloop.Decision{Kind: employeeloop.Reply, Reply: "本次消息内容过长，暂未开始处理。请分段发送或改为文件。"}}
				saved.Failure = err.Error()
			} else {
				if err != nil {
					return true, w.store.Retry(ctx, job, err.Error())
				}
				host := &employeeSceneHost{worker: w, job: job, envelopes: workEnvelopes, abort: cancel}
				durableModel := &employeeJournalModel{store: w.store, job: job, delegate: w.model, abort: cancel}
				input.Config.OnBatchRejected = func(calls []employeeloop.ToolCall, err error) { employeeTraceBatchRejected(runCtx, calls, err) }
				saved.Outcome, err = employeeloop.New(input.Config, durableModel, host).Run(runCtx, input.Input)
				if err == nil {
					host.attachCapabilityReplies(&saved.Outcome)
				}
				if err != nil {
					saved.Failure = err.Error()
					replies, unresolved := employeeAcceptedReplies(saved.Outcome)
					if len(replies) > 0 {
						saved.Outcome.Kind = employeeloop.Dispatched
						saved.Outcome.Reply = strings.Join(replies, "\n")
						if unresolved {
							saved.Outcome.Reply += "\n其余请求暂未完成受理。"
						}
					} else {
						saved.Outcome.Kind = employeeloop.Reply
						saved.Outcome.Reply = "这次没能完成受理，请稍后再试。"
					}
				}
			}
		}
		// Persist accepted facts even when the foreground deadline was reached after
		// a Host commit. A restart recovers the outcome, not a new model decision.
		checkpointCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		raw, encodeErr := json.Marshal(saved)
		if encodeErr == nil {
			encodeErr = w.store.SaveOutcome(checkpointCtx, job, raw)
		}
		stop()
		if encodeErr != nil {
			return true, encodeErr
		}
	}
	if err = w.complete(ctx, job, envelopes, saved); err != nil {
		return true, w.store.Retry(ctx, job, err.Error())
	}
	committed = true
	return true, nil
}

func (w *EmployeeSceneWorker) buildInput(ctx context.Context, job employeeentry.Job, envelopes, originalEnvelopes []employeeDispatchEnvelope) (employeeSavedInput, error) {
	messages := []employeeSourceMessage{}
	for i, item := range job.Items {
		messages = append(messages, employeeSourceMessages(item, envelopes[i])...)
	}
	window, err := json.Marshal(messages)
	if err != nil {
		return employeeSavedInput{}, err
	}
	if len(window) > 256<<10 {
		return employeeSavedInput{}, errEmployeeWindowTooLarge
	}
	input := employeeSavedInput{Input: employeeloop.Input{Identity: employeeloop.Identity{WorkspaceID: job.Scope.WorkspaceID, AgentID: job.Scope.AgentID, TenantOrgID: job.Scope.TenantOrgID, Scene: scene.Ref{SceneID: job.Scope.SceneID}, ReceiptID: job.Items[0].ReceiptID}, CurrentWindow: string(window)}, Config: employeeloop.Config{Tools: employeeSceneTools()}}
	if defaults, ok := w.model.(interface{ DefaultModel() string }); ok {
		input.Config.Model = defaults.DefaultModel()
	}
	agentID := parseUUID(job.Scope.AgentID)
	agent, err := w.handler.Queries.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{ID: agentID, WorkspaceID: parseUUID(job.Scope.WorkspaceID)})
	if err != nil {
		return employeeSavedInput{}, err
	}
	input.Config.Persona.Instructions, err = employeeRoleInstructions(agent)
	if err != nil {
		return employeeSavedInput{}, err
	}
	capabilities, err := employeeSceneCapabilities(ctx, w.handler, job, originalEnvelopes)
	if err != nil {
		return employeeSavedInput{}, err
	}
	if capabilities.Prompt != "" {
		input.Config.Persona.Instructions += "\n\n" + capabilities.Prompt
	}
	// Freeze reply guidance with new input snapshots; do not change the shared
	// prompt builder, which also renders already-journaled historical inputs.
	input.Config.Persona.Instructions += "\n\nMEMORY REPLIES:\n" +
		"Honor the user's requested output format exactly. If asked for only the current value, output that value alone, without a preamble, explanation or correction history. " +
		"If the requested fact is unavailable in the current authorized memory, say you do not know in the requested format. Do not enumerate unrelated memories or offer or claim access to another scene's private memory. " +
		"For ordinary memory confirmations, use brief natural language without record IDs, internal states or source/evidence metadata. After forgetting, do not repeat the forgotten content. Include such details only when the user explicitly requests an audit."
	input.Config.Persona.Expertise = capabilities.Directory
	if voice, e := w.handler.Queries.GetAgentVoice(ctx, agentID); e == nil {
		input.Config.Persona.Personality = voice.Persona
		input.Config.Persona.Tone = voice.ReplyTone
	}
	if identity, e := w.handler.Queries.GetAgentDingTalkIdentity(ctx, db.GetAgentDingTalkIdentityParams{WorkspaceID: parseUUID(job.Scope.WorkspaceID), AgentID: agentID}); e == nil {
		input.Config.Persona.Name = identity.AccountDisplayName
	}
	if w.handler.EmployeeMemory != nil {
		brief, e := w.handler.EmployeeMemory.Brief(ctx, employeememory.Scope{WorkspaceID: parseUUID(job.Scope.WorkspaceID), AgentID: agentID, TenantOrgID: job.Scope.TenantOrgID, Scene: scene.Ref{SceneID: job.Scope.SceneID}, Kind: employeememory.ScopeScene}, "", 8)
		if e == nil {
			input.Input.Memory = brief
		} else {
			input.Input.Memory = "Scene memory unavailable."
		}
		originalMessages := []employeeSourceMessage{}
		for i, item := range job.Items {
			originalMessages = append(originalMessages, employeeSourceMessages(item, originalEnvelopes[i])...)
		}
		registered, e := employeeSceneFence(ctx, w.handler, job)
		if e != nil {
			return employeeSavedInput{}, e
		}
		if requester, unique := employeeAutomaticPrivateRequester(registered, originalMessages); unique {
			private, e := w.handler.EmployeeMemory.Brief(ctx, employeememory.Scope{WorkspaceID: parseUUID(job.Scope.WorkspaceID), AgentID: agentID, TenantOrgID: job.Scope.TenantOrgID, Scene: scene.Ref{SceneID: job.Scope.SceneID}, Kind: employeememory.ScopePrivate, PrincipalID: requester}, "", 4)
			if e == nil && private != "" {
				input.Input.Memory += "\nRequester-private background context for this source only; do not disclose it to other participants.\n" + private
			}
		}
	}
	return input, nil
}

// employeeJournalModel counts a provider attempt before I/O and durably freezes
// native completions before any tool can execute. Restart replays those exact IDs.
type employeeJournalModel struct {
	store    *employeeentry.Store
	job      employeeentry.Job
	delegate employeeloop.Model
	ordinal  int
	abort    context.CancelFunc
}

func (m *employeeJournalModel) Chat(ctx context.Context, request openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
	ordinal := m.ordinal
	m.ordinal++
	failJournal := func(err error) (*openai.ChatCompletion, error) {
		if m.abort != nil {
			m.abort()
		}
		return nil, err
	}
	raw, err := json.Marshal(request)
	if err != nil {
		return failJournal(err)
	}
	cached, err := m.store.BeginModel(ctx, m.job, ordinal, raw)
	if err != nil {
		var recorded *employeeentry.ModelFailure
		if errors.As(err, &recorded) {
			return nil, recorded
		}
		return failJournal(err)
	}
	if len(cached) > 0 {
		var out openai.ChatCompletion
		if err = json.Unmarshal(cached, &out); err != nil {
			return failJournal(err)
		}
		return &out, nil
	}
	// Keep journal identity unchanged across upgrades, including old snapshots
	// with a blank model or user-provided configuration URLs. Only real I/O
	// applies provider normalization; cached responses and failures bypass it.
	clean, err := employeeModelConfigLinks(raw)
	if err != nil {
		return failJournal(err)
	}
	if string(clean) != string(raw) {
		if err = json.Unmarshal(clean, &request); err != nil {
			return failJournal(err)
		}
	}
	// New snapshots already freeze their effective model in Config.
	if strings.TrimSpace(string(request.Model)) == "" {
		if defaults, ok := m.delegate.(interface{ DefaultModel() string }); ok {
			request.Model = defaults.DefaultModel()
		}
	}
	generation := employeeTraceGeneration(ctx, m.job, ordinal, request)
	out, err := m.delegate.Chat(ctx, request)
	if err == nil && out == nil {
		err = errors.New("empty employee model completion")
	}
	employeeTraceEndGeneration(generation, out, err)
	if err != nil {
		message := err.Error()
		if message == "" {
			message = "employee model request failed"
		}
		if saveErr := m.store.SaveModelFailure(ctx, m.job, ordinal, message); saveErr != nil {
			return failJournal(saveErr)
		}
		return nil, &employeeentry.ModelFailure{Message: message}
	}
	raw, err = json.Marshal(out)
	if err != nil {
		return failJournal(err)
	}
	if err = m.store.SaveModel(ctx, m.job, ordinal, raw); err != nil {
		return failJournal(err)
	}
	return out, nil
}

func employeeAcceptedReplies(outcome employeeloop.Outcome) ([]string, bool) {
	replies := []string{}
	executed := map[string]bool{}
	unresolved := false
	for _, effect := range outcome.ToolOutcomes {
		executed[effect.NativeToolCallID] = true
		if effect.Result.Receipt != "" && effect.Result.Terminal != nil && effect.Result.Terminal.Reply != "" {
			replies = append(replies, effect.Result.Terminal.Reply)
		} else if effect.Error != "" {
			unresolved = true
		}
	}
	for _, entry := range outcome.Entries {
		if entry.Message != nil {
			for _, call := range entry.Message.ToolCalls {
				if !executed[call.ID] {
					unresolved = true
				}
			}
		}
	}
	return replies, unresolved
}

func (w *EmployeeSceneWorker) complete(ctx context.Context, job employeeentry.Job, envelopes []employeeDispatchEnvelope, saved employeeSavedOutcome) error {
	h := w.handler
	actionIDs := []string{}
	err := w.store.Complete(ctx, job, func(tx pgx.Tx) error {
		// Reuse this transaction for the use-time fences; borrowing the pool here
		// deadlocks a single-connection deployment while Complete holds its lease.
		permissionView := &Handler{Queries: db.New(tx)}
		registered, err := employeeSceneFence(ctx, permissionView, job)
		if err != nil {
			return err
		}
		for i, env := range envelopes {
			if err := employeePrincipalAllowed(ctx, permissionView, job.Scope, env.PrincipalID); err != nil {
				return err
			}
			text := saved.SourceReplies[env.Command.EventReceiptID]
			ownsReply := saved.ReplyReceiptID == env.Command.EventReceiptID || (saved.ReplyReceiptID == "" && i == 0)
			if ownsReply && saved.Outcome.Kind != employeeloop.Quiet {
				if text != "" {
					text += "\n"
				}
				text += strings.TrimSpace(saved.Outcome.Reply)
			}
			command := env.Command
			if command.CompletionCallback != nil {
				if h.TaskService == nil {
					return errors.New("employee completion outbox is unavailable")
				}
				dc := agentDispatchContext{WorkspaceID: parseUUID(job.Scope.WorkspaceID), AgentID: parseUUID(job.Scope.AgentID), UserID: parseUUID(env.PrincipalID)}
				if err := h.registerDingTalkResponseRoute(ctx, tx, command, dc); err != nil {
					return err
				}
				transactional := service.TaskService{Queries: db.New(tx), TxStarter: tx}
				if text == "" {
					err = transactional.EnqueueSynchronousSilence(ctx, command.CompletionCallback.URL, command.CompletionCallback.Target, dc.AgentID)
				} else {
					err = transactional.EnqueueSynchronousWorkReceipt(ctx, command.CompletionCallback.URL, command.CompletionCallback.Target, dc.AgentID, text)
				}
				if err != nil {
					return err
				}
			} else if text != "" {
				if h.DingTalkResponses == nil || command.ExternalIdentity.DWS == nil {
					return errors.New("employee response outbox is unavailable")
				}
				sender := firstNonEmpty(command.Event.Data.Sender.OpenDingTalkID, command.Event.Data.Sender.SenderOpenDingTalkID)
				in := dingtalkresponse.ActionInput{WorkspaceID: job.Scope.WorkspaceID, AgentID: job.Scope.AgentID, DWSUID: command.ExternalIdentity.DWS.UID, DWSOrgID: job.Scope.TenantOrgID, SceneID: job.Scope.SceneID, ConversationID: registered.ExternalSceneID, IsGroup: registered.SceneKind == scene.KindGroup, SenderOpenDingTalkID: sender, DWSEnvironment: commandDWSEnvironment(command), Text: text}
				if command.ResponsePolicy != nil {
					in.ShowAITag = command.ResponsePolicy.ShowAITag
				}
				actionID, enqueueErr := h.DingTalkResponses.EnqueueSceneNotice(ctx, tx, in, employeeJobReplyID(job, saved, command.EventReceiptID))
				if enqueueErr != nil {
					return fmt.Errorf("employee response: %w", enqueueErr)
				}
				actionIDs = append(actionIDs, actionID)
			}
		}
		return nil
	})
	if err == nil {
		if h.DingTalkResponses != nil {
			h.DingTalkResponses.Notify()
		}
		if h.TaskService != nil && h.TaskService.CompletionNotifier != nil {
			h.TaskService.CompletionNotifier.NotifyTaskCompletion()
		}
		w.logCompleted(ctx, job, saved, actionIDs)
	}
	return err
}

// logCompleted observes only committed metadata. model_attempts is the durable
// pre-provider reservation count; response/failure counts distinguish completed
// provider outcomes without exporting prompts, completions or private memory.
func (w *EmployeeSceneWorker) logCompleted(ctx context.Context, job employeeentry.Job, saved employeeSavedOutcome, actionIDs []string) {
	receipts := make([]string, 0, len(job.Items))
	for _, item := range job.Items {
		receipts = append(receipts, item.ReceiptID)
	}
	runs := []string{}
	for _, receipt := range saved.Outcome.Receipts {
		if _, err := scene.ParseID(receipt.ID); err == nil {
			runs = append(runs, receipt.ID)
		}
	}
	fields := []any{"event", "employee_scene_job_completed", "workspace_id", job.Scope.WorkspaceID, "agent_id", job.Scope.AgentID, "tenant_org_id", job.Scope.TenantOrgID, "scene_id", job.Scope.SceneID, "job_id", job.ID, "receipt_ids", receipts, "state", "completed", "kind", saved.Outcome.Kind, "run_ids", runs, "action_ids", actionIDs}
	database, ok := employeeEntryDB(w.handler)
	if !ok {
		slog.InfoContext(ctx, "employee scene job completed", append(fields, "model_counts_known", false)...)
		return
	}
	observeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	var attempts, responses, failures int
	err := database.QueryRow(observeCtx, `SELECT model_attempts,
 (SELECT count(*) FROM jsonb_array_elements(model_journal) turn WHERE jsonb_typeof(turn->'response')='object'),
 (SELECT count(*) FROM jsonb_array_elements(model_journal) turn WHERE COALESCE(turn->>'failure','')<>'')
 FROM employee_scene_job WHERE id=$1::uuid AND workspace_id=$2::uuid AND agent_id=$3::uuid AND tenant_org_id=$4 AND scene_id=$5::uuid AND state='completed'`, job.ID, job.Scope.WorkspaceID, job.Scope.AgentID, job.Scope.TenantOrgID, job.Scope.SceneID).Scan(&attempts, &responses, &failures)
	fields = append(fields, "model_counts_known", err == nil)
	if err == nil {
		fields = append(fields, "model_attempts", attempts, "model_response_count", responses, "model_failure_count", failures)
	}
	slog.InfoContext(ctx, "employee scene job completed", fields...)
}

// Ready verifies the configured services, live replica protocol and current
// runtime before the settings API enables Employee.
func (w *EmployeeSceneWorker) Ready(ctx context.Context, workspaceID, agentID pgtype.UUID) error {
	if w == nil || w.handler == nil || w.model == nil || w.store == nil || w.handler.Queries == nil || w.handler.TxStarter == nil || w.handler.TaskService == nil || w.handler.DingTalkResponses == nil {
		return errors.New("employee services are not configured")
	}
	if enabled, ok := w.model.(interface{ Enabled() bool }); ok && !enabled.Enabled() {
		return errors.New("employee model is not configured")
	}
	if w.handler.DingTalkResponses.BeforeSend == nil || w.handler.EmployeeRunNoticeArtifacts == nil || w.handler.EmployeeMemory == nil {
		return errors.New("employee delivery and memory services are not configured")
	}
	if w.ReplicaReady == nil {
		return errors.New("employee replica capability verification is unavailable")
	}
	if err := w.ReplicaReady(ctx); err != nil {
		return err
	}
	agent, err := w.handler.Queries.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{ID: agentID, WorkspaceID: workspaceID})
	if err != nil {
		return err
	}
	if agent.ArchivedAt.Valid || !agent.RuntimeID.Valid {
		return errors.New("employee agent is archived or has no runtime")
	}
	runtime, err := w.handler.Queries.GetAgentRuntime(ctx, agent.RuntimeID)
	if err != nil {
		return err
	}
	if runtime.WorkspaceID != workspaceID || !service.DirectTaskRuntimeCapable(runtime) {
		return errors.New("runtime has not proved employee-direct-v1 support")
	}
	return nil
}
