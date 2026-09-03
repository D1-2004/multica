package handler

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type TransferOwnerRequest struct {
	OwnerID string `json:"owner_id"`
}

func canTransferOwnedBy(ownerID pgtype.UUID, member db.Member) bool {
	if roleAllowed(member.Role, "owner", "admin") {
		return true
	}
	return ownerID.Valid && uuidToString(ownerID) == uuidToString(member.UserID)
}

func (h *Handler) resolveTransferOwnerTarget(w http.ResponseWriter, r *http.Request, workspaceID pgtype.UUID) (pgtype.UUID, bool) {
	var req TransferOwnerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return pgtype.UUID{}, false
	}
	ownerUUID, ok := parseUUIDOrBadRequest(w, req.OwnerID, "owner_id")
	if !ok {
		return pgtype.UUID{}, false
	}
	target, err := h.Queries.GetMemberByUserAndWorkspace(r.Context(), db.GetMemberByUserAndWorkspaceParams{
		UserID:      ownerUUID,
		WorkspaceID: workspaceID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusBadRequest, "owner must be a current workspace member")
			return pgtype.UUID{}, false
		}
		writeError(w, http.StatusInternalServerError, "failed to verify owner membership")
		return pgtype.UUID{}, false
	}
	isServiceUser, err := h.isWorkspaceAccessServiceUser(r.Context(), target.UserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to verify owner membership")
		return pgtype.UUID{}, false
	}
	if isServiceUser {
		writeError(w, http.StatusBadRequest, "owner must be a human workspace member")
		return pgtype.UUID{}, false
	}
	return ownerUUID, true
}
