package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/agentsource"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func (h *Handler) SyncAgentSource(w http.ResponseWriter, r *http.Request) {
	agent, source, ok := h.loadPackageSourceForManage(w, r)
	if !ok { return }
	r.Body = http.MaxBytesReader(w, r.Body, 4 << 20)
	var request struct { PreviewID string `json:"preview_id"`; Secrets map[string]string `json:"secrets"`; DeferredBindings []string `json:"deferred_bindings"` }
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid source confirmation"); return
	}
	if request.PreviewID == "" {
		writeError(w, http.StatusPreconditionRequired, "preview_id is required; preview source changes before confirming sync"); return
	}
	preview, err := h.readAgentSourcePreview(r, agent.WorkspaceID, request.PreviewID)
	if err != nil { writeGitRepoError(w, err); return }
	if preview.AgentID != agent.ID || preview.ExpectedStateHash == "" || (!preview.AppliedAt.Valid && preview.AgentSourceID != source.ID) {
		writeError(w, http.StatusBadRequest, "preview does not belong to this Agent source"); return
	}
	resolved, err := h.resolveAgentSourcePreview(r.Context(), preview)
	if err != nil { writeGitRepoError(w, err); return }
	if resolved.bundle.Definition["a2a"] != nil && r.Header.Get("X-Actor-Source") != "" { writeError(w, http.StatusForbidden, "A2A policy import requires a human actor"); return }
	if preview.AppliedAt.Valid { writeSourceSyncReplay(w, preview, resolved.bundle.Warnings); return }
	if source.SourceType == "git" && preview.Repository == "" { writeError(w,http.StatusConflict,"Git Agents publish from their repository; preview a branch, tag or commit"); return }
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil { writeError(w, http.StatusInternalServerError, "failed to start source sync"); return }
	defer tx.Rollback(r.Context())
	queries := h.Queries.WithTx(tx)
	preview, err = lockSourcePreview(r.Context(), queries, preview)
	if err != nil { writeGitRepoError(w, err); return }
	if preview.AppliedAt.Valid {
		_ = tx.Rollback(r.Context())
		writeSourceSyncReplay(w, preview, resolved.bundle.Warnings)
		return
	}
	agent, err = queries.GetAgentForUpdate(r.Context(), agent.ID)
	if err != nil { writeError(w, http.StatusConflict, "Agent no longer exists"); return }
	if !h.canManageAgent(w, r, agent) { return }
	if agent.ArchivedAt.Valid { writeError(w, http.StatusConflict, "restore the Agent before syncing its source"); return }
	lockedSource, err := queries.LockAgentSourceByAgentID(r.Context(), agent.ID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) { writeError(w, http.StatusInternalServerError, "failed to read Agent source"); return }
	if lockedSource.ID != preview.AgentSourceID || (preview.Repository != "" && (lockedSource.GitConnectionID != preview.GitConnectionID || lockedSource.RepositoryUrl != preview.Repository)) ||
		lockedSource.SyncedCommitSha != preview.ExpectedSourceSha || lockedSource.ManagedSourceKey.Valid {
		writeError(w, http.StatusConflict, "Agent source changed after preview; preview again"); return
	}
	current, stateHash, err := sourceStateFiles(r.Context(), queries, agent, lockedSource, resolved.bundle)
	if err != nil { writeAgentSourceDatabaseError(w,err); return }
	if stateHash != preview.ExpectedStateHash {
		writeError(w, http.StatusConflict, "Agent configuration changed after preview; preview again"); return
	}
	if agent.RuntimeID.Valid {
		runtime, err := queries.GetAgentRuntimeForWorkspace(r.Context(), db.GetAgentRuntimeForWorkspaceParams{ID:agent.RuntimeID, WorkspaceID:agent.WorkspaceID})
		if err != nil { writeError(w, http.StatusConflict, "Agent runtime is unavailable"); return }
		if !providerCompatible(resolved.bundle.Manifest.Spec.Compatibility.Providers, runtime.Provider) {
			writeError(w, http.StatusUnprocessableEntity, "Agent runtime is incompatible with the source"); return
		}
	}
	if !lockedSource.ID.Valid {
		lockedSource, err = queries.CreateLocalAgentSource(r.Context(), db.CreateLocalAgentSourceParams{AgentID:agent.ID, WorkspaceID:agent.WorkspaceID, SyncedCommitSha:resolved.sha, CreatedBy:parseUUID(requestUserID(r))})
		if err != nil { writeAgentSourceDatabaseError(w, err); return }
	}
	configurationChanged := len(diffPackageState(current, sourceDefinitionFiles(resolved.bundle))) > 0
	changed := configurationChanged || lockedSource.Ref != resolved.ref || lockedSource.SyncedCommitSha != resolved.sha
	if changed {
		if err := (agentPackageService{handler:h}).Import(r.Context(), tx, agent, lockedSource, resolved, request.Secrets, request.DeferredBindings, parseUUID(requestUserID(r)), false); err != nil { writeAgentSourceDatabaseError(w, err); return }
	}
	updatedSource := lockedSource
	// Record the selected Git revision or local package hash atomically with
	// its materialized configuration and publication receipt.
	if preview.Repository != "" || lockedSource.SourceType == "local" {
		updatedSource, err = queries.MarkAgentSourceBranchSyncSucceeded(r.Context(), db.MarkAgentSourceBranchSyncSucceededParams{
			ID:lockedSource.ID, Ref:resolved.ref, SyncedCommitSha:resolved.sha, ManifestPath:agentsource.SourceManifestPath(resolved.bundle),
		})
		if err != nil { writeError(w, http.StatusInternalServerError, "failed to record source version"); return }
	}

	if err := markSourcePreviewApplied(r.Context(), queries, preview, updatedSource, changed); err != nil {
		writeAgentSourceDatabaseError(w, err); return
	}
	if err := tx.Commit(r.Context()); err != nil { writeError(w, http.StatusInternalServerError, "failed to commit source sync"); return }
	if h.EventTriggers != nil { h.EventTriggers.Notify() }
	if h.DingTalkResponsePolicyNotifier != nil { h.DingTalkResponsePolicyNotifier.NotifyResponsePolicyChanged() }
	h.publishAgentSourceSync(r, agent, changed)
	writeJSON(w, http.StatusOK, AgentSourceSyncResponse{Source:agentSourceToResponse(updatedSource), Changed:changed, Warnings:resolved.bundle.Warnings})
}

func writeSourceSyncReplay(w http.ResponseWriter, preview db.AgentSourcePreview, warnings []string) {
	writeJSON(w, http.StatusOK, map[string]any{"source":json.RawMessage(preview.AppliedSource), "changed":preview.AppliedChanged, "warnings":warnings})
}

func applySourceSkills(ctx context.Context, queries *db.Queries, agent db.Agent, source db.AgentSource, resolved preparedAgentSource, actorID pgtype.UUID, creating bool) error {
	targets, err := resolvePackageSkillTargets(ctx,queries,agent,source,resolved.bundle,creating)
	if err != nil { return err }
	mappings, err := queries.ListAgentSourceSkills(ctx,source.ID)
	if err != nil { return err }
	retained := map[pgtype.UUID]bool{}
	desiredPaths := map[pgtype.UUID]string{}
	currentPaths := map[pgtype.UUID]string{}
	for _, target := range targets { if target.Managed { retained[target.Skill.ID] = true; desiredPaths[target.Skill.ID] = target.Definition.SourcePath } }
	// Remove only skills owned by this source. Existing workspace references
	// retain their original ownership and are never converted to source skills.
	for _, mapping := range mappings {
		currentPaths[mapping.SkillID] = mapping.SourcePath
		if retained[mapping.SkillID] {
			// Recreate the mapping below so directory renames preserve skill IDs,
			// including simultaneous swaps of two source paths.
			if desiredPaths[mapping.SkillID] != mapping.SourcePath {
				if err := queries.DeleteAgentSourceSkill(ctx,db.DeleteAgentSourceSkillParams{AgentSourceID:source.ID,SkillID:mapping.SkillID}); err != nil { return err }
			}
			continue
		}
		if err := queries.DeleteSkillDependents(ctx,mapping.SkillID); err != nil { return err }
		if err := queries.DeleteSkill(ctx,db.DeleteSkillParams{ID:mapping.SkillID,WorkspaceID:agent.WorkspaceID}); err != nil { return err }
	}
	for _, target := range targets {
		skillID := target.Skill.ID
		if skillID.Valid {
			if target.Managed {
				if err := updateSourceSkillInTx(ctx,queries,skillID,resolved,source.ID,target.Definition); err != nil { return err }
			} else if err := updateReferencedPackageSkill(ctx,queries,target,actorID); err != nil { return err }
		} else {
			created, err := createSourceSkillInTx(ctx,queries,agent.WorkspaceID,agent.OwnerID,resolved,source.ID,target.Definition)
			if err != nil { return err }
			skillID, target.Managed = created.ID, true
		}
		if target.Managed && currentPaths[skillID] != target.Definition.SourcePath {
			if _, err := queries.CreateAgentSourceSkill(ctx,db.CreateAgentSourceSkillParams{AgentSourceID:source.ID,SkillID:skillID,SourcePath:target.Definition.SourcePath}); err != nil { return err }
		}
		if err := queries.AddAgentSkill(ctx,db.AddAgentSkillParams{AgentID:agent.ID,SkillID:skillID}); err != nil { return err }
		if !target.Skill.ID.Valid || target.Enabled == target.Definition.Disabled {
			if _, err := queries.SetAgentSkillEnabled(ctx,db.SetAgentSkillEnabledParams{AgentID:agent.ID,SkillID:skillID,Enabled:!target.Definition.Disabled}); err != nil { return err }
		}
	}
	return nil
}
