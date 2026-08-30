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
		"description": "Create a public-unlisted static Site or a new revision and return a short-lived, single-use raw ZIP upload capability. Hosted pages may set window.__MULTICA_FETCH_PROXY_ALLOWLIST__ to an array of exact HTTPS URLs. When ordinary window.fetch calls an exact match, Multica sends the request through its same-origin proxy; same-origin and unmatched requests continue to use native fetch. The server independently enforces allowed target origins and SSRF protections; the page allowlist does not grant server-side access. Site ownership comes only from the user authenticated by the API token or task token; ownership identifiers are never accepted as arguments. In a sandbox, PUT upload_path through the current MULTICA_SERVER_URL with the task token in Authorization and the upload capability in the returned upload_token_header. For direct public upload_url access, Authorization: Bearer <upload_token> remains supported. Send application/zip; never put ZIP or base64 data in MCP arguments.",
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
		"outputSchema": multicaMCPPrepareStaticSiteOutputSchema(),
		"annotations": map[string]any{"readOnlyHint": false, "destructiveHint": false, "idempotentHint": false, "openWorldHint": false},
	}
}

func multicaMCPGetStaticSiteDefinition() map[string]any {
	return map[string]any{
		"name": multicaMCPGetStaticSiteTool,
		"title": "Get a static site deployment",
		"description": "Get deployment status for a Site owned by the user authenticated by the API token or task token.",
		"inputSchema": map[string]any{
			"type": "object", "additionalProperties": false,
			"properties": map[string]any{"site_id": map[string]any{"type": "string"}},
			"required": []string{"site_id"},
		},
		"outputSchema": multicaMCPGetStaticSiteOutputSchema(),
		"annotations": map[string]any{"readOnlyHint": true, "destructiveHint": false, "idempotentHint": true, "openWorldHint": false},
	}
}

func multicaMCPPrepareStaticSiteOutputSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"site_id":             map[string]any{"type": "string"},
			"revision_id":         map[string]any{"type": "string"},
			"upload_id":           map[string]any{"type": "string"},
			"upload_path":         map[string]any{"type": "string"},
			"upload_url":          map[string]any{"type": "string"},
			"upload_method":       map[string]any{"type": "string", "enum": []string{"PUT"}},
			"upload_token":        map[string]any{"type": "string"},
			"upload_token_header": map[string]any{"type": "string"},
			"expires_at":           map[string]any{"type": "string", "format": "date-time"},
			"archive":              map[string]any{"type": "string", "enum": []string{"zip"}},
			"entrypoint":           map[string]any{"type": "string"},
			"site_url":             map[string]any{"type": "string"},
			"limits": map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]any{
					"max_archive_bytes":  map[string]any{"type": "integer", "minimum": 1},
					"max_expanded_bytes": map[string]any{"type": "integer", "minimum": 1},
					"max_file_bytes":     map[string]any{"type": "integer", "minimum": 1},
					"max_files":          map[string]any{"type": "integer", "minimum": 1},
				},
				"required": []string{"max_archive_bytes", "max_expanded_bytes", "max_file_bytes", "max_files"},
			},
		},
		"required": []string{
			"site_id", "revision_id", "upload_id", "upload_path", "upload_url", "upload_method",
			"upload_token", "upload_token_header", "expires_at", "archive", "entrypoint", "site_url", "limits",
		},
	}
}

func multicaMCPGetStaticSiteOutputSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"site_id":            map[string]any{"type": "string"},
			"public_site_id":     map[string]any{"type": "string"},
			"status":             map[string]any{"type": "string"},
			"active_revision_id": map[string]any{"type": "string"},
			"latest_revision_id": map[string]any{"type": "string"},
			"latest_status":      map[string]any{"type": "string"},
			"latest_error":       map[string]any{"type": "string"},
			"created_at":         map[string]any{"type": "string", "format": "date-time"},
			"updated_at":         map[string]any{"type": "string", "format": "date-time"},
			"site_url":           map[string]any{"type": "string"},
		},
		"required": []string{
			"site_id", "public_site_id", "status", "latest_revision_id", "latest_status", "created_at", "updated_at", "site_url",
		},
	}
}

func (h *Handler) handleMulticaMCPStaticSiteCall(w http.ResponseWriter, r *http.Request, id json.RawMessage, toolName string, rawArguments json.RawMessage) {
	if h.SiteHosting == nil {
		h.writeMulticaMCPToolError(w, id, "static site hosting is unavailable")
		return
	}
	ownerUserID := strings.TrimSpace(r.Header.Get("X-User-ID"))
	if ownerUserID == "" {
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
			OwnerUserID: ownerUserID,
			SiteID: strings.TrimSpace(args.SiteID),
			ExpectedSHA256: args.ExpectedSHA256, ExpectedLength: args.ContentLength,
			Entrypoint: args.Entrypoint, SPAFallback: args.SPAFallback,
		})
	} else {
		var args multicaMCPGetStaticSiteArguments
		if decodeErr := decodeMulticaMCPArguments(rawArguments, &args); decodeErr != nil || strings.TrimSpace(args.SiteID) == "" {
			h.writeMulticaMCPError(w, id, -32602, "invalid get_static_site_deploy arguments")
			return
		}
		result, err = h.SiteHosting.GetStatus(r.Context(), strings.TrimSpace(args.SiteID), ownerUserID)
	}
	if err != nil {
		message := "static site operation failed"
		switch {
		case errors.Is(err, sitehosting.ErrUnavailable):
			message = "static site hosting is unavailable"
		case errors.Is(err, sitehosting.ErrSiteForbidden), errors.Is(err, sitehosting.ErrSiteNotFound):
			message = "static Site was not found or is not owned by this user"
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
