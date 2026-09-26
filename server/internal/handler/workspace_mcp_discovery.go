package handler

import (
	"net/http"

	featureflags "github.com/multica-ai/multica/server/internal/featureflags"
)

func (h *Handler) GetWorkspaceMCPDiscovery(w http.ResponseWriter, r *http.Request) {
	if !featureflags.WorkspaceMCPEndpointEnabled(r.Context(), h.FeatureFlags) {
		http.NotFound(w, r)
		return
	}
	workspaceID := workspaceIDFromURL(r, "id")
	baseURL, err := normalizeAgentA2APublicBaseURL(h.currentConfig().PublicURL)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "public URL unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"workspace_id":        workspaceID,
		"url":                 baseURL + "/api/mcp/workspaces/" + workspaceID,
		"authentication":      "Authorization: Bearer <workspace MCP token>",
		"replace_agent_links": featureflags.WorkspaceMCPReplaceAgentLinksEnabled(r.Context(), h.FeatureFlags),
	})
}
