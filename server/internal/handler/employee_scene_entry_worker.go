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
	"github.com/multica-ai/multica/server/internal/scene"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/service/dingtalkresponse"
	"github.com/multica-ai/multica/server/internal/service/employeeloop"
	"github.com/multica-ai/multica/server/internal/service/employeememory"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	openai "github.com/openai/openai-go/v3"
)

const EmployeeLoopReplicaMarker = "[employee-loop:1]"

var errEmployeeWindowTooLarge = errors.New("employee window exceeds context bounds")

type EmployeeSceneWorker struct {
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
	Outcome employeeloop.Outcome `json:"outcome"`
	Failure string               `json:"failure,omitempty"`
}

func (w *EmployeeSceneWorker) ProcessNext(ctx context.Context) (bool, error) {
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
	var saved employeeSavedOutcome
	if len(job.Outcome) > 0 {
		if err = json.Unmarshal(job.Outcome, &saved); err != nil {
			return true, err
		}
	} else {
		var input employeeSavedInput
		if len(job.InputSnapshot) > 0 {
			err = json.Unmarshal(job.InputSnapshot, &input)
		} else {
			input, err = w.buildInput(runCtx, job, envelopes)
			if err == nil {
				var raw []byte
				raw, err = json.Marshal(input)
				if err == nil {
					_, err = w.store.SaveInput(runCtx, job, raw)
				}
			}
		}
		if errors.Is(err, errEmployeeWindowTooLarge) {
			saved = employeeSavedOutcome{Outcome: employeeloop.Outcome{Decision: employeeloop.Decision{Kind: employeeloop.Reply, Reply: "本次消息内容过长，暂未开始处理。请分段发送或改为文件。"}}, Failure: err.Error()}
		} else {
			if err != nil {
				return true, w.store.Retry(ctx, job, err.Error())
			}
			host := &employeeSceneHost{worker: w, job: job, envelopes: envelopes, abort: cancel}
			durableModel := &employeeJournalModel{store: w.store, job: job, delegate: w.model, abort: cancel}
			saved.Outcome, err = employeeloop.New(input.Config, durableModel, host).Run(runCtx, input.Input)
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
	return true, nil
}

func (w *EmployeeSceneWorker) buildInput(ctx context.Context, job employeeentry.Job, envelopes []employeeDispatchEnvelope) (employeeSavedInput, error) {
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
	agentID := parseUUID(job.Scope.AgentID)
	agent, err := w.handler.Queries.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{ID: agentID, WorkspaceID: parseUUID(job.Scope.WorkspaceID)})
	if err != nil {
		return employeeSavedInput{}, err
	}
	input.Config.Persona.Instructions, err = employeeRoleInstructions(agent)
	if err != nil {
		return employeeSavedInput{}, err
	}
	if skills, e := employeeSkillDirectory(ctx, w.handler, job.Scope); e == nil {
		input.Config.Persona.Expertise = skills
	} else {
		input.Input.TaskBrief = "Skill catalog unavailable; unavailable does not mean no skills are installed."
	}
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
	out, err := m.delegate.Chat(ctx, request)
	if err == nil && out == nil {
		err = errors.New("empty employee model completion")
	}
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
	err := w.store.Complete(ctx, job, func(tx pgx.Tx) error {
		registered, err := employeeSceneFence(ctx, h, job)
		if err != nil {
			return err
		}
		for i, env := range envelopes {
			if err := employeePrincipalAllowed(ctx, h, job.Scope, env.PrincipalID); err != nil {
				return err
			}
			text := ""
			if i == 0 && saved.Outcome.Kind != employeeloop.Quiet {
				text = strings.TrimSpace(saved.Outcome.Reply)
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
				if _, err = h.DingTalkResponses.EnqueueSceneNotice(ctx, tx, in, job.ID); err != nil {
					return fmt.Errorf("employee response: %w", err)
				}
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
	}
	return err
}

// Ready verifies the currently bound runtime before the settings API enables Employee.
func (w *EmployeeSceneWorker) Ready(ctx context.Context, workspaceID, agentID pgtype.UUID) error {
	if w == nil || w.handler == nil || w.model == nil || w.handler.TaskService == nil || w.handler.DingTalkResponses == nil {
		return errors.New("employee services are not configured")
	}
	if enabled, ok := w.model.(interface{ Enabled() bool }); ok && !enabled.Enabled() {
		return errors.New("employee model is not configured")
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
