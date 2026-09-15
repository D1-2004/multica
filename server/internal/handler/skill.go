package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	skillpkg "github.com/multica-ai/multica/server/internal/skill"
	"github.com/multica-ai/multica/server/internal/gitrepo"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// sanitizeNullBytes makes a string safe for a PostgreSQL TEXT column.
//
// Two failure modes covered:
//   - Embedded NUL (0x00) — PG rejects with SQLSTATE 22021. Removed.
//   - Other invalid-UTF-8 byte sequences (e.g. 0x91 = Windows-1252 smart
//     quote, which crashed agent-template import of skills containing
//     Windows-encoded prose). `strings.ToValidUTF8` drops them.
//
// Name is kept for compatibility with the many call sites; the behaviour
// is a strict superset of the original.
func sanitizeNullBytes(s string) string {
	return strings.ToValidUTF8(strings.ReplaceAll(s, "\x00", ""), "")
}

// --- Response structs ---

type SkillResponse struct {
	ID          string  `json:"id"`
	WorkspaceID string  `json:"workspace_id"`
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Content     string  `json:"content"`
	Config      any     `json:"config"`
	CreatedBy   *string `json:"created_by"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
}

// SkillSummaryResponse is the list-endpoint shape: everything SkillResponse
// has except `content`. SKILL.md bodies routinely run 50–200KB and shipping
// them in list payloads bloats responses past CLI timeouts on high-latency
// links (GH multica-ai/multica#2174). Detail endpoints still return the full
// SkillResponse with content.
type SkillSummaryResponse struct {
	ID          string  `json:"id"`
	WorkspaceID string  `json:"workspace_id"`
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Config      any     `json:"config"`
	CreatedBy   *string `json:"created_by"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
	// Enabled is only populated for agent-scoped skill responses. Workspace
	// skill lists describe the skill itself, so they omit assignment state.
	Enabled *bool `json:"enabled,omitempty"`
	// ContentOmitted and FilesOmitted are always true here. They exist so a
	// client can tell "withheld by this endpoint" apart from "the skill is
	// empty": without a positive marker the absent `content` key coerces to
	// "" in most scripting languages (`row.get("content", "")`, `.content //
	// ""`), and a diffing caller reads that as a wiped body. Fetch
	// `GET /api/skills/{id}` for the SKILL.md body and
	// `GET /api/skills/{id}/files` for file contents before comparing
	// against local state. A missing key means "not returned here", never
	// "empty".
	ContentOmitted bool `json:"content_omitted"`
	FilesOmitted   bool `json:"files_omitted"`
}

// AgentSkillSummary is the still-narrower shape used for skills embedded in
// an Agent payload (`GET /api/agents`, `GET /api/agents/{id}`). The agent
// list batch query only joins enough columns to render the assignee chip in
// the UI; the standalone `/api/agents/{id}/skills` endpoint returns the full
// SkillSummaryResponse for callers that need the source/origin info.
type AgentSkillSummary struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Enabled     bool   `json:"enabled"`
}

// MarshalJSON stamps the same always-true omission markers onto the embedded
// agent-skill shape. They are emitted by the marshaller rather than declared
// as struct fields because AgentSkillSummary is built by plain composite
// literals in agent.go; a plain bool field would default to false there and
// tell clients the opposite of the truth. The alias indirection keeps future
// fields flowing through automatically and avoids recursing into this method.
func (s AgentSkillSummary) MarshalJSON() ([]byte, error) {
	type alias AgentSkillSummary
	return json.Marshal(struct {
		alias
		ContentOmitted bool `json:"content_omitted"`
		FilesOmitted   bool `json:"files_omitted"`
	}{alias: alias(s), ContentOmitted: true, FilesOmitted: true})
}

type SkillFileResponse struct {
	ID        string `json:"id"`
	SkillID   string `json:"skill_id"`
	Path      string `json:"path"`
	Content   string `json:"content"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

type SkillSearchCandidateResponse struct {
	Name         string  `json:"name"`
	URL          string  `json:"url"`
	Source       string  `json:"source"`
	Repo         *string `json:"repo"`
	InstallCount *int64  `json:"install_count"`
	GitHubStars  *int64  `json:"github_stars"`
	Description  string  `json:"description"`
}

type SkillWithFilesResponse struct {
	SkillResponse
	Files []SkillFileResponse `json:"files"`
}

type SkillImportResult struct {
	Status        string                  `json:"status"`
	Reason        string                  `json:"reason,omitempty"`
	Skill         *SkillWithFilesResponse `json:"skill,omitempty"`
	ExistingSkill *ExistingSkillIdentity  `json:"existing_skill,omitempty"`
}

type ExistingSkillIdentity struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	CreatedBy    string `json:"created_by,omitempty"`
	CanOverwrite bool   `json:"can_overwrite,omitempty"`
}

func writeSkillImportDuplicateConflict(w http.ResponseWriter, existing ExistingSkillIdentity) {
	writeJSON(w, http.StatusConflict, map[string]any{
		"error":          "a skill with this name already exists",
		"existing_skill": existing,
	})
}

func skillToResponse(s db.Skill) SkillResponse {
	return SkillResponse{
		ID:          uuidToString(s.ID),
		WorkspaceID: uuidToString(s.WorkspaceID),
		Name:        s.Name,
		Description: s.Description,
		Content:     s.Content,
		Config:      decodeSkillConfig(s.Config),
		CreatedBy:   uuidToPtr(s.CreatedBy),
		CreatedAt:   timestampToString(s.CreatedAt),
		UpdatedAt:   timestampToString(s.UpdatedAt),
	}
}

func (h *Handler) existingSkillIdentityByName(ctx context.Context, workspaceID pgtype.UUID, name string) (ExistingSkillIdentity, bool, error) {
	skill, err := h.Queries.GetSkillByWorkspaceAndName(ctx, db.GetSkillByWorkspaceAndNameParams{
		WorkspaceID: workspaceID,
		Name:        name,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ExistingSkillIdentity{}, false, nil
		}
		return ExistingSkillIdentity{}, false, err
	}
	return existingSkillIdentity(skill, ""), true, nil
}

func existingSkillIdentity(skill db.Skill, userID string) ExistingSkillIdentity {
	identity := ExistingSkillIdentity{
		ID:           uuidToString(skill.ID),
		Name:         skill.Name,
		CanOverwrite: canOverwriteSkillByLocalImport(userID, skill),
	}
	if skill.CreatedBy.Valid {
		identity.CreatedBy = uuidToString(skill.CreatedBy)
	}
	return identity
}

// decodeSkillConfig decodes a JSONB skill.config blob, defaulting to {} when
// missing or unparseable so the API surface always returns a JSON object.
func decodeSkillConfig(raw []byte) any {
	var config any
	if raw != nil {
		_ = json.Unmarshal(raw, &config)
	}
	if config == nil {
		return map[string]any{}
	}
	return config
}

func skillSummaryToResponse(
	id, workspaceID pgtype.UUID,
	name, description string,
	config []byte,
	createdBy pgtype.UUID,
	createdAt, updatedAt pgtype.Timestamptz,
) SkillSummaryResponse {
	return SkillSummaryResponse{
		ID:          uuidToString(id),
		WorkspaceID: uuidToString(workspaceID),
		Name:        name,
		Description: description,
		Config:      decodeSkillConfig(config),
		CreatedBy:   uuidToPtr(createdBy),
		CreatedAt:   timestampToString(createdAt),
		UpdatedAt:   timestampToString(updatedAt),
		// Constant for every list surface: this constructor feeds ListSkills,
		// ListAgentSkills and writeUpdatedAgentSkills, none of which SELECT
		// the content column or join skill_file.
		ContentOmitted: true,
		FilesOmitted:   true,
	}
}

func skillFileToResponse(f db.SkillFile) SkillFileResponse {
	return SkillFileResponse{
		ID:        uuidToString(f.ID),
		SkillID:   uuidToString(f.SkillID),
		Path:      f.Path,
		Content:   f.Content,
		CreatedAt: timestampToString(f.CreatedAt),
		UpdatedAt: timestampToString(f.UpdatedAt),
	}
}

// --- Request structs ---

type CreateSkillRequest struct {
	Name        string                   `json:"name"`
	Description string                   `json:"description"`
	Content     string                   `json:"content"`
	Config      any                      `json:"config"`
	Files       []CreateSkillFileRequest `json:"files,omitempty"`
}

type CreateSkillFileRequest struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type UpdateSkillRequest struct {
	Name        *string                  `json:"name"`
	Description *string                  `json:"description"`
	Content     *string                  `json:"content"`
	Config      any                      `json:"config"`
	Files       []CreateSkillFileRequest `json:"files,omitempty"`
}

type SetAgentSkillsRequest struct {
	SkillIDs []string `json:"skill_ids"`
}

type AddAgentSkillsRequest struct {
	SkillIDs []string `json:"skill_ids"`
}

// --- Helpers ---

// validateFilePath checks that a file path is safe (no traversal, no absolute paths).
func validateFilePath(p string) bool {
	if p == "" {
		return false
	}
	if filepath.IsAbs(p) {
		return false
	}
	cleaned := filepath.Clean(p)
	if strings.HasPrefix(cleaned, "..") {
		return false
	}
	return true
}

func (h *Handler) loadSkillForUser(w http.ResponseWriter, r *http.Request, id string) (db.Skill, bool) {
	workspaceID := h.resolveWorkspaceID(r)
	if workspaceID == "" {
		writeError(w, http.StatusBadRequest, "workspace_id is required")
		return db.Skill{}, false
	}

	skillUUID, ok := parseUUIDOrBadRequest(w, id, "skill id")
	if !ok {
		return db.Skill{}, false
	}

	skill, err := h.Queries.GetSkillInWorkspace(r.Context(), db.GetSkillInWorkspaceParams{
		ID:          skillUUID,
		WorkspaceID: parseUUID(workspaceID),
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "skill not found")
		return skill, false
	}
	return skill, true
}

// --- Skill CRUD ---

func (h *Handler) ListSkills(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)

	skills, err := h.Queries.ListSkillSummariesByWorkspace(r.Context(), parseUUID(workspaceID))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list skills")
		return
	}

	resp := make([]SkillSummaryResponse, len(skills))
	for i, s := range skills {
		resp[i] = skillSummaryToResponse(
			s.ID, s.WorkspaceID, s.Name, s.Description, s.Config,
			s.CreatedBy, s.CreatedAt, s.UpdatedAt,
		)
	}

	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) SearchSkills(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if query == "" {
		writeError(w, http.StatusBadRequest, "query is required")
		return
	}

	httpClient := &http.Client{Timeout: 30 * time.Second}
	candidates, err := searchClawHubSkills(httpClient, query)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"code":  "upstream_unavailable",
			"error": err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, candidates)
}

func (h *Handler) GetSkill(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	skill, ok := h.loadSkillForUser(w, r, id)
	if !ok {
		return
	}

	files, err := h.Queries.ListSkillFiles(r.Context(), skill.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list skill files")
		return
	}

	fileResps := make([]SkillFileResponse, len(files))
	for i, f := range files {
		fileResps[i] = skillFileToResponse(f)
	}

	writeJSON(w, http.StatusOK, SkillWithFilesResponse{
		SkillResponse: skillToResponse(skill),
		Files:         fileResps,
	})
}

func (h *Handler) CreateSkill(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)

	creatorID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}
	creatorUUID := parseUUID(creatorID)

	var req CreateSkillRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}

	for _, f := range req.Files {
		if !validateFilePath(f.Path) {
			writeError(w, http.StatusBadRequest, "invalid file path: "+f.Path)
			return
		}
	}

	resp, err := h.createSkillWithFiles(r.Context(), skillCreateInput{
		WorkspaceID: workspaceUUID,
		CreatorID:   creatorUUID,
		Name:        req.Name,
		Description: req.Description,
		Content:     req.Content,
		Config:      req.Config,
		Files:       req.Files,
	})
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "a skill with this name already exists")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to create skill: "+err.Error())
		return
	}
	actorType, actorID := h.resolveActor(r, creatorID, workspaceID)
	h.publish(protocol.EventSkillCreated, workspaceID, actorType, actorID, map[string]any{"skill": resp})
	writeJSON(w, http.StatusCreated, resp)
}

// canManageSkill checks whether the current user can update or delete a skill.
// The skill creator or workspace owner/admin can manage any skill.
func (h *Handler) canManageSkill(w http.ResponseWriter, r *http.Request, skill db.Skill) bool {
	wsID := uuidToString(skill.WorkspaceID)
	member, ok := h.requireWorkspaceRole(w, r, wsID, "skill not found", "owner", "admin", "member")
	if !ok {
		return false
	}
	if !canManageSkillForUser(member.Role, requestUserID(r), skill) {
		writeError(w, http.StatusForbidden, "only the skill creator can manage this skill")
		return false
	}
	return true
}

func canManageSkillForUser(role, userID string, skill db.Skill) bool {
	return roleAllowed(role,"owner","admin") || (skill.CreatedBy.Valid && uuidToString(skill.CreatedBy) == userID)
}

// canOverwriteSkillByLocalImport reports whether userID may overwrite skill via
// a runtime-local-skill re-import. This is intentionally NARROWER than
// canManageSkill: only the original creator may overwrite by re-importing.
// Workspace owners/admins who want to change a skill they did not create must
// edit it in-app instead. See MUL-2701 / MUL-2800.
func canOverwriteSkillByLocalImport(userID string, skill db.Skill) bool {
	return skill.CreatedBy.Valid && uuidToString(skill.CreatedBy) == userID
}

func (h *Handler) UpdateSkill(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	skill, ok := h.loadSkillForUser(w, r, id)
	if !ok {
		return
	}
	if !h.canManageSkill(w, r, skill) {
		return
	}
	if h.rejectSourceManagedSkillWrite(w, r, skill.ID) {
		return
	}

	var req UpdateSkillRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	for _, f := range req.Files {
		if !validateFilePath(f.Path) {
			writeError(w, http.StatusBadRequest, "invalid file path: "+f.Path)
			return
		}
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start transaction")
		return
	}
	defer tx.Rollback(r.Context())

	qtx := h.Queries.WithTx(tx)

	skill, err = qtx.GetSkillInWorkspaceForUpdate(r.Context(), db.GetSkillInWorkspaceForUpdateParams{ID:skill.ID, WorkspaceID:skill.WorkspaceID})
	if err != nil {
		writeError(w, http.StatusNotFound, "skill not found")
		return
	}
	params := db.UpdateSkillParams{
		ID: skill.ID,
	}
	if req.Name != nil {
		params.Name = pgtype.Text{String: sanitizeNullBytes(*req.Name), Valid: true}
	}
	if req.Description != nil {
		params.Description = pgtype.Text{String: sanitizeNullBytes(*req.Description), Valid: true}
	}
	if req.Content != nil {
		params.Content = pgtype.Text{String: sanitizeNullBytes(*req.Content), Valid: true}
	}
	if req.Config != nil {
		mapping, sourceErr := qtx.GetAgentSourceSkillBySkillID(r.Context(), skill.ID)
		if sourceErr != nil && !errors.Is(sourceErr, pgx.ErrNoRows) {
			writeError(w, http.StatusInternalServerError, "failed to verify skill source ownership"); return
		}
		if sourceErr == nil {
			configuration, ok := req.Config.(map[string]any)
			if !ok { writeError(w, http.StatusBadRequest, "exclusive skill config must be an object"); return }
			source, err := qtx.GetAgentSourceByID(r.Context(), mapping.AgentSourceID)
			if err != nil { writeError(w, http.StatusInternalServerError, "failed to load skill source"); return }
			configuration["exclusive_agent_id"] = uuidToString(source.AgentID)
			configuration["origin"] = map[string]any{
				"type":"git_agent_source", "repository":source.RepositoryUrl,
				"ref":source.Ref, "commit_sha":source.SyncedCommitSha, "path":mapping.SourcePath,
			}
		}
		config, _ := json.Marshal(req.Config)
		params.Config = config
	}

	skill, err = qtx.UpdateSkill(r.Context(), params)
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "a skill with this name already exists")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to update skill: "+err.Error())
		return
	}

	// If files are provided, replace all files.
	var fileResps []SkillFileResponse
	if req.Files != nil {
		if err := qtx.DeleteSkillFilesBySkill(r.Context(), skill.ID); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to delete old skill files")
			return
		}
		fileResps = make([]SkillFileResponse, 0, len(req.Files))
		for _, f := range req.Files {
			// SKILL.md is reserved for the primary skill content (skill.Content).
			if skillpkg.IsReservedContentPath(f.Path) {
				continue
			}
			sf, err := qtx.UpsertSkillFile(r.Context(), db.UpsertSkillFileParams{
				SkillID: skill.ID,
				Path:    sanitizeNullBytes(f.Path),
				Content: sanitizeNullBytes(f.Content),
			})
			if err != nil {
				writeError(w, http.StatusInternalServerError, "failed to upsert skill file: "+err.Error())
				return
			}
			fileResps = append(fileResps, skillFileToResponse(sf))
		}
	} else {
		files, _ := qtx.ListSkillFiles(r.Context(), skill.ID)
		fileResps = make([]SkillFileResponse, len(files))
		for i, f := range files {
			fileResps[i] = skillFileToResponse(f)
		}
	}

	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit")
		return
	}

	resp := SkillWithFilesResponse{
		SkillResponse: skillToResponse(skill),
		Files:         fileResps,
	}
	wsID := h.resolveWorkspaceID(r)
	actorType, actorID := h.resolveActor(r, requestUserID(r), wsID)
	h.publish(protocol.EventSkillUpdated, wsID, actorType, actorID, map[string]any{"skill": resp})
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) DeleteSkill(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	skill, ok := h.loadSkillForUser(w, r, id)
	if !ok {
		return
	}
	if !h.canManageSkill(w, r, skill) {
		return
	}
	if h.rejectSourceManagedSkillWrite(w, r, skill.ID) {
		return
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start transaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	if _, err := qtx.GetSkillInWorkspaceForUpdate(r.Context(), db.GetSkillInWorkspaceForUpdateParams{ID:skill.ID, WorkspaceID:skill.WorkspaceID}); err != nil {
		writeError(w, http.StatusNotFound, "skill not found")
		return
	}
	if err := qtx.DeleteSkillDependents(r.Context(), skill.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to remove skill dependents")
		return
	}
	if err := qtx.DeleteSkill(r.Context(), db.DeleteSkillParams{
		ID:          skill.ID,
		WorkspaceID: skill.WorkspaceID,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete skill")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit skill deletion")
		return
	}
	actorType, actorID := h.resolveActor(r, requestUserID(r), uuidToString(skill.WorkspaceID))
	h.publish(protocol.EventSkillDeleted, uuidToString(skill.WorkspaceID), actorType, actorID, map[string]any{"skill_id": uuidToString(skill.ID)})
	w.WriteHeader(http.StatusNoContent)
}

// --- Skill import ---

type ImportSkillRequest struct {
	ConnectionID string `json:"connection_id,omitempty"`
	Ref string `json:"ref,omitempty"`
	Path string `json:"path,omitempty"`
	URL        string `json:"url"`
	OnConflict string `json:"on_conflict,omitempty"`
}

const (
	importOnConflictFail      = "fail"
	importOnConflictOverwrite = "overwrite"
	importOnConflictRename    = "rename"
	importOnConflictSkip      = "skip"
)

const maxImportRenameAttempts = 50

func validImportOnConflict(strategy string) bool {
	switch strategy {
	case "", importOnConflictFail, importOnConflictOverwrite, importOnConflictRename, importOnConflictSkip:
		return true
	}
	return false
}

// Per-import bundle limits. These mirror the local-runtime importer so that
// URL imports cannot smuggle in payloads that the rest of the stack would
// reject. fetchRawFile enforces the per-file cap; importedSkill.addFile
// enforces the bundle-wide caps.
const (
	maxImportFileSize  = 1 << 20 // 1 MiB per file
	maxImportTotalSize = 8 << 20 // 8 MiB per import bundle (sum of supporting files)
	maxImportFileCount = 256     // max number of supporting files
)

// importedSkill holds the data extracted from an external source.
type importedSkill struct {
	name        string
	description string
	content     string // SKILL.md body
	files       []importedFile
	bundleSize  int            // running sum of file content bytes for cap enforcement
	origin      map[string]any // written into skill.config.origin so the UI can show provenance
}

type importedFile struct {
	path    string
	content string
}

// errImportCapExceeded marks an error caused by a per-file or per-bundle cap.
// Such errors must abort the import — silently dropping a file would otherwise
// produce an incomplete skill that looks valid to the user.
var errImportCapExceeded = errors.New("import cap exceeded")

// errImportSourceUnavailable marks a transient failure to read the upstream
// source (e.g. the GitHub tree API rate limiting). The import can't proceed
// safely but should be retried, so it maps to a retryable HTTP status rather
// than a permanent error.
var errImportSourceUnavailable = errors.New("import source temporarily unavailable")

// isCapError reports whether err is (or wraps) errImportCapExceeded.
func isCapError(err error) bool {
	return errors.Is(err, errImportCapExceeded)
}

// addFile appends a supporting file while enforcing the per-bundle caps. It
// returns an error when either the file count or aggregate byte budget would
// be exceeded so the caller fails the import instead of silently truncating.
//
// Binary files (images, fonts, archives) are silently skipped: their bytes
// can't survive a PG TEXT column (SQLSTATE 22021), and they're reference
// assets the agent never reads as text anyway. Logging the skip leaves a
// breadcrumb if a user expected one of these to import.
func (s *importedSkill) addFile(path, content string) error {
	if isLikelyBinaryFilePath(path) {
		slog.Info("skill import: skipping binary file", "path", path, "size", len(content))
		return nil
	}
	if len(s.files) >= maxImportFileCount {
		return fmt.Errorf("%w: import bundle exceeds %d file limit", errImportCapExceeded, maxImportFileCount)
	}
	if s.bundleSize+len(content) > maxImportTotalSize {
		return fmt.Errorf("%w: import bundle exceeds %d byte limit", errImportCapExceeded, maxImportTotalSize)
	}
	s.bundleSize += len(content)
	s.files = append(s.files, importedFile{path: path, content: content})
	return nil
}

// isLikelyBinaryFilePath reports whether the file's extension indicates a
// non-text payload. Conservative blacklist — extensions not on the list
// are assumed text and pass through. `sanitizeNullBytes` (called at PG
// insert time) is the second-line defence against any text file that
// turns out to have stray invalid-UTF-8 bytes.
func isLikelyBinaryFilePath(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case
		// images
		".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp", ".tiff", ".ico", ".heic",
		// fonts
		".ttf", ".otf", ".woff", ".woff2", ".eot",
		// archives
		".zip", ".gz", ".tar", ".bz2", ".7z", ".rar",
		// documents (binary office)
		".pdf", ".docx", ".xlsx", ".pptx", ".doc", ".xls", ".ppt",
		// media
		".mp3", ".mp4", ".wav", ".avi", ".mov", ".webm", ".m4a", ".flac",
		// compiled / executable
		".exe", ".dll", ".so", ".dylib", ".class", ".jar", ".wasm",
		// db / cache
		".db", ".sqlite", ".sqlite3", ".pyc":
		return true
	}
	return false
}

// --- ClawHub types ---

var clawHubAPIBase = "https://clawhub.ai/api/v1"

const clawHubSearchStatsLimit = 10

type clawhubSearchResponse struct {
	Results []clawhubSearchResult `json:"results"`
}

type clawhubSearchResult struct {
	Slug        string `json:"slug"`
	DisplayName string `json:"displayName"`
	Summary     string `json:"summary"`
	OwnerHandle string `json:"ownerHandle"`
}

type clawhubSkillStats struct {
	InstallsAllTime int64 `json:"installsAllTime"`
	InstallsCurrent int64 `json:"installsCurrent"`
}

type clawhubGetSkillResponse struct {
	Skill         clawhubSkill          `json:"skill"`
	LatestVersion *clawhubLatestVersion `json:"latestVersion"`
}

type clawhubSkill struct {
	Slug        string            `json:"slug"`
	DisplayName string            `json:"displayName"`
	Summary     string            `json:"summary"`
	Tags        map[string]string `json:"tags"`
	Stats       clawhubSkillStats `json:"stats"`
}

type clawhubLatestVersion struct {
	Version string `json:"version"`
}

type clawhubVersionDetailResponse struct {
	Version clawhubVersionDetail `json:"version"`
}

type clawhubVersionDetail struct {
	Version string             `json:"version"`
	Files   []clawhubFileEntry `json:"files"`
}

type clawhubFileEntry struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// --- URL detection ---

// importSource identifies where a URL points.
type importSource int

const (
	sourceClawHub importSource = iota
	sourceSkillsSh
	sourceGitRepo
)

// detectImportSource determines the source from a URL.
// Returns the source and a normalized URL (with scheme).
func detectImportSource(raw string) (importSource, string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, "", fmt.Errorf("empty URL")
	}

	if _, err := gitrepo.ParseAddress(raw); err == nil { return sourceGitRepo,raw,nil }
	normalized := raw
	if !strings.HasPrefix(normalized, "http://") && !strings.HasPrefix(normalized, "https://") {
		normalized = "https://" + normalized
	}

	parsed, err := url.Parse(normalized)
	if err != nil {
		return 0, "", fmt.Errorf("invalid URL: %w", err)
	}

	host := strings.ToLower(parsed.Hostname())
	switch {
	case host == "skills.sh" || host == "www.skills.sh":
		return sourceSkillsSh, normalized, nil
	case host == "clawhub.ai" || host == "www.clawhub.ai":
		return sourceClawHub, normalized, nil
	case host == "github.com" || host == "www.github.com" || host == "code.alibaba-inc.com" || host == "gitlab.alibaba-inc.com" || host == "code.aone.alibaba-inc.com" || host == "code-sc.aone.alibaba-inc.com":
		if _, err := gitrepo.ParseAddress(normalized); err != nil { return 0,"",err }; return sourceGitRepo, normalized, nil
	default:
		// If no host (bare slug), default to clawhub
		if !strings.Contains(raw, "/") || !strings.Contains(raw, ".") {
			return sourceClawHub, raw, nil
		}
		return 0, "", fmt.Errorf("unsupported source: %s (supported: clawhub.ai, skills.sh, GitHub and Alibaba Code repository URLs)", host)
	}
}

// --- ClawHub import ---

// parseClawHubSlug extracts the skill slug from a clawhub.ai URL.
func parseClawHubSlug(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid URL: %w", err)
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	// /{owner}/{slug} — take the last segment as the slug
	if len(parts) == 2 {
		return parts[1], nil
	}
	if len(parts) == 1 && parts[0] != "" {
		return parts[0], nil
	}
	// Bare slug (no path)
	if raw == parsed.Host || parsed.Path == "" || parsed.Path == "/" {
		return "", fmt.Errorf("missing skill slug in URL")
	}
	return "", fmt.Errorf("could not extract skill slug from URL: %s", raw)
}

func searchClawHubSkills(httpClient *http.Client, query string) ([]SkillSearchCandidateResponse, error) {
	searchURL := clawHubAPIBase + "/search?q=" + url.QueryEscape(query)
	resp, err := httpClient.Get(searchURL)
	if err != nil {
		return nil, fmt.Errorf("failed to reach ClawHub: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ClawHub search returned status %d", resp.StatusCode)
	}

	var searchResp clawhubSearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&searchResp); err != nil {
		return nil, fmt.Errorf("failed to parse ClawHub search response")
	}

	candidates := make([]SkillSearchCandidateResponse, 0, len(searchResp.Results))
	for i, result := range searchResp.Results {
		if result.Slug == "" {
			continue
		}
		candidate := SkillSearchCandidateResponse{
			Name:        result.DisplayName,
			URL:         buildClawHubSkillURL(result.OwnerHandle, result.Slug),
			Source:      "clawhub.ai",
			Description: result.Summary,
		}
		if candidate.Name == "" {
			candidate.Name = result.Slug
		}
		if i < clawHubSearchStatsLimit {
			if count, ok := fetchClawHubInstallCount(httpClient, result.Slug); ok {
				candidate.InstallCount = &count
			}
		}
		candidates = append(candidates, candidate)
	}
	return candidates, nil
}

func buildClawHubSkillURL(ownerHandle, slug string) string {
	if ownerHandle == "" {
		return "https://clawhub.ai/" + url.PathEscape(slug)
	}
	return "https://clawhub.ai/" + url.PathEscape(ownerHandle) + "/" + url.PathEscape(slug)
}

func fetchClawHubInstallCount(httpClient *http.Client, slug string) (int64, bool) {
	detailURL := clawHubAPIBase + "/skills/" + url.PathEscape(slug)
	resp, err := httpClient.Get(detailURL)
	if err != nil {
		slog.Warn("clawhub search: failed to fetch skill details", "slug", slug, "error", err)
		return 0, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		slog.Warn("clawhub search: skill details returned non-200", "slug", slug, "status", resp.StatusCode)
		return 0, false
	}
	var detail clawhubGetSkillResponse
	if err := json.NewDecoder(resp.Body).Decode(&detail); err != nil {
		slog.Warn("clawhub search: failed to parse skill details", "slug", slug, "error", err)
		return 0, false
	}
	if detail.Skill.Stats.InstallsAllTime > 0 {
		return detail.Skill.Stats.InstallsAllTime, true
	}
	return detail.Skill.Stats.InstallsCurrent, true
}

func fetchFromClawHub(ctx context.Context, httpClient *http.Client, rawURL string) (*importedSkill, error) {
	slug, err := parseClawHubSlug(rawURL)
	if err != nil {
		return nil, err
	}

	apiBase := clawHubAPIBase

	// 1. Fetch skill metadata
	skillReq, err := http.NewRequestWithContext(ctx, http.MethodGet, apiBase+"/skills/"+url.PathEscape(slug), nil)
	if err != nil {
		return nil, err
	}
	skillResp, err := httpClient.Do(skillReq)
	if err != nil {
		return nil, fmt.Errorf("failed to reach ClawHub: %w", err)
	}
	defer skillResp.Body.Close()

	if skillResp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("skill not found on ClawHub: %s", slug)
	}
	if skillResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ClawHub returned status %d", skillResp.StatusCode)
	}

	var chResp clawhubGetSkillResponse
	if err := json.NewDecoder(skillResp.Body).Decode(&chResp); err != nil {
		return nil, fmt.Errorf("failed to parse ClawHub response")
	}
	chSkill := chResp.Skill

	// 2. Determine latest version and fetch file list
	latestVersion := ""
	if v, ok := chSkill.Tags["latest"]; ok {
		latestVersion = v
	} else if chResp.LatestVersion != nil {
		latestVersion = chResp.LatestVersion.Version
	}

	var filePaths []string
	if latestVersion != "" {
		vURL := fmt.Sprintf("%s/skills/%s/versions/%s", apiBase, url.PathEscape(slug), url.PathEscape(latestVersion))
		vReq, verr := http.NewRequestWithContext(ctx, http.MethodGet, vURL, nil)
		if verr != nil {
			return nil, verr
		}
		vResp, err := httpClient.Do(vReq)
		if err == nil {
			defer vResp.Body.Close()
			if vResp.StatusCode == http.StatusOK {
				var vDetail clawhubVersionDetailResponse
				if err := json.NewDecoder(vResp.Body).Decode(&vDetail); err == nil {
					for _, f := range vDetail.Version.Files {
						filePaths = append(filePaths, f.Path)
					}
				}
			}
		}
	}

	// 3. Download each file
	result := &importedSkill{
		name:        chSkill.DisplayName,
		description: chSkill.Summary,
		origin: map[string]any{
			"type":       "clawhub",
			"source_url": rawURL,
			"slug":       slug,
		},
	}
	if result.name == "" {
		result.name = slug
	}

	for _, fp := range filePaths {
		fileURL := fmt.Sprintf("%s/skills/%s/file?path=%s", apiBase, url.PathEscape(slug), url.QueryEscape(fp))
		if latestVersion != "" {
			fileURL += "&version=" + url.QueryEscape(latestVersion)
		}
		body, err := fetchRawFile(ctx, httpClient, fileURL)
		if err != nil {
			// Cap violations must abort: silently dropping a file would
			// produce an incomplete bundle that looks valid. SKILL.md is
			// load-bearing, so any failure on it is fatal too.
			if isCapError(err) || fp == "SKILL.md" {
				return nil, fmt.Errorf("clawhub import: %s: %w", fp, err)
			}
			// A cancelled context (overall deadline / client disconnect) is
			// fatal for the same reason: skipping every remaining file would
			// persist a half-populated bundle as a success.
			if ctx.Err() != nil {
				return nil, fmt.Errorf("clawhub import: fetch aborted at %s: %w", fp, ctx.Err())
			}
			slog.Warn("clawhub import: file download failed", "path", fp, "error", err)
			continue
		}
		if fp == "SKILL.md" {
			result.content = string(body)
			continue
		}
		if err := result.addFile(fp, string(body)); err != nil {
			return nil, err
		}
	}

	if result.content == "" {
		return nil, fmt.Errorf("clawhub import: SKILL.md is empty or missing for %s", slug)
	}

	return result, nil
}

// --- skills.sh import ---

// parseSkillsShParts extracts owner, repo, skill-name from a skills.sh URL.
// URL format: https://skills.sh/{owner}/{repo}/{skill-name}
func parseSkillsShParts(raw string) (owner, repo, skillName string, err error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", "", "", fmt.Errorf("invalid URL: %w", err)
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) != 3 {
		return "", "", "", fmt.Errorf("expected URL format: skills.sh/{owner}/{repo}/{skill-name}, got: %s", parsed.Path)
	}
	return parts[0], parts[1], parts[2], nil
}

func fetchRawFile(ctx context.Context, httpClient *http.Client, fileURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fileURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxImportFileSize+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxImportFileSize {
		return nil, fmt.Errorf("%w: file exceeds %d byte limit", errImportCapExceeded, maxImportFileSize)
	}
	return body, nil
}


func skillImportConflictReason() string {
	return "a skill with this name already exists; use --on-conflict overwrite to replace it or --on-conflict rename to import a copy"
}

func (h *Handler) createImportedSkillWithName(ctx context.Context, workspaceID, creatorID pgtype.UUID, name string, imported *importedSkill, config map[string]any, files []CreateSkillFileRequest) (SkillWithFilesResponse, error) {
	return h.createSkillWithFiles(ctx, skillCreateInput{
		WorkspaceID: workspaceID,
		CreatorID:   creatorID,
		Name:        name,
		Description: imported.description,
		Content:     imported.content,
		Config:      config,
		Files:       files,
	})
}

func (h *Handler) createRenamedImportedSkill(ctx context.Context, workspaceID, creatorID pgtype.UUID, baseName string, imported *importedSkill, config map[string]any, files []CreateSkillFileRequest) (SkillWithFilesResponse, error) {
	for suffix := 2; suffix < maxImportRenameAttempts+2; suffix++ {
		candidate := fmt.Sprintf("%s-%d", baseName, suffix)
		resp, err := h.createImportedSkillWithName(ctx, workspaceID, creatorID, candidate, imported, config, files)
		if err == nil {
			return resp, nil
		}
		if !isUniqueViolation(err) {
			return SkillWithFilesResponse{}, err
		}
	}
	return SkillWithFilesResponse{}, fmt.Errorf("failed to find an available renamed skill name after %d attempts", maxImportRenameAttempts)
}

func skillImportOverwriteFailure(err error) (int, string) {
	switch {
	case errors.Is(err, errSkillOverwriteNotFound):
		return http.StatusConflict, "target skill no longer exists"
	case errors.Is(err, errSkillOverwriteForbidden):
		return http.StatusForbidden, "only the skill creator can overwrite this skill"
	case errors.Is(err, errSkillOverwriteNameMismatch):
		return http.StatusConflict, "target skill name no longer matches the imported skill"
	default:
		return http.StatusInternalServerError, "failed to overwrite skill: " + err.Error()
	}
}

// mergeImportedSkillConfig keeps execution policy owned by the workspace while
// refreshing importer-owned provenance. Re-importing a skill must not silently
// remove its Runtime capability boundary.
func mergeImportedSkillConfig(existingRaw []byte, imported map[string]any) map[string]any {
	merged := make(map[string]any, len(imported)+1)
	for key, value := range imported {
		merged[key] = value
	}
	var existing map[string]any
	if err := json.Unmarshal(existingRaw, &existing); err == nil {
		if execution, ok := existing["execution"]; ok {
			merged["execution"] = execution
		}
	}
	return merged
}

func (h *Handler) resolveImportSkillConflict(w http.ResponseWriter, r *http.Request, strategy string, workspaceID string, workspaceUUID, creatorUUID pgtype.UUID, creatorID string, name string, imported *importedSkill, config map[string]any, files []CreateSkillFileRequest, existing db.Skill) {
	existingInfo := existingSkillIdentity(existing, creatorID)
	switch strategy {
	case importOnConflictSkip:
		writeJSON(w, http.StatusOK, SkillImportResult{
			Status:        "skipped",
			Reason:        "a skill with this name already exists",
			ExistingSkill: &existingInfo,
		})
	case importOnConflictOverwrite:
		if !canOverwriteSkillByLocalImport(creatorID, existing) {
			writeJSON(w, http.StatusForbidden, SkillImportResult{
				Status:        "failed",
				Reason:        "only the skill creator can overwrite this skill",
				ExistingSkill: &existingInfo,
			})
			return
		}
		resp, err := h.overwriteSkillWithFiles(r.Context(), skillOverwriteInput{
			WorkspaceID:   workspaceUUID,
			TargetSkillID: existing.ID,
			UserID:        creatorID,
			ExpectedName:  name,
			Description:   imported.description,
			Content:       imported.content,
			Config:        config,
			Files:         files,
		})
		if err != nil {
			status, reason := skillImportOverwriteFailure(err)
			writeJSON(w, status, SkillImportResult{
				Status:        "failed",
				Reason:        reason,
				ExistingSkill: &existingInfo,
			})
			return
		}
		actorType, actorID := h.resolveActor(r, creatorID, workspaceID)
		h.publish(protocol.EventSkillUpdated, workspaceID, actorType, actorID, map[string]any{"skill": resp})
		writeJSON(w, http.StatusOK, SkillImportResult{Status: "updated", Skill: &resp})
	case importOnConflictRename:
		resp, err := h.createRenamedImportedSkill(r.Context(), workspaceUUID, creatorUUID, name, imported, config, files)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, SkillImportResult{
				Status:        "failed",
				Reason:        "failed to create renamed skill: " + err.Error(),
				ExistingSkill: &existingInfo,
			})
			return
		}
		actorType, actorID := h.resolveActor(r, creatorID, workspaceID)
		h.publish(protocol.EventSkillCreated, workspaceID, actorType, actorID, map[string]any{"skill": resp})
		writeJSON(w, http.StatusCreated, SkillImportResult{
			Status:        "created",
			Reason:        "renamed to avoid an existing skill",
			Skill:         &resp,
			ExistingSkill: &existingInfo,
		})
	default:
		writeJSON(w, http.StatusConflict, SkillImportResult{
			Status:        "conflict",
			Reason:        skillImportConflictReason(),
			ExistingSkill: &existingInfo,
		})
	}
}

// --- Import handler ---

func (h *Handler) ImportSkill(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)

	creatorID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}
	creatorUUID := parseUUID(creatorID)

	// An uploaded skill archive (.skill / .zip) arrives as multipart/form-data;
	// a hosted-URL import arrives as JSON. Both converge on the same create +
	// conflict tail via finishSkillImport.
	if isMultipartForm(r) {
		h.importSkillFromArchive(w, r, workspaceID, workspaceUUID, creatorUUID, creatorID)
		return
	}

	var req ImportSkillRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if !validImportOnConflict(req.OnConflict) {
		writeError(w, http.StatusBadRequest, "on_conflict must be one of: fail, overwrite, rename, skip")
		return
	}
	structuredResult := req.OnConflict != ""
	strategy := req.OnConflict
	if strategy == "" {
		strategy = importOnConflictFail
	}

	source, normalized, err := detectImportSource(req.URL)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	httpClient := &http.Client{Timeout: 30 * time.Second}

	// Bound the whole server-side fetch under an overall deadline that is
	// shorter than the reverse-proxy / CDN gateway timeout in front of the API.
	// If an upstream is slow, the import returns a clear error instead of the
	// proxy severing the connection with a 504, and a client disconnect cancels
	// the in-flight fetch instead of letting it run on orphaned.
	ctx, cancel := context.WithTimeout(r.Context(), importFetchTimeout)
	defer cancel()

	var imported *importedSkill
	switch source {
	case sourceClawHub:
		imported, err = fetchFromClawHub(ctx, httpClient, normalized)
	case sourceSkillsSh:
		imported, err = h.fetchRepositorySkill(ctx, workspaceUUID, req, source, normalized)
	case sourceGitRepo:
		imported, err = h.fetchRepositorySkill(ctx, workspaceUUID, req, source, normalized)
	}
	if err != nil {
		if source != sourceClawHub && !isCapError(err) && ctx.Err() == nil { writeGitRepoError(w,err); return }
		status, msg := importFetchErrorResponse(ctx, err)
		writeError(w, status, msg)
		return
	}

	h.finishSkillImport(w, r, workspaceID, workspaceUUID, creatorUUID, creatorID, strategy, structuredResult, imported)
}

// importFetchTimeout bounds the total time spent fetching a skill's files from
// the upstream source. It is deliberately below the reverse-proxy gateway
// timeout so a slow or oversized source surfaces as a readable API error rather
// than a proxy 504.
const importFetchTimeout = 45 * time.Second

// importFetchErrorResponse maps a fetch failure onto an HTTP status and message.
// Cap violations become 413 (the skill is too large to import), an exhausted
// deadline becomes 504, a transiently unavailable source becomes a retryable
// 503, and everything else stays 502.
func importFetchErrorResponse(ctx context.Context, err error) (int, string) {
	if isCapError(err) {
		return http.StatusRequestEntityTooLarge, err.Error()
	}
	if errors.Is(err, context.DeadlineExceeded) || ctx.Err() == context.DeadlineExceeded {
		return http.StatusGatewayTimeout, "skill import timed out fetching source files; the skill may be too large or the source too slow"
	}
	if errors.Is(err, errImportSourceUnavailable) {
		return http.StatusServiceUnavailable, err.Error()
	}
	return http.StatusBadGateway, err.Error()
}

// finishSkillImport runs the shared tail of every skill import — whether the
// bundle came from a hosted URL or an uploaded archive (.skill / .zip). It maps
// the extracted files onto CreateSkillFileRequest, records provenance into
// config.origin, and creates the skill, routing same-name collisions through
// the on_conflict strategy.
func (h *Handler) finishSkillImport(w http.ResponseWriter, r *http.Request, workspaceID string, workspaceUUID, creatorUUID pgtype.UUID, creatorID, strategy string, structuredResult bool, imported *importedSkill) {
	files := make([]CreateSkillFileRequest, 0, len(imported.files))
	for _, f := range imported.files {
		if !validateFilePath(f.path) {
			continue
		}
		files = append(files, CreateSkillFileRequest{
			Path:    f.path,
			Content: f.content,
		})
	}

	// Persist provenance into skill.config.origin so list/detail UI can show
	// "Imported from GitHub / ClawHub / Skills.sh" and link back to the source.
	config := map[string]any{}
	if imported.origin != nil {
		config["origin"] = imported.origin
	}
	name := sanitizeNullBytes(imported.name)

	if structuredResult {
		if existing, found, lerr := h.lookupSkillByName(r.Context(), workspaceUUID, name); lerr != nil {
			writeJSON(w, http.StatusInternalServerError, SkillImportResult{
				Status: "failed",
				Reason: "failed to check for existing skill: " + lerr.Error(),
			})
			return
		} else if found {
			h.resolveImportSkillConflict(w, r, strategy, workspaceID, workspaceUUID, creatorUUID, creatorID, name, imported, config, files, existing)
			return
		}
	}

	resp, err := h.createImportedSkillWithName(r.Context(), workspaceUUID, creatorUUID, name, imported, config, files)
	if err != nil {
		if isUniqueViolation(err) {
			if structuredResult {
				if existing, found, lerr := h.lookupSkillByName(r.Context(), workspaceUUID, name); lerr == nil && found {
					h.resolveImportSkillConflict(w, r, strategy, workspaceID, workspaceUUID, creatorUUID, creatorID, name, imported, config, files, existing)
					return
				}
			}
			if existing, found, findErr := h.existingSkillIdentityByName(r.Context(), workspaceUUID, name); findErr == nil && found {
				writeSkillImportDuplicateConflict(w, existing)
			} else {
				writeError(w, http.StatusConflict, "a skill with this name already exists")
			}
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to create skill: "+err.Error())
		return
	}
	actorType, actorID := h.resolveActor(r, creatorID, workspaceID)
	h.publish(protocol.EventSkillCreated, workspaceID, actorType, actorID, map[string]any{"skill": resp})
	if structuredResult {
		writeJSON(w, http.StatusCreated, SkillImportResult{Status: "created", Skill: &resp})
		return
	}
	writeJSON(w, http.StatusCreated, resp)
}

// --- Skill File endpoints ---

func (h *Handler) ListSkillFiles(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	skill, ok := h.loadSkillForUser(w, r, id)
	if !ok {
		return
	}

	files, err := h.Queries.ListSkillFiles(r.Context(), skill.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list skill files")
		return
	}

	resp := make([]SkillFileResponse, len(files))
	for i, f := range files {
		resp[i] = skillFileToResponse(f)
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) UpsertSkillFile(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	skill, ok := h.loadSkillForUser(w, r, id)
	if !ok {
		return
	}
	if !h.canManageSkill(w, r, skill) {
		return
	}
	if h.rejectSourceManagedSkillWrite(w, r, skill.ID) {
		return
	}

	var req CreateSkillFileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if !validateFilePath(req.Path) {
		writeError(w, http.StatusBadRequest, "invalid file path")
		return
	}
	if skillpkg.IsReservedContentPath(req.Path) {
		writeError(w, http.StatusBadRequest, "SKILL.md is reserved for the primary skill content")
		return
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil { writeError(w, http.StatusInternalServerError, "failed to start skill file update"); return }
	defer tx.Rollback(r.Context())
	queries := h.Queries.WithTx(tx)
	if _, err := queries.GetSkillInWorkspaceForUpdate(r.Context(), db.GetSkillInWorkspaceForUpdateParams{ID:skill.ID, WorkspaceID:skill.WorkspaceID}); err != nil {
		writeError(w, http.StatusNotFound, "skill not found"); return
	}
	sf, err := queries.UpsertSkillFile(r.Context(), db.UpsertSkillFileParams{
		SkillID: skill.ID,
		Path:    sanitizeNullBytes(req.Path),
		Content: sanitizeNullBytes(req.Content),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to upsert skill file: "+err.Error())
		return
	}

	if err := tx.Commit(r.Context()); err != nil { writeError(w, http.StatusInternalServerError, "failed to save skill file"); return }
	writeJSON(w, http.StatusOK, skillFileToResponse(sf))
}

func (h *Handler) DeleteSkillFile(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	skill, ok := h.loadSkillForUser(w, r, id)
	if !ok {
		return
	}
	if !h.canManageSkill(w, r, skill) {
		return
	}
	if h.rejectSourceManagedSkillWrite(w, r, skill.ID) {
		return
	}

	fileID := chi.URLParam(r, "fileId")
	fileUUID, ok := parseUUIDOrBadRequest(w, fileID, "file id")
	if !ok {
		return
	}
	// Verify the file belongs to the parent skill we just authorized — guards
	// against deleting a file owned by a different skill via the URL param.
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil { writeError(w, http.StatusInternalServerError, "failed to start skill file deletion"); return }
	defer tx.Rollback(r.Context())
	queries := h.Queries.WithTx(tx)
	if _, err := queries.GetSkillInWorkspaceForUpdate(r.Context(), db.GetSkillInWorkspaceForUpdateParams{ID:skill.ID, WorkspaceID:skill.WorkspaceID}); err != nil {
		writeError(w, http.StatusNotFound, "skill not found"); return
	}
	file, err := queries.GetSkillFile(r.Context(), fileUUID)
	if err != nil || uuidToString(file.SkillID) != uuidToString(skill.ID) {
		writeError(w, http.StatusNotFound, "skill file not found")
		return
	}
	if err := queries.DeleteSkillFile(r.Context(), file.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete skill file")
		return
	}
	if err := tx.Commit(r.Context()); err != nil { writeError(w, http.StatusInternalServerError, "failed to delete skill file"); return }
	w.WriteHeader(http.StatusNoContent)
}

// --- Agent-Skill junction ---

func (h *Handler) ListAgentSkills(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	agent, ok := h.loadAgentForUser(w, r, id)
	if !ok {
		return
	}

	skills, err := h.Queries.ListAgentSkillSummaries(r.Context(), agent.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list agent skills")
		return
	}

	resp := make([]SkillSummaryResponse, len(skills))
	for i, s := range skills {
		resp[i] = skillSummaryToResponse(
			s.ID, s.WorkspaceID, s.Name, s.Description, s.Config,
			s.CreatedBy, s.CreatedAt, s.UpdatedAt,
		)
		resp[i].Enabled = &s.Enabled
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) SetAgentSkills(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	agent, ok := h.loadAgentForUser(w, r, id)
	if !ok {
		return
	}
	if !h.canManageAgent(w, r, agent) {
		return
	}

	var req SetAgentSkillsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	skillUUIDs, ok := parseUUIDSliceOrBadRequest(w, req.SkillIDs, "skill_ids")
	if !ok {
		return
	}
	if !h.validateAgentSkillIDsInWorkspace(w, r, agent, skillUUIDs) {
		return
	}
	if !h.ensureSourceManagedSkillsIncluded(w, r, agent, skillUUIDs) {
		return
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start transaction")
		return
	}
	defer tx.Rollback(r.Context())

	qtx := h.Queries.WithTx(tx)

	if err := qtx.RemoveAllAgentSkills(r.Context(), agent.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to clear agent skills")
		return
	}

	for _, skillID := range skillUUIDs {
		if err := qtx.AddAgentSkill(r.Context(), db.AddAgentSkillParams{
			AgentID: agent.ID,
			SkillID: skillID,
		}); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to add agent skill: "+err.Error())
			return
		}
	}

	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit")
		return
	}

	h.writeUpdatedAgentSkills(w, r, agent)
}

func (h *Handler) AddAgentSkills(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	agent, ok := h.loadAgentForUser(w, r, id)
	if !ok {
		return
	}
	if !h.canManageAgent(w, r, agent) {
		return
	}

	var req AddAgentSkillsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	skillUUIDs, ok := parseUUIDSliceOrBadRequest(w, req.SkillIDs, "skill_ids")
	if !ok {
		return
	}
	if !h.validateAgentSkillIDsInWorkspace(w, r, agent, skillUUIDs) {
		return
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start transaction")
		return
	}
	defer tx.Rollback(r.Context())

	qtx := h.Queries.WithTx(tx)
	for _, skillID := range skillUUIDs {
		if err := qtx.AddAgentSkill(r.Context(), db.AddAgentSkillParams{
			AgentID: agent.ID,
			SkillID: skillID,
		}); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to add agent skill: "+err.Error())
			return
		}
	}

	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit")
		return
	}

	h.writeUpdatedAgentSkills(w, r, agent)
}

func (h *Handler) SetAgentSkillEnabled(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	agent, ok := h.loadAgentForUser(w, r, id)
	if !ok {
		return
	}
	if !h.canManageAgent(w, r, agent) {
		return
	}

	skillID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "skillId"), "skill_id")
	if !ok {
		return
	}
	var req struct {
		Enabled *bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Enabled == nil {
		writeError(w, http.StatusBadRequest, "enabled is required")
		return
	}
	rows, err := h.Queries.SetAgentSkillEnabled(r.Context(), db.SetAgentSkillEnabledParams{
		AgentID: agent.ID,
		SkillID: skillID,
		Enabled: *req.Enabled,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update agent skill")
		return
	}
	if rows == 0 {
		writeError(w, http.StatusNotFound, "agent skill not found")
		return
	}

	h.writeUpdatedAgentSkills(w, r, agent)
}

func (h *Handler) RemoveAgentSkill(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	agent, ok := h.loadAgentForUser(w, r, id)
	if !ok {
		return
	}
	if !h.canManageAgent(w, r, agent) {
		return
	}
	skillID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "skillId"), "skill_id")
	if !ok {
		return
	}
	if managed, err := h.isSourceManagedSkill(r.Context(), skillID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to verify skill source ownership")
		return
	} else if managed {
		writeError(w, http.StatusConflict, "source-managed skills cannot be removed manually")
		return
	}
	if err := h.Queries.RemoveAgentSkill(r.Context(), db.RemoveAgentSkillParams{
		AgentID: agent.ID,
		SkillID: skillID,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to remove agent skill")
		return
	}
	h.writeUpdatedAgentSkills(w, r, agent)
}

func (h *Handler) validateAgentSkillIDsInWorkspace(w http.ResponseWriter, r *http.Request, agent db.Agent, skillUUIDs []pgtype.UUID) bool {
	managedForAgent := map[string]struct{}{}
	if source, err := h.Queries.GetAgentSourceByAgentID(r.Context(), agent.ID); err == nil {
		mappings, err := h.Queries.ListAgentSourceSkills(r.Context(), source.ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to load source-managed skills")
			return false
		}
		for _, mapping := range mappings {
			managedForAgent[uuidToString(mapping.SkillID)] = struct{}{}
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusInternalServerError, "failed to load agent source")
		return false
	}
	seen := map[string]struct{}{}
	for _, skillID := range skillUUIDs {
		key := uuidToString(skillID)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		if _, err := h.Queries.GetSkillInWorkspace(r.Context(), db.GetSkillInWorkspaceParams{
			ID:          skillID,
			WorkspaceID: agent.WorkspaceID,
		}); err != nil {
			writeError(w, http.StatusNotFound, "skill not found")
			return false
		}
		if managed, err := h.isSourceManagedSkill(r.Context(), skillID); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to verify skill source ownership")
			return false
		} else if managed {
			if _, belongsToAgent := managedForAgent[key]; !belongsToAgent {
				writeError(w, http.StatusBadRequest, "source-managed skills cannot be attached to another agent")
				return false
			}
		}
	}
	return true
}

func (h *Handler) ensureSourceManagedSkillsIncluded(w http.ResponseWriter, r *http.Request, agent db.Agent, skillUUIDs []pgtype.UUID) bool {
	source, err := h.Queries.GetAgentSourceByAgentID(r.Context(), agent.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return true
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load agent source")
		return false
	}
	mappings, err := h.Queries.ListAgentSourceSkills(r.Context(), source.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load source-managed skills")
		return false
	}
	requested := make(map[string]struct{}, len(skillUUIDs))
	for _, skillID := range skillUUIDs {
		requested[uuidToString(skillID)] = struct{}{}
	}
	for _, mapping := range mappings {
		if _, ok := requested[uuidToString(mapping.SkillID)]; !ok {
			writeError(w, http.StatusConflict, "source-managed skills cannot be removed manually")
			return false
		}
	}
	return true
}

func (h *Handler) rejectSourceManagedSkillWrite(w http.ResponseWriter, r *http.Request, skillID pgtype.UUID) bool {
	mapping, err := h.Queries.GetAgentSourceSkillBySkillID(r.Context(), skillID)
	if errors.Is(err, pgx.ErrNoRows) { return false }
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to verify skill source ownership")
		return true
	}
	source, err := h.Queries.GetAgentSourceByID(r.Context(), mapping.AgentSourceID)
	if err != nil { writeError(w, http.StatusInternalServerError, "failed to verify skill source ownership"); return true }
	if source.ManagedSourceKey.Valid {
		writeError(w, http.StatusConflict, "this skill is managed by Multica")
		return true
	}
	return false
}

func (h *Handler) writeUpdatedAgentSkills(w http.ResponseWriter, r *http.Request, agent db.Agent) {
	skills, err := h.Queries.ListAgentSkillSummaries(r.Context(), agent.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list agent skills")
		return
	}

	resp := make([]SkillSummaryResponse, len(skills))
	for i, s := range skills {
		resp[i] = skillSummaryToResponse(
			s.ID, s.WorkspaceID, s.Name, s.Description, s.Config,
			s.CreatedBy, s.CreatedAt, s.UpdatedAt,
		)
		resp[i].Enabled = &s.Enabled
	}
	actorType, actorID := h.resolveActor(r, requestUserID(r), uuidToString(agent.WorkspaceID))
	h.publish(protocol.EventAgentStatus, uuidToString(agent.WorkspaceID), actorType, actorID, map[string]any{"agent_id": uuidToString(agent.ID), "skills": resp})
	writeJSON(w, http.StatusOK, resp)
}
