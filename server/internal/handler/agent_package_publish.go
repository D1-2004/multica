package handler

import (
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/agentsource"
)

func (h *Handler) previewAgentPackagePublication(w http.ResponseWriter, r *http.Request) {
	agent, source, ok := h.loadPackageSourceForManage(w, r)
	if !ok { return }
	if source.SourceType == "git" { writeError(w,http.StatusConflict,"Git Agents publish from their repository; select a branch, tag or commit"); return }
	content, err := readAgentPackageUpload(w, r)
	if err != nil { writePackageUploadError(w, err); return }
	parsed, err := agentsource.ParseAgentPackage(r.Context(), content)
	if err != nil { writeAgentPackageValidationError(w, err); return }
	bundle, err := parsed.Bundle()
	if err != nil { writeAgentPackageValidationError(w, err); return }
	resolved := preparedAgentSource{bundle:bundle, sha:bundle.Hash, snapshot:agentsource.RepositorySnapshot{Definition:bundle}}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil { writeError(w, http.StatusInternalServerError, "failed to read Agent configuration"); return }
	defer tx.Rollback(r.Context())
	q := h.Queries.WithTx(tx)
	agent, err = q.GetAgentForUpdate(r.Context(), agent.ID)
	if err != nil { writeError(w, http.StatusConflict, "Agent no longer exists"); return }
	if !h.canManageAgent(w, r, agent) { return }
	lockedSource, err := q.LockAgentSourceByAgentID(r.Context(), agent.ID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) { writeError(w, http.StatusInternalServerError, "failed to read Agent source"); return }
	if lockedSource.ID != source.ID || lockedSource.ManagedSourceKey.Valid { writeError(w, http.StatusConflict, "Agent source changed; preview again"); return }
	current, stateHash, err := sourceStateFiles(r.Context(), q, agent, lockedSource, bundle)
	if err != nil { writeAgentSourceDatabaseError(w,err); return }
	if err := tx.Commit(r.Context()); err != nil { writeError(w, http.StatusInternalServerError, "failed to read Agent configuration"); return }
	preview, err := h.saveAgentSourcePreview(r, agent.WorkspaceID, agent, lockedSource, resolved, stateHash)
	if err != nil { writeError(w, http.StatusInternalServerError, "failed to save package preview"); return }
	w.Header().Set("Cache-Control", "no-store")
	changes := diffPackageState(current, sourceDefinitionFiles(bundle))
	requirements, err := h.packageRequirementsForAgent(r.Context(),h.Queries,agent,requestUserID(r),bundle)
	if err != nil { writeAgentSourceDatabaseError(w,err); return }
	writeJSON(w, http.StatusOK, AgentSourceSyncPreviewResponse{
		Requirements: requirements, PreviewID:uuidToString(preview.ID), ExpiresAt:timestampToString(preview.ExpiresAt),
		BaseSHA:lockedSource.SyncedCommitSha, ResolvedSHA:bundle.Hash, GitChanges:[]agentsource.FileChange{}, ConfigurationChanges:changes,
		Warnings:bundle.Warnings, Changed:len(changes) > 0 || lockedSource.SyncedCommitSha != bundle.Hash,
	})
}
