package handler

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/service/userdecision"
)

// ReceiveUserDecisionEvent is mounted only behind the internal bearer gate.
func (h *Handler) ReceiveUserDecisionEvent(w http.ResponseWriter, r *http.Request) {
	if h.UserDecisions == nil {
		writeError(w, 503, "user decision service unavailable")
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		writeError(w, 400, "invalid card event size")
		return
	}
	event, err := userdecision.ParseAuditEvent(raw)
	if err != nil {
		writeError(w, 400, "invalid card event")
		return
	}
	outcome, err := h.UserDecisions.Store.Accept(r.Context(), event)
	if err != nil {
		writeError(w, 409, "card event could not be matched or persisted")
		return
	}
	writeJSON(w, 200, map[string]string{"outcome": outcome})
}

// UserDecisionHealth exposes durable consumer liveness without credentials or
// sender identifiers. It uses the same workspace/private-agent access as export.
func (h *Handler) UserDecisionHealth(w http.ResponseWriter, r *http.Request) {
	agent, ok := h.loadAgentForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	ws := uuidToString(agent.WorkspaceID)
	actorType, actorID := h.resolveActor(r, requestUserID(r), ws)
	if !h.canAccessPrivateAgent(r.Context(), agent, actorType, actorID, ws) {
		writeError(w, 403, "you do not have access to this agent")
		return
	}
	if h.UserDecisions == nil {
		writeError(w, 503, "user decision service unavailable")
		return
	}
	rows, err := h.UserDecisions.Pool.Query(r.Context(), `SELECT jsonb_build_object(
 'decision_id',d.id,'state',d.state,'environment',d.environment,
 'consumer_present',c.owner IS NOT NULL,'consumer_ready',COALESCE(c.ready,false),
 'consumer_lease_live',COALESCE(c.lease_expires_at>clock_timestamp(),false),
 'consumer_updated_at',c.updated_at,'consumer_lease_expires_at',c.lease_expires_at,
 'last_event_at',(SELECT max(e.created_at) FROM coordinator_user_decision_event e WHERE e.decision_id=d.id),
 'card_update_pending',d.card_update_pending,'decision_updated_at',d.updated_at)
 FROM coordinator_user_decision d LEFT JOIN coordinator_user_decision_consumer c
 ON c.environment=d.environment AND c.sender_uid=d.sender_uid AND c.sender_org_id=d.sender_org_id
 WHERE d.workspace_id=$1 AND d.agent_id=$2 AND d.environment=$3 ORDER BY d.created_at DESC LIMIT 100`, ws, uuidToString(agent.ID), h.UserDecisions.Store.Environment)
	if err != nil {
		writeError(w, 500, "could not read decision health")
		return
	}
	defer rows.Close()
	items := []json.RawMessage{}
	for rows.Next() {
		var raw json.RawMessage
		if rows.Scan(&raw) != nil {
			writeError(w, 500, "could not decode decision health")
			return
		}
		items = append(items, raw)
	}
	if rows.Err() != nil {
		writeError(w, 500, "could not read decision health")
		return
	}
	rows.Close()
	metrics, err := h.UserDecisions.Store.Metrics(r.Context(), ws, uuidToString(agent.ID))
	if err != nil {
		writeError(w, 500, "could not read decision metrics")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]any{"environment": h.UserDecisions.Store.Environment, "decisions": items, "metrics": metrics})
}

func (h *Handler) ExportUserDecisions(w http.ResponseWriter, r *http.Request) {
	agent, ok := h.loadAgentForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	ws := uuidToString(agent.WorkspaceID)
	actorType, actorID := h.resolveActor(r, requestUserID(r), ws)
	if !h.canAccessPrivateAgent(r.Context(), agent, actorType, actorID, ws) {
		writeError(w, 403, "you do not have access to this agent")
		return
	}
	if h.UserDecisions == nil {
		writeError(w, 503, "user decision service unavailable")
		return
	}
	from := time.Unix(0, 0)
	to := time.Now().Add(time.Minute)
	for key, target := range map[string]*time.Time{"from": &from, "to": &to} {
		if value := r.URL.Query().Get(key); value != "" {
			parsed, err := time.Parse(time.RFC3339, value)
			if err != nil {
				writeError(w, 400, "invalid "+key+" time")
				return
			}
			*target = parsed
		}
	}
	limit := 100
	if value := r.URL.Query().Get("limit"); value != "" {
		n, err := strconv.Atoi(value)
		if err != nil || n < 1 || n > 1000 {
			writeError(w, 400, "limit must be 1..1000")
			return
		}
		limit = n
	}
	rows, err := h.UserDecisions.Pool.Query(r.Context(), `SELECT to_jsonb(d),COALESCE((SELECT jsonb_agg(jsonb_build_object('event_id',e.event_id,'operator_id',e.operator_id,'outcome',e.outcome,'received_at',e.created_at,'payload',e.payload,'protocol',e.protocol,'delivery_count',e.delivery_count,'last_received_at',e.last_received_at,'last_delivery_outcome',e.last_delivery_outcome) ORDER BY e.created_at) FROM coordinator_user_decision_event e WHERE e.decision_id=d.id),'[]'::jsonb)
 FROM coordinator_user_decision d WHERE workspace_id=$1 AND agent_id=$2 AND created_at >=$3 AND created_at<$4 AND ($5='' OR state=$5) AND ($6='' OR version=$6) AND ($7='' OR id::text=$7) AND ($8='' OR id::text>$8) ORDER BY id LIMIT $9`, ws, uuidToString(agent.ID), from, to, r.URL.Query().Get("state"), r.URL.Query().Get("version"), r.URL.Query().Get("decision_id"), r.URL.Query().Get("after"), limit)
	if err != nil {
		writeError(w, 500, "could not read decisions")
		return
	}
	defer rows.Close()
	// Finish all reads before starting the response, so an object-store failure
	// cannot produce an apparently successful truncated JSONL download.
	var samples []map[string]any
	totalBytes := 0
	for rows.Next() {
		var raw, events []byte
		if rows.Scan(&raw, &events) != nil {
			writeError(w, 500, "could not decode decision")
			return
		}
		var request userdecision.Request
		var sample map[string]any
		if json.Unmarshal(raw, &request) != nil || json.Unmarshal(raw, &sample) != nil {
			writeError(w, 500, "invalid decision snapshot")
			return
		}
		snapshot, err := h.UserDecisions.Store.Snapshot(r.Context(), request)
		if err != nil {
			writeError(w, 503, "snapshot unavailable")
			return
		}
		totalBytes += len(raw) + len(events) + len(snapshot)
		if totalBytes > 32<<20 {
			writeError(w, 413, "export exceeds 32 MiB; use a smaller limit or time range")
			return
		}
		var input, interactions any
		if json.Unmarshal(snapshot, &input) != nil || json.Unmarshal(events, &interactions) != nil {
			writeError(w, 500, "invalid dataset")
			return
		}
		sample["snapshot"] = input
		sample["interactions"] = interactions
		for _, key := range []string{"sender_uid", "sender_org_id", "lease_token", "lease_expires_at", "send_request_id"} {
			delete(sample, key)
		}
		sample["label_source"] = "user_submission_not_gold"
		sample["split_group"] = request.ConversationID
		samples = append(samples, userdecision.ExportValue(sample).(map[string]any))
	}
	if rows.Err() != nil {
		writeError(w, 500, "dataset read failed")
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if len(samples) > 0 {
		w.Header().Set("X-Next-Cursor", samples[len(samples)-1]["id"].(string))
	}
	enc := json.NewEncoder(w)
	for _, sample := range samples {
		if enc.Encode(sample) != nil {
			return
		}
	}
}
