package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/eventrouter"
	"github.com/multica-ai/multica/server/internal/integrations/agentmessagerouter"
	"github.com/multica-ai/multica/server/internal/scene"
)

// GetAgentEventIngress exposes the existing endpoint locator, never its
// delivery credential. Reading it neither creates nor rebinds an endpoint.
func (h *Handler) GetAgentEventIngress(w http.ResponseWriter, r *http.Request) {
	caller, ok := h.agentSceneAdmin(w, r)
	if !ok {
		return
	}
	reader, available := h.DispatchEndpoints.(interface {
		Get(context.Context, pgtype.UUID) (agentmessagerouter.DispatchEndpoint, error)
	})
	if !available {
		writeError(w, http.StatusServiceUnavailable, "dispatch endpoints unavailable")
		return
	}
	ep, err := reader.Get(r.Context(), caller.agent.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "dispatch endpoint not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "dispatch endpoint unavailable")
		return
	}
	row, err := h.Queries.GetAgentDispatchEndpoint(r.Context(), caller.agent.ID)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "dispatch endpoint unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"agent_id": caller.agentID, "tenant_org_id": caller.orgID,
		"dispatch_url": ep.DispatchURL, "endpoint_namespace_id": uuidToString(row.ID)})
}

type eventReceiptView struct {
	ID            string             `json:"id"`
	Source        string             `json:"source"`
	SourceEventID string             `json:"source_event_id"`
	TenantOrgID   string             `json:"tenant_org_id"`
	AgentScene    *scene.Ref         `json:"agent_scene,omitempty"`
	Route         string             `json:"route"`
	State         string             `json:"state"`
	Reason        string             `json:"reason"`
	ConfigVersion string             `json:"config_version"`
	CreatedAt     time.Time          `json:"created_at"`
	Event         *eventrouter.Event `json:"event,omitempty"`
}

// ListAgentEventReceipts is a human manage-gated read. Every query has both
// owner ids and a currently served tenant; a receipt grants no access itself.
func (h *Handler) ListAgentEventReceipts(w http.ResponseWriter, r *http.Request) {
	caller, ok := h.agentSceneAdmin(w, r)
	if !ok {
		return
	}
	org := strings.TrimSpace(r.URL.Query().Get("org_id"))
	if org == "" {
		org = caller.orgID
	}
	owner := scene.Owner{WorkspaceID: caller.agent.WorkspaceID, AgentID: caller.agent.ID}
	served, err := agentTenantOrg(r.Context(), h.Queries, owner, org)
	if err != nil || served == "" {
		status := http.StatusServiceUnavailable
		if eventrouter.UnmappedReason(err) != "" || served == "" && err == nil {
			status = http.StatusNotFound
		}
		writeError(w, status, "tenant unavailable")
		return
	}
	limit := 20
	if raw := r.URL.Query().Get("limit"); raw != "" {
		v, e := strconv.Atoi(raw)
		if e != nil || v < 1 || v > 100 {
			writeError(w, http.StatusBadRequest, "limit must be 1-100")
			return
		}
		limit = v
	}
	idFilter := r.URL.Query().Get("receipt_id")
	var receiptID pgtype.UUID
	if idFilter != "" {
		var ok bool
		receiptID, ok = parseUUIDOrBadRequest(w, idFilter, "receipt_id")
		if !ok {
			return
		}
	}
	rows, err := h.DB.Query(r.Context(), `SELECT id,source,source_event_id,tenant_org_id,scene_id,route,state,reason,config_version,created_at,envelope
		FROM scene_event_receipt WHERE workspace_id=$1 AND agent_id=$2 AND tenant_org_id=$3
		AND ($4::text='' OR source_event_id=$4) AND ($5::uuid IS NULL OR id=$5)
		ORDER BY created_at DESC,id DESC LIMIT $6`, caller.agent.WorkspaceID, caller.agent.ID, served, r.URL.Query().Get("source_event_id"), receiptID, limit)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "event receipts unavailable")
		return
	}
	defer rows.Close()
	out := []eventReceiptView{}
	for rows.Next() {
		var v eventReceiptView
		var id, sid pgtype.UUID
		var raw []byte
		if err := rows.Scan(&id, &v.Source, &v.SourceEventID, &v.TenantOrgID, &sid, &v.Route, &v.State, &v.Reason, &v.ConfigVersion, &v.CreatedAt, &raw); err != nil {
			writeError(w, http.StatusServiceUnavailable, "event receipt unavailable")
			return
		}
		v.ID = uuidToString(id)
		if sid.Valid {
			ref := scene.Ref{SceneID: uuidToString(sid)}
			if _, err := fencedScene(r.Context(), h.Queries, &ref, owner, served); err == nil {
				v.AgentScene = &ref
			}
		}
		if r.URL.Query().Get("include_payload") == "true" {
			var event eventrouter.Event
			if err := json.Unmarshal(raw, &event); err != nil {
				writeError(w, http.StatusServiceUnavailable, "event envelope unavailable")
				return
			}
			v.Event = &event
		}
		out = append(out, v)
	}
	if rows.Err() != nil {
		writeError(w, http.StatusServiceUnavailable, "event receipts unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"receipts": out})
}
