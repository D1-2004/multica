package handler

import (
	"errors"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/agentsource"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func (h *Handler) ExportAgent(w http.ResponseWriter, r *http.Request) {
	agent, ok := h.loadAgentForUser(w, r, chi.URLParam(r, "id"))
	if !ok || !h.canManageAgent(w, r, agent) { return }
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil { writeError(w, http.StatusInternalServerError, "failed to read agent configuration"); return }
	defer tx.Rollback(r.Context())
	if _, err := tx.Exec(r.Context(), "SET TRANSACTION ISOLATION LEVEL REPEATABLE READ, READ ONLY"); err != nil { writeError(w, http.StatusInternalServerError, "failed to read agent snapshot"); return }
	queries := h.Queries.WithTx(tx)
	agent, err = queries.GetAgent(r.Context(), agent.ID)
	if err != nil { writeError(w, http.StatusInternalServerError, "failed to read agent"); return }
	if !h.canManageAgent(w, r, agent) { return }
	assignments, err := queries.ListAgentSkillSummaries(r.Context(), agent.ID)
	if err != nil { writeError(w, http.StatusInternalServerError, "failed to read agent skills"); return }
	source, sourceErr := queries.GetAgentSourceByAgentID(r.Context(), agent.ID)
	if sourceErr != nil && !errors.Is(sourceErr, pgx.ErrNoRows) { writeError(w, http.StatusInternalServerError, "failed to read agent source"); return }
	mapped := map[string]string{}
	instructionsPath := "AGENTS.md"
	if sourceErr == nil {
		mappings, err := queries.ListAgentSourceSkills(r.Context(), source.ID)
		if err != nil { writeError(w, http.StatusInternalServerError, "failed to read source skills"); return }
		for _, mapping := range mappings { mapped[uuidToString(mapping.SkillID)] = mapping.SourcePath }
		preview, err := queries.LatestAppliedAgentSourcePreview(r.Context(), db.LatestAppliedAgentSourcePreviewParams{AgentID:agent.ID, WorkspaceID:agent.WorkspaceID})
		if err != nil && !errors.Is(err, pgx.ErrNoRows) { writeError(w, http.StatusInternalServerError, "failed to read source layout"); return }
		if err == nil {
			var snapshot agentsource.RepositorySnapshot
			if err := json.Unmarshal(preview.Snapshot, &snapshot); err != nil { writeError(w, http.StatusInternalServerError, "invalid source layout"); return }
			instructionsPath = snapshot.Definition.Manifest.Spec.Instructions
		}
	}
	if len(assignments) > agentsource.MaxSkills { writeError(w, http.StatusUnprocessableEntity, "agent exceeds the source skill limit"); return }
	skills := make([]agentsource.Skill, 0, len(assignments))
	for _, assignment := range assignments {
		skill, err := queries.GetSkillInWorkspace(r.Context(), db.GetSkillInWorkspaceParams{ID:assignment.ID, WorkspaceID:agent.WorkspaceID})
		if err != nil { writeError(w, http.StatusInternalServerError, "failed to read skill"); return }
		files, err := queries.ListSkillFiles(r.Context(), skill.ID)
		if err != nil { writeError(w, http.StatusInternalServerError, "failed to read skill files"); return }
		name := skill.Name
		skillPath := "workspace-skills/" + uuidToString(skill.ID)
		if sourcePath, ok := mapped[uuidToString(skill.ID)]; ok { name = strings.TrimSuffix(name, sourceManagedSkillName("", source.ID)); skillPath = sourcePath }
		// UUID directories avoid name/path collisions and remain stable on re-export.
		compiled := agentsource.Skill{SourcePath:skillPath, Name:name, Description:skill.Description, Content:skill.Content, Disabled:!assignment.Enabled, Files:[]agentsource.File{}}
		for _, file := range files { compiled.Files = append(compiled.Files, agentsource.File{Path:file.Path, Content:file.Content}) }
		skills = append(skills, compiled)
	}
	manifest, notes, err := buildAgentExportManifest(r.Context(), queries, agent, instructionsPath)
	if err != nil { writeError(w, http.StatusInternalServerError, "failed to read complete agent configuration"); return }
	if err := tx.Commit(r.Context()); err != nil { writeError(w, http.StatusInternalServerError, "failed to finish agent snapshot"); return }
	archive, err := agentsource.ExportAgentPackage(r.Context(), manifest, agent.Instructions, skills, notes)
	if err != nil { writeError(w, http.StatusUnprocessableEntity, err.Error()); return }
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="agent-%s.zip"`, uuidToString(agent.ID)))
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(archive)
}
