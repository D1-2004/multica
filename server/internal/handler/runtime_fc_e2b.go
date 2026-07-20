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
	Name       string `json:"name"`
	TemplateID string `json:"template_id"`
	Template   string `json:"template"`
	Provider   string `json:"provider"`
	Visibility string `json:"visibility"`
}

type updateFCE2BRuntimeTemplateRequest struct {
	TemplateID string `json:"template_id"`
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
	if !h.cfg.FCE2B.Enabled {
		writeError(w, http.StatusServiceUnavailable, "FC/E2B runtime is disabled")
		return
	}
	if err := h.cfg.FCE2B.Validate(); err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}

	workspaceID := h.resolveWorkspaceID(r)
	member, ok := h.requireWorkspaceRole(w, r, workspaceID, "workspace not found", "owner", "admin")
	if !ok {
		return
	}

	var req createFCE2BRuntimeRequest
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
	}
	templateRef := strings.TrimSpace(req.TemplateID)
	if templateRef == "" {
		templateRef = strings.TrimSpace(req.Template)
	}
	if templateRef == "" {
		writeError(w, http.StatusBadRequest, "template_id is required")
		return
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
	provider, ok := resolveFCE2BProvider(req.Provider, selected)
	if !ok {
		writeError(w, http.StatusBadRequest, "provider must be one of: "+strings.Join(service.FCE2BSupportedProviders, ", "))
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
		"kind":            service.FCE2BMetadataKind,
		"template":        selected.Template,
		"template_id":     selected.ID,
		"template_name":   selected.Name,
		"template_status": selected.Status,
		"capabilities":    fcE2BTemplateCapabilities(provider, selected),
		"timeout_seconds": h.cfg.FCE2B.TimeoutSeconds,
		"created_by":      uuidToString(member.UserID),
		"runner":          service.FCE2BRunnerCommandForProvider(provider),
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
		"template", selected.Template,
		"template_id", selected.ID,
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

// resolveFCE2BProvider picks the agent provider for a new runtime: an explicit
// request value wins (a template image may ship several agent CLIs), otherwise
// the template name decides. Returns ok=false for unsupported values.
func resolveFCE2BProvider(requested string, t service.FCE2BTemplate) (string, bool) {
	requested = strings.ToLower(strings.TrimSpace(requested))
	if requested == "" {
		return service.FCE2BProviderForTemplate(t.Template, t.ID, t.Name), true
	}
	if !service.IsFCE2BSupportedProvider(requested) {
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
