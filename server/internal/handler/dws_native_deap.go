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

	"github.com/multica-ai/multica/server/internal/dwsclient"
	"github.com/multica-ai/multica/server/internal/dwsidentity"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// A DingTalk digital employee's (DEAP's) credentials are issued through its
// supervisor (internal/dwsidentity, for every server path that acts as the
// identity). Deployment operators set the DEAP link that names the employee
// and its supervisor; everyone else sees it read-only.

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
			versions[util.UUIDToString(link.AgentID)+"\x00"+link.DwsUid+"\x00"+link.OrgID] = dwsidentity.Version(link)
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
