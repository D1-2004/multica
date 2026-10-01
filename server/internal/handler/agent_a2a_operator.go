package handler

import (
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/logger"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// The A2A operator surface lets a deployment operator bind a DEAP digital
// employee identity to an Agent and, on pre-release, accept that employee's
// production A2A traffic. Both act through someone else's credential, so
// neither is an ordinary Agent-management permission: the deployment names the
// operators by email (MULTICA_A2A_OPERATOR_EMAILS) and everyone else sees only
// operator=false.
//
// The identity is the agent_dingtalk_identity row the Integrations DingTalk
// binding also writes; binding or clearing it here or there changes both.

var (
	agentA2AOperatorDecimalID  = regexp.MustCompile(`^[1-9][0-9]{0,19}$`)
	agentA2AForwardRPCPath     = regexp.MustCompile(`^/api/a2a/agents/([A-Za-z0-9_-]{16,128})/v1$`)
	agentA2AOperatorDEAPUUIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
)

const agentA2AOperatorMaxNameRunes = 100

type AgentA2AOperatorIdentityResponse struct {
	UID              string  `json:"uid"`
	OrgID            string  `json:"org_id"`
	DisplayName      string  `json:"display_name"`
	OrganizationName string  `json:"organization_name"`
	DEAPAgentUUID    *string `json:"deap_agent_uuid"`
	A2AEnabled       bool    `json:"a2a_enabled"`
	BoundAt          string  `json:"bound_at"`
}

// AgentA2AProdForwardRegistrationResponse is one production registry as seen
// from pre-release. Current means the registry holds this Agent's present
// identity; a registration that is not current no longer receives traffic.
type AgentA2AProdForwardRegistrationResponse struct {
	Registry     string  `json:"registry"`
	RegisteredAt *string `json:"registered_at"`
	Current      bool    `json:"current"`
	Error        string  `json:"error,omitempty"`
}

// AgentA2AProdForwardResponse is the pre-release side: whether this Agent
// accepts production forwards for its identity, why it cannot register yet,
// and each registry's registration.
type AgentA2AProdForwardResponse struct {
	Accept        bool                                      `json:"accept"`
	BlockedReason string                                    `json:"blocked_reason,omitempty"`
	Registrations []AgentA2AProdForwardRegistrationResponse `json:"registrations"`
}

// AgentA2AForwardTargetResponse is the production side: the pre-release Agent
// currently registered for this Agent's identity.
type AgentA2AForwardTargetResponse struct {
	RPCURL       string `json:"rpc_url"`
	AgentName    string `json:"agent_name"`
	RegisteredAt string `json:"registered_at"`
}

type AgentA2AOperatorResponse struct {
	Operator      bool                              `json:"operator"`
	DWSIdentity   *AgentA2AOperatorIdentityResponse `json:"dws_identity,omitempty"`
	ProdForward   *AgentA2AProdForwardResponse      `json:"prod_forward,omitempty"`
	ForwardTarget *AgentA2AForwardTargetResponse    `json:"forward_target,omitempty"`
}

type updateAgentA2AOperatorIdentityRequest struct {
	UID              string `json:"uid"`
	OrgID            string `json:"org_id"`
	DisplayName      string `json:"display_name"`
	OrganizationName string `json:"organization_name"`
	DEAPAgentUUID    string `json:"deap_agent_uuid"`
}

type updateAgentA2AProdForwardRequest struct {
	Accept *bool `json:"accept"`
}

// isAgentA2AOperator reports whether the human actor's account email is listed
// in the deployment operator allow-list. An empty list admits nobody.
func (h *Handler) isAgentA2AOperator(r *http.Request, actorID pgtype.UUID) bool {
	operators := h.currentConfig().A2AOperatorEmails
	if len(operators) == 0 || !actorID.Valid {
		return false
	}
	user, err := h.Queries.GetUser(r.Context(), actorID)
	if err != nil {
		return false
	}
	email := strings.ToLower(strings.TrimSpace(user.Email))
	if email == "" {
		return false
	}
	for _, operator := range operators {
		if strings.ToLower(strings.TrimSpace(operator)) == email {
			return true
		}
	}
	return false
}

func (h *Handler) requireAgentA2AOperator(w http.ResponseWriter, r *http.Request) (agentA2AManagementScope, bool) {
	scope, ok := h.requireAgentA2AManager(w, r)
	if !ok {
		return agentA2AManagementScope{}, false
	}
	if !h.isAgentA2AOperator(r, scope.ActorUserID) {
		writeError(w, http.StatusForbidden, "only deployment A2A operators can change this setting")
		return agentA2AManagementScope{}, false
	}
	return scope, true
}

func (h *Handler) GetAgentA2AOperatorConfig(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.requireAgentA2AManager(w, r)
	if !ok {
		return
	}
	if !h.isAgentA2AOperator(r, scope.ActorUserID) {
		writeJSON(w, http.StatusOK, AgentA2AOperatorResponse{Operator: false})
		return
	}
	h.writeAgentA2AOperatorResponse(w, r, scope)
}

func (h *Handler) UpdateAgentA2AOperatorIdentity(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.requireAgentA2AOperator(w, r)
	if !ok {
		return
	}
	var req updateAgentA2AOperatorIdentityRequest
	if err := decodeAgentA2AJSON(w, r, &req, false); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	uid := strings.TrimSpace(req.UID)
	orgID := strings.TrimSpace(req.OrgID)
	if !agentA2AOperatorDecimalID.MatchString(uid) || !agentA2AOperatorDecimalID.MatchString(orgID) {
		writeError(w, http.StatusBadRequest, "uid and org_id must be positive decimal identifiers")
		return
	}
	displayName := strings.TrimSpace(req.DisplayName)
	organizationName := strings.TrimSpace(req.OrganizationName)
	if len([]rune(displayName)) > agentA2AOperatorMaxNameRunes || len([]rune(organizationName)) > agentA2AOperatorMaxNameRunes {
		writeError(w, http.StatusBadRequest, "display_name and organization_name must be at most 100 characters")
		return
	}
	deapAgentUUID := pgtype.Text{}
	if value := strings.TrimSpace(req.DEAPAgentUUID); value != "" {
		if !agentA2AOperatorDEAPUUIDRe.MatchString(value) {
			writeError(w, http.StatusBadRequest, "deap_agent_uuid is invalid")
			return
		}
		deapAgentUUID = pgtype.Text{String: value, Valid: true}
	}
	if !h.rejectTagTemplateBinding(w, r, scope.WorkspaceID, scope.Agent.ID) {
		return
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start transaction")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	queries := h.Queries.WithTx(tx)
	// A pending QR attempt must not overwrite the identity bound here.
	if err := queries.DeleteAgentDingTalkIdentityAttempts(r.Context(), db.DeleteAgentDingTalkIdentityAttemptsParams{
		WorkspaceID: scope.WorkspaceID,
		AgentID:     scope.Agent.ID,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save DingTalk identity")
		return
	}
	if err := queries.UpsertAgentDingTalkIdentityManual(r.Context(), db.UpsertAgentDingTalkIdentityManualParams{
		AgentID:            scope.Agent.ID,
		WorkspaceID:        scope.WorkspaceID,
		DwsUid:             uid,
		OrgID:              orgID,
		AccountDisplayName: displayName,
		OrganizationName:   organizationName,
		BoundBy:            scope.ActorUserID,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save DingTalk identity")
		return
	}
	if err := queries.SetAgentA2AIdentityEnabled(r.Context(), db.SetAgentA2AIdentityEnabledParams{
		AgentID:            scope.Agent.ID,
		WorkspaceID:        scope.WorkspaceID,
		A2aIdentityEnabled: true,
		DeapAgentUuid:      deapAgentUUID,
		UpdatedBy:          scope.ActorUserID,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save DingTalk identity")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save DingTalk identity")
		return
	}
	h.auditAgentA2AOperatorChange(r, scope, "dws_identity_set")
	h.syncAgentA2AForwardRegistration(r.Context(), scope, true)
	h.writeAgentA2AOperatorResponse(w, r, scope)
}

func (h *Handler) DeleteAgentA2AOperatorIdentity(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.requireAgentA2AOperator(w, r)
	if !ok {
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start transaction")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	queries := h.Queries.WithTx(tx)
	if _, err := queries.DeleteAgentDingTalkIdentity(r.Context(), db.DeleteAgentDingTalkIdentityParams{
		WorkspaceID: scope.WorkspaceID,
		AgentID:     scope.Agent.ID,
	}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusInternalServerError, "failed to clear DingTalk identity")
		return
	}
	if err := queries.SetAgentA2AIdentityEnabled(r.Context(), db.SetAgentA2AIdentityEnabledParams{
		AgentID:            scope.Agent.ID,
		WorkspaceID:        scope.WorkspaceID,
		A2aIdentityEnabled: false,
		UpdatedBy:          scope.ActorUserID,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to clear DingTalk identity")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to clear DingTalk identity")
		return
	}
	h.auditAgentA2AOperatorChange(r, scope, "dws_identity_cleared")
	h.syncAgentA2AForwardRegistration(r.Context(), scope, false)
	h.writeAgentA2AOperatorResponse(w, r, scope)
}

// UpdateAgentA2AProdForward turns the pre-release "accept production forwards"
// switch on or off; {accept: true} while already on re-registers. It exists
// only where this deployment registers with a production registry.
func (h *Handler) UpdateAgentA2AProdForward(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.requireAgentA2AOperator(w, r)
	if !ok {
		return
	}
	if !h.agentA2AForwardRegistrant() {
		writeError(w, http.StatusNotFound, "production forwarding is not available on this deployment")
		return
	}
	var req updateAgentA2AProdForwardRequest
	if err := decodeAgentA2AJSON(w, r, &req, false); err != nil || req.Accept == nil {
		writeError(w, http.StatusBadRequest, "accept is required")
		return
	}
	if err := h.Queries.SetAgentA2AAcceptProdForward(r.Context(), db.SetAgentA2AAcceptProdForwardParams{
		AgentID:           scope.Agent.ID,
		WorkspaceID:       scope.WorkspaceID,
		AcceptProdForward: *req.Accept,
		UpdatedBy:         scope.ActorUserID,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save production forward setting")
		return
	}
	action := "prod_forward_disabled"
	if *req.Accept {
		action = "prod_forward_enabled"
	}
	h.auditAgentA2AOperatorChange(r, scope, action)
	// Turning the switch on (again) always re-registers with a fresh key, which
	// is also how an operator repairs a registration a registry lost.
	h.syncAgentA2AForwardRegistration(r.Context(), scope, *req.Accept)
	h.writeAgentA2AOperatorResponse(w, r, scope)
}

func (h *Handler) writeAgentA2AOperatorResponse(w http.ResponseWriter, r *http.Request, scope agentA2AManagementScope) {
	response, err := h.loadAgentA2AOperatorResponse(r, scope)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load A2A operator configuration")
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *Handler) loadAgentA2AOperatorResponse(r *http.Request, scope agentA2AManagementScope) (AgentA2AOperatorResponse, error) {
	response := AgentA2AOperatorResponse{Operator: true}
	config, err := h.Queries.GetAgentA2AOperatorConfig(r.Context(), db.GetAgentA2AOperatorConfigParams{
		WorkspaceID: scope.WorkspaceID,
		AgentID:     scope.Agent.ID,
	})
	hasConfig := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return AgentA2AOperatorResponse{}, err
	}
	identity, err := h.Queries.GetAgentDingTalkIdentity(r.Context(), db.GetAgentDingTalkIdentityParams{
		WorkspaceID: scope.WorkspaceID,
		AgentID:     scope.Agent.ID,
	})
	hasIdentity := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return AgentA2AOperatorResponse{}, err
	}
	if hasIdentity {
		item := &AgentA2AOperatorIdentityResponse{
			UID:              identity.DwsUid,
			OrgID:            identity.OrgID,
			DisplayName:      identity.AccountDisplayName,
			OrganizationName: identity.OrganizationName,
			A2AEnabled:       hasConfig && config.A2aIdentityEnabled,
			BoundAt:          timestampToString(identity.BoundAt),
		}
		if hasConfig && config.DeapAgentUuid.Valid {
			value := config.DeapAgentUuid.String
			item.DEAPAgentUUID = &value
		}
		response.DWSIdentity = item
	}
	if h.agentA2AForwardRegistrant() {
		forward, err := h.loadAgentA2AProdForwardResponse(r, scope)
		if err != nil {
			return AgentA2AOperatorResponse{}, err
		}
		response.ProdForward = forward
	}
	if h.agentA2AForwardRegistry() && hasIdentity && hasConfig && config.A2aIdentityEnabled {
		registration, err := h.Queries.GetA2AForwardRegistration(r.Context(), db.GetA2AForwardRegistrationParams{
			DwsUid: identity.DwsUid,
			OrgID:  identity.OrgID,
		})
		if err == nil {
			response.ForwardTarget = &AgentA2AForwardTargetResponse{
				RPCURL:       registration.RpcUrl,
				AgentName:    registration.TargetAgentName,
				RegisteredAt: timestampToString(registration.RegisteredAt),
			}
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return AgentA2AOperatorResponse{}, err
		}
	}
	return response, nil
}

func (h *Handler) loadAgentA2AProdForwardResponse(r *http.Request, scope agentA2AManagementScope) (*AgentA2AProdForwardResponse, error) {
	desired, err := h.loadAgentA2AForwardDesired(r.Context(), scope.Agent)
	if err != nil {
		return nil, err
	}
	rows, err := h.Queries.ListAgentA2AForwardRegistrants(r.Context(), db.ListAgentA2AForwardRegistrantsParams{
		AgentID:     scope.Agent.ID,
		WorkspaceID: scope.WorkspaceID,
	})
	if err != nil {
		return nil, err
	}
	forward := &AgentA2AProdForwardResponse{
		Accept:        desired.Accept,
		BlockedReason: desired.Reason,
		Registrations: []AgentA2AProdForwardRegistrationResponse{},
	}
	byOrigin := make(map[string]db.AgentA2aForwardRegistrant, len(rows))
	for _, row := range rows {
		byOrigin[row.RegistryOrigin] = row
	}
	for _, origin := range normalizedAgentA2AForwardOrigins(h.currentConfig().A2AForwardRegistryURLs) {
		item := AgentA2AProdForwardRegistrationResponse{Registry: origin}
		if row, ok := byOrigin[origin]; ok {
			if row.RegisteredAt.Valid {
				value := timestampToString(row.RegisteredAt)
				item.RegisteredAt = &value
			}
			item.Current = agentA2AForwardRegistrantCurrent(row, desired)
			item.Error = row.LastError.String
		}
		forward.Registrations = append(forward.Registrations, item)
	}
	return forward, nil
}

func (h *Handler) auditAgentA2AOperatorChange(r *http.Request, scope agentA2AManagementScope, action string, attrs ...any) {
	fields := append(logger.RequestAttrs(r),
		"event", "a2a_operator_config_changed",
		"action", action,
		"workspace_id", uuidToString(scope.WorkspaceID),
		"agent_id", uuidToString(scope.Agent.ID),
		"actor_user_id", uuidToString(scope.ActorUserID),
	)
	slog.Info("A2A operator configuration changed", append(fields, attrs...)...)
}

// validateAgentA2AForwardRPCURL accepts only the canonical JSON-RPC URL of a
// hosted Agent on an allow-listed HTTPS origin.
func validateAgentA2AForwardRPCURL(raw string, allowedOrigins []string) (string, error) {
	allowed := normalizedAgentA2AForwardOrigins(allowedOrigins)
	if len(allowed) == 0 {
		return "", errors.New("A2A forwarding is not enabled on this deployment")
	}
	parsed, err := agentA2AForwardOriginAllowed(strings.TrimSpace(raw), allowed)
	if err != nil {
		return "", err
	}
	if !agentA2AForwardRPCPath.MatchString(parsed.Path) {
		return "", errors.New("rpc_url must be an A2A JSON-RPC URL like https://host/api/a2a/agents/{id}/v1")
	}
	return parsed.Scheme + "://" + parsed.Host + parsed.Path, nil
}

func agentA2AForwardOriginAllowed(raw string, allowed []string) (*url.URL, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("rpc_url must be an https URL without credentials, query or fragment")
	}
	origin := strings.ToLower(parsed.Scheme + "://" + parsed.Host)
	for _, candidate := range allowed {
		if candidate == origin {
			return parsed, nil
		}
	}
	return nil, errors.New("rpc_url origin is not in MULTICA_A2A_FORWARD_ALLOWED_ORIGINS")
}

func normalizedAgentA2AForwardOrigins(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		parsed, err := url.Parse(strings.TrimSpace(value))
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil ||
			(parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
			continue
		}
		out = append(out, strings.ToLower(parsed.Scheme+"://"+parsed.Host))
	}
	return out
}
