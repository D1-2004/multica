package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/jackc/pgx/v5"
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
	if err != nil { writeGitHubSourceError(w, err); return }
	if preview.AgentID != agent.ID || preview.ExpectedStateHash == "" || (!preview.AppliedAt.Valid && preview.AgentSourceID != source.ID) {
		writeError(w, http.StatusBadRequest, "preview does not belong to this Agent source"); return
	}
	resolved, err := h.resolveAgentSourcePreview(r.Context(), preview)
	if err != nil { writeGitHubSourceError(w, err); return }
	if resolved.bundle.Definition["a2a"] != nil && r.Header.Get("X-Actor-Source") != "" { writeError(w, http.StatusForbidden, "A2A policy import requires a human actor"); return }
	if preview.AppliedAt.Valid { writeSourceSyncReplay(w, preview, resolved.bundle.Warnings); return }
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil { writeError(w, http.StatusInternalServerError, "failed to start source sync"); return }
	defer tx.Rollback(r.Context())
	queries := h.Queries.WithTx(tx)
	preview, err = lockSourcePreview(r.Context(), queries, preview)
	if err != nil { writeGitHubSourceError(w, err); return }
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
	if lockedSource.ID != preview.AgentSourceID || (preview.GithubInstallationID.Valid && lockedSource.GithubInstallationID != preview.GithubInstallationID) ||
		lockedSource.SyncedCommitSha != preview.ExpectedSourceSha || lockedSource.ManagedSourceKey.Valid {
		writeError(w, http.StatusConflict, "Agent source changed after preview; preview again"); return
	}
	current, stateHash, err := sourceStateFiles(r.Context(), queries, agent, lockedSource)
	if err != nil { writeError(w, http.StatusInternalServerError, "failed to read current Agent configuration"); return }
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
		if err := h.applyPackageSourceConfiguration(r.Context(), queries, agent, resolved, request.Secrets, request.DeferredBindings, parseUUID(requestUserID(r))); err != nil { writeAgentSourceDatabaseError(w, err); return }
		if _, err := queries.UpdateAgent(r.Context(), gitAgentSourceSnapshotUpdate(agent.ID, resolved.bundle)); err != nil {
			writeAgentSourceDatabaseError(w, err); return
		}
		if err := applySourceSkills(r.Context(), queries, agent, lockedSource, resolved); err != nil {
			writeAgentSourceDatabaseError(w, err); return
		}
	}
	updatedSource := lockedSource
	// ZIP publication changes configuration but retains an existing Git binding
	// and its last published Git commit for the next branch diff.
	if preview.GithubInstallationID.Valid || lockedSource.SourceType == "local" {
		updatedSource, err = queries.MarkAgentSourceBranchSyncSucceeded(r.Context(), db.MarkAgentSourceBranchSyncSucceededParams{
			ID:lockedSource.ID, Ref:resolved.ref, SyncedCommitSha:resolved.sha, ManifestPath:agentsource.SourceManifestPath(resolved.bundle),
		})
		if err != nil { writeError(w, http.StatusInternalServerError, "failed to record source version"); return }
	}

	if err := markSourcePreviewApplied(r.Context(), queries, preview, updatedSource, changed); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record source confirmation"); return
	}
	if err := tx.Commit(r.Context()); err != nil { writeError(w, http.StatusInternalServerError, "failed to commit source sync"); return }
	if h.DingTalkResponsePolicyNotifier != nil { h.DingTalkResponsePolicyNotifier.NotifyResponsePolicyChanged() }
	h.publishAgentSourceSync(r, agent, changed)
	writeJSON(w, http.StatusOK, AgentSourceSyncResponse{Source:agentSourceToResponse(updatedSource), Changed:changed, Warnings:resolved.bundle.Warnings})
}

func writeSourceSyncReplay(w http.ResponseWriter, preview db.AgentSourcePreview, warnings []string) {
	writeJSON(w, http.StatusOK, map[string]any{"source":json.RawMessage(preview.AppliedSource), "changed":preview.AppliedChanged, "warnings":warnings})
}

func applySourceSkills(ctx context.Context, queries *db.Queries, agent db.Agent, source db.AgentSource, resolved preparedAgentSource) error {
	mappings, err := queries.ListAgentSourceSkills(ctx, source.ID)
	if err != nil { return err }
	byPath := map[string]db.AgentSourceSkill{}
	for _, mapping := range mappings { byPath[mapping.SourcePath] = mapping }
	targetPaths := map[string]bool{}
	for _, compiled := range resolved.bundle.Skills { targetPaths[compiled.SourcePath] = true }
	// Remove obsolete paths first so a renamed directory can retain its skill
	// name without colliding with the old row. The enclosing transaction rolls
	// these deletions back if any later creation fails.
	for sourcePath, removed := range byPath {
		if targetPaths[sourcePath] { continue }
		if err := queries.DeleteSkillDependents(ctx, removed.SkillID); err != nil { return err }
		if err := queries.DeleteSkill(ctx, db.DeleteSkillParams{ID:removed.SkillID, WorkspaceID:agent.WorkspaceID}); err != nil { return err }
		delete(byPath, sourcePath)
	}
	for _, compiled := range resolved.bundle.Skills {
		mapping, exists := byPath[compiled.SourcePath]
		skillID := mapping.SkillID
		if exists {
			if err := updateSourceSkillInTx(ctx, queries, skillID, resolved, source.ID, compiled); err != nil { return err }
			delete(byPath, compiled.SourcePath)
		} else {
			created, err := createSourceSkillInTx(ctx, queries, agent.WorkspaceID, agent.OwnerID, resolved, source.ID, compiled)
			if err != nil { return err }
			skillID = created.ID
			if _, err := queries.CreateAgentSourceSkill(ctx, db.CreateAgentSourceSkillParams{AgentSourceID:source.ID, SkillID:skillID, SourcePath:compiled.SourcePath}); err != nil { return err }
		}
		if err := queries.AddAgentSkill(ctx, db.AddAgentSkillParams{AgentID:agent.ID, SkillID:skillID}); err != nil { return err }
		if _, err := queries.SetAgentSkillEnabled(ctx, db.SetAgentSkillEnabledParams{AgentID:agent.ID, SkillID:skillID, Enabled:!compiled.Disabled}); err != nil { return err }
	}
	return nil
}
