package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/agentsource"
	"github.com/multica-ai/multica/server/internal/coordinatorcontract"
	agentpkg "github.com/multica-ai/multica/server/pkg/agent"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type CreateAgentPackageRequest struct {
	Secrets map[string]string `json:"secrets"`
	DeferredBindings []string `json:"deferred_bindings"`
	CreateAgentRequest
	PreviewID string `json:"preview_id"`
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
	var request CreateAgentPackageRequest
	rawFields, err := decodeJSONBodyWithRawFields(r.Body, &request)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	var preview db.AgentSourcePreview
	var resolved preparedAgentSource
	if request.PreviewID != "" {
		preview, err = h.readAgentSourcePreview(r, wsUUID, request.PreviewID)
		if err != nil { writeGitRepoError(w, err); return }
		if preview.AgentSourceID.Valid || preview.ExpectedStateHash != "" {
			writeError(w, http.StatusBadRequest, "a sync preview cannot create an Agent")
			return
		}
		if rawFields["connection_id"] != nil || rawFields["repository"] != nil || rawFields["ref"] != nil || rawFields["resolved_sha"] != nil {
			writeError(w, http.StatusBadRequest, "preview_id already fixes the repository, connection and commit; omit source overrides")
			return
		}
		resolved, err = h.resolveAgentSourcePreview(r.Context(), preview)
		if err == nil && preview.AppliedAt.Valid {
			h.writeCreatedSourceReplay(w, r, preview)
			return
		}
	} else {
		writeError(w,http.StatusPreconditionRequired,"preview_id is required; preview the Agent package before creating it")
		return
	}
	if err != nil {
		writeGitRepoError(w, err)
		return
	}
	definition, err := preparePackageConfiguration(&request, rawFields, resolved.bundle)
	if err != nil {
		writeGitRepoError(w, err)
		return
	}
	if definition.A2A != nil && r.Header.Get("X-Actor-Source") != "" {
		writeError(w, http.StatusForbidden, "A2A policy import requires a human actor")
		return
	}
	agentName, agentDescription, err := agentPackageInstanceProfile(request, rawFields, resolved.bundle)
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
		writeGitRepoError(w, err)
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
		writeGitRepoError(w, err)
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
			WorkspaceID:wsUUID, OwnerID:ownerUUID, Name:agentName, Description:agentDescription,
			RuntimeMode:runtime.RuntimeMode, RuntimeID:runtime.ID, RuntimeConfig:[]byte("{}"),
			CustomEnv:[]byte("{}"), CustomArgs:[]byte("[]"), MaxConcurrentTasks:6,
			Visibility:"private", PermissionMode:"private",
		}
		permission = resolvedPermission{mode:"private"}
	}
	created, err := materializeAgentBundleInTx(r.Context(), qtx, createParams, permission, manualSkills, func(created db.Agent) error {
		var createErr error
		if resolved.repository.HTMLURL == "" {
			source, createErr = qtx.CreateLocalAgentSource(r.Context(), db.CreateLocalAgentSourceParams{AgentID: created.ID, WorkspaceID: wsUUID, SyncedCommitSha: resolved.bundle.Hash, CreatedBy: ownerUUID})
		} else {
			source, createErr = qtx.CreateAgentSource(r.Context(), db.CreateAgentSourceParams{
				AgentID: created.ID, WorkspaceID: wsUUID, GitConnectionID: resolved.connection.ID,
				RepositoryUrl: resolved.repository.HTMLURL, RepoOwner: ownerFromFullName(resolved.repository.FullName), RepoName: repoFromFullName(resolved.repository.FullName),
				Ref: resolved.ref, ManifestPath: agentsource.SourceManifestPath(resolved.bundle), SyncedCommitSha: resolved.sha, CreatedBy: ownerUUID,
			})
		}
		if createErr != nil {
			return createErr
		}
		return (agentPackageService{handler:h}).Import(r.Context(), tx, created, source, resolved, request.Secrets, request.DeferredBindings, ownerUUID, true)
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
		writeError(w, http.StatusInternalServerError, "failed to commit Git Agent create")
		return
	}

	if h.EventTriggers != nil { h.EventTriggers.Notify() }
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
	if resolved.remote != nil {
		if currentSHA, resolveErr := resolved.remote.ResolveCommit(r.Context(), resolved.ref); resolveErr == nil && !strings.EqualFold(currentSHA, resolved.sha) {
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

func agentPackageInstanceProfile(request CreateAgentPackageRequest, rawFields map[string]json.RawMessage, bundle agentsource.Bundle) (string, string, error) {
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
