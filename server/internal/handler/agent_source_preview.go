package handler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/agentsource"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type gitSourceRequestError struct {
	status int
	message string
}

func (e *gitSourceRequestError) Error() string { return e.message }

func sourceRequestError(status int, message string) error {
	return &gitSourceRequestError{status:status, message:message}
}

type AgentSourceSyncPreviewResponse struct {
	PreviewID string `json:"preview_id"`
	ExpiresAt string `json:"expires_at"`
	RepositoryURL string `json:"repository_url"`
	Ref string `json:"ref"`
	BaseSHA string `json:"base_sha"`
	ResolvedSHA string `json:"resolved_sha"`
	GitChanges []agentsource.FileChange `json:"git_changes"`
	ConfigurationChanges []agentsource.FileChange `json:"configuration_changes"`
	Warnings []string `json:"warnings"`
	Changed bool `json:"changed"`
}

func (h *Handler) ListGitHubAgentBranches(w http.ResponseWriter, r *http.Request) {
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceIDFromURL(r, "id"), "workspace id")
	if !ok { return }
	h.listGitHubAgentBranches(w, r, wsUUID, GitHubAgentSourceInput{
		InstallationID:r.URL.Query().Get("installation_id"), Repository:r.URL.Query().Get("repository"),
	})
}

func (h *Handler) ListAgentSourceBranches(w http.ResponseWriter, r *http.Request) {
	agent, source, ok := h.loadGitHubSourceForManage(w, r)
	if !ok { return }
	h.listGitHubAgentBranches(w, r, agent.WorkspaceID, GitHubAgentSourceInput{
		InstallationID:uuidToString(source.GithubInstallationID), Repository:source.RepoOwner + "/" + source.RepoName,
	})
}

func (h *Handler) listGitHubAgentBranches(w http.ResponseWriter, r *http.Request, workspaceID pgtype.UUID, input GitHubAgentSourceInput) {
	resolved, err := h.resolveGitHubAgentRepository(r.Context(), workspaceID, input)
	if err != nil { writeGitHubSourceError(w, err); return }
	branches, err := h.GitHubApp.ListBranches(r.Context(), resolved.installation.InstallationID, ownerFromFullName(resolved.repository.FullName), repoFromFullName(resolved.repository.FullName))
	if err != nil { writeGitHubSourceError(w, err); return }
	writeJSON(w, http.StatusOK, map[string]any{
		"repository":resolved.repository.FullName, "repository_url":"https://github.com/" + resolved.repository.FullName,
		"default_branch":resolved.repository.DefaultBranch, "branches":branches,
	})
}

func (h *Handler) loadGitHubSourceForManage(w http.ResponseWriter, r *http.Request) (db.Agent, db.AgentSource, bool) {
	agent, ok := h.loadAgentForUser(w, r, chi.URLParam(r, "id"))
	if !ok || !h.canManageAgent(w, r, agent) { return db.Agent{}, db.AgentSource{}, false }
	source, err := h.Queries.GetAgentSourceByAgentID(r.Context(), agent.ID)
	if err != nil {
		writeError(w, http.StatusNotFound, "agent source not found")
		return db.Agent{}, db.AgentSource{}, false
	}
	if source.ManagedSourceKey.Valid {
		writeError(w, http.StatusConflict, "this Agent source is updated automatically by Multica")
		return db.Agent{}, db.AgentSource{}, false
	}
	if !source.GithubInstallationID.Valid {
		writeError(w, http.StatusConflict, "GitHub installation is disconnected")
		return db.Agent{}, db.AgentSource{}, false
	}
	return agent, source, true
}

func (h *Handler) saveAgentSourcePreview(r *http.Request, workspaceID pgtype.UUID, agent db.Agent, source db.AgentSource, resolved resolvedGitHubAgentSource, stateHash string) (db.AgentSourcePreview, error) {
	userID, err := parseUUIDValue(requestUserID(r))
	if err != nil { return db.AgentSourcePreview{}, err }
	snapshot, err := json.Marshal(resolved.snapshot)
	if err != nil { return db.AgentSourcePreview{}, err }
	if len(snapshot) > 64 << 20 { return db.AgentSourcePreview{}, errors.New("agent preview exceeds the snapshot size limit") }
	if err := h.Queries.DeleteExpiredAgentSourcePreviews(r.Context(), db.DeleteExpiredAgentSourcePreviewsParams{WorkspaceID:workspaceID, CreatedBy:userID}); err != nil {
		return db.AgentSourcePreview{}, err
	}
	return h.Queries.CreateAgentSourcePreview(r.Context(), db.CreateAgentSourcePreviewParams{
		WorkspaceID:workspaceID, CreatedBy:userID, AgentID:agent.ID, AgentSourceID:source.ID,
		GithubInstallationID:resolved.installation.ID, Repository:resolved.repository.FullName, Ref:resolved.ref,
		ResolvedSha:resolved.sha, ExpectedSourceSha:source.SyncedCommitSha, ExpectedStateHash:stateHash, Snapshot:snapshot,
	})
}

func (h *Handler) readAgentSourcePreview(r *http.Request, workspaceID pgtype.UUID, id string) (db.AgentSourcePreview, error) {
	previewID, err := parseUUIDValue(id)
	if err != nil { return db.AgentSourcePreview{}, sourceRequestError(http.StatusBadRequest, "invalid preview_id") }
	userID, err := parseUUIDValue(requestUserID(r))
	if err != nil { return db.AgentSourcePreview{}, sourceRequestError(http.StatusUnauthorized, "user identity is required") }
	preview, err := h.Queries.GetAgentSourcePreview(r.Context(), db.GetAgentSourcePreviewParams{ID:previewID, WorkspaceID:workspaceID, CreatedBy:userID})
	if errors.Is(err, pgx.ErrNoRows) { return preview, sourceRequestError(http.StatusNotFound, "agent source preview not found") }
	if err != nil { return preview, err }
	if !preview.AppliedAt.Valid && !preview.ExpiresAt.Time.After(time.Now()) {
		return preview, sourceRequestError(http.StatusConflict, "agent source preview expired; preview again")
	}
	return preview, nil
}

func (h *Handler) resolveAgentSourcePreview(ctx context.Context, preview db.AgentSourcePreview) (resolvedGitHubAgentSource, error) {
	// Recheck current Git permission, but never resolve the branch a second time.
	resolved, err := h.resolveGitHubAgentRepository(ctx, preview.WorkspaceID, GitHubAgentSourceInput{
		InstallationID:uuidToString(preview.GithubInstallationID), Repository:preview.Repository, Ref:preview.Ref,
	})
	if err != nil { return resolvedGitHubAgentSource{}, err }
	if err := json.Unmarshal(preview.Snapshot, &resolved.snapshot); err != nil { return resolvedGitHubAgentSource{}, err }
	if err := agentsource.ValidateBundle(resolved.snapshot.Definition); err != nil { return resolvedGitHubAgentSource{}, err }
	resolved.sha, resolved.bundle = preview.ResolvedSha, resolved.snapshot.Definition
	return resolved, nil
}

func lockSourcePreview(ctx context.Context, queries *db.Queries, preview db.AgentSourcePreview) (db.AgentSourcePreview, error) {
	locked, err := queries.LockAgentSourcePreview(ctx, db.LockAgentSourcePreviewParams{ID:preview.ID, WorkspaceID:preview.WorkspaceID, CreatedBy:preview.CreatedBy})
	if errors.Is(err, pgx.ErrNoRows) {
		return locked, sourceRequestError(http.StatusConflict, "agent source preview is no longer available; preview again")
	}
	if err != nil { return locked, err }
	if !locked.AppliedAt.Valid && !locked.ExpiresAt.Time.After(time.Now()) {
		return locked, sourceRequestError(http.StatusConflict, "agent source preview expired; preview again")
	}
	return locked, nil
}

func markSourcePreviewApplied(ctx context.Context, queries *db.Queries, preview db.AgentSourcePreview, source db.AgentSource, changed bool) error {
	response, err := json.Marshal(agentSourceToResponse(source))
	if err != nil { return err }
	_, err = queries.MarkAgentSourcePreviewApplied(ctx, db.MarkAgentSourcePreviewAppliedParams{
		ID:preview.ID, AgentID:source.AgentID, AppliedSource:response, AppliedChanged:changed,
	})
	return err
}

func (h *Handler) writeCreatedSourceReplay(w http.ResponseWriter, r *http.Request, preview db.AgentSourcePreview) {
	agent, err := h.Queries.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{ID:preview.AgentID, WorkspaceID:preview.WorkspaceID})
	if err != nil { writeError(w, http.StatusConflict, "the imported Agent no longer exists"); return }
	if !h.canManageAgent(w, r, agent) { return }
	response := h.agentToResponse(agent)
	if err := h.attachAgentSkills(r.Context(), &response, agent.ID); err != nil { writeError(w, http.StatusInternalServerError, "failed to load imported skills"); return }
	if err := h.enrichAgentResponseWithTargets(r.Context(), &response, agent.ID); err != nil { writeError(w, http.StatusInternalServerError, "failed to load imported Agent access"); return }
	actorType, _ := h.resolveActor(r, requestUserID(r), uuidToString(agent.WorkspaceID))
	redactAgentResponseForActor(&response, actorType)
	writeJSON(w, http.StatusOK, map[string]any{"agent":response, "source":json.RawMessage(preview.AppliedSource), "warnings":[]string{}})
}

func sourceDefinitionFiles(bundle agentsource.Bundle) map[string]string {
	files := map[string]string{"instructions":bundle.Instructions}
	for _, skill := range bundle.Skills {
		prefix := "skills/" + skill.SourcePath + "/"
		files[prefix + "name"] = skill.Name
		files[prefix + "description"] = skill.Description
		files[prefix + "enabled"] = strconv.FormatBool(!skill.Disabled)
		files[prefix + "SKILL.md"] = skill.Content
		for _, file := range skill.Files { files[prefix + "files/" + file.Path] = file.Content }
	}
	return files
}

// sourceStateFiles must run in a transaction. All writers to supporting files
// lock their parent skill, allowing preview/confirmation to read one state.
func sourceStateFiles(ctx context.Context, queries *db.Queries, agent db.Agent, source db.AgentSource) (map[string]string, string, error) {
	skills, err := queries.LockSourceSkills(ctx, source.ID)
	if err != nil { return nil, "", err }
	assignments, err := queries.LockSourceSkillAssignments(ctx, source.ID)
	if err != nil { return nil, "", err }
	mappings, err := queries.ListAgentSourceSkills(ctx, source.ID)
	if err != nil { return nil, "", err }
	paths := map[pgtype.UUID]string{}
	for _, mapping := range mappings { paths[mapping.SkillID] = mapping.SourcePath }
	enabled := map[pgtype.UUID]bool{}
	for _, assignment := range assignments {
		if assignment.AgentID == agent.ID { enabled[assignment.SkillID] = assignment.Enabled }
	}
	files := map[string]string{"instructions":agent.Instructions}
	for _, skill := range skills {
		prefix := "skills/" + paths[skill.ID] + "/"
		name := skill.Name
		name = strings.TrimSuffix(name, sourceManagedSkillName("", source.ID))
		files[prefix + "name"] = name
		files[prefix + "description"] = skill.Description
		files[prefix + "SKILL.md"] = skill.Content
		if value, assigned := enabled[skill.ID]; assigned { files[prefix + "enabled"] = strconv.FormatBool(value) }
		supporting, err := queries.ListSkillFiles(ctx, skill.ID)
		if err != nil { return nil, "", err }
		for _, file := range supporting { files[prefix + "files/" + file.Path] = file.Content }
	}
	state := struct {
		Files map[string]string
		SourceID pgtype.UUID
		InstallationID pgtype.UUID
		Repository string
		Ref string
		SHA string
		RuntimeID pgtype.UUID
		OwnerID pgtype.UUID
		Mappings []db.AgentSourceSkill
	}{files, source.ID, source.GithubInstallationID, source.RepoOwner + "/" + source.RepoName, source.Ref, source.SyncedCommitSha, agent.RuntimeID, agent.OwnerID, mappings}
	encoded, err := json.Marshal(state)
	if err != nil { return nil, "", err }
	digest := sha256.Sum256(encoded)
	return files, hex.EncodeToString(digest[:]), nil
}

func (h *Handler) PreviewAgentSourceSync(w http.ResponseWriter, r *http.Request) {
	agent, source, ok := h.loadGitHubSourceForManage(w, r)
	if !ok { return }
	var request struct { Ref string `json:"ref"` }
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil { writeError(w, http.StatusBadRequest, "invalid request body"); return }
	if request.Ref == "" { request.Ref = source.Ref }
	resolved, err := h.resolveAndCompileGitHubAgent(r.Context(), agent.WorkspaceID, GitHubAgentSourceInput{
		InstallationID:uuidToString(source.GithubInstallationID), Repository:source.RepoOwner + "/" + source.RepoName, Ref:request.Ref,
	})
	if err != nil { writeGitHubSourceError(w, err); return }
	base, err := h.publishedSourceSnapshot(r.Context(), agent, source, resolved.installation.InstallationID)
	if err != nil { writeGitHubSourceError(w, err); return }
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil { writeError(w, http.StatusInternalServerError, "failed to read source state"); return }
	defer tx.Rollback(r.Context())
	queries := h.Queries.WithTx(tx)
	lockedAgent, err := queries.GetAgentForUpdate(r.Context(), agent.ID)
	if err != nil { writeError(w, http.StatusConflict, "Agent changed while preparing preview"); return }
	lockedSource, err := queries.LockAgentSourceByAgentID(r.Context(), agent.ID)
	if err != nil || lockedSource.SyncedCommitSha != source.SyncedCommitSha || lockedSource.Ref != source.Ref || lockedSource.GithubInstallationID != source.GithubInstallationID {
		writeError(w, http.StatusConflict, "Agent source changed while preparing preview"); return
	}
	current, stateHash, err := sourceStateFiles(r.Context(), queries, lockedAgent, lockedSource)
	if err != nil { writeError(w, http.StatusInternalServerError, "failed to read source skills"); return }
	if err := tx.Commit(r.Context()); err != nil { writeError(w, http.StatusInternalServerError, "failed to read source state"); return }
	preview, err := h.saveAgentSourcePreview(r, agent.WorkspaceID, agent, source, resolved, stateHash)
	if err != nil { writeError(w, http.StatusInternalServerError, "failed to save source preview"); return }
	changes := agentsource.DiffFiles(current, sourceDefinitionFiles(resolved.bundle))
	writeJSON(w, http.StatusOK, AgentSourceSyncPreviewResponse{
		PreviewID:uuidToString(preview.ID), ExpiresAt:timestampToString(preview.ExpiresAt), RepositoryURL:"https://github.com/" + resolved.repository.FullName,
		Ref:resolved.ref, BaseSHA:source.SyncedCommitSha, ResolvedSHA:resolved.sha,
		GitChanges:agentsource.DiffRepository(base, resolved.snapshot), ConfigurationChanges:changes, Warnings:resolved.bundle.Warnings,
		Changed:len(changes) > 0 || source.Ref != resolved.ref || source.SyncedCommitSha != resolved.sha,
	})
}

func (h *Handler) publishedSourceSnapshot(ctx context.Context, agent db.Agent, source db.AgentSource, installationID int64) (agentsource.RepositorySnapshot, error) {
	previous, err := h.Queries.LatestAppliedAgentSourcePreview(ctx, db.LatestAppliedAgentSourcePreviewParams{AgentID:agent.ID, WorkspaceID:agent.WorkspaceID})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) { return agentsource.RepositorySnapshot{}, err }
	if err == nil && previous.ResolvedSha == source.SyncedCommitSha {
		var snapshot agentsource.RepositorySnapshot
		if err := json.Unmarshal(previous.Snapshot, &snapshot); err != nil { return snapshot, err }
		return snapshot, agentsource.ValidateBundle(snapshot.Definition)
	}
	// Sources created before previews existed have no stored Git baseline yet.
	return agentsource.ReadDTARepository(ctx, h.GitHubApp, agentsource.Source{
		InstallationID:installationID, Owner:source.RepoOwner, Repository:source.RepoName, CommitSHA:source.SyncedCommitSha,
	})
}
