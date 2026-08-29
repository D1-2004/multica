package handler

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/multica-ai/multica/server/internal/sitehosting"
)

type multicaMCPPrepareStaticSiteArguments struct {
	SiteID         string `json:"site_id"`
	ExpectedSHA256 string `json:"expected_sha256"`
	ContentLength  int64  `json:"content_length"`
	Entrypoint     string `json:"entrypoint"`
	SPAFallback    bool   `json:"spa_fallback"`
}

type multicaMCPGetStaticSiteArguments struct {
	SiteID string `json:"site_id"`
}

func multicaMCPPrepareStaticSiteDefinition() map[string]any {
	return map[string]any{
		"name": multicaMCPPrepareStaticSiteTool,
		"title": "Prepare a static site deployment",
		"description": "Create a public-unlisted static Site or a new revision and return a short-lived, single-use raw ZIP upload capability. Workspace, Agent, and task identity come only from the authenticated task token. In a sandbox, PUT upload_path through the current MULTICA_SERVER_URL with the task token in Authorization and the upload capability in the returned upload_token_header. For direct public upload_url access, Authorization: Bearer <upload_token> remains supported. Send application/zip; never put ZIP or base64 data in MCP arguments.",
		"inputSchema": map[string]any{
			"type": "object", "additionalProperties": false,
			"properties": map[string]any{
				"site_id": map[string]any{"type": "string", "description": "Existing Site UUID to update. Omit to create a Site."},
				"expected_sha256": map[string]any{"type": "string", "pattern": "^[0-9a-fA-F]{64}$"},
				"content_length": map[string]any{"type": "integer", "minimum": 1, "maximum": sitehosting.DefaultMaxArchiveBytes},
				"entrypoint": map[string]any{"type": "string", "default": "index.html"},
				"spa_fallback": map[string]any{"type": "boolean", "default": false},
			},
			"required": []string{"expected_sha256", "content_length"},
		},
		"annotations": map[string]any{"readOnlyHint": false, "destructiveHint": false, "idempotentHint": false, "openWorldHint": false},
	}
}

func multicaMCPGetStaticSiteDefinition() map[string]any {
	return map[string]any{
		"name": multicaMCPGetStaticSiteTool,
		"title": "Get a static site deployment",
		"description": "Get deployment status for a Site owned by the authenticated task Agent in the authenticated Workspace.",
		"inputSchema": map[string]any{
			"type": "object", "additionalProperties": false,
			"properties": map[string]any{"site_id": map[string]any{"type": "string"}},
			"required": []string{"site_id"},
		},
		"annotations": map[string]any{"readOnlyHint": true, "destructiveHint": false, "idempotentHint": true, "openWorldHint": false},
	}
}

func (h *Handler) handleMulticaMCPStaticSiteCall(w http.ResponseWriter, r *http.Request, id json.RawMessage, toolName string, rawArguments json.RawMessage) {
	if !multicaMCPTaskTokenAuthenticated(r) {
		h.writeMulticaMCPToolError(w, id, "static site tools require a task token")
		return
	}
	if h.SiteHosting == nil {
		h.writeMulticaMCPToolError(w, id, "static site hosting is unavailable")
		return
	}
	workspaceID := strings.TrimSpace(r.Header.Get("X-Workspace-ID"))
	agentID := strings.TrimSpace(r.Header.Get("X-Agent-ID"))
	if workspaceID == "" || agentID == "" {
		h.writeMulticaMCPToolError(w, id, "authenticated static site authority is invalid")
		return
	}
	var result any
	var err error
	if toolName == multicaMCPPrepareStaticSiteTool {
		var args multicaMCPPrepareStaticSiteArguments
		if decodeErr := decodeMulticaMCPArguments(rawArguments, &args); decodeErr != nil {
			h.writeMulticaMCPError(w, id, -32602, "invalid prepare_static_site_deploy arguments")
			return
		}
		result, err = h.SiteHosting.Prepare(r.Context(), sitehosting.PrepareInput{
			WorkspaceID: workspaceID, AgentID: agentID, SiteID: strings.TrimSpace(args.SiteID),
			ExpectedSHA256: args.ExpectedSHA256, ExpectedLength: args.ContentLength,
			Entrypoint: args.Entrypoint, SPAFallback: args.SPAFallback,
		})
	} else {
		var args multicaMCPGetStaticSiteArguments
		if decodeErr := decodeMulticaMCPArguments(rawArguments, &args); decodeErr != nil || strings.TrimSpace(args.SiteID) == "" {
			h.writeMulticaMCPError(w, id, -32602, "invalid get_static_site_deploy arguments")
			return
		}
		result, err = h.SiteHosting.GetStatus(r.Context(), strings.TrimSpace(args.SiteID), workspaceID, agentID)
	}
	if err != nil {
		message := "static site operation failed"
		switch {
		case errors.Is(err, sitehosting.ErrUnavailable):
			message = "static site hosting is unavailable"
		case errors.Is(err, sitehosting.ErrSiteForbidden), errors.Is(err, sitehosting.ErrSiteNotFound):
			message = "static Site was not found or is not owned by this Agent"
		default:
			slog.Error("Multica MCP static site operation failed", "tool", toolName, "source_task_id", r.Header.Get("X-Task-ID"), "error", err)
		}
		h.writeMulticaMCPToolError(w, id, message)
		return
	}
	payload, _ := json.Marshal(result)
	h.writeMulticaMCPResult(w, id, multicaMCPToolResult{
		Content: []multicaMCPContent{{Type: "text", Text: string(payload)}},
		StructuredContent: result,
	})
}
