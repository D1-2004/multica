package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

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
		"Capability directory: enabled configuration, not runtime availability or authority. Unlisted/unloaded tools are not proof that a capability is absent. Actual access, credentials and tools are checked by the Direct executor.",
		"config-qwen-tag-scene: this scene can manage prompts, offered skills/connectors and remote MCP configuration through a Direct task. Use scene_config_get to inspect; dispatch_task for requested management. Never infer inability to manage from foreground tool count.",
		"For capability introductions or a requested configuration link use describe_capabilities with your answer; Host appends this scene's configuration link. Do not invent or include configuration URLs yourself.",
	}}
	add := func(label string) { out.Directory = append(out.Directory, label) }
	for _, kind := range []struct {
		name, query string
		resources   []contextcap.EffectiveResource
	}{
		{"skill", `SELECT name,description,left(content,4096),true FROM skill WHERE workspace_id=$1::uuid AND id=$2::uuid`, effective.Skills},
		{"connector", `SELECT name,'','',enabled FROM internal_connector WHERE workspace_id=$1::uuid AND id=$2::uuid`, effective.Connectors},
	} {
		for _, resource := range kind.resources {
			if len(out.Directory) >= 67 {
				add("Additional capabilities omitted; catalog_complete=false. Omission does not mean unavailable.")
				return out, nil
			}
			var name, description, head string
			var enabled bool
			err = h.DB.QueryRow(ctx, kind.query, job.Scope.WorkspaceID, resource.ID).Scan(&name, &description, &head, &enabled)
			if errors.Is(err, pgx.ErrNoRows) {
				add(kind.name + " metadata unavailable; catalog_complete=false")
				continue
			}
			if err != nil {
				return employeeCapabilityContext{}, err
			}
			if kind.name == "skill" {
				description = inboundcoord.SkillCatalogDescription(description, head)
			}
			add(fmt.Sprintf("%s %s: %s [layer=%s; configuration_enabled=%t; runtime_availability=unverified]", kind.name, employeeCatalogLabel(name, 100), employeeCatalogLabel(description, 200), resource.Layer, enabled))
		}
	}
	for _, server := range effective.AppliedMCPServers() {
		var config struct {
			Disabled bool `json:"disabled"`
		}
		status := "unavailable"
		if json.Unmarshal(server.Config, &config) == nil {
			status = fmt.Sprint(!config.Disabled)
		}
		add("remote MCP " + employeeCatalogLabel(server.Name, 100) + " [configuration_enabled=" + status + "; runtime_availability=unverified]")
	}
	return out, nil
}

// A person layer requires the same known actor and staff identifier on every
// original message, including messages removed by foreground command handling.
func employeeCapabilityPerson(job employeeentry.Job, envelopes []employeeDispatchEnvelope) string {
	actor, staff := "", ""
	for _, env := range envelopes {
		if len(env.Command.Event.Data.Messages) == 0 {
			return ""
		}
		for _, message := range env.Command.Event.Data.Messages {
			ref := employeeRequesterRef(job.Scope.TenantOrgID, message)
			key := strings.TrimSpace(message.SenderStaffID)
			if ref == "" || !contextcap.ValidStaffID(key) {
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
	// existing scene MCP. Do not pretend this read was a routine task.
	value := result.(map[string]any)
	value["read_only"] = true
	value["management_via"] = "dispatch_task / Direct config-qwen-tag-scene"
	value["runtime_availability"] = "unverified; configuration switches do not prove runtime access"
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
	origin, err := view.contextConfigLinkOrigin()
	if err != nil {
		return reply
	}
	result, err := view.mintContextConfigLink(ctx, contextConfigLinkMint{WorkspaceID: h.job.Scope.WorkspaceID, AgentID: h.job.Scope.AgentID, Scope: target.scope, Origin: origin, Issuer: contextConfigLinkIssuerEmployee, EmployeeJobID: h.job.ID})
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
	return strings.TrimSpace(reply) + "\n\n[" + label + "](" + result.DingTalkURL + ")（30 分钟内有效）"
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
