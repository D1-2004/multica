package handler

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/logger"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func (h *Handler) TransferSkillOwner(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	skill, ok := h.loadSkillForUser(w, r, id)
	if !ok {
		return
	}
	wsID := uuidToString(skill.WorkspaceID)
	member, ok := h.requireWorkspaceRole(w, r, wsID, "skill not found", "owner", "admin", "member")
	if !ok {
		return
	}
	if !canTransferOwnedBy(skill.CreatedBy, member) {
		writeError(w, http.StatusForbidden, "only the skill creator or a workspace admin can transfer this skill")
		return
	}
	ownerUUID, ok := h.resolveTransferOwnerTarget(w, r, skill.WorkspaceID)
	if !ok {
		return
	}
	if skill.CreatedBy.Valid && skill.CreatedBy.Bytes == ownerUUID.Bytes {
		writeJSON(w, http.StatusOK, skillToResponse(skill))
		return
	}
	updated, err := h.Queries.UpdateSkillOwner(r.Context(), db.UpdateSkillOwnerParams{
		ID:          skill.ID,
		CreatedBy:   ownerUUID,
		WorkspaceID: skill.WorkspaceID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusConflict, "skill state changed; please retry")
			return
		}
		slog.Warn("transfer skill owner failed", append(logger.RequestAttrs(r), "error", err, "skill_id", id)...)
		writeError(w, http.StatusInternalServerError, "failed to transfer skill owner")
		return
	}
	resp := skillToResponse(updated)
	userID := requestUserID(r)
	actorType, actorID := h.resolveActor(r, userID, wsID)
	h.publish(protocol.EventSkillUpdated, wsID, actorType, actorID, map[string]any{"skill": resp})
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) TransferSquadOwner(w http.ResponseWriter, r *http.Request) {
	squad, workspaceID, ok := h.loadSquadInWorkspace(w, r)
	if !ok {
		return
	}
	member, ok := h.requireWorkspaceMember(w, r, workspaceID, "workspace not found")
	if !ok {
		return
	}
	if !canTransferOwnedBy(squad.CreatorID, member) {
		writeError(w, http.StatusForbidden, "only the squad creator or a workspace admin can transfer this squad")
		return
	}
	ownerUUID, ok := h.resolveTransferOwnerTarget(w, r, squad.WorkspaceID)
	if !ok {
		return
	}
	if squad.CreatorID.Bytes == ownerUUID.Bytes {
		resp, err := h.squadToResponseWithPreview(r.Context(), squad)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to load squad member preview")
			return
		}
		writeJSON(w, http.StatusOK, resp)
		return
	}
	updated, err := h.Queries.UpdateSquadOwner(r.Context(), db.UpdateSquadOwnerParams{
		ID:          squad.ID,
		CreatorID:   ownerUUID,
		WorkspaceID: squad.WorkspaceID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusConflict, "squad state changed; please retry")
			return
		}
		slog.Warn("transfer squad owner failed", append(logger.RequestAttrs(r), "error", err, "squad_id", uuidToString(squad.ID))...)
		writeError(w, http.StatusInternalServerError, "failed to transfer squad owner")
		return
	}
	resp, err := h.squadToResponseWithPreview(r.Context(), updated)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load squad member preview")
		return
	}
	h.publish(protocol.EventSquadUpdated, workspaceID, "member", requestUserID(r), map[string]any{"squad": resp})
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) TransferAutopilotOwner(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	workspaceID := h.resolveWorkspaceID(r)
	autopilot, ok := h.loadAutopilotInWorkspace(w, r, id, workspaceID)
	if !ok {
		return
	}
	member, ok := h.requireWorkspaceMember(w, r, workspaceID, "workspace not found")
	if !ok {
		return
	}
	if !autopilotWriteByOwnership(autopilot, member) {
		writeError(w, http.StatusForbidden, "only the autopilot creator or a workspace admin can transfer this autopilot")
		return
	}
	ownerUUID, ok := h.resolveTransferOwnerTarget(w, r, autopilot.WorkspaceID)
	if !ok {
		return
	}
	if autopilot.CreatedByType == "member" && autopilot.CreatedByID.Bytes == ownerUUID.Bytes {
		writeJSON(w, http.StatusOK, autopilotToResponse(autopilot, nil))
		return
	}
	updated, err := h.Queries.UpdateAutopilotOwner(r.Context(), db.UpdateAutopilotOwnerParams{
		ID:          autopilot.ID,
		CreatedByID: ownerUUID,
		WorkspaceID: autopilot.WorkspaceID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusConflict, "autopilot state changed; please retry")
			return
		}
		slog.Warn("transfer autopilot owner failed", append(logger.RequestAttrs(r), "error", err, "autopilot_id", id)...)
		writeError(w, http.StatusInternalServerError, "failed to transfer autopilot owner")
		return
	}
	resp := autopilotToResponse(updated, nil)
	h.publish(protocol.EventAutopilotUpdated, workspaceID, "member", requestUserID(r), map[string]any{"autopilot": resp})
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) TransferRuntimeOwner(w http.ResponseWriter, r *http.Request) {
	runtimeID := chi.URLParam(r, "runtimeId")
	runtimeUUID, ok := parseUUIDOrBadRequest(w, runtimeID, "runtime_id")
	if !ok {
		return
	}
	rt, err := h.Queries.GetAgentRuntime(r.Context(), runtimeUUID)
	if err != nil {
		writeError(w, http.StatusNotFound, "runtime not found")
		return
	}
	wsID := uuidToString(rt.WorkspaceID)
	member, ok := h.requireWorkspaceMember(w, r, wsID, "runtime not found")
	if !ok {
		return
	}
	if !canTransferOwnedBy(rt.OwnerID, member) {
		writeError(w, http.StatusForbidden, "only the runtime owner or a workspace admin can transfer this runtime")
		return
	}
	ownerUUID, ok := h.resolveTransferOwnerTarget(w, r, rt.WorkspaceID)
	if !ok {
		return
	}
	if rt.OwnerID.Valid && rt.OwnerID.Bytes == ownerUUID.Bytes {
		writeJSON(w, http.StatusOK, runtimeToResponse(rt))
		return
	}
	updated, err := h.Queries.UpdateAgentRuntimeOwner(r.Context(), db.UpdateAgentRuntimeOwnerParams{
		ID:          rt.ID,
		OwnerID:     ownerUUID,
		WorkspaceID: rt.WorkspaceID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusConflict, "runtime state changed; please retry")
			return
		}
		slog.Warn("transfer runtime owner failed", append(logger.RequestAttrs(r), "error", err, "runtime_id", runtimeID)...)
		writeError(w, http.StatusInternalServerError, "failed to transfer runtime owner")
		return
	}
	h.publish(protocol.EventDaemonRegister, wsID, "member", uuidToString(member.UserID), map[string]any{
		"action": "update",
	})
	writeJSON(w, http.StatusOK, runtimeToResponse(updated))
}
