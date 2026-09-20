package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/internal/wsfs"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type filesystemRoot struct {
	Kind        string `json:"kind"`
	ID          string `json:"id,omitempty"`
	Provisioned bool   `json:"provisioned"`
	Access      string `json:"access,omitempty"`
}

func (h *Handler) filesystemStore() wsfs.Store {
	return wsfs.Store{DB: h.DB}
}

func googleUUID(u pgtype.UUID) uuid.UUID {
	if !u.Valid {
		return uuid.Nil
	}
	return uuid.UUID(u.Bytes)
}

func (h *Handler) filesystemActor(w http.ResponseWriter, r *http.Request) (uuid.UUID, dbMemberRole, bool) {
	parsed, err := util.ParseUUID(ctxWorkspaceID(r.Context()))
	if err != nil {
		writeError(w, http.StatusBadRequest, "workspace is required")
		return uuid.Nil, "", false
	}
	wsID := googleUUID(parsed)
	member, ok := h.workspaceMember(w, r, wsID.String())
	if !ok {
		return uuid.Nil, "", false
	}
	return wsID, dbMemberRole(member.Role), true
}

type dbMemberRole string

func humanSharedAccess(role dbMemberRole) string {
	if role == "owner" || role == "admin" {
		return wsfs.AccessWrite
	}
	return wsfs.AccessRead
}

func (h *Handler) GetWorkspaceFilesystemRoots(w http.ResponseWriter, r *http.Request) {
	wsID, role, ok := h.filesystemActor(w, r)
	if !ok {
		return
	}
	store := h.filesystemStore()
	_, err := store.GetBinding(r.Context(), wsID)
	provisioned := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusServiceUnavailable, "filesystem status is unavailable")
		return
	}
	roots := []filesystemRoot{{Kind: "shared", Provisioned: provisioned, Access: humanSharedAccess(role)}}
	ids, err := store.ListEmployeeFilesystemAgentIDs(r.Context(), wsID)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "filesystem status is unavailable")
		return
	}
	for _, id := range ids {
		agent, loadErr := h.Queries.GetAgent(r.Context(), util.MustParseUUID(id.String()))
		if loadErr != nil {
			continue
		}
		if uuidToString(agent.WorkspaceID) != wsID.String() || !h.canManageAgentFilesystem(r, agent) {
			continue
		}
		roots = append(roots, filesystemRoot{Kind: "agent", ID: id.String(), Provisioned: true, Access: wsfs.AccessWrite})
	}
	writeJSON(w, http.StatusOK, map[string]any{"roots": roots})
}

func (h *Handler) GetWorkspaceFilesystemEntries(w http.ResponseWriter, r *http.Request) {
	_, _, ok := h.filesystemActor(w, r)
	if !ok {
		return
	}
	if _, err := wsfs.JailRelPath(r.URL.Query().Get("path")); err != nil {
		writeErrorCode(w, http.StatusBadRequest, "filesystem_invalid_path", "path is outside the filesystem jail")
		return
	}
	root := r.URL.Query().Get("root")
	if root != "shared" && !validAgentRoot(root) {
		writeErrorCode(w, http.StatusBadRequest, "filesystem_invalid_root", "unknown filesystem root")
		return
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	limit := wsfs.ClampListLimit(atoiDefault(r.URL.Query().Get("limit"), wsfs.DefaultListLimit))
	if offset < 0 {
		offset = 0
	}
	_ = limit
	writeErrorCode(w, http.StatusServiceUnavailable, "filesystem_host_starting", "filesystem listing host is not running")
}

func (h *Handler) GetWorkspaceFilesystemGrants(w http.ResponseWriter, r *http.Request) {
	wsID, role, ok := h.filesystemActor(w, r)
	if !ok {
		return
	}
	if role != "owner" && role != "admin" {
		writeError(w, http.StatusForbidden, "only workspace owners and admins can read filesystem grants")
		return
	}
	grants, err := h.filesystemStore().ListGrants(r.Context(), wsID)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "filesystem grants are unavailable")
		return
	}
	if grants == nil {
		grants = []wsfs.Grant{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"grants": grants})
}

func (h *Handler) PutWorkspaceFilesystemGrant(w http.ResponseWriter, r *http.Request) {
	wsID, role, ok := h.filesystemActor(w, r)
	if !ok {
		return
	}
	if role != "owner" && role != "admin" {
		writeError(w, http.StatusForbidden, "only workspace owners and admins can update filesystem grants")
		return
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	var body struct {
		AgentID string `json:"agent_id"`
		Access  string `json:"access"`
	}
	if err := decoder.Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid filesystem grant")
		return
	}
	agentIDParsed, err := util.ParseUUID(body.AgentID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "agent_id is required")
		return
	}
	agentID := googleUUID(agentIDParsed)
	agent, loadOK := h.loadAgentForUser(w, r, body.AgentID)
	if !loadOK {
		return
	}
	if uuidToString(agent.WorkspaceID) != wsID.String() || googleUUID(agent.ID) != agentID {
		writeError(w, http.StatusNotFound, "agent not found")
		return
	}
	updatedByParsed, err := util.ParseUUID(requestUserID(r))
	if err != nil {
		writeError(w, http.StatusBadRequest, "user is required")
		return
	}
	grant, err := h.filesystemStore().PutGrant(r.Context(), wsID, agentID, googleUUID(updatedByParsed), body.Access)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid filesystem grant")
		return
	}
	writeJSON(w, http.StatusOK, grant)
}

func (h *Handler) canManageAgentFilesystem(r *http.Request, agent db.Agent) bool {
	member, ok := ctxMember(r.Context())
	if !ok {
		return false
	}
	return roleAllowed(member.Role, "owner", "admin") || uuidToString(agent.OwnerID) == requestUserID(r)
}

func validAgentRoot(root string) bool {
	const prefix = "agent:"
	if len(root) <= len(prefix) || root[:len(prefix)] != prefix {
		return false
	}
	_, err := util.ParseUUID(root[len(prefix):])
	return err == nil
}

func atoiDefault(s string, fallback int) int {
	if s == "" {
		return fallback
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return fallback
	}
	return n
}
