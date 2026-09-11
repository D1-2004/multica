package handler

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/integrations/agentmessagerouter"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type agentExportNotes struct {
	SecretAliases map[string]string `json:"-"`
	Resources []map[string]string `json:"resources"`
	Secrets   []map[string]string `json:"secrets"`
	Notes     []string            `json:"notes"`
}

func (n *agentExportNotes) resource(kind, id, label string) map[string]any {
	digest := sha256.Sum256([]byte(kind + ":" + id))
	ref := fmt.Sprintf("%s-%x", kind, digest[:8])
	for _, entry := range n.Resources {
		if entry["ref"] == ref {
			return map[string]any{"ref": ref}
		}
	}
	n.Resources = append(n.Resources, map[string]string{"ref": ref, "kind": kind, "label": label})
	return map[string]any{"ref": ref}
}

func (n *agentExportNotes) secret(path string) map[string]any {
	digest := sha256.Sum256([]byte(path))
	ref := n.SecretAliases[path]
	if ref == "" { ref = fmt.Sprintf("secret-%x", digest[:8]) }
	n.Secrets = append(n.Secrets, map[string]string{"ref": ref, "path": path})
	return map[string]any{"secret_ref": ref}
}

var exportCLIFlag = regexp.MustCompile(`^--?[A-Za-z][A-Za-z0-9_-]*$`)
var exportCommand = regexp.MustCompile(`^[A-Za-z0-9_./-]+$`)
var exportMCPTypePath = regexp.MustCompile(`^/configuration/mcp_config/(mcpServers|mcp)/[^/]+/type$`)

// Config strings are private unless their position has an explicit public
// meaning. Environment values, argument values, headers and provider extension
// strings become references, including secrets without recognizable key names.
func (n *agentExportNotes) configValue(value any, path, field string) any {
	switch v := value.(type) {
	case map[string]any:
		result := map[string]any{}
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			child := path + "/" + strings.ReplaceAll(strings.ReplaceAll(key, "~", "~0"), "/", "~1")
			if field == "custom_env" || field == "env" || field == "environment" || field == "headers" {
				result[key] = n.secret(child)
			} else {
				result[key] = n.configValue(v[key], child, key)
			}
		}
		return result
	case []any:
		result := make([]any, len(v))
		for i, item := range v {
			itemField := field
			if field == "command" && i > 0 {
				itemField = "args"
			}
			result[i] = n.configValue(item, fmt.Sprintf("%s/%d", path, i), itemField)
		}
		return result
	case string:
		if exportMCPTypePath.MatchString(path) {
			return v
		}
		switch field {
		case "type", "transport", "mode":
			if v == "stdio" || v == "http" || v == "sse" || v == "streamable-http" || v == "local" || v == "remote" || v == "gateway" {
				return v
			}
		case "url", "sink_url":
			parsed, err := url.Parse(v)
			if v == "" || (err == nil && parsed.User == nil && parsed.RawQuery == "" && parsed.Fragment == "" && (parsed.Scheme == "https" || parsed.Scheme == "http")) {
				return v
			}
		case "host":
			if !strings.ContainsAny(v, "@/?# \t\r\n") {
				return v
			}
		case "command":
			if exportCommand.MatchString(v) {
				return v
			}
		case "args", "custom_args":
			if exportCLIFlag.MatchString(v) {
				return v
			}
		}
		return n.secret(path)
	default:
		return value
	}
}

func newAgentExportNotes() *agentExportNotes {
	return &agentExportNotes{Resources: []map[string]string{}, Secrets: []map[string]string{}, Notes: []string{
		"This package contains the Agent definition currently stored in Multica. Runtime history, platform system instructions and credentials are not included.",
		"Resource aliases require explicit binding in the destination workspace. Secret references replace environment values, argument values, headers and unclassified provider strings; this file never contains their values.",
		"GitHub execution identity is managed by the external identity service and must be checked and rebound separately. Git repository connections and release history are not Agent configuration.",
		"Local ZIP and Git creation use the same validated package. Select the destination runtime, supply secret references and explicitly acknowledge external bindings that will be configured after creation.",
	}}
}

func exportPackageConfiguration(ctx context.Context, q *db.Queries, agent db.Agent, notes *agentExportNotes) (map[string]any, error) {
	config := map[string]any{
		"avatar_url": agent.AvatarUrl.String, "model": agent.Model.String, "thinking_level": agent.ThinkingLevel.String, "service_tier": agent.ServiceTier.String,
		"max_concurrent_tasks": agent.MaxConcurrentTasks, "dispatch_always_new_issue": agent.DispatchAlwaysNewIssue,
		"composio_toolkit_allowlist": agent.ComposioToolkitAllowlist,
	}
	for _, field := range []struct {
		name     string
		data     []byte
		fallback any
		private  bool
	}{
		{"custom_env", agent.CustomEnv, map[string]any{}, true}, {"custom_args", agent.CustomArgs, []any{}, true},
		{"runtime_config", agent.RuntimeConfig, map[string]any{}, true}, {"mcp_config", agent.McpConfig, nil, true},
		{"dispatch_prompt_overrides", agent.DispatchPromptOverrides, map[string]any{}, false},
	} {
		value := field.fallback
		if len(field.data) > 0 && string(field.data) != "null" {
			if err := json.Unmarshal(field.data, &value); err != nil {
				return nil, fmt.Errorf("invalid stored %s", field.name)
			}
		}
		if field.private {
			value = notes.configValue(value, "/configuration/"+field.name, field.name)
		}
		config[field.name] = value
	}
	voice, err := q.GetAgentVoice(ctx, agent.ID)
	if err != nil {
		return nil, err
	}
	config["persona"], config["reply_tone"] = voice.Persona, voice.ReplyTone
	policy, err := q.GetAgentDingTalkResponsePolicy(ctx, agent.ID)
	if err != nil {
		return nil, err
	}
	config["inbound_coordinator"], config["dingtalk_show_ai_tag"], config["dingtalk_response_enabled"] = policy.InboundCoordinator, policy.DingtalkShowAiTag, policy.DingtalkResponseEnabled
	resume, err := q.GetAgentChatSessionResume(ctx, agent.ID)
	if err != nil {
		return nil, err
	}
	config["chat_session_resume"] = resume
	events, err := q.GetAgentPackageEventTriggerEnabled(ctx,db.GetAgentPackageEventTriggerEnabledParams{AgentID:agent.ID,WorkspaceID:agent.WorkspaceID})
	if err != nil { return nil,err }
	config["event_trigger_enabled"] = events
	loop, err := q.GetAgentTaskFinishedLoop(ctx, agent.ID)
	if err != nil {
		return nil, err
	}
	config["task_finished_loop_enabled"] = loop
	flags, err := q.GetAgentSceneMemoryFlags(ctx, agent.ID)
	if err != nil {
		return nil, err
	}
	config["scene_memory_write_enabled"], config["scene_memory_recall_enabled"], config["scene_memory_ui_enabled"], config["scene_memory_bootstrap_enabled"] = flags.WriteEnabled, flags.RecallEnabled, flags.UIEnabled, flags.BootstrapEnabled
	return config, nil
}

func exportPackageBindings(ctx context.Context, q *db.Queries, agent db.Agent, notes *agentExportNotes) (map[string]any, error) {
	bindings := map[string]any{"runner": nil, "enterprise_identity": nil, "dingtalk_account": nil, "bots": []any{}}
	if agent.RuntimeID.Valid {
		runtime, err := q.GetAgentRuntimeForWorkspace(ctx, db.GetAgentRuntimeForWorkspaceParams{ID: agent.RuntimeID, WorkspaceID: agent.WorkspaceID})
		if err != nil {
			return nil, err
		}
		label := runtime.Name
		if runtime.CustomName.Valid && runtime.CustomName.String != "" {
			label = runtime.CustomName.String
		}
		ref := notes.resource("runtime", uuidToString(runtime.ID), label)
		ref["provider"], ref["runtime_mode"] = runtime.Provider, agent.RuntimeMode
		bindings["runtime"] = ref
	}
	runners, err := q.ListAgentRunnerBindings(ctx, db.ListAgentRunnerBindingsParams{WorkspaceID: agent.WorkspaceID, AgentID: agent.ID})
	if err != nil {
		return nil, err
	}
	if len(runners) > 1 {
		return nil, errors.New("multiple runner bindings cannot be represented by this manifest version")
	}
	if len(runners) == 1 {
		bindings["runner"] = notes.resource("runner", uuidToString(runners[0].MachineID), runners[0].Name)
	}
	identity, err := q.GetAgentEnterpriseIdentity(ctx, db.GetAgentEnterpriseIdentityParams{WorkspaceID: agent.WorkspaceID, AgentID: agent.ID})
	if err == nil {
		bindings["enterprise_identity"] = notes.resource("enterprise-identity", uuidToString(identity.ID)+":"+identity.RawEmpID, identity.DisplayName)
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	account, err := q.GetDingTalkAccountBindingByAgent(ctx, db.GetDingTalkAccountBindingByAgentParams{WorkspaceID: agent.WorkspaceID, AgentID: agent.ID})
	if err == nil {
		var config agentmessagerouter.DingTalkAccountConfig
		if err := json.Unmarshal(account.Config,&config); err != nil { return nil, errors.New("invalid stored DingTalk account configuration") }
		bindings["dingtalk_account"] = notes.resource("dingtalk-account", uuidToString(account.ID)+":"+config.RouterTenantID+":"+config.RouterAccountID, "DingTalk account")
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	bots := []map[string]any{}
	for _, platform := range []string{"dingtalk", "lark", "slack", "wecom"} {
		installations, err := q.ListChannelInstallationsByWorkspace(ctx, db.ListChannelInstallationsByWorkspaceParams{WorkspaceID: agent.WorkspaceID, ChannelType: platform})
		if err != nil {
			return nil, err
		}
		for _, installation := range installations {
			if installation.AgentID != agent.ID {
				continue
			}
			ref := notes.resource(platform+"-bot", uuidToString(installation.ID), platform+" bot")
			ref["platform"] = platform
			bots = append(bots, ref)
		}
	}
	bindings["bots"] = bots
	return bindings, nil
}

func exportPackageDisabledRuntimeSkills(ctx context.Context, q *db.Queries, agent db.Agent, notes *agentExportNotes) ([]map[string]any, error) {
	disabled := []map[string]any{}
	var storedSkills []DisabledRuntimeSkill
	if len(agent.DisabledRuntimeSkills) > 0 {
		if err := json.Unmarshal(agent.DisabledRuntimeSkills, &storedSkills); err != nil {
			return nil, errors.New("invalid stored runtime skills")
		}
	}
	for _, skill := range storedSkills {
		ref := notes.resource("runtime", skill.RuntimeID, skill.Provider)
		item := map[string]any{"runtime_ref": ref["ref"], "provider": skill.Provider, "root": skill.Root, "key": skill.Key}
		if skill.Name != "" {
			item["name"] = skill.Name
		}
		if skill.Plugin != "" {
			item["plugin"] = skill.Plugin
		}
		disabled = append(disabled, item)
	}
	return disabled, nil
}

func exportPackagePlugins(ctx context.Context, q *db.Queries, agent db.Agent, notes *agentExportNotes) ([]map[string]any, error) {
	plugins, err := q.ListDshPluginsForAgent(ctx, db.ListDshPluginsForAgentParams{WorkspaceID: agent.WorkspaceID, AgentID: agent.ID})
	if err != nil {
		return nil, err
	}
	pluginRefs := []map[string]any{}
	for _, plugin := range plugins {
		ref := notes.resource("dsh-plugin", uuidToString(plugin.ID), plugin.PackageName)
		ref["enabled"] = plugin.Enabled
		pluginRefs = append(pluginRefs, ref)
	}
	return pluginRefs, nil
}

func exportPackageOKRs(ctx context.Context, q *db.Queries, agent db.Agent, notes *agentExportNotes) ([]map[string]any, error) {
	okrRows, err := q.ListAgentOKRs(ctx, db.ListAgentOKRsParams{WorkspaceID: agent.WorkspaceID, AgentID: agent.ID})
	if err != nil {
		return nil, err
	}
	okrs := []map[string]any{}
	objectives := map[string]int{}
	for _, row := range okrRows {
		if row.Kind == "objective" {
			objectives[uuidToString(row.ID)] = len(okrs)
			okrs = append(okrs, map[string]any{"objective":agentOKRText(row), "key_results":[]string{}})
		} else if row.Kind == "key_result" {
			index, ok := objectives[uuidToString(row.ParentID)]; if !ok { return nil, errors.New("invalid stored OKR parent") }
			okrs[index]["key_results"] = append(okrs[index]["key_results"].([]string), agentOKRText(row))
		}
	}
	return okrs, nil
}

func exportPackageAccess(ctx context.Context, q *db.Queries, agent db.Agent, notes *agentExportNotes) (map[string]any, error) {
	targetRows, err := q.ListAgentInvocationTargets(ctx, agent.ID)
	if err != nil {
		return nil, err
	}
	targets := []map[string]any{}
	for _, target := range targetRows {
		item := map[string]any{"target_type": target.TargetType}
		if target.TargetType != "workspace" {
			label := target.TargetType
			if target.TargetType == "member" {
				member, err := q.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{UserID: target.TargetID, WorkspaceID: agent.WorkspaceID})
				if err == nil {
					user, err := q.GetUser(ctx, member.UserID)
					if err != nil {
						return nil, err
					}
					label = user.Name
				} else if !errors.Is(err, pgx.ErrNoRows) {
					return nil, err
				}
			}
			item["ref"] = notes.resource(target.TargetType, uuidToString(target.TargetID), label)["ref"]
		}
		targets = append(targets, item)
	}
	return map[string]any{"permission_mode": agent.PermissionMode, "invocation_targets": targets}, nil
}

func exportPackageA2A(ctx context.Context, q *db.Queries, agent db.Agent, notes *agentExportNotes) (map[string]any, error) {
	endpoint, err := q.GetAgentA2AEndpointByAgent(ctx, agent.ID)
	if err == nil {
		var cardSkills any = []any{}
		if len(endpoint.CardSkills) > 0 {
			if err := json.Unmarshal(endpoint.CardSkills, &cardSkills); err != nil {
				return nil, errors.New("invalid stored A2A card skills")
			}
		}
		clients, err := q.ListAgentA2AClientsForOwner(ctx, db.ListAgentA2AClientsForOwnerParams{OwnerUserID: agent.OwnerID, WorkspaceID: agent.WorkspaceID, AgentID: agent.ID})
		if err != nil {
			return nil, err
		}
		mappings, err := readPackageClientMappings(ctx, q, agent.ID)
		if err != nil {
			return nil, err
		}
		keys := map[string]string{}
		for key, clientID := range mappings {
			keys[clientID] = key
		}
		clientSpecs := []map[string]any{}
		for _, client := range clients {
			digest := sha256.Sum256([]byte(uuidToString(client.ID)))
			key := keys[uuidToString(client.ID)]
			if key == "" {
				key = fmt.Sprintf("client-%x", digest[:8])
			}
			clientSpecs = append(clientSpecs, map[string]any{"key": key, "name": client.Name, "status": client.Status, "scopes": client.Scopes, "rate_limit_per_minute": client.RateLimitPerMinute, "max_concurrent_tasks": client.MaxConcurrentTasks})
		}
		return map[string]any{"enabled": endpoint.Enabled, "card_name": endpoint.CardName, "card_description": endpoint.CardDescription, "card_version": endpoint.CardVersion, "card_skills": cardSkills, "clients": clientSpecs}, nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	} else {
		return map[string]any{"enabled": false, "clients": []any{}}, nil
	}
}
