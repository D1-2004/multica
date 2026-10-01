package handler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/dwsclient"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dws"
)

// A DingTalk digital employee's (DEAP's) Agent Identity credential subscribes
// as another principal and receives none of the employee's messages; DEAP
// issues the employee's own DWS auth code, but only to its supervisor. A
// native subscription with a DEAP link mints its event credential that way:
// the supervisor's Agent Identity credential asks DEAP for the employee's
// code. The code serves the native event stream only (its sessions carry
// their own credential scope); replies, history and tasks keep the agent's
// own credentials. Links are set by deployment operators only.

// nativeDEAPLinkStore reads and writes DEAP links; h.Queries in production.
type nativeDEAPLinkStore interface {
	GetDWSNativeDEAPLink(context.Context, db.GetDWSNativeDEAPLinkParams) (db.AgentDwsNativeDeapLink, error)
	GetWorkspaceDWSNativeDEAPLink(context.Context, db.GetWorkspaceDWSNativeDEAPLinkParams) (db.AgentDwsNativeDeapLink, error)
	UpsertDWSNativeDEAPLink(context.Context, db.UpsertDWSNativeDEAPLinkParams) (db.AgentDwsNativeDeapLink, error)
	DeleteDWSNativeDEAPLink(context.Context, db.DeleteDWSNativeDEAPLinkParams) error
	ListDWSNativeDEAPLinks(context.Context) ([]db.AgentDwsNativeDeapLink, error)
}

func (h *Handler) nativeDEAPLinks() nativeDEAPLinkStore {
	if h.dwsNativeDEAPLinks != nil {
		return h.dwsNativeDEAPLinks
	}
	if h.Queries != nil {
		return h.Queries
	}
	return nil
}

// deapSupervisor is a supervisor's DWS session as the native mint uses it.
// DEAP answers {success, data}: CallRaw keeps the whole payload (Call would
// keep only a result field).
type deapSupervisor interface {
	CallRaw(ctx context.Context, server dws.Server, tool string, args any) (json.RawMessage, error)
	Token() dws.Token
}

// deapSupervisorOpener opens a supervisor's session, minting with mint when
// no token exists; tests replace it.
type deapSupervisorOpener func(context.Context, dwsclient.Identity, func(context.Context) (dwsclient.Credential, error)) (deapSupervisor, error)

// NativeCredentialScope keeps native event credentials apart from the
// agent's usual ones (dwsclient.CLI.CredentialScope).
const NativeCredentialScope = "native-subscription"

type nativeMintFunc func(context.Context, dwsclient.Identity) (dwsclient.Credential, error)

// NativeSubscriptionMint returns the native event source's mint: an
// identity with a DEAP link gets its code from DEAP through its
// supervisor; any other identity is minted by base, as before.
func (h *Handler) NativeSubscriptionMint(base func(context.Context, dwsclient.Identity) (dwsclient.Credential, error), sessions dwsclient.Shared) func(context.Context, dwsclient.Identity) (dwsclient.Credential, error) {
	open := h.nativeDEAPSupervisor
	if open == nil {
		open = func(ctx context.Context, id dwsclient.Identity, mint func(context.Context) (dwsclient.Credential, error)) (deapSupervisor, error) {
			return sessions.Client(ctx, id, mint)
		}
	}
	return func(ctx context.Context, id dwsclient.Identity) (dwsclient.Credential, error) {
		store := h.nativeDEAPLinks()
		agentID, err := util.ParseUUID(id.AgentID)
		if store == nil || err != nil {
			return base(ctx, id)
		}
		link, err := store.GetDWSNativeDEAPLink(ctx, db.GetDWSNativeDEAPLinkParams{AgentID: agentID, DwsUid: id.UID, OrgID: id.OrgID})
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return dwsclient.Credential{}, fmt.Errorf("load the native subscription's DEAP link: %w", err)
		}
		// Mint only what the stream's credential version names: the token
		// is stored under that version, so a link that changed since the
		// sweep waits for the next one (which restarts the stream).
		version := ""
		if err == nil {
			version = deapCredentialVersion(link)
		}
		if version != id.CredentialVersion {
			return dwsclient.Credential{}, errDEAPLinkChanged
		}
		if version == "" {
			return base(ctx, id)
		}
		return mintThroughDEAP(ctx, base, open, id, link)
	}
}

// NativeSubscriptionIdentities lists the identities the native event source
// streams. An identity with a DEAP link carries a credential version named
// by the link, so changing the link mints and connects afresh (on whichever
// replica holds the stream).
func (h *Handler) NativeSubscriptionIdentities(ctx context.Context) ([]dwsclient.Identity, error) {
	subscriptions := h.nativeSubscriptions()
	if subscriptions == nil {
		return nil, nil
	}
	rows, err := subscriptions.ListActiveDWSNativeSubscriptions(ctx)
	if err != nil {
		return nil, err
	}
	versions := map[string]string{}
	if links := h.nativeDEAPLinks(); links != nil && len(rows) > 0 {
		all, err := links.ListDWSNativeDEAPLinks(ctx)
		if err != nil {
			// Unversioned, a linked identity's mint refuses until the links
			// read again; the other streams are unaffected.
			slog.Warn("native DEAP links unavailable", "event", "dws_native_deap_links_failed", "error", err)
		}
		for _, link := range all {
			versions[util.UUIDToString(link.AgentID)+"\x00"+link.DwsUid+"\x00"+link.OrgID] = deapCredentialVersion(link)
		}
	}
	ids := make([]dwsclient.Identity, 0, len(rows))
	for _, row := range rows {
		agentID := util.UUIDToString(row.AgentID)
		ids = append(ids, dwsclient.Identity{AgentID: agentID, UID: row.DwsUid, OrgID: row.OrgID,
			CredentialVersion: versions[agentID+"\x00"+row.DwsUid+"\x00"+row.OrgID]})
	}
	return ids, nil
}

// deapCredentialVersion names a link without its ids (it ends up in Redis
// keys and stream fingerprints).
func deapCredentialVersion(link db.AgentDwsNativeDeapLink) string {
	sum := sha256.Sum256([]byte(link.DeapAgentUuid + "\x00" + link.SupervisorUid))
	return "deap-" + hex.EncodeToString(sum[:8])
}

var errDEAPLinkChanged = errors.New("the native subscription's DEAP link changed; the stream restarts with it")

// deapCall runs a DEAP tool as the supervisor and returns its business data;
// a business failure is an error.
func deapCall(ctx context.Context, client deapSupervisor, tool string, args map[string]any) (map[string]any, error) {
	raw, err := client.CallRaw(ctx, dws.ServerDEAP, tool, args)
	if err != nil {
		return nil, err
	}
	var envelope struct {
		Success   *bool  `json:"success"`
		ErrorCode string `json:"errorCode"`
		ErrorMsg  string `json:"errorMsg"`
	}
	_ = json.Unmarshal(raw, &envelope)
	if envelope.Success != nil && !*envelope.Success {
		return nil, fmt.Errorf("DEAP %s refused: %s %s", tool, truncateRunes(envelope.ErrorCode, 64), truncateRunes(envelope.ErrorMsg, 200))
	}
	data := deapBusinessData(raw)
	if data == nil {
		return nil, fmt.Errorf("DEAP %s returned no data", tool)
	}
	return data, nil
}

func mintThroughDEAP(ctx context.Context, base nativeMintFunc, open deapSupervisorOpener,
	id dwsclient.Identity, link db.AgentDwsNativeDeapLink) (dwsclient.Credential, error) {
	supervisor := dwsclient.Identity{AgentID: id.AgentID, UID: link.SupervisorUid, OrgID: id.OrgID}
	client, err := open(ctx, supervisor, func(ctx context.Context) (dwsclient.Credential, error) {
		return base(ctx, supervisor)
	})
	if err != nil {
		return dwsclient.Credential{}, fmt.Errorf("open the DEAP supervisor's session: %w", err)
	}
	// The employee the link names must be this account: a link to another
	// employee would subscribe someone else's messages.
	detail, err := deapCall(ctx, client, "get_digital_employee_detail",
		map[string]any{"agentUuid": link.DeapAgentUuid, "snapshot": "published"})
	if err != nil {
		return dwsclient.Credential{}, fmt.Errorf("read the DEAP digital employee: %w", err)
	}
	profile, _ := detail["profile"].(map[string]any)
	corpID := deapScalar(profile["corpId"])
	if got := deapScalar(profile["userId"]); got != id.UID || corpID == "" {
		return dwsclient.Credential{}, fmt.Errorf("the DEAP digital employee is not this identity's account")
	}
	args := map[string]any{"agentUuid": link.DeapAgentUuid}
	// Ask for a code of this deployment's DWS app: the code is exchanged
	// with its secret.
	if clientID := strings.TrimSpace(client.Token().ClientID); clientID != "" {
		args["clientId"] = clientID
	}
	data, err := deapCall(ctx, client, "get_dws_auth_code", args)
	if err != nil {
		return dwsclient.Credential{}, fmt.Errorf("request the digital employee's DWS auth code: %w", err)
	}
	clientID, code := deapScalar(data["dwsClientId"]), deapScalar(data["dwsAuthCode"])
	if clientID == "" || code == "" {
		return dwsclient.Credential{}, errors.New("DEAP returned no DWS auth code")
	}
	slog.Info("native subscription credential issued by DEAP", "event", "dws_native_deap_credential",
		"agent_id", id.AgentID, "deap_agent_uuid", link.DeapAgentUuid, "client_id", clientID)
	// The exchange checks that the code is the employee's own (as the dws
	// CLI's managed exchange does).
	return dwsclient.Credential{UID: id.UID, ClientID: clientID, AuthCode: code,
		ExpectUserID: id.UID, ExpectCorpID: corpID}, nil
}

// deapBusinessData is a DEAP tool payload's business object: its data or
// result object, or the payload itself (as the dws CLI reads it).
func deapBusinessData(raw json.RawMessage) map[string]any {
	var value map[string]any
	if json.Unmarshal(raw, &value) != nil {
		return nil
	}
	for _, key := range []string{"data", "result"} {
		if nested, ok := value[key].(map[string]any); ok {
			return nested
		}
	}
	return value
}

func deapScalar(v any) string {
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x)
	case json.Number:
		return x.String()
	case float64:
		return strings.TrimSuffix(strings.TrimRight(fmt.Sprintf("%f", x), "0"), ".")
	}
	return ""
}

var deapAgentUUID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

type nativeDEAPLinkView struct {
	DeapAgentUUID string `json:"deap_agent_uuid"`
	SupervisorUID string `json:"supervisor_uid"`
	UpdatedAt     string `json:"updated_at,omitempty"`
}

func deapLinkView(link db.AgentDwsNativeDeapLink) *nativeDEAPLinkView {
	view := &nativeDEAPLinkView{DeapAgentUUID: link.DeapAgentUuid, SupervisorUID: link.SupervisorUid}
	if link.UpdatedAt.Valid {
		view.UpdatedAt = link.UpdatedAt.Time.UTC().Format("2006-01-02T15:04:05Z")
	}
	return view
}

// nativeDEAPLinkFor is the agent's link while it names identity's account.
func (h *Handler) nativeDEAPLinkFor(ctx context.Context, workspaceID, agentID pgtype.UUID, identity db.AgentDingtalkIdentity) (*nativeDEAPLinkView, error) {
	store := h.nativeDEAPLinks()
	if store == nil {
		return nil, nil
	}
	link, err := store.GetWorkspaceDWSNativeDEAPLink(ctx, db.GetWorkspaceDWSNativeDEAPLinkParams{WorkspaceID: workspaceID, AgentID: agentID})
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && (link.DwsUid != identity.DwsUid || link.OrgID != identity.OrgID)) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return deapLinkView(link), nil
}

type setNativeDEAPLinkRequest struct {
	DeapAgentUUID string `json:"deap_agent_uuid"`
	SupervisorUID string `json:"supervisor_uid"`
}

// SetDWSNativeDEAPLink sets (PUT) or removes (DELETE) the DEAP link of an
// agent's execution identity. Deployment operators only: the supervisor's
// Agent Identity credential acts toward DEAP.
func (h *Handler) SetDWSNativeDEAPLink(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "workspace id")
	if !ok {
		return
	}
	agentID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "agentId"), "agent id")
	if !ok {
		return
	}
	actorID, ok := parseUUIDOrBadRequest(w, userID, "user id")
	if !ok {
		return
	}
	var request setNativeDEAPLinkRequest
	if r.Method != http.MethodDelete {
		if err := decodeLimitedJSON(w, r, 1<<10, &request, "invalid_request"); err != nil {
			return
		}
		request.DeapAgentUUID = strings.ToLower(strings.TrimSpace(request.DeapAgentUUID))
		request.SupervisorUID = strings.TrimSpace(request.SupervisorUID)
		if !deapAgentUUID.MatchString(request.DeapAgentUUID) || !dingTalkDecimalID.MatchString(request.SupervisorUID) {
			writeDingTalkAccountBindingAPIError(w, http.StatusBadRequest, "invalid_deap_link",
				"deap_agent_uuid must be a DEAP digital employee id and supervisor_uid a decimal id")
			return
		}
	}
	if _, ok := h.authorizeDingTalkAccountBindingOperation(w, r, actorID, workspaceID, agentID); !ok {
		return
	}
	if !h.isAgentA2AOperator(r, actorID) {
		writeDingTalkAccountBindingAPIError(w, http.StatusForbidden, "operator_only", "the DEAP supervisor is limited to deployment operators")
		return
	}
	links, subscriptions := h.nativeDEAPLinks(), h.nativeSubscriptions()
	if links == nil || subscriptions == nil {
		writeDingTalkAccountBindingAPIError(w, http.StatusServiceUnavailable, "native_subscription_unavailable", "native subscription is not configured")
		return
	}
	ctx := r.Context()
	if r.Method == http.MethodDelete {
		if err := links.DeleteDWSNativeDEAPLink(ctx, db.DeleteDWSNativeDEAPLinkParams{WorkspaceID: workspaceID, AgentID: agentID}); err != nil {
			writeDingTalkAccountBindingAPIError(w, http.StatusInternalServerError, "native_subscription_failed", "failed to remove the DEAP link")
			return
		}
		h.nativeDEAPLinkChanged(workspaceID, agentID, userID)
		writeJSON(w, http.StatusOK, map[string]any{"deap_link": nil})
		return
	}
	identity, err := subscriptions.GetAgentDingTalkIdentity(ctx, db.GetAgentDingTalkIdentityParams{WorkspaceID: workspaceID, AgentID: agentID})
	if errors.Is(err, pgx.ErrNoRows) {
		writeDingTalkAccountBindingAPIError(w, http.StatusConflict, "native_subscription_requires_identity",
			"bind an execution identity before linking its DEAP digital employee")
		return
	}
	if err != nil {
		writeDingTalkAccountBindingAPIError(w, http.StatusInternalServerError, "native_subscription_failed", "failed to load the execution identity")
		return
	}
	if request.SupervisorUID == identity.DwsUid {
		writeDingTalkAccountBindingAPIError(w, http.StatusBadRequest, "invalid_deap_link", "the supervisor must be another account than the digital employee")
		return
	}
	link, err := links.UpsertDWSNativeDEAPLink(ctx, db.UpsertDWSNativeDEAPLinkParams{
		AgentID: agentID, WorkspaceID: workspaceID, DwsUid: identity.DwsUid, OrgID: identity.OrgID,
		DeapAgentUuid: request.DeapAgentUUID, SupervisorUid: request.SupervisorUID, UpdatedBy: actorID,
	})
	if err != nil {
		writeDingTalkAccountBindingAPIError(w, http.StatusInternalServerError, "native_subscription_failed", "failed to save the DEAP link")
		return
	}
	h.nativeDEAPLinkChanged(workspaceID, agentID, userID)
	writeJSON(w, http.StatusOK, map[string]any{"deap_link": deapLinkView(link)})
}

func (h *Handler) nativeDEAPLinkChanged(workspaceID, agentID pgtype.UUID, actor string) {
	slog.Info("dws native DEAP link updated", "event", "dws_native_deap_link_updated",
		"workspace_id", util.UUIDToString(workspaceID), "agent_id", util.UUIDToString(agentID), "actor_id", actor)
}
