package handler

import (
	"encoding/json"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type portableResource struct {
	Ref         string `json:"ref"`
	Provider    string `json:"provider,omitempty"`
	RuntimeMode string `json:"runtime_mode,omitempty"`
	Platform    string `json:"platform,omitempty"`
}
type portableTarget struct {
	TargetType string `json:"target_type"`
	Ref        string `json:"ref,omitempty"`
}
type portableAccess struct {
	PermissionMode    string           `json:"permission_mode"`
	InvocationTargets []portableTarget `json:"invocation_targets"`
}

// The keys are provider binding kinds defined by JSON Schema. Raw null is an
// explicit unbind request; an absent key preserves the destination binding.
type portableBindings map[string]json.RawMessage

type packageBindingsCodec struct{}

func (packageBindingsCodec) Import(c *packageOperation, value portableBindings) error {
	for kind, declaration := range value {
		c.state.declare("/bindings/"+kind, declaration)
	}
	return nil
}
func (packageBindingsCodec) Export(c *packageOperation) (portableBindings, error) {
	value, err := exportPackageBindings(c.ctx, c.q, c.agent, c.notes)
	if err != nil {
		return nil, err
	}
	result, err := packageJSONValue[portableBindings](value)
	if err != nil {
		return nil, err
	}
	for path, declaration := range c.state.Declarations {
		if kind, ok := strings.CutPrefix(path, "/bindings/"); ok {
			if kind == "github_identity" {
				result[kind] = declaration
				continue
			}
			result[kind] = c.state.exportDeclaration(path, declaration, result[kind])
		}
	}
	return result, nil
}

type packageDisabledRuntimeSkillsCodec struct{}

func (packageDisabledRuntimeSkillsCodec) Import(c *packageOperation, value []portableRuntimeSkill) error {
	if c.definition.DisabledRuntimeSkills == nil && len(value) > 0 {
		c.state.declareValue("/disabled_runtime_skills", value)
		return nil
	}
	delete(c.state.Declarations, "/disabled_runtime_skills")
	delete(c.state.Receipts, "/disabled_runtime_skills")
	return (packageConfiguration{DisabledRuntimeSkills: value}).importDisabledRuntimeSkills(c.ctx, c.q, c.agent, c.actorID)
}
func (packageDisabledRuntimeSkillsCodec) Export(c *packageOperation) ([]portableRuntimeSkill, error) {
	value, err := exportPackageDisabledRuntimeSkills(c.ctx, c.q, c.agent, c.notes)
	if err != nil {
		return nil, err
	}
	result, err := packageExportDeclaration[[]portableRuntimeSkill](c, "/disabled_runtime_skills", value)
	if err != nil {
		return nil, err
	}
	if raw := c.state.Declarations["/bindings/runtime"]; raw != nil {
		var runtime portableResource
		_ = json.Unmarshal(raw, &runtime)
		actual := c.notes.resource("runtime", uuidToString(c.agent.RuntimeID), "runtime")["ref"]
		for i := range result {
			if result[i].RuntimeRef == actual {
				result[i].RuntimeRef = runtime.Ref
			}
		}
	}
	return result, nil
}

type packagePluginsCodec struct{}

func (packagePluginsCodec) Import(c *packageOperation, value []packageDshPlugin) error {
	delete(c.state.Declarations, "/dsh_plugins")
	delete(c.state.Receipts, "/dsh_plugins")
	return c.definition.applyDshPlugins(c.ctx, c.q, c.agent, c.actorID)
}
func (packagePluginsCodec) Export(c *packageOperation) ([]packageDshPlugin, error) {
	value, err := exportPackagePlugins(c.ctx, c.q, c.agent, c.notes)
	if err != nil {
		return nil, err
	}
	return packageJSONValue[[]packageDshPlugin](value)
}

type packageOKRsCodec struct{}

func (packageOKRsCodec) Import(c *packageOperation, value []portableOKR) error {
	return (packageConfiguration{OKRs: value}).importOKRs(c.ctx, c.q, c.agent, c.actorID)
}
func (packageOKRsCodec) Export(c *packageOperation) ([]portableOKR, error) {
	value, err := exportPackageOKRs(c.ctx, c.q, c.agent, c.notes)
	if err != nil {
		return nil, err
	}
	return packageJSONValue[[]portableOKR](value)
}

type packageAccessCodec struct{}

func (packageAccessCodec) Import(c *packageOperation, value portableAccess) error {
	c.state.declareValue("/access", value)
	for _, target := range value.InvocationTargets {
		if target.TargetType != "workspace" {
			return nil
		}
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	var request CreateAgentRequest
	if err := json.Unmarshal(data, &request); err != nil {
		return err
	}
	permission, _, err := parsePermissionInput(c.agent.WorkspaceID, request.PermissionMode, request.InvocationTargets, true, true, nil)
	if err != nil {
		return err
	}
	if err := replaceInvocationTargetsWithQueries(c.ctx, c.q, c.agent.ID, c.agent.OwnerID, permission.targets); err != nil {
		return err
	}
	_, err = c.q.UpdateAgent(c.ctx, db.UpdateAgentParams{ID: c.agent.ID, PermissionMode: pgtype.Text{String: permission.mode, Valid: true}, Visibility: pgtype.Text{String: permission.legacyVisibility(), Valid: true}})
	return err
}
func (packageAccessCodec) Export(c *packageOperation) (portableAccess, error) {
	value, err := exportPackageAccess(c.ctx, c.q, c.agent, c.notes)
	if err != nil {
		return portableAccess{}, err
	}
	return packageExportDeclaration[portableAccess](c, "/access", value)
}

type packageA2ACodec struct{}

func (packageA2ACodec) Import(c *packageOperation, value *portableA2A) error {
	return (packageConfiguration{A2A: value}).importA2A(c.ctx, c.q, c.agent, c.actorID)
}
func (packageA2ACodec) Export(c *packageOperation) (*portableA2A, error) {
	value, err := exportPackageA2A(c.ctx, c.q, c.agent, c.notes)
	if err != nil {
		return nil, err
	}
	return packageJSONValue[*portableA2A](value)
}

func packageExportDeclaration[T any](c *packageOperation, path string, actual any) (T, error) {
	if declaration, exists := c.state.Declarations[path]; exists {
		encoded, err := json.Marshal(actual)
		if err != nil {
			var zero T
			return zero, err
		}
		return packageJSONValue[T](c.state.exportDeclaration(path, declaration, encoded))
	}
	return packageJSONValue[T](actual)
}
