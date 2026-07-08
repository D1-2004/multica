package handler

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type createFCE2BRuntimeRequest struct {
	Name       string `json:"name"`
	TemplateID string `json:"template_id"`
	Template   string `json:"template"`
	Visibility string `json:"visibility"`
}

func (h *Handler) ListFCE2BTemplates(w http.ResponseWriter, r *http.Request) {
	if !h.cfg.FCE2B.Enabled {
		writeError(w, http.StatusServiceUnavailable, "FC/E2B runtime is disabled")
		return
	}
	if err := h.cfg.FCE2B.ValidateTemplateAPI(); err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}

	workspaceID := h.resolveWorkspaceID(r)
	if _, ok := h.requireWorkspaceRole(w, r, workspaceID, "workspace not found", "owner", "admin"); !ok {
		return
	}

	templates, err := service.ListFCE2BTemplates(r.Context(), h.cfg.FCE2B, nil)
	if err != nil {
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
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	selected, ok := selectFCE2BTemplate(templates, templateRef)
	if !ok {
		writeError(w, http.StatusBadRequest, "template_id does not match an available FC/E2B template")
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = defaultFCE2BRuntimeName(selected)
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
		"capabilities":    fcE2BTemplateCapabilities(selected),
		"timeout_seconds": h.cfg.FCE2B.TimeoutSeconds,
		"created_by":      uuidToString(member.UserID),
		"runner":          service.FCE2BRunnerCommand,
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
		Provider:    service.FCE2BProvider,
		Status:      "online",
		DeviceInfo:  "FC/E2B one-shot sandbox",
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

func defaultFCE2BRuntimeName(t service.FCE2BTemplate) string {
	base := strings.TrimSpace(t.Name)
	if base == "" {
		base = strings.TrimSpace(t.Template)
	}
	if base == "" {
		return "FC-Hermes"
	}
	base = strings.TrimPrefix(base, "multica-fc-")
	base = strings.TrimSuffix(base, "-runtime")
	base = strings.TrimSuffix(base, "-template")
	parts := strings.FieldsFunc(base, func(r rune) bool {
		return r == '-' || r == '_' || r == '.'
	})
	clean := make([]string, 0, len(parts))
	for _, part := range parts {
		if part == "" {
			continue
		}
		clean = append(clean, strings.ToUpper(part[:1])+part[1:])
	}
	if len(clean) == 0 {
		return "FC-Hermes"
	}
	return "FC-" + strings.Join(clean, "-")
}

func fcE2BTemplateCapabilities(t service.FCE2BTemplate) []string {
	capabilities := []string{"hermes"}
	haystack := strings.ToLower(strings.Join([]string{t.Template, t.ID, t.Name}, " "))
	if strings.Contains(haystack, "dws") {
		capabilities = append(capabilities, "dws")
	}
	return capabilities
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
