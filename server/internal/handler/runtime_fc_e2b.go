package handler

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type createFCE2BRuntimeRequest struct {
	SandboxBackend  string `json:"sandbox_backend"`
	Name            string `json:"name"`
	ArtifactRef     string `json:"artifact_ref"`
	ArtifactBuildID string `json:"artifact_build_id"`
	ArtifactAlias   string `json:"artifact_alias"`
	ArtifactDigest  string `json:"artifact_digest"`
	ArtifactChannel string `json:"artifact_channel"`
	TemplateID      string `json:"template_id"`
	Template        string `json:"template"`
	TemplateChannel string `json:"template_channel"`
	Provider        string `json:"provider"`
	Visibility      string `json:"visibility"`
}

type updateFCE2BRuntimeTemplateRequest struct {
	TemplateID string `json:"template_id"`
}

type updateCloudSandboxArtifactRequest struct {
	ArtifactRef     string `json:"artifact_ref"`
	ArtifactBuildID string `json:"artifact_build_id"`
	ArtifactAlias   string `json:"artifact_alias"`
	ArtifactDigest  string `json:"artifact_digest"`
}

func (h *Handler) ListFCE2BTemplates(w http.ResponseWriter, r *http.Request) {
	if !h.cfg.FCE2B.Enabled {
		slog.Warn("FC/E2B template list rejected: runtime disabled")
		writeError(w, http.StatusServiceUnavailable, "FC/E2B runtime is disabled")
		return
	}
	if err := h.cfg.FCE2B.ValidateTemplateAPI(); err != nil {
		slog.Warn("FC/E2B template list rejected: invalid config", "error", err)
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}

	workspaceID := h.resolveWorkspaceID(r)
	if _, ok := h.requireWorkspaceRole(w, r, workspaceID, "workspace not found", "owner", "admin"); !ok {
		return
	}

	templates, err := service.ListFCE2BTemplates(r.Context(), h.cfg.FCE2B, nil)
	if err != nil {
		slog.Error("FC/E2B template list failed", "error", err)
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, templates)
}

func (h *Handler) CreateFCE2BRuntime(w http.ResponseWriter, r *http.Request) {
	h.createCloudSandboxRuntime(w, r, service.SandboxBackendAliyunFC)
}

func (h *Handler) CreateCloudSandboxRuntime(w http.ResponseWriter, r *http.Request) {
	h.createCloudSandboxRuntime(w, r, "")
}

func (h *Handler) createCloudSandboxRuntime(
	w http.ResponseWriter,
	r *http.Request,
	forcedBackend service.SandboxBackendKind,
) {
	var req createFCE2BRuntimeRequest
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
	}
	backend := service.SandboxBackendKind(strings.ToLower(strings.TrimSpace(req.SandboxBackend)))
	if forcedBackend != "" {
		if backend != "" && backend != forcedBackend {
			writeError(w, http.StatusBadRequest, "sandbox_backend does not match this endpoint")
			return
		}
		backend = forcedBackend
	}
	switch backend {
	case service.SandboxBackendAliyunFC:
		if !h.cfg.FCE2B.Enabled {
			writeError(w, http.StatusServiceUnavailable, "FC/E2B runtime is disabled")
			return
		}
		if err := h.cfg.FCE2B.Validate(); err != nil {
			writeError(w, http.StatusServiceUnavailable, err.Error())
			return
		}
	case service.SandboxBackendASB:
		if !h.cfg.ASB.Enabled || h.ASBLauncher == nil {
			writeError(w, http.StatusServiceUnavailable, "Aone Sandbox runtime is disabled")
			return
		}
		if err := h.cfg.ASB.Validate(); err != nil {
			writeError(w, http.StatusServiceUnavailable, err.Error())
			return
		}
	default:
		writeError(w, http.StatusBadRequest, "sandbox_backend must be 'aliyun_fc' or 'asb'")
		return
	}
	workspaceID := h.resolveWorkspaceID(r)
	member, ok := h.requireWorkspaceRole(w, r, workspaceID, "workspace not found", "owner", "admin")
	if !ok {
		return
	}
	switch backend {
	case service.SandboxBackendAliyunFC:
		h.createAliyunFCRuntime(w, r, workspaceID, member, req)
	case service.SandboxBackendASB:
		h.createASBRuntime(w, r, workspaceID, member, req)
	}
}

func (h *Handler) createAliyunFCRuntime(
	w http.ResponseWriter,
	r *http.Request,
	workspaceID string,
	member db.Member,
	req createFCE2BRuntimeRequest,
) {
	templateChannel := strings.ToLower(strings.TrimSpace(req.TemplateChannel))
	artifactChannel := strings.ToLower(strings.TrimSpace(req.ArtifactChannel))
	if templateChannel != "" && artifactChannel != "" && templateChannel != artifactChannel {
		writeError(w, http.StatusBadRequest, "artifact_channel and template_channel must match")
		return
	}
	if templateChannel == "" {
		templateChannel = artifactChannel
	}
	if templateChannel == "" {
		templateChannel = service.CloudSandboxChannelStable
	}
	if templateChannel != service.CloudSandboxChannelStable &&
		templateChannel != service.CloudSandboxChannelCandidate {
		writeError(w, http.StatusBadRequest, "template_channel must be 'stable' or 'candidate'")
		return
	}
	templateRef := strings.TrimSpace(req.TemplateID)
	expectedStableBuildID := ""
	expectedStableDigest := ""
	if templateRef == "" {
		templateRef = strings.TrimSpace(req.Template)
	}
	if templateChannel == service.CloudSandboxChannelCandidate {
		if !h.canPublishFCE2BStable(r) {
			writeError(w, http.StatusForbidden, "candidate FC/E2B runtimes are restricted to stable publishers")
			return
		}
		if templateRef == "" {
			writeError(w, http.StatusBadRequest, "template_id is required for a candidate runtime")
			return
		}
	} else {
		if templateRef != "" {
			writeError(w, http.StatusBadRequest, "stable runtimes resolve their template from the stable channel")
			return
		}
		if h.FCE2BStable == nil {
			writeError(w, http.StatusServiceUnavailable, "FC/E2B stable channel is unavailable")
			return
		}
		current, err := h.FCE2BStable.CurrentArtifact(r.Context(), service.SandboxBackendAliyunFC)
		if errors.Is(err, service.ErrFCE2BStableChannelUninitialized) {
			writeError(w, http.StatusServiceUnavailable, err.Error())
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to resolve FC/E2B stable template")
			return
		}
		templateRef = current.TemplateID
		expectedStableBuildID = current.TemplateBuildID
		expectedStableDigest = current.ArtifactDigest
	}
	templates, err := service.ListFCE2BTemplates(r.Context(), h.cfg.FCE2B, nil)
	if err != nil {
		slog.Error("FC/E2B template validation failed during runtime creation", "error", err)
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	selected, ok := selectFCE2BTemplate(templates, templateRef)
	if !ok {
		writeError(w, http.StatusBadRequest, "template_id does not match an available FC/E2B template")
		return
	}
	if expectedStableBuildID != "" && selected.BuildID != expectedStableBuildID {
		writeError(w, http.StatusServiceUnavailable, "stable template build no longer matches the verified catalog")
		return
	}
	if !service.IsFCE2BTemplateReady(selected) || !service.IsFCE2BTemplatePublished(selected) {
		writeError(w, http.StatusBadRequest, "FC/E2B template is not ready with a verified manifest")
		return
	}
	provider, ok := resolveFCE2BProvider(req.Provider, selected)
	if !ok {
		writeError(w, http.StatusBadRequest, "provider must be declared by the selected template: "+strings.Join(selected.Providers, ", "))
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = defaultFCE2BRuntimeName(provider, selected)
	}
	visibility := strings.TrimSpace(req.Visibility)
	if visibility == "" {
		visibility = "private"
	}
	if visibility != "private" && visibility != "public" {
		writeError(w, http.StatusBadRequest, "visibility must be 'private' or 'public'")
		return
	}

	metadata, err := json.Marshal(map[string]any{
		"kind":               service.CloudSandboxMetadataKind,
		"sandbox_backend":    string(service.SandboxBackendAliyunFC),
		"provider":           provider,
		"artifact_kind":      service.CloudSandboxArtifactE2BTemplate,
		"artifact_channel":   templateChannel,
		"artifact_ref":       selected.ID,
		"artifact_build_id":  selected.BuildID,
		"artifact_alias":     selected.Template,
		"artifact_digest":    expectedStableDigest,
		"artifact_status":    selected.Status,
		"template":           selected.Template,
		"template_id":        selected.ID,
		"template_build_id":  selected.BuildID,
		"template_alias":     selected.Template,
		"template_name":      selected.Name,
		"template_status":    selected.Status,
		"manifest_version":   selected.ManifestVersion,
		"capabilities":       fcE2BTemplateCapabilities(provider, selected),
		"component_versions": selected.ComponentVersions,
		"runner_protocol":    selected.RunnerProtocol,
		"template_channel":   templateChannel,
		"timeout_seconds":    h.cfg.FCE2B.TimeoutSeconds,
		"created_by":         uuidToString(member.UserID),
		"runner":             service.FCE2BRunnerCommandForProvider(provider),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to encode runtime metadata")
		return
	}

	daemonID := "fc-e2b:" + workspaceID + ":" + runtimeSlug(selected.Template) + ":" + runtimeSlug(name) + ":" + randomID()[:8]
	rt, err := h.Queries.UpsertCloudAgentRuntime(r.Context(), db.UpsertCloudAgentRuntimeParams{
		WorkspaceID: parseUUID(workspaceID),
		DaemonID:    pgtype.Text{String: daemonID, Valid: true},
		Name:        name,
		RuntimeMode: "cloud",
		Provider:    provider,
		Status:      "online",
		DeviceInfo:  name,
		Metadata:    metadata,
		OwnerID:     member.UserID,
		Visibility:  visibility,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create FC/E2B runtime")
		return
	}

	h.publish(protocol.EventDaemonRegister, workspaceID, "member", uuidToString(member.UserID), map[string]any{
		"action": "create",
	})
	writeJSON(w, http.StatusCreated, runtimeToResponse(rt))
}

func (h *Handler) createASBRuntime(
	w http.ResponseWriter,
	r *http.Request,
	workspaceID string,
	member db.Member,
	req createFCE2BRuntimeRequest,
) {
	artifactChannel := strings.ToLower(strings.TrimSpace(req.ArtifactChannel))
	if artifactChannel == "" {
		artifactChannel = strings.ToLower(strings.TrimSpace(req.TemplateChannel))
	}
	if artifactChannel == "" {
		artifactChannel = service.CloudSandboxChannelStable
	}
	if artifactChannel != service.CloudSandboxChannelStable &&
		artifactChannel != service.CloudSandboxChannelCandidate {
		writeError(w, http.StatusBadRequest, "artifact_channel must be 'stable' or 'candidate'")
		return
	}
	artifact := service.ASBArtifact{
		Ref:     strings.TrimSpace(req.ArtifactRef),
		BuildID: strings.TrimSpace(req.ArtifactBuildID),
		Alias:   strings.TrimSpace(req.ArtifactAlias),
		Digest:  strings.ToLower(strings.TrimSpace(req.ArtifactDigest)),
	}
	switch artifactChannel {
	case service.CloudSandboxChannelStable:
		if artifact.Ref != "" || artifact.BuildID != "" || artifact.Digest != "" ||
			strings.TrimSpace(req.TemplateID) != "" || strings.TrimSpace(req.Template) != "" {
			writeError(w, http.StatusBadRequest, "stable runtimes resolve their artifact from the ASB stable channel")
			return
		}
		if h.FCE2BStable == nil {
			writeError(w, http.StatusServiceUnavailable, "ASB stable channel is unavailable")
			return
		}
		current, err := h.FCE2BStable.CurrentASBArtifact(r.Context())
		if errors.Is(err, service.ErrFCE2BStableChannelUninitialized) {
			writeError(w, http.StatusServiceUnavailable, err.Error())
			return
		}
		if err != nil {
			slog.Error("ASB stable artifact resolution failed", "error", err)
			writeError(w, http.StatusServiceUnavailable, "failed to resolve the verified ASB stable artifact")
			return
		}
		artifact = current
	case service.CloudSandboxChannelCandidate:
		if !h.canPublishFCE2BStable(r) {
			writeError(w, http.StatusForbidden, "candidate ASB runtimes are restricted to stable publishers")
			return
		}
		if artifact.Ref == "" || artifact.BuildID == "" || artifact.Digest == "" {
			writeError(w, http.StatusBadRequest, "artifact_ref, artifact_build_id and artifact_digest are required for a candidate runtime")
			return
		}
		manifest, err := h.ASBLauncher.VerifyStableArtifact(r.Context(), artifact)
		if err != nil {
			slog.Error("ASB candidate artifact validation failed", "error", err)
			writeError(w, http.StatusBadRequest, "ASB candidate artifact validation failed")
			return
		}
		artifact.Manifest = manifest
	}

	metadataValues, err := service.BuildASBRuntimeMetadata(
		artifact,
		req.Provider,
		artifactChannel,
	)
	if errors.Is(err, service.ErrFCE2BTemplateProviderUnsupported) {
		writeError(w, http.StatusBadRequest, "provider is not declared by the ASB runtime manifest")
		return
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	provider, _ := metadataValues["provider"].(string)
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = "ASB-" + cloudSandboxProviderDisplayName(provider)
	}
	visibility := strings.TrimSpace(req.Visibility)
	if visibility == "" {
		visibility = "private"
	}
	if visibility != "private" && visibility != "public" {
		writeError(w, http.StatusBadRequest, "visibility must be 'private' or 'public'")
		return
	}
	metadataValues["timeout_seconds"] = h.cfg.ASB.TimeoutSeconds
	metadataValues["created_by"] = uuidToString(member.UserID)
	metadata, err := json.Marshal(metadataValues)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to encode runtime metadata")
		return
	}
	daemonID := "cloud-sandbox:" + workspaceID + ":asb:" + runtimeSlug(provider) + ":" +
		runtimeSlug(name) + ":" + randomID()[:8]
	rt, err := h.Queries.UpsertCloudAgentRuntime(r.Context(), db.UpsertCloudAgentRuntimeParams{
		WorkspaceID: parseUUID(workspaceID),
		DaemonID:    pgtype.Text{String: daemonID, Valid: true},
		Name:        name,
		RuntimeMode: "cloud",
		Provider:    provider,
		Status:      "online",
		DeviceInfo:  name,
		Metadata:    metadata,
		OwnerID:     member.UserID,
		Visibility:  visibility,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create ASB runtime")
		return
	}
	h.publish(protocol.EventDaemonRegister, workspaceID, "member", uuidToString(member.UserID), map[string]any{
		"action": "create",
	})
	writeJSON(w, http.StatusCreated, runtimeToResponse(rt))
}

func cloudSandboxProviderDisplayName(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "hermes":
		return "Hermes"
	case "opencode":
		return "OpenCode"
	case "pi":
		return "Pi"
	default:
		return provider
	}
}

func (h *Handler) UpdateCloudSandboxRuntimeArtifact(w http.ResponseWriter, r *http.Request) {
	if !h.cfg.ASB.Enabled || h.ASBLauncher == nil {
		writeError(w, http.StatusServiceUnavailable, "Aone Sandbox runtime is disabled")
		return
	}
	if err := h.cfg.ASB.Validate(); err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	runtimeID := chi.URLParam(r, "runtimeId")
	runtimeUUID, ok := parseUUIDOrBadRequest(w, runtimeID, "runtime_id")
	if !ok {
		return
	}
	runtime, err := h.Queries.GetAgentRuntime(r.Context(), runtimeUUID)
	if err != nil {
		writeError(w, http.StatusNotFound, "runtime not found")
		return
	}
	member, ok := h.requireWorkspaceRole(
		w,
		r,
		uuidToString(runtime.WorkspaceID),
		"runtime not found",
		"owner",
		"admin",
	)
	if !ok {
		return
	}
	if !service.IsASBRuntime(runtime) {
		writeError(w, http.StatusBadRequest, service.ErrCloudSandboxRuntimeRequired.Error())
		return
	}
	if service.CloudSandboxRuntimeChannel(runtime) != service.CloudSandboxChannelCandidate {
		writeError(w, http.StatusConflict, "stable-managed runtime artifacts can only be changed by a stable release")
		return
	}
	if !h.canPublishFCE2BStable(r) {
		writeError(w, http.StatusForbidden, "candidate ASB runtime updates are restricted to stable publishers")
		return
	}
	var req updateCloudSandboxArtifactRequest
	if r.Body == nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	artifact := service.ASBArtifact{
		Ref:     strings.TrimSpace(req.ArtifactRef),
		BuildID: strings.TrimSpace(req.ArtifactBuildID),
		Alias:   strings.TrimSpace(req.ArtifactAlias),
		Digest:  strings.ToLower(strings.TrimSpace(req.ArtifactDigest)),
	}
	manifest, err := h.ASBLauncher.VerifyStableArtifact(r.Context(), artifact)
	if err != nil {
		slog.Error("ASB candidate artifact update validation failed",
			"error", err,
			"runtime_id", runtimeID,
		)
		writeError(w, http.StatusBadRequest, "ASB candidate artifact validation failed")
		return
	}
	artifact.Manifest = manifest
	result, err := h.ASBLauncher.UpdateRuntimeArtifact(r.Context(), runtimeUUID, artifact)
	if err != nil {
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			writeError(w, http.StatusNotFound, "runtime not found")
		case errors.Is(err, service.ErrCloudSandboxRuntimeRequired):
			writeError(w, http.StatusBadRequest, err.Error())
		default:
			slog.Error("ASB runtime artifact update failed",
				"error", err,
				"runtime_id", runtimeID,
			)
			writeError(w, http.StatusInternalServerError, "failed to update ASB runtime artifact")
		}
		return
	}
	slog.Info("ASB runtime artifact update completed",
		"event", "cloud_sandbox_runtime_artifact_updated",
		"actor_id", uuidToString(member.UserID),
		"workspace_id", uuidToString(result.Runtime.WorkspaceID),
		"runtime_id", runtimeID,
		"sandbox_backend", string(service.SandboxBackendASB),
		"previous_artifact_ref", result.PreviousArtifactRef,
		"previous_artifact_build_id", result.PreviousArtifactBuildID,
		"previous_artifact_digest", result.PreviousArtifactDigest,
		"artifact_ref", artifact.Ref,
		"artifact_build_id", artifact.BuildID,
		"artifact_digest", artifact.Digest,
		"invalidated_sandbox_count", result.InvalidatedSandboxCount,
		"changed", result.Changed,
	)
	if result.Changed {
		h.publish(
			protocol.EventDaemonRegister,
			uuidToString(result.Runtime.WorkspaceID),
			"member",
			uuidToString(member.UserID),
			map[string]any{"action": "update"},
		)
	}
	writeJSON(w, http.StatusOK, runtimeToResponse(result.Runtime))
}

func (h *Handler) UpdateFCE2BRuntimeTemplate(w http.ResponseWriter, r *http.Request) {
	if !h.cfg.FCE2B.Enabled {
		writeError(w, http.StatusServiceUnavailable, "FC/E2B runtime is disabled")
		return
	}
	if err := h.cfg.FCE2B.ValidateTemplateAPI(); err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	if h.FCE2BLauncher == nil {
		writeError(w, http.StatusServiceUnavailable, "FC/E2B runtime launcher is unavailable")
		return
	}

	runtimeID := chi.URLParam(r, "runtimeId")
	runtimeUUID, ok := parseUUIDOrBadRequest(w, runtimeID, "runtime_id")
	if !ok {
		return
	}
	runtime, err := h.Queries.GetAgentRuntime(r.Context(), runtimeUUID)
	if err != nil {
		writeError(w, http.StatusNotFound, "runtime not found")
		return
	}
	member, ok := h.requireWorkspaceRole(w, r, uuidToString(runtime.WorkspaceID), "runtime not found", "owner", "admin")
	if !ok {
		return
	}
	if !service.IsFCE2BRuntime(runtime) {
		writeError(w, http.StatusBadRequest, service.ErrFCE2BRuntimeRequired.Error())
		return
	}
	if service.FCE2BRuntimeTemplateChannel(runtime) != "candidate" {
		writeError(w, http.StatusConflict, "stable-managed runtime templates can only be changed by a stable release")
		return
	}
	if !h.canPublishFCE2BStable(r) {
		writeError(w, http.StatusForbidden, "candidate FC/E2B runtime updates are restricted to stable publishers")
		return
	}

	var req updateFCE2BRuntimeTemplateRequest
	if r.Body == nil || json.NewDecoder(r.Body).Decode(&req) != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	templateID := strings.TrimSpace(req.TemplateID)
	if templateID == "" {
		writeError(w, http.StatusBadRequest, "template_id is required")
		return
	}

	templates, err := service.ListFCE2BTemplates(r.Context(), h.cfg.FCE2B, h.FCE2BLauncher.Runner)
	if err != nil {
		slog.Error("FC/E2B template validation failed during runtime update", "error", err, "runtime_id", runtimeID)
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	selected, ok := selectFCE2BTemplateByID(templates, templateID)
	if !ok {
		writeError(w, http.StatusBadRequest, "template_id does not match an available FC/E2B template ID")
		return
	}
	if !service.IsFCE2BTemplateReady(selected) {
		writeError(w, http.StatusBadRequest, "FC/E2B template is not ready")
		return
	}

	result, err := h.FCE2BLauncher.UpdateRuntimeTemplate(r.Context(), runtimeUUID, selected)
	if err != nil {
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			writeError(w, http.StatusNotFound, "runtime not found")
		case errors.Is(err, service.ErrFCE2BRuntimeRequired):
			writeError(w, http.StatusBadRequest, err.Error())
		case errors.Is(err, service.ErrFCE2BTemplateProviderUnsupported):
			writeError(w, http.StatusBadRequest, err.Error())
		default:
			slog.Error("FC/E2B runtime template update failed", "error", err, "runtime_id", runtimeID)
			writeError(w, http.StatusInternalServerError, "failed to update FC/E2B runtime template")
		}
		return
	}

	slog.Info("FC/E2B runtime template update completed",
		"event", "fc_e2b_runtime_template_updated",
		"actor_id", uuidToString(member.UserID),
		"workspace_id", uuidToString(result.Runtime.WorkspaceID),
		"runtime_id", runtimeID,
		"provider", result.Runtime.Provider,
		"previous_template", result.PreviousTemplate,
		"previous_template_id", result.PreviousTemplateID,
		"previous_template_build_id", result.PreviousTemplateBuildID,
		"template", selected.Template,
		"template_id", selected.ID,
		"template_build_id", selected.BuildID,
		"invalidated_sandbox_count", result.InvalidatedSandboxCount,
		"changed", result.Changed,
	)
	if result.Changed {
		h.publish(protocol.EventDaemonRegister, uuidToString(result.Runtime.WorkspaceID), "member", uuidToString(member.UserID), map[string]any{
			"action": "update",
		})
	}
	writeJSON(w, http.StatusOK, runtimeToResponse(result.Runtime))
}

func selectFCE2BTemplate(templates []service.FCE2BTemplate, ref string) (service.FCE2BTemplate, bool) {
	ref = strings.TrimSpace(ref)
	for _, t := range templates {
		for _, candidate := range []string{t.Template, t.ID, t.Name} {
			if strings.TrimSpace(candidate) == ref {
				return t, true
			}
		}
	}
	return service.FCE2BTemplate{}, false
}

func selectFCE2BTemplateByID(templates []service.FCE2BTemplate, id string) (service.FCE2BTemplate, bool) {
	id = strings.TrimSpace(id)
	if id == "" {
		return service.FCE2BTemplate{}, false
	}
	for _, template := range templates {
		if strings.TrimSpace(template.ID) == id && strings.TrimSpace(template.ID) != "" {
			return template, true
		}
	}
	return service.FCE2BTemplate{}, false
}

// resolveFCE2BProvider picks an explicit provider only when the verified
// template manifest declares it. With no explicit value, the first supported
// provider in the manifest is selected.
func resolveFCE2BProvider(requested string, t service.FCE2BTemplate) (string, bool) {
	requested = strings.ToLower(strings.TrimSpace(requested))
	if requested == "" {
		return service.FCE2BProviderForTemplate(t)
	}
	if !service.FCE2BTemplateSupportsProvider(t, requested) {
		return "", false
	}
	return requested, true
}

// defaultFCE2BRuntimeName derives a display name from the template, prefixing
// the provider when the template name does not already mention it — two
// runtimes created from the same dual-CLI template must not collide.
func defaultFCE2BRuntimeName(provider string, t service.FCE2BTemplate) string {
	providerPart := strings.ToUpper(provider[:1]) + provider[1:]
	base := strings.TrimSpace(t.Name)
	if base == "" {
		base = strings.TrimSpace(t.Template)
	}
	if base == "" {
		return "FC-" + providerPart
	}
	base = strings.TrimPrefix(base, "multica-fc-")
	base = strings.TrimSuffix(base, "-runtime")
	base = strings.TrimSuffix(base, "-template")
	parts := strings.FieldsFunc(base, func(r rune) bool {
		return r == '-' || r == '_' || r == '.'
	})
	clean := make([]string, 0, len(parts)+1)
	hasProvider := false
	for _, part := range parts {
		if part == "" {
			continue
		}
		if strings.EqualFold(part, provider) {
			hasProvider = true
		}
		clean = append(clean, strings.ToUpper(part[:1])+part[1:])
	}
	if len(clean) == 0 {
		return "FC-" + providerPart
	}
	if !hasProvider {
		clean = append([]string{providerPart}, clean...)
	}
	return "FC-" + strings.Join(clean, "-")
}

func fcE2BTemplateCapabilities(provider string, t service.FCE2BTemplate) []string {
	return service.FCE2BTemplateCapabilities(provider, t)
}

func runtimeSlug(name string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash && b.Len() > 0 {
			b.WriteByte('-')
			lastDash = true
		}
	}
	slug := strings.Trim(b.String(), "-")
	if slug == "" {
		return "fc-hermes"
	}
	return slug
}
