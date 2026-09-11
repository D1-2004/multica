package handler

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/agentsource"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type AgentPackageDraftRequest struct {
	Manifest json.RawMessage `json:"manifest"`
	Files map[string]string `json:"files"`
}

func agentBuilderPackageInstructions() string {
	return agentBuilderInstructions + "\n\nAuthoritative Agent JSON Schema:\n" + string(agentsource.PortableSchema)
}

// Only the creator's hidden Builder and an explicit new-client protocol marker
// activate this upgrade. Ordinary Agent instructions are never changed here.
func (h *Handler) refreshBuilderPackageContract(r *http.Request, agent db.Agent, content string) (db.Agent, error) {
	if !isAgentBuilderCarrier(agent) || uuidToString(agent.OwnerID) != requestUserID(r) || !strings.HasPrefix(content, "MULTICA_AGENT_BUILDER_INPUT\n") { return agent, nil }
	var input struct { Protocol string `json:"protocol"` }
	if json.Unmarshal([]byte(strings.TrimPrefix(content, "MULTICA_AGENT_BUILDER_INPUT\n")), &input) != nil || input.Protocol != "multica.agent-package/v1" { return agent, nil }
	instructions := agentBuilderPackageInstructions()
	if agent.Instructions == instructions { return agent, nil }
	return h.Queries.UpdateAgent(r.Context(), db.UpdateAgentParams{ID:agent.ID, Instructions:pgtype.Text{String:instructions, Valid:true}})
}

func (h *Handler) PrepareAgentPackage(w http.ResponseWriter, r *http.Request) {
	workspaceID := workspaceIDFromURL(r, "id")
	id, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id"); if !ok { return }
	if _, ok := h.requireWorkspaceRole(w, r, workspaceID, "workspace not found", "owner", "admin"); !ok { return }
	r.Body = http.MaxBytesReader(w, r.Body, 64 << 20)
	var request AgentPackageDraftRequest
	decoder := json.NewDecoder(r.Body); decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil { writePackageUploadError(w, err); return }
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) { writeError(w, http.StatusBadRequest, "package draft must contain exactly one JSON object"); return }
	archive, err := agentsource.BuildAgentPackage(r.Context(), request.Manifest, request.Files)
	if err != nil { writeAgentPackageValidationError(w, err); return }
	parsed, err := agentsource.ParseAgentPackage(r.Context(), archive)
	if err != nil { writeAgentPackageValidationError(w, err); return }
	files := map[string]string{agentsource.PortableManifestPath:string(request.Manifest)}
	for path, content := range request.Files { files[path] = content }
	h.writeAgentPackagePreview(w, r, id, parsed, files)
}

// DownloadPreparedAgentPackage downloads the actor-bound immutable draft, before
// secret resolution, so a Builder result can also be uploaded or put in Git.
func (h *Handler) DownloadPreparedAgentPackage(w http.ResponseWriter, r *http.Request) {
	workspaceID := workspaceIDFromURL(r, "id")
	id, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id"); if !ok { return }
	if _, ok := h.requireWorkspaceRole(w, r, workspaceID, "workspace not found", "owner", "admin"); !ok { return }
	preview, err := h.readAgentSourcePreview(r, id, chi.URLParam(r, "previewId"))
	if err != nil { writeGitHubSourceError(w, err); return }
	resolved, err := h.resolveAgentSourcePreview(r.Context(), preview)
	if err != nil { writeGitHubSourceError(w, err); return }
	manifest, exists := resolved.snapshot.Files[agentsource.PortableManifestPath]
	if !exists { writeError(w, http.StatusBadRequest, "this preview has no downloadable authored package"); return }
	files := map[string]string{}
	for path, content := range resolved.snapshot.Files { if path != agentsource.PortableManifestPath { files[path] = content } }
	archive, err := agentsource.BuildAgentPackage(r.Context(), json.RawMessage(manifest), files)
	if err != nil { writeAgentPackageValidationError(w, err); return }
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="agent.zip"`)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(archive)
}
