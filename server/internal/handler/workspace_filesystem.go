package handler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/dshhost"
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

func parseFilesystemRoot(root string) (uuid.UUID, bool) {
	if root == "shared" {
		return uuid.Nil, true
	}
	if !validAgentRoot(root) {
		return uuid.Nil, false
	}
	id, err := uuid.Parse(strings.TrimPrefix(root, "agent:"))
	if err != nil {
		return uuid.Nil, false
	}
	return id, true
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
	// The workbench catalog is always available. NAS binding is tracked
	// separately and does not block create/list for humans.
	_ = provisioned
	roots := []filesystemRoot{{Kind: "shared", Provisioned: true, Access: humanSharedAccess(role)}}
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
	wsID, _, ok := h.filesystemActor(w, r)
	if !ok {
		return
	}
	rel, err := wsfs.JailRelPath(r.URL.Query().Get("path"))
	if err != nil {
		writeErrorCode(w, http.StatusBadRequest, "filesystem_invalid_path", "path is outside the filesystem jail")
		return
	}
	root := r.URL.Query().Get("root")
	agentID, rootOK := parseFilesystemRoot(root)
	if !rootOK {
		writeErrorCode(w, http.StatusBadRequest, "filesystem_invalid_root", "unknown filesystem root")
		return
	}
	if agentID != uuid.Nil && !h.canAccessAgentFilesystem(w, r, wsID, agentID) {
		return
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	limit := wsfs.ClampListLimit(atoiDefault(r.URL.Query().Get("limit"), wsfs.DefaultListLimit))
	if offset < 0 {
		offset = 0
	}
	store := h.filesystemStore()
	if r.URL.Query().Get("recursive") == "1" {
		all, err := store.ListAll(r.Context(), wsID, agentID)
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "filesystem listing is unavailable")
			return
		}
		if len(all) > wsfs.MaxDirectorySize {
			writeErrorCode(w, http.StatusRequestEntityTooLarge, "filesystem_directory_too_large", "directory is too large to list")
			return
		}
		if offset > len(all) {
			offset = len(all)
		}
		end := offset + limit
		truncated := end < len(all)
		if end > len(all) {
			end = len(all)
		}
		var nextOffset any
		if truncated {
			nextOffset = end
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"root": root, "path": rel, "offset": offset, "limit": limit,
			"entries": all[offset:end], "count": end - offset, "truncated": truncated, "next_offset": nextOffset,
		})
		return
	}
	okDir, err := store.DirExists(r.Context(), wsID, agentID, rel)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "filesystem listing is unavailable")
		return
	}
	if !okDir {
		writeErrorCode(w, http.StatusNotFound, "filesystem_not_found", "directory not found")
		return
	}
	children, err := store.ListChildren(r.Context(), wsID, agentID, rel)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "filesystem listing is unavailable")
		return
	}
	page, truncated, next, err := wsfs.PageDirectory(children, offset, limit)
	if err != nil {
		writeErrorCode(w, http.StatusRequestEntityTooLarge, "filesystem_directory_too_large", "directory is too large to list")
		return
	}
	var nextOffset any
	if truncated {
		nextOffset = next
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"root":        root,
		"path":        rel,
		"offset":      offset,
		"limit":       limit,
		"entries":     page,
		"count":       len(page),
		"truncated":   truncated,
		"next_offset": nextOffset,
	})
}

func (h *Handler) PostWorkspaceFilesystemMkdir(w http.ResponseWriter, r *http.Request) {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	var body struct {
		Root string `json:"root"`
		Path string `json:"path"`
	}
	if err := decoder.Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid mkdir")
		return
	}
	wsID, agentID, ok := h.requireCatalogWrite(w, r, body.Root)
	if !ok {
		return
	}
	rel, err := wsfs.JailRelPath(body.Path)
	if err != nil || rel == "." {
		writeErrorCode(w, http.StatusBadRequest, "filesystem_invalid_path", "path is outside the filesystem jail")
		return
	}
	parent := wsfs.ParentPath(rel)
	name := rel
	if i := strings.LastIndex(rel, "/"); i >= 0 {
		name = rel[i+1:]
	}
	store := h.filesystemStore()
	createdBy, err := util.ParseUUID(requestUserID(r))
	if err != nil {
		writeError(w, http.StatusBadRequest, "user is required")
		return
	}
	if err := store.EnsureParents(r.Context(), wsID, agentID, googleUUID(createdBy), parent); err != nil {
		writeError(w, http.StatusBadGateway, "could not create directory")
		return
	}
	entry, err := store.InsertDir(r.Context(), wsID, agentID, googleUUID(createdBy), rel, name)
	if isUniqueViolation(err) {
		writeErrorCode(w, http.StatusConflict, "filesystem_exists", "path already exists")
		return
	}
	if err != nil {
		writeError(w, http.StatusBadGateway, "could not create directory")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"name": entry.Name, "path": entry.RelPath, "is_dir": true, "size_bytes": 0,
	})
}

func (h *Handler) PostWorkspaceFilesystemUpload(w http.ResponseWriter, r *http.Request) {
	if h.Storage == nil {
		writeError(w, http.StatusServiceUnavailable, "filesystem storage is unavailable")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize+1024)
	if err := r.ParseMultipartForm(maxUploadSize + 1024); err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "file is too large")
		return
	}
	root := r.FormValue("root")
	wsID, agentID, ok := h.requireCatalogWrite(w, r, root)
	if !ok {
		return
	}
	dirRel, err := wsfs.JailRelPath(r.FormValue("path"))
	if err != nil {
		writeErrorCode(w, http.StatusBadRequest, "filesystem_invalid_path", "path is outside the filesystem jail")
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "file is required")
		return
	}
	defer file.Close()
	filename := r.FormValue("filename")
	if filename == "" {
		filename = path.Base(header.Filename)
	}
	name, err := wsfs.JailFileName(filename)
	if err != nil {
		writeErrorCode(w, http.StatusBadRequest, "filesystem_invalid_path", "filename is invalid")
		return
	}
	rel := wsfs.JoinRel(dirRel, name)
	store := h.filesystemStore()
	data, err := io.ReadAll(io.LimitReader(file, maxUploadSize+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, "could not read file")
		return
	}
	if int64(len(data)) > maxUploadSize {
		writeError(w, http.StatusRequestEntityTooLarge, "file is too large")
		return
	}
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	id := uuid.New()
	key := fmt.Sprintf("wsfs/%s/%s", wsID.String(), id.String())
	contentType := http.DetectContentType(data)
	if _, err := h.Storage.Upload(r.Context(), key, data, contentType, name); err != nil {
		slog.Error("workspace filesystem upload failed", "error", err)
		writeError(w, http.StatusBadGateway, "upload failed")
		return
	}
	createdBy, err := util.ParseUUID(requestUserID(r))
	if err != nil {
		writeError(w, http.StatusBadRequest, "user is required")
		return
	}
	if err := store.EnsureParents(r.Context(), wsID, agentID, googleUUID(createdBy), dirRel); err != nil {
		h.Storage.Delete(r.Context(), key)
		writeError(w, http.StatusBadGateway, "could not create directory")
		return
	}
	entry, err := store.InsertFile(r.Context(), id, wsID, agentID, googleUUID(createdBy), rel, name, key, digest, int64(len(data)))
	if isUniqueViolation(err) {
		h.Storage.Delete(r.Context(), key)
		writeErrorCode(w, http.StatusConflict, "filesystem_exists", "path already exists")
		return
	}
	if err != nil {
		h.Storage.Delete(r.Context(), key)
		writeError(w, http.StatusBadGateway, "could not create file")
		return
	}
	if agentID == uuid.Nil && h.FCE2BLauncher != nil {
		if syncErr := h.FCE2BLauncher.SyncSharedFile(r.Context(), wsID, rel, data); syncErr != nil {
			slog.Error("workspace shared file was not written to NAS", "path", rel, "error", syncErr)
			_ = store.DeleteUnder(r.Context(), wsID, agentID, rel)
			h.Storage.Delete(r.Context(), key)
			writeError(w, http.StatusBadGateway, "could not write the shared disk")
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"name": entry.Name, "path": entry.RelPath, "is_dir": false,
		"size_bytes": entry.SizeBytes, "sha256": entry.SHA256,
	})
}

func (h *Handler) PostWorkspaceFilesystemRename(w http.ResponseWriter, r *http.Request) {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	var body struct {
		Root string `json:"root"`
		Path string `json:"path"`
		Name string `json:"name"`
	}
	if err := decoder.Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid rename")
		return
	}
	wsID, agentID, ok := h.requireCatalogWrite(w, r, body.Root)
	if !ok {
		return
	}
	oldRel, err := wsfs.JailRelPath(body.Path)
	if err != nil || oldRel == "." {
		writeErrorCode(w, http.StatusBadRequest, "filesystem_invalid_path", "path is outside the filesystem jail")
		return
	}
	newName, err := wsfs.JailFileName(body.Name)
	if err != nil {
		writeErrorCode(w, http.StatusBadRequest, "filesystem_invalid_path", "name is invalid")
		return
	}
	newRel := wsfs.JoinRel(wsfs.ParentPath(oldRel), newName)
	if newRel == oldRel {
		writeJSON(w, http.StatusOK, map[string]any{"path": newRel, "name": newName})
		return
	}
	store := h.filesystemStore()
	_, err = store.GetEntry(r.Context(), wsID, agentID, oldRel)
	if errors.Is(err, pgx.ErrNoRows) {
		writeErrorCode(w, http.StatusNotFound, "filesystem_not_found", "path not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "filesystem is unavailable")
		return
	}
	if _, err := store.GetEntry(r.Context(), wsID, agentID, newRel); err == nil {
		writeErrorCode(w, http.StatusConflict, "filesystem_exists", "path already exists")
		return
	} else if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusServiceUnavailable, "filesystem is unavailable")
		return
	}
	if err := store.Rename(r.Context(), wsID, agentID, oldRel, newRel, newName); err != nil {
		writeError(w, http.StatusBadGateway, "could not rename")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": newRel, "name": newName})
}

func (h *Handler) DeleteWorkspaceFilesystemEntry(w http.ResponseWriter, r *http.Request) {
	root := r.URL.Query().Get("root")
	wsID, agentID, ok := h.requireCatalogWrite(w, r, root)
	if !ok {
		return
	}
	rel, err := wsfs.JailRelPath(r.URL.Query().Get("path"))
	if err != nil || rel == "." {
		writeErrorCode(w, http.StatusBadRequest, "filesystem_invalid_path", "path is outside the filesystem jail")
		return
	}
	store := h.filesystemStore()
	keys, err := store.StorageKeysUnder(r.Context(), wsID, agentID, rel)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "filesystem is unavailable")
		return
	}
	if err := store.DeleteUnder(r.Context(), wsID, agentID, rel); err != nil {
		writeError(w, http.StatusBadGateway, "could not delete")
		return
	}
	if h.Storage != nil {
		for _, key := range keys {
			h.Storage.Delete(r.Context(), key)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) GetWorkspaceFilesystemContent(w http.ResponseWriter, r *http.Request) {
	wsID, _, ok := h.filesystemActor(w, r)
	if !ok {
		return
	}
	if h.Storage == nil {
		writeError(w, http.StatusServiceUnavailable, "filesystem storage is unavailable")
		return
	}
	root := r.URL.Query().Get("root")
	agentID, rootOK := parseFilesystemRoot(root)
	if !rootOK {
		writeErrorCode(w, http.StatusBadRequest, "filesystem_invalid_root", "unknown filesystem root")
		return
	}
	if agentID != uuid.Nil && !h.canAccessAgentFilesystem(w, r, wsID, agentID) {
		return
	}
	rel, err := wsfs.JailRelPath(r.URL.Query().Get("path"))
	if err != nil || rel == "." {
		writeErrorCode(w, http.StatusBadRequest, "filesystem_invalid_path", "path is outside the filesystem jail")
		return
	}
	entry, err := h.filesystemStore().GetEntry(r.Context(), wsID, agentID, rel)
	if errors.Is(err, pgx.ErrNoRows) {
		writeErrorCode(w, http.StatusNotFound, "filesystem_not_found", "file not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "filesystem is unavailable")
		return
	}
	if entry.IsDir || entry.StorageKey == "" {
		writeErrorCode(w, http.StatusBadRequest, "filesystem_invalid_path", "path is not a file")
		return
	}
	if entry.SizeBytes > maxUploadSize {
		writeError(w, http.StatusRequestEntityTooLarge, "file is too large")
		return
	}
	reader, err := h.Storage.GetReader(r.Context(), entry.StorageKey)
	if err != nil {
		writeError(w, http.StatusBadGateway, "could not read file")
		return
	}
	defer reader.Close()
	ctype := extContentTypes[strings.ToLower(path.Ext(entry.Name))]
	if ctype == "" {
		ctype = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ctype)
	disposition := "attachment"
	if r.URL.Query().Get("inline") == "1" {
		disposition = "inline"
	}
	w.Header().Set("Content-Disposition", disposition+`; filename="`+strings.ReplaceAll(entry.Name, `"`, "")+`"`)
	w.Header().Set("Content-Length", strconv.FormatInt(entry.SizeBytes, 10))
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, io.LimitReader(reader, entry.SizeBytes))
}

func (h *Handler) requireCatalogWrite(w http.ResponseWriter, r *http.Request, root string) (uuid.UUID, uuid.UUID, bool) {
	wsID, role, ok := h.filesystemActor(w, r)
	if !ok {
		return uuid.Nil, uuid.Nil, false
	}
	agentID, rootOK := parseFilesystemRoot(root)
	if !rootOK {
		writeErrorCode(w, http.StatusBadRequest, "filesystem_invalid_root", "unknown filesystem root")
		return uuid.Nil, uuid.Nil, false
	}
	if agentID == uuid.Nil {
		if role != "owner" && role != "admin" {
			writeErrorCode(w, http.StatusForbidden, "filesystem_write_denied", "only workspace owners and admins can write shared files")
			return uuid.Nil, uuid.Nil, false
		}
		return wsID, uuid.Nil, true
	}
	if !h.canAccessAgentFilesystem(w, r, wsID, agentID) {
		return uuid.Nil, uuid.Nil, false
	}
	return wsID, agentID, true
}

func (h *Handler) canAccessAgentFilesystem(w http.ResponseWriter, r *http.Request, wsID, agentID uuid.UUID) bool {
	agent, loadOK := h.loadAgentForUser(w, r, agentID.String())
	if !loadOK {
		return false
	}
	if uuidToString(agent.WorkspaceID) != wsID.String() || googleUUID(agent.ID) != agentID {
		writeError(w, http.StatusNotFound, "agent not found")
		return false
	}
	if !h.canManageAgentFilesystem(r, agent) {
		writeErrorCode(w, http.StatusForbidden, "filesystem_write_denied", "cannot manage this agent filesystem")
		return false
	}
	return true
}

func (h *Handler) GetWorkspaceFilesystemGrants(w http.ResponseWriter, r *http.Request) {
	wsID, _, ok := h.filesystemActor(w, r)
	if !ok {
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
	if (grant.Access == wsfs.AccessRead || grant.Access == wsfs.AccessWrite) && h.FCE2BLauncher != nil && h.FCE2BLauncher.PrepareWorkspaceMount != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
		defer cancel()
		var employee *dshhost.Host
		if host, hostErr := (dshhost.PostgresStore{DB: h.DB}).Get(ctx, dshhost.Key{WorkspaceID: wsID, AgentID: agentID}); hostErr == nil {
			employee = &host
		}
		if _, prepErr := h.FCE2BLauncher.PrepareWorkspaceMount(ctx, h.DB, wsID, agentID, employee); prepErr != nil {
			slog.Warn("workspace filesystem provision after grant is pending",
				"error", prepErr,
				"workspace_id", wsID,
				"agent_id", agentID,
			)
		}
		if grant.Access == wsfs.AccessRead || grant.Access == wsfs.AccessWrite {
			if syncErr := h.FCE2BLauncher.SyncSharedCatalog(ctx, wsID); syncErr != nil {
				slog.Warn("workspace shared catalog was not written to NAS",
					"error", syncErr,
					"workspace_id", wsID,
					"agent_id", agentID,
				)
			}
		}
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
