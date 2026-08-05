package handler

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const dtaLoadSmokeMetadataKind = "dta_load_smoke"

var (
	dtaLoadSmokeMarkerPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,159}$`)
	dtaLoadSmokeSkillPattern  = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9._-]{0,62}[a-z0-9])?$`)
)

type dtaLoadSmokeRequest struct {
	AgentID        string   `json:"agent_id"`
	Marker         string   `json:"marker"`
	RequiredSkills []string `json:"required_skills"`
}

type dtaLoadSmokeRetryRequest struct {
	Reason string `json:"reason"`
}

type dtaLoadSmokeMetadata struct {
	Kind        string `json:"kind"`
	SubjectID   string `json:"subject_id"`
	TokenID     string `json:"token_id,omitempty"`
	AgentID     string `json:"agent_id"`
	Marker      string `json:"marker"`
	RequestHash string `json:"request_hash"`
	Schema      string `json:"schema"`
}

// CreateDTALoadSmoke creates the one narrow Issue shape used to verify that a
// deployed Agent can load its required Skills. Callers cannot supply arbitrary
// Issue fields, descriptions, mentions, or tool instructions.
func (h *Handler) CreateDTALoadSmoke(w http.ResponseWriter, r *http.Request) {
	principal, isToken := middleware.WorkspaceAccessPrincipalFromContext(r.Context())
	if !isToken {
		writeError(w, http.StatusForbidden, "workspace_access_operation_not_allowed")
		return
	}
	var request dtaLoadSmokeRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	request.AgentID = strings.TrimSpace(request.AgentID)
	request.Marker = strings.TrimSpace(request.Marker)
	if !dtaLoadSmokeMarkerPattern.MatchString(request.Marker) {
		writeError(w, http.StatusBadRequest, "marker contains unsupported characters")
		return
	}
	if len(request.RequiredSkills) > 100 {
		writeError(w, http.StatusBadRequest, "required_skills must contain at most 100 entries")
		return
	}
	seenSkills := make(map[string]struct{}, len(request.RequiredSkills))
	for _, name := range request.RequiredSkills {
		if !dtaLoadSmokeSkillPattern.MatchString(name) {
			writeError(w, http.StatusBadRequest, "required_skills contains an invalid name")
			return
		}
		if _, exists := seenSkills[name]; exists {
			writeError(w, http.StatusBadRequest, "required_skills contains a duplicate name")
			return
		}
		seenSkills[name] = struct{}{}
	}
	if request.RequiredSkills == nil {
		request.RequiredSkills = []string{}
	}

	workspaceID := h.resolveWorkspaceID(r)
	workspaceUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}
	agentUUID, ok := parseUUIDOrBadRequest(w, request.AgentID, "agent_id")
	if !ok {
		return
	}
	agent, err := h.Queries.GetAgent(r.Context(), agentUUID)
	if err != nil || uuidToString(agent.WorkspaceID) != workspaceID || agent.ArchivedAt.Valid {
		writeError(w, http.StatusNotFound, "agent not found")
		return
	}
	if uuidToString(agent.OwnerID) != principal.UserID {
		writeError(w, http.StatusForbidden, "workspace_access_resource_not_allowed")
		return
	}

	subjectID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	requestHash := dtaLoadSmokeRequestHash(request.RequiredSkills)
	lookup := db.GetDTALoadSmokeByOperationParams{
		WorkspaceID: workspaceUUID,
		TokenID:     principal.TokenID,
		AgentID:     request.AgentID,
		Marker:      request.Marker,
	}
	if existing, lookupErr := h.Queries.GetDTALoadSmokeByOperation(r.Context(), lookup); lookupErr == nil {
		h.writeDTALoadSmokeReplay(w, r, existing, lookup, requestHash)
		return
	} else if !isNotFound(lookupErr) {
		writeError(w, http.StatusInternalServerError, "failed to resolve load smoke operation")
		return
	}
	metadata := dtaLoadSmokeMetadata{
		Kind: dtaLoadSmokeMetadataKind, SubjectID: subjectID, AgentID: request.AgentID,
		Marker: request.Marker, RequestHash: requestHash, Schema: "dta-multica-load-smoke@2",
	}
	metadata.TokenID = principal.TokenID
	metadataJSON, _ := json.Marshal(metadata)
	expectedResponse, _ := json.Marshal(map[string]any{
		"schema": "dta-multica-load-smoke@2", "marker": request.Marker,
		"loaded": request.RequiredSkills,
	})
	description := strings.Join([]string{
		"DTA_MULTICA_LOAD_SMOKE@2",
		"marker=" + request.Marker,
		"required_skills=" + string(mustJSON(request.RequiredSkills)),
		"This is a deployment control-plane verification task.",
		"Load every required skill above through the Host skill mechanism, in the listed order.",
		"Do not call DWS, network, or business tools. Only the Host Issue control-plane calls needed to read this Issue and persist the exact result are exempt.",
		"Post exactly this JSON as an Issue comment (and nothing else): " + string(expectedResponse),
	}, "\n")

	result, err := h.IssueService.Create(r.Context(), service.IssueCreateParams{
		WorkspaceID:    workspaceUUID,
		Title:          "[DTA load smoke " + dtaLoadSmokeOperationFingerprint(principal.TokenID, request.AgentID, request.Marker) + "] " + request.Marker,
		Description:    pgtype.Text{String: description, Valid: true},
		Status:         "todo",
		Priority:       "none",
		AssigneeType:   pgtype.Text{String: "agent", Valid: true},
		AssigneeID:     agentUUID,
		CreatorType:    "member",
		CreatorID:      parseUUID(subjectID),
		Metadata:       metadataJSON,
		AllowDuplicate: false,
	}, service.IssueCreateOpts{
		ActorID: subjectID, AnalyticsAgentID: request.AgentID, Platform: "dta",
	})
	if err != nil {
		if errors.Is(err, service.ErrActiveDuplicate) && result.DuplicateIssue != nil {
			h.writeDTALoadSmokeReplay(w, r, *result.DuplicateIssue, lookup, requestHash)
			return
		}
		if isUniqueViolation(err) {
			if existing, lookupErr := h.Queries.GetDTALoadSmokeByOperation(r.Context(), lookup); lookupErr == nil {
				h.writeDTALoadSmokeReplay(w, r, existing, lookup, requestHash)
				return
			}
		}
		writeError(w, http.StatusInternalServerError, "failed to create load smoke")
		return
	}
	writeJSON(w, http.StatusCreated, issueToResponse(result.Issue, h.getIssuePrefix(r.Context(), workspaceUUID)))
}

// GetDTALoadSmokeByOperation recovers a create response after the caller lost
// it or timed out. The operation is scoped to the authenticated Token, so a
// marker is never a cross-Token discovery mechanism.
func (h *Handler) GetDTALoadSmokeByOperation(w http.ResponseWriter, r *http.Request) {
	principal, isToken := middleware.WorkspaceAccessPrincipalFromContext(r.Context())
	if !isToken {
		writeError(w, http.StatusForbidden, "workspace_access_operation_not_allowed")
		return
	}
	agentID := strings.TrimSpace(r.URL.Query().Get("agent_id"))
	marker := strings.TrimSpace(r.URL.Query().Get("marker"))
	if !dtaLoadSmokeMarkerPattern.MatchString(marker) {
		writeError(w, http.StatusBadRequest, "marker contains unsupported characters")
		return
	}
	workspaceID := h.resolveWorkspaceID(r)
	workspaceUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}
	agentUUID, ok := parseUUIDOrBadRequest(w, agentID, "agent_id")
	if !ok {
		return
	}
	agent, err := h.Queries.GetAgent(r.Context(), agentUUID)
	if err != nil || uuidToString(agent.WorkspaceID) != workspaceID || agent.ArchivedAt.Valid {
		writeError(w, http.StatusNotFound, "agent not found")
		return
	}
	if uuidToString(agent.OwnerID) != principal.UserID {
		writeError(w, http.StatusForbidden, "workspace_access_resource_not_allowed")
		return
	}
	issue, err := h.Queries.GetDTALoadSmokeByOperation(r.Context(), db.GetDTALoadSmokeByOperationParams{
		WorkspaceID: workspaceUUID,
		TokenID:     principal.TokenID,
		AgentID:     agentID,
		Marker:      marker,
	})
	if err != nil {
		if isNotFound(err) {
			writeError(w, http.StatusNotFound, "load smoke not found")
		} else {
			writeError(w, http.StatusInternalServerError, "failed to resolve load smoke operation")
		}
		return
	}
	writeJSON(w, http.StatusOK, issueToResponse(issue, h.getIssuePrefix(r.Context(), workspaceUUID)))
}

func (h *Handler) writeDTALoadSmokeReplay(
	w http.ResponseWriter,
	r *http.Request,
	issue db.Issue,
	operation db.GetDTALoadSmokeByOperationParams,
	requestHash string,
) {
	var metadata dtaLoadSmokeMetadata
	if json.Unmarshal(issue.Metadata, &metadata) != nil || metadata.Kind != dtaLoadSmokeMetadataKind ||
		metadata.Schema != "dta-multica-load-smoke@2" || metadata.TokenID != operation.TokenID ||
		metadata.AgentID != operation.AgentID || metadata.Marker != operation.Marker ||
		uuidToString(issue.WorkspaceID) != uuidToString(operation.WorkspaceID) ||
		metadata.RequestHash == "" || metadata.RequestHash != requestHash {
		writeError(w, http.StatusConflict, "load_smoke_operation_conflict")
		return
	}
	writeJSON(w, http.StatusOK, issueToResponse(issue, h.getIssuePrefix(r.Context(), issue.WorkspaceID)))
}

func dtaLoadSmokeRequestHash(requiredSkills []string) string {
	digest := sha256.Sum256(mustJSON(requiredSkills))
	return fmt.Sprintf("%x", digest[:])
}

func dtaLoadSmokeOperationFingerprint(tokenID, agentID, marker string) string {
	digest := sha256.Sum256([]byte(tokenID + "\x00" + agentID + "\x00" + marker))
	return fmt.Sprintf("%x", digest[:8])
}

func mustJSON(value any) []byte {
	encoded, _ := json.Marshal(value)
	return encoded
}

func (h *Handler) ListDTALoadSmokeRuns(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.loadDTALoadSmoke(w, r); !ok {
		return
	}
	addRouteParam(r, "id", chi.URLParam(r, "issueId"))
	h.ListTasksByIssue(w, r)
}

func (h *Handler) ListDTALoadSmokeMessages(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadDTALoadSmoke(w, r)
	if !ok {
		return
	}
	taskID := chi.URLParam(r, "taskId")
	taskUUID, parseOK := parseUUIDOrBadRequest(w, taskID, "task_id")
	if !parseOK {
		return
	}
	task, err := h.Queries.GetAgentTask(r.Context(), taskUUID)
	if err != nil || !task.IssueID.Valid || task.IssueID != issue.ID {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}
	addRouteParam(r, "taskId", taskID)
	h.ListTaskMessagesByUser(w, r)
}

func (h *Handler) ListDTALoadSmokeComments(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.loadDTALoadSmoke(w, r); !ok {
		return
	}
	addRouteParam(r, "id", chi.URLParam(r, "issueId"))
	h.ListComments(w, r)
}

func (h *Handler) RetryDTALoadSmoke(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadDTALoadSmoke(w, r)
	if !ok {
		return
	}
	var request dtaLoadSmokeRetryRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	var content string
	switch request.Reason {
	case "initial":
		content = "Execute the deployment verification exactly as specified in this Issue description."
	case "skills_not_visible":
		content = "Skills were not yet visible in the sandbox; re-run the deployment verification exactly as specified in this Issue description."
	default:
		writeError(w, http.StatusBadRequest, "reason must be initial or skills_not_visible")
		return
	}
	agentID := uuidToString(issue.AssigneeID)
	body, _ := json.Marshal(CreateCommentRequest{
		Content: fmt.Sprintf("[@DTA load smoke](mention://agent/%s) %s", agentID, content),
	})
	r.Body = io.NopCloser(bytes.NewReader(body))
	addRouteParam(r, "id", chi.URLParam(r, "issueId"))
	h.CreateComment(w, r)
}

func (h *Handler) loadDTALoadSmoke(w http.ResponseWriter, r *http.Request) (db.Issue, bool) {
	principal, isToken := middleware.WorkspaceAccessPrincipalFromContext(r.Context())
	if !isToken {
		writeError(w, http.StatusForbidden, "workspace_access_operation_not_allowed")
		return db.Issue{}, false
	}
	issueID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "issueId"), "issue_id")
	if !ok {
		return db.Issue{}, false
	}
	workspaceID := h.resolveWorkspaceID(r)
	workspaceUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return db.Issue{}, false
	}
	issue, err := h.Queries.GetIssueInWorkspace(r.Context(), db.GetIssueInWorkspaceParams{
		ID: issueID, WorkspaceID: workspaceUUID,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "load smoke not found")
		return db.Issue{}, false
	}
	var metadata dtaLoadSmokeMetadata
	if json.Unmarshal(issue.Metadata, &metadata) != nil || metadata.Kind != dtaLoadSmokeMetadataKind ||
		metadata.SubjectID != requestUserID(r) || metadata.AgentID != uuidToString(issue.AssigneeID) {
		writeError(w, http.StatusNotFound, "load smoke not found")
		return db.Issue{}, false
	}
	if metadata.TokenID != principal.TokenID {
		writeError(w, http.StatusNotFound, "load smoke not found")
		return db.Issue{}, false
	}
	agent, err := h.Queries.GetAgent(r.Context(), issue.AssigneeID)
	if err != nil || uuidToString(agent.OwnerID) != principal.UserID {
		writeError(w, http.StatusForbidden, "workspace_access_resource_not_allowed")
		return db.Issue{}, false
	}
	return issue, true
}

func addRouteParam(r *http.Request, key, value string) {
	chi.RouteContext(r.Context()).URLParams.Add(key, value)
}
