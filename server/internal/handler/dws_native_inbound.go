package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"

	"github.com/multica-ai/multica/server/internal/dwsclient"
	dwsevents "github.com/multica-ai/multica/server/pkg/dws/events"
)

// nativeEventLine is the dws CLI ndjson line a native event arrives as
// (dwsclient.EventLine).
type nativeEventLine struct {
	EventID     string `json:"event_id"`
	CorpID      string `json:"event_corp_id"`
	EventType   string `json:"event_type"`
	SubscribeID string `json:"subscribe_id"`
	Data        string `json:"data"`
}

// decodeNativeEvent rebuilds the event of a line and its typed message.
func decodeNativeEvent(line []byte) (dwsevents.Event, *dwsevents.MessageEvent, error) {
	var raw nativeEventLine
	if err := json.Unmarshal(line, &raw); err != nil {
		return dwsevents.Event{}, nil, err
	}
	ev := dwsevents.Event{ID: raw.EventID, Key: raw.EventType, CorpID: raw.CorpID,
		SubscriptionID: raw.SubscribeID, Data: json.RawMessage(raw.Data)}
	typed, err := ev.Typed()
	if err != nil {
		return ev, nil, err
	}
	message, ok := typed.(*dwsevents.MessageEvent)
	if !ok {
		return ev, nil, errors.New("not a message event")
	}
	return ev, message, nil
}

// HandleDWSNativeEvent receives one IM event of an execution identity with
// native subscription on. An event that does not decode is acknowledged
// (redelivery would not make it readable); a dispatch failure is returned so
// the event is delivered again.
func (h *Handler) HandleDWSNativeEvent(ctx context.Context, id dwsclient.Identity, line []byte) error {
	ev, message, err := decodeNativeEvent(line)
	if err != nil {
		slog.Warn("DWS native event not decodable", "event", "dws_native_event_undecodable",
			"agent_id", id.AgentID, "event_key", ev.Key, "event_id", ev.ID, "error", err)
		return nil
	}
	slog.Info("DWS native event received", "event", "dws_native_event_received",
		"agent_id", id.AgentID, "event_key", ev.Key, "event_id", ev.ID,
		"has_content", message.Content != "", "quoted", message.QuotedMessage != nil)
	return h.dispatchNativeMessage(ctx, id, ev, message)
}

// dispatchNativeMessage hands a native message to the agent's inbound
// pipeline (acceptNativeMessage in dws_native_dispatch.go).
func (h *Handler) dispatchNativeMessage(ctx context.Context, id dwsclient.Identity, ev dwsevents.Event, message *dwsevents.MessageEvent) error {
	return h.acceptNativeMessage(ctx, id, ev, message)
}
