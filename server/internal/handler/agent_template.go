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
	"github.com/multica-ai/multica/server/internal/agenttemplate"
	agentpkg "github.com/multica-ai/multica/server/pkg/agent"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

var agentTemplateSlugPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

type AgentTemplateGitHubSourceResponse struct {
	InstallationID  *string `json:"installation_id"`
	Repository      string  `json:"repository"`
	Ref             string  `json:"ref"`
	SyncedCommitSHA string  `json:"synced_commit_sha"`
	SyncStatus      string  `json:"sync_status"`
	LastSyncError   *string `json:"last_sync_error"`
	LastSyncAttempt *string `json:"last_sync_attempt_at"`
	LastSyncedAt    string  `json:"last_synced_at"`
	GitHubConnected bool    `json:"github_connected"`
}

type AgentTemplateResponse struct {
	ID                  string                             `json:"id"`
	Slug                string                             `json:"slug"`
	DisplayName         string                             `json:"display_name"`
	Description         string                             `json:"description"`
	SourceType          string                             `json:"source_type"`
	ManagementMode      string                             `json:"management_mode"`
	ContentHash         string                             `json:"content_hash"`
	BundleSizeBytes     int32                              `json:"bundle_size_bytes"`
	Instructions        string                             `json:"instructions,omitempty"`
	CompatibleProviders []string                           `json:"compatible_providers,omitempty"`
	Skills              []GitHubAgentSkillPreview          `json:"skills,omitempty"`
	Warnings            []string                           `json:"warnings,omitempty"`
	GitHubSource        *AgentTemplateGitHubSourceResponse `json:"github_source,omitempty"`
	CreatedAt           string                             `json:"created_at"`
	UpdatedAt           string                             `json:"updated_at"`
}

type CreateGitHubAgentTemplateRequest struct {
	Slug           string `json:"slug"`
	InstallationID string `json:"installation_id"`
	Repository     string `json:"repository"`
	Ref            string `json:"ref"`
}

type AgentTemplateSyncResponse struct {
	Template AgentTemplateResponse `json:"template"`
	Changed  bool                  `json:"changed"`
	Warnings []string              `json:"warnings"`
}

type CreateAgentFromTemplateRequest struct {
	CreateAgentRequest
	TemplateSkillPaths []string `json:"template_skill_paths"`
}

func (h *Handler) ListAgentTemplates(w http.ResponseWriter, r *http.Request) {
	workspaceID := workspaceIDFromURL(r, "id")
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	rows, err := h.Queries.ListAgentTemplatesByWorkspace(r.Context(), wsUUID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list agent templates")
		return
	}
	items := make([]AgentTemplateResponse, 0, len(rows))
	for _, row := range rows {
		items = append(items, agentTemplateListRowToResponse(row))
	}
	writeJSON(w, http.StatusOK, map[string]any{"templates": items})
}

func (h *Handler) GetAgentTemplate(w http.ResponseWriter, r *http.Request) {
	workspaceID := workspaceIDFromURL(r, "id")
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	template, err := h.Queries.GetAgentTemplateByWorkspaceAndSlug(r.Context(), db.GetAgentTemplateByWorkspaceAndSlugParams{
		WorkspaceID: wsUUID, Slug: chi.URLParam(r, "slug"),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "agent template not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load agent template")
		return
	}
	response, err := h.agentTemplateDetail(r.Context(), template)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "agent template snapshot is invalid")
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *Handler) CreateGitHubAgentTemplate(w http.ResponseWriter, r *http.Request) {
	workspaceID := workspaceIDFromURL(r, "id")
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	creatorID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	var request CreateGitHubAgentTemplateRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	request.Slug = strings.ToLower(strings.TrimSpace(request.Slug))
	if !agentTemplateSlugPattern.MatchString(request.Slug) {
		writeError(w, http.StatusBadRequest, "slug must contain only lowercase letters, numbers, and hyphens")
		return
	}
	resolved, err := h.resolveAndCompileGitHubAgent(r.Context(), wsUUID, GitHubAgentSourceInput{
		InstallationID: request.InstallationID, Repository: strings.TrimSpace(request.Repository), Ref: request.Ref,
	})
	if err != nil {
		writeGitHubSourceError(w, err)
		return
	}
	bundleJSON, err := json.Marshal(resolved.bundle)
	if err != nil || len(bundleJSON) > agentsource.MaxBundleSize {
		writeError(w, http.StatusUnprocessableEntity, "GitHub agent template bundle is too large")
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start agent template create")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	created, err := qtx.CreateGitHubAgentTemplate(r.Context(), db.CreateGitHubAgentTemplateParams{
		WorkspaceID: wsUUID, Slug: request.Slug,
		DisplayName:         resolved.bundle.Manifest.Metadata.Name,
		Description:         resolved.bundle.Manifest.Metadata.Description,
		BundleSchemaVersion: agenttemplate.BundleSchemaVersion,
		BundleSizeBytes:     int32(len(bundleJSON)), Bundle: bundleJSON,
		ContentHash: resolved.bundle.Hash, CreatedBy: parseUUID(creatorID),
	})
	if err != nil {
		writeAgentTemplateDatabaseError(w, err)
		return
	}
	if _, err := qtx.CreateAgentTemplateGitHubSource(r.Context(), db.CreateAgentTemplateGitHubSourceParams{
		TemplateID: created.ID, WorkspaceID: wsUUID, GithubInstallationID: resolved.installation.ID,
		RepoOwner: ownerFromFullName(resolved.repository.FullName), RepoName: repoFromFullName(resolved.repository.FullName),
		Ref: resolved.ref, SyncedCommitSha: resolved.sha,
	}); err != nil {
		writeAgentTemplateDatabaseError(w, err)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit agent template create")
		return
	}
	response, _ := h.agentTemplateDetail(r.Context(), created)
	writeJSON(w, http.StatusCreated, response)
}

func (h *Handler) SyncAgentTemplate(w http.ResponseWriter, r *http.Request) {
	workspaceID := workspaceIDFromURL(r, "id")
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	template, err := h.Queries.GetAgentTemplateByWorkspaceAndSlug(r.Context(), db.GetAgentTemplateByWorkspaceAndSlugParams{
		WorkspaceID: wsUUID, Slug: chi.URLParam(r, "slug"),
	})
	if err != nil || template.SourceType != "github" || template.ManagementMode != "workspace_managed" {
		writeError(w, http.StatusNotFound, "workspace-managed GitHub agent template not found")
		return
	}
	source, err := h.Queries.GetAgentTemplateGitHubSource(r.Context(), db.GetAgentTemplateGitHubSourceParams{WorkspaceID: wsUUID, TemplateID: template.ID})
	if err != nil || !source.GithubInstallationID.Valid {
		writeError(w, http.StatusConflict, "GitHub installation is disconnected")
		return
	}
	resolved, err := h.resolveAndCompileGitHubAgent(r.Context(), wsUUID, GitHubAgentSourceInput{
		InstallationID: uuidToString(source.GithubInstallationID),
		Repository:     source.RepoOwner + "/" + source.RepoName, Ref: source.Ref,
	})
	if err != nil {
		h.recordAgentTemplateSyncFailure(r.Context(), wsUUID, template.ID, err)
		writeGitHubSourceError(w, err)
		return
	}
	bundleJSON, err := json.Marshal(resolved.bundle)
	if err != nil || len(bundleJSON) > agentsource.MaxBundleSize {
		err = errors.New("GitHub agent template bundle is too large")
		h.recordAgentTemplateSyncFailure(r.Context(), wsUUID, template.ID, err)
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start agent template sync")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	locked, err := qtx.LockAgentTemplateGitHubSource(r.Context(), db.LockAgentTemplateGitHubSourceParams{WorkspaceID: wsUUID, TemplateID: template.ID})
	if err != nil {
		writeError(w, http.StatusConflict, "agent template source changed while sync was running")
		return
	}
	if locked.GithubInstallationID != source.GithubInstallationID || locked.Ref != source.Ref || locked.RepoOwner != source.RepoOwner || locked.RepoName != source.RepoName || locked.SyncedCommitSha != source.SyncedCommitSha {
		writeError(w, http.StatusConflict, "agent template source changed while sync was running")
		return
	}
	changed := template.ContentHash != resolved.bundle.Hash
	if changed {
		template, err = qtx.ReplaceGitHubAgentTemplateSnapshot(r.Context(), db.ReplaceGitHubAgentTemplateSnapshotParams{
			WorkspaceID: wsUUID, ID: template.ID,
			DisplayName:         resolved.bundle.Manifest.Metadata.Name,
			Description:         resolved.bundle.Manifest.Metadata.Description,
			BundleSchemaVersion: agenttemplate.BundleSchemaVersion,
			BundleSizeBytes:     int32(len(bundleJSON)), Bundle: bundleJSON, ContentHash: resolved.bundle.Hash,
		})
		if err != nil {
			writeAgentTemplateDatabaseError(w, err)
			return
		}
	}
	if _, err := qtx.MarkAgentTemplateSyncSucceeded(r.Context(), db.MarkAgentTemplateSyncSucceededParams{
		WorkspaceID: wsUUID, TemplateID: template.ID, SyncedCommitSha: resolved.sha,
	}); err != nil {
		writeAgentTemplateDatabaseError(w, err)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit agent template sync")
		return
	}
	response, _ := h.agentTemplateDetail(r.Context(), template)
	writeJSON(w, http.StatusOK, AgentTemplateSyncResponse{Template: response, Changed: changed, Warnings: resolved.bundle.Warnings})
}

func (h *Handler) DeleteAgentTemplate(w http.ResponseWriter, r *http.Request) {
	workspaceID := workspaceIDFromURL(r, "id")
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	count, err := h.Queries.DeleteWorkspaceManagedAgentTemplate(r.Context(), db.DeleteWorkspaceManagedAgentTemplateParams{
		WorkspaceID: wsUUID, Slug: chi.URLParam(r, "slug"),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete agent template")
		return
	}
	if count == 0 {
		writeError(w, http.StatusNotFound, "workspace-managed GitHub agent template not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) CreateAgentFromTemplate(w http.ResponseWriter, r *http.Request) {
	workspaceID := workspaceIDFromURL(r, "id")
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	ownerID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	var request CreateAgentFromTemplateRequest
	rawFields, err := decodeJSONBodyWithRawFields(r.Body, &request)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	template, err := h.Queries.GetAgentTemplateByWorkspaceAndSlug(r.Context(), db.GetAgentTemplateByWorkspaceAndSlugParams{
		WorkspaceID: wsUUID, Slug: chi.URLParam(r, "slug"),
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "agent template not found")
		return
	}
	bundle, err := agenttemplate.DecodeBundle(template.Bundle, template.ContentHash, template.BundleSizeBytes)
	if err != nil {
		slog.Error("invalid persisted agent template bundle", "template_id", uuidToString(template.ID), "error", err)
		writeError(w, http.StatusInternalServerError, "agent template snapshot is invalid")
		return
	}
	selectedSkills, err := selectTemplateSkills(bundle.Skills, request.TemplateSkillPaths, rawFields["template_skill_paths"] != nil)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	bundle.Skills = selectedSkills
	runtimeConfig, _ := json.Marshal(request.RuntimeConfig)
	if request.RuntimeConfig == nil {
		runtimeConfig = []byte("{}")
	}
	if !h.validateDWSProfileConfigForOwner(w, r, wsUUID, parseUUID(ownerID), runtimeConfig) {
		return
	}
	created, response, err := h.materializeTemplateAgent(r, wsUUID, parseUUID(ownerID), request.CreateAgentRequest, rawFields, bundle)
	if err != nil {
		writeMaterializeTemplateError(w, err)
		return
	}
	actorType, actorID := h.resolveActor(r, ownerID, workspaceID)
	h.publish(protocol.EventAgentCreated, workspaceID, actorType, actorID, map[string]any{"agent": broadcastAgentResponse(response)})
	if h.TaskService != nil {
		h.sendAgentWelcomeChat(r.Context(), created, ownerID, workspaceID)
	}
	redactAgentResponseForActor(&response, actorType)
	writeJSON(w, http.StatusCreated, map[string]any{
		"agent": response, "agent_id": response.ID, "template_slug": template.Slug, "warnings": bundle.Warnings,
	})
}

func selectTemplateSkills(available []agentsource.Skill, requested []string, specified bool) ([]agentsource.Skill, error) {
	if !specified {
		return available, nil
	}
	requestedSet := make(map[string]struct{}, len(requested))
	for _, sourcePath := range requested {
		if strings.TrimSpace(sourcePath) == "" {
			return nil, errors.New("template_skill_paths must not contain empty paths")
		}
		if _, duplicate := requestedSet[sourcePath]; duplicate {
			return nil, fmt.Errorf("template_skill_paths contains duplicate path %q", sourcePath)
		}
		requestedSet[sourcePath] = struct{}{}
	}
	selected := make([]agentsource.Skill, 0, len(requestedSet))
	for _, skill := range available {
		if _, ok := requestedSet[skill.SourcePath]; ok {
			selected = append(selected, skill)
			delete(requestedSet, skill.SourcePath)
		}
	}
	for unknown := range requestedSet {
		return nil, fmt.Errorf("template skill %q is not part of this template", unknown)
	}
	return selected, nil
}

func (h *Handler) materializeTemplateAgent(r *http.Request, workspaceID, ownerID pgtype.UUID, request CreateAgentRequest, rawFields map[string]json.RawMessage, bundle agentsource.Bundle) (db.Agent, AgentResponse, error) {
	if strings.TrimSpace(request.RuntimeID) == "" {
		return db.Agent{}, AgentResponse{}, templateRequestError("runtime_id is required")
	}
	runtimeID, err := parseUUIDValue(request.RuntimeID)
	if err != nil {
		return db.Agent{}, AgentResponse{}, templateRequestError("invalid runtime_id")
	}
	runtime, err := h.Queries.GetAgentRuntimeForWorkspace(r.Context(), db.GetAgentRuntimeForWorkspaceParams{ID: runtimeID, WorkspaceID: workspaceID})
	if err != nil {
		return db.Agent{}, AgentResponse{}, templateRequestError("invalid runtime_id")
	}
	member, err := h.Queries.GetMemberByUserAndWorkspace(r.Context(), db.GetMemberByUserAndWorkspaceParams{WorkspaceID: workspaceID, UserID: ownerID})
	if err != nil || !canUseRuntimeForAgent(member, runtime) {
		return db.Agent{}, AgentResponse{}, templateForbiddenError("this runtime is private; only its owner or a workspace admin can create agents on it")
	}
	if !providerCompatible(bundle.Manifest.Spec.Compatibility.Providers, runtime.Provider) {
		return db.Agent{}, AgentResponse{}, templateRequestError(fmt.Sprintf("runtime provider %q is not allowed by the template", runtime.Provider))
	}
	if !agentpkg.IsKnownThinkingValue(runtime.Provider, request.ThinkingLevel) {
		return db.Agent{}, AgentResponse{}, templateRequestError(fmt.Sprintf("thinking_level %q is not recognised for runtime %q", request.ThinkingLevel, runtime.Provider))
	}
	name := strings.TrimSpace(request.Name)
	if name == "" {
		name = bundle.Manifest.Metadata.Name
	}
	description := request.Description
	if _, present := rawFields["description"]; !present {
		description = bundle.Manifest.Metadata.Description
	}
	if utf8.RuneCountInString(description) > maxAgentDescriptionLength {
		return db.Agent{}, AgentResponse{}, templateRequestError(fmt.Sprintf("description must be %d characters or fewer", maxAgentDescriptionLength))
	}
	if request.Visibility == "" {
		request.Visibility = "private"
	}
	if request.MaxConcurrentTasks == 0 {
		request.MaxConcurrentTasks = 6
	}
	_, hasTargets := rawFields["invocation_targets"]
	legacyVisibility := request.Visibility
	permission, _, err := parsePermissionInput(workspaceID, request.PermissionMode, request.InvocationTargets, request.PermissionMode != nil, hasTargets, &legacyVisibility)
	if err != nil {
		return db.Agent{}, AgentResponse{}, templateRequestError(err.Error())
	}
	preserveMaskedGatewayToken(request.RuntimeConfig, nil)
	runtimeConfig, _ := json.Marshal(request.RuntimeConfig)
	if request.RuntimeConfig == nil {
		runtimeConfig = []byte("{}")
	}
	allowlist := normaliseComposioToolkitAllowlist(request.ComposioToolkitAllowlist)
	if !h.composioMCPAppsEnabled(r.Context()) {
		allowlist = nil
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
	manualSkills := make([]pgtype.UUID, 0, len(request.SkillIDs))
	for _, rawID := range request.SkillIDs {
		skillID, err := parseUUIDValue(rawID)
		if err != nil {
			return db.Agent{}, AgentResponse{}, templateRequestError("invalid skill_ids")
		}
		if _, err := h.Queries.GetSkillInWorkspace(r.Context(), db.GetSkillInWorkspaceParams{ID: skillID, WorkspaceID: workspaceID}); err != nil {
			return db.Agent{}, AgentResponse{}, templateRequestError("skill does not belong to this workspace")
		}
		if managed, err := h.isSourceManagedSkill(r.Context(), skillID); err != nil || managed {
			return db.Agent{}, AgentResponse{}, templateRequestError("source-managed skills cannot be attached manually")
		}
		manualSkills = append(manualSkills, skillID)
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		return db.Agent{}, AgentResponse{}, err
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	created, err := materializeAgentBundleInTx(r.Context(), qtx, db.CreateAgentParams{
		WorkspaceID: workspaceID, Name: name, Description: description, Instructions: bundle.Instructions,
		AvatarUrl: ptrToText(request.AvatarURL), RuntimeMode: runtime.RuntimeMode,
		RuntimeConfig: runtimeConfig, RuntimeID: runtime.ID,
		Visibility: permission.legacyVisibility(), PermissionMode: permission.mode,
		MaxConcurrentTasks: request.MaxConcurrentTasks, OwnerID: ownerID,
		CustomEnv: customEnv, CustomArgs: customArgs, McpConfig: mcpConfig,
		Model:                    pgtype.Text{String: request.Model, Valid: request.Model != ""},
		ThinkingLevel:            pgtype.Text{String: request.ThinkingLevel, Valid: request.ThinkingLevel != ""},
		ComposioToolkitAllowlist: allowlist,
	}, permission, manualSkills, func(created db.Agent) error {
		for _, compiled := range bundle.Skills {
			createdSkill, createErr := qtx.CreateSkill(r.Context(), db.CreateSkillParams{
				WorkspaceID: workspaceID, Name: materializedTemplateSkillName(compiled.Name, created.ID),
				Description: sanitizeNullBytes(compiled.Description), Content: sanitizeNullBytes(compiled.Content),
				Config: []byte("{}"), CreatedBy: ownerID,
			})
			if createErr != nil {
				return createErr
			}
			for _, file := range compiled.Files {
				if _, createErr := qtx.UpsertSkillFile(r.Context(), db.UpsertSkillFileParams{SkillID: createdSkill.ID, Path: sanitizeNullBytes(file.Path), Content: sanitizeNullBytes(file.Content)}); createErr != nil {
					return createErr
				}
			}
			if createErr := qtx.AddAgentSkill(r.Context(), db.AddAgentSkillParams{AgentID: created.ID, SkillID: createdSkill.ID}); createErr != nil {
				return createErr
			}
		}
		return nil
	})
	if err != nil {
		return db.Agent{}, AgentResponse{}, err
	}
	if err := tx.Commit(r.Context()); err != nil {
		return db.Agent{}, AgentResponse{}, err
	}
	if runtime.Status == "online" && h.TaskService != nil {
		h.TaskService.ReconcileAgentStatus(r.Context(), created.ID)
		created, _ = h.Queries.GetAgent(r.Context(), created.ID)
	}
	response := agentToResponse(created)
	_ = h.attachAgentSkills(r.Context(), &response, created.ID)
	_ = h.enrichAgentResponseWithTargets(r.Context(), &response, created.ID)
	return created, response, nil
}

func (h *Handler) agentTemplateDetail(ctx context.Context, template db.AgentTemplate) (AgentTemplateResponse, error) {
	bundle, err := agenttemplate.DecodeBundle(template.Bundle, template.ContentHash, template.BundleSizeBytes)
	if err != nil {
		return AgentTemplateResponse{}, err
	}
	response := agentTemplateToResponse(template)
	response.Instructions = bundle.Instructions
	response.CompatibleProviders = bundle.Manifest.Spec.Compatibility.Providers
	response.Skills = sourceSkillPreviews(bundle.Skills)
	response.Warnings = bundle.Warnings
	if template.SourceType == "github" {
		source, err := h.Queries.GetAgentTemplateGitHubSource(ctx, db.GetAgentTemplateGitHubSourceParams{WorkspaceID: template.WorkspaceID, TemplateID: template.ID})
		if err != nil {
			return AgentTemplateResponse{}, err
		}
		response.GitHubSource = agentTemplateGitHubSourceToResponse(source)
	}
	return response, nil
}

func agentTemplateToResponse(template db.AgentTemplate) AgentTemplateResponse {
	return AgentTemplateResponse{
		ID: uuidToString(template.ID), Slug: template.Slug, DisplayName: template.DisplayName,
		Description: template.Description, SourceType: template.SourceType, ManagementMode: template.ManagementMode,
		ContentHash: template.ContentHash, BundleSizeBytes: template.BundleSizeBytes,
		CreatedAt: timestampToString(template.CreatedAt), UpdatedAt: timestampToString(template.UpdatedAt),
	}
}

func agentTemplateListRowToResponse(row db.ListAgentTemplatesByWorkspaceRow) AgentTemplateResponse {
	response := AgentTemplateResponse{
		ID: uuidToString(row.ID), Slug: row.Slug, DisplayName: row.DisplayName, Description: row.Description,
		SourceType: row.SourceType, ManagementMode: row.ManagementMode, ContentHash: row.ContentHash,
		BundleSizeBytes: row.BundleSizeBytes, CreatedAt: timestampToString(row.CreatedAt), UpdatedAt: timestampToString(row.UpdatedAt),
	}
	if row.SourceType == "github" {
		status := row.SyncStatus.String
		connected := row.GithubInstallationID.Valid
		if !connected {
			status = "disconnected"
		}
		response.GitHubSource = &AgentTemplateGitHubSourceResponse{
			InstallationID: uuidToPtr(row.GithubInstallationID), Repository: row.RepoOwner.String + "/" + row.RepoName.String,
			Ref: row.Ref.String, SyncedCommitSHA: row.SyncedCommitSha.String, SyncStatus: status,
			LastSyncError: textToPtr(row.LastSyncError), LastSyncAttempt: timestampToPtr(row.LastSyncAttemptAt),
			LastSyncedAt: timestampToString(row.LastSyncedAt), GitHubConnected: connected,
		}
	}
	return response
}

func agentTemplateGitHubSourceToResponse(source db.AgentTemplateGithubSource) *AgentTemplateGitHubSourceResponse {
	status := source.SyncStatus
	connected := source.GithubInstallationID.Valid
	if !connected {
		status = "disconnected"
	}
	return &AgentTemplateGitHubSourceResponse{
		InstallationID: uuidToPtr(source.GithubInstallationID), Repository: source.RepoOwner + "/" + source.RepoName,
		Ref: source.Ref, SyncedCommitSHA: source.SyncedCommitSha, SyncStatus: status,
		LastSyncError: textToPtr(source.LastSyncError), LastSyncAttempt: timestampToPtr(source.LastSyncAttemptAt),
		LastSyncedAt: timestampToString(source.LastSyncedAt), GitHubConnected: connected,
	}
}

func materializedTemplateSkillName(name string, agentID pgtype.UUID) string {
	id := strings.ReplaceAll(uuidToString(agentID), "-", "")
	if len(id) > 16 {
		id = id[:16]
	}
	return sanitizeNullBytes(strings.TrimSpace(name)) + "--" + id
}

func (h *Handler) recordAgentTemplateSyncFailure(ctx context.Context, workspaceID, templateID pgtype.UUID, syncErr error) {
	message := syncErr.Error()
	if len(message) > 1000 {
		message = message[:1000]
	}
	if _, err := h.Queries.MarkAgentTemplateSyncFailed(ctx, db.MarkAgentTemplateSyncFailedParams{
		WorkspaceID: workspaceID, TemplateID: templateID, LastSyncError: pgtype.Text{String: message, Valid: true},
	}); err != nil {
		slog.Warn("record agent template sync failure", "template_id", uuidToString(templateID), "error", err)
	}
}

type materializeTemplateError struct {
	status  int
	message string
}

func (e materializeTemplateError) Error() string { return e.message }
func templateRequestError(message string) error {
	return materializeTemplateError{status: http.StatusBadRequest, message: message}
}
func templateForbiddenError(message string) error {
	return materializeTemplateError{status: http.StatusForbidden, message: message}
}

func writeMaterializeTemplateError(w http.ResponseWriter, err error) {
	var requestErr materializeTemplateError
	if errors.As(err, &requestErr) {
		writeError(w, requestErr.status, requestErr.message)
		return
	}
	writeAgentTemplateDatabaseError(w, err)
}

func writeAgentTemplateDatabaseError(w http.ResponseWriter, err error) {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		writeError(w, http.StatusConflict, "an agent template, agent, or skill with this name already exists in the workspace")
		return
	}
	writeError(w, http.StatusInternalServerError, "failed to persist agent template snapshot")
}
