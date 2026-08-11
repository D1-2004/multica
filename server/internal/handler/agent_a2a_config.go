package handler

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/featureflags"
	a2aintegration "github.com/multica-ai/multica/server/internal/integrations/a2a"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	agentA2AProtocolVersion = "1.0"
	maxAgentA2ARequestBody  = 128 << 10
	maxAgentA2ACardSkills   = 64
)

var agentA2AScopeOrder = []string{"send", "read"}
var defaultAgentA2AScopes = []string{"send", "read"}

type AgentA2AEndpointResponse struct {
	ID                string          `json:"id"`
	AgentID           string          `json:"agent_id"`
	PublicAgentID     string          `json:"public_agent_id"`
	Enabled           bool            `json:"enabled"`
	DelegatedByUserID string          `json:"delegated_by_user_id"`
	CardName          string          `json:"card_name"`
	CardDescription   string          `json:"card_description"`
	CardVersion       string          `json:"card_version"`
	CardSkills        json.RawMessage `json:"card_skills"`
	CardURL           string          `json:"card_url"`
	RPCURL            string          `json:"rpc_url"`
	MCPURL            string          `json:"mcp_url"`
	ProtocolVersion   string          `json:"protocol_version"`
	CreatedAt         string          `json:"created_at"`
	UpdatedAt         string          `json:"updated_at"`
}

type AgentA2ACredentialResponse struct {
	ID          string  `json:"id"`
	KeyID       string  `json:"key_id"`
	TokenPrefix string  `json:"token_prefix"`
	Status      string  `json:"status"`
	ExpiresAt   *string `json:"expires_at"`
	LastUsedAt  *string `json:"last_used_at"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
	RevokedAt   *string `json:"revoked_at"`
}

type AgentA2AClientResponse struct {
	ID                 string                       `json:"id"`
	Name               string                       `json:"name"`
	Status             string                       `json:"status"`
	Scopes             []string                     `json:"scopes"`
	RateLimitPerMinute *int32                       `json:"rate_limit_per_minute"`
	MaxConcurrentTasks *int32                       `json:"max_concurrent_tasks"`
	Credentials        []AgentA2ACredentialResponse `json:"credentials"`
	CreatedAt          string                       `json:"created_at"`
	UpdatedAt          string                       `json:"updated_at"`
	RevokedAt          *string                      `json:"revoked_at"`
}

type AgentA2AConfigResponse struct {
	Endpoint  *AgentA2AEndpointResponse `json:"endpoint"`
	AgentCard *a2a.AgentCard            `json:"agent_card"`
	Clients   []AgentA2AClientResponse  `json:"clients"`
}

type AgentA2ACredentialSecretResponse struct {
	Credential AgentA2ACredentialResponse `json:"credential"`
	Token      string                     `json:"token"`
}

type updateAgentA2AConfigRequest struct {
	Enabled         *bool           `json:"enabled"`
	CardName        string          `json:"card_name"`
	CardDescription string          `json:"card_description"`
	CardVersion     string          `json:"card_version"`
	CardSkills      json.RawMessage `json:"card_skills"`
}

type createAgentA2AClientRequest struct {
	Name               string   `json:"name"`
	Scopes             []string `json:"scopes"`
	RateLimitPerMinute *int32   `json:"rate_limit_per_minute"`
	MaxConcurrentTasks *int32   `json:"max_concurrent_tasks"`
}

type updateAgentA2AClientRequest struct {
	Name               *string         `json:"name"`
	Status             *string         `json:"status"`
	Scopes             *[]string       `json:"scopes"`
	RateLimitPerMinute json.RawMessage `json:"rate_limit_per_minute"`
	MaxConcurrentTasks json.RawMessage `json:"max_concurrent_tasks"`
}

type createAgentA2ACredentialRequest struct {
	ExpiresAt *string `json:"expires_at"`
}

type agentA2AManagementScope struct {
	Agent       db.Agent
	OwnerUserID pgtype.UUID
	ActorUserID pgtype.UUID
	WorkspaceID pgtype.UUID
}

// requireAgentA2AManager follows the general Agent management boundary: the
// Agent owner and workspace owners/admins may publish it or manage integration
// credentials. Machine credentials remain forbidden because this surface can
// mint bearer tokens and expose an Agent outside the workspace.
func (h *Handler) requireAgentA2AManager(w http.ResponseWriter, r *http.Request) (agentA2AManagementScope, bool) {
	if !featureflags.AgentA2AInboundEnabled(r.Context(), h.FeatureFlags) {
		writeError(w, http.StatusNotFound, "agent A2A inbound is not enabled")
		return agentA2AManagementScope{}, false
	}
	if r.Header.Get("X-Actor-Source") != "" {
		writeError(w, http.StatusForbidden, "this endpoint is only available to human actors")
		return agentA2AManagementScope{}, false
	}

	actorID, ok := requireUserID(w, r)
	if !ok {
		return agentA2AManagementScope{}, false
	}
	agent, ok := h.loadAgentForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return agentA2AManagementScope{}, false
	}
	member, ok := h.requireWorkspaceRole(
		w,
		r,
		uuidToString(agent.WorkspaceID),
		"agent not found",
		"owner",
		"admin",
		"member",
	)
	if !ok {
		return agentA2AManagementScope{}, false
	}
	if !roleAllowed(member.Role, "owner", "admin") && uuidToString(agent.OwnerID) != actorID {
		writeError(w, http.StatusForbidden, "only the agent owner or a workspace admin can manage A2A")
		return agentA2AManagementScope{}, false
	}
	return agentA2AManagementScope{
		Agent:       agent,
		OwnerUserID: agent.OwnerID,
		ActorUserID: member.UserID,
		WorkspaceID: agent.WorkspaceID,
	}, true
}

func (h *Handler) GetAgentA2AConfig(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.requireAgentA2AManager(w, r)
	if !ok {
		return
	}
	response, err := h.loadAgentA2AConfigResponse(r, scope)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load agent A2A configuration")
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *Handler) UpdateAgentA2AConfig(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.requireAgentA2AManager(w, r)
	if !ok {
		return
	}

	var request updateAgentA2AConfigRequest
	if err := decodeAgentA2AJSON(w, r, &request, false); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	request.CardName = strings.TrimSpace(request.CardName)
	request.CardVersion = strings.TrimSpace(request.CardVersion)
	if request.Enabled == nil {
		writeError(w, http.StatusBadRequest, "enabled is required")
		return
	}
	runtimeSafety := agentA2ARuntimeSafetyDecision{Mode: agentA2ARuntimeSafetyModeDenied}
	if len(request.CardSkills) == 0 || string(request.CardSkills) == "null" {
		writeError(w, http.StatusBadRequest, "card_skills must be an array")
		return
	}
	if err := validateAgentA2ACardMetadata(request.CardName, request.CardDescription, request.CardVersion); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	cardSkills, encodedSkills, err := normalizeAgentA2ACardSkills(request.CardSkills)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	publicAgentID := uuid.NewString()
	existing, err := h.Queries.GetAgentA2AEndpointForOwner(r.Context(), db.GetAgentA2AEndpointForOwnerParams{
		OwnerUserID: scope.OwnerUserID,
		WorkspaceID: scope.WorkspaceID,
		AgentID:     scope.Agent.ID,
	})
	if err == nil {
		publicAgentID = existing.PublicAgentID
	} else if !errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusInternalServerError, "failed to load agent A2A configuration")
		return
	}

	if *request.Enabled {
		if scope.Agent.ArchivedAt.Valid {
			writeError(w, http.StatusBadRequest, "archived agents cannot enable A2A")
			return
		}
		if !scope.Agent.RuntimeID.Valid {
			writeError(w, http.StatusBadRequest, "agent runtime is required to enable A2A")
			return
		}
		runtime, runtimeErr := h.Queries.GetAgentRuntimeForWorkspace(r.Context(), db.GetAgentRuntimeForWorkspaceParams{
			ID:          scope.Agent.RuntimeID,
			WorkspaceID: scope.WorkspaceID,
		})
		if errors.Is(runtimeErr, pgx.ErrNoRows) {
			writeError(w, http.StatusBadRequest, "agent runtime is not available")
			return
		}
		if runtimeErr != nil {
			writeError(w, http.StatusInternalServerError, "failed to load agent runtime")
			return
		}
		if runtimeErr = validateAgentA2ARuntimeFamily(runtime); runtimeErr != nil {
			writeError(w, http.StatusBadRequest, runtimeErr.Error())
			return
		}
		runtimeSafety = evaluateAgentA2ARuntimeSafety(h.currentConfig().PublicURL)
		if !runtimeSafety.Allowed {
			writeError(w, http.StatusForbidden, agentA2AUnsafeRuntimeForbidden)
			return
		}
		if _, buildErr := a2aintegration.BuildAgentCard(a2aintegration.CardConfig{
			BaseURL:       runtimeSafety.PublicBaseURL,
			PublicAgentID: publicAgentID,
			Name:          request.CardName,
			Description:   request.CardDescription,
			Version:       request.CardVersion,
			Skills:        cardSkills,
		}); buildErr != nil {
			writeError(w, http.StatusBadRequest, "invalid agent card configuration")
			return
		}
	}

	_, err = h.Queries.UpsertAgentA2AEndpoint(r.Context(), db.UpsertAgentA2AEndpointParams{
		PublicAgentID:   publicAgentID,
		Enabled:         *request.Enabled,
		OwnerUserID:     scope.OwnerUserID,
		ActorUserID:     scope.ActorUserID,
		CardName:        request.CardName,
		CardDescription: request.CardDescription,
		CardVersion:     request.CardVersion,
		CardSkills:      encodedSkills,
		AgentID:         scope.Agent.ID,
		WorkspaceID:     scope.WorkspaceID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "agent not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update agent A2A configuration")
		return
	}
	if *request.Enabled {
		warnAgentA2AUnsafeRuntimeAccepted(
			runtimeSafety.Mode,
			"enable",
			uuidToString(scope.WorkspaceID),
			uuidToString(scope.Agent.ID),
			publicAgentID,
			"",
		)
	}

	response, err := h.loadAgentA2AConfigResponse(r, scope)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load agent A2A configuration")
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *Handler) CreateAgentA2AClient(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.requireAgentA2AManager(w, r)
	if !ok {
		return
	}
	var request createAgentA2AClientRequest
	if err := decodeAgentA2AJSON(w, r, &request, false); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	request.Name = strings.TrimSpace(request.Name)
	if err := validateAgentA2AClientName(request.Name); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if request.Scopes == nil {
		request.Scopes = append([]string(nil), defaultAgentA2AScopes...)
	}
	scopes, err := normalizeAgentA2AScopes(request.Scopes)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if request.RateLimitPerMinute != nil {
		writeError(w, http.StatusBadRequest, "rate_limit_per_minute is not supported yet")
		return
	}
	if request.MaxConcurrentTasks != nil {
		writeError(w, http.StatusBadRequest, "max_concurrent_tasks is not supported yet")
		return
	}

	client, err := h.Queries.CreateAgentA2AClientForOwner(r.Context(), db.CreateAgentA2AClientForOwnerParams{
		Name:               request.Name,
		Scopes:             scopes,
		RateLimitPerMinute: pgtype.Int4{},
		MaxConcurrentTasks: pgtype.Int4{},
		OwnerUserID:        scope.OwnerUserID,
		ActorUserID:        scope.ActorUserID,
		WorkspaceID:        scope.WorkspaceID,
		AgentID:            scope.Agent.ID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "agent A2A endpoint not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create A2A client")
		return
	}
	writeJSON(w, http.StatusCreated, agentA2AClientToResponse(client, []AgentA2ACredentialResponse{}))
}

func (h *Handler) UpdateAgentA2AClient(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.requireAgentA2AManager(w, r)
	if !ok {
		return
	}
	clientID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "clientId"), "client id")
	if !ok {
		return
	}
	current, found, err := h.findAgentA2AClient(r, scope, clientID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load A2A client")
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "A2A client not found")
		return
	}
	if current.Status == "revoked" {
		writeError(w, http.StatusConflict, "A2A client is revoked")
		return
	}

	var request updateAgentA2AClientRequest
	if err := decodeAgentA2AJSON(w, r, &request, false); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	name := current.Name
	if request.Name != nil {
		name = strings.TrimSpace(*request.Name)
	}
	if err := validateAgentA2AClientName(name); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	status := current.Status
	if request.Status != nil {
		status = strings.TrimSpace(*request.Status)
	}
	if status != "active" && status != "disabled" && status != "revoked" {
		writeError(w, http.StatusBadRequest, "status must be active, disabled, or revoked")
		return
	}
	scopes := current.Scopes
	if request.Scopes != nil {
		scopes, err = normalizeAgentA2AScopes(*request.Scopes)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	rateLimit := current.RateLimitPerMinute
	if len(request.RateLimitPerMinute) != 0 {
		if string(request.RateLimitPerMinute) != "null" {
			writeError(w, http.StatusBadRequest, "rate_limit_per_minute is not supported yet")
			return
		}
		rateLimit = pgtype.Int4{}
	}
	maxConcurrent := current.MaxConcurrentTasks
	if len(request.MaxConcurrentTasks) != 0 {
		if string(request.MaxConcurrentTasks) != "null" {
			writeError(w, http.StatusBadRequest, "max_concurrent_tasks is not supported yet")
			return
		}
		maxConcurrent = pgtype.Int4{}
	}

	if status == "revoked" {
		revoked, revokeErr := h.Queries.RevokeAgentA2AClientForOwner(r.Context(), db.RevokeAgentA2AClientForOwnerParams{
			OwnerUserID: scope.OwnerUserID,
			ActorUserID: scope.ActorUserID,
			ClientID:    clientID,
			WorkspaceID: scope.WorkspaceID,
			AgentID:     scope.Agent.ID,
		})
		if errors.Is(revokeErr, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "A2A client not found")
			return
		}
		if revokeErr != nil {
			writeError(w, http.StatusInternalServerError, "failed to revoke A2A client")
			return
		}
		credentials, credentialsErr := h.loadAgentA2ACredentialResponses(r, scope, clientID)
		if credentialsErr != nil {
			writeError(w, http.StatusInternalServerError, "failed to load A2A client credentials")
			return
		}
		writeJSON(w, http.StatusOK, revokedAgentA2AClientToResponse(revoked, credentials))
		return
	}

	updated, err := h.Queries.UpdateAgentA2AClientForOwner(r.Context(), db.UpdateAgentA2AClientForOwnerParams{
		Name:               name,
		Status:             status,
		Scopes:             scopes,
		RateLimitPerMinute: rateLimit,
		MaxConcurrentTasks: maxConcurrent,
		OwnerUserID:        scope.OwnerUserID,
		ActorUserID:        scope.ActorUserID,
		ClientID:           clientID,
		WorkspaceID:        scope.WorkspaceID,
		AgentID:            scope.Agent.ID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "A2A client not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update A2A client")
		return
	}
	credentials, err := h.loadAgentA2ACredentialResponses(r, scope, clientID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load A2A client credentials")
		return
	}
	writeJSON(w, http.StatusOK, agentA2AClientToResponse(updated, credentials))
}

func (h *Handler) CreateAgentA2ACredential(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.requireAgentA2AManager(w, r)
	if !ok {
		return
	}
	clientID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "clientId"), "client id")
	if !ok {
		return
	}
	client, found, err := h.findAgentA2AClient(r, scope, clientID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load A2A client")
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "A2A client not found")
		return
	}
	if client.Status == "revoked" {
		writeError(w, http.StatusConflict, "A2A client is revoked")
		return
	}

	var request createAgentA2ACredentialRequest
	if err := decodeAgentA2AJSON(w, r, &request, true); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	expiresAt, err := parseAgentA2ACredentialExpiry(request.ExpiresAt)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	rawToken, err := auth.GenerateA2AToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to generate A2A credential")
		return
	}
	credential, err := h.Queries.CreateAgentA2ACredentialForOwner(r.Context(), db.CreateAgentA2ACredentialForOwnerParams{
		KeyID:       uuid.NewString(),
		TokenHash:   auth.HashToken(rawToken),
		TokenPrefix: agentA2ATokenPrefix(rawToken),
		ExpiresAt:   expiresAt,
		OwnerUserID: scope.OwnerUserID,
		ActorUserID: scope.ActorUserID,
		ClientID:    clientID,
		WorkspaceID: scope.WorkspaceID,
		AgentID:     scope.Agent.ID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "A2A client not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create A2A credential")
		return
	}
	// This is the only response that ever contains the raw bearer token. Keep
	// it out of browser, proxy, and shared HTTP caches; subsequent reads expose
	// metadata only and the token cannot be recovered.
	w.Header().Set("Cache-Control", "no-store, private")
	w.Header().Set("Pragma", "no-cache")
	writeJSON(w, http.StatusCreated, AgentA2ACredentialSecretResponse{
		Credential: createAgentA2ACredentialToResponse(credential),
		Token:      rawToken,
	})
}

func (h *Handler) DeleteAgentA2ACredential(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.requireAgentA2AManager(w, r)
	if !ok {
		return
	}
	clientID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "clientId"), "client id")
	if !ok {
		return
	}
	credentialID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "credentialId"), "credential id")
	if !ok {
		return
	}
	credentials, err := h.Queries.ListAgentA2ACredentialsForOwner(r.Context(), db.ListAgentA2ACredentialsForOwnerParams{
		OwnerUserID: scope.OwnerUserID,
		ClientID:    clientID,
		WorkspaceID: scope.WorkspaceID,
		AgentID:     scope.Agent.ID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load A2A credential")
		return
	}
	found := false
	alreadyRevoked := false
	for _, credential := range credentials {
		if credential.ID == credentialID {
			found = true
			alreadyRevoked = credential.Status == "revoked"
			break
		}
	}
	if !found {
		writeError(w, http.StatusNotFound, "A2A credential not found")
		return
	}
	if alreadyRevoked {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	_, err = h.Queries.RevokeAgentA2ACredentialForOwner(r.Context(), db.RevokeAgentA2ACredentialForOwnerParams{
		OwnerUserID:  scope.OwnerUserID,
		ActorUserID:  scope.ActorUserID,
		CredentialID: credentialID,
		ClientID:     clientID,
		WorkspaceID:  scope.WorkspaceID,
		AgentID:      scope.Agent.ID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		// Ownership and existence were established by the scoped read above. A
		// concurrent revoke is therefore the same successful end state.
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to revoke A2A credential")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) loadAgentA2AConfigResponse(r *http.Request, scope agentA2AManagementScope) (AgentA2AConfigResponse, error) {
	response := AgentA2AConfigResponse{Clients: []AgentA2AClientResponse{}}
	endpoint, err := h.Queries.GetAgentA2AEndpointForOwner(r.Context(), db.GetAgentA2AEndpointForOwnerParams{
		OwnerUserID: scope.OwnerUserID,
		WorkspaceID: scope.WorkspaceID,
		AgentID:     scope.Agent.ID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		endpoint, err = h.Queries.CreateAgentA2AEndpointIfMissing(r.Context(), db.CreateAgentA2AEndpointIfMissingParams{
			WorkspaceID:     scope.WorkspaceID,
			OwnerUserID:     scope.OwnerUserID,
			AgentID:         scope.Agent.ID,
			PublicAgentID:   uuid.NewString(),
			CardName:        scope.Agent.Name,
			CardDescription: scope.Agent.Description,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			// Another request may have won the unique agent_id insert race.
			endpoint, err = h.Queries.GetAgentA2AEndpointForOwner(r.Context(), db.GetAgentA2AEndpointForOwnerParams{
				OwnerUserID: scope.OwnerUserID,
				WorkspaceID: scope.WorkspaceID,
				AgentID:     scope.Agent.ID,
			})
		}
	}
	if err != nil {
		return AgentA2AConfigResponse{}, err
	}

	runtimeSupported := false
	if scope.Agent.RuntimeID.Valid {
		runtime, runtimeErr := h.Queries.GetAgentRuntimeForWorkspace(r.Context(), db.GetAgentRuntimeForWorkspaceParams{
			ID:          scope.Agent.RuntimeID,
			WorkspaceID: scope.WorkspaceID,
		})
		if runtimeErr != nil && !errors.Is(runtimeErr, pgx.ErrNoRows) {
			return AgentA2AConfigResponse{}, runtimeErr
		}
		runtimeSupported = runtimeErr == nil && isAgentA2ASupportedRuntimeFamily(runtime)
	}

	endpointResponse, card, err := h.agentA2AEndpointPresentation(scope.Agent, runtimeSupported, endpoint)
	if err != nil {
		return AgentA2AConfigResponse{}, err
	}
	response.Endpoint = &endpointResponse
	response.AgentCard = card

	clients, err := h.Queries.ListAgentA2AClientsForOwner(r.Context(), db.ListAgentA2AClientsForOwnerParams{
		OwnerUserID: scope.OwnerUserID,
		WorkspaceID: scope.WorkspaceID,
		AgentID:     scope.Agent.ID,
	})
	if err != nil {
		return AgentA2AConfigResponse{}, err
	}
	for _, client := range clients {
		credentials, credentialsErr := h.loadAgentA2ACredentialResponses(r, scope, client.ID)
		if credentialsErr != nil {
			return AgentA2AConfigResponse{}, credentialsErr
		}
		response.Clients = append(response.Clients, agentA2AClientToResponse(client, credentials))
	}
	return response, nil
}

func (h *Handler) agentA2AEndpointPresentation(agent db.Agent, runtimeSupported bool, endpoint db.AgentA2aEndpoint) (AgentA2AEndpointResponse, *a2a.AgentCard, error) {
	skills, encodedSkills, err := normalizeAgentA2ACardSkills(endpoint.CardSkills)
	if err != nil {
		return AgentA2AEndpointResponse{}, nil, err
	}
	response := AgentA2AEndpointResponse{
		ID:                uuidToString(endpoint.ID),
		AgentID:           uuidToString(endpoint.AgentID),
		PublicAgentID:     endpoint.PublicAgentID,
		Enabled:           false,
		DelegatedByUserID: uuidToString(endpoint.DelegatedByUserID),
		CardName:          endpoint.CardName,
		CardDescription:   endpoint.CardDescription,
		CardVersion:       endpoint.CardVersion,
		CardSkills:        json.RawMessage(encodedSkills),
		ProtocolVersion:   agentA2AProtocolVersion,
		CreatedAt:         timestampToString(endpoint.CreatedAt),
		UpdatedAt:         timestampToString(endpoint.UpdatedAt),
	}

	runtimeSafety := evaluateAgentA2ARuntimeSafety(h.currentConfig().PublicURL)
	if runtimeSafety.PublicBaseURL == "" {
		// A disabled endpoint remains manageable when deployment configuration is
		// absent. Empty URLs and a null Card make the misconfiguration explicit;
		// callers must never synthesize an origin from request headers.
		return response, nil, nil
	}
	baseURL := runtimeSafety.PublicBaseURL
	cardURL, err := a2aintegration.AgentCardURL(baseURL, endpoint.PublicAgentID)
	if err != nil {
		return AgentA2AEndpointResponse{}, nil, err
	}
	rpcURL, err := a2aintegration.AgentRPCURL(baseURL, endpoint.PublicAgentID)
	if err != nil {
		return AgentA2AEndpointResponse{}, nil, err
	}
	mcpURL, err := a2aintegration.AgentMCPURL(baseURL, endpoint.PublicAgentID)
	if err != nil {
		return AgentA2AEndpointResponse{}, nil, err
	}
	card, err := a2aintegration.BuildAgentCard(a2aintegration.CardConfig{
		BaseURL:       baseURL,
		PublicAgentID: endpoint.PublicAgentID,
		Name:          endpoint.CardName,
		Description:   endpoint.CardDescription,
		Version:       endpoint.CardVersion,
		Skills:        skills,
	})
	if err != nil {
		return AgentA2AEndpointResponse{}, nil, err
	}
	response.CardURL = cardURL
	response.RPCURL = rpcURL
	response.MCPURL = mcpURL
	response.Enabled = endpoint.Enabled &&
		uuidToString(endpoint.DelegatedByUserID) == uuidToString(agent.OwnerID) &&
		!agent.ArchivedAt.Valid && agent.RuntimeID.Valid && runtimeSupported &&
		runtimeSafety.Allowed
	return response, card, nil
}

func (h *Handler) findAgentA2AClient(r *http.Request, scope agentA2AManagementScope, clientID pgtype.UUID) (db.A2aClient, bool, error) {
	clients, err := h.Queries.ListAgentA2AClientsForOwner(r.Context(), db.ListAgentA2AClientsForOwnerParams{
		OwnerUserID: scope.OwnerUserID,
		WorkspaceID: scope.WorkspaceID,
		AgentID:     scope.Agent.ID,
	})
	if err != nil {
		return db.A2aClient{}, false, err
	}
	for _, client := range clients {
		if client.ID == clientID {
			return client, true, nil
		}
	}
	return db.A2aClient{}, false, nil
}

func (h *Handler) loadAgentA2ACredentialResponses(r *http.Request, scope agentA2AManagementScope, clientID pgtype.UUID) ([]AgentA2ACredentialResponse, error) {
	rows, err := h.Queries.ListAgentA2ACredentialsForOwner(r.Context(), db.ListAgentA2ACredentialsForOwnerParams{
		OwnerUserID: scope.OwnerUserID,
		ClientID:    clientID,
		WorkspaceID: scope.WorkspaceID,
		AgentID:     scope.Agent.ID,
	})
	if err != nil {
		return nil, err
	}
	responses := make([]AgentA2ACredentialResponse, 0, len(rows))
	for _, row := range rows {
		responses = append(responses, listedAgentA2ACredentialToResponse(row))
	}
	return responses, nil
}

func agentA2AClientToResponse(client db.A2aClient, credentials []AgentA2ACredentialResponse) AgentA2AClientResponse {
	return AgentA2AClientResponse{
		ID:                 uuidToString(client.ID),
		Name:               client.Name,
		Status:             client.Status,
		Scopes:             append([]string(nil), client.Scopes...),
		RateLimitPerMinute: int4ToPtr(client.RateLimitPerMinute),
		MaxConcurrentTasks: int4ToPtr(client.MaxConcurrentTasks),
		Credentials:        credentials,
		CreatedAt:          timestampToString(client.CreatedAt),
		UpdatedAt:          timestampToString(client.UpdatedAt),
		RevokedAt:          timestampToPtr(client.RevokedAt),
	}
}

func revokedAgentA2AClientToResponse(client db.RevokeAgentA2AClientForOwnerRow, credentials []AgentA2ACredentialResponse) AgentA2AClientResponse {
	return AgentA2AClientResponse{
		ID:                 uuidToString(client.ID),
		Name:               client.Name,
		Status:             client.Status,
		Scopes:             append([]string(nil), client.Scopes...),
		RateLimitPerMinute: int4ToPtr(client.RateLimitPerMinute),
		MaxConcurrentTasks: int4ToPtr(client.MaxConcurrentTasks),
		Credentials:        credentials,
		CreatedAt:          timestampToString(client.CreatedAt),
		UpdatedAt:          timestampToString(client.UpdatedAt),
		RevokedAt:          timestampToPtr(client.RevokedAt),
	}
}

func listedAgentA2ACredentialToResponse(credential db.ListAgentA2ACredentialsForOwnerRow) AgentA2ACredentialResponse {
	return newAgentA2ACredentialResponse(
		credential.ID,
		credential.KeyID,
		credential.TokenPrefix,
		credential.Status,
		credential.ExpiresAt,
		credential.LastUsedAt,
		credential.CreatedAt,
		credential.UpdatedAt,
		credential.RevokedAt,
	)
}

func createAgentA2ACredentialToResponse(credential db.CreateAgentA2ACredentialForOwnerRow) AgentA2ACredentialResponse {
	return newAgentA2ACredentialResponse(
		credential.ID,
		credential.KeyID,
		credential.TokenPrefix,
		credential.Status,
		credential.ExpiresAt,
		credential.LastUsedAt,
		credential.CreatedAt,
		credential.UpdatedAt,
		credential.RevokedAt,
	)
}

func newAgentA2ACredentialResponse(
	id pgtype.UUID,
	keyID string,
	tokenPrefix string,
	status string,
	expiresAt pgtype.Timestamptz,
	lastUsedAt pgtype.Timestamptz,
	createdAt pgtype.Timestamptz,
	updatedAt pgtype.Timestamptz,
	revokedAt pgtype.Timestamptz,
) AgentA2ACredentialResponse {
	return AgentA2ACredentialResponse{
		ID:          uuidToString(id),
		KeyID:       keyID,
		TokenPrefix: tokenPrefix,
		Status:      status,
		ExpiresAt:   timestampToPtr(expiresAt),
		LastUsedAt:  timestampToPtr(lastUsedAt),
		CreatedAt:   timestampToString(createdAt),
		UpdatedAt:   timestampToString(updatedAt),
		RevokedAt:   timestampToPtr(revokedAt),
	}
}

func decodeAgentA2AJSON(w http.ResponseWriter, r *http.Request, target any, allowEmpty bool) error {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAgentA2ARequestBody))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		if allowEmpty && errors.Is(err, io.EOF) {
			return nil
		}
		return errors.New("invalid request body")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("invalid request body")
	}
	return nil
}

func validateAgentA2ACardMetadata(name, description, version string) error {
	if name == "" {
		return errors.New("card_name is required")
	}
	if utf8.RuneCountInString(name) > 200 {
		return errors.New("card_name must be at most 200 characters")
	}
	if utf8.RuneCountInString(description) > 4000 {
		return errors.New("card_description must be at most 4000 characters")
	}
	if version == "" {
		return errors.New("card_version is required")
	}
	if utf8.RuneCountInString(version) > 64 {
		return errors.New("card_version must be at most 64 characters")
	}
	return nil
}

func normalizeAgentA2ACardSkills(raw json.RawMessage) ([]a2a.AgentSkill, []byte, error) {
	if len(raw) == 0 || string(raw) == "null" {
		raw = json.RawMessage(`[]`)
	}
	var skills []a2a.AgentSkill
	if err := json.Unmarshal(raw, &skills); err != nil || skills == nil {
		return nil, nil, errors.New("card_skills must be an array of valid A2A skills")
	}
	if len(skills) > maxAgentA2ACardSkills {
		return nil, nil, errors.New("card_skills must contain at most 64 skills")
	}
	seen := make(map[string]struct{}, len(skills))
	for index := range skills {
		skill := &skills[index]
		skill.ID = strings.TrimSpace(skill.ID)
		skill.Name = strings.TrimSpace(skill.Name)
		if skill.ID == "" || skill.Name == "" || strings.TrimSpace(skill.Description) == "" {
			return nil, nil, errors.New("each card skill requires id, name, and description")
		}
		if utf8.RuneCountInString(skill.ID) > 128 || utf8.RuneCountInString(skill.Name) > 200 || utf8.RuneCountInString(skill.Description) > 4000 {
			return nil, nil, errors.New("card skill fields exceed the supported length")
		}
		if _, exists := seen[skill.ID]; exists {
			return nil, nil, errors.New("card skill ids must be unique")
		}
		seen[skill.ID] = struct{}{}
		if skill.Tags == nil {
			skill.Tags = []string{}
		}
		if !agentA2ATextModesOnly(skill.InputModes) || !agentA2ATextModesOnly(skill.OutputModes) {
			return nil, nil, errors.New("card skill inputModes and outputModes only support text/plain")
		}
		// Security is endpoint-wide in the first inbound release. Do not allow a
		// stored skill payload to add undeclared or weaker per-skill requirements.
		skill.SecurityRequirements = nil
	}
	encoded, err := json.Marshal(skills)
	if err != nil {
		return nil, nil, errors.New("failed to encode card_skills")
	}
	return skills, encoded, nil
}

func agentA2ATextModesOnly(modes []string) bool {
	for _, mode := range modes {
		if mode != "text/plain" {
			return false
		}
	}
	return true
}

func validateAgentA2AClientName(name string) error {
	if name == "" {
		return errors.New("name is required")
	}
	if utf8.RuneCountInString(name) > 200 {
		return errors.New("name must be at most 200 characters")
	}
	return nil
}

func normalizeAgentA2AScopes(scopes []string) ([]string, error) {
	if len(scopes) == 0 {
		return nil, errors.New("scopes must contain at least one scope")
	}
	selected := make(map[string]struct{}, len(scopes))
	for _, scope := range scopes {
		selected[strings.TrimSpace(scope)] = struct{}{}
	}
	normalized := make([]string, 0, len(selected))
	for _, allowed := range agentA2AScopeOrder {
		if _, ok := selected[allowed]; ok {
			normalized = append(normalized, allowed)
			delete(selected, allowed)
		}
	}
	if len(selected) != 0 {
		return nil, errors.New("scopes currently only support send and read")
	}
	return normalized, nil
}

func validateAgentA2ARuntimeFamily(runtime db.AgentRuntime) error {
	if isAgentA2ASupportedRuntimeFamily(runtime) {
		return nil
	}
	return errors.New("A2A inbound requires a local Claude runtime or a managed Aliyun FC OpenCode cloud sandbox; all runtime template and capability versions in those families are supported")
}

func isAgentA2ASupportedRuntimeFamily(runtime db.AgentRuntime) bool {
	// Runtime versions, template aliases, manifest versions and capabilities are
	// deliberately absent from this predicate. They select native versus legacy
	// claim handling, never endpoint admission. Provider families remain an
	// explicit boundary because the daemon currently has verified A2A isolation
	// only for Claude and the managed Aliyun FC OpenCode sandbox adapter.
	if runtime.RuntimeMode == "local" && runtime.Provider == "claude" {
		return true
	}
	if runtime.RuntimeMode != "cloud" || runtime.Provider != "opencode" {
		return false
	}
	metadata, err := service.ParseCloudSandboxRuntime(runtime)
	return err == nil &&
		metadata.SandboxBackend == service.SandboxBackendAliyunFC &&
		metadata.Provider == "opencode"
}

func parseAgentA2ACredentialExpiry(value *string) (pgtype.Timestamptz, error) {
	if value == nil || strings.TrimSpace(*value) == "" {
		return pgtype.Timestamptz{}, nil
	}
	parsed, err := time.Parse(time.RFC3339, *value)
	if err != nil {
		return pgtype.Timestamptz{}, errors.New("expires_at must be an RFC3339 timestamp or null")
	}
	if !parsed.After(time.Now()) {
		return pgtype.Timestamptz{}, errors.New("expires_at must be in the future")
	}
	return pgtype.Timestamptz{Time: parsed, Valid: true}, nil
}

func normalizeAgentA2APublicBaseURL(raw string) (string, error) {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || !parsed.IsAbs() || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("MULTICA_PUBLIC_URL must be an absolute HTTPS URL")
	}
	if parsed.Scheme == "https" {
		return raw, nil
	}
	if parsed.Scheme != "http" || !isAgentA2ALoopbackHost(parsed.Hostname()) {
		return "", errors.New("MULTICA_PUBLIC_URL must use HTTPS (HTTP is allowed only on loopback)")
	}
	return raw, nil
}

func isAgentA2ALoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}

func agentA2ATokenPrefix(rawToken string) string {
	if len(rawToken) > 14 {
		return rawToken[:14]
	}
	return rawToken
}
