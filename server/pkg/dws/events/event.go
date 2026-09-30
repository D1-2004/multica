// Package events keeps DingTalk personal event streams connected from the
// backend: one WebSocket per identity, fed by subscriptions reconciled
// against the product's configuration.
//
// A Listener keeps one identity connected: it reconciles subscriptions,
// fetches a fresh ticket, dials, answers pings, dedupes, hands each event to
// the host (which persists it) and acks only after that succeeded, and
// reconnects with backoff. A Manager runs a Listener for every configured
// identity, holding a lease per identity so exactly one replica connects,
// and picks identities back up after a restart. All shared state lives in a
// Store (Redis or a database); the host is told about outages through Alert.
package events

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/pkg/dws"
)

// Event is one personal event.
type Event struct {
	// ID is the dedupe key: the eventId (header, then payload), else the
	// frame's messageId, else a content hash.
	ID  string `json:"id"`
	Key string `json:"key"` // a dws.Event* key
	// SubscriptionID is the subscription that matched.
	SubscriptionID string    `json:"subscriptionId,omitempty"`
	CorpID         string    `json:"corpId,omitempty"`
	OccurredAt     time.Time `json:"occurredAt,omitempty"`

	// Common IM fields, filled when the body has them.
	ConversationID       string `json:"conversationId,omitempty"`
	MessageID            string `json:"messageId,omitempty"`
	SenderOpenDingTalkID string `json:"senderOpenDingTalkId,omitempty"`
	Sender               string `json:"sender,omitempty"`
	Content              string `json:"content,omitempty"`

	// Malformed: the frame could not be decoded. It is still handed to
	// Handle so nothing is acked unrecorded; Data holds the raw frame data,
	// redacted, or a placeholder when text that may carry a VoIP room code
	// could not be parsed. Key is set when a header named it.
	Malformed bool `json:"malformed,omitempty"`

	// Body is payload.body, the event-specific fields.
	Body json.RawMessage `json:"body,omitempty"`
	// Data is the whole decoded frame data.
	Data json.RawMessage `json:"data,omitempty"`
}

// frame is the DingTalk stream wire frame.
type frame struct {
	SpecVersion string         `json:"specVersion"`
	Type        string         `json:"type"`
	Headers     map[string]any `json:"headers"`
	Data        string         `json:"data"`
}

// header is the first non-empty header among names, matched exactly and
// then case-insensitively, trimmed, as dws reads frame headers.
func (f frame) header(names ...string) string {
	for _, name := range names {
		if s := headerString(f.Headers[name]); s != "" {
			return s
		}
		for k, v := range f.Headers {
			if strings.EqualFold(k, name) {
				if s := headerString(v); s != "" {
					return s
				}
			}
		}
	}
	return ""
}

func headerString(v any) string {
	if v == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(v))
}

// Header and data names dws accepts for the event key and the subscription.
var (
	keyHeaders = []string{"eventType", "EVENT_TYPE", "event_type", "EVENT_KEY", "event_key", "eventKey"}
	subHeaders = []string{"SUB_ID", "subscribe_id", "subscribeId", "sub_id", "subId"}
)

var errMalformedEvent = errors.New("malformed event frame")

// decodeEvent reads an EVENT/CALLBACK frame. data may be JSON-encoded once
// or twice; keys come from headers first, like dws does. The id is the
// event's own id wherever it is carried, before the frame's messageId, which
// may change between deliveries of one event.
func decodeEvent(f frame) (Event, error) {
	data := unwrapJSON(json.RawMessage(f.Data))
	var d struct {
		EventID      string          `json:"eventId"`
		EventIDSnake string          `json:"event_id"`
		EventKey     string          `json:"eventKey"`
		EventKeySnk  string          `json:"event_key"`
		SubID        string          `json:"subId"`
		SubIDSnake   string          `json:"sub_id"`
		SubscribeID  string          `json:"subscribeId"`
		SubscribeSnk string          `json:"subscribe_id"`
		OccurredAtMs int64           `json:"occurredAtMs"`
		Payload      json.RawMessage `json:"payload"`
	}
	if (len(data) == 0 && strings.TrimSpace(f.Data) != "") || (len(data) > 0 && json.Unmarshal(data, &d) != nil) {
		return Event{}, errMalformedEvent
	}
	dataKey := strings.TrimSpace(firstNonEmpty(d.EventKey, d.EventKeySnk))
	// Hosts store and forward events; a VoIP room code lets anyone join. The
	// header and the data may disagree on the key, so either one counts, and
	// a room code under any other key is dropped as well.
	if f.header(keyHeaders...) == dws.EventVoIPInvite || topic(f) == dws.EventVoIPInvite ||
		dataKey == dws.EventVoIPInvite || mentionsRoomCode(data) {
		data = redactRoomCode(data)
		d.Payload = nil
		_ = json.Unmarshal(data, &d)
	}
	var payload struct {
		CorpID string          `json:"corpid"`
		Body   json.RawMessage `json:"body"`
	}
	_ = json.Unmarshal(unwrapJSON(d.Payload), &payload)
	body := unwrapJSON(payload.Body)
	if voip := f.header(keyHeaders...) == dws.EventVoIPInvite || topic(f) == dws.EventVoIPInvite ||
		dataKey == dws.EventVoIPInvite; voip && (mentionsRoomCode(data) || mentionsRoomCode(body)) {
		// Whatever encoding hid it from the redactor, a VoIP invite that
		// still names a room code is not handed on.
		return Event{}, errMalformedEvent
	}

	ev := Event{
		ID:             firstNonEmpty(f.header("eventId", "event_id"), strings.TrimSpace(firstNonEmpty(d.EventID, d.EventIDSnake)), f.header("messageId", "MESSAGE_ID")),
		Key:            firstNonEmpty(f.header(keyHeaders...), topic(f), dataKey),
		SubscriptionID: firstNonEmpty(f.header(subHeaders...), strings.TrimSpace(firstNonEmpty(d.SubID, d.SubIDSnake, d.SubscribeID, d.SubscribeSnk))),
		CorpID:         firstNonEmpty(f.header("eventCorpId"), payload.CorpID),
		Body:           body,
		Data:           data,
	}
	if d.OccurredAtMs > 0 {
		ev.OccurredAt = time.UnixMilli(d.OccurredAtMs)
	}
	var im struct {
		OpenConversationID   string `json:"openConversationId"`
		OpenMessageID        string `json:"openMessageId"`
		OpenSourceMessageID  string `json:"openSourceMessageId"`
		SenderOpenDingTalkID string `json:"senderOpenDingTalkId"`
		Sender               string `json:"sender"`
		Content              any    `json:"content"`
	}
	if len(body) > 0 && json.Unmarshal(body, &im) == nil {
		ev.ConversationID = im.OpenConversationID
		ev.MessageID = firstNonEmpty(im.OpenMessageID, im.OpenSourceMessageID)
		ev.SenderOpenDingTalkID = im.SenderOpenDingTalkID
		ev.Sender = im.Sender
		if s, ok := im.Content.(string); ok {
			ev.Content = s
		}
	}
	if ev.ID == "" {
		sum := sha256.Sum256([]byte(f.Data))
		ev.ID = ev.Key + ":" + f.header("eventBornTime", "time") + ":" + hex.EncodeToString(sum[:8])
	}
	if ev.Key == "" {
		return Event{}, errMalformedEvent
	}
	return ev, nil
}

// topic is the TOPIC header unless it is the "*" wildcard.
func topic(f frame) string {
	if t := f.header("TOPIC", "topic"); t != "*" {
		return t
	}
	return ""
}

// unwrapJSON decodes a JSON string that itself holds JSON.
func unwrapJSON(raw json.RawMessage) json.RawMessage {
	for i := 0; i < 2; i++ {
		var s string
		if json.Unmarshal(raw, &s) != nil {
			break
		}
		raw = json.RawMessage(s)
	}
	if !json.Valid(raw) {
		return nil
	}
	return raw
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// redactRoomCode drops every roomCode field (any case, with _ or -) from a
// JSON value, including inside JSON-encoded strings, as dws does for VoIP
// events. A value that is not JSON comes back unchanged.
func redactRoomCode(raw json.RawMessage) json.RawMessage {
	v, ok := decodeJSONNumber(raw)
	if !ok {
		return raw
	}
	v, changed := redactRoomCodeValue(v, 0)
	if !changed {
		return raw
	}
	return encodeJSON(v)
}

// undecodableRoomCode replaces text that names a room code but does not
// parse: the code cannot be cut out of it, so none of it is kept.
const undecodableRoomCode = "[dropped: undecodable text that may hold a VoIP room code]"

// redactRoomCodeValue reports whether it removed anything, so values that
// hold no room code keep their original bytes. Strings holding JSON, once
// or several times encoded, are redacted inside and re-encoded the same way.
func redactRoomCodeValue(v any, depth int) (any, bool) {
	switch x := v.(type) {
	case map[string]any:
		changed := false
		for k, value := range x {
			if isRoomCodeName(k) {
				delete(x, k)
				changed = true
				continue
			}
			next, c := redactRoomCodeValue(value, depth)
			x[k], changed = next, changed || c
		}
		return x, changed
	case []any:
		changed := false
		for i := range x {
			next, c := redactRoomCodeValue(x[i], depth)
			x[i], changed = next, changed || c
		}
		return x, changed
	case string:
		t := strings.TrimSpace(x)
		if t == "" || (t[0] != '{' && t[0] != '[' && t[0] != '"') {
			return x, false
		}
		inner, ok := decodeJSONNumber(json.RawMessage(t))
		if !ok || depth >= 4 {
			if mentionsRoomCode([]byte(x)) {
				return undecodableRoomCode, true
			}
			return x, false
		}
		inner, changed := redactRoomCodeValue(inner, depth+1)
		if !changed {
			return x, false
		}
		return string(encodeJSON(inner)), true
	default:
		return v, false
	}
}

func isRoomCodeName(name string) bool {
	return roomCodeNormalizer.Replace(strings.ToLower(name)) == "roomcode"
}

var roomCodeNormalizer = strings.NewReplacer("_", "", "-", "")

// mentionsRoomCode reports raw text that may carry a room code field.
func mentionsRoomCode(raw []byte) bool {
	return strings.Contains(roomCodeNormalizer.Replace(strings.ToLower(string(raw))), "roomcode")
}

// safeRawData is rawData for a frame that did not decode: a room code
// cannot be removed from text that does not parse, so such text is dropped
// (fail closed, as dws does) and only its absence is recorded.
func safeRawData(data string, voip bool) json.RawMessage {
	// Frame data may be string-encoded more than once.
	for i := 0; i < 2; i++ {
		var s string
		if json.Unmarshal([]byte(data), &s) != nil {
			break
		}
		data = s
	}
	dropped, _ := json.Marshal(undecodableRoomCode)
	if !json.Valid([]byte(data)) && (voip || mentionsRoomCode([]byte(data))) {
		return dropped
	}
	out := redactRoomCode(rawData(data))
	if mentionsRoomCode(out) {
		// A malformed frame is only recorded; losing a text that merely
		// names a room code costs less than keeping a code.
		return dropped
	}
	return out
}

// decodeJSONNumber decodes one JSON value, keeping numbers exact.
func decodeJSONNumber(raw json.RawMessage) (any, bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if dec.Decode(&v) != nil || dec.More() {
		return nil, false
	}
	return v, true
}

func encodeJSON(v any) json.RawMessage {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	return json.RawMessage(bytes.TrimRight(b.Bytes(), "\n"))
}
