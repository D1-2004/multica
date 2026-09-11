package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/agentsource"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type packageSkillTarget struct {
	Definition agentsource.Skill
	Skill db.Skill
	Files []db.SkillFile
	Enabled bool
	Managed bool
}

// resolvePackageSkillTargets is shared by preview and publication. An exported
// identity takes precedence over its archive path when updating the same Agent.
// New Agent creation and foreign-workspace imports retain copy semantics.
func resolvePackageSkillTargets(ctx context.Context, q *db.Queries, agent db.Agent, source db.AgentSource, bundle agentsource.Bundle, creating bool) ([]packageSkillTarget, error) {
	mappings, err := q.ListAgentSourceSkills(ctx, source.ID)
	if err != nil { return nil, err }
	byPath := map[string]pgtype.UUID{}
	managed := map[pgtype.UUID]bool{}
	for _, mapping := range mappings { byPath[mapping.SourcePath], managed[mapping.SkillID] = mapping.SkillID, true }
	attached := map[pgtype.UUID]bool{}
	if !creating {
		assignments, err := q.ListAgentSkillSummaries(ctx,agent.ID)
		if err != nil { return nil,err }
		for _, assignment := range assignments { attached[assignment.ID] = true }
	}
	result := make([]packageSkillTarget, 0, len(bundle.Skills))
	seen := map[pgtype.UUID]bool{}
	for _, compiled := range bundle.Skills {
		id := byPath[compiled.SourcePath]
		if !creating && compiled.Scope != nil && compiled.Scope.Type == "workspace" && strings.EqualFold(compiled.Scope.ID, uuidToString(agent.WorkspaceID)) {
			id, err = parseUUIDValue(compiled.SkillID)
			if err != nil { return nil, sourceRequestError(http.StatusUnprocessableEntity, "invalid skill_id") }
		} else if !creating && compiled.Scope == nil && strings.HasPrefix(compiled.SourcePath,"workspace-skills/") {
			// Public ZIPs exported before explicit identities used this exact
			// directory convention. Recover only an already attached local ID.
			legacyID, err := parseUUIDValue(strings.TrimPrefix(compiled.SourcePath,"workspace-skills/"))
			if err == nil && attached[legacyID] { id = legacyID }
		}
		if id.Valid {
			if seen[id] { return nil, sourceRequestError(http.StatusUnprocessableEntity,"multiple package skills resolve to the same workspace skill") }
			seen[id] = true
		}
		result = append(result, packageSkillTarget{Definition:compiled, Skill:db.Skill{ID:id}, Managed:managed[id]})
	}
	// Shared skills can appear in several Agents. Lock them in a stable order.
	sort.Slice(result, func(i,j int) bool { return uuidToString(result[i].Skill.ID) < uuidToString(result[j].Skill.ID) })
	for i := range result {
		target := &result[i]
		if !target.Skill.ID.Valid { continue }
		target.Skill, err = q.GetSkillInWorkspaceForUpdate(ctx, db.GetSkillInWorkspaceForUpdateParams{ID:target.Skill.ID, WorkspaceID:agent.WorkspaceID})
		if errors.Is(err,pgx.ErrNoRows) { return nil, sourceRequestError(http.StatusConflict,fmt.Sprintf("skill %q no longer exists in the target workspace",target.Definition.SkillID)) }
		if err != nil { return nil,err }
		target.Enabled, err = q.LockAgentPackageSkillEnabled(ctx, db.LockAgentPackageSkillEnabledParams{AgentID:agent.ID, SkillID:target.Skill.ID})
		if errors.Is(err,pgx.ErrNoRows) { return nil, sourceRequestError(http.StatusConflict,fmt.Sprintf("skill %q is not attached to this Agent; refresh its configuration before publishing",uuidToString(target.Skill.ID))) }
		if err != nil { return nil,err }
		var configuration struct { ExclusiveAgentID string `json:"exclusive_agent_id"` }
		if err := json.Unmarshal(target.Skill.Config,&configuration); err != nil { return nil,err }
		if configuration.ExclusiveAgentID != "" && configuration.ExclusiveAgentID != uuidToString(agent.ID) { return nil,sourceRequestError(http.StatusForbidden,"skill is exclusive to another Agent") }
		mapping, err := q.GetAgentSourceSkillBySkillID(ctx,target.Skill.ID)
		if err != nil && !errors.Is(err,pgx.ErrNoRows) { return nil,err }
		if err == nil && mapping.AgentSourceID != source.ID { return nil,sourceRequestError(http.StatusForbidden,"skill is managed by another Agent source") }
		target.Files, err = q.ListSkillFiles(ctx,target.Skill.ID)
		if err != nil { return nil,err }
	}
	return result,nil
}

func packageSkillContentMatches(skill db.Skill, files []db.SkillFile, desired agentsource.Skill) bool {
	if skill.Name != desired.Name || skill.Description != desired.Description || skill.Content != desired.Content || len(files) != len(desired.Files) { return false }
	byPath := map[string]string{}
	for _, file := range files { byPath[file.Path] = file.Content }
	for _, file := range desired.Files { if content,ok := byPath[file.Path]; !ok || content != file.Content { return false } }
	return true
}

func updateReferencedPackageSkill(ctx context.Context, q *db.Queries, target packageSkillTarget, actorID pgtype.UUID) error {
	if packageSkillContentMatches(target.Skill,target.Files,target.Definition) { return nil }
	member, err := q.GetMemberByUserAndWorkspace(ctx,db.GetMemberByUserAndWorkspaceParams{UserID:actorID,WorkspaceID:target.Skill.WorkspaceID})
	if err != nil || !canManageSkillForUser(member.Role,uuidToString(actorID),target.Skill) { return sourceRequestError(http.StatusForbidden,"only the skill creator or a workspace owner/admin can update the referenced skill") }
	desired := target.Definition
	if _, err := q.UpdateSkill(ctx,db.UpdateSkillParams{ID:target.Skill.ID,Name:pgtype.Text{String:desired.Name,Valid:true},Description:pgtype.Text{String:desired.Description,Valid:true},Content:pgtype.Text{String:desired.Content,Valid:true}}); err != nil { return err }
	return reconcilePackageSkillFiles(ctx,q,target.Skill.ID,target.Files,desired.Files)
}

func reconcilePackageSkillFiles(ctx context.Context, q *db.Queries, skillID pgtype.UUID, existing []db.SkillFile, desired []agentsource.File) error {
	byPath := map[string]db.SkillFile{}
	for _, file := range existing { byPath[file.Path] = file }
	for _, file := range desired {
		previous, found := byPath[file.Path]
		if !found || previous.Content != file.Content {
			if _, err := q.UpsertSkillFile(ctx,db.UpsertSkillFileParams{SkillID:skillID,Path:sanitizeNullBytes(file.Path),Content:sanitizeNullBytes(file.Content)}); err != nil { return err }
		}
		delete(byPath,file.Path)
	}
	for _, file := range byPath { if err := q.DeleteSkillFile(ctx,file.ID); err != nil { return err } }
	return nil
}
