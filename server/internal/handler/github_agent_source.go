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
	"github.com/multica-ai/multica/server/internal/coordinatorcontract"
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
	CoordinatorContract *coordinatorcontract.Contract `json:"coordinator_contract"`
	Definition          map[string]any                `json:"definition"`
	Requirements        PackageRequirements           `json:"requirements"`
	PreviewID           string                        `json:"preview_id"`
	ExpiresAt           string                        `json:"expires_at"`
	RepositoryURL       string                        `json:"repository_url"`
	InstallationID      string                        `json:"installation_id"`
	Repository          string                        `json:"repository"`
	Ref                 string                        `json:"ref"`
	ResolvedSHA         string                        `json:"resolved_sha"`
	Name                string                        `json:"name"`
	Description         string                        `json:"description"`
	Instructions        string                        `json:"instructions"`
	Skills              []GitHubAgentSkillPreview     `json:"skills"`
	CompatibleProviders []string                      `json:"compatible_providers"`
	Warnings            []string                      `json:"warnings"`
	Blockers            []string                      `json:"blockers"`
}

type GitHubAgentSkillPreview struct {
	Enabled     bool   `json:"enabled"`
	SourcePath  string `json:"source_path"`
	Name        string `json:"name"`
	Description string `json:"description"`
	FileCount   int    `json:"file_count"`
}

type CreateGitHubAgentRequest struct {
	DshPluginBindings map[string]string `json:"dsh_plugin_bindings"`
	Secrets           map[string]string `json:"secrets"`
	DeferredBindings  []string          `json:"deferred_bindings"`
	CreateAgentRequest
	PreviewID      string `json:"preview_id"`
	InstallationID string `json:"installation_id"`
	Repository     string `json:"repository"`
	Ref            string `json:"ref"`
	ResolvedSHA    string `json:"resolved_sha"`
}

type AgentSourceResponse struct {
	RepositoryURL      string   `json:"repository_url"`
	CanSync            bool     `json:"can_sync"`
	ConfigurationScope []string `json:"configuration_scope"`
	AgentID            string   `json:"agent_id"`
	SourceType         string   `json:"source_type"`
	InstallationID     *string  `json:"installation_id"`
	Repository         string   `json:"repository"`
	Ref                string   `json:"ref"`
	ManifestPath       string   `json:"manifest_path"`
	SyncedCommitSHA    string   `json:"synced_commit_sha"`
	SyncStatus         string   `json:"sync_status"`
	LastSyncError      *string  `json:"last_sync_error"`
	LastSyncAttemptAt  *string  `json:"last_sync_attempt_at"`
	LastSyncedAt       string   `json:"last_synced_at"`
	GitHubConnected    bool     `json:"github_connected"`
}

type AgentSourceSyncResponse struct {
	Source   AgentSourceResponse `json:"source"`
	Changed  bool                `json:"changed"`
	Warnings []string            `json:"warnings"`
}

type preparedAgentSource struct {
	installation          db.GithubInstallation
	repository            githubapp.Repository
	ref                   string
	sha                   string
	bundle                agentsource.Bundle
	snapshot              agentsource.RepositorySnapshot
	rollbackOf            string
	publicationDefinition *agentsource.Bundle
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
	preview, err := h.saveAgentSourcePreview(r, wsUUID, db.Agent{}, db.AgentSource{}, resolved, "")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save GitHub agent preview")
		return
	}
	writeJSON(w, http.StatusOK, GitHubAgentPreviewResponse{
		Definition: packageDefinitionPreview(resolved.bundle), Requirements: packageRequirements(resolved.bundle),
		PreviewID:           uuidToString(preview.ID),
		ExpiresAt:           timestampToString(preview.ExpiresAt),
		RepositoryURL:       "https://github.com/" + resolved.repository.FullName,
		InstallationID:      uuidToString(resolved.installation.ID),
		Repository:          resolved.repository.FullName,
		Ref:                 resolved.ref,
		ResolvedSHA:         resolved.sha,
		Name:                resolved.bundle.Manifest.Metadata.Name,
		Description:         resolved.bundle.Manifest.Metadata.Description,
		Instructions:        resolved.bundle.Instructions,
		CoordinatorContract: resolved.bundle.CoordinatorContract,
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
			Description: compiled.Description, FileCount: len(compiled.Files), Enabled: !compiled.Disabled,
		})
	}
	return previews
}

func (h *Handler) CreateGitHubAgent(w http.ResponseWriter, r *http.Request) {
	h.CreateAgentFromPackage(w, r)
}

// CreateAgentFromPackage is the confirmation path for every prepared source.
// Acquisition is finished before this method; source checks only reauthorize
// the pinned preview and never re-read mutable branch contents.
func (h *Handler) CreateAgentFromPackage(w http.ResponseWriter, r *http.Request) {
	workspaceID := workspaceIDFromURL(r, "id")
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	if _, ok := h.requireWorkspaceRole(w, r, workspaceID, "workspace not found", "owner", "admin"); !ok {
		return
	}
	ownerID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	ownerUUID := parseUUID(ownerID)
	r.Body = http.MaxBytesReader(w, r.Body, 4<<20)
	var request CreateGitHubAgentRequest
	rawFields, err := decodeJSONBodyWithRawFields(r.Body, &request)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	var preview db.AgentSourcePreview
	var resolved preparedAgentSource
	if request.PreviewID != "" {
		preview, err = h.readAgentSourcePreview(r, wsUUID, request.PreviewID)
		if err != nil {
			writeGitHubSourceError(w, err)
			return
		}
		if preview.AgentSourceID.Valid || preview.ExpectedStateHash != "" {
			writeError(w, http.StatusBadRequest, "a sync preview cannot create an Agent")
			return
		}
		if request.InstallationID != "" || request.Repository != "" || request.Ref != "" || request.ResolvedSHA != "" {
			writeError(w, http.StatusBadRequest, "preview_id already fixes the repository, connection and commit; omit source overrides")
			return
		}
		resolved, err = h.resolveAgentSourcePreview(r.Context(), preview)
		if err == nil && preview.AppliedAt.Valid {
			h.writeCreatedSourceReplay(w, r, preview)
			return
		}
	} else {
		// Existing API clients may confirm an immutable SHA directly. The new
		// UI uses preview_id so retries also preserve the created Agent identity.
		if !immutableGitSHA.MatchString(request.ResolvedSHA) {
			writeError(w, http.StatusBadRequest, "preview_id or an immutable resolved_sha is required")
			return
		}
		resolved, err = h.resolveAndCompileGitHubAgent(r.Context(), wsUUID, GitHubAgentSourceInput{
			InstallationID: request.InstallationID, Repository: request.Repository, Ref: request.Ref, ResolvedSHA: request.ResolvedSHA,
		})
		if err == nil {
			preview, err = h.saveAgentSourcePreview(r, wsUUID, db.Agent{}, db.AgentSource{}, resolved, "")
		}
	}
	if err != nil {
		writeGitHubSourceError(w, err)
		return
	}
	definition, err := preparePackageConfiguration(&request, rawFields, resolved.bundle)
	if err != nil {
		writeGitHubSourceError(w, err)
		return
	}
	if err := validatePackageActor(resolved.bundle.Definition, r.Header.Get("X-Actor-Source")); err != nil {
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
	if !agentpkg.IsKnownServiceTier(runtime.Provider, request.ServiceTier) {
		writeError(w, http.StatusBadRequest, "service_tier is not supported by this runtime")
		return
	}
	if err := definition.validateRuntime(runtime); err != nil {
		writeGitHubSourceError(w, err)
		return
	}
	if request.Visibility == "" {
		request.Visibility = "private"
	}
	if request.MaxConcurrentTasks == 0 {
		request.MaxConcurrentTasks = 6
	}
	if err := validateAgentMaxConcurrentTasks(request.MaxConcurrentTasks); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
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
		if resolved.bundle.Definition != nil && len(allowlist) > 0 {
			writeError(w, http.StatusUnprocessableEntity, "Composio apps are unavailable in this workspace")
			return
		}
		if resolved.bundle.Definition == nil {
			allowlist = nil
		}
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
	preview, err = lockSourcePreview(r.Context(), qtx, preview)
	if err != nil {
		writeGitHubSourceError(w, err)
		return
	}
	if preview.AppliedAt.Valid {
		_ = tx.Rollback(r.Context())
		h.writeCreatedSourceReplay(w, r, preview)
		return
	}
	var source db.AgentSource
	createParams := db.CreateAgentParams{
		WorkspaceID:         wsUUID,
		Name:                agentName,
		Description:         agentDescription,
		Instructions:        resolved.bundle.Instructions,
		CoordinatorContract: coordinatorcontract.Marshal(resolved.bundle.CoordinatorContract),
		AvatarUrl:           ptrToText(request.AvatarURL),
		RuntimeMode:         runtime.RuntimeMode, RuntimeConfig: runtimeConfig, RuntimeID: runtime.ID,
		Visibility: permission.legacyVisibility(), PermissionMode: permission.mode,
		MaxConcurrentTasks: request.MaxConcurrentTasks, OwnerID: ownerUUID,
		CustomEnv: customEnv, CustomArgs: customArgs, McpConfig: mcpConfig,
		Model:                    pgtype.Text{String: request.Model, Valid: request.Model != ""},
		ThinkingLevel:            pgtype.Text{String: request.ThinkingLevel, Valid: request.ThinkingLevel != ""},
		ServiceTier:              pgtype.Text{String: request.ServiceTier, Valid: request.ServiceTier != ""},
		ComposioToolkitAllowlist: allowlist,
	}
	if resolved.bundle.Definition != nil {
		// V2 creates only the instance shell here. All package-owned content is
		// written once by the shared Import codecs inside this transaction.
		createParams = db.CreateAgentParams{
			WorkspaceID: wsUUID, OwnerID: ownerUUID, Name: agentName, Description: agentDescription,
			RuntimeMode: runtime.RuntimeMode, RuntimeID: runtime.ID, RuntimeConfig: []byte("{}"),
			CustomEnv: []byte("{}"), CustomArgs: []byte("[]"), MaxConcurrentTasks: 6,
			Visibility: "private", PermissionMode: "private",
		}
		permission = resolvedPermission{mode: "private"}
	}
	created, err := materializeAgentBundleInTx(r.Context(), qtx, createParams, permission, manualSkills, func(created db.Agent) error {
		var createErr error
		if !resolved.installation.ID.Valid {
			source, createErr = qtx.CreateLocalAgentSource(r.Context(), db.CreateLocalAgentSourceParams{AgentID: created.ID, WorkspaceID: wsUUID, SyncedCommitSha: resolved.bundle.Hash, CreatedBy: ownerUUID})
		} else {
			source, createErr = qtx.CreateAgentSource(r.Context(), db.CreateAgentSourceParams{
				AgentID: created.ID, WorkspaceID: wsUUID, GithubInstallationID: resolved.installation.ID,
				RepoOwner: ownerFromFullName(resolved.repository.FullName), RepoName: repoFromFullName(resolved.repository.FullName),
				Ref: resolved.ref, ManifestPath: agentsource.SourceManifestPath(resolved.bundle), SyncedCommitSha: resolved.sha, CreatedBy: ownerUUID,
			})
		}
		if createErr != nil {
			return createErr
		}
		return (agentPackageService{handler: h}).Import(r.Context(), tx, created, source, resolved, request.Secrets, request.DeferredBindings, request.DshPluginBindings, ownerUUID, true)
	})
	if err != nil {
		writeAgentSourceDatabaseError(w, err)
		return
	}
	if err := markSourcePreviewApplied(r.Context(), qtx, preview, source, true); err != nil {
		writeAgentSourceDatabaseError(w, err)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit GitHub agent create")
		return
	}

	if h.EventTriggers != nil {
		h.EventTriggers.Notify()
	}
	created, err = h.Queries.GetAgent(r.Context(), created.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read created Agent")
		return
	}
	if runtime.Status == "online" && h.TaskService != nil {
		h.TaskService.ReconcileAgentStatus(r.Context(), created.ID)
		created, _ = h.Queries.GetAgent(r.Context(), created.ID)
	}
	response := h.agentToResponse(created)
	h.hydrateImportedAgent(r.Context(), &response, created.ID)
	_ = h.attachAgentSkills(r.Context(), &response, created.ID)
	_ = h.enrichAgentResponseWithTargets(r.Context(), &response, created.ID)
	actorType, actorID := h.resolveActor(r, ownerID, workspaceID)
	h.publish(protocol.EventAgentCreated, workspaceID, actorType, actorID, map[string]any{"agent": broadcastAgentResponse(response)})
	if h.TaskService != nil {
		h.sendAgentWelcomeChat(r.Context(), created, ownerID, workspaceID)
	}
	redactAgentResponseForActor(&response, actorType)
	warnings := append([]string{}, resolved.bundle.Warnings...)
	if resolved.installation.ID.Valid {
		if currentSHA, resolveErr := h.GitHubApp.ResolveCommit(
			r.Context(),
			resolved.installation.InstallationID,
			ownerFromFullName(resolved.repository.FullName),
			repoFromFullName(resolved.repository.FullName),
			resolved.ref,
		); resolveErr == nil && !strings.EqualFold(currentSHA, resolved.sha) {
			warnings = append(warnings, "the configured Git ref advanced after preview; the agent was created from the previewed commit")
		}
	}
	warnings = append(warnings, definition.warnings...)
	payload := map[string]any{
		"agent":    response,
		"source":   agentSourceToResponse(source),
		"warnings": warnings,
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

// v1 keeps its historical instance profile; v2 manages declared profile fields.
func gitAgentSourceSnapshotUpdate(agentID pgtype.UUID, bundle agentsource.Bundle) db.UpdateAgentParams {
	contract := coordinatorcontract.Marshal(bundle.CoordinatorContract)
	if contract == nil {
		contract = []byte("null")
	}
	params := db.UpdateAgentParams{ID: agentID, Instructions: pgtype.Text{String: bundle.Instructions, Valid: true}, CoordinatorContract: contract}
	if bundle.Definition != nil {
		params.Name = pgtype.Text{String: bundle.Manifest.Metadata.Name, Valid: true}
		if _, present := bundle.Definition["description"]; present {
			params.Description = pgtype.Text{String: bundle.Manifest.Metadata.Description, Valid: true}
		}
	}
	return params

}

func (h *Handler) publishAgentSourceSync(r *http.Request, agentRow db.Agent, changed bool) {
	refreshed, err := h.Queries.GetAgent(r.Context(), agentRow.ID)
	if err != nil {
		slog.Warn("load agent after GitHub source sync", "error", err, "agent_id", uuidToString(agentRow.ID))
		return
	}
	response := h.agentToResponse(refreshed)
	h.hydrateImportedAgent(r.Context(), &response, refreshed.ID)
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

func (h *Handler) resolveAndCompileGitHubAgent(ctx context.Context, workspaceID pgtype.UUID, input GitHubAgentSourceInput) (preparedAgentSource, error) {
	resolved, err := h.resolveGitHubAgentRepository(ctx, workspaceID, input)
	if err != nil {
		return preparedAgentSource{}, err
	}
	sha := strings.TrimSpace(input.ResolvedSHA)
	if sha == "" {
		sha, err = h.GitHubApp.ResolveCommit(ctx, resolved.installation.InstallationID, ownerFromFullName(resolved.repository.FullName), repoFromFullName(resolved.repository.FullName), resolved.ref)
		if err != nil {
			return preparedAgentSource{}, err
		}
	}
	if !immutableGitSHA.MatchString(sha) {
		return preparedAgentSource{}, sourceRequestError(http.StatusBadRequest, "resolved_sha must be an immutable Git commit SHA")
	}
	snapshot, err := agentsource.ReadAgentRepository(ctx, h.GitHubApp, agentsource.Source{
		InstallationID: resolved.installation.InstallationID,
		Owner:          ownerFromFullName(resolved.repository.FullName), Repository: repoFromFullName(resolved.repository.FullName), CommitSHA: sha,
	})
	if err != nil {
		return preparedAgentSource{}, err
	}
	resolved.sha, resolved.bundle, resolved.snapshot = sha, snapshot.Definition, snapshot
	return resolved, nil
}

func (h *Handler) resolveGitHubAgentRepository(ctx context.Context, workspaceID pgtype.UUID, input GitHubAgentSourceInput) (preparedAgentSource, error) {
	if h.GitHubApp == nil {
		return preparedAgentSource{}, githubapp.ErrUnavailable
	}
	repositoryName, requestedRef, err := agentsource.ParseGitHubRepository(input.Repository, input.Ref)
	if err != nil {
		return preparedAgentSource{}, sourceRequestError(http.StatusBadRequest, err.Error())
	}
	installationUUID, err := parseUUIDValue(input.InstallationID)
	if err != nil {
		return preparedAgentSource{}, sourceRequestError(http.StatusBadRequest, "invalid installation_id")
	}
	installation, err := h.Queries.GetGitHubInstallationInWorkspace(ctx, db.GetGitHubInstallationInWorkspaceParams{ID: installationUUID, WorkspaceID: workspaceID})
	if err != nil {
		return preparedAgentSource{}, sourceRequestError(http.StatusNotFound, "GitHub installation not found")
	}
	repositories, err := h.GitHubApp.ListRepositories(ctx, installation.InstallationID)
	if err != nil {
		return preparedAgentSource{}, err
	}
	var repository githubapp.Repository
	for _, candidate := range repositories {
		if strings.EqualFold(candidate.FullName, repositoryName) {
			repository = candidate
			break
		}
	}
	if repository.FullName == "" {
		return preparedAgentSource{}, sourceRequestError(http.StatusForbidden, "repository is not accessible to this GitHub installation")
	}
	ref := requestedRef
	if ref == "" {
		ref = repository.DefaultBranch
	}
	if !agentsource.ValidGitRef(ref) {
		return preparedAgentSource{}, sourceRequestError(http.StatusBadRequest, "invalid Git ref")
	}
	return preparedAgentSource{installation: installation, repository: repository, ref: ref}, nil
}

func createSourceSkillInTx(ctx context.Context, queries *db.Queries, workspaceID, creatorID pgtype.UUID, source preparedAgentSource, agentSourceID pgtype.UUID, compiled agentsource.Skill) (db.Skill, error) {
	configuration := sourceSkillConfig(source, compiled.SourcePath)
	owner, err := queries.GetAgentSourceByID(ctx, agentSourceID)
	if err != nil {
		return db.Skill{}, err
	}
	configuration["exclusive_agent_id"] = uuidToString(owner.AgentID)
	config, err := json.Marshal(configuration)
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

func updateSourceSkillInTx(ctx context.Context, queries *db.Queries, skillID pgtype.UUID, source preparedAgentSource, agentSourceID pgtype.UUID, compiled agentsource.Skill) error {
	existing, err := queries.GetSkill(ctx, skillID)
	if err != nil {
		return err
	}
	configuration := map[string]any{}
	if len(existing.Config) > 0 {
		if err := json.Unmarshal(existing.Config, &configuration); err != nil {
			return err
		}
	}
	if configuration == nil {
		configuration = map[string]any{}
	}
	configuration["origin"] = sourceSkillConfig(source, compiled.SourcePath)["origin"]
	owner, err := queries.GetAgentSourceByID(ctx, agentSourceID)
	if err != nil {
		return err
	}
	configuration["exclusive_agent_id"] = uuidToString(owner.AgentID)
	config, err := json.Marshal(configuration)
	if err != nil {
		return err
	}
	files, err := queries.ListSkillFiles(ctx, skillID)
	if err != nil {
		return err
	}
	desired := compiled
	desired.Name = sourceManagedSkillName(compiled.Name, agentSourceID)
	if packageSkillContentMatches(existing, files, desired) && sourceContractStateValue(existing.Config) == sourceContractStateValue(config) {
		return nil
	}
	if _, err := queries.UpdateSkill(ctx, db.UpdateSkillParams{
		ID: skillID, Name: pgtype.Text{String: sourceManagedSkillName(compiled.Name, agentSourceID), Valid: true},
		Description: pgtype.Text{String: sanitizeNullBytes(compiled.Description), Valid: true},
		Content:     pgtype.Text{String: sanitizeNullBytes(compiled.Content), Valid: true}, Config: config,
	}); err != nil {
		return err
	}
	return reconcilePackageSkillFiles(ctx, queries, skillID, files, compiled.Files)
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

func sourceSkillConfig(source preparedAgentSource, sourcePath string) map[string]any {
	if !source.installation.ID.Valid {
		return map[string]any{"origin": map[string]any{"type": "agent_package", "package_hash": source.bundle.Hash, "path": sourcePath}}
	}
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
	if source.SourceType == "github" && !source.ManagedSourceKey.Valid && !connected {
		status = "disconnected"
	}
	var installationID *string
	if connected {
		value := uuidToString(source.GithubInstallationID)
		installationID = &value
	}
	repository, repositoryURL := "", ""
	if source.SourceType == "github" {
		repository = source.RepoOwner + "/" + source.RepoName
		repositoryURL = "https://github.com/" + repository
	}
	return AgentSourceResponse{
		RepositoryURL:      repositoryURL,
		CanSync:            connected && !source.ManagedSourceKey.Valid,
		ConfigurationScope: []string{"name", "description", "instructions", "configuration", "access", "okrs", "a2a", "disabled_runtime_skills", "skills", "skill_files"},
		AgentID:            uuidToString(source.AgentID), SourceType: source.SourceType,
		InstallationID: installationID, Repository: repository,
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
	if writeManifestSchemaError(w, err) {
		return
	}
	var apiErr *githubapp.APIError
	var requestErr *gitSourceRequestError
	switch {
	case errors.As(err, &requestErr):
		writeError(w, requestErr.status, requestErr.message)
	case errors.Is(err, githubapp.ErrUnavailable):
		writeError(w, http.StatusServiceUnavailable, "GitHub agent sources are unavailable")
	case errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound:
		writeError(w, http.StatusNotFound, "GitHub repository content not found")
	case errors.As(err, &apiErr) && (apiErr.StatusCode == http.StatusUnauthorized || apiErr.StatusCode == http.StatusForbidden):
		writeError(w, http.StatusForbidden, "GitHub installation cannot access this repository")
	case errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusTooManyRequests:
		writeError(w, http.StatusTooManyRequests, "GitHub rate limit exceeded")
	case strings.Contains(err.Error(), "coordinator_contract") ||
		strings.Contains(err.Error(), "manifest") ||
		strings.Contains(err.Error(), "dingtalk-agent.json") ||
		strings.Contains(err.Error(), "project protocol") ||
		strings.Contains(err.Error(), "skill") ||
		strings.Contains(err.Error(), "required file") ||
		strings.Contains(err.Error(), "bundle"):
		writeError(w, http.StatusUnprocessableEntity, err.Error())
	default:
		writeError(w, http.StatusBadGateway, err.Error())
	}
}

func writeAgentSourceDatabaseError(w http.ResponseWriter, err error) {
	if writeManifestSchemaError(w, err) {
		return
	}
	var requestErr *gitSourceRequestError
	if errors.As(err, &requestErr) {
		writeError(w, requestErr.status, requestErr.message)
		return
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		writeError(w, http.StatusConflict, "an agent or skill with this name already exists in the workspace")
		return
	}
	writeError(w, http.StatusInternalServerError, "failed to save Agent package")
}
