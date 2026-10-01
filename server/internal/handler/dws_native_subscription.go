package handler

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/integrations/agentmessagerouter"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// An execution identity with native subscription receives its own DingTalk
// messages through DWS personal event subscriptions (always the production
// DWS gateway) instead of the Agent Message Router. It is mutually exclusive
// with the agent's digital-employee message binding.

// dwsNativeSubscriptionStore is what native subscription reads and writes;
// h.Queries in production.
type dwsNativeSubscriptionStore interface {
	GetAgentDWSNativeSubscription(context.Context, db.GetAgentDWSNativeSubscriptionParams) (db.AgentDwsNativeSubscription, error)
	EnableAgentDWSNativeSubscription(context.Context, db.EnableAgentDWSNativeSubscriptionParams) (db.AgentDwsNativeSubscription, error)
	DisableAgentDWSNativeSubscription(context.Context, db.DisableAgentDWSNativeSubscriptionParams) error
	ListWorkspaceDWSNativeSubscriptions(context.Context, pgtype.UUID) ([]db.AgentDwsNativeSubscription, error)
	GetAgentDingTalkIdentity(context.Context, db.GetAgentDingTalkIdentityParams) (db.AgentDingtalkIdentity, error)
	GetDingTalkAccountBindingByAgent(context.Context, db.GetDingTalkAccountBindingByAgentParams) (db.ChannelInstallation, error)
	// Enabling also checks managed responses and account reuse
	// (nativeSubscriptionPreconditions).
	nativeEligibilityStore
	ListActiveDWSNativeSubscriptions(context.Context) ([]db.ListActiveDWSNativeSubscriptionsRow, error)
	// Account-level guards: who owns an account, and whether a Router
	// message binding routes it.
	nativeOwnershipStore
	HasActiveDingTalkMessageRouteForAccount(context.Context, db.HasActiveDingTalkMessageRouteForAccountParams) (bool, error)
	DeleteStaleDWSNativeSubscriptionsForAccount(context.Context, db.DeleteStaleDWSNativeSubscriptionsForAccountParams) error
	ListAgentDingTalkIdentities(context.Context, pgtype.UUID) ([]db.AgentDingtalkIdentity, error)
}

// nativeSubscriptions returns the store, or nil when none is wired (then no
// agent has native subscription).
func (h *Handler) nativeSubscriptions() dwsNativeSubscriptionStore {
	if h.dwsNativeSubscriptions != nil {
		return h.dwsNativeSubscriptions
	}
	if h.Queries != nil {
		return h.Queries
	}
	return nil
}

type setDWSNativeSubscriptionRequest struct {
	Enabled *bool `json:"enabled"`
}

type bindDingTalkMessageRouteManuallyRequest struct {
	OrgID        string `json:"org_id"`
	UID          string `json:"uid"`
	MessageScope string `json:"message_scope"`
}

var dingTalkDecimalID = regexp.MustCompile(`^[1-9][0-9]{0,19}$`)

// SetDWSNativeSubscription switches native subscription for an agent's bound
// execution identity.
func (h *Handler) SetDWSNativeSubscription(w http.ResponseWriter, r *http.Request) {
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
	var request setDWSNativeSubscriptionRequest
	if err := decodeLimitedJSON(w, r, 1<<10, &request, "invalid_request"); err != nil {
		return
	}
	if request.Enabled == nil {
		writeDingTalkAccountBindingAPIError(w, http.StatusBadRequest, "invalid_request", "enabled is required")
		return
	}
	agent, ok := h.authorizeDingTalkAccountBindingOperation(w, r, actorID, workspaceID, agentID)
	if !ok {
		return
	}
	store := h.nativeSubscriptions()
	if store == nil {
		writeDingTalkAccountBindingError(w, agentmessagerouter.ErrNotConfigured)
		return
	}
	ctx := r.Context()
	if !*request.Enabled {
		if err := store.DisableAgentDWSNativeSubscription(ctx, db.DisableAgentDWSNativeSubscriptionParams{
			WorkspaceID: workspaceID, AgentID: agentID,
		}); err != nil {
			writeDingTalkAccountBindingAPIError(w, http.StatusInternalServerError, "native_subscription_failed", "failed to update native subscription")
			return
		}
		h.nativeSubscriptionChanged(workspaceID, agentID, userID, false)
		writeJSON(w, http.StatusOK, map[string]any{"native_subscription": false})
		return
	}
	identity, err := store.GetAgentDingTalkIdentity(ctx, db.GetAgentDingTalkIdentityParams{
		WorkspaceID: workspaceID, AgentID: agentID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeDingTalkAccountBindingAPIError(w, http.StatusConflict, "native_subscription_requires_identity",
				"bind an execution identity before enabling native subscription")
			return
		}
		writeDingTalkAccountBindingAPIError(w, http.StatusInternalServerError, "native_subscription_failed", "failed to load the execution identity")
		return
	}
	active, err := h.messageRouteActive(ctx, workspaceID, agentID)
	if err == nil && !active {
		// Any agent's message binding that routes this account through the
		// Router; the per-message ownership rule is what keeps deliveries
		// exclusive, this only keeps the switch honest.
		active, err = store.HasActiveDingTalkMessageRouteForAccount(ctx, db.HasActiveDingTalkMessageRouteForAccountParams{
			OrgID: identity.OrgID, DwsUid: identity.DwsUid,
		})
	}
	if err != nil {
		writeDingTalkAccountBindingAPIError(w, http.StatusInternalServerError, "native_subscription_failed", "failed to load the message binding")
		return
	}
	if active {
		writeDingTalkAccountBindingAPIError(w, http.StatusConflict, "native_subscription_conflicts_with_message_binding",
			"unbind the digital employee message binding before enabling native subscription")
		return
	}
	if !h.nativeSubscriptionPreconditions(w, r, store, agent, identity, actorID) {
		return
	}
	// A row of an archived agent or of an agent rebound elsewhere no longer
	// owns the account; it must not hold the account's unique slot.
	if err := store.DeleteStaleDWSNativeSubscriptionsForAccount(ctx, db.DeleteStaleDWSNativeSubscriptionsForAccountParams{
		OrgID: identity.OrgID, DwsUid: identity.DwsUid, ExceptAgentID: agentID,
	}); err != nil {
		writeDingTalkAccountBindingAPIError(w, http.StatusInternalServerError, "native_subscription_failed", "failed to update native subscription")
		return
	}
	if _, err := store.EnableAgentDWSNativeSubscription(ctx, db.EnableAgentDWSNativeSubscriptionParams{
		AgentID: agentID, WorkspaceID: workspaceID, EnabledBy: actorID,
		DwsUid: identity.DwsUid, OrgID: identity.OrgID,
	}); err != nil {
		if isUniqueViolation(err) {
			// The (org_id, dws_uid) index: another agent's row holds the account.
			writeDingTalkAccountBindingAPIError(w, http.StatusConflict, "native_subscription_account_in_use",
				"another agent already receives this DingTalk account's messages through native subscription")
			return
		}
		writeDingTalkAccountBindingAPIError(w, http.StatusInternalServerError, "native_subscription_failed", "failed to update native subscription")
		return
	}
	h.nativeSubscriptionChanged(workspaceID, agentID, userID, true)
	writeJSON(w, http.StatusOK, map[string]any{"native_subscription": true})
}

// digitalEmployeeBinder binds a digital employee account to an agent's
// message route directly, without the DBase scan.
type digitalEmployeeBinder interface {
	BindDigitalEmployee(context.Context, agentmessagerouter.DirectBindingParams) (agentmessagerouter.DirectDigitalEmployeeBinding, error)
}

// BindDingTalkMessageRouteManually lets a deployment operator bind the
// digital-employee message route by organization and account id instead of
// scanning.
func (h *Handler) BindDingTalkMessageRouteManually(w http.ResponseWriter, r *http.Request) {
	binder, configured := h.DingTalkAccountBindings.(digitalEmployeeBinder)
	if h.DingTalkAccountBindings == nil || !configured {
		writeDingTalkAccountBindingError(w, agentmessagerouter.ErrNotConfigured)
		return
	}
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
	var request bindDingTalkMessageRouteManuallyRequest
	if err := decodeLimitedJSON(w, r, 1<<10, &request, "invalid_request"); err != nil {
		return
	}
	request.OrgID, request.UID = strings.TrimSpace(request.OrgID), strings.TrimSpace(request.UID)
	if !dingTalkDecimalID.MatchString(request.OrgID) || !dingTalkDecimalID.MatchString(request.UID) {
		writeDingTalkAccountBindingAPIError(w, http.StatusBadRequest, "invalid_identity", "org_id and uid must be DingTalk decimal ids")
		return
	}
	scope := strings.TrimSpace(request.MessageScope)
	if scope == "" {
		scope = agentmessagerouter.DingTalkMessageScopeAll
	}
	if scope != agentmessagerouter.DingTalkMessageScopeAll && scope != agentmessagerouter.DingTalkMessageScopeDirectOnly {
		writeDingTalkAccountBindingAPIError(w, http.StatusBadRequest, "invalid_message_scope", "message_scope must be all or direct_only")
		return
	}
	agent, ok := h.authorizeDingTalkAccountBindingOperation(w, r, actorID, workspaceID, agentID)
	if !ok {
		return
	}
	if !h.isAgentA2AOperator(r, actorID) {
		writeDingTalkAccountBindingAPIError(w, http.StatusForbidden, "operator_only", "manual binding is limited to deployment operators")
		return
	}
	if blocked, err := h.nativeSubscriptionEnabled(r.Context(), workspaceID, agentID); err != nil {
		writeDingTalkAccountBindingAPIError(w, http.StatusInternalServerError, "binding_internal_error", "failed to load native subscription")
		return
	} else if blocked {
		writeDingTalkAccountBindingAPIError(w, http.StatusConflict, "message_binding_conflicts_with_native_subscription",
			"turn native subscription off before binding the digital employee message route")
		return
	}
	if store := h.nativeSubscriptions(); store != nil {
		if _, owned, err := nativeAccountOwner(r.Context(), store, request.UID, request.OrgID); err != nil {
			writeDingTalkAccountBindingAPIError(w, http.StatusInternalServerError, "binding_internal_error", "failed to load native subscription")
			return
		} else if owned {
			writeDingTalkAccountBindingAPIError(w, http.StatusConflict, "message_binding_conflicts_with_native_subscription",
				"this DingTalk account receives its messages through native subscription; turn it off first")
			return
		}
	}
	metadataStore := h.dingTalkAccountBindingMetadata
	if metadataStore == nil {
		metadataStore = h.Queries
	}
	workspace, err := metadataStore.GetWorkspace(r.Context(), workspaceID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "workspace not found")
		} else {
			writeError(w, http.StatusInternalServerError, "failed to load workspace")
		}
		return
	}
	result, err := binder.BindDigitalEmployee(r.Context(), agentmessagerouter.DirectBindingParams{
		Agent: agentmessagerouter.BeginAgent{
			ID:   agent.ID,
			Name: agent.Name,
			Workspace: agentmessagerouter.BeginWorkspace{
				ID:   workspace.ID,
				Name: workspace.Name,
			},
		},
		InitiatorID:       actorID,
		TenantID:          request.OrgID,
		DigitalEmployeeID: request.UID,
		SurfaceType:       agentmessagerouter.DingTalkSurfaceAuto,
		MessageScope:      scope,
		EnabledDomains:    []string{"channel"},
	})
	if err != nil {
		writeDingTalkAccountBindingError(w, err)
		return
	}
	slog.Info("dingtalk message route bound manually", "event", "dingtalk_message_route_manual_bind",
		"workspace_id", util.UUIDToString(workspaceID), "agent_id", util.UUIDToString(agentID),
		"actor_id", userID, "message_scope", scope, "status", result.Status)
	h.publish(protocol.EventDingTalkAccountBindingActivated, util.UUIDToString(workspaceID), "user", userID,
		map[string]any{"id": util.UUIDToString(agentID)})
	writeJSON(w, http.StatusOK, result)
}

// messageRouteActive reports whether the agent has an active digital-employee
// message binding.
func (h *Handler) messageRouteActive(ctx context.Context, workspaceID, agentID pgtype.UUID) (bool, error) {
	store := h.nativeSubscriptions()
	if store == nil {
		return false, nil
	}
	row, err := store.GetDingTalkAccountBindingByAgent(ctx, db.GetDingTalkAccountBindingByAgentParams{
		WorkspaceID: workspaceID, AgentID: agentID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return row.Status == "active", nil
}

func (h *Handler) nativeSubscriptionEnabled(ctx context.Context, workspaceID, agentID pgtype.UUID) (bool, error) {
	store := h.nativeSubscriptions()
	if store == nil {
		return false, nil
	}
	_, err := store.GetAgentDWSNativeSubscription(ctx, db.GetAgentDWSNativeSubscriptionParams{
		WorkspaceID: workspaceID, AgentID: agentID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// markNativeSubscriptions flags the bindings whose identity has native
// subscription on.
func (h *Handler) markNativeSubscriptions(ctx context.Context, workspaceID pgtype.UUID, bindings []agentmessagerouter.PublicDingTalkAccountBinding) error {
	store := h.nativeSubscriptions()
	if store == nil {
		return nil
	}
	rows, err := store.ListWorkspaceDWSNativeSubscriptions(ctx, workspaceID)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil
	}
	identities, err := store.ListAgentDingTalkIdentities(ctx, workspaceID)
	if err != nil {
		return err
	}
	current := make(map[string]db.AgentDingtalkIdentity, len(identities))
	for _, identity := range identities {
		current[util.UUIDToString(identity.AgentID)] = identity
	}
	// On only while the row still names the agent's current identity: a
	// rebound identity is not subscribed until enabled again.
	enabled := make(map[string]bool, len(rows))
	for _, row := range rows {
		agentID := util.UUIDToString(row.AgentID)
		if identity, ok := current[agentID]; ok && identity.DwsUid == row.DwsUid && identity.OrgID == row.OrgID {
			enabled[agentID] = true
		}
	}
	for i := range bindings {
		if enabled[bindings[i].AgentID] && bindings[i].DWSIdentity.Status == "active" {
			bindings[i].DWSIdentity.NativeSubscription = true
		}
	}
	return nil
}

// clearNativeSubscription removes native subscription with the identity it
// belongs to. A failure is returned: a row left behind would make the
// account native-owned again if the same identity were bound later.
func (h *Handler) clearNativeSubscription(ctx context.Context, workspaceID, agentID pgtype.UUID, actor string) error {
	store := h.nativeSubscriptions()
	if store == nil {
		return nil
	}
	if err := store.DisableAgentDWSNativeSubscription(ctx, db.DisableAgentDWSNativeSubscriptionParams{
		WorkspaceID: workspaceID, AgentID: agentID,
	}); err != nil {
		slog.Warn("native subscription cleanup failed", "event", "dws_native_subscription_cleanup_failed",
			"workspace_id", util.UUIDToString(workspaceID), "agent_id", util.UUIDToString(agentID), "error", err)
		return err
	}
	h.nativeSubscriptionChanged(workspaceID, agentID, actor, false)
	return nil
}

func (h *Handler) nativeSubscriptionChanged(workspaceID, agentID pgtype.UUID, actor string, enabled bool) {
	slog.Info("dws native subscription updated", "event", "dws_native_subscription_updated",
		"workspace_id", util.UUIDToString(workspaceID), "agent_id", util.UUIDToString(agentID),
		"actor_id", actor, "enabled", enabled)
	// Apply at once instead of at the next sweep.
	if h.DWSNativeEvents != nil {
		h.DWSNativeEvents.Kick()
	}
}
