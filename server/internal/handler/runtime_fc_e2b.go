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
	Visibility string `json:"visibility"`
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
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = "FC-Hermes"
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
		"template":        h.cfg.FCE2B.Template,
		"api_url":         h.cfg.FCE2B.APIURL,
		"domain":          h.cfg.FCE2B.Domain,
		"model_base_url":  h.cfg.FCE2B.LLMBaseURL,
		"model":           h.cfg.FCE2B.LLMModel,
		"timeout_seconds": h.cfg.FCE2B.TimeoutSeconds,
		"created_by":      uuidToString(member.UserID),
		"runner":          service.FCE2BRunnerCommand,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to encode runtime metadata")
		return
	}

	daemonID := "fc-e2b:" + workspaceID + ":" + runtimeSlug(name)
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
