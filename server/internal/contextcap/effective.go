package contextcap

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Context layers of an effective context, outermost first: the agent's
// global configuration, then its tenant (org), then the group scene, then
// the person. A nearer layer replaces an outer layer's prompt component or
// custom MCP server of the same name (DSH "nearest layer wins");
// connectors and skills are a union.
const (
	LayerGlobal = "global"
	LayerOrg    = ScopeOrg
	LayerScene  = ScopeScene
	LayerPerson = ScopePerson
)

// layerRank orders layers outermost first; unknown layers sort last.
func layerRank(layer string) int {
	switch layer {
	case LayerGlobal:
		return 0
	case LayerOrg:
		return 1
	case LayerScene:
		return 2
	case LayerPerson:
		return 3
	default:
		return 4
	}
}

// ContextLayer is the stored configuration of one layer as MergeContext
// sees it. ConnectorIDs and SkillIDs are the resources switched on in the
// layer (for scope layers: enabled bindings of offered resources; for the
// global layer: the agent's grants and agent skills). MCPConfig is the
// layer's custom MCP servers in the agent mcp_config format
// ({"mcpServers": {<name>: <server>}}), nil when none. Prompts may include
// disabled components and a scope layer's servers may carry
// "disabled": true; MergeContext leaves both out.
type ContextLayer struct {
	Layer        string
	ScopeKey     string
	Prompts      []PromptComponent
	ConnectorIDs []string
	SkillIDs     []string
	MCPConfig    json.RawMessage
}

// EffectivePrompt is one prompt component in an effective context.
type EffectivePrompt struct {
	Name  string
	Order int
	Text  string
	Layer string
	// OverriddenBy is the nearer layer whose component of the same name
	// replaces this one; "" when this component applies.
	OverriddenBy string
}

// EffectiveResource is one connector or skill in an effective context.
// Layer is the layer that brings it: global when the agent has it
// globally, else the nearest layer that switches it on.
type EffectiveResource struct {
	ID    string
	Layer string
}

// EffectiveMCPServer is one custom MCP server in an effective context.
type EffectiveMCPServer struct {
	Name   string
	Layer  string
	Config json.RawMessage
	// OverriddenBy is the nearer layer whose server of the same name
	// replaces this one; "" when this server applies.
	OverriddenBy string
}

// EffectiveContext is the merged context of a set of layers
// (MergeContext). Prompts and MCPServers list every component, the
// overridden ones included (OverriddenBy set), so a preview can show them;
// AppliedPrompts and AppliedMCPServers return what a run gets.
type EffectiveContext struct {
	// Prompts are ordered by order, then layer (outermost first), then name.
	Prompts []EffectivePrompt
	// Connectors and Skills keep first-appearance order, outermost layer
	// first.
	Connectors []EffectiveResource
	Skills     []EffectiveResource
	// MCPServers are ordered by name, then layer.
	MCPServers []EffectiveMCPServer
	// InvalidMCPLayers lists layers whose MCPConfig is not a JSON object with
	// an object-valued mcpServers; their servers are left out.
	InvalidMCPLayers []string
}

// MergeContext merges layers into the effective context. It is the one
// merge used by both the admin preview and the claim-time runtime. Layers
// are ordered outermost first by their Layer (a stable sort, so a caller
// may pass them in any order); within one layer, prompt names and server
// names are unique by construction.
//
//   - Prompt components: nearest layer wins by name; the losers are kept with
//     OverriddenBy. A disabled component takes no part: it neither applies
//     nor overrides an outer component of the same name.
//   - Custom MCP servers: nearest layer wins by server name, likewise. A
//     scope layer's server with "disabled": true takes no part either, and
//     the "disabled" key is dropped from the servers that apply
//     (scopeMCPServer). The global layer (the agent's own mcp_config) is
//     taken as it is, as the runtime passes it.
//   - Connectors and skills: union, deduplicated by id; the layer is global
//     when the global layer has it, else the nearest layer that has it (the
//     connector resolver's binding-layer rule).
func MergeContext(layers ...ContextLayer) EffectiveContext {
	ordered := append([]ContextLayer(nil), layers...)
	sort.SliceStable(ordered, func(i, j int) bool { return layerRank(ordered[i].Layer) < layerRank(ordered[j].Layer) })

	out := EffectiveContext{
		Prompts: []EffectivePrompt{}, Connectors: []EffectiveResource{}, Skills: []EffectiveResource{},
		MCPServers: []EffectiveMCPServer{},
	}

	promptWinner := map[string]string{}
	for _, layer := range ordered {
		for _, prompt := range layer.Prompts {
			if !prompt.Disabled {
				promptWinner[prompt.Name] = layer.Layer
			}
		}
	}
	for _, layer := range ordered {
		for _, prompt := range layer.Prompts {
			if prompt.Disabled {
				continue
			}
			item := EffectivePrompt{Name: prompt.Name, Order: prompt.Order, Text: prompt.Text, Layer: layer.Layer}
			if winner := promptWinner[prompt.Name]; winner != layer.Layer {
				item.OverriddenBy = winner
			}
			out.Prompts = append(out.Prompts, item)
		}
	}
	sort.SliceStable(out.Prompts, func(i, j int) bool {
		a, b := out.Prompts[i], out.Prompts[j]
		if a.Order != b.Order {
			return a.Order < b.Order
		}
		if ra, rb := layerRank(a.Layer), layerRank(b.Layer); ra != rb {
			return ra < rb
		}
		return a.Name < b.Name
	})

	out.Connectors = mergeResources(ordered, func(layer ContextLayer) []string { return layer.ConnectorIDs })
	out.Skills = mergeResources(ordered, func(layer ContextLayer) []string { return layer.SkillIDs })

	type layerServers struct {
		layer   string
		servers map[string]json.RawMessage
	}
	parsed := make([]layerServers, 0, len(ordered))
	serverWinner := map[string]string{}
	for _, layer := range ordered {
		servers, err := MCPServers(layer.MCPConfig)
		if err != nil {
			out.InvalidMCPLayers = append(out.InvalidMCPLayers, layer.Layer)
			continue
		}
		if layer.Layer != LayerGlobal {
			for name, config := range servers {
				cleaned, disabled := scopeMCPServer(config)
				if disabled {
					delete(servers, name)
					continue
				}
				servers[name] = cleaned
			}
		}
		parsed = append(parsed, layerServers{layer: layer.Layer, servers: servers})
		for name := range servers {
			serverWinner[name] = layer.Layer
		}
	}
	for _, layer := range parsed {
		for name, config := range layer.servers {
			item := EffectiveMCPServer{Name: name, Layer: layer.layer, Config: config}
			if winner := serverWinner[name]; winner != layer.layer {
				item.OverriddenBy = winner
			}
			out.MCPServers = append(out.MCPServers, item)
		}
	}
	sort.SliceStable(out.MCPServers, func(i, j int) bool {
		a, b := out.MCPServers[i], out.MCPServers[j]
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return layerRank(a.Layer) < layerRank(b.Layer)
	})
	return out
}

// MCPServerDisabledKey is the key of a scope's custom MCP server object that
// switches the server off ("disabled": true) without deleting it.
const MCPServerDisabledKey = "disabled"

// scopeMCPServer reads the switch of one scope custom MCP server: it reports
// whether the server object carries "disabled": true, and returns the object
// without the "disabled" key, which no runtime reads. A server that is not a
// JSON object, or has no such key, is returned unchanged and enabled.
func scopeMCPServer(config json.RawMessage) (json.RawMessage, bool) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(config, &fields); err != nil || fields == nil {
		return config, false
	}
	raw, ok := fields[MCPServerDisabledKey]
	if !ok {
		return config, false
	}
	disabled := bytes.Equal(bytes.TrimSpace(raw), []byte("true"))
	delete(fields, MCPServerDisabledKey)
	cleaned, err := json.Marshal(fields)
	if err != nil {
		return config, disabled
	}
	return cleaned, disabled
}

// mergeResources unions the ids ids(layer) returns across ordered layers.
func mergeResources(ordered []ContextLayer, ids func(ContextLayer) []string) []EffectiveResource {
	out := []EffectiveResource{}
	index := map[string]int{}
	for _, layer := range ordered {
		for _, id := range ids(layer) {
			at, seen := index[id]
			switch {
			case !seen:
				index[id] = len(out)
				out = append(out, EffectiveResource{ID: id, Layer: layer.Layer})
			case out[at].Layer != LayerGlobal:
				// A nearer scope layer brings it (global always keeps it).
				out[at].Layer = layer.Layer
			}
		}
	}
	return out
}

// AppliedPrompts returns the prompt components a run gets (not overridden),
// in effective order.
func (e EffectiveContext) AppliedPrompts() []EffectivePrompt {
	out := make([]EffectivePrompt, 0, len(e.Prompts))
	for _, prompt := range e.Prompts {
		if prompt.OverriddenBy == "" {
			out = append(out, prompt)
		}
	}
	return out
}

// AppliedMCPServers returns the custom MCP servers a run gets (not
// overridden), ordered by name.
func (e EffectiveContext) AppliedMCPServers() []EffectiveMCPServer {
	out := make([]EffectiveMCPServer, 0, len(e.MCPServers))
	for _, server := range e.MCPServers {
		if server.OverriddenBy == "" {
			out = append(out, server)
		}
	}
	return out
}

// ContextPromptHeading heads the block PromptBlock renders.
const ContextPromptHeading = "## 场域上下文"

// PromptBlock renders the applied prompt components as one instruction
// block: ContextPromptHeading, then "### <name>" and the text of each
// component in effective order. It returns "" when no component applies.
func (e EffectiveContext) PromptBlock() string {
	applied := e.AppliedPrompts()
	if len(applied) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(ContextPromptHeading)
	for _, prompt := range applied {
		b.WriteString("\n\n### ")
		b.WriteString(prompt.Name)
		b.WriteString("\n\n")
		b.WriteString(prompt.Text)
	}
	return b.String()
}

// MCPServers returns the mcpServers map of an agent mcp_config document.
// nil, empty and JSON null configs have no servers; a document that is not
// a JSON object, or whose mcpServers is not an object (or null), is
// ErrInvalidInput.
func MCPServers(config json.RawMessage) (map[string]json.RawMessage, error) {
	trimmed := bytes.TrimSpace(config)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return map[string]json.RawMessage{}, nil
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &document); err != nil || document == nil {
		return nil, ErrInvalidInput
	}
	raw := bytes.TrimSpace(document["mcpServers"])
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return map[string]json.RawMessage{}, nil
	}
	var servers map[string]json.RawMessage
	if err := json.Unmarshal(raw, &servers); err != nil || servers == nil {
		return nil, ErrInvalidInput
	}
	return servers, nil
}

// MergeMCPConfig layers the applied custom MCP servers of the scope layers
// (every applied server whose layer is not global) onto base, the agent's
// own mcp_config as the run already carries it: a scope server replaces a
// base server of the same name, and base keys other than mcpServers stay.
// With no scope server, base is returned unchanged. A malformed base is an
// error and base is returned unchanged with it, so a caller can keep the
// agent's servers.
func MergeMCPConfig(base json.RawMessage, e EffectiveContext) (json.RawMessage, error) {
	scoped := map[string]json.RawMessage{}
	for _, server := range e.AppliedMCPServers() {
		if server.Layer != LayerGlobal {
			scoped[server.Name] = server.Config
		}
	}
	if len(scoped) == 0 {
		return base, nil
	}
	document := map[string]json.RawMessage{}
	if trimmed := bytes.TrimSpace(base); len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null")) {
		if err := json.Unmarshal(trimmed, &document); err != nil || document == nil {
			return base, errors.Join(ErrInvalidInput, err)
		}
	}
	servers, err := MCPServers(base)
	if err != nil {
		return base, err
	}
	for name, config := range scoped {
		servers[name] = config
	}
	encoded, err := json.Marshal(servers)
	if err != nil {
		return base, err
	}
	document["mcpServers"] = encoded
	merged, err := json.Marshal(document)
	if err != nil {
		return base, err
	}
	return merged, nil
}

// LayerSelection names the scope layers of one effective context under
// OrgID (a tenant of the agent, see AgentTenants): the org layer when Org is
// set (OrgID must then be non-empty), the scene SceneID (an agent_scene id)
// and the person PersonKey when non-empty. The caller decides that OrgID is a
// tenant; LoadLayers and LayerCredentials do not check it.
type LayerSelection struct {
	OrgID     string
	Org       bool
	SceneID   string
	PersonKey string
}

// LoadLayers reads the stored configuration of the org, scene and person
// layers of sel (in that order, absent ones skipped): enabled bindings
// whose resource is in the agent's enabled offer catalog, prompt
// components and custom MCP servers. It does not check the library switch
// of connectors (callers resolve connectors against enabled library rows)
// nor whether OrgID is a tenant. An empty selection reads nothing.
func LoadLayers(ctx context.Context, db DBTX, workspaceID, agentID string, sel LayerSelection) ([]ContextLayer, error) {
	type want struct{ scopeType, key string }
	wanted := []want{}
	if sel.Org {
		wanted = append(wanted, want{ScopeOrg, sel.OrgID})
	}
	if sel.SceneID != "" {
		wanted = append(wanted, want{ScopeScene, sel.SceneID})
	}
	if sel.PersonKey != "" {
		wanted = append(wanted, want{ScopePerson, sel.PersonKey})
	}
	if len(wanted) == 0 {
		return []ContextLayer{}, nil
	}
	for _, w := range wanted {
		if !ValidConfigScope(w.scopeType, sel.OrgID, w.key) {
			return nil, ErrInvalidInput
		}
	}
	offers, err := ListOffers(ctx, db, workspaceID, agentID)
	if err != nil {
		return nil, err
	}
	out := make([]ContextLayer, 0, len(wanted))
	for _, w := range wanted {
		layer, err := loadScopeLayer(ctx, db, workspaceID, agentID, w.scopeType, sel.OrgID, w.key, offers)
		if err != nil {
			return nil, err
		}
		out = append(out, layer)
	}
	return out, nil
}

// LayerCredentials returns, with ciphertext, the connector credentials of
// the org, scene and person layers of sel (the scopes LoadLayers reads),
// ordered by connector, then person, scene, org. An empty selection reads
// nothing. Callers open each with OpenCredentialSecret and apply the
// person > scene > org > workspace precedence to every connector a task may
// use (granted or offered).
func LayerCredentials(ctx context.Context, db DBTX, workspaceID, agentID string, sel LayerSelection) ([]Credential, error) {
	orgKey := ""
	if sel.Org {
		orgKey = sel.OrgID
	}
	if orgKey == "" && sel.SceneID == "" && sel.PersonKey == "" {
		return []Credential{}, nil
	}
	rows, err := db.Query(ctx, `SELECT workspace_id::text, agent_id::text, connector_id::text, scope_type, org_id, scope_key, ciphertext, hint, updated_at
		FROM context_connector_credential
		WHERE workspace_id = $1::uuid AND agent_id = $2::uuid AND org_id = $3
		  AND ((scope_type = 'org' AND $4::text <> '' AND scope_key = $4::text)
		    OR (scope_type = 'scene' AND $5::text <> '' AND scope_key = $5::text)
		    OR (scope_type = 'person' AND $6::text <> '' AND scope_key = $6::text))
		ORDER BY connector_id, CASE scope_type WHEN 'person' THEN 0 WHEN 'scene' THEN 1 ELSE 2 END`,
		workspaceID, agentID, sel.OrgID, orgKey, sel.SceneID, sel.PersonKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Credential{}
	for rows.Next() {
		var c Credential
		if err := rows.Scan(&c.WorkspaceID, &c.AgentID, &c.ConnectorID, &c.ScopeType, &c.OrgID, &c.ScopeKey, &c.Ciphertext, &c.Hint, &c.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// LoadScopeLayer reads one org, scene or person layer (see LoadLayers).
func LoadScopeLayer(ctx context.Context, db DBTX, workspaceID, agentID, scopeType, orgID, scopeKey string) (ContextLayer, error) {
	if !ValidConfigScope(scopeType, orgID, scopeKey) {
		return ContextLayer{}, ErrInvalidInput
	}
	offers, err := ListOffers(ctx, db, workspaceID, agentID)
	if err != nil {
		return ContextLayer{}, err
	}
	return loadScopeLayer(ctx, db, workspaceID, agentID, scopeType, orgID, scopeKey, offers)
}

func loadScopeLayer(ctx context.Context, db DBTX, workspaceID, agentID, scopeType, orgID, scopeKey string, offers Offers) (ContextLayer, error) {
	layer := ContextLayer{Layer: scopeType, ScopeKey: scopeKey, ConnectorIDs: []string{}, SkillIDs: []string{}}
	bindings, err := ListScopeBindings(ctx, db, workspaceID, agentID, scopeType, orgID, scopeKey)
	if err != nil {
		return layer, err
	}
	for _, binding := range bindings {
		if !binding.Enabled || !offers.Contains(binding.ResourceType, binding.ResourceID) {
			continue
		}
		switch binding.ResourceType {
		case ResourceConnector:
			layer.ConnectorIDs = append(layer.ConnectorIDs, binding.ResourceID)
		case ResourceSkill:
			layer.SkillIDs = append(layer.SkillIDs, binding.ResourceID)
		}
	}
	if layer.Prompts, err = ListPromptComponents(ctx, db, workspaceID, agentID, scopeType, orgID, scopeKey); err != nil {
		return layer, err
	}
	config, err := GetScopeMCPConfig(ctx, db, workspaceID, agentID, scopeType, orgID, scopeKey)
	switch {
	case err == nil:
		layer.MCPConfig = config.MCPConfig
	case !errors.Is(err, ErrNotFound):
		return layer, err
	}
	return layer, nil
}

// LoadGlobalLayer reads the agent's global layer as the preview shows it:
// the enabled library connectors granted to the agent, its enabled agent
// skills (by name) and its own mcp_config. It has no prompt components (the
// agent's instructions are not components). The runtime builds its global
// layer from what the claim already resolved instead.
func LoadGlobalLayer(ctx context.Context, db DBTX, workspaceID, agentID string) (ContextLayer, error) {
	layer := ContextLayer{Layer: LayerGlobal, ConnectorIDs: []string{}, SkillIDs: []string{}}
	var config []byte
	err := db.QueryRow(ctx, `SELECT mcp_config FROM agent WHERE id = $2::uuid AND workspace_id = $1::uuid`, workspaceID, agentID).Scan(&config)
	if errors.Is(err, pgx.ErrNoRows) {
		return layer, ErrNotFound
	}
	if err != nil {
		return layer, err
	}
	if len(config) > 0 {
		layer.MCPConfig = json.RawMessage(config)
	}
	for _, q := range []struct {
		query string
		dst   *[]string
	}{
		{`SELECT c.id::text FROM internal_connector c
			JOIN internal_connector_agent g ON g.connector_id = c.id AND g.workspace_id = c.workspace_id
			WHERE c.workspace_id = $1::uuid AND g.agent_id = $2::uuid AND c.enabled
			ORDER BY c.name, c.id`, &layer.ConnectorIDs},
		{`SELECT s.id::text FROM skill s JOIN agent_skill ask ON ask.skill_id = s.id
			WHERE ask.agent_id = $2::uuid AND ask.enabled AND s.workspace_id = $1::uuid
			ORDER BY s.name, s.id`, &layer.SkillIDs},
	} {
		rows, err := db.Query(ctx, q.query, workspaceID, agentID)
		if err != nil {
			return layer, err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return layer, err
			}
			*q.dst = append(*q.dst, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return layer, err
		}
	}
	return layer, nil
}
