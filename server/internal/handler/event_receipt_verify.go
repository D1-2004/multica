package handler

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/eventrouter"
	"github.com/multica-ai/multica/server/internal/scene"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// VerifyAgentEventReceipt is a pre-release operator probe. It uses a retained
// real provider envelope and the deployed admission code against PostgreSQL;
// every write is rolled back, and no business handler or provider send runs.
func (h *Handler) VerifyAgentEventReceipt(w http.ResponseWriter, r *http.Request) {
	if !h.EventReceiptVerificationEnabled {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	caller, ok := h.agentSceneAdmin(w, r)
	if !ok {
		return
	}
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "receiptId"), "receipt_id")
	if !ok {
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "receipt verification unavailable")
		return
	}
	defer tx.Rollback(r.Context())
	q := db.New(tx)
	var row db.SceneEventReceipt
	err = tx.QueryRow(r.Context(), `SELECT id,workspace_id,agent_id,principal_id,tenant_org_id,source,source_event_id,fingerprint,envelope,scene_id,route,state,reason,config_version,created_at FROM scene_event_receipt WHERE id=$1 AND workspace_id=$2 AND agent_id=$3`, id, caller.agent.WorkspaceID, caller.agent.ID).Scan(&row.ID, &row.WorkspaceID, &row.AgentID, &row.PrincipalID, &row.TenantOrgID, &row.Source, &row.SourceEventID, &row.Fingerprint, &row.Envelope, &row.SceneID, &row.Route, &row.State, &row.Reason, &row.ConfigVersion, &row.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "receipt not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "receipt unavailable")
		return
	}
	owner := scene.Owner{WorkspaceID: row.WorkspaceID, AgentID: row.AgentID}
	org, err := agentTenantOrg(r.Context(), q, owner, row.TenantOrgID)
	if err != nil || !row.SceneID.Valid {
		writeError(w, http.StatusConflict, "verification needs an active resolved receipt")
		return
	}
	sc, err := scene.Get(r.Context(), q, owner, row.SceneID)
	if err != nil || scene.CheckTenant(sc, org) != nil {
		writeError(w, http.StatusConflict, "receipt scene inactive")
		return
	}
	var event eventrouter.Event
	if json.Unmarshal(row.Envelope, &event) != nil {
		writeError(w, http.StatusServiceUnavailable, "receipt envelope unavailable")
		return
	}
	host := eventrouter.Host{Owner: owner, PrincipalID: row.PrincipalID, TenantOrgID: org,
		Locator: scene.Locator{Provider: sc.Provider, TenantOrgID: org, Namespace: sc.SourceNamespace, Kind: sc.SceneKind, ExternalID: sc.ExternalSceneID},
		Route:   eventrouter.Unified, ConfigVersion: "verification-current", Fingerprint: row.Fingerprint}
	if h.EventRouteConfig != nil {
		host.Route, host.ConfigVersion = h.EventRouteConfig(caller.workspaceID, caller.agentID, org)
	}
	pass := map[string]bool{}
	replayed, replay, e := eventrouter.Admit(r.Context(), tx, event, host)
	pass["replay_same_receipt_ref_and_frozen_route"] = e == nil && replay && replayed.ID == row.ID && replayed.SceneID == row.SceneID && replayed.Route == row.Route && replayed.ConfigVersion == row.ConfigVersion
	changed := host
	digest := sha256.Sum256(append([]byte(row.Fingerprint), []byte("changed-content")...))
	changed.Fingerprint = fmt.Sprintf("sha256:%x", digest)
	_, _, e = eventrouter.Admit(r.Context(), tx, event, changed)
	pass["changed_fingerprint_conflict"] = errors.Is(e, eventrouter.ErrConflict)
	changed = host
	changed.PrincipalID = parseUUID(uuid.NewString())
	_, _, e = eventrouter.Admit(r.Context(), tx, event, changed)
	pass["principal_conflict"] = errors.Is(e, eventrouter.ErrConflict)
	changed = host
	changed.TenantOrgID = "verify-other-tenant"
	_, _, e = eventrouter.Admit(r.Context(), tx, event, changed)
	pass["tenant_conflict"] = errors.Is(e, scene.ErrStaleTenant)
	newEvent := event
	newEvent.ID = "verify-" + uuid.NewString()
	missing := host
	missing.Route = eventrouter.Unified
	missing.Locator.Kind = ""
	held, _, e := eventrouter.Admit(r.Context(), tx, newEvent, missing)
	pass["unknown_locator_unmapped"] = e == nil && held.State == eventrouter.Unmapped && !held.SceneID.Valid
	missing.Route = eventrouter.Legacy
	missing.ConfigVersion = "disabled"
	missing.Locator.Kind = sc.SceneKind
	heldAgain, again, e := eventrouter.Admit(r.Context(), tx, newEvent, missing)
	pass["held_not_released_by_disabled_config"] = e == nil && again && heldAgain.ID == held.ID && heldAgain.State == eventrouter.Unmapped && !heldAgain.SceneID.Valid
	all := true
	for _, value := range pass {
		all = all && value
	}
	if err := tx.Rollback(r.Context()); err != nil {
		writeError(w, http.StatusServiceUnavailable, "verification rollback failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"passed": all, "checks": pass, "receipt_id": uuidToString(row.ID), "agent_scene": scene.Ref{SceneID: uuidToString(row.SceneID)}, "source": row.Source, "source_event_id": row.SourceEventID, "frozen_route": row.Route, "current_route": host.Route, "writes_rolled_back": true, "business_execution": false, "evidence_kind": "deployed_pg_contract_probe_on_real_event"})
}
