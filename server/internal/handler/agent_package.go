package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"sort"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/agentsource"
	"github.com/multica-ai/multica/server/internal/coordinatorcontract"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type AgentPackagePreviewResponse struct {
	CoordinatorContract *coordinatorcontract.Contract `json:"coordinator_contract"`
	Definition          map[string]any                `json:"definition"`
	PreviewID           string                        `json:"preview_id"`
	ExpiresAt           string                        `json:"expires_at"`
	Requirements        PackageRequirements           `json:"requirements"`
	ManifestVersion     string                        `json:"manifest_version"`
	PackageHash         string                        `json:"package_hash"`
	Name                string                        `json:"name"`
	Description         string                        `json:"description"`
	Instructions        string                        `json:"instructions"`
	Skills              []GitAgentSkillPreview     `json:"skills"`
	ManifestFields      []string                      `json:"manifest_fields"`
	ConfigurationFields []string                      `json:"configuration_fields"`
	Warnings            []string                      `json:"warnings"`
}

func (h *Handler) DownloadAgentSchema(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/schema+json")
	w.Header().Set("Content-Disposition", `attachment; filename="agent.schema.json"`)
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(agentsource.PortableSchema)
}

func (h *Handler) PreviewAgentPackage(w http.ResponseWriter, r *http.Request) {
	workspaceID := workspaceIDFromURL(r, "id")
	if _, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id"); !ok {
		return
	}
	if _, ok := h.requireWorkspaceRole(w, r, workspaceID, "workspace not found", "owner", "admin"); !ok {
		return
	}
	content, err := readAgentPackageUpload(w, r)
	if err != nil { writePackageUploadError(w, err); return }
	parsed, err := agentsource.ParseAgentPackage(r.Context(), content)
	if err != nil {
		writeAgentPackageValidationError(w, err)
		return
	}
	h.writeAgentPackagePreview(w, r, parseUUID(workspaceID), parsed, nil)
}

func (h *Handler) writeAgentPackagePreview(w http.ResponseWriter, r *http.Request, workspaceID pgtype.UUID, parsed agentsource.ParsedAgentPackage, files map[string]string) {
	bundle, err := parsed.Bundle()
	if err != nil { writeGitRepoError(w, err); return }
	resolved := preparedAgentSource{bundle:bundle, sha:bundle.Hash, snapshot:agentsource.RepositorySnapshot{Definition:bundle, Files:files}}
	preview, err := h.saveAgentSourcePreview(r, workspaceID, db.Agent{}, db.AgentSource{}, resolved, "")
	if err != nil { writeError(w, http.StatusInternalServerError, "failed to save package preview"); return }
	var header agentsource.PortableManifest
	encoded, err := json.Marshal(parsed.Manifest)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read parsed manifest")
		return
	}
	if err := json.Unmarshal(encoded, &header); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read parsed manifest")
		return
	}
	response := AgentPackagePreviewResponse{
		CoordinatorContract: bundle.CoordinatorContract,
		Definition:          packageDefinitionPreview(bundle), PreviewID: uuidToString(preview.ID), ExpiresAt: timestampToString(preview.ExpiresAt), Requirements: packageRequirements(bundle),
		ManifestVersion: header.Version, PackageHash: bundle.Hash, Name: header.Name, Description: header.Description,
		Instructions: parsed.Instructions, Skills: []GitAgentSkillPreview{}, ManifestFields: []string{}, ConfigurationFields: []string{}, Warnings: parsed.Warnings,
	}
	for name := range parsed.Manifest {
		response.ManifestFields = append(response.ManifestFields, name)
	}
	var configuration map[string]json.RawMessage
	if raw, exists := parsed.Manifest["configuration"]; exists {
		if err := json.Unmarshal(raw, &configuration); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to read parsed configuration")
			return
		}
	}
	for name := range configuration {
		response.ConfigurationFields = append(response.ConfigurationFields, name)
	}
	sort.Strings(response.ManifestFields)
	sort.Strings(response.ConfigurationFields)
	for _, skill := range parsed.Skills {
		response.Skills = append(response.Skills, GitAgentSkillPreview{SourcePath: skill.SourcePath, Name: skill.Name, Description: skill.Description, Enabled: !skill.Disabled, FileCount: len(skill.Files)})
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, response)
}

func readAgentPackageUpload(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	r.Body = http.MaxBytesReader(w, r.Body, agentsource.MaxAgentPackageSize+(64<<10))
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		return nil, sourceRequestError(http.StatusUnsupportedMediaType, "use application/zip or multipart/form-data")
	}
	var content []byte
	switch mediaType {
	case "application/zip":
		content, err = io.ReadAll(io.LimitReader(r.Body, agentsource.MaxAgentPackageSize+1))
	case "multipart/form-data":
		reader, multipartErr := r.MultipartReader()
		if multipartErr != nil {
			return nil, multipartErr
		}
		part, multipartErr := reader.NextPart()
		if multipartErr != nil {
			return nil, multipartErr
		}
		if part.FormName() != "file" || part.FileName() == "" {
			_ = part.Close()
			return nil, sourceRequestError(http.StatusBadRequest, "upload one ZIP file in the file field")
		}
		content, err = io.ReadAll(io.LimitReader(part, agentsource.MaxAgentPackageSize+1))
		// Do not drain an oversized compressed upload while closing the part.
		if len(content) > agentsource.MaxAgentPackageSize {
			return nil, sourceRequestError(http.StatusRequestEntityTooLarge, "Agent package exceeds the upload size limit")
		}
		if err != nil {
			return nil, err
		}
		if err := part.Close(); err != nil {
			return nil, err
		}
		if _, nextErr := reader.NextPart(); !errors.Is(nextErr, io.EOF) {
			if nextErr != nil {
				return nil, nextErr
			}
			return nil, sourceRequestError(http.StatusBadRequest, "upload exactly one ZIP file")
		}
	default:
		return nil, sourceRequestError(http.StatusUnsupportedMediaType, "use application/zip or multipart/form-data")
	}
	if err != nil {
		return nil, err
	}
	if len(content) == 0 {
		return nil, sourceRequestError(http.StatusBadRequest, "Agent package is empty")
	}
	if len(content) > agentsource.MaxAgentPackageSize {
		return nil, sourceRequestError(http.StatusRequestEntityTooLarge, "Agent package exceeds the upload size limit")
	}
	return content, nil
}

func writeManifestSchemaError(w http.ResponseWriter, err error) bool {
	var invalid *agentsource.ManifestSchemaError
	if !errors.As(err, &invalid) { return false }
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error":invalid.Error(), "code":"invalid_agent_manifest", "issues":invalid.Issues, "validation":invalid.Validation, "schema_url":"/api/agent-schema"})
	return true
}

func writeAgentPackageValidationError(w http.ResponseWriter, err error) {
	if writeManifestSchemaError(w, err) { return }
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error":err.Error(), "code":"invalid_agent_package", "schema_url":"/api/agent-schema"})
}

func writePackageUploadError(w http.ResponseWriter, err error) {
	var tooLarge *http.MaxBytesError
	var requestErr *gitSourceRequestError
	var syntax *json.SyntaxError
	switch {
	case errors.As(err, &tooLarge): writeError(w, http.StatusRequestEntityTooLarge, "Agent package exceeds the upload size limit")
	case errors.As(err, &requestErr): writeError(w, requestErr.status, requestErr.message)
	case errors.As(err, &syntax): writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid package JSON at byte %d: %s", syntax.Offset, syntax.Error()))
	default: writeError(w, http.StatusBadRequest, "failed to read Agent package upload: " + err.Error())
	}
}
