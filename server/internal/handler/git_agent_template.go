package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type gitAgentTemplateContextKey struct{}

type GitAgentTemplateResponse struct {
	service.GitAgentTemplate
	Available           bool                      `json:"available"`
	UnavailableReason   string                    `json:"unavailable_reason,omitempty"`
	ResolvedSHA         string                    `json:"resolved_sha,omitempty"`
	DefaultAgentName    string                    `json:"default_agent_name,omitempty"`
	DefaultDescription  string                    `json:"default_description,omitempty"`
	CompatibleProviders []string                  `json:"compatible_providers,omitempty"`
	Skills              []GitHubAgentSkillPreview `json:"skills,omitempty"`
	Warnings            []string                  `json:"warnings,omitempty"`
	Blockers            []string                  `json:"blockers,omitempty"`
}

type CreateGitAgentFromTemplateRequest struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
	RuntimeID   string  `json:"runtime_id"`
}

func (h *Handler) ListGitAgentTemplates(w http.ResponseWriter, r *http.Request) {
	if h.GitAgentTemplates == nil {
		writeJSON(w, http.StatusOK, map[string]any{"templates": []GitAgentTemplateResponse{}})
		return
	}
	workspaceID := workspaceIDFromURL(r, "id")
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	items := h.GitAgentTemplates.List(r.Context(), workspaceID)
	response := make([]GitAgentTemplateResponse, 0, len(items))
	for _, item := range items {
		entry := GitAgentTemplateResponse{GitAgentTemplate: item}
		switch {
		case !item.Enabled:
			entry.UnavailableReason = "template_disabled"
		case h.GitHubApp == nil:
			entry.UnavailableReason = "github_unavailable"
		default:
			_, err := h.gitAgentTemplateInstallation(r.Context(), wsUUID, item)
			if err == nil {
				entry.Available = true
			} else {
				entry.UnavailableReason = gitAgentTemplateUnavailableReason(err)
			}
		}
		response = append(response, entry)
	}
	writeJSON(w, http.StatusOK, map[string]any{"templates": response})
}

func (h *Handler) GetGitAgentTemplate(w http.ResponseWriter, r *http.Request) {
	if h.GitAgentTemplates == nil {
		writeError(w, http.StatusNotFound, "Git agent template not found")
		return
	}
	workspaceID := workspaceIDFromURL(r, "id")
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	item, err := h.GitAgentTemplates.Get(r.Context(), workspaceID, chi.URLParam(r, "templateKey"))
	if errors.Is(err, service.ErrGitAgentTemplateNotFound) {
		writeError(w, http.StatusNotFound, "Git agent template not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load Git agent template")
		return
	}
	if !item.Enabled {
		writeJSON(w, http.StatusOK, GitAgentTemplateResponse{GitAgentTemplate: item, UnavailableReason: "template_disabled"})
		return
	}
	resolved, err := h.resolveGitAgentTemplate(r.Context(), wsUUID, item)
	if err != nil {
		writeJSON(w, http.StatusOK, GitAgentTemplateResponse{GitAgentTemplate: item, UnavailableReason: gitAgentTemplateUnavailableReason(err)})
		return
	}
	blockers, err := h.githubAgentPreviewBlockers(r.Context(), wsUUID, resolved.bundle)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to check Git agent template conflicts")
		return
	}
	writeJSON(w, http.StatusOK, GitAgentTemplateResponse{
		GitAgentTemplate:    item,
		Available:           true,
		ResolvedSHA:         resolved.sha,
		DefaultAgentName:    resolved.bundle.Manifest.Metadata.Name,
		DefaultDescription:  resolved.bundle.Manifest.Metadata.Description,
		CompatibleProviders: resolved.bundle.Manifest.Spec.Compatibility.Providers,
		Skills:              sourceSkillPreviews(resolved.bundle.Skills),
		Warnings:            resolved.bundle.Warnings,
		Blockers:            blockers,
	})
}

func (h *Handler) CreateGitAgentFromTemplate(w http.ResponseWriter, r *http.Request) {
	if h.GitAgentTemplates == nil {
		writeError(w, http.StatusNotFound, "Git agent template not found")
		return
	}
	workspaceID := workspaceIDFromURL(r, "id")
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	item, err := h.GitAgentTemplates.Get(r.Context(), workspaceID, chi.URLParam(r, "templateKey"))
	if errors.Is(err, service.ErrGitAgentTemplateNotFound) {
		writeError(w, http.StatusNotFound, "Git agent template not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load Git agent template")
		return
	}
	if !item.Enabled {
		writeError(w, http.StatusConflict, "Git agent template is disabled")
		return
	}
	var request CreateGitAgentFromTemplateRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(request.RuntimeID) == "" {
		writeError(w, http.StatusBadRequest, "runtime_id is required")
		return
	}
	resolved, err := h.resolveGitAgentTemplate(r.Context(), wsUUID, item)
	if err != nil {
		writeGitAgentTemplateResolutionError(w, err)
		return
	}

	payload := map[string]any{
		"installation_id": uuidToString(resolved.installation.ID),
		"repository":      resolved.repository.FullName,
		"ref":             resolved.ref,
		"resolved_sha":    resolved.sha,
		"runtime_id":      strings.TrimSpace(request.RuntimeID),
		"visibility":      "private",
	}
	if request.Name != nil {
		payload["name"] = *request.Name
	}
	if request.Description != nil {
		payload["description"] = *request.Description
	}
	body, err := json.Marshal(payload)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to encode Git agent create request")
		return
	}
	forward := r.Clone(context.WithValue(r.Context(), gitAgentTemplateContextKey{}, item.Key))
	forward.Body = io.NopCloser(bytes.NewReader(body))
	h.CreateGitHubAgent(w, forward)
}

func (h *Handler) resolveGitAgentTemplate(ctx context.Context, workspaceID pgtype.UUID, item service.GitAgentTemplate) (resolvedGitHubAgentSource, error) {
	if h.GitHubApp == nil {
		return resolvedGitHubAgentSource{}, errors.New("GitHub agent sources are unavailable")
	}
	installation, err := h.gitAgentTemplateInstallation(ctx, workspaceID, item)
	if err != nil {
		return resolvedGitHubAgentSource{}, err
	}
	return h.resolveAndCompileGitHubAgent(ctx, workspaceID, GitHubAgentSourceInput{
		InstallationID: uuidToString(installation.ID),
		Repository:     item.Repository,
		Ref:            item.Ref,
	})
}

func (h *Handler) gitAgentTemplateInstallation(ctx context.Context, workspaceID pgtype.UUID, item service.GitAgentTemplate) (db.GithubInstallation, error) {
	owner := ownerFromFullName(item.Repository)
	installations, err := h.Queries.ListGitHubInstallationsByWorkspace(ctx, workspaceID)
	if err != nil {
		return db.GithubInstallation{}, fmt.Errorf("list GitHub installations: %w", err)
	}
	var candidates []db.GithubInstallation
	for _, installation := range installations {
		if strings.EqualFold(strings.TrimSpace(installation.AccountLogin), owner) {
			candidates = append(candidates, installation)
		}
	}
	if len(candidates) == 0 {
		return db.GithubInstallation{}, errors.New("GitHub installation for template repository owner not found")
	}
	if len(candidates) > 1 {
		return db.GithubInstallation{}, errors.New("multiple GitHub installations match template repository owner")
	}
	return candidates[0], nil
}

func gitAgentTemplateUnavailableReason(err error) string {
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "installation") && strings.Contains(message, "not found"):
		return "installation_unavailable"
	case strings.Contains(message, "multiple github installations"):
		return "installation_ambiguous"
	case strings.Contains(message, "not accessible") || strings.Contains(message, "repository"):
		return "repository_unavailable"
	case strings.Contains(message, "unavailable"):
		return "github_unavailable"
	default:
		return "template_unavailable"
	}
}

func writeGitAgentTemplateResolutionError(w http.ResponseWriter, err error) {
	switch gitAgentTemplateUnavailableReason(err) {
	case "installation_unavailable", "installation_ambiguous":
		writeError(w, http.StatusConflict, err.Error())
	case "github_unavailable":
		writeError(w, http.StatusServiceUnavailable, err.Error())
	default:
		writeGitHubSourceError(w, err)
	}
}
