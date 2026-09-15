package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/agentsource"
	"github.com/multica-ai/multica/server/internal/coordinatorcontract"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// agentPackageService is the only business entry point for portable imports and
// exports. Transport parsing and HTTP authorization stay outside this service;
// all business readers/writers run in the caller's snapshot/atomic transaction.
type agentPackageService struct { handler *Handler }

type packageOperation struct {
	ctx context.Context
	q *db.Queries
	tx pgx.Tx
	h *Handler
	agent db.Agent
	actorID pgtype.UUID
	source db.AgentSource
	prepared preparedAgentSource
	definition packageConfiguration
	creating bool
	instructionsPath string
	skills []agentsource.Skill
	notes *agentExportNotes
	state packageBindingState
}

type packageExport struct {
	Manifest map[string]any
	Notes []byte
	Instructions string
	Skills []agentsource.Skill
}

// One registry drives both directions. Every registration must satisfy the
// generic Import/Export pair with the same value type, checked by the compiler.
var agentPackageFields = []agentsource.PackageField[packageOperation]{
	agentsource.NewPackageField[packageOperation, string]("name", packageNameCodec{}),
	agentsource.NewPackageField[packageOperation, string]("description", packageDescriptionCodec{}),
	agentsource.NewPackageField[packageOperation, string]("instructions", packageInstructionsCodec{}),
	agentsource.NewPackageField[packageOperation, *coordinatorcontract.Contract]("coordinator_contract", packageContractCodec{}),
	agentsource.NewPackageField[packageOperation, map[string]json.RawMessage]("configuration", packageConfigurationCodec{}),
	agentsource.NewPackageField[packageOperation, portableBindings]("bindings", packageBindingsCodec{}),
	agentsource.NewPackageField[packageOperation, []portableRuntimeSkill]("disabled_runtime_skills", packageDisabledRuntimeSkillsCodec{}),
	agentsource.NewPackageField[packageOperation, []portablePlugin]("dsh_plugins", packagePluginsCodec{}),
	agentsource.NewPackageField[packageOperation, []portableOKR]("okrs", packageOKRsCodec{}),
	agentsource.NewPackageField[packageOperation, portableAccess]("access", packageAccessCodec{}),
	agentsource.NewPackageField[packageOperation, *portableA2A]("a2a", packageA2ACodec{}),
	agentsource.NewPackageField[packageOperation, []portableSkill]("skills", packageSkillsCodec{}),
}

func (s agentPackageService) Import(ctx context.Context, tx pgx.Tx, agent db.Agent, source db.AgentSource, prepared preparedAgentSource, secrets map[string]string, deferred []string, actorID pgtype.UUID, creating bool) error {
	q := s.handler.Queries.WithTx(tx)
	state, err := readPackageBindingState(ctx, q, agent)
	if err != nil { return err }
	request := CreateAgentPackageRequest{Secrets:secrets, DeferredBindings:deferred}
	if !creating {
		reusedSecrets, ready, err := s.handler.reusablePackageInputs(ctx,q,agent,uuidToString(actorID),state,prepared.bundle)
		if err != nil { return err }
		for ref, value := range secrets { reusedSecrets[ref] = value }
		request.Secrets = reusedSecrets
		request.DeferredBindings = append(append([]string{},deferred...),ready...)
	}
	definition, err := preparePackageConfiguration(&request, map[string]json.RawMessage{}, prepared.bundle)
	if err != nil { return err }
	if prepared.bundle.Definition != nil {
		if !agent.RuntimeID.Valid { return sourceRequestError(http.StatusConflict,"select a runtime before publishing configuration") }
		runtime, err := q.GetAgentRuntimeForWorkspace(ctx, db.GetAgentRuntimeForWorkspaceParams{ID:agent.RuntimeID, WorkspaceID:agent.WorkspaceID})
		if err != nil { return err }
		if err := definition.validateRuntime(runtime); err != nil { return err }
	}
	fields := definition.resolved
	if fields == nil { fields = map[string]json.RawMessage{} }
	// The normalized bundle is authoritative for resolved file contents. V1
	// shares these operations without acquiring V2-only configuration defaults.
	fields["instructions"], _ = json.Marshal(prepared.bundle.Manifest.Spec.Instructions)
	fields["skills"] = json.RawMessage(`[]`)
	if value, present := prepared.bundle.Definition["disabled_runtime_skills"]; present { fields["disabled_runtime_skills"] = value }
	fields["coordinator_contract"] = coordinatorcontract.Marshal(prepared.bundle.CoordinatorContract)
	if fields["coordinator_contract"] == nil { fields["coordinator_contract"] = json.RawMessage(`null`) }
	op := &packageOperation{ctx:ctx, q:q, tx:tx, h:s.handler, agent:agent, source:source, prepared:prepared, definition:definition, actorID:actorID, creating:creating, state:state}
	for _, field := range agentPackageFields {
		if value, present := fields[field.Name()]; present {
			if err := field.Import(op, value); err != nil { return err }
		}
	}
	if prepared.bundle.Definition != nil {
		op.state.captureSecrets(prepared.bundle.Definition["configuration"])
		if err := op.confirmAppliedLocalBindings(); err != nil { return err }
		return writePackageBindingState(ctx, q, agent, op.state)
	}
	return nil
}

func (s agentPackageService) Export(ctx context.Context, q *db.Queries, agent db.Agent, instructionsPath string) (packageExport, error) {
	state, err := readPackageBindingState(ctx, q, agent)
	if err != nil { return packageExport{}, err }
	op := &packageOperation{ctx:ctx, q:q, h:s.handler, agent:agent, instructionsPath:instructionsPath, notes:newAgentExportNotes(), state:state}
	op.notes.SecretAliases = state.SecretRefs
	manifest := map[string]any{"$schema":agentsource.PortableSchemaPath, "version":"multica.agent/v2"}
	for _, field := range agentPackageFields {
		value, err := field.Export(op)
		if err != nil { return packageExport{}, err }
		if field.Name() == "coordinator_contract" && string(value) == "null" { continue }
		var decoded any
		if err := json.Unmarshal(value, &decoded); err != nil { return packageExport{}, err }
		manifest[field.Name()] = decoded
	}
	notes, err := json.MarshalIndent(op.notes, "", "  ")
	return packageExport{Manifest:manifest, Notes:notes, Instructions:agent.Instructions, Skills:op.skills}, err
}

func buildAgentExportManifest(ctx context.Context, q *db.Queries, agent db.Agent, instructionsPath string) (map[string]any, []byte, error) {
	result, err := (agentPackageService{}).Export(ctx, q, agent, instructionsPath)
	return result.Manifest, result.Notes, err
}

type packageNameCodec struct{}
func (packageNameCodec) Import(c *packageOperation, value string) error {
	if c.creating { return nil }
	_, err := c.q.UpdateAgent(c.ctx, db.UpdateAgentParams{ID:c.agent.ID, Name:pgtype.Text{String:value, Valid:true}})
	return err
}
func (packageNameCodec) Export(c *packageOperation) (string, error) { return c.agent.Name, nil }

type packageDescriptionCodec struct{}
func (packageDescriptionCodec) Import(c *packageOperation, value string) error {
	if c.creating { return nil }
	_, err := c.q.UpdateAgent(c.ctx, db.UpdateAgentParams{ID:c.agent.ID, Description:pgtype.Text{String:value, Valid:true}})
	return err
}
func (packageDescriptionCodec) Export(c *packageOperation) (string, error) { return c.agent.Description, nil }

type packageInstructionsCodec struct{}
func (packageInstructionsCodec) Import(c *packageOperation, path string) error {
	_, err := c.q.UpdateAgent(c.ctx, db.UpdateAgentParams{ID:c.agent.ID, Instructions:pgtype.Text{String:c.prepared.bundle.Instructions, Valid:true}})
	return err
}
func (packageInstructionsCodec) Export(c *packageOperation) (string, error) { return c.instructionsPath, nil }

type packageContractCodec struct{}
func (packageContractCodec) Import(c *packageOperation, value *coordinatorcontract.Contract) error {
	data := coordinatorcontract.Marshal(value)
	if data == nil { data = []byte("null") }
	_, err := c.q.UpdateAgent(c.ctx, db.UpdateAgentParams{ID:c.agent.ID, CoordinatorContract:data})
	return err
}
func (packageContractCodec) Export(c *packageOperation) (*coordinatorcontract.Contract, error) { return coordinatorcontract.Parse(c.agent.CoordinatorContract) }

type packageConfigurationCodec struct{}
func (packageConfigurationCodec) Import(c *packageOperation, value map[string]json.RawMessage) error {
	return c.h.importPackageConfiguration(c.ctx, c.tx, c.q, c.agent, value, c.actorID)
}
func (packageConfigurationCodec) Export(c *packageOperation) (map[string]json.RawMessage, error) {
	value, err := exportPackageConfiguration(c.ctx, c.q, c.agent, c.notes)
	if err != nil { return nil, err }
	return packageJSONValue[map[string]json.RawMessage](value)
}

func packageJSONValue[T any](value any) (T, error) {
	var result T
	data, err := json.Marshal(value)
	if err != nil { return result, err }
	err = json.Unmarshal(data, &result)
	return result, err
}

type packageSkillsCodec struct{}
func (packageSkillsCodec) Import(c *packageOperation, value []portableSkill) error {
	return applySourceSkills(c.ctx, c.q, c.agent, c.source, c.prepared, c.actorID, c.creating)
}
func (packageSkillsCodec) Export(c *packageOperation) ([]portableSkill, error) {
	var err error
	c.skills, err = exportPackageSkills(c.ctx, c.q, c.agent)
	if err != nil { return nil, err }
	result := []portableSkill{}
	for _, skill := range c.skills { result = append(result, portableSkill{Scope:skill.Scope, SkillID:skill.SkillID, Path:skill.SourcePath, Name:skill.Name, Description:skill.Description, Enabled:!skill.Disabled}) }
	return result, nil
}

type portableSkill struct { Scope *agentsource.SkillScope `json:"scope,omitempty"`; SkillID string `json:"skill_id,omitempty"`; Path string `json:"path"`; Name string `json:"name"`; Description string `json:"description,omitempty"`; Enabled bool `json:"enabled"` }

func exportPackageSkills(ctx context.Context, queries *db.Queries, agent db.Agent) ([]agentsource.Skill, error) {
	assignments, err := queries.ListAgentSkillSummaries(ctx, agent.ID)
	if err != nil { return nil, err }
	source, err := queries.GetAgentSourceByAgentID(ctx, agent.ID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) { return nil, err }
	mapped := map[string]string{}
	if err == nil {
		mappings, err := queries.ListAgentSourceSkills(ctx, source.ID)
		if err != nil { return nil, err }
		for _, mapping := range mappings { mapped[uuidToString(mapping.SkillID)] = mapping.SourcePath }
	}
	if len(assignments) > agentsource.MaxSkills { return nil, fmt.Errorf("agent exceeds the source skill limit") }
	skills := make([]agentsource.Skill, 0, len(assignments))
	for _, assignment := range assignments {
		skill, err := queries.GetSkillInWorkspace(ctx, db.GetSkillInWorkspaceParams{ID:assignment.ID, WorkspaceID:agent.WorkspaceID})
		if err != nil { return nil, err }
		files, err := queries.ListSkillFiles(ctx, skill.ID)
		if err != nil { return nil, err }
		name, path := skill.Name, "workspace-skills/" + uuidToString(skill.ID)
		if sourcePath, ok := mapped[uuidToString(skill.ID)]; ok { name = strings.TrimSuffix(name, sourceManagedSkillName("", source.ID)); path = sourcePath }
		compiled := agentsource.Skill{Scope:&agentsource.SkillScope{Type:"workspace", ID:uuidToString(agent.WorkspaceID)}, SkillID:uuidToString(skill.ID), SourcePath:path, Name:name, Description:skill.Description, Content:skill.Content, Disabled:!assignment.Enabled, Files:[]agentsource.File{}}
		for _, file := range files { compiled.Files = append(compiled.Files, agentsource.File{Path:file.Path, Content:file.Content}) }
		skills = append(skills, compiled)
	}
	return skills, nil
}

func (c *packageOperation) confirmAppliedLocalBindings() error {
	current, err := c.q.GetAgent(c.ctx,c.agent.ID)
	if err != nil { return err }
	notes := newAgentExportNotes()
	bindings, err := exportPackageBindings(c.ctx,c.q,current,notes)
	if err != nil { return err }
	for kind, value := range bindings {
		path := "/bindings/" + kind
		declaration, exists := c.state.Declarations[path]
		if !exists { continue }
		actual, _ := json.Marshal(value)
		mappings := map[string]string{}
		if kind == "runtime" {
			var wanted, selected portableResource
			_ = json.Unmarshal(declaration,&wanted); _ = json.Unmarshal(actual,&selected)
			mappings[wanted.Ref] = selected.Ref
		}
		if packageBindingMatches(declaration,actual,mappings) { c.state.Receipts[path] = packageBindingReceipt{Declaration:packageValueHash(declaration),Actual:actual,Mappings:mappings} }
	}
	if declaration, exists := c.state.Declarations["/access"]; exists {
		value, err := exportPackageAccess(c.ctx,c.q,current,notes); if err != nil { return err }
		actual, _ := json.Marshal(value)
		if packageBindingMatches(declaration,actual,nil) { c.state.Receipts["/access"] = packageBindingReceipt{Declaration:packageValueHash(declaration),Actual:actual} }
	}
	return nil
}
