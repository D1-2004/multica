package handler

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/auth"
	a2aintegration "github.com/multica-ai/multica/server/internal/integrations/a2a"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Production A2A traffic reaches a pre-release Agent through registrations,
// the same shape as the FC sandbox relay: pre-release signs, production
// verifies. A pre-release Agent whose digital employee identity is bound and
// which accepts production forwards (the default) keeps one dedicated A2A
// client per registry (MULTICA_A2A_FORWARD_REGISTRY_URLS) and employee, and
// registers {identity, JSON-RPC URL, client, key} there. The registry
// deployment (production) then forwards the calls of its own Agent bound to
// the same identity. Requests are authenticated with an HMAC over the
// timestamp and body using MULTICA_A2A_FORWARD_REGISTRATION_SECRET, which both
// deployments share.
//
// Whoever operates pre-release can therefore redirect any production Agent
// whose operator enabled the same identity for A2A. Both operator lists name
// the same people; the shared secret never leaves the two deployments.
//
// A registration that goes stale (identity changed on the Integrations page,
// switch turned off, withdrawal lost) fails closed: pre-release answers the key
// with 401 plus agentA2AForwardKeyRejectedHeader once it no longer matches or
// was revoked, and production then retires the registration and serves the
// call itself. Any other 401 passes through unchanged.

const (
	agentA2AForwardRegistrationPath        = "/api/internal/a2a/forward-registrations"
	agentA2AForwardSignatureHeader         = "X-Multica-A2A-Forward-Signature"
	agentA2AForwardTimestampHeader         = "X-Multica-A2A-Forward-Timestamp"
	agentA2AForwardRegistrationMaxBody     = 64 << 10
	agentA2AForwardRegistrationMaxSkew     = 5 * time.Minute
	agentA2AForwardRegistrationMinSecret   = 32
	agentA2AForwardRegistrationClientName  = "Production forward"
	agentA2AForwardRegistrationActionSet   = "register"
	agentA2AForwardRegistrationActionClear = "revoke"
	agentA2AForwardSyncTimeout             = time.Minute
	agentA2AForwardSyncLockRetry           = 200 * time.Millisecond
	// agentA2AForwardKeyRejectedHeader marks a 401 that pre-release gives a
	// production-forward key it no longer honors. Nothing ran for the call.
	agentA2AForwardKeyRejectedHeader = "X-Multica-A2A-Forward-Key-Rejected"
)

var agentA2AForwardSHA256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)

var agentA2AForwardRegistrationHTTPClient = &http.Client{
	Timeout:   10 * time.Second,
	Transport: &http.Transport{Proxy: nil, TLSHandshakeTimeout: 5 * time.Second},
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

type agentA2AForwardRegistrationRequest struct {
	Action          string `json:"action"`
	DWSUID          string `json:"dws_uid"`
	OrgID           string `json:"org_id"`
	RPCURL          string `json:"rpc_url"`
	TargetClientID  string `json:"target_client_id,omitempty"`
	Token           string `json:"token,omitempty"`
	TokenSHA256     string `json:"token_sha256,omitempty"`
	TargetAgentName string `json:"target_agent_name,omitempty"`
}

func (h *Handler) agentA2AForwardSecret() []byte {
	secret := strings.TrimSpace(h.currentConfig().A2AForwardRegistrationSecret)
	if len(secret) < agentA2AForwardRegistrationMinSecret {
		return nil
	}
	return []byte(secret)
}

// agentA2AForwardRegistrant reports whether this deployment registers its
// Agents with production registries (the pre-release role).
func (h *Handler) agentA2AForwardRegistrant() bool {
	return h.agentA2AForwardSecret() != nil && len(normalizedAgentA2AForwardOrigins(h.currentConfig().A2AForwardRegistryURLs)) > 0
}

// agentA2AForwardRegistry reports whether this deployment accepts
// registrations and forwards to them (the production role).
func (h *Handler) agentA2AForwardRegistry() bool {
	return h.agentA2AForwardSecret() != nil &&
		len(normalizedAgentA2AForwardOrigins(h.currentConfig().A2AForwardAllowedOrigins)) > 0 &&
		h.A2AService != nil && h.A2AService.PushSecrets != nil
}

func signAgentA2AForwardRegistration(secret []byte, timestamp string, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(timestamp))
	mac.Write([]byte("\n"))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func agentA2AForwardTokenSHA256(token string) string {
	digest := sha256.Sum256([]byte(token))
	return hex.EncodeToString(digest[:])
}

// agentA2AForwardBindingKey identifies a forward target for the source-client
// binding: one pre-release client behind one JSON-RPC URL. It survives key
// rotations, so the production client that claimed the target keeps it.
func agentA2AForwardBindingKey(rpcURL string, targetClientID pgtype.UUID) string {
	return agentA2AForwardTokenSHA256("a2a-forward-target\n" + rpcURL + "\n" + uuidToString(targetClientID))
}

// HandleA2AForwardRegistration is the production registry endpoint. It is not
// user-authenticated; the shared-secret signature is the only credential. The
// signed timestamp (Unix milliseconds) also orders registrations: an older
// signed request never replaces or withdraws a newer registration.
func (h *Handler) HandleA2AForwardRegistration(w http.ResponseWriter, r *http.Request) {
	if !h.agentA2AForwardRegistry() {
		http.NotFound(w, r)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, agentA2AForwardRegistrationMaxBody))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid registration body")
		return
	}
	timestamp := strings.TrimSpace(r.Header.Get(agentA2AForwardTimestampHeader))
	signedAtMs, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil || absDuration(time.Since(time.UnixMilli(signedAtMs))) > agentA2AForwardRegistrationMaxSkew {
		writeError(w, http.StatusUnauthorized, "invalid registration signature")
		return
	}
	expected := signAgentA2AForwardRegistration(h.agentA2AForwardSecret(), timestamp, body)
	if !hmac.Equal([]byte(expected), []byte(strings.TrimSpace(r.Header.Get(agentA2AForwardSignatureHeader)))) {
		writeError(w, http.StatusUnauthorized, "invalid registration signature")
		return
	}
	var req agentA2AForwardRegistrationRequest
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid registration body")
		return
	}
	if !agentA2AOperatorDecimalID.MatchString(req.DWSUID) || !agentA2AOperatorDecimalID.MatchString(req.OrgID) {
		writeError(w, http.StatusBadRequest, "dws_uid and org_id must be positive decimal identifiers")
		return
	}
	rpcURL, err := validateAgentA2AForwardRPCURL(req.RPCURL, h.currentConfig().A2AForwardAllowedOrigins)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	switch req.Action {
	case agentA2AForwardRegistrationActionSet:
		if !validAgentAccessToken(req.Token) || len([]rune(req.TargetAgentName)) > 200 {
			writeError(w, http.StatusBadRequest, "invalid registration token or agent name")
			return
		}
		targetClientID, ok := parseUUIDOrBadRequest(w, req.TargetClientID, "target_client_id")
		if !ok {
			return
		}
		sealed, err := h.A2AService.PushSecrets.Seal([]byte(req.Token))
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to protect the forward credential")
			return
		}
		applied, err := h.Queries.UpsertA2AForwardRegistration(r.Context(), db.UpsertA2AForwardRegistrationParams{
			DwsUid:          req.DWSUID,
			OrgID:           req.OrgID,
			RpcUrl:          rpcURL,
			TargetClientID:  targetClientID,
			TokenEncrypted:  sealed,
			TokenSha256:     agentA2AForwardTokenSHA256(req.Token),
			TargetAgentName: strings.TrimSpace(req.TargetAgentName),
			SignedAtMs:      signedAtMs,
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to save registration")
			return
		}
		if applied == 0 {
			writeError(w, http.StatusConflict, "a newer registration state exists for this identity")
			return
		}
	case agentA2AForwardRegistrationActionClear:
		if !agentA2AForwardSHA256Hex.MatchString(req.TokenSHA256) {
			writeError(w, http.StatusBadRequest, "token_sha256 is required to withdraw a registration")
			return
		}
		if err := h.Queries.RevokeA2AForwardRegistration(r.Context(), db.RevokeA2AForwardRegistrationParams{
			DwsUid:      req.DWSUID,
			OrgID:       req.OrgID,
			RpcUrl:      rpcURL,
			TokenSha256: req.TokenSHA256,
			SignedAtMs:  signedAtMs,
		}); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to clear registration")
			return
		}
	default:
		writeError(w, http.StatusBadRequest, "action must be register or revoke")
		return
	}
	slog.Info("A2A forward registration changed",
		"event", "a2a_forward_registration",
		"action", req.Action,
		"dws_uid", req.DWSUID,
		"org_id", req.OrgID,
		"rpc_url", rpcURL,
	)
	writeJSON(w, http.StatusOK, map[string]string{"status": req.Action})
}

func absDuration(value time.Duration) time.Duration {
	if value < 0 {
		return -value
	}
	return value
}

// agentA2AForwardDesired is the registration a pre-release Agent should hold
// right now. UID/OrgID are the identity A2A uses; Registrable=false with a
// Reason means the switch is on but something else is missing.
type agentA2AForwardDesired struct {
	Accept      bool
	Registrable bool
	Reason      string
	UID         string
	OrgID       string
	RPCURL      string
}

// loadAgentA2AForwardDesired derives the desired registration from the
// operator switches, the shared DingTalk identity and the A2A endpoint. Read
// failures return an error so a transient fault never withdraws a working
// registration.
func (h *Handler) loadAgentA2AForwardDesired(ctx context.Context, agent db.Agent) (agentA2AForwardDesired, error) {
	config, err := h.Queries.GetAgentA2AOperatorConfig(ctx, db.GetAgentA2AOperatorConfigParams{
		WorkspaceID: agent.WorkspaceID,
		AgentID:     agent.ID,
	})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return agentA2AForwardDesired{}, err
	}
	desired := agentA2AForwardDesired{Accept: errors.Is(err, pgx.ErrNoRows) || config.AcceptProdForward}
	if !desired.Accept {
		return desired, nil
	}
	if !config.A2aIdentityEnabled {
		desired.Reason = "bind a digital employee identity in operator settings"
		return desired, nil
	}
	identity, err := h.Queries.GetAgentDingTalkIdentity(ctx, db.GetAgentDingTalkIdentityParams{
		WorkspaceID: agent.WorkspaceID,
		AgentID:     agent.ID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		desired.Reason = "bind a digital employee identity in operator settings"
		return desired, nil
	}
	if err != nil {
		return agentA2AForwardDesired{}, err
	}
	desired.UID = identity.DwsUid
	desired.OrgID = identity.OrgID
	if agent.ArchivedAt.Valid {
		desired.Reason = "the Agent is archived"
		return desired, nil
	}
	endpoint, err := h.Queries.GetAgentA2AEndpointByAgent(ctx, agent.ID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !endpoint.Enabled) {
		desired.Reason = "enable A2A on this Agent to accept production forwards"
		return desired, nil
	}
	if err != nil {
		return agentA2AForwardDesired{}, err
	}
	baseURL, err := normalizeAgentA2APublicBaseURL(h.currentConfig().PublicURL)
	if err != nil {
		desired.Reason = "public URL is not configured"
		return desired, nil
	}
	rpcURL, err := a2aintegration.AgentRPCURL(baseURL, endpoint.PublicAgentID)
	if err != nil {
		desired.Reason = err.Error()
		return desired, nil
	}
	desired.Registrable = true
	desired.RPCURL = rpcURL
	return desired, nil
}

// agentA2AForwardRegistrantCurrent reports whether a registrant row holds the
// desired registration.
func agentA2AForwardRegistrantCurrent(row db.AgentA2aForwardRegistrant, desired agentA2AForwardDesired) bool {
	return row.RegisteredAt.Valid && desired.Registrable &&
		row.ClientUid == desired.UID &&
		row.ClientOrgID == desired.OrgID &&
		row.RegisteredRpcUrl.String == desired.RPCURL
}

// syncAgentA2AForwardRegistration brings this pre-release Agent's production
// registrations in line with its identity, the operator switches and its A2A
// endpoint. force re-registers with a fresh key even when a registry already
// holds the current identity. Syncs of one Agent are serialized across
// replicas, and each re-reads the state after taking the lock. Failures are
// recorded per registry and shown to the operator; they never fail the change
// that triggered the sync.
func (h *Handler) syncAgentA2AForwardRegistration(parent context.Context, scope agentA2AManagementScope, force bool) {
	if !h.agentA2AForwardRegistrant() || h.TxStarter == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), agentA2AForwardSyncTimeout)
	defer cancel()
	agentID := uuidToString(scope.Agent.ID)
	lock, err := h.lockAgentA2AForwardSync(ctx, agentID)
	if err != nil {
		slog.Warn("A2A forward registration sync could not lock the Agent", "agent_id", agentID, "error", err)
		return
	}
	defer func() { _ = lock.Rollback(ctx) }()

	agent, err := h.Queries.GetAgent(ctx, scope.Agent.ID)
	if err != nil {
		slog.Warn("A2A forward registration sync could not load the Agent", "agent_id", agentID, "error", err)
		return
	}
	scope.Agent = agent
	scope.OwnerUserID = agent.OwnerID
	desired, err := h.loadAgentA2AForwardDesired(ctx, agent)
	if err != nil {
		slog.Warn("A2A forward registration sync could not read settings", "agent_id", agentID, "error", err)
		return
	}
	rows, err := h.Queries.ListAgentA2AForwardRegistrants(ctx, db.ListAgentA2AForwardRegistrantsParams{
		AgentID:     scope.Agent.ID,
		WorkspaceID: scope.WorkspaceID,
	})
	if err != nil {
		slog.Warn("A2A forward registration sync could not read registrations", "agent_id", agentID, "error", err)
		return
	}
	existing := make(map[string]db.AgentA2aForwardRegistrant, len(rows))
	for _, row := range rows {
		existing[row.RegistryOrigin] = row
	}
	configured := normalizedAgentA2AForwardOrigins(h.currentConfig().A2AForwardRegistryURLs)
	origins := append([]string(nil), configured...)
	for _, row := range rows {
		if !slices.Contains(configured, row.RegistryOrigin) {
			// A registry removed from the configuration still gets its
			// registration withdrawn.
			origins = append(origins, row.RegistryOrigin)
		}
	}
	for _, origin := range origins {
		row, exists := existing[origin]
		wanted := desired
		if !slices.Contains(configured, origin) {
			wanted.Registrable = false
		}
		h.syncAgentA2AForwardRegistry(ctx, scope, origin, row, exists, wanted, force)
	}
}

// lockAgentA2AForwardSync takes the Agent's sync lock. It polls with
// pg_try_advisory_xact_lock so a waiting sync never parks a pooled connection
// the lock holder may need.
func (h *Handler) lockAgentA2AForwardSync(ctx context.Context, agentID string) (pgx.Tx, error) {
	for {
		tx, err := h.TxStarter.Begin(ctx)
		if err != nil {
			return nil, err
		}
		var locked bool
		if err := tx.QueryRow(ctx, "SELECT pg_try_advisory_xact_lock(hashtextextended($1, 0))", "a2a_forward_registrant:"+agentID).Scan(&locked); err != nil {
			_ = tx.Rollback(ctx)
			return nil, err
		}
		if locked {
			return tx, nil
		}
		_ = tx.Rollback(ctx)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(agentA2AForwardSyncLockRetry):
		}
	}
}

func (h *Handler) syncAgentA2AForwardRegistry(
	ctx context.Context,
	scope agentA2AManagementScope,
	origin string,
	row db.AgentA2aForwardRegistrant,
	exists bool,
	desired agentA2AForwardDesired,
	force bool,
) {
	current := exists && agentA2AForwardRegistrantCurrent(row, desired)
	sameEmployee := exists && desired.UID != "" && row.ClientUid == desired.UID && row.ClientOrgID == desired.OrgID
	state := db.SaveAgentA2AForwardRegistrantParams{
		AgentID:               scope.Agent.ID,
		RegistryOrigin:        origin,
		WorkspaceID:           scope.WorkspaceID,
		ClientID:              row.ClientID,
		ClientUid:             row.ClientUid,
		ClientOrgID:           row.ClientOrgID,
		RegisteredRpcUrl:      row.RegisteredRpcUrl,
		RegisteredTokenSha256: row.RegisteredTokenSha256,
		RegisteredAt:          row.RegisteredAt,
	}
	var problems []string
	save := func() error {
		state.LastError = pgtype.Text{}
		if len(problems) > 0 {
			state.LastError = pgtype.Text{String: truncateAgentA2AText(strings.Join(problems, "; "), 1000), Valid: true}
		}
		err := h.Queries.SaveAgentA2AForwardRegistrant(ctx, state)
		if err != nil {
			slog.Warn("save A2A forward registration state failed", "agent_id", uuidToString(scope.Agent.ID), "registry", origin, "error", err)
		}
		return err
	}
	clearRegistration := func() {
		state.RegisteredRpcUrl = pgtype.Text{}
		state.RegisteredTokenSha256 = pgtype.Text{}
		state.RegisteredAt = pgtype.Timestamptz{}
	}

	if exists && row.RegisteredAt.Valid && !current {
		// Stop honoring the stale key here first. Should the withdrawal not
		// arrive, production gets the rejected-key 401, retires the
		// registration and serves the call itself.
		h.revokeAgentA2AForwardCredentials(ctx, scope, row.ClientID, pgtype.UUID{})
		if problem, _ := h.postAgentA2AForwardRegistration(ctx, origin, agentA2AForwardRegistrationRequest{
			Action:      agentA2AForwardRegistrationActionClear,
			DWSUID:      row.ClientUid,
			OrgID:       row.ClientOrgID,
			RPCURL:      row.RegisteredRpcUrl.String,
			TokenSHA256: row.RegisteredTokenSha256.String,
		}); problem != "" {
			problems = append(problems, "withdraw: "+problem)
		}
		clearRegistration()
	}
	if !desired.Registrable {
		if exists {
			_ = save()
		}
		return
	}
	if current && !force && !row.LastError.Valid {
		return
	}
	if !sameEmployee {
		// Another employee's traffic must never reach the tasks of the
		// previous one, so it gets a new client and the old one is retired.
		if exists {
			h.retireAgentA2AForwardClient(ctx, scope, row.ClientID)
		}
		state.ClientID = pgtype.UUID{}
	}
	clientID, credentialID, token, err := h.mintAgentA2AForwardCredential(ctx, scope, state.ClientID, desired.UID, desired.OrgID)
	if err != nil {
		problems = append(problems, "mint forward key: "+err.Error())
		if exists {
			// Keep the row's previous (possibly just retired) client; the
			// next sync starts over from it.
			state.ClientID = row.ClientID
			_ = save()
		}
		return
	}
	// Record the client and its employee before the registry can hand out the
	// new key; without that record the key is never published.
	state.ClientID = clientID
	state.ClientUid = desired.UID
	state.ClientOrgID = desired.OrgID
	if err := save(); err != nil {
		h.revokeAgentA2AForwardCredential(ctx, scope, clientID, credentialID)
		return
	}

	problem, definite := h.postAgentA2AForwardRegistration(ctx, origin, agentA2AForwardRegistrationRequest{
		Action:          agentA2AForwardRegistrationActionSet,
		DWSUID:          desired.UID,
		OrgID:           desired.OrgID,
		RPCURL:          desired.RPCURL,
		TargetClientID:  uuidToString(clientID),
		Token:           token,
		TargetAgentName: scope.Agent.Name,
	})
	registered := func() {
		state.RegisteredRpcUrl = pgtype.Text{String: desired.RPCURL, Valid: true}
		state.RegisteredTokenSha256 = pgtype.Text{String: agentA2AForwardTokenSHA256(token), Valid: true}
		state.RegisteredAt = pgtype.Timestamptz{Time: time.Now(), Valid: true}
	}
	switch {
	case problem == "":
		// Only the key the registry now holds stays valid.
		h.revokeAgentA2AForwardCredentials(ctx, scope, clientID, credentialID)
		registered()
	case definite:
		// The registry refused before storing anything, so it keeps the
		// previous key (if any), which stays valid; only the unused new key
		// goes.
		h.revokeAgentA2AForwardCredential(ctx, scope, clientID, credentialID)
		problems = append(problems, "register: "+problem)
	default:
		// No answer, or a server error that may have come after the write:
		// the registry may hold either key, so both stay valid. The recorded
		// error makes the next sync register again and retire the rest.
		registered()
		problems = append(problems, "register (unconfirmed): "+problem)
	}
	_ = save()
	slog.Info("A2A forward registration synced",
		"event", "a2a_forward_registration_sync",
		"agent_id", uuidToString(scope.Agent.ID),
		"registry", origin,
		"registered", state.RegisteredAt.Valid,
		"problems", len(problems),
	)
}

// mintAgentA2AForwardCredential issues a new key on the registry's dedicated
// client for the employee uid/orgID. A missing client (first use, or someone
// revoked it) is created first, together with its ownership record, which must
// exist before the client has any key. The client stays the same across
// rotations so forwarded tasks stay reachable.
func (h *Handler) mintAgentA2AForwardCredential(
	ctx context.Context,
	scope agentA2AManagementScope,
	clientID pgtype.UUID,
	uid string,
	orgID string,
) (pgtype.UUID, pgtype.UUID, string, error) {
	rawToken, err := auth.GenerateA2AToken()
	if err != nil {
		return pgtype.UUID{}, pgtype.UUID{}, "", err
	}
	create := func(client pgtype.UUID) (db.CreateAgentA2ACredentialForOwnerRow, error) {
		return h.Queries.CreateAgentA2ACredentialForOwner(ctx, db.CreateAgentA2ACredentialForOwnerParams{
			KeyID:       uuid.NewString(),
			TokenHash:   auth.HashToken(rawToken),
			TokenPrefix: agentA2ATokenPrefix(rawToken),
			OwnerUserID: scope.OwnerUserID,
			ActorUserID: scope.ActorUserID,
			ClientID:    client,
			WorkspaceID: scope.WorkspaceID,
			AgentID:     scope.Agent.ID,
		})
	}
	if clientID.Valid {
		credential, err := create(clientID)
		if err == nil {
			return clientID, credential.ID, rawToken, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return pgtype.UUID{}, pgtype.UUID{}, "", err
		}
		h.retireAgentA2AForwardClient(ctx, scope, clientID)
	}
	scopes, err := normalizeAgentA2AScopes(append([]string(nil), defaultAgentA2AScopes...))
	if err != nil {
		return pgtype.UUID{}, pgtype.UUID{}, "", err
	}
	client, err := h.Queries.CreateAgentA2AClientForOwner(ctx, db.CreateAgentA2AClientForOwnerParams{
		Name:        agentA2AForwardRegistrationClientName,
		Scopes:      scopes,
		OwnerUserID: scope.OwnerUserID,
		ActorUserID: scope.ActorUserID,
		WorkspaceID: scope.WorkspaceID,
		AgentID:     scope.Agent.ID,
	})
	if err != nil {
		return pgtype.UUID{}, pgtype.UUID{}, "", err
	}
	if err := h.Queries.CreateAgentA2AForwardClient(ctx, db.CreateAgentA2AForwardClientParams{
		ClientID:    client.ID,
		AgentID:     scope.Agent.ID,
		WorkspaceID: scope.WorkspaceID,
		DwsUid:      uid,
		OrgID:       orgID,
	}); err != nil {
		h.revokeAgentA2AForwardClient(ctx, scope, client.ID)
		return pgtype.UUID{}, pgtype.UUID{}, "", err
	}
	credential, err := create(client.ID)
	if err != nil {
		return pgtype.UUID{}, pgtype.UUID{}, "", err
	}
	return client.ID, credential.ID, rawToken, nil
}

// revokeAgentA2AForwardCredentials revokes every active key of the client
// except keep.
func (h *Handler) revokeAgentA2AForwardCredentials(
	ctx context.Context,
	scope agentA2AManagementScope,
	clientID pgtype.UUID,
	keep pgtype.UUID,
) {
	if !clientID.Valid {
		return
	}
	credentials, err := h.Queries.ListAgentA2ACredentialsForOwner(ctx, db.ListAgentA2ACredentialsForOwnerParams{
		OwnerUserID: scope.OwnerUserID,
		ClientID:    clientID,
		WorkspaceID: scope.WorkspaceID,
		AgentID:     scope.Agent.ID,
	})
	if err != nil {
		slog.Warn("list A2A forward keys failed", "agent_id", uuidToString(scope.Agent.ID), "error", err)
		return
	}
	for _, credential := range credentials {
		if credential.Status != "active" || (keep.Valid && credential.ID == keep) {
			continue
		}
		h.revokeAgentA2AForwardCredential(ctx, scope, clientID, credential.ID)
	}
}

// retireAgentA2AForwardClient marks a forward client as replaced, so its keys
// are refused even if revoking them fails, and then revokes it.
func (h *Handler) retireAgentA2AForwardClient(ctx context.Context, scope agentA2AManagementScope, clientID pgtype.UUID) {
	if !clientID.Valid {
		return
	}
	if err := h.Queries.RetireAgentA2AForwardClient(ctx, db.RetireAgentA2AForwardClientParams{
		ClientID: clientID,
		AgentID:  scope.Agent.ID,
	}); err != nil {
		slog.Warn("retire previous A2A forward client failed", "agent_id", uuidToString(scope.Agent.ID), "error", err)
	}
	h.revokeAgentA2AForwardClient(ctx, scope, clientID)
}

// revokeAgentA2AForwardClient revokes a forward client and all its keys.
func (h *Handler) revokeAgentA2AForwardClient(ctx context.Context, scope agentA2AManagementScope, clientID pgtype.UUID) {
	if !clientID.Valid {
		return
	}
	if _, err := h.Queries.RevokeAgentA2AClientForOwner(ctx, db.RevokeAgentA2AClientForOwnerParams{
		ActorUserID: scope.ActorUserID,
		ClientID:    clientID,
		WorkspaceID: scope.WorkspaceID,
		AgentID:     scope.Agent.ID,
		OwnerUserID: scope.OwnerUserID,
	}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		slog.Warn("revoke previous A2A forward client failed", "agent_id", uuidToString(scope.Agent.ID), "error", err)
	}
}

func (h *Handler) revokeAgentA2AForwardCredential(
	ctx context.Context,
	scope agentA2AManagementScope,
	clientID pgtype.UUID,
	credentialID pgtype.UUID,
) {
	if _, err := h.Queries.RevokeAgentA2ACredentialForOwner(ctx, db.RevokeAgentA2ACredentialForOwnerParams{
		ActorUserID:  scope.ActorUserID,
		CredentialID: credentialID,
		ClientID:     clientID,
		WorkspaceID:  scope.WorkspaceID,
		AgentID:      scope.Agent.ID,
		OwnerUserID:  scope.OwnerUserID,
	}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		slog.Warn("revoke A2A forward key failed", "agent_id", uuidToString(scope.Agent.ID), "error", err)
	}
}

// agentA2AForwardCredentialStale reports whether an authenticated credential
// belongs to a production-forward client this Agent no longer honors: the
// client was replaced by another employee's, it was made for another employee
// than the one A2A uses now (for example after a rebinding on the Integrations
// page), or the switch is off. The endpoint and archive state are left to the
// A2A handler, which still serves reads and cancels of forwarded tasks after
// either changes. A read failure returns an error so the caller answers 503
// instead of retiring the registration.
func (h *Handler) agentA2AForwardCredentialStale(ctx context.Context, credential db.GetAgentA2ACredentialByTokenHashRow) (bool, error) {
	if !h.agentA2AForwardRegistrant() {
		return false, nil
	}
	owner, err := h.Queries.GetAgentA2AForwardClient(ctx, db.GetAgentA2AForwardClientParams{
		ClientID: credential.ClientID,
		AgentID:  credential.AgentID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if owner.RetiredAt.Valid {
		return true, nil
	}
	agent, err := h.Queries.GetAgent(ctx, credential.AgentID)
	if err != nil {
		return false, err
	}
	desired, err := h.loadAgentA2AForwardDesired(ctx, agent)
	if err != nil {
		return false, err
	}
	return !desired.Accept || desired.UID != owner.DwsUid || desired.OrgID != owner.OrgID, nil
}

// agentA2AForwardKeyRevoked reports whether a key that failed authentication
// was revoked here, which on a registrant deployment is how a withdrawn
// production-forward key looks.
func (h *Handler) agentA2AForwardKeyRevoked(ctx context.Context, tokenHash string) bool {
	if !h.agentA2AForwardRegistrant() {
		return false
	}
	revoked, err := h.Queries.IsA2ACredentialRevoked(ctx, tokenHash)
	return err == nil && revoked
}

// postAgentA2AForwardRegistration sends one signed request to a registry. It
// returns a readable problem when the request was not accepted, and whether
// the registry definitely refused it (a 4xx answer) rather than leaving the
// outcome unknown (no answer, or a server error).
func (h *Handler) postAgentA2AForwardRegistration(ctx context.Context, registry string, req agentA2AForwardRegistrationRequest) (string, bool) {
	body, err := json.Marshal(req)
	if err != nil {
		return "encode registration: " + err.Error(), true
	}
	timestamp := strconv.FormatInt(time.Now().UnixMilli(), 10)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, registry+agentA2AForwardRegistrationPath, bytes.NewReader(body))
	if err != nil {
		return registry + ": " + err.Error(), true
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(agentA2AForwardTimestampHeader, timestamp)
	request.Header.Set(agentA2AForwardSignatureHeader, signAgentA2AForwardRegistration(h.agentA2AForwardSecret(), timestamp, body))
	response, err := agentA2AForwardRegistrationHTTPClient.Do(request)
	if err != nil {
		return registry + ": " + err.Error(), false
	}
	detail, _ := io.ReadAll(io.LimitReader(response.Body, 300))
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		// The registry validates and refuses with 4xx before it writes; a 5xx
		// (or anything else) may come after the write.
		refused := response.StatusCode >= 400 && response.StatusCode < 500
		return fmt.Sprintf("%s: HTTP %d %s", registry, response.StatusCode, strings.TrimSpace(string(detail))), refused
	}
	return "", true
}

func truncateAgentA2AText(value string, maxRunes int) string {
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	return string(runes[:maxRunes]) + "…"
}
