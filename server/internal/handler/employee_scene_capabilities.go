package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/contextcap"
	"github.com/multica-ai/multica/server/internal/employeeentry"
	"github.com/multica-ai/multica/server/internal/scene"
	"github.com/multica-ai/multica/server/internal/service/employeeloop"
	"github.com/multica-ai/multica/server/internal/service/inboundcoord"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type employeeCapabilityContext struct {
	Prompt    string
	Directory []string
}

// The foreground talks and reads; the Direct executor acts. A foreground
// without a tool for something is not evidence that the executor lacks it,
// so the default for work it cannot do here is dispatch, not refusal.
const employeeForegroundBoundary = "Foreground boundary: here you reply, read the current window, memory and tasks, read this scene's configuration with scene_config_get, and give the configuration link with describe_capabilities. Everything else runs in a background task through dispatch_task: DWS lookups (contacts, managers, org, calendar, docs, messages beyond this window), skills, connectors, MCP servers, scripts, files and scene configuration changes. When a request needs such work, dispatch it; never answer that you cannot do it, have no record, or lack a tool because this foreground has none. Only a task result can show that something is impossible. Never claim a change or lookup is done before a task result says so."

// Scene changes are made by the executor's config-qwen-tag-scene MCP tools
// (scene_routine_create and friends), mounted for every task with a current
// group or 1:1 scene. The foreground hands the request over unchanged.
const employeeSceneSelfManagement = "Scene self-management is background work you arrange with dispatch_task, not a missing capability: create, change, pause, resume, delete or run now a routine (例行任务/定时任务: a cron schedule at most every 15 minutes, default timezone Asia/Shanghai, or a webhook); add, change or delete scene prompts; switch offered skills or connectors on or off; add, change or remove remote MCP servers. The background executor changes this scene only, with its config-qwen-tag-scene tools; every routine run then executes in the background with this scene's configuration, and the platform posts its start and end messages here. Dispatch such a request with the requester's exact words (what each run does, when it runs, names) in goal and prompt. Never send the requester to an administrator or platform setting instead. Ask briefly before dispatching only when the request lacks what to do or when. Connecting an account or authorizing a connector happens on the configuration page: give the link with describe_capabilities."

// The directory is a configuration projection, never a runtime or authority grant.
func employeeSceneCapabilities(ctx context.Context, h *Handler, job employeeentry.Job, envelopes []employeeDispatchEnvelope) (employeeCapabilityContext, error) {
	target, err := employeeSceneConfigTarget(ctx, h, job)
	if err != nil {
		return employeeCapabilityContext{}, err
	}
	for _, env := range envelopes {
		if err = employeePrincipalAllowed(ctx, h, job.Scope, env.PrincipalID); err != nil {
			return employeeCapabilityContext{}, err
		}
	}
	target.scope.PersonKey = employeeCapabilityPerson(job, envelopes)
	global, err := contextcap.LoadGlobalLayer(ctx, h.DB, job.Scope.WorkspaceID, job.Scope.AgentID)
	if err != nil {
		return employeeCapabilityContext{}, err
	}
	layers, err := contextcap.LoadLayers(ctx, h.DB, job.Scope.WorkspaceID, job.Scope.AgentID, target.scope.Selection())
	if err != nil {
		return employeeCapabilityContext{}, err
	}
	effective, _ := mergeTaskContext(append([]contextcap.ContextLayer{global}, layers...)...)
	out := employeeCapabilityContext{Prompt: effective.PromptBlock(), Directory: []string{
		"Internal execution catalog. Skills/connectors/MCP, including DWS/dws-shortcuts, run in background tasks, not this foreground. Configured entries do not prove access; verify when executing. Missing entries do not prove absence.",
		"For ordinary capability introductions or link-only requests, use this directory and call describe_capabilities on the first model call. Do not call scene_config_get merely for a more accurate introduction; it cannot verify runtime access. Host appends the link and how long it stays valid; never invent a URL or state its lifetime yourself.",
		"Only use scene_config_get when the user explicitly asks for configuration details (exact switches, stored prompts, or existing routines) not already in context. For explicit configuration questions, preserve exact skill names, switch states, and stored prompt text as requested; answer fully, including when a link is also requested.",
		"For ordinary introductions, reply like a colleague: normally 1–3 short sentences, not a configuration inventory. Describe useful work, not internal tool names, skill package names, Direct, or configuration fields. Say you can arrange executor work; never claim you can call DWS or shell here. Mention access uncertainty briefly only when material.",
		employeeForegroundBoundary,
		employeeSceneSelfManagement,
	}}
	catalogLimit := len(out.Directory) + 64
	add := func(label string) { out.Directory = append(out.Directory, label) }
	for _, kind := range []struct {
		name, query string
		resources   []contextcap.EffectiveResource
	}{
		{"skill", `SELECT name,description,left(content,4096),true FROM skill WHERE workspace_id=$1::uuid AND id=$2::uuid`, effective.Skills},
		{"connector", `SELECT name,'','',enabled FROM internal_connector WHERE workspace_id=$1::uuid AND id=$2::uuid`, effective.Connectors},
	} {
		for _, resource := range kind.resources {
			if len(out.Directory) >= catalogLimit {
				add("Additional capabilities omitted; this directory is incomplete. Omission does not mean unavailable.")
				return out, nil
			}
			var name, description, head string
			var enabled bool
			err = h.DB.QueryRow(ctx, kind.query, job.Scope.WorkspaceID, resource.ID).Scan(&name, &description, &head, &enabled)
			if errors.Is(err, pgx.ErrNoRows) {
				add(kind.name + " metadata unavailable; this directory is incomplete")
				continue
			}
			if err != nil {
				return employeeCapabilityContext{}, err
			}
			if kind.name == "skill" {
				description = inboundcoord.SkillCatalogDescription(description, head)
			}
			status := "disabled in configuration"
			if enabled {
				status = "enabled in configuration"
			}
			add(fmt.Sprintf("%s %s: %s [%s; background use; access unverified]", kind.name, employeeCatalogLabel(name, 100), employeeCatalogLabel(description, 200), status))
		}
	}
	for _, server := range effective.AppliedMCPServers() {
		var config struct {
			Disabled bool `json:"disabled"`
		}
		status := "configuration unavailable"
		if json.Unmarshal(server.Config, &config) == nil {
			status = "enabled in configuration"
			if config.Disabled {
				status = "disabled in configuration"
			}
		}
		add("remote MCP " + employeeCatalogLabel(server.Name, 100) + " [" + status + "; background use; access unverified]")
	}
	return out, nil
}

// A person layer requires the same known actor and person key
// (contextcap.TriggerPersonKey: the staffId, else the openDingTalkId of a
// native sender) on every original message, including messages removed by
// foreground command handling.
func employeeCapabilityPerson(job employeeentry.Job, envelopes []employeeDispatchEnvelope) string {
	actor, staff := "", ""
	for _, env := range envelopes {
		if len(env.Command.Event.Data.Messages) == 0 {
			return ""
		}
		for _, message := range env.Command.Event.Data.Messages {
			ref := employeeRequesterRef(job.Scope.TenantOrgID, message)
			key := contextcap.TriggerPersonKey(message.SenderStaffID, message.SenderOpenDingTalkID)
			if ref == "" || key == "" {
				return ""
			}
			if actor != "" && (actor != ref || staff != key) {
				return ""
			}
			actor, staff = ref, key
		}
	}
	return staff
}

// employeeSenderName is the display name of the window's first message.
func employeeSenderName(envelopes []employeeDispatchEnvelope) string {
	for _, env := range envelopes {
		for _, message := range env.Command.Event.Data.Messages {
			if name := firstNonEmpty(message.SenderDisplayName, env.Command.Event.Data.Sender.DisplayName); name != "" {
				return name
			}
		}
	}
	return ""
}

func employeeSceneConfigTarget(ctx context.Context, h *Handler, job employeeentry.Job) (sceneConfigTarget, error) {
	registered, err := employeeSceneFence(ctx, h, job)
	if err != nil {
		return sceneConfigTarget{}, err
	}
	if registered.SceneKind != scene.KindGroup && registered.SceneKind != scene.KindDM {
		return sceneConfigTarget{}, errors.New("employee configuration requires a conversation scene")
	}
	scope := contextcap.Scope{Dispatched: true, OrgID: job.Scope.TenantOrgID, SceneID: job.Scope.SceneID, SceneTitle: registered.Title, ConversationType: registered.SceneKind}
	return sceneConfigTarget{agent: contextCapAgent{ID: job.Scope.AgentID, WorkspaceID: job.Scope.WorkspaceID, OrgID: job.Scope.TenantOrgID}, scope: scope, scene: contextcap.SceneSummary{SceneID: job.Scope.SceneID, Kind: registered.SceneKind, Title: registered.Title, OrgID: job.Scope.TenantOrgID, ConversationID: registered.ExternalSceneID}}, nil
}

func (h *employeeSceneHost) sceneConfiguration(ctx context.Context, tx pgx.Tx) (string, error) {
	view := *h.worker.handler
	view.DB, view.Queries = tx, db.New(tx)
	target, err := employeeSceneConfigTarget(ctx, &view, h.job)
	if err != nil {
		return "", err
	}
	result, err := view.sceneConfigGet(ctx, target)
	if err != nil {
		return "", err
	}
	// Read access is a foreground capability; mutations remain on Direct's
	// existing scene MCP. "read_only" in the executor result means a routine
	// run; here it read as "this scene cannot be changed", so it is dropped.
	value := result.(map[string]any)
	delete(value, "read_only")
	value["how_to_change"] = "dispatch_task with the requester's exact request: the background executor changes this scene (routines, prompts, offered skill/connector switches, remote MCP servers) with config-qwen-tag-scene. This read changes nothing."
	value["runtime_availability"] = "skill and connector switches are configuration; access is verified when a background task uses them"
	raw, err := json.Marshal(value)
	return string(raw), err
}

// A savepoint makes a failed link mint leave the original answer usable. The
// outer tool journal transaction commits the bearer reply and minted link once.
func (h *employeeSceneHost) capabilityReply(ctx context.Context, tx pgx.Tx, reply string) string {
	// A short client deadline can close pgx's connection and destroy the outer
	// journal transaction. PostgreSQL query cancellation is recoverable by this
	// savepoint; rollback also restores its transaction-local GUC changes.
	mintTx, err := tx.Begin(ctx)
	if err != nil {
		return reply
	}
	defer mintTx.Rollback(context.WithoutCancel(ctx))
	var originalTimeout string
	var originalTimeoutMS int64
	if err = mintTx.QueryRow(ctx, `SELECT current_setting('statement_timeout'), setting::bigint FROM pg_settings WHERE name='statement_timeout'`).Scan(&originalTimeout, &originalTimeoutMS); err != nil {
		return reply
	}
	mintTimeoutMS := int64(2000)
	if originalTimeoutMS > 0 && originalTimeoutMS < mintTimeoutMS {
		mintTimeoutMS = originalTimeoutMS
	}
	if _, err = mintTx.Exec(ctx, `SELECT set_config('statement_timeout',$1,true)`, fmt.Sprintf("%dms", mintTimeoutMS)); err != nil {
		return reply
	}
	view := *h.worker.handler
	view.DB, view.Queries = mintTx, db.New(mintTx)
	target, err := employeeSceneConfigTarget(ctx, &view, h.job)
	if err != nil {
		return reply
	}
	// A 1:1 chat's link also carries its person (mintContextConfigLink).
	target.scope.PersonKey = employeeCapabilityPerson(h.job, h.envelopes)
	if target.scope.PersonKey != "" {
		target.scope.PersonName = employeeSenderName(h.envelopes)
	}
	origin, err := view.contextConfigLinkOrigin()
	if err != nil {
		return reply
	}
	result, err := view.mintContextConfigLink(ctx, contextConfigLinkMint{WorkspaceID: h.job.Scope.WorkspaceID, AgentID: h.job.Scope.AgentID, Scope: target.scope, Origin: origin, Issuer: contextConfigLinkIssuerEmployee, EmployeeJobID: h.job.ID, HostAppended: true})
	if err != nil {
		return reply
	}
	// RELEASE SAVEPOINT retains SET LOCAL changes in the outer transaction,
	// so the successful path must restore the prior value before releasing it.
	if _, err = mintTx.Exec(ctx, `SELECT set_config('statement_timeout',$1,true)`, originalTimeout); err != nil {
		return reply
	}
	if err = mintTx.Commit(ctx); err != nil {
		return reply
	}
	label := "本群能力配置"
	if target.scene.Kind == scene.KindDM {
		label = "本单聊能力配置"
	}
	return strings.TrimSpace(reply) + "\n\n[" + label + "](" + result.DingTalkURL + ")（" + strconv.Itoa(int(configLinkValidFor(result)/time.Minute)) + " 分钟内有效）"
}

// Attach only the Host suffix after the loop has composed every accepted reply.
// Reads and additional answers in the same native batch cannot erase it.
func (h *employeeSceneHost) attachCapabilityReplies(outcome *employeeloop.Outcome) {
	if outcome.Kind != employeeloop.Reply {
		return
	}
	for _, tool := range outcome.ToolOutcomes {
		delivery, ok := h.deliveryReplies[tool.NativeToolCallID]
		if !ok || tool.Error != "" || tool.Result.Terminal == nil {
			continue
		}
		body := tool.Result.Terminal.Reply
		if !strings.HasPrefix(delivery, body) {
			continue
		}
		if suffix := strings.TrimSpace(strings.TrimPrefix(delivery, body)); suffix != "" {
			outcome.Reply = strings.TrimSpace(outcome.Reply) + "\n\n" + suffix
		}
	}
}
