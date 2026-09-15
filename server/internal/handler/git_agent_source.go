package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/agentsource"
	"github.com/multica-ai/multica/server/internal/coordinatorcontract"
	"github.com/multica-ai/multica/server/internal/gitrepo"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

var immutableGitSHA = regexp.MustCompile(`^[0-9a-fA-F]{40}([0-9a-fA-F]{24})?$`)

type GitAgentSourceInput struct {
	ConnectionID string `json:"connection_id"`
	Repository     string `json:"repository"`
	Ref            string `json:"ref"`
}

type GitAgentPreviewRequest struct {
	ConnectionID string `json:"connection_id"`
	Repository     string `json:"repository"`
	Ref            string `json:"ref"`
}

type GitAgentPreviewResponse struct {
	CoordinatorContract *coordinatorcontract.Contract `json:"coordinator_contract"`
	Definition          map[string]any                `json:"definition"`
	Requirements        PackageRequirements           `json:"requirements"`
	PreviewID           string                        `json:"preview_id"`
	ExpiresAt           string                        `json:"expires_at"`
	RepositoryURL       string                        `json:"repository_url"`
	ConnectionID      string                        `json:"connection_id"`
	Repository          string                        `json:"repository"`
	Ref                 string                        `json:"ref"`
	ResolvedSHA         string                        `json:"resolved_sha"`
	Name                string                        `json:"name"`
	Description         string                        `json:"description"`
	Instructions        string                        `json:"instructions"`
	Skills              []GitAgentSkillPreview     `json:"skills"`
	CompatibleProviders []string                      `json:"compatible_providers"`
	Warnings            []string                      `json:"warnings"`
	Blockers            []string                      `json:"blockers"`
}

type GitAgentSkillPreview struct {
	Enabled     bool   `json:"enabled"`
	SourcePath  string `json:"source_path"`
	Name        string `json:"name"`
	Description string `json:"description"`
	FileCount   int    `json:"file_count"`
}

type AgentSourceResponse struct {
	RepositoryURL      string   `json:"repository_url"`
	CanSync            bool     `json:"can_sync"`
	ConfigurationScope []string `json:"configuration_scope"`
	AgentID            string   `json:"agent_id"`
	SourceType         string   `json:"source_type"`
	ConnectionID     *string  `json:"connection_id"`
	Repository         string   `json:"repository"`
	Ref                string   `json:"ref"`
	ManifestPath       string   `json:"manifest_path"`
	SyncedCommitSHA    string   `json:"synced_commit_sha"`
	SyncStatus         string   `json:"sync_status"`
	LastSyncError      *string  `json:"last_sync_error"`
	LastSyncAttemptAt  *string  `json:"last_sync_attempt_at"`
	LastSyncedAt       string   `json:"last_synced_at"`
	Connected    bool     `json:"connected"`
}

type AgentSourceSyncResponse struct {
	Source   AgentSourceResponse `json:"source"`
	Changed  bool                `json:"changed"`
	Warnings []string            `json:"warnings"`
}

type preparedAgentSource struct {
	connection gitrepo.Connection
	remote gitrepo.Remote
	repository   gitrepo.Repository
	ref          string
	sha          string
	bundle       agentsource.Bundle
	snapshot     agentsource.RepositorySnapshot
	rollbackOf   string
	publicationDefinition *agentsource.Bundle
}

func (h *Handler) PreviewGitAgent(w http.ResponseWriter, r *http.Request) {
	workspaceID := workspaceIDFromURL(r, "id")
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	var request GitAgentPreviewRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	resolved, err := h.resolveAndCompileGitAgent(r.Context(), wsUUID, GitAgentSourceInput{
		ConnectionID: request.ConnectionID,
		Repository:     request.Repository,
		Ref:            request.Ref,
	})
	if err != nil {
		writeGitRepoError(w, err)
		return
	}
	blockers, err := h.gitAgentPreviewBlockers(r.Context(), wsUUID, resolved.bundle)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to check Git Agent conflicts")
		return
	}
	preview, err := h.saveAgentSourcePreview(r, wsUUID, db.Agent{}, db.AgentSource{}, resolved, "")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save Git Agent preview")
		return
	}
	writeJSON(w, http.StatusOK, GitAgentPreviewResponse{
		Definition: packageDefinitionPreview(resolved.bundle), Requirements: packageRequirements(resolved.bundle),
		PreviewID:           uuidToString(preview.ID),
		ExpiresAt:           timestampToString(preview.ExpiresAt),
		RepositoryURL:       resolved.repository.HTMLURL,
		ConnectionID:      uuidToString(resolved.connection.ID),
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

func (h *Handler) gitAgentPreviewBlockers(_ context.Context, _ pgtype.UUID, _ agentsource.Bundle) ([]string, error) {
	// Source-managed skills are materialized under a source-scoped storage name,
	// so two Agents may safely use the same repository skill without sharing a
	// mutable skill row. Runtime snapshots remain isolated per Agent Source.
	return []string{}, nil
}

func sourceSkillPreviews(skills []agentsource.Skill) []GitAgentSkillPreview {
	previews := make([]GitAgentSkillPreview, 0, len(skills))
	for _, compiled := range skills {
		previews = append(previews, GitAgentSkillPreview{
			SourcePath: compiled.SourcePath, Name: compiled.Name,
			Description: compiled.Description, FileCount: len(compiled.Files), Enabled: !compiled.Disabled,
		})
	}
	return previews
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
		slog.Warn("load agent after Git source sync", "error", err, "agent_id", uuidToString(agentRow.ID))
		return
	}
	response := h.agentToResponse(refreshed)
	h.hydrateImportedAgent(r.Context(), &response, refreshed.ID)
	if err := h.attachAgentSkills(r.Context(), &response, refreshed.ID); err != nil {
		slog.Warn("load agent skills after Git source sync", "error", err, "agent_id", uuidToString(agentRow.ID))
		return
	}
	if err := h.enrichAgentResponseWithTargets(r.Context(), &response, refreshed.ID); err != nil {
		slog.Warn("load agent access after Git source sync", "error", err, "agent_id", uuidToString(agentRow.ID))
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

func (h *Handler) resolveAndCompileGitAgent(ctx context.Context, workspaceID pgtype.UUID, input GitAgentSourceInput) (preparedAgentSource, error) {
	resolved, err := h.resolveGitAgentRepository(ctx, workspaceID, input)
	if err != nil {
		return preparedAgentSource{}, err
	}
	sha, err := resolved.remote.ResolveCommit(ctx,resolved.ref)
	if err != nil { return preparedAgentSource{}, err }
	if !immutableGitSHA.MatchString(sha) {
		return preparedAgentSource{}, sourceRequestError(http.StatusBadRequest, "resolved_sha must be an immutable Git commit SHA")
	}
	snapshot, err := agentsource.ReadAgentRepository(ctx, resolved.remote, agentsource.Source{CommitSHA: sha})
	if err != nil {
		return preparedAgentSource{}, err
	}
	resolved.sha, resolved.bundle, resolved.snapshot = sha, snapshot.Definition, snapshot
	return resolved, nil
}

func (h *Handler) gitRepositories() gitrepo.Service {
	return gitrepo.Service{Queries:h.Queries, GitHub:h.GitHubApp, Code:h.GitRepoCodeConfig, Secrets:h.GitRepoSecrets}
}

func (h *Handler) resolveGitAgentRepository(ctx context.Context, workspaceID pgtype.UUID, input GitAgentSourceInput) (preparedAgentSource, error) {
	access, err := h.gitRepositories().Open(ctx,workspaceID,input.Repository,input.ConnectionID)
	if err != nil { return preparedAgentSource{}, err }
	if access.Address.LinkKind != "" { return preparedAgentSource{}, sourceRequestError(http.StatusBadRequest,"use the repository root URL and select a branch, tag or commit separately") }
	ref := strings.TrimSpace(input.Ref)
	if ref == "" { ref = "refs/heads/" + access.Remote.Info().DefaultBranch }
	if !gitrepo.ValidRef(ref) { return preparedAgentSource{}, sourceRequestError(http.StatusBadRequest,"invalid Git ref") }
	return preparedAgentSource{connection:access.Connection, remote:access.Remote, repository:access.Remote.Info(), ref:ref}, nil
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
	files, err := queries.ListSkillFiles(ctx,skillID)
	if err != nil { return err }
	desired := compiled
	desired.Name = sourceManagedSkillName(compiled.Name,agentSourceID)
	if packageSkillContentMatches(existing,files,desired) && sourceContractStateValue(existing.Config) == sourceContractStateValue(config) { return nil }
	if _, err := queries.UpdateSkill(ctx, db.UpdateSkillParams{
		ID: skillID, Name: pgtype.Text{String: sourceManagedSkillName(compiled.Name, agentSourceID), Valid: true},
		Description: pgtype.Text{String: sanitizeNullBytes(compiled.Description), Valid: true},
		Content:     pgtype.Text{String: sanitizeNullBytes(compiled.Content), Valid: true}, Config: config,
	}); err != nil {
		return err
	}
	return reconcilePackageSkillFiles(ctx,queries,skillID,files,compiled.Files)
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
	if source.repository.HTMLURL == "" {
		return map[string]any{"origin": map[string]any{"type": "agent_package", "package_hash": source.bundle.Hash, "path": sourcePath}}
	}
	return map[string]any{"origin": map[string]any{
		"type": "git_agent_source", "repository": source.repository.HTMLURL,
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
		slog.Warn("record Git Agent source failure", "error", err, "agent_source_id", uuidToString(sourceID))
	}
}

func agentSourceToResponse(source db.AgentSource) AgentSourceResponse {
	status := source.SyncStatus
	connected := source.SourceType == "git" && !source.ManagedSourceKey.Valid && source.RepositoryUrl != "" && source.SyncStatus != "disconnected"
	if source.SourceType == "git" && !source.ManagedSourceKey.Valid && !connected {
		status = "disconnected"
	}
	var installationID *string
	if source.GitConnectionID.Valid {
		value := uuidToString(source.GitConnectionID)
		installationID = &value
	}
	repository, repositoryURL := "", ""
	if source.SourceType == "git" {
		repository = source.RepoOwner + "/" + source.RepoName
		repositoryURL = source.RepositoryUrl
	}
	return AgentSourceResponse{
		RepositoryURL:      repositoryURL,
		CanSync:            connected && !source.ManagedSourceKey.Valid,
		ConfigurationScope: []string{"name", "description", "instructions", "configuration", "access", "okrs", "a2a", "disabled_runtime_skills", "skills", "skill_files"},
		AgentID:            uuidToString(source.AgentID), SourceType: source.SourceType,
		ConnectionID: installationID, Repository: repository,
		Ref: source.Ref, ManifestPath: source.ManifestPath, SyncedCommitSHA: source.SyncedCommitSha,
		SyncStatus: status, LastSyncError: textToPtr(source.LastSyncError),
		LastSyncAttemptAt: timestampToPtr(source.LastSyncAttemptAt), LastSyncedAt: timestampToString(source.LastSyncedAt),
		Connected: connected,
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

func writeGitRepoError(w http.ResponseWriter, err error) {
	if writeManifestSchemaError(w, err) {
		return
	}
	var apiErr *gitrepo.APIError
	var requestErr *gitSourceRequestError
	var accessErr *gitrepo.AccessError
	switch {
	case errors.As(err, &accessErr):
		writeJSON(w,accessErr.Status,map[string]any{"error":accessErr.Message,"code":accessErr.Code})
	case errors.As(err, &requestErr):
		writeError(w, requestErr.status, requestErr.message)
	case errors.Is(err, gitrepo.ErrGitHubUnavailable):
		writeError(w, http.StatusServiceUnavailable, "Git Agent sources are unavailable")
	case errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound:
		writeError(w, http.StatusNotFound, apiErr.Error())
	case errors.As(err, &apiErr) && (apiErr.StatusCode == http.StatusUnauthorized || apiErr.StatusCode == http.StatusForbidden):
		writeError(w, http.StatusForbidden, apiErr.Error())
	case errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusTooManyRequests:
		writeError(w, http.StatusTooManyRequests, "Git provider rate limit exceeded")
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
	if writeManifestSchemaError(w, err) { return }
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
