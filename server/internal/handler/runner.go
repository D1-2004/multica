package handler

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/realtime"
	"github.com/multica-ai/multica/server/internal/runnerws"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/runnerprotocol"
)

const (
	runnerPairingTTL   = 10 * time.Minute
	runnerChallengeTTL = 90 * time.Second
	runnerOnlineTTL    = 45 * time.Second
	runnerMaxRoots     = 32
	runnerMaxRootBytes = 4096
	runnerMaxResult    = 2 << 20

	runnerBindingAuthorizedActivity = "runner_binding_authorized"
	runnerBindingRevokedActivity    = "runner_binding_revoked"
)

type runnerBindingResponse struct {
	BindingID     string   `json:"binding_id"`
	MachineID     string   `json:"machine_id"`
	Name          string   `json:"name"`
	OS            string   `json:"os"`
	Arch          string   `json:"arch"`
	ClientVersion string   `json:"client_version"`
	Roots         []string `json:"roots"`
	Online        bool     `json:"online"`
	LastSeenAt    *string  `json:"last_seen_at"`
	BoundAt       string   `json:"bound_at"`
}

func generateRunnerSecret(prefix string, byteCount int) (string, error) {
	raw := make([]byte, byteCount)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(raw), nil
}

func runnerPairingIDFromToken(token string) (pgtype.UUID, bool) {
	token = strings.TrimSpace(token)
	if !strings.HasPrefix(token, "rps_") {
		return pgtype.UUID{}, false
	}
	parts := strings.SplitN(strings.TrimPrefix(token, "rps_"), "_", 2)
	if len(parts) != 2 || len(parts[1]) != 48 {
		return pgtype.UUID{}, false
	}
	if _, err := hex.DecodeString(parts[1]); err != nil {
		return pgtype.UUID{}, false
	}
	pairingID, err := util.ParseUUID(parts[0])
	return pairingID, err == nil
}

func logRunnerDeviceAuthorizationRejected(reason string, pairingID pgtype.UUID, attributes ...any) {
	fields := []any{
		"event", "runner_device_authorization_rejected",
		"reason", reason,
	}
	if pairingID.Valid {
		fields = append(fields, "pairing_id", uuidToString(pairingID))
	}
	fields = append(fields, attributes...)
	slog.Warn("Runner device authorization rejected", fields...)
}

func generateRunnerUserCode() (string, error) {
	raw := make([]byte, 5)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	encoded := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw)
	return encoded[:4] + "-" + encoded[4:8], nil
}

func runnerBaseURL(raw string) (string, error) {
	value := strings.TrimRight(strings.TrimSpace(raw), "/")
	parsed, err := url.ParseRequestURI(value)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil || parsed.EscapedPath() != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("Runner URL must be an HTTP(S) origin without credentials, query, or fragment")
	}
	return value, nil
}

func runnerShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func decodeRunnerRequest(w http.ResponseWriter, r *http.Request, limit int64, out any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("request body must contain one JSON value")
	}
	return nil
}

func decodeRunnerRoots(raw []byte) []string {
	var roots []string
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &roots)
	}
	if roots == nil {
		return []string{}
	}
	return roots
}

func validateRunnerRoots(roots []string) error {
	if len(roots) == 0 || len(roots) > runnerMaxRoots {
		return fmt.Errorf("roots must contain 1 to %d paths", runnerMaxRoots)
	}
	seen := make(map[string]struct{}, len(roots))
	for _, root := range roots {
		if root == "" || len(root) > runnerMaxRootBytes || !strings.HasPrefix(root, "/") || strings.ContainsRune(root, '\x00') {
			return errors.New("roots must be absolute paths")
		}
		if _, exists := seen[root]; exists {
			return errors.New("roots must not contain duplicates")
		}
		seen[root] = struct{}{}
	}
	return nil
}

func (h *Handler) requireRunnerAgentOwner(w http.ResponseWriter, r *http.Request) (db.Agent, bool) {
	agent, ok := h.loadAgentForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return db.Agent{}, false
	}
	if _, ok := h.requireWorkspaceRole(w, r, uuidToString(agent.WorkspaceID), "agent not found", "owner", "admin", "member"); !ok {
		return db.Agent{}, false
	}
	if requestUserID(r) != uuidToString(agent.OwnerID) {
		writeError(w, http.StatusForbidden, "only the agent owner can bind a Runner")
		return db.Agent{}, false
	}
	return agent, true
}

func (h *Handler) CreateAgentRunnerPairing(w http.ResponseWriter, r *http.Request) {
	agent, ok := h.requireRunnerAgentOwner(w, r)
	if !ok {
		return
	}
	publicURL, err := runnerBaseURL(h.currentConfig().PublicURL)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "Runner binding requires MULTICA_PUBLIC_URL")
		return
	}
	secret, err := generateRunnerSecret("", 24)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create Runner pairing")
		return
	}
	pairingID := pgtype.UUID{Bytes: uuid.New(), Valid: true}
	token := "rps_" + uuidToString(pairingID) + "_" + secret
	expiresAt := time.Now().Add(runnerPairingTTL)
	row, err := h.Queries.CreateRunnerPairingSession(r.Context(), db.CreateRunnerPairingSessionParams{
		ID:               pairingID,
		WorkspaceID:      agent.WorkspaceID,
		AgentID:          agent.ID,
		OwnerID:          agent.OwnerID,
		PairingTokenHash: auth.HashToken(token),
		ExpiresAt:        pgtype.Timestamptz{Time: expiresAt, Valid: true},
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create Runner pairing")
		return
	}
	slog.Info("Runner pairing created",
		"event", "runner_pairing_created",
		"pairing_id", uuidToString(row.ID),
		"workspace_id", uuidToString(agent.WorkspaceID),
		"agent_id", uuidToString(agent.ID),
		"owner_id", uuidToString(agent.OwnerID),
	)
	command := fmt.Sprintf(
		"curl -fsSL %s | sh -s -- --server-url %s --pairing-token %s",
		runnerShellQuote(publicURL+"/api/runner/install"),
		runnerShellQuote(publicURL),
		runnerShellQuote(token),
	)
	writeJSON(w, http.StatusCreated, map[string]any{
		"id":              uuidToString(row.ID),
		"install_command": command,
		"expires_at":      expiresAt.UTC().Format(time.RFC3339),
	})
}

func (h *Handler) ListAgentRunnerBindings(w http.ResponseWriter, r *http.Request) {
	agent, ok := h.loadAgentForUser(w, r, chi.URLParam(r, "id"))
	if !ok || !h.canManageAgent(w, r, agent) {
		return
	}
	rows, err := h.Queries.ListAgentRunnerBindings(r.Context(), db.ListAgentRunnerBindingsParams{
		WorkspaceID: agent.WorkspaceID,
		AgentID:     agent.ID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list Runner bindings")
		return
	}
	now := time.Now()
	items := make([]runnerBindingResponse, 0, len(rows))
	for _, row := range rows {
		item := runnerBindingResponse{
			BindingID:     uuidToString(row.BindingID),
			MachineID:     uuidToString(row.MachineID),
			Name:          row.Name,
			OS:            row.Os,
			Arch:          row.Arch,
			ClientVersion: row.ClientVersion,
			Roots:         decodeRunnerRoots(row.Roots),
			Online:        row.LastSeenAt.Valid && now.Sub(row.LastSeenAt.Time) <= runnerOnlineTTL,
			BoundAt:       row.BoundAt.Time.UTC().Format(time.RFC3339),
		}
		if row.LastSeenAt.Valid {
			value := row.LastSeenAt.Time.UTC().Format(time.RFC3339)
			item.LastSeenAt = &value
		}
		items = append(items, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"machines": items})
}

func (h *Handler) RevokeAgentRunnerBinding(w http.ResponseWriter, r *http.Request) {
	agent, ok := h.loadAgentForUser(w, r, chi.URLParam(r, "id"))
	if !ok || !h.canManageAgent(w, r, agent) {
		return
	}
	bindingID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "bindingId"), "Runner binding id")
	if !ok {
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to revoke Runner binding")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	qtx := h.Queries.WithTx(tx)
	revoked, err := qtx.RevokeAgentRunnerBinding(r.Context(), db.RevokeAgentRunnerBindingParams{
		ID:        bindingID,
		AgentID:   agent.ID,
		RevokedBy: parseUUID(requestUserID(r)),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "Runner binding not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to revoke Runner binding")
		return
	}
	expiredCalls, err := qtx.ExpireRunnerCallsForBinding(r.Context(), db.ExpireRunnerCallsForBindingParams{
		AgentID: agent.ID, MachineID: revoked.MachineID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to revoke active Runner calls")
		return
	}
	details, _ := json.Marshal(map[string]any{
		"agent_id":   uuidToString(agent.ID),
		"binding_id": uuidToString(revoked.ID),
		"machine_id": uuidToString(revoked.MachineID),
	})
	if _, err := qtx.CreateActivity(r.Context(), db.CreateActivityParams{
		WorkspaceID: agent.WorkspaceID,
		IssueID:     pgtype.UUID{},
		ActorType:   pgtype.Text{String: "member", Valid: true},
		ActorID:     parseUUID(requestUserID(r)),
		Action:      runnerBindingRevokedActivity,
		Details:     details,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to audit Runner revocation")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to revoke Runner binding")
		return
	}
	slog.Info("Runner binding revoked",
		"event", "runner_binding_revoked",
		"workspace_id", uuidToString(agent.WorkspaceID),
		"agent_id", uuidToString(agent.ID),
		"binding_id", uuidToString(revoked.ID),
		"machine_id", uuidToString(revoked.MachineID),
		"actor_id", requestUserID(r),
		"expired_call_count", len(expiredCalls),
	)
	w.WriteHeader(http.StatusNoContent)
}

type beginRunnerDeviceRequest struct {
	PairingToken  string   `json:"pairing_token"`
	PublicKey     string   `json:"public_key"`
	MachineName   string   `json:"machine_name"`
	OS            string   `json:"os"`
	Arch          string   `json:"arch"`
	ClientVersion string   `json:"client_version"`
	Roots         []string `json:"roots"`
}

func (h *Handler) BeginRunnerDeviceAuthorization(w http.ResponseWriter, r *http.Request) {
	appURL, err := runnerBaseURL(h.currentConfig().AppURL)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "Runner authorization requires MULTICA_APP_URL")
		return
	}
	var req beginRunnerDeviceRequest
	if err := decodeRunnerRequest(w, r, 64<<10, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	publicKey, err := base64.RawURLEncoding.DecodeString(req.PublicKey)
	if err != nil || len(publicKey) != ed25519.PublicKeySize {
		writeError(w, http.StatusBadRequest, "public_key must be an Ed25519 public key")
		return
	}
	req.MachineName = strings.TrimSpace(req.MachineName)
	if req.MachineName == "" || len(req.MachineName) > 120 {
		writeError(w, http.StatusBadRequest, "machine_name is required")
		return
	}
	if req.OS != "darwin" && req.OS != "linux" {
		writeError(w, http.StatusBadRequest, "unsupported Runner operating system")
		return
	}
	if req.Arch != "amd64" && req.Arch != "arm64" {
		writeError(w, http.StatusBadRequest, "unsupported Runner architecture")
		return
	}
	req.ClientVersion = strings.TrimSpace(req.ClientVersion)
	if len(req.ClientVersion) > 64 {
		writeError(w, http.StatusBadRequest, "client_version is too long")
		return
	}
	if err := validateRunnerRoots(req.Roots); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	roots, _ := json.Marshal(req.Roots)
	pairingID, ok := runnerPairingIDFromToken(req.PairingToken)
	if !ok {
		logRunnerDeviceAuthorizationRejected("token_format", pgtype.UUID{})
		writeError(w, http.StatusBadRequest, "Runner pairing token is invalid or expired")
		return
	}
	pairing, err := h.Queries.GetRunnerPairingByID(r.Context(), pairingID)
	if errors.Is(err, pgx.ErrNoRows) {
		logRunnerDeviceAuthorizationRejected("pairing_not_found", pairingID)
		writeError(w, http.StatusBadRequest, "Runner pairing token is invalid or expired")
		return
	}
	if err != nil {
		slog.Error("Failed to read Runner pairing for device authorization",
			"event", "runner_device_authorization_failed",
			"reason", "pairing_lookup",
			"pairing_id", uuidToString(pairingID),
			"error", err,
		)
		writeError(w, http.StatusInternalServerError, "failed to begin Runner authorization")
		return
	}
	expectedHash := auth.HashToken(strings.TrimSpace(req.PairingToken))
	if subtle.ConstantTimeCompare([]byte(expectedHash), []byte(pairing.PairingTokenHash)) != 1 {
		logRunnerDeviceAuthorizationRejected("token_hash_mismatch", pairing.ID)
		writeError(w, http.StatusBadRequest, "Runner pairing token is invalid or expired")
		return
	}
	if pairing.State != "pending" {
		logRunnerDeviceAuthorizationRejected("pairing_state", pairing.ID, "pairing_state", pairing.State)
		writeError(w, http.StatusBadRequest, "Runner pairing token is invalid or expired")
		return
	}
	if !pairing.ExpiresAt.Valid {
		logRunnerDeviceAuthorizationRejected("pairing_expiry_missing", pairing.ID)
		writeError(w, http.StatusBadRequest, "Runner pairing token is invalid or expired")
		return
	}
	if !pairing.ExpiresAt.Time.After(time.Now()) {
		logRunnerDeviceAuthorizationRejected("pairing_expired", pairing.ID)
		writeError(w, http.StatusBadRequest, "Runner pairing token is invalid or expired")
		return
	}
	pairingID = pairing.ID
	deviceCode, err := generateRunnerSecret("rdc_", 24)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to begin Runner authorization")
		return
	}

	var authorizedPairing db.RunnerPairingSession
	for attempt := 0; attempt < 5; attempt++ {
		userCode, codeErr := generateRunnerUserCode()
		if codeErr != nil {
			writeError(w, http.StatusInternalServerError, "failed to begin Runner authorization")
			return
		}
		authorizedPairing, err = h.Queries.BeginRunnerDeviceAuthorization(r.Context(), db.BeginRunnerDeviceAuthorizationParams{
			ID:             pairingID,
			DeviceCodeHash: pgtype.Text{String: auth.HashToken(deviceCode), Valid: true},
			UserCode:       pgtype.Text{String: userCode, Valid: true},
			PublicKey:      publicKey,
			MachineName:    pgtype.Text{String: req.MachineName, Valid: true},
			Os:             pgtype.Text{String: req.OS, Valid: true},
			Arch:           pgtype.Text{String: req.Arch, Valid: true},
			ClientVersion:  req.ClientVersion,
			Roots:          roots,
		})
		if err == nil {
			break
		}
		if !isUniqueViolation(err) {
			break
		}
	}
	if errors.Is(err, pgx.ErrNoRows) {
		logRunnerDeviceAuthorizationRejected("state_transition_conflict", pairing.ID)
		writeError(w, http.StatusBadRequest, "Runner pairing token is invalid or expired")
		return
	}
	if err != nil {
		slog.Error("Failed to transition Runner pairing into device authorization",
			"event", "runner_device_authorization_failed",
			"reason", "state_transition",
			"pairing_id", uuidToString(pairing.ID),
			"error", err,
		)
		writeError(w, http.StatusInternalServerError, "failed to begin Runner authorization")
		return
	}
	pairing = authorizedPairing
	slog.Info("Runner device authorization started",
		"event", "runner_device_authorization_started",
		"pairing_id", uuidToString(pairing.ID),
		"agent_id", uuidToString(pairing.AgentID),
		"machine_os", req.OS,
		"machine_arch", req.Arch,
		"root_count", len(req.Roots),
	)
	verificationURI := appURL + "/runners/authorize"
	writeJSON(w, http.StatusOK, map[string]any{
		"device_code":               deviceCode,
		"user_code":                 pairing.UserCode.String,
		"verification_uri":          verificationURI,
		"verification_uri_complete": verificationURI + "?code=" + pairing.UserCode.String,
		"expires_in":                int(time.Until(pairing.ExpiresAt.Time).Seconds()),
		"interval":                  2,
	})
}

func (h *Handler) PollRunnerDeviceAuthorization(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DeviceCode string `json:"device_code"`
	}
	if err := decodeRunnerRequest(w, r, 8<<10, &req); err != nil || strings.TrimSpace(req.DeviceCode) == "" {
		writeError(w, http.StatusBadRequest, "device_code is required")
		return
	}
	pairing, err := h.Queries.GetRunnerPairingByDeviceCode(r.Context(), pgtype.Text{String: auth.HashToken(req.DeviceCode), Valid: true})
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "expired"})
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read Runner authorization")
		return
	}
	switch pairing.State {
	case "device_pending":
		writeJSON(w, http.StatusOK, map[string]string{"status": "pending"})
	case "denied":
		writeJSON(w, http.StatusOK, map[string]string{"status": "denied"})
	case "approved":
		consumed, consumeErr := h.Queries.ConsumeRunnerPairing(r.Context(), pairing.ID)
		if consumeErr != nil {
			writeError(w, http.StatusConflict, "Runner authorization was already consumed")
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{
			"status":     "approved",
			"machine_id": uuidToString(consumed.MachineID),
		})
	case "consumed":
		writeJSON(w, http.StatusOK, map[string]string{
			"status":     "approved",
			"machine_id": uuidToString(pairing.MachineID),
		})
	default:
		writeJSON(w, http.StatusOK, map[string]string{"status": pairing.State})
	}
}

func (h *Handler) GetRunnerDeviceAuthorization(w http.ResponseWriter, r *http.Request) {
	userCode := strings.ToUpper(strings.TrimSpace(chi.URLParam(r, "userCode")))
	pairing, err := h.Queries.GetRunnerPairingByUserCode(r.Context(), pgtype.Text{String: userCode, Valid: userCode != ""})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "Runner authorization not found or expired")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read Runner authorization")
		return
	}
	if requestUserID(r) != uuidToString(pairing.OwnerID) {
		writeError(w, http.StatusForbidden, "this Runner authorization belongs to another user")
		return
	}
	agent, err := h.Queries.GetAgent(r.Context(), pairing.AgentID)
	if err != nil {
		writeError(w, http.StatusNotFound, "Agent not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"user_code":    pairing.UserCode.String,
		"state":        pairing.State,
		"agent_id":     uuidToString(pairing.AgentID),
		"agent_name":   agent.Name,
		"machine_name": pairing.MachineName.String,
		"os":           pairing.Os.String,
		"arch":         pairing.Arch.String,
		"roots":        decodeRunnerRoots(pairing.Roots),
		"expires_at":   pairing.ExpiresAt.Time.UTC().Format(time.RFC3339),
	})
}

func (h *Handler) ApproveRunnerDeviceAuthorization(w http.ResponseWriter, r *http.Request) {
	h.finishRunnerDeviceAuthorization(w, r, true)
}

func (h *Handler) DenyRunnerDeviceAuthorization(w http.ResponseWriter, r *http.Request) {
	h.finishRunnerDeviceAuthorization(w, r, false)
}

func (h *Handler) finishRunnerDeviceAuthorization(w http.ResponseWriter, r *http.Request, approve bool) {
	userCode := strings.ToUpper(strings.TrimSpace(chi.URLParam(r, "userCode")))
	pairing, err := h.Queries.GetRunnerPairingByUserCode(r.Context(), pgtype.Text{String: userCode, Valid: userCode != ""})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "Runner authorization not found or expired")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read Runner authorization")
		return
	}
	actorID := requestUserID(r)
	if actorID != uuidToString(pairing.OwnerID) {
		writeError(w, http.StatusForbidden, "only the Agent owner can approve this Runner")
		return
	}
	if !approve {
		if _, err := h.Queries.MarkRunnerPairingDenied(r.Context(), pairing.ID); err != nil {
			writeError(w, http.StatusConflict, "Runner authorization is no longer pending")
			return
		}
		slog.Info("Runner authorization denied",
			"event", "runner_authorization_denied",
			"pairing_id", uuidToString(pairing.ID),
			"agent_id", uuidToString(pairing.AgentID),
			"owner_id", actorID,
		)
		writeJSON(w, http.StatusOK, map[string]string{"status": "denied"})
		return
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to approve Runner")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	qtx := h.Queries.WithTx(tx)
	currentAgent, err := qtx.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{
		ID: pairing.AgentID, WorkspaceID: pairing.WorkspaceID,
	})
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && currentAgent.OwnerID != pairing.OwnerID) {
		writeError(w, http.StatusConflict, "Agent ownership changed; create a new Runner pairing")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to verify Runner authorization")
		return
	}
	machine, err := qtx.UpsertRunnerMachine(r.Context(), db.UpsertRunnerMachineParams{
		OwnerID:       pairing.OwnerID,
		Name:          pairing.MachineName.String,
		Os:            pairing.Os.String,
		Arch:          pairing.Arch.String,
		PublicKey:     pairing.PublicKey,
		ClientVersion: pairing.ClientVersion,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to approve Runner")
		return
	}
	binding, err := qtx.CreateAgentRunnerBinding(r.Context(), db.CreateAgentRunnerBindingParams{
		WorkspaceID: pairing.WorkspaceID,
		AgentID:     pairing.AgentID,
		MachineID:   machine.ID,
		BoundBy:     pairing.OwnerID,
		Roots:       pairing.Roots,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to approve Runner")
		return
	}
	if _, err := qtx.MarkRunnerPairingApproved(r.Context(), db.MarkRunnerPairingApprovedParams{ID: pairing.ID, MachineID: machine.ID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusConflict, "Runner authorization is no longer pending")
		} else {
			writeError(w, http.StatusInternalServerError, "failed to approve Runner")
		}
		return
	}
	details, _ := json.Marshal(map[string]any{
		"agent_id":   uuidToString(pairing.AgentID),
		"binding_id": uuidToString(binding.ID),
		"machine_id": uuidToString(machine.ID),
		"machine_os": machine.Os,
		"root_count": len(decodeRunnerRoots(binding.Roots)),
	})
	if _, err := qtx.CreateActivity(r.Context(), db.CreateActivityParams{
		WorkspaceID: pairing.WorkspaceID,
		IssueID:     pgtype.UUID{},
		ActorType:   pgtype.Text{String: "member", Valid: true},
		ActorID:     pairing.OwnerID,
		Action:      runnerBindingAuthorizedActivity,
		Details:     details,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to audit Runner authorization")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to approve Runner")
		return
	}
	slog.Info("Runner binding authorized",
		"event", "runner_binding_authorized",
		"workspace_id", uuidToString(pairing.WorkspaceID),
		"agent_id", uuidToString(pairing.AgentID),
		"binding_id", uuidToString(binding.ID),
		"machine_id", uuidToString(machine.ID),
		"owner_id", actorID,
		"root_count", len(decodeRunnerRoots(binding.Roots)),
	)
	writeJSON(w, http.StatusOK, map[string]string{"status": "approved", "machine_id": uuidToString(machine.ID)})
}

func (h *Handler) CreateRunnerChallenge(w http.ResponseWriter, r *http.Request) {
	machineID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "machineId"), "Runner machine id")
	if !ok {
		return
	}
	if _, err := h.Queries.GetRunnerMachine(r.Context(), machineID); err != nil {
		writeError(w, http.StatusNotFound, "Runner machine not found")
		return
	}
	challenge, err := generateRunnerSecret("rch_", 32)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create Runner challenge")
		return
	}
	row, err := h.Queries.CreateRunnerAuthChallenge(r.Context(), db.CreateRunnerAuthChallengeParams{
		MachineID:     machineID,
		ChallengeHash: auth.HashToken(challenge),
		ExpiresAt:     pgtype.Timestamptz{Time: time.Now().Add(runnerChallengeTTL), Valid: true},
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create Runner challenge")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"challenge_id": uuidToString(row.ID),
		"challenge":    challenge,
		"expires_in":   int(runnerChallengeTTL.Seconds()),
	})
}

func (h *Handler) RunnerWebSocket(w http.ResponseWriter, r *http.Request) {
	if h.RunnerHub == nil {
		writeError(w, http.StatusServiceUnavailable, "Runner WebSocket unavailable")
		return
	}
	machineID, err := util.ParseUUID(strings.TrimSpace(r.URL.Query().Get("machine_id")))
	if err != nil {
		writeError(w, http.StatusBadRequest, "machine_id is required")
		return
	}
	challengeID, err := util.ParseUUID(strings.TrimSpace(r.Header.Get("X-Runner-Challenge-ID")))
	if err != nil {
		writeError(w, http.StatusUnauthorized, "Runner challenge is required")
		return
	}
	challenge := strings.TrimSpace(r.Header.Get("X-Runner-Challenge"))
	signature, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(r.Header.Get("X-Runner-Signature")))
	if err != nil || challenge == "" || len(signature) != ed25519.SignatureSize {
		writeError(w, http.StatusUnauthorized, "invalid Runner signature")
		return
	}
	row, err := h.Queries.GetRunnerAuthChallenge(r.Context(), db.GetRunnerAuthChallengeParams{ID: challengeID, MachineID: machineID})
	if err != nil || row.ConsumedAt.Valid || row.MachineRevokedAt.Valid || !row.ExpiresAt.Time.After(time.Now()) {
		writeError(w, http.StatusUnauthorized, "Runner challenge is invalid or expired")
		return
	}
	expectedHash := auth.HashToken(challenge)
	if subtle.ConstantTimeCompare([]byte(expectedHash), []byte(row.ChallengeHash)) != 1 ||
		!ed25519.Verify(ed25519.PublicKey(row.PublicKey), []byte(challenge), signature) {
		writeError(w, http.StatusUnauthorized, "invalid Runner signature")
		return
	}
	if _, err := h.Queries.ConsumeRunnerAuthChallenge(r.Context(), db.ConsumeRunnerAuthChallengeParams{
		ID: challengeID, MachineID: machineID, ChallengeHash: expectedHash,
	}); err != nil {
		writeError(w, http.StatusUnauthorized, "Runner challenge was already used")
		return
	}
	h.RunnerHub.HandleWebSocket(w, r, runnerws.Identity{MachineID: uuidToString(machineID)})
}

func (h *Handler) handleRunnerConnected(ctx context.Context, identity runnerws.Identity) {
	machineID, err := util.ParseUUID(identity.MachineID)
	if err != nil {
		return
	}
	machine, err := h.Queries.GetRunnerMachine(ctx, machineID)
	if err != nil {
		return
	}
	if h.RunnerHub != nil && h.RunnerHub.Connected(identity.MachineID) {
		if subscriber, ok := h.RunnerRelay.(realtime.RunnerMachineScopeSubscriber); ok {
			subscriber.SubscribeRunnerMachine(identity.MachineID)
		}
	}
	_, _ = h.Queries.UpdateRunnerMachineHeartbeat(ctx, db.UpdateRunnerMachineHeartbeatParams{
		ID: machineID, ClientVersion: machine.ClientVersion,
	})
	slog.Info("Runner connected",
		"event", "runner_connected",
		"machine_id", identity.MachineID,
		"client_version", machine.ClientVersion,
	)
	calls, err := h.Queries.ListQueuedRunnerCalls(ctx, machineID)
	if err != nil {
		return
	}
	for _, call := range calls {
		h.dispatchRunnerCall(ctx, identity.MachineID, uuidToString(call.ID))
	}
}

func (h *Handler) handleRunnerDisconnected(identity runnerws.Identity) {
	if h.RunnerHub != nil && h.RunnerHub.Connected(identity.MachineID) {
		return
	}
	if subscriber, ok := h.RunnerRelay.(realtime.RunnerMachineScopeSubscriber); ok {
		subscriber.UnsubscribeRunnerMachine(identity.MachineID)
	}
	slog.Info("Runner disconnected", "event", "runner_disconnected", "machine_id", identity.MachineID)
}

func (h *Handler) handleRunnerHeartbeat(ctx context.Context, identity runnerws.Identity, heartbeat runnerprotocol.Heartbeat) {
	machineID, err := util.ParseUUID(identity.MachineID)
	if err != nil {
		return
	}
	clientVersion := strings.TrimSpace(heartbeat.ClientVersion)
	if len(clientVersion) > 64 {
		return
	}
	_, _ = h.Queries.UpdateRunnerMachineHeartbeat(ctx, db.UpdateRunnerMachineHeartbeatParams{ID: machineID, ClientVersion: clientVersion})
}

func (h *Handler) handleRunnerResult(ctx context.Context, identity runnerws.Identity, result runnerprotocol.Result) {
	callID, err := util.ParseUUID(strings.TrimSpace(result.CallID))
	if err != nil || len(result.Result) > runnerMaxResult {
		runnerws.LogDroppedResult(identity, result, errors.New("invalid result"))
		return
	}
	machineID, err := util.ParseUUID(identity.MachineID)
	if err != nil {
		return
	}
	if len(result.Result) == 0 {
		result.Result = json.RawMessage(`{}`)
	}
	_, err = h.Queries.CompleteRunnerCall(ctx, db.CompleteRunnerCallParams{
		Succeeded:    result.Succeeded,
		Result:       result.Result,
		ErrorCode:    pgtype.Text{String: strings.TrimSpace(result.ErrorCode), Valid: result.ErrorCode != ""},
		ErrorMessage: pgtype.Text{String: strings.TrimSpace(result.ErrorMessage), Valid: result.ErrorMessage != ""},
		ID:           callID,
		MachineID:    machineID,
	})
	if err != nil {
		runnerws.LogDroppedResult(identity, result, err)
		return
	}
	slog.Info("Runner call completed",
		"event", "runner_call_completed",
		"machine_id", identity.MachineID,
		"call_id", result.CallID,
		"succeeded", result.Succeeded,
		"error_code", strings.TrimSpace(result.ErrorCode),
	)
	h.dispatchQueuedRunnerCalls(ctx, machineID)
}

func (h *Handler) dispatchQueuedRunnerCalls(ctx context.Context, machineID pgtype.UUID) {
	calls, err := h.Queries.ListQueuedRunnerCalls(ctx, machineID)
	if err != nil {
		return
	}
	for _, call := range calls {
		h.dispatchRunnerCall(ctx, uuidToString(machineID), uuidToString(call.ID))
	}
}

type runnerDispatchFrame struct {
	Type   string `json:"type"`
	CallID string `json:"call_id"`
}

func (h *Handler) DeliverRunnerMachine(scopeID string, frame []byte, _ string) {
	var dispatch runnerDispatchFrame
	if json.Unmarshal(frame, &dispatch) != nil || dispatch.Type != "runner:dispatch" {
		return
	}
	h.dispatchRunnerCall(context.Background(), scopeID, dispatch.CallID)
}

func (h *Handler) dispatchRunnerCall(ctx context.Context, machineIDString, callIDString string) {
	if h.RunnerHub == nil || !h.RunnerHub.Connected(machineIDString) {
		return
	}
	machineID, err := util.ParseUUID(machineIDString)
	if err != nil {
		return
	}
	callID, err := util.ParseUUID(callIDString)
	if err != nil {
		return
	}
	call, err := h.Queries.ClaimRunnerCall(ctx, db.ClaimRunnerCallParams{ID: callID, MachineID: machineID})
	if err != nil {
		return
	}
	frame, err := json.Marshal(runnerprotocol.Call{
		Type:      runnerprotocol.MessageCall,
		CallID:    callIDString,
		ToolName:  call.ToolName,
		Arguments: json.RawMessage(call.Arguments),
		Roots:     decodeRunnerRoots(call.Roots),
		ExpiresAt: call.ExpiresAt.Time.UTC().Format(time.RFC3339Nano),
	})
	if err != nil || !h.RunnerHub.Send(machineIDString, frame) {
		_ = h.Queries.RequeueRunnerCall(ctx, db.RequeueRunnerCallParams{ID: callID, MachineID: machineID})
		return
	}
	slog.Info("Runner call dispatched",
		"event", "runner_call_dispatched",
		"machine_id", machineIDString,
		"call_id", callIDString,
		"task_id", uuidToString(call.TaskID),
		"agent_id", uuidToString(call.AgentID),
		"tool_name", call.ToolName,
	)
}

func (h *Handler) notifyRunnerCall(machineID, callID string) {
	frame, _ := json.Marshal(runnerDispatchFrame{Type: "runner:dispatch", CallID: callID})
	h.DeliverRunnerMachine(machineID, frame, "")
	if h.RunnerRelay != nil {
		h.RunnerRelay.BroadcastToScope(realtime.ScopeRunnerMachine, machineID, frame)
	}
}
