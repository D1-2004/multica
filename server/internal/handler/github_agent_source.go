package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/agentsource"
	"github.com/multica-ai/multica/server/internal/githubapp"
	agentpkg "github.com/multica-ai/multica/server/pkg/agent"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

var immutableGitSHA = regexp.MustCompile(`^[0-9a-fA-F]{40}([0-9a-fA-F]{24})?$`)

type GitHubAgentRepositoryResponse struct {
	InstallationID string `json:"installation_id"`
	FullName       string `json:"full_name"`
	Private        bool   `json:"private"`
	DefaultBranch  string `json:"default_branch"`
	HTMLURL        string `json:"html_url"`
}

type GitHubAgentSourceInput struct {
	InstallationID string `json:"installation_id"`
	Repository     string `json:"repository"`
	Ref            string `json:"ref"`
	ResolvedSHA    string `json:"resolved_sha"`
}

type GitHubAgentPreviewRequest struct {
	InstallationID string `json:"installation_id"`
	Repository     string `json:"repository"`
	Ref            string `json:"ref"`
}

type GitHubAgentPreviewResponse struct {
	InstallationID      string                    `json:"installation_id"`
	Repository          string                    `json:"repository"`
	Ref                 string                    `json:"ref"`
	ResolvedSHA         string                    `json:"resolved_sha"`
	Name                string                    `json:"name"`
	Description         string                    `json:"description"`
	Instructions        string                    `json:"instructions"`
	Skills              []GitHubAgentSkillPreview `json:"skills"`
	CompatibleProviders []string                  `json:"compatible_providers"`
	Warnings            []string                  `json:"warnings"`
	Blockers            []string                  `json:"blockers"`
}

type GitHubAgentSkillPreview struct {
	SourcePath  string `json:"source_path"`
	Name        string `json:"name"`
	Description string `json:"description"`
	FileCount   int    `json:"file_count"`
}

type CreateGitHubAgentRequest struct {
	CreateAgentRequest
	InstallationID string `json:"installation_id"`
	Repository     string `json:"repository"`
	Ref            string `json:"ref"`
	ResolvedSHA    string `json:"resolved_sha"`
}

type AgentSourceResponse struct {
	AgentID           string  `json:"agent_id"`
	SourceType        string  `json:"source_type"`
	InstallationID    *string `json:"installation_id"`
	Repository        string  `json:"repository"`
	Ref               string  `json:"ref"`
	ManifestPath      string  `json:"manifest_path"`
	SyncedCommitSHA   string  `json:"synced_commit_sha"`
	SyncStatus        string  `json:"sync_status"`
	LastSyncError     *string `json:"last_sync_error"`
	LastSyncAttemptAt *string `json:"last_sync_attempt_at"`
	LastSyncedAt      string  `json:"last_synced_at"`
	GitHubConnected   bool    `json:"github_connected"`
}

type AgentSourceSyncResponse struct {
	Source   AgentSourceResponse `json:"source"`
	Changed  bool                `json:"changed"`
	Warnings []string            `json:"warnings"`
}

type resolvedGitHubAgentSource struct {
	installation db.GithubInstallation
	repository   githubapp.Repository
	ref          string
	sha          string
	bundle       agentsource.Bundle
}

func (h *Handler) ListGitHubAgentRepositories(w http.ResponseWriter, r *http.Request) {
	workspaceID := workspaceIDFromURL(r, "id")
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	installationID, ok := parseUUIDOrBadRequest(w, r.URL.Query().Get("installation_id"), "installation_id")
	if !ok {
		return
	}
	if h.GitHubApp == nil {
		writeError(w, http.StatusServiceUnavailable, "GitHub agent sources are unavailable")
		return
	}
	installation, err := h.Queries.GetGitHubInstallationInWorkspace(r.Context(), db.GetGitHubInstallationInWorkspaceParams{
		ID: installationID, WorkspaceID: wsUUID,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "GitHub installation not found")
		return
	}
	repositories, err := h.GitHubApp.ListRepositories(r.Context(), installation.InstallationID)
	if err != nil {
		writeGitHubSourceError(w, err)
		return
	}
	response := make([]GitHubAgentRepositoryResponse, 0, len(repositories))
	for _, repository := range repositories {
		response = append(response, GitHubAgentRepositoryResponse{
			InstallationID: uuidToString(installation.ID),
			FullName:       repository.FullName, Private: repository.Private,
			DefaultBranch: repository.DefaultBranch, HTMLURL: repository.HTMLURL,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"repositories": response})
}

func (h *Handler) PreviewGitHubAgent(w http.ResponseWriter, r *http.Request) {
	workspaceID := workspaceIDFromURL(r, "id")
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	var request GitHubAgentPreviewRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	resolved, err := h.resolveAndCompileGitHubAgent(r.Context(), wsUUID, GitHubAgentSourceInput{
		InstallationID: request.InstallationID,
		Repository:     request.Repository,
		Ref:            request.Ref,
	})
	if err != nil {
		writeGitHubSourceError(w, err)
		return
	}
	blockers, err := h.githubAgentPreviewBlockers(r.Context(), wsUUID, resolved.bundle)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to check GitHub agent conflicts")
		return
	}
	writeJSON(w, http.StatusOK, GitHubAgentPreviewResponse{
		InstallationID:      uuidToString(resolved.installation.ID),
		Repository:          resolved.repository.FullName,
		Ref:                 resolved.ref,
		ResolvedSHA:         resolved.sha,
		Name:                resolved.bundle.Manifest.Metadata.Name,
		Description:         resolved.bundle.Manifest.Metadata.Description,
		Instructions:        resolved.bundle.Instructions,
		Skills:              sourceSkillPreviews(resolved.bundle.Skills),
		CompatibleProviders: resolved.bundle.Manifest.Spec.Compatibility.Providers,
		Warnings:            resolved.bundle.Warnings,
		Blockers:            blockers,
	})
}

func (h *Handler) githubAgentPreviewBlockers(_ context.Context, _ pgtype.UUID, _ agentsource.Bundle) ([]string, error) {
	// Source-managed skills are materialized under a source-scoped storage name,
	// so two Agents may safely use the same repository skill without sharing a
	// mutable skill row. Runtime snapshots remain isolated per Agent Source.
	return []string{}, nil
}

func sourceSkillPreviews(skills []agentsource.Skill) []GitHubAgentSkillPreview {
	previews := make([]GitHubAgentSkillPreview, 0, len(skills))
	for _, compiled := range skills {
		previews = append(previews, GitHubAgentSkillPreview{
			SourcePath: compiled.SourcePath, Name: compiled.Name,
			Description: compiled.Description, FileCount: len(compiled.Files),
		})
	}
	return previews
}

func (h *Handler) CreateGitHubAgent(w http.ResponseWriter, r *http.Request) {
	workspaceID := workspaceIDFromURL(r, "id")
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	ownerID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	ownerUUID := parseUUID(ownerID)
	var request CreateGitHubAgentRequest
	rawFields, err := decodeJSONBodyWithRawFields(r.Body, &request)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if !immutableGitSHA.MatchString(request.ResolvedSHA) {
		writeError(w, http.StatusBadRequest, "resolved_sha must be an immutable Git commit SHA")
		return
	}
	resolved, err := h.resolveAndCompileGitHubAgent(r.Context(), wsUUID, GitHubAgentSourceInput{
		InstallationID: request.InstallationID,
		Repository:     request.Repository,
		Ref:            request.Ref,
		ResolvedSHA:    request.ResolvedSHA,
	})
	if err != nil {
		writeGitHubSourceError(w, err)
		return
	}
	agentName, agentDescription, err := gitAgentInstanceProfile(request, rawFields, resolved.bundle)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	if request.RuntimeID == "" {
		writeError(w, http.StatusBadRequest, "runtime_id is required")
		return
	}
	runtimeUUID, ok := parseUUIDOrBadRequest(w, request.RuntimeID, "runtime_id")
	if !ok {
		return
	}
	runtime, err := h.Queries.GetAgentRuntimeForWorkspace(r.Context(), db.GetAgentRuntimeForWorkspaceParams{ID: runtimeUUID, WorkspaceID: wsUUID})
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid runtime_id")
		return
	}
	member, ok := h.workspaceMember(w, r, workspaceID)
	if !ok {
		return
	}
	if !canUseRuntimeForAgent(member, runtime) {
		writeError(w, http.StatusForbidden, "this runtime is private; only its owner or a workspace admin can create agents on it")
		return
	}
	if !providerCompatible(resolved.bundle.Manifest.Spec.Compatibility.Providers, runtime.Provider) {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("runtime provider %q is not allowed by the manifest", runtime.Provider))
		return
	}
	if !agentpkg.IsKnownThinkingValue(runtime.Provider, request.ThinkingLevel) {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("thinking_level %q is not recognised for runtime %q", request.ThinkingLevel, runtime.Provider))
		return
	}
	if request.Visibility == "" {
		request.Visibility = "private"
	}
	if request.MaxConcurrentTasks == 0 {
		request.MaxConcurrentTasks = 6
	}
	_, hasTargets := rawFields["invocation_targets"]
	legacyVisibility := request.Visibility
	permission, _, err := parsePermissionInput(wsUUID, request.PermissionMode, request.InvocationTargets, request.PermissionMode != nil, hasTargets, &legacyVisibility)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	preserveMaskedGatewayToken(request.RuntimeConfig, nil)
	runtimeConfig, _ := json.Marshal(request.RuntimeConfig)
	if request.RuntimeConfig == nil {
		runtimeConfig = []byte("{}")
	}
	if !h.validateDWSProfileConfigForOwner(w, r, wsUUID, ownerUUID, runtimeConfig) {
		return
	}
	customEnv, _ := json.Marshal(request.CustomEnv)
	if request.CustomEnv == nil {
		customEnv = []byte("{}")
	}
	customArgs, _ := json.Marshal(request.CustomArgs)
	if request.CustomArgs == nil {
		customArgs = []byte("[]")
	}
	var mcpConfig []byte
	if rawMCP, present := rawFields["mcp_config"]; present && !bytes.Equal(bytes.TrimSpace(rawMCP), []byte("null")) {
		mcpConfig = append([]byte(nil), rawMCP...)
	}
	allowlist := normaliseComposioToolkitAllowlist(request.ComposioToolkitAllowlist)
	if !h.composioMCPAppsEnabled(r.Context()) {
		allowlist = nil
	}
	manualSkills, ok := parseUUIDSliceOrBadRequest(w, request.SkillIDs, "skill_ids")
	if !ok {
		return
	}
	for _, skillID := range manualSkills {
		if _, err := h.Queries.GetSkillInWorkspace(r.Context(), db.GetSkillInWorkspaceParams{ID: skillID, WorkspaceID: wsUUID}); err != nil {
			writeError(w, http.StatusBadRequest, "skill does not belong to this workspace")
			return
		}
		if managed, err := h.isSourceManagedSkill(r.Context(), skillID); err != nil || managed {
			writeError(w, http.StatusBadRequest, "source-managed skills cannot be attached manually")
			return
		}
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start agent create transaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	created, err := qtx.CreateAgent(r.Context(), db.CreateAgentParams{
		WorkspaceID:  wsUUID,
		Name:         agentName,
		Description:  agentDescription,
		Instructions: resolved.bundle.Instructions,
		AvatarUrl:    ptrToText(request.AvatarURL),
		RuntimeMode:  runtime.RuntimeMode, RuntimeConfig: runtimeConfig, RuntimeID: runtime.ID,
		Visibility: permission.legacyVisibility(), PermissionMode: permission.mode,
		MaxConcurrentTasks: request.MaxConcurrentTasks, OwnerID: ownerUUID,
		CustomEnv: customEnv, CustomArgs: customArgs, McpConfig: mcpConfig,
		Model:                    pgtype.Text{String: request.Model, Valid: request.Model != ""},
		ThinkingLevel:            pgtype.Text{String: request.ThinkingLevel, Valid: request.ThinkingLevel != ""},
		ComposioToolkitAllowlist: allowlist,
	})
	if err != nil {
		writeAgentSourceDatabaseError(w, err)
		return
	}
	if err := replaceInvocationTargetsWithQueries(r.Context(), qtx, created.ID, ownerUUID, permission.targets); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save agent access")
		return
	}
	source, err := qtx.CreateAgentSource(r.Context(), db.CreateAgentSourceParams{
		AgentID: created.ID, GithubInstallationID: resolved.installation.ID,
		RepoOwner: ownerFromFullName(resolved.repository.FullName), RepoName: repoFromFullName(resolved.repository.FullName),
		Ref: resolved.ref, ManifestPath: agentsource.ManifestPath, SyncedCommitSha: resolved.sha, CreatedBy: ownerUUID,
	})
	if err != nil {
		writeAgentSourceDatabaseError(w, err)
		return
	}
	for _, skillID := range manualSkills {
		if err := qtx.AddAgentSkill(r.Context(), db.AddAgentSkillParams{AgentID: created.ID, SkillID: skillID}); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to attach agent skill")
			return
		}
	}
	for _, compiledSkill := range resolved.bundle.Skills {
		skillRow, err := createSourceSkillInTx(r.Context(), qtx, wsUUID, ownerUUID, resolved, source.ID, compiledSkill)
		if err != nil {
			writeAgentSourceDatabaseError(w, err)
			return
		}
		if err := qtx.AddAgentSkill(r.Context(), db.AddAgentSkillParams{AgentID: created.ID, SkillID: skillRow.ID}); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to attach source skill")
			return
		}
		if _, err := qtx.CreateAgentSourceSkill(r.Context(), db.CreateAgentSourceSkillParams{AgentSourceID: source.ID, SkillID: skillRow.ID, SourcePath: compiledSkill.SourcePath}); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to track source skill")
			return
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit GitHub agent create")
		return
	}

	if runtime.Status == "online" && h.TaskService != nil {
		h.TaskService.ReconcileAgentStatus(r.Context(), created.ID)
		created, _ = h.Queries.GetAgent(r.Context(), created.ID)
	}
	response := agentToResponse(created)
	_ = h.attachAgentSkills(r.Context(), &response, created.ID)
	_ = h.enrichAgentResponseWithTargets(r.Context(), &response, created.ID)
	actorType, actorID := h.resolveActor(r, ownerID, workspaceID)
	h.publish(protocol.EventAgentCreated, workspaceID, actorType, actorID, map[string]any{"agent": broadcastAgentResponse(response)})
	if h.TaskService != nil {
		h.sendAgentWelcomeChat(r.Context(), created, ownerID, workspaceID)
	}
	redactAgentResponseForActor(&response, actorType)
	warnings := append([]string{}, resolved.bundle.Warnings...)
	if currentSHA, resolveErr := h.GitHubApp.ResolveCommit(
		r.Context(),
		resolved.installation.InstallationID,
		ownerFromFullName(resolved.repository.FullName),
		repoFromFullName(resolved.repository.FullName),
		resolved.ref,
	); resolveErr == nil && !strings.EqualFold(currentSHA, resolved.sha) {
		warnings = append(warnings, "the configured Git ref advanced after preview; the agent was created from the previewed commit")
	}
	payload := map[string]any{
		"agent":    response,
		"source":   agentSourceToResponse(source),
		"warnings": warnings,
	}
	if templateKey, ok := r.Context().Value(gitAgentTemplateContextKey{}).(string); ok && templateKey != "" {
		payload["agent_id"] = response.ID
		payload["template_key"] = templateKey
	}
	writeJSON(w, http.StatusCreated, payload)
}

func gitAgentInstanceProfile(request CreateGitHubAgentRequest, rawFields map[string]json.RawMessage, bundle agentsource.Bundle) (string, string, error) {
	name := strings.TrimSpace(request.Name)
	if name == "" {
		name = bundle.Manifest.Metadata.Name
	}
	description := request.Description
	if _, present := rawFields["description"]; !present {
		description = bundle.Manifest.Metadata.Description
	}
	if utf8.RuneCountInString(description) > maxAgentDescriptionLength {
		return "", "", fmt.Errorf("description must be %d characters or fewer", maxAgentDescriptionLength)
	}
	return name, description, nil
}

func (h *Handler) GetAgentSource(w http.ResponseWriter, r *http.Request) {
	agentRow, ok := h.loadAgentForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	source, err := h.Queries.GetAgentSourceByAgentID(r.Context(), agentRow.ID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "agent source not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load agent source")
		return
	}
	writeJSON(w, http.StatusOK, agentSourceToResponse(source))
}

func (h *Handler) SyncAgentSource(w http.ResponseWriter, r *http.Request) {
	agentRow, ok := h.loadAgentForUser(w, r, chi.URLParam(r, "id"))
	if !ok || !h.canManageAgent(w, r, agentRow) {
		return
	}
	source, err := h.Queries.GetAgentSourceByAgentID(r.Context(), agentRow.ID)
	if err != nil {
		writeError(w, http.StatusNotFound, "agent source not found")
		return
	}
	if !source.GithubInstallationID.Valid {
		writeError(w, http.StatusConflict, "GitHub installation is disconnected")
		return
	}
	installation, err := h.Queries.GetGitHubInstallationInWorkspace(r.Context(), db.GetGitHubInstallationInWorkspaceParams{ID: source.GithubInstallationID, WorkspaceID: agentRow.WorkspaceID})
	if err != nil || h.GitHubApp == nil {
		writeError(w, http.StatusConflict, "GitHub installation is disconnected")
		return
	}
	sha, err := h.GitHubApp.ResolveCommit(r.Context(), installation.InstallationID, source.RepoOwner, source.RepoName, source.Ref)
	if err != nil {
		h.recordAgentSourceFailure(r.Context(), source.ID, err)
		writeGitHubSourceError(w, err)
		return
	}
	bundle, err := agentsource.Compile(r.Context(), h.GitHubApp, agentsource.Source{InstallationID: installation.InstallationID, Owner: source.RepoOwner, Repository: source.RepoName, CommitSHA: sha})
	if err != nil {
		h.recordAgentSourceFailure(r.Context(), source.ID, err)
		writeGitHubSourceError(w, err)
		return
	}
	if agentRow.RuntimeID.Valid {
		runtime, runtimeErr := h.Queries.GetAgentRuntimeForWorkspace(r.Context(), db.GetAgentRuntimeForWorkspaceParams{ID: agentRow.RuntimeID, WorkspaceID: agentRow.WorkspaceID})
		if runtimeErr != nil {
			writeError(w, http.StatusConflict, "agent runtime is unavailable")
			return
		}
		if !providerCompatible(bundle.Manifest.Spec.Compatibility.Providers, runtime.Provider) {
			err = fmt.Errorf("runtime provider %q is not allowed by the manifest", runtime.Provider)
			h.recordAgentSourceFailure(r.Context(), source.ID, err)
			writeError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start source sync")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	recordTransactionFailure := func(syncErr error, status int, publicMessage string, databaseError bool) {
		_ = tx.Rollback(r.Context())
		h.recordAgentSourceFailure(r.Context(), source.ID, syncErr)
		if databaseError {
			writeAgentSourceDatabaseError(w, syncErr)
			return
		}
		writeError(w, status, publicMessage)
	}
	locked, err := qtx.LockAgentSourceByAgentID(r.Context(), agentRow.ID)
	if err != nil {
		writeError(w, http.StatusNotFound, "agent source not found")
		return
	}
	if locked.SyncedCommitSha != source.SyncedCommitSha {
		writeError(w, http.StatusConflict, "agent source changed while this sync was running")
		return
	}
	if locked.SyncedCommitSha == sha {
		locked, err = qtx.MarkAgentSourceSyncSucceeded(r.Context(), db.MarkAgentSourceSyncSucceededParams{ID: locked.ID, SyncedCommitSha: sha})
		if err != nil {
			recordTransactionFailure(fmt.Errorf("record unchanged source sync: %w", err), http.StatusInternalServerError, "failed to record source sync", false)
			return
		}
		if err := tx.Commit(r.Context()); err != nil {
			h.recordAgentSourceFailure(r.Context(), source.ID, fmt.Errorf("commit unchanged source sync: %w", err))
			writeError(w, http.StatusInternalServerError, "failed to record source sync")
			return
		}
		h.publishAgentSourceSync(r, agentRow, false)
		writeJSON(w, http.StatusOK, AgentSourceSyncResponse{Source: agentSourceToResponse(locked), Changed: false, Warnings: bundle.Warnings})
		return
	}
	if _, err := qtx.UpdateAgent(r.Context(), gitAgentSourceSnapshotUpdate(agentRow.ID, bundle)); err != nil {
		recordTransactionFailure(fmt.Errorf("update agent snapshot: %w", err), http.StatusInternalServerError, "failed to update agent snapshot", true)
		return
	}
	existingMappings, err := qtx.ListAgentSourceSkills(r.Context(), locked.ID)
	if err != nil {
		recordTransactionFailure(fmt.Errorf("load source skills: %w", err), http.StatusInternalServerError, "failed to load source skills", false)
		return
	}
	byPath := make(map[string]db.AgentSourceSkill, len(existingMappings))
	for _, mapping := range existingMappings {
		byPath[mapping.SourcePath] = mapping
	}
	for _, compiledSkill := range bundle.Skills {
		mapping, exists := byPath[compiledSkill.SourcePath]
		if exists {
			if err := updateSourceSkillInTx(r.Context(), qtx, mapping.SkillID, resolvedGitHubAgentSource{installation: installation, repository: githubapp.Repository{FullName: source.RepoOwner + "/" + source.RepoName}, ref: source.Ref, sha: sha, bundle: bundle}, locked.ID, compiledSkill); err != nil {
				recordTransactionFailure(fmt.Errorf("update source skill %q: %w", compiledSkill.SourcePath, err), http.StatusInternalServerError, "failed to update source skill", true)
				return
			}
			delete(byPath, compiledSkill.SourcePath)
			continue
		}
		createdSkill, err := createSourceSkillInTx(r.Context(), qtx, agentRow.WorkspaceID, agentRow.OwnerID, resolvedGitHubAgentSource{installation: installation, repository: githubapp.Repository{FullName: source.RepoOwner + "/" + source.RepoName}, ref: source.Ref, sha: sha, bundle: bundle}, locked.ID, compiledSkill)
		if err != nil {
			recordTransactionFailure(fmt.Errorf("create source skill %q: %w", compiledSkill.SourcePath, err), http.StatusInternalServerError, "failed to create source skill", true)
			return
		}
		if err := qtx.AddAgentSkill(r.Context(), db.AddAgentSkillParams{AgentID: agentRow.ID, SkillID: createdSkill.ID}); err != nil {
			recordTransactionFailure(fmt.Errorf("attach source skill %q: %w", compiledSkill.SourcePath, err), http.StatusInternalServerError, "failed to attach source skill", false)
			return
		}
		if _, err := qtx.CreateAgentSourceSkill(r.Context(), db.CreateAgentSourceSkillParams{AgentSourceID: locked.ID, SkillID: createdSkill.ID, SourcePath: compiledSkill.SourcePath}); err != nil {
			recordTransactionFailure(fmt.Errorf("track source skill %q: %w", compiledSkill.SourcePath, err), http.StatusInternalServerError, "failed to track source skill", false)
			return
		}
	}
	for _, removed := range byPath {
		if err := qtx.RemoveAgentSkill(r.Context(), db.RemoveAgentSkillParams{AgentID: agentRow.ID, SkillID: removed.SkillID}); err != nil {
			recordTransactionFailure(fmt.Errorf("detach removed source skill %q: %w", removed.SourcePath, err), http.StatusInternalServerError, "failed to detach removed source skill", false)
			return
		}
		if err := qtx.DeleteAgentSourceSkill(r.Context(), db.DeleteAgentSourceSkillParams{AgentSourceID: locked.ID, SkillID: removed.SkillID}); err != nil {
			recordTransactionFailure(fmt.Errorf("remove source skill mapping %q: %w", removed.SourcePath, err), http.StatusInternalServerError, "failed to remove source skill mapping", false)
			return
		}
		if err := qtx.DeleteSkill(r.Context(), db.DeleteSkillParams{ID: removed.SkillID, WorkspaceID: agentRow.WorkspaceID}); err != nil {
			recordTransactionFailure(fmt.Errorf("delete removed source skill %q: %w", removed.SourcePath, err), http.StatusInternalServerError, "failed to delete removed source skill", false)
			return
		}
	}
	updatedSource, err := qtx.MarkAgentSourceSyncSucceeded(r.Context(), db.MarkAgentSourceSyncSucceededParams{ID: locked.ID, SyncedCommitSha: sha})
	if err != nil {
		recordTransactionFailure(fmt.Errorf("record source sync success: %w", err), http.StatusInternalServerError, "failed to record source sync", false)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		h.recordAgentSourceFailure(r.Context(), source.ID, fmt.Errorf("commit source sync: %w", err))
		writeError(w, http.StatusInternalServerError, "failed to commit source sync")
		return
	}
	h.publishAgentSourceSync(r, agentRow, true)
	writeJSON(w, http.StatusOK, AgentSourceSyncResponse{Source: agentSourceToResponse(updatedSource), Changed: true, Warnings: bundle.Warnings})
}

// gitAgentSourceSnapshotUpdate is intentionally narrow: repository sync owns
// executable instructions, while the Multica Agent owns its display name and
// description after creation. Keep those profile fields unset so a manifest
// update cannot silently rename an existing Agent.
func gitAgentSourceSnapshotUpdate(agentID pgtype.UUID, bundle agentsource.Bundle) db.UpdateAgentParams {
	return db.UpdateAgentParams{
		ID:           agentID,
		Instructions: pgtype.Text{String: bundle.Instructions, Valid: true},
	}
}

func (h *Handler) publishAgentSourceSync(r *http.Request, agentRow db.Agent, changed bool) {
	refreshed, err := h.Queries.GetAgent(r.Context(), agentRow.ID)
	if err != nil {
		slog.Warn("load agent after GitHub source sync", "error", err, "agent_id", uuidToString(agentRow.ID))
		return
	}
	response := agentToResponse(refreshed)
	if err := h.attachAgentSkills(r.Context(), &response, refreshed.ID); err != nil {
		slog.Warn("load agent skills after GitHub source sync", "error", err, "agent_id", uuidToString(agentRow.ID))
		return
	}
	if err := h.enrichAgentResponseWithTargets(r.Context(), &response, refreshed.ID); err != nil {
		slog.Warn("load agent access after GitHub source sync", "error", err, "agent_id", uuidToString(agentRow.ID))
		return
	}
	workspaceID := uuidToString(refreshed.WorkspaceID)
	actorType, actorID := h.resolveActor(r, requestUserID(r), workspaceID)
	h.publish(protocol.EventAgentStatus, workspaceID, actorType, actorID, map[string]any{"agent": broadcastAgentResponse(response)})
	if changed {
		h.publish(protocol.EventSkillUpdated, workspaceID, actorType, actorID, map[string]any{
			"agent_id": uuidToString(refreshed.ID), "source_sync": true,
		})
	}
}

func (h *Handler) resolveAndCompileGitHubAgent(ctx context.Context, workspaceID pgtype.UUID, input GitHubAgentSourceInput) (resolvedGitHubAgentSource, error) {
	if h.GitHubApp == nil {
		return resolvedGitHubAgentSource{}, githubapp.ErrUnavailable
	}
	installationUUID, err := parseUUIDValue(input.InstallationID)
	if err != nil {
		return resolvedGitHubAgentSource{}, errors.New("invalid installation_id")
	}
	installation, err := h.Queries.GetGitHubInstallationInWorkspace(ctx, db.GetGitHubInstallationInWorkspaceParams{ID: installationUUID, WorkspaceID: workspaceID})
	if err != nil {
		return resolvedGitHubAgentSource{}, errors.New("GitHub installation not found")
	}
	repositories, err := h.GitHubApp.ListRepositories(ctx, installation.InstallationID)
	if err != nil {
		return resolvedGitHubAgentSource{}, err
	}
	var repository githubapp.Repository
	for _, candidate := range repositories {
		if strings.EqualFold(candidate.FullName, input.Repository) {
			repository = candidate
			break
		}
	}
	if repository.FullName == "" {
		return resolvedGitHubAgentSource{}, errors.New("repository is not accessible to this GitHub installation")
	}
	ref := strings.TrimSpace(input.Ref)
	if ref == "" {
		ref = repository.DefaultBranch
	}
	if ref == "" || len(ref) > 255 || strings.ContainsRune(ref, '\x00') {
		return resolvedGitHubAgentSource{}, errors.New("invalid Git ref")
	}
	sha := strings.TrimSpace(input.ResolvedSHA)
	if sha == "" {
		sha, err = h.GitHubApp.ResolveCommit(ctx, installation.InstallationID, ownerFromFullName(repository.FullName), repoFromFullName(repository.FullName), ref)
		if err != nil {
			return resolvedGitHubAgentSource{}, err
		}
	} else if !immutableGitSHA.MatchString(sha) {
		return resolvedGitHubAgentSource{}, errors.New("resolved_sha must be an immutable Git commit SHA")
	}
	bundle, err := agentsource.Compile(ctx, h.GitHubApp, agentsource.Source{
		InstallationID: installation.InstallationID,
		Owner:          ownerFromFullName(repository.FullName), Repository: repoFromFullName(repository.FullName), CommitSHA: sha,
	})
	if err != nil {
		return resolvedGitHubAgentSource{}, err
	}
	return resolvedGitHubAgentSource{installation: installation, repository: repository, ref: ref, sha: sha, bundle: bundle}, nil
}

func createSourceSkillInTx(ctx context.Context, queries *db.Queries, workspaceID, creatorID pgtype.UUID, source resolvedGitHubAgentSource, agentSourceID pgtype.UUID, compiled agentsource.Skill) (db.Skill, error) {
	config, err := json.Marshal(sourceSkillConfig(source, compiled.SourcePath))
	if err != nil {
		return db.Skill{}, err
	}
	created, err := queries.CreateSkill(ctx, db.CreateSkillParams{
		WorkspaceID: workspaceID, Name: sourceManagedSkillName(compiled.Name, agentSourceID),
		Description: sanitizeNullBytes(compiled.Description), Content: sanitizeNullBytes(compiled.Content),
		Config: config, CreatedBy: creatorID,
	})
	if err != nil {
		return db.Skill{}, err
	}
	for _, file := range compiled.Files {
		if _, err := queries.UpsertSkillFile(ctx, db.UpsertSkillFileParams{SkillID: created.ID, Path: sanitizeNullBytes(file.Path), Content: sanitizeNullBytes(file.Content)}); err != nil {
			return db.Skill{}, err
		}
	}
	return created, nil
}

func updateSourceSkillInTx(ctx context.Context, queries *db.Queries, skillID pgtype.UUID, source resolvedGitHubAgentSource, agentSourceID pgtype.UUID, compiled agentsource.Skill) error {
	config, err := json.Marshal(sourceSkillConfig(source, compiled.SourcePath))
	if err != nil {
		return err
	}
	if _, err := queries.UpdateSkill(ctx, db.UpdateSkillParams{
		ID: skillID, Name: pgtype.Text{String: sourceManagedSkillName(compiled.Name, agentSourceID), Valid: true},
		Description: pgtype.Text{String: sanitizeNullBytes(compiled.Description), Valid: true},
		Content:     pgtype.Text{String: sanitizeNullBytes(compiled.Content), Valid: true}, Config: config,
	}); err != nil {
		return err
	}
	if err := queries.DeleteSkillFilesBySkill(ctx, skillID); err != nil {
		return err
	}
	for _, file := range compiled.Files {
		if _, err := queries.UpsertSkillFile(ctx, db.UpsertSkillFileParams{SkillID: skillID, Path: sanitizeNullBytes(file.Path), Content: sanitizeNullBytes(file.Content)}); err != nil {
			return err
		}
	}
	return nil
}

func sourceManagedSkillName(name string, agentSourceID pgtype.UUID) string {
	sourceID := strings.ReplaceAll(uuidToString(agentSourceID), "-", "")
	if len(sourceID) > 16 {
		sourceID = sourceID[:16]
	}
	name = sanitizeNullBytes(strings.TrimSpace(name))
	if sourceID == "" {
		return name
	}
	return name + "--" + sourceID
}

func sourceSkillConfig(source resolvedGitHubAgentSource, sourcePath string) map[string]any {
	return map[string]any{"origin": map[string]any{
		"type": "github_agent_source", "repository": source.repository.FullName,
		"ref": source.ref, "commit_sha": source.sha, "path": sourcePath,
	}}
}

func (h *Handler) isSourceManagedSkill(ctx context.Context, skillID pgtype.UUID) (bool, error) {
	_, err := h.Queries.GetAgentSourceSkillBySkillID(ctx, skillID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (h *Handler) recordAgentSourceFailure(ctx context.Context, sourceID pgtype.UUID, syncErr error) {
	message := syncErr.Error()
	if len(message) > 1000 {
		message = message[:1000]
	}
	if _, err := h.Queries.MarkAgentSourceSyncFailed(ctx, db.MarkAgentSourceSyncFailedParams{ID: sourceID, LastSyncError: pgtype.Text{String: message, Valid: true}}); err != nil {
		slog.Warn("record GitHub agent source failure", "error", err, "agent_source_id", uuidToString(sourceID))
	}
}

func agentSourceToResponse(source db.AgentSource) AgentSourceResponse {
	status := source.SyncStatus
	connected := source.GithubInstallationID.Valid
	if !connected {
		status = "disconnected"
	}
	var installationID *string
	if connected {
		value := uuidToString(source.GithubInstallationID)
		installationID = &value
	}
	return AgentSourceResponse{
		AgentID: uuidToString(source.AgentID), SourceType: source.SourceType,
		InstallationID: installationID, Repository: source.RepoOwner + "/" + source.RepoName,
		Ref: source.Ref, ManifestPath: source.ManifestPath, SyncedCommitSHA: source.SyncedCommitSha,
		SyncStatus: status, LastSyncError: textToPtr(source.LastSyncError),
		LastSyncAttemptAt: timestampToPtr(source.LastSyncAttemptAt), LastSyncedAt: timestampToString(source.LastSyncedAt),
		GitHubConnected: connected,
	}
}

func providerCompatible(allowed []string, provider string) bool {
	if len(allowed) == 0 {
		return true
	}
	for _, candidate := range allowed {
		if candidate == provider {
			return true
		}
	}
	return false
}

func ownerFromFullName(fullName string) string {
	owner, _, _ := strings.Cut(fullName, "/")
	return owner
}

func repoFromFullName(fullName string) string {
	_, repository, _ := strings.Cut(fullName, "/")
	return repository
}

func parseUUIDValue(value string) (pgtype.UUID, error) {
	var parsed pgtype.UUID
	if err := parsed.Scan(value); err != nil || !parsed.Valid {
		return pgtype.UUID{}, errors.New("invalid UUID")
	}
	return parsed, nil
}

func writeGitHubSourceError(w http.ResponseWriter, err error) {
	var apiErr *githubapp.APIError
	switch {
	case errors.Is(err, githubapp.ErrUnavailable):
		writeError(w, http.StatusServiceUnavailable, "GitHub agent sources are unavailable")
	case errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound:
		writeError(w, http.StatusNotFound, "GitHub repository content not found")
	case errors.As(err, &apiErr) && (apiErr.StatusCode == http.StatusUnauthorized || apiErr.StatusCode == http.StatusForbidden):
		writeError(w, http.StatusForbidden, "GitHub installation cannot access this repository")
	case errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusTooManyRequests:
		writeError(w, http.StatusTooManyRequests, "GitHub rate limit exceeded")
	case strings.Contains(err.Error(), "manifest") || strings.Contains(err.Error(), "skill") || strings.Contains(err.Error(), "required file") || strings.Contains(err.Error(), "bundle"):
		writeError(w, http.StatusUnprocessableEntity, err.Error())
	default:
		writeError(w, http.StatusBadGateway, err.Error())
	}
}

func writeAgentSourceDatabaseError(w http.ResponseWriter, err error) {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		writeError(w, http.StatusConflict, "an agent or skill with this name already exists in the workspace")
		return
	}
	writeError(w, http.StatusInternalServerError, "failed to materialize GitHub agent")
}
