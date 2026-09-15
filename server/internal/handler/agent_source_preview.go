package handler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"mime"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/agentsource"
	"github.com/multica-ai/multica/server/internal/gitrepo"
	"github.com/multica-ai/multica/server/internal/coordinatorcontract"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type gitSourceRequestError struct {
	status  int
	message string
}

func (e *gitSourceRequestError) Error() string { return e.message }

func sourceRequestError(status int, message string) error {
	return &gitSourceRequestError{status: status, message: message}
}

type AgentSourceSyncPreviewResponse struct {
	RollbackOf           string                   `json:"rollback_of,omitempty"`
	Requirements         PackageRequirements      `json:"requirements"`
	PreviewID            string                   `json:"preview_id"`
	ExpiresAt            string                   `json:"expires_at"`
	RepositoryURL        string                   `json:"repository_url"`
	Ref                  string                   `json:"ref"`
	BaseSHA              string                   `json:"base_sha"`
	ResolvedSHA          string                   `json:"resolved_sha"`
	GitChanges           []agentsource.FileChange `json:"git_changes"`
	ConfigurationChanges []agentsource.FileChange `json:"configuration_changes"`
	Warnings             []string                 `json:"warnings"`
	Changed              bool                     `json:"changed"`
}

func (h *Handler) ListGitAgentBranches(w http.ResponseWriter, r *http.Request) {
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceIDFromURL(r, "id"), "workspace id")
	if !ok {
		return
	}
	h.listGitAgentBranches(w, r, wsUUID, GitAgentSourceInput{
		ConnectionID: r.URL.Query().Get("connection_id"), Repository: r.URL.Query().Get("repository"),
	})
}

func (h *Handler) ListAgentSourceBranches(w http.ResponseWriter, r *http.Request) {
	agent, source, ok := h.loadGitSourceForManage(w, r)
	if !ok {
		return
	}
	h.listGitAgentBranches(w, r, agent.WorkspaceID, GitAgentSourceInput{
		ConnectionID: uuidToString(source.GitConnectionID), Repository: source.RepositoryUrl,
	})
}

func (h *Handler) listGitAgentBranches(w http.ResponseWriter, r *http.Request, workspaceID pgtype.UUID, input GitAgentSourceInput) {
	resolved, err := h.resolveGitAgentRepository(r.Context(), workspaceID, input)
	if err != nil {
		writeGitRepoError(w, err)
		return
	}
	branches, err := resolved.remote.ListBranches(r.Context())
	if err != nil {
		writeGitRepoError(w, err)
		return
	}
	tags, err := resolved.remote.ListTags(r.Context())
	if err != nil { writeGitRepoError(w, err); return }
	writeJSON(w, http.StatusOK, map[string]any{
		"repository": resolved.repository.FullName, "repository_url": resolved.repository.HTMLURL,
		"connection_id": uuidToString(resolved.connection.ID), "default_branch": resolved.repository.DefaultBranch, "branches": branches, "tags": tags,
	})
}

func (h *Handler) loadPackageSourceForManage(w http.ResponseWriter, r *http.Request) (db.Agent, db.AgentSource, bool) {
	agent, ok := h.loadAgentForUser(w, r, chi.URLParam(r, "id"))
	if !ok || !h.canManageAgent(w, r, agent) { return db.Agent{}, db.AgentSource{}, false }
	if agent.Kind != "user" || agent.ArchivedAt.Valid { writeError(w, http.StatusConflict, "only active user Agents accept package publication"); return db.Agent{}, db.AgentSource{}, false }
	source, err := h.Queries.GetAgentSourceByAgentID(r.Context(), agent.ID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) { writeError(w, http.StatusInternalServerError, "failed to read Agent source"); return db.Agent{}, db.AgentSource{}, false }
	if source.ManagedSourceKey.Valid { writeError(w, http.StatusConflict, "this Agent source is updated automatically by Multica"); return db.Agent{}, db.AgentSource{}, false }
	return agent, source, true
}

func (h *Handler) loadGitSourceForManage(w http.ResponseWriter, r *http.Request) (db.Agent, db.AgentSource, bool) {
	agent, source, ok := h.loadPackageSourceForManage(w, r)
	if !ok { return agent, source, false }
	if !source.ID.Valid { writeError(w, http.StatusNotFound, "agent source not found"); return agent, source, false }
	if source.SourceType != "git" || source.SyncStatus == "disconnected" { writeError(w, http.StatusConflict, "Git connection is disconnected"); return agent, source, false }
	return agent, source, true
}

func (h *Handler) saveAgentSourcePreview(r *http.Request, workspaceID pgtype.UUID, agent db.Agent, source db.AgentSource, resolved preparedAgentSource, stateHash string) (db.AgentSourcePreview, error) {
	userID, err := parseUUIDValue(requestUserID(r))
	if err != nil {
		return db.AgentSourcePreview{}, err
	}
	snapshot, err := marshalAgentPublicationSnapshot(agentPublicationSnapshot{RepositorySnapshot:resolved.snapshot, RollbackOf:resolved.rollbackOf, PublishedDefinition:resolved.publicationDefinition})
	if err != nil {
		return db.AgentSourcePreview{}, err
	}
	if err := h.Queries.DeleteExpiredAgentSourcePreviews(r.Context(), db.DeleteExpiredAgentSourcePreviewsParams{WorkspaceID: workspaceID, CreatedBy: userID}); err != nil {
		return db.AgentSourcePreview{}, err
	}
	return h.Queries.CreateAgentSourcePreview(r.Context(), db.CreateAgentSourcePreviewParams{
		WorkspaceID: workspaceID, CreatedBy: userID, AgentID: agent.ID, AgentSourceID: source.ID,
		GitConnectionID: resolved.connection.ID, Repository: resolved.repository.HTMLURL, Ref: resolved.ref,
		ResolvedSha: resolved.sha, ExpectedSourceSha: source.SyncedCommitSha, ExpectedStateHash: stateHash, Snapshot: snapshot,
	})
}

func (h *Handler) readAgentSourcePreview(r *http.Request, workspaceID pgtype.UUID, id string) (db.AgentSourcePreview, error) {
	previewID, err := parseUUIDValue(id)
	if err != nil {
		return db.AgentSourcePreview{}, sourceRequestError(http.StatusBadRequest, "invalid preview_id")
	}
	userID, err := parseUUIDValue(requestUserID(r))
	if err != nil {
		return db.AgentSourcePreview{}, sourceRequestError(http.StatusUnauthorized, "user identity is required")
	}
	preview, err := h.Queries.GetAgentSourcePreview(r.Context(), db.GetAgentSourcePreviewParams{ID: previewID, WorkspaceID: workspaceID, CreatedBy: userID})
	if errors.Is(err, pgx.ErrNoRows) {
		return preview, sourceRequestError(http.StatusNotFound, "agent source preview not found")
	}
	if err != nil {
		return preview, err
	}
	if !preview.AppliedAt.Valid && !preview.ExpiresAt.Time.After(time.Now()) {
		return preview, sourceRequestError(http.StatusConflict, "agent source preview expired; preview again")
	}
	return preview, nil
}

func (h *Handler) resolveAgentSourcePreview(ctx context.Context, preview db.AgentSourcePreview) (preparedAgentSource, error) {
	var saved agentPublicationSnapshot
	if err := json.Unmarshal(preview.Snapshot, &saved); err != nil { return preparedAgentSource{}, err }
	bundle := saved.Definition
	if saved.RollbackOf != "" && saved.PublishedDefinition != nil { bundle = *saved.PublishedDefinition }
	if err := agentsource.ValidateBundle(bundle); err != nil { return preparedAgentSource{}, err }
	// Local previews have no external permission to recheck.
	if preview.Repository == "" {
		return preparedAgentSource{snapshot:saved.RepositorySnapshot, bundle:bundle, sha:preview.ResolvedSha, rollbackOf:saved.RollbackOf, publicationDefinition:saved.PublishedDefinition}, nil
	}
	// Recheck current Git permission, but never resolve the branch a second time.
	resolved, err := h.resolveGitAgentRepository(ctx, preview.WorkspaceID, GitAgentSourceInput{
		ConnectionID: uuidToString(preview.GitConnectionID), Repository: preview.Repository, Ref: preview.Ref,
	})
	if err != nil {
		return preparedAgentSource{}, err
	}
	resolved.snapshot, resolved.sha, resolved.bundle = saved.RepositorySnapshot, preview.ResolvedSha, bundle
	resolved.rollbackOf, resolved.publicationDefinition = saved.RollbackOf, saved.PublishedDefinition
	return resolved, nil
}

func lockSourcePreview(ctx context.Context, queries *db.Queries, preview db.AgentSourcePreview) (db.AgentSourcePreview, error) {
	locked, err := queries.LockAgentSourcePreview(ctx, db.LockAgentSourcePreviewParams{ID: preview.ID, WorkspaceID: preview.WorkspaceID, CreatedBy: preview.CreatedBy})
	if errors.Is(err, pgx.ErrNoRows) {
		return locked, sourceRequestError(http.StatusConflict, "agent source preview is no longer available; preview again")
	}
	if err != nil {
		return locked, err
	}
	if !locked.AppliedAt.Valid && !locked.ExpiresAt.Time.After(time.Now()) {
		return locked, sourceRequestError(http.StatusConflict, "agent source preview expired; preview again")
	}
	return locked, nil
}

func markSourcePreviewApplied(ctx context.Context, queries *db.Queries, preview db.AgentSourcePreview, source db.AgentSource, changed bool) error {
	snapshot, err := captureAgentPublication(ctx, queries, preview, source)
	if err != nil { return err }
	response, err := json.Marshal(agentSourceToResponse(source))
	if err != nil {
		return err
	}
	_, err = queries.MarkAgentSourcePreviewApplied(ctx, db.MarkAgentSourcePreviewAppliedParams{
		ID: preview.ID, AgentID: source.AgentID, AppliedSource: response, AppliedChanged: changed, Snapshot:snapshot,
	})
	return err
}

func (h *Handler) writeCreatedSourceReplay(w http.ResponseWriter, r *http.Request, preview db.AgentSourcePreview) {
	agent, err := h.Queries.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{ID: preview.AgentID, WorkspaceID: preview.WorkspaceID})
	if err != nil {
		writeError(w, http.StatusConflict, "the imported Agent no longer exists")
		return
	}
	if !h.canManageAgent(w, r, agent) {
		return
	}
	response := h.agentToResponse(agent)
	h.hydrateImportedAgent(r.Context(), &response, agent.ID)
	if err := h.attachAgentSkills(r.Context(), &response, agent.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load imported skills")
		return
	}
	if err := h.enrichAgentResponseWithTargets(r.Context(), &response, agent.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load imported Agent access")
		return
	}
	actorType, _ := h.resolveActor(r, requestUserID(r), uuidToString(agent.WorkspaceID))
	redactAgentResponseForActor(&response, actorType)
	writeJSON(w, http.StatusOK, map[string]any{"agent": response, "source": json.RawMessage(preview.AppliedSource), "warnings": []string{}})
}

// Normalize JSONB and compiled JSON to the same representation for both preview
// diffs and optimistic state hashes. Omit null entries to preserve existing
// no-contract preview hashes; removing a present entry remains a deletion.
func sourceContractStateValue(raw []byte) string {
	if len(raw) == 0 {
		return "null"
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return string(raw)
	}
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func sourceDefinitionFiles(bundle agentsource.Bundle) map[string]string {
	files := map[string]string{"instructions": bundle.Instructions}
	if contract := sourceContractStateValue(coordinatorcontract.Marshal(bundle.CoordinatorContract)); contract != "null" {
		files["coordinator_contract"] = contract
	}
	appendPackageStateFiles(files, packageDefinitionPreview(bundle))
	for _, skill := range bundle.Skills {
		prefix := "skills/" + skill.SourcePath + "/"
		files[prefix+"name"] = skill.Name
		files[prefix+"description"] = skill.Description
		files[prefix+"enabled"] = strconv.FormatBool(!skill.Disabled)
		files[prefix+"SKILL.md"] = skill.Content
		for _, file := range skill.Files {
			files[prefix+"files/"+file.Path] = file.Content
		}
	}
	return files
}

// sourceStateFiles must run in a transaction. All writers to supporting files
// lock their parent skill, allowing preview/confirmation to read one state.
func sourceStateFiles(ctx context.Context, queries *db.Queries, agent db.Agent, source db.AgentSource, desired agentsource.Bundle) (map[string]string, string, error) {
	targets, err := resolvePackageSkillTargets(ctx,queries,agent,source,desired,false)
	if err != nil { return nil,"",err }
	skills, err := queries.LockSourceSkills(ctx, source.ID)
	if err != nil {
		return nil, "", err
	}
	assignments, err := queries.LockSourceSkillAssignments(ctx, source.ID)
	if err != nil {
		return nil, "", err
	}
	mappings, err := queries.ListAgentSourceSkills(ctx, source.ID)
	if err != nil {
		return nil, "", err
	}
	paths := map[pgtype.UUID]string{}
	managedIDs := map[pgtype.UUID]bool{}
	for _, mapping := range mappings {
		paths[mapping.SkillID] = mapping.SourcePath
		managedIDs[mapping.SkillID] = true
	}
	enabled := map[pgtype.UUID]bool{}
	for _, assignment := range assignments {
		if assignment.AgentID == agent.ID {
			enabled[assignment.SkillID] = assignment.Enabled
		}
	}
	files := map[string]string{"instructions": agent.Instructions}
	for _, target := range targets {
		if !target.Skill.ID.Valid { continue }
		paths[target.Skill.ID] = target.Definition.SourcePath
		enabled[target.Skill.ID] = target.Enabled
		if !target.Managed { skills = append(skills,target.Skill) }
	}
	if contract := sourceContractStateValue(agent.CoordinatorContract); contract != "null" {
		files["coordinator_contract"] = contract
	}
	for _, skill := range skills {
		prefix := "skills/" + paths[skill.ID] + "/"
		name := skill.Name
		if managedIDs[skill.ID] { name = strings.TrimSuffix(name, sourceManagedSkillName("", source.ID)) }
		files[prefix+"name"] = name
		files[prefix+"description"] = skill.Description
		files[prefix+"SKILL.md"] = skill.Content
		if value, assigned := enabled[skill.ID]; assigned {
			files[prefix+"enabled"] = strconv.FormatBool(value)
		}
		supporting, err := queries.ListSkillFiles(ctx, skill.ID)
		if err != nil {
			return nil, "", err
		}
		for _, file := range supporting {
			files[prefix+"files/"+file.Path] = file.Content
		}
	}
	manifest, _, err := buildAgentExportManifest(ctx, queries, agent, source.ManifestPath)
	if err != nil {
		return nil, "", err
	}
	if a2a, ok := manifest["a2a"].(map[string]any); ok {
		mappings, err := readPackageClientMappings(ctx, queries, agent.ID)
		if err != nil {
			return nil, "", err
		}
		if clients, ok := a2a["clients"].([]any); ok {
			managed := []map[string]any{}
			for _, rawClient := range clients {
				client, _ := rawClient.(map[string]any)
				if key, ok := client["key"].(string); ok && mappings[key] != "" {
					managed = append(managed, client)
				}
			}
			a2a["clients"] = managed
		}
	}
	appendPackageStateFiles(files, manifest)
	bindingState, err := readPackageBindingState(ctx,queries,agent)
	if err != nil { return nil,"",err }
	state := struct {
		ReferencedSkills []packageSkillTarget
		PackageBindings packageBindingState
		PrivateConfig  [][]byte
		Files          map[string]string
		SourceID       pgtype.UUID
		ConnectionID pgtype.UUID
		Repository     string
		Ref            string
		SHA            string
		RuntimeID      pgtype.UUID
		OwnerID        pgtype.UUID
		Mappings       []db.AgentSourceSkill
	}{targets, bindingState, [][]byte{agent.CustomEnv, agent.CustomArgs, agent.RuntimeConfig, agent.McpConfig}, files, source.ID, source.GitConnectionID, source.RepositoryUrl, source.Ref, source.SyncedCommitSha, agent.RuntimeID, agent.OwnerID, mappings}
	encoded, err := json.Marshal(state)
	if err != nil {
		return nil, "", err
	}
	digest := sha256.Sum256(encoded)
	return files, hex.EncodeToString(digest[:]), nil
}

func (h *Handler) PreviewAgentSourceSync(w http.ResponseWriter, r *http.Request) {
	mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mediaType == "application/zip" || mediaType == "multipart/form-data" { h.previewAgentPackagePublication(w, r); return }
	agent, source, ok := h.loadGitSourceForManage(w, r)
	if !ok {
		return
	}
	var request struct {
		Ref string `json:"ref"`
		PublicationID string `json:"publication_id"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1 << 20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if request.PublicationID != "" {
		if request.Ref != "" { writeError(w,http.StatusBadRequest,"select either ref or publication_id"); return }
		h.previewAgentPublicationRollback(w,r,agent,source,request.PublicationID)
		return
	}
	if request.Ref == "" {
		request.Ref = source.Ref
	}
	resolved, err := h.resolveAndCompileGitAgent(r.Context(), agent.WorkspaceID, GitAgentSourceInput{
		ConnectionID: uuidToString(source.GitConnectionID), Repository: source.RepositoryUrl, Ref: request.Ref,
	})
	if err != nil {
		writeGitRepoError(w, err)
		return
	}
	base, err := h.publishedSourceSnapshot(r.Context(), agent, source, resolved.remote)
	if err != nil {
		writeGitRepoError(w, err)
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read source state")
		return
	}
	defer tx.Rollback(r.Context())
	queries := h.Queries.WithTx(tx)
	lockedAgent, err := queries.GetAgentForUpdate(r.Context(), agent.ID)
	if err != nil {
		writeError(w, http.StatusConflict, "Agent changed while preparing preview")
		return
	}
	lockedSource, err := queries.LockAgentSourceByAgentID(r.Context(), agent.ID)
	if err != nil || lockedSource.SyncedCommitSha != source.SyncedCommitSha || lockedSource.Ref != source.Ref || lockedSource.GitConnectionID != source.GitConnectionID {
		writeError(w, http.StatusConflict, "Agent source changed while preparing preview")
		return
	}
	current, stateHash, err := sourceStateFiles(r.Context(), queries, lockedAgent, lockedSource, resolved.bundle)
	if err != nil {
		writeAgentSourceDatabaseError(w,err)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read source state")
		return
	}
	preview, err := h.saveAgentSourcePreview(r, agent.WorkspaceID, agent, source, resolved, stateHash)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save source preview")
		return
	}
	changes := diffPackageState(current, sourceDefinitionFiles(resolved.bundle))
	requirements, err := h.packageRequirementsForAgent(r.Context(),h.Queries,agent,requestUserID(r),resolved.bundle)
	if err != nil { writeAgentSourceDatabaseError(w,err); return }
	writeJSON(w, http.StatusOK, AgentSourceSyncPreviewResponse{
		Requirements: requirements, PreviewID: uuidToString(preview.ID), ExpiresAt: timestampToString(preview.ExpiresAt), RepositoryURL: resolved.repository.HTMLURL,
		Ref: resolved.ref, BaseSHA: source.SyncedCommitSha, ResolvedSHA: resolved.sha,
		GitChanges: agentsource.DiffRepository(packageDiffSnapshot(base), packageDiffSnapshot(resolved.snapshot)), ConfigurationChanges: changes, Warnings: resolved.bundle.Warnings,
		Changed: len(changes) > 0 || source.Ref != resolved.ref || source.SyncedCommitSha != resolved.sha,
	})
}

func (h *Handler) publishedSourceSnapshot(ctx context.Context, agent db.Agent, source db.AgentSource, remote gitrepo.Remote) (agentsource.RepositorySnapshot, error) {
	previous, err := h.Queries.LatestAppliedAgentSourcePreview(ctx, db.LatestAppliedAgentSourcePreviewParams{AgentID: agent.ID, WorkspaceID: agent.WorkspaceID})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return agentsource.RepositorySnapshot{}, err
	}
	if err == nil && previous.ResolvedSha == source.SyncedCommitSha {
		var snapshot agentsource.RepositorySnapshot
		if err := json.Unmarshal(previous.Snapshot, &snapshot); err != nil {
			return snapshot, err
		}
		return snapshot, agentsource.ValidateBundle(snapshot.Definition)
	}
	// Sources created before previews existed have no stored Git baseline yet.
	return agentsource.ReadAgentRepository(ctx, remote, agentsource.Source{CommitSHA:source.SyncedCommitSha})
}

func appendPackageStateFiles(files map[string]string, manifest map[string]any) {
	if a2a, ok := manifest["a2a"].(map[string]any); ok {
		for key, value := range a2a {
			encoded, _ := json.Marshal(value)
			files["configuration/a2a/"+key] = string(encoded)
		}
	}
	if configuration, ok := manifest["configuration"].(map[string]any); ok {
		for key, value := range configuration {
			encoded, _ := json.Marshal(value)
			files["configuration/"+key] = string(encoded)
		}
	}
	for _, key := range []string{"name", "description", "okrs", "access", "disabled_runtime_skills"} {
		if value, exists := manifest[key]; exists {
			encoded, _ := json.Marshal(value)
			files["configuration/"+key] = string(encoded)
		}
	}
}

func diffPackageState(current, desired map[string]string) []agentsource.FileChange {
	managed := map[string]string{}
	for key, value := range current {
		if _, present := desired[key]; present || !strings.HasPrefix(key, "configuration/") {
			managed[key] = value
		}
	}
	return agentsource.DiffFiles(managed, desired)
}

// Git object hashes still reveal that a private file changed, but diff text
// follows the same secret-redaction policy as the configuration preview.
func packageDiffSnapshot(snapshot agentsource.RepositorySnapshot) agentsource.RepositorySnapshot {
	if snapshot.Definition.Definition == nil {
		return snapshot
	}
	files := map[string]string{}
	for path, content := range snapshot.Files {
		files[path] = content
	}
	if manifest, exists := files[agentsource.PortableManifestPath]; exists {
		// Rollback Definition includes materialized platform values. The Git
		// pane must still show the original repository manifest, with redaction.
		var original map[string]json.RawMessage
		if err := json.Unmarshal([]byte(manifest), &original); err != nil {
			delete(files, agentsource.PortableManifestPath)
		} else {
			encoded, _ := json.MarshalIndent(packageDefinitionPreview(agentsource.Bundle{Definition:original}), "", "  ")
			files[agentsource.PortableManifestPath] = string(encoded)
		}
	}
	snapshot.Files = files
	return snapshot
}
