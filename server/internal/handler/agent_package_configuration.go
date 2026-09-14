package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/agentsource"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Requirements contain aliases and locations only, never configuration values.
type PackageRequirements struct {
	DshPlugins       []PackageDshPluginRequirement `json:"dsh_plugins"`
	Secrets          []string                      `json:"secrets"`
	DeferredBindings []string                      `json:"deferred_bindings"`
	RuntimeProvider  string                        `json:"runtime_provider"`
}

type packageConfiguration struct {
	DshPlugins     []packageDshPlugin `json:"dsh_plugins"`
	pluginBindings map[string]string
	Configuration  UpdateAgentRequest `json:"configuration"`
	Bindings       struct {
		Runtime struct {
			Ref         string `json:"ref"`
			Provider    string `json:"provider"`
			RuntimeMode string `json:"runtime_mode"`
		} `json:"runtime"`
	} `json:"bindings"`
	DisabledRuntimeSkills []struct {
		RuntimeRef string `json:"runtime_ref"`
		DisabledRuntimeSkill
	} `json:"disabled_runtime_skills"`
	OKRs []struct {
		Objective  string   `json:"objective"`
		KeyResults []string `json:"key_results"`
	} `json:"okrs"`
	A2A *struct {
		Enabled         *bool           `json:"enabled"`
		CardName        *string         `json:"card_name"`
		CardDescription *string         `json:"card_description"`
		CardVersion     *string         `json:"card_version"`
		CardSkills      json.RawMessage `json:"card_skills"`
		Clients         []struct {
			Key           string   `json:"key"`
			Name          string   `json:"name"`
			Status        string   `json:"status"`
			Scopes        []string `json:"scopes"`
			RateLimit     *int32   `json:"rate_limit_per_minute"`
			MaxConcurrent *int32   `json:"max_concurrent_tasks"`
		} `json:"clients"`
	} `json:"a2a"`
	warnings []string
}

func packageRequirements(bundle agentsource.Bundle) PackageRequirements {
	result := PackageRequirements{Secrets: []string{}, DeferredBindings: []string{}, DshPlugins: []PackageDshPluginRequirement{}}
	if bundle.Definition == nil {
		return result
	}
	var root map[string]any
	encoded, _ := json.Marshal(bundle.Definition)
	_ = json.Unmarshal(encoded, &root)
	secrets := map[string]bool{}
	var walk func(any)
	walk = func(value any) {
		switch item := value.(type) {
		case map[string]any:
			if ref, ok := item["secret_ref"].(string); ok {
				secrets[ref] = true
			}
			for _, child := range item {
				walk(child)
			}
		case []any:
			for _, child := range item {
				walk(child)
			}
		}
	}
	walk(root["configuration"])
	walk(root["dsh_plugins"])
	for ref := range secrets {
		result.Secrets = append(result.Secrets, ref)
	}
	bindings, _ := root["bindings"].(map[string]any)
	runtime, _ := bindings["runtime"].(map[string]any)
	result.RuntimeProvider, _ = runtime["provider"].(string)
	for key, value := range bindings {
		if key == "runtime" {
			continue
		}
		if items, ok := value.([]any); ok && len(items) == 0 {
			continue
		}
		result.DeferredBindings = append(result.DeferredBindings, "/bindings/"+key)
	}
	if plugins, ok := root["dsh_plugins"].([]any); ok {
		for _, item := range plugins {
			encoded, _ := json.Marshal(item)
			var plugin PackageDshPluginRequirement
			_ = json.Unmarshal(encoded, &plugin)
			result.DshPlugins = append(result.DshPlugins, plugin)
		}
	}
	if access, ok := root["access"].(map[string]any); ok {
		if targets, ok := access["invocation_targets"].([]any); ok {
			for _, target := range targets {
				item, _ := target.(map[string]any)
				if item["target_type"] != "workspace" {
					result.DeferredBindings = append(result.DeferredBindings, "/access")
					break
				}
			}
		}
	}
	if disabled, ok := root["disabled_runtime_skills"].([]any); ok {
		for _, item := range disabled {
			skill, _ := item.(map[string]any)
			if runtime["ref"] == nil || skill["runtime_ref"] != runtime["ref"] {
				result.DeferredBindings = append(result.DeferredBindings, "/disabled_runtime_skills")
				break
			}
		}
	}
	sort.Strings(result.Secrets)
	sort.Strings(result.DeferredBindings)
	return result
}

func preparePackageConfiguration(request *CreateGitHubAgentRequest, raw map[string]json.RawMessage, bundle agentsource.Bundle) (packageConfiguration, error) {
	result := packageConfiguration{}
	if bundle.Definition == nil {
		return result, nil
	}
	requirements := packageRequirements(bundle)
	deferred := map[string]bool{}
	for _, field := range request.DeferredBindings {
		deferred[field] = true
	}
	for _, field := range requirements.DeferredBindings {
		if !deferred[field] {
			return result, sourceRequestError(http.StatusUnprocessableEntity, "confirm destination setup after creation for "+field)
		}
		result.warnings = append(result.warnings, field+" was explicitly deferred; configure it on the destination Agent before use")
	}
	for _, ref := range requirements.Secrets {
		if _, ok := request.Secrets[ref]; !ok {
			return result, sourceRequestError(http.StatusUnprocessableEntity, "provide a value for secret reference "+ref)
		}
	}
	encoded, err := json.Marshal(bundle.Definition)
	if err != nil {
		return result, err
	}
	var root map[string]any
	if err := json.Unmarshal(encoded, &root); err != nil {
		return result, err
	}
	var resolve func(any) any
	resolve = func(value any) any {
		switch item := value.(type) {
		case map[string]any:
			if ref, ok := item["secret_ref"].(string); ok {
				return request.Secrets[ref]
			}
			for key, child := range item {
				item[key] = resolve(child)
			}
		case []any:
			for index, child := range item {
				item[index] = resolve(child)
			}
		}
		return value
	}
	root["configuration"] = resolve(root["configuration"])
	root["dsh_plugins"] = resolve(root["dsh_plugins"])
	if deferred["/disabled_runtime_skills"] {
		delete(root, "disabled_runtime_skills")
	}
	encoded, err = json.Marshal(root)
	if err != nil {
		return result, err
	}
	if err := json.Unmarshal(encoded, &result); err != nil {
		return result, sourceRequestError(http.StatusUnprocessableEntity, "resolved package configuration is invalid")
	}
	result.pluginBindings = request.DshPluginBindings
	if err := validatePackagePluginBindings(result.DshPlugins, result.pluginBindings); err != nil {
		return result, err
	}
	configuration, _ := json.Marshal(root["configuration"])
	instance := request.CreateAgentRequest
	request.CreateAgentRequest = CreateAgentRequest{}
	if err := json.Unmarshal(configuration, &request.CreateAgentRequest); err != nil {
		return result, sourceRequestError(http.StatusUnprocessableEntity, "resolved configuration has invalid field types")
	}
	request.Name, request.Description, request.RuntimeID = instance.Name, instance.Description, instance.RuntimeID
	request.SkillIDs = instance.SkillIDs
	if config, ok := root["configuration"].(map[string]any); ok {
		for key, value := range config {
			raw[key], _ = json.Marshal(value)
		}
	}
	if access, ok := root["access"].(map[string]any); ok && !deferred["/access"] {
		encoded, _ := json.Marshal(access)
		if err := json.Unmarshal(encoded, &request.CreateAgentRequest); err != nil {
			return result, err
		}
		raw["invocation_targets"], _ = json.Marshal(access["invocation_targets"])
	}
	return result, nil
}

func (config packageConfiguration) validateRuntime(runtime db.AgentRuntime) error {
	if len(config.DshPlugins) > 0 && (runtime.Provider != "dsh" || runtime.RuntimeMode != "cloud") {
		return sourceRequestError(http.StatusUnprocessableEntity, "plugin recipes require a cloud DSH runtime")
	}
	if provider := config.Bindings.Runtime.Provider; provider != "" && provider != runtime.Provider {
		return sourceRequestError(http.StatusUnprocessableEntity, fmt.Sprintf("manifest requires runtime provider %s", provider))
	}
	if mode := config.Bindings.Runtime.RuntimeMode; mode != "" && mode != runtime.RuntimeMode {
		return sourceRequestError(http.StatusUnprocessableEntity, fmt.Sprintf("manifest requires runtime mode %s", mode))
	}
	for _, item := range config.DisabledRuntimeSkills {
		if item.Provider != runtime.Provider {
			return sourceRequestError(http.StatusUnprocessableEntity, "disabled runtime skill provider does not match selected runtime")
		}
	}
	return nil
}

func optionalPackageBool(value *bool) pgtype.Bool {
	if value == nil {
		return pgtype.Bool{}
	}
	return pgtype.Bool{Bool: *value, Valid: true}
}

// apply executes inside the same transaction as the Agent and its skills.
// It writes definition data only. External identities and credential issuance
// stay in their existing destination workflows and require explicit deferral.
func (definition packageConfiguration) apply(ctx context.Context, q *db.Queries, agent db.Agent, actorID pgtype.UUID) error {
	if err := definition.applyDshPlugins(ctx, q, agent, actorID); err != nil {
		return err
	}
	c := definition.Configuration
	params := db.UpdateAgentParams{ID: agent.ID, DispatchAlwaysNewIssue: optionalPackageBool(c.DispatchAlwaysNewIssue)}
	if c.DispatchPromptOverrides != nil {
		params.DispatchPromptOverrides, _ = json.Marshal(c.DispatchPromptOverrides)
	}
	if _, err := q.UpdateAgent(ctx, params); err != nil {
		return err
	}
	if c.ChatSessionResume != nil {
		if err := q.UpdateAgentChatSessionResume(ctx, agent.ID, *c.ChatSessionResume); err != nil {
			return err
		}
	}
	if c.TaskFinishedLoopEnabled != nil {
		if err := q.UpdateAgentTaskFinishedLoop(ctx, agent.ID, *c.TaskFinishedLoopEnabled); err != nil {
			return err
		}
	}
	if _, err := q.UpdateAgentDingTalkResponsePolicy(ctx, db.UpdateAgentDingTalkResponsePolicyParams{ID: agent.ID, InboundCoordinator: optionalPackageBool(c.InboundCoordinator), ShowAiTag: optionalPackageBool(c.DingTalkShowAITag), ResponseEnabled: optionalPackageBool(c.DingTalkResponseEnabled)}); err != nil {
		return err
	}
	if err := q.UpdateAgentSceneMemoryFlags(ctx, db.UpdateAgentSceneMemoryFlagsParams{ID: agent.ID, WriteEnabled: optionalPackageBool(c.SceneMemoryWriteEnabled), RecallEnabled: optionalPackageBool(c.SceneMemoryRecallEnabled), UIEnabled: optionalPackageBool(c.SceneMemoryUIEnabled), BootstrapEnabled: optionalPackageBool(c.SceneMemoryBootstrapEnabled)}); err != nil {
		return err
	}
	if c.Persona != nil || c.ReplyTone != nil {
		voice, err := q.GetAgentVoice(ctx, agent.ID)
		if err != nil {
			return err
		}
		persona, tone := voice.Persona, voice.ReplyTone
		if c.Persona != nil {
			persona = *c.Persona
		}
		if c.ReplyTone != nil {
			tone = *c.ReplyTone
		}
		if err := q.UpdateAgentVoice(ctx, db.UpdateAgentVoiceParams{ID: agent.ID, Persona: persona, ReplyTone: tone}); err != nil {
			return err
		}
	}
	if definition.DisabledRuntimeSkills != nil {
		skills := []DisabledRuntimeSkill{}
		for _, entry := range definition.DisabledRuntimeSkills {
			item := entry.DisabledRuntimeSkill
			item.RuntimeID = uuidToString(agent.RuntimeID)
			skills = append(skills, item)
		}
		encoded, _ := json.Marshal(skills)
		if _, err := q.UpdateAgentDisabledRuntimeSkills(ctx, db.UpdateAgentDisabledRuntimeSkillsParams{ID: agent.ID, DisabledRuntimeSkills: encoded}); err != nil {
			return err
		}
	}
	reusableLabels := []pgtype.UUID{}
	if definition.OKRs != nil {
		var err error
		reusableLabels, err = q.ListAgentOKRLabelIDs(ctx, db.ListAgentOKRLabelIDsParams{WorkspaceID: agent.WorkspaceID, AgentID: agent.ID})
		if err != nil {
			return err
		}
		if err := q.DeleteAgentOKRsByAgent(ctx, db.DeleteAgentOKRsByAgentParams{WorkspaceID: agent.WorkspaceID, AgentID: agent.ID}); err != nil {
			return err
		}
	}
	for index, entry := range definition.OKRs {
		label, err := q.UpsertAgentOKRLabel(ctx, db.UpsertAgentOKRLabelParams{WorkspaceID: agent.WorkspaceID, Name: agentOKRObjectivePrefix + strings.TrimSpace(entry.Objective), Description: agentOKRLabelDescription, Color: agentOKRObjectiveColor, ReusableLabelIds: reusableLabels})
		if err != nil {
			return sourceRequestError(http.StatusConflict, "an OKR objective label already exists; choose a distinct objective")
		}
		objective, err := q.CreateAgentOKR(ctx, db.CreateAgentOKRParams{WorkspaceID: agent.WorkspaceID, AgentID: agent.ID, Kind: "objective", LabelID: label.ID, Position: int32(index)})
		if err != nil {
			return err
		}
		for position, text := range entry.KeyResults {
			label, err := q.UpsertAgentOKRLabel(ctx, db.UpsertAgentOKRLabelParams{WorkspaceID: agent.WorkspaceID, Name: agentOKRKeyResultPrefix + strings.TrimSpace(text), Description: agentOKRLabelDescription, Color: agentOKRKeyResultColor, ReusableLabelIds: reusableLabels})
			if err != nil {
				return sourceRequestError(http.StatusConflict, "an OKR key-result label already exists; choose a distinct key result")
			}
			if _, err := q.CreateAgentOKR(ctx, db.CreateAgentOKRParams{WorkspaceID: agent.WorkspaceID, AgentID: agent.ID, Kind: "key_result", ParentID: objective.ID, LabelID: label.ID, Position: int32(position)}); err != nil {
				return err
			}
		}
	}
	if definition.A2A != nil {
		a := definition.A2A
		endpoint, err := q.GetAgentA2AEndpointByAgent(ctx, agent.ID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if errors.Is(err, pgx.ErrNoRows) {
			endpoint.PublicAgentID = uuid.NewString()
			endpoint.CardName = agent.Name
			endpoint.CardVersion = "1.0.0"
			endpoint.CardSkills = json.RawMessage(`[]`)
		}
		if a.Enabled != nil {
			endpoint.Enabled = *a.Enabled
		}
		if a.CardName != nil {
			endpoint.CardName = *a.CardName
		}
		if a.CardDescription != nil {
			endpoint.CardDescription = *a.CardDescription
		}
		if a.CardVersion != nil {
			endpoint.CardVersion = *a.CardVersion
		}
		if a.CardSkills != nil {
			endpoint.CardSkills = a.CardSkills
		}
		if _, err := q.UpsertAgentA2AEndpoint(ctx, db.UpsertAgentA2AEndpointParams{WorkspaceID: agent.WorkspaceID, AgentID: agent.ID, OwnerUserID: agent.OwnerID, ActorUserID: actorID, PublicAgentID: endpoint.PublicAgentID, Enabled: endpoint.Enabled, CardName: endpoint.CardName, CardDescription: endpoint.CardDescription, CardVersion: endpoint.CardVersion, CardSkills: endpoint.CardSkills}); err != nil {
			return err
		}
		clients, err := q.ListAgentA2AClientsForOwner(ctx, db.ListAgentA2AClientsForOwnerParams{WorkspaceID: agent.WorkspaceID, AgentID: agent.ID, OwnerUserID: agent.OwnerID})
		if err != nil {
			return err
		}
		mappings, err := readPackageClientMappings(ctx, q, agent.ID)
		if err != nil {
			return err
		}
		byID := map[string]db.A2aClient{}
		for _, client := range clients {
			byID[uuidToString(client.ID)] = client
		}
		seen := map[string]bool{}
		for _, item := range a.Clients {
			if seen[item.Key] {
				return sourceRequestError(http.StatusUnprocessableEntity, "manifest A2A client keys must be unique")
			}
			seen[item.Key] = true
			rate, concurrent := pgtype.Int4{}, pgtype.Int4{}
			if item.RateLimit != nil {
				rate = pgtype.Int4{Int32: *item.RateLimit, Valid: true}
			}
			if item.MaxConcurrent != nil {
				concurrent = pgtype.Int4{Int32: *item.MaxConcurrent, Valid: true}
			}
			client, exists := byID[mappings[item.Key]]
			if !exists {
				client, err = q.CreateAgentA2AClientForOwner(ctx, db.CreateAgentA2AClientForOwnerParams{WorkspaceID: agent.WorkspaceID, AgentID: agent.ID, OwnerUserID: agent.OwnerID, ActorUserID: actorID, Name: item.Name, Scopes: item.Scopes, RateLimitPerMinute: rate, MaxConcurrentTasks: concurrent})
				if err != nil {
					return err
				}
			}
			mappings[item.Key] = uuidToString(client.ID)
			if client.Status == "revoked" && item.Status != "revoked" {
				return sourceRequestError(http.StatusConflict, "revoked A2A clients cannot be reactivated by importing a package")
			}
			if item.Status == "revoked" {
				if _, err := q.RevokeAgentA2AClientForOwner(ctx, db.RevokeAgentA2AClientForOwnerParams{ClientID: client.ID, WorkspaceID: agent.WorkspaceID, AgentID: agent.ID, OwnerUserID: agent.OwnerID, ActorUserID: actorID}); err != nil {
					return err
				}
			} else {
				if _, err := q.UpdateAgentA2AClientForOwner(ctx, db.UpdateAgentA2AClientForOwnerParams{ClientID: client.ID, WorkspaceID: agent.WorkspaceID, AgentID: agent.ID, OwnerUserID: agent.OwnerID, ActorUserID: actorID, Name: item.Name, Status: item.Status, Scopes: item.Scopes, RateLimitPerMinute: rate, MaxConcurrentTasks: concurrent}); err != nil {
					return err
				}
			}
		}
		if a.Clients != nil {
			for key, clientID := range mappings {
				client, exists := byID[clientID]
				if exists && !seen[key] && client.Status != "revoked" {
					if _, err := q.RevokeAgentA2AClientForOwner(ctx, db.RevokeAgentA2AClientForOwnerParams{ClientID: client.ID, WorkspaceID: agent.WorkspaceID, AgentID: agent.ID, OwnerUserID: agent.OwnerID, ActorUserID: actorID}); err != nil {
						return err
					}
				}
			}
			encoded, _ := json.Marshal(mappings)
			if err := q.UpdateAgentPackageClientMappings(ctx, db.UpdateAgentPackageClientMappingsParams{AgentID: agent.ID, Mappings: encoded}); err != nil {
				return err
			}
		}
	}
	return nil
}

func (h *Handler) hydrateImportedAgent(ctx context.Context, response *AgentResponse, id pgtype.UUID) {
	h.hydrateChatSessionResume(ctx, response, id)
	h.hydrateDingTalkResponsePolicy(ctx, response, id)
	h.hydrateTaskFinishedLoop(ctx, response, id)
	h.hydrateSceneMemoryFlags(ctx, response, id)
	h.hydrateAgentVoice(ctx, response, id)
}

// packageDefinitionPreview preserves reviewable values and removes private
// runtime strings using the same policy as portable exports.
func packageDefinitionPreview(bundle agentsource.Bundle) map[string]any {
	encoded, _ := json.Marshal(bundle.Definition)
	preview := map[string]any{}
	_ = json.Unmarshal(encoded, &preview)
	if configuration, ok := preview["configuration"].(map[string]any); ok {
		notes := &agentExportNotes{}
		for _, key := range []string{"custom_env", "custom_args", "runtime_config", "mcp_config"} {
			if value, exists := configuration[key]; exists {
				configuration[key] = notes.configValue(value, "/configuration/"+key, key)
			}
		}
	}
	if plugins, ok := preview["dsh_plugins"].([]any); ok {
		for _, item := range plugins {
			if plugin, ok := item.(map[string]any); ok {
				plugin["config"] = previewPackagePluginConfig(plugin["config"], "/dsh_plugins/"+fmt.Sprint(plugin["ref"])+"/config", "config")
			}
		}
	}
	return preview
}

func readPackageClientMappings(ctx context.Context, q *db.Queries, agentID pgtype.UUID) (map[string]string, error) {
	encoded, err := q.GetAgentPackageClientMappings(ctx, agentID)
	if err != nil {
		return nil, err
	}
	result := map[string]string{}
	if err := json.Unmarshal(encoded, &result); err != nil {
		return nil, err
	}
	return result, nil
}
