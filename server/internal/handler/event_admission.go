package handler

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/multica-ai/multica/server/internal/eventrouter"
	"github.com/multica-ai/multica/server/internal/scene"
	dwsevents "github.com/multica-ai/multica/server/pkg/dws/events"
)

type nativeEventContextKey struct{}

func eventPayloadFingerprint(base string, payload json.RawMessage) string {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return ""
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(append(append([]byte(base), '\n'), canonical...))
	return fmt.Sprintf("sha256:%x", sum)
}

// Native metadata comes from the owned source, never from the HTTP wire.
func withNativeEvent(ctx context.Context, ev dwsevents.Event) context.Context {
	return context.WithValue(ctx, nativeEventContextKey{}, ev)
}

func dispatchEventCategory(c DispatchCommand) string {
	if c.Control != nil && c.Control.Action == "cancel" {
		return eventrouter.Control
	}
	if c.Event.Domain == "channel" && c.Event.Type == "message.created" {
		return eventrouter.UserMessage
	}
	return eventrouter.Observation
}

// dispatchProviderEvent preserves only the business event, not identity tokens,
// prompt material or callback secrets. Unknown native payload fields survive.
func dispatchProviderEvent(ctx context.Context, raw []byte, c DispatchCommand, source, id string) (eventrouter.Event, error) {
	var wire struct {
		Event json.RawMessage `json:"event"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return eventrouter.Event{}, err
	}
	e := eventrouter.Event{Version: eventrouter.Version, ID: id, Source: source,
		Type: c.Event.Domain + "." + c.Event.Type, Category: dispatchEventCategory(c),
		OccurredAt: dispatchMessageOccurredAt(c), PayloadSchema: "messagerouter.dispatch/2.0.event", Payload: wire.Event}
	if c.Control != nil {
		var err error
		e.Payload, err = json.Marshal(struct {
			Event   json.RawMessage  `json:"event"`
			Control *DispatchControl `json:"control"`
		}{wire.Event, c.Control})
		if err != nil {
			return eventrouter.Event{}, err
		}
		e.PayloadSchema = "messagerouter.dispatch/2.0.control"
	}
	if native, ok := ctx.Value(nativeEventContextKey{}).(dwsevents.Event); ok {
		e.Type = native.Key
		e.PayloadSchema = "dws.native.event/1"
		e.ID = firstNonEmpty(strings.TrimSpace(native.ID), id)
		var err error
		e.Payload, err = json.Marshal(native)
		if err != nil {
			return eventrouter.Event{}, err
		}
	}
	return e, e.Validate()
}

// admitDispatchEvent is the routing seam before any scene business handler.
// true means the receipt was held/rejected and the caller must stop.
func (h *Handler) admitDispatchEvent(w http.ResponseWriter, r *http.Request, raw []byte, c *DispatchCommand, dc agentDispatchContext) bool {
	if h.Queries == nil || h.TxStarter == nil {
		if h.EventRouteConfig != nil {
			route, _ := h.EventRouteConfig(uuidToString(dc.WorkspaceID), uuidToString(dc.AgentID), dispatchRecordedOrg(*c))
			if route == eventrouter.Unified {
				writeError(w, http.StatusServiceUnavailable, "event routing storage unavailable")
				return true
			}
		}
		// Storage-less unit/self-host dispatch retains the existing entry.
		h.attachDispatchScene(r.Context(), c, dc)
		return false
	}
	owner := scene.Owner{WorkspaceID: dc.WorkspaceID, AgentID: dc.AgentID}
	org, err := agentTenantOrg(r.Context(), h.Queries, owner, dispatchRecordedOrg(*c))
	reason := eventrouter.UnmappedReason(err)
	if err != nil && reason == "" {
		writeError(w, http.StatusServiceUnavailable, "event tenant lookup unavailable")
		return true
	}
	if err != nil {
		org = dispatchRecordedOrg(*c)
	}
	loc, obs, locErr := dispatchSceneLocator(*c, org)
	if reason == "" {
		reason = eventrouter.UnmappedReason(locErr)
	}
	route, version := eventrouter.Legacy, "default"
	if h.EventRouteConfig != nil {
		route, version = h.EventRouteConfig(uuidToString(dc.WorkspaceID), uuidToString(dc.AgentID), org)
	}
	if route == eventrouter.Unified && h.EventRouteReady != nil {
		ready, err := h.EventRouteReady(r.Context())
		if err != nil || !ready {
			writeError(w, http.StatusServiceUnavailable, "event router replicas are not ready")
			return true
		}
	}
	transport := "messagerouter"
	if isNativeDispatch(r.Context()) {
		transport = "dws-native"
	}
	namespace := uuidToString(dc.EndpointNamespaceID)
	if namespace == "" {
		digest := sha256.Sum256([]byte(dc.EndpointID))
		namespace = fmt.Sprintf("legacy-endpoint:%x", digest)
	}
	source := fmt.Sprintf("%s/%s", transport, namespace)
	c.DispatchEndpointID = uuidToString(dc.EndpointNamespaceID)
	key := dispatchIdempotencyKey(r, *c)
	e, err := dispatchProviderEvent(r.Context(), raw, *c, source, key)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid event envelope")
		return true
	}
	fingerprint := dispatchRequestFingerprint(*c, key)
	// Include the complete business payload on Router deliveries, including
	// fields the current typed projection does not understand. Native quoted
	// author enrichment keeps the existing stable native fingerprint rule.
	if transport == "messagerouter" {
		fingerprint = eventPayloadFingerprint(fingerprint, e.Payload)
	}
	hook, stop := h.prepareEmployeeReceipt(w, r, *c, dc, org, reason, e)
	if stop {
		return true
	}
	receipt, replay, err := eventrouter.AdmitWithHook(r.Context(), h.TxStarter, e, eventrouter.Host{
		Owner: owner, PrincipalID: dc.UserID, TenantOrgID: org, Locator: loc, Observation: obs,
		UnmappedReason: reason, Route: route, ConfigVersion: version, Fingerprint: fingerprint,
	}, hook)
	if err != nil {
		status := http.StatusServiceUnavailable
		message := "event admission failed"
		if errors.Is(err, eventrouter.ErrInvalidEvent) {
			status = http.StatusBadRequest
		}
		if errors.Is(err, eventrouter.ErrConflict) || errors.Is(err, scene.ErrStaleTenant) {
			status = http.StatusConflict
		}
		if errors.Is(err, eventrouter.ErrConflict) {
			message = "idempotency key conflicts with another dispatch"
		}
		writeError(w, status, message)
		return true
	}
	c.AgentScene = nil
	c.EventReceiptID = uuidToString(receipt.ID)
	if receipt.SceneID.Valid {
		ref := scene.Ref{SceneID: uuidToString(receipt.SceneID)}
		if _, err := fencedScene(r.Context(), h.Queries, &ref, owner, receipt.TenantOrgID); err != nil {
			status := http.StatusServiceUnavailable
			if eventrouter.UnmappedReason(err) != "" || errors.Is(err, scene.ErrNotFound) {
				status = http.StatusConflict
			}
			writeError(w, status, "event scene is inactive")
			return true
		}
		c.AgentScene = &ref
	}
	slog.Info("event routed to scene entry", "event", "agent_event_routed",
		"receipt_id", uuidToString(receipt.ID), "workspace_id", uuidToString(dc.WorkspaceID),
		"agent_id", uuidToString(dc.AgentID), "scene_id", uuidToString(receipt.SceneID),
		"route", receipt.Route, "state", receipt.State, "reason", receipt.Reason,
		"config_version", receipt.ConfigVersion, "provider", transport, "replay", replay)
	if h.admitEmployeeScene(w, r, c, dc, receipt) {
		return true
	}
	if receipt.State == eventrouter.Unmapped {
		writeJSON(w, http.StatusAccepted, map[string]string{"event_receipt_id": uuidToString(receipt.ID), "state": receipt.State, "reason": receipt.Reason})
		return true
	}
	// Event-only admission ends at the durable scene entry. Dispatches with a
	// callback continue through the existing idempotent business admission.
	// A callback-less consumer cannot safely execute a replayed business effect.
	if receipt.Route == eventrouter.Unified && c.CompletionCallback == nil && (c.Control == nil || c.Control.Action != "cancel") {
		writeJSON(w, http.StatusAccepted, map[string]string{"event_receipt_id": uuidToString(receipt.ID), "scene_id": uuidToString(receipt.SceneID), "state": receipt.State})
		return true
	}
	return false
}
