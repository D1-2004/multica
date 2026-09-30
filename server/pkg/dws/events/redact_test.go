package events

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/pkg/dws"
)

// Hosts persist Event.Data and Body; a VoIP invite's room code must not be
// in them, even when the payload arrives JSON-encoded as a string.
func TestVoIPEventsAreStoredWithoutTheRoomCode(t *testing.T) {
	inner := voipData()
	var doc map[string]any
	if err := json.Unmarshal([]byte(inner), &doc); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(doc["payload"])
	doc["payload"] = string(payload) // payload as a JSON string
	nested, _ := json.Marshal(doc)
	for name, data := range map[string]string{"object": inner, "string payload": string(nested)} {
		encoded, _ := json.Marshal(data)
		ev, err := decodeEvent(frame{Type: "EVENT", Headers: map[string]any{"eventType": dws.EventVoIPInvite, "messageId": "f1"}, Data: string(encoded)})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, raw := range []json.RawMessage{ev.Data, ev.Body} {
			if strings.Contains(string(raw), "sensitive-code") || strings.Contains(strings.ToLower(string(raw)), "roomcode") {
				t.Fatalf("%s: room code kept: %s", name, raw)
			}
		}
		if !strings.Contains(string(ev.Body), `"roomId":"room-1"`) || !strings.Contains(string(ev.Data), "1780630479124") {
			t.Fatalf("%s: other fields lost: %s / %s", name, ev.Body, ev.Data)
		}
		if _, err := ev.Typed(); err != nil {
			t.Fatalf("%s: typed: %v", name, err)
		}
	}
	// An unparsable frame is recorded as is, minus any room code.
	bad := frameEvent(frame{Type: "EVENT", Headers: map[string]any{"messageId": "f2"}, Data: `{"roomCode":"sensitive-code","x":1}`})
	if !bad.Malformed || strings.Contains(string(bad.Data), "sensitive-code") {
		t.Fatalf("malformed = %+v %s", bad, bad.Data)
	}
}

// Text that does not parse cannot be redacted, so it is not kept; a header
// and data that disagree on the key are redacted either way; and values
// without a room code keep their bytes.
func TestRoomCodeRedactionFailsClosed(t *testing.T) {
	leaks := func(raw json.RawMessage) bool {
		return strings.Contains(string(raw), "sensitive-code")
	}
	trunc := `{"eventKey":"user_voip_call_receive_invite","payload":{"body":{"roomCode":"sensitive-code","roomId":"r"`
	if ev := frameEvent(frame{Type: "EVENT", Headers: map[string]any{"messageId": "f1"}, Data: trunc}); !ev.Malformed || leaks(ev.Data) {
		t.Fatalf("truncated frame: %+v %s", ev, ev.Data)
	}
	if ev := frameEvent(frame{Type: "EVENT", Headers: map[string]any{"eventType": dws.EventVoIPInvite, "messageId": "f1"}, Data: `{"x":`}); !ev.Malformed || leaks(ev.Data) || len(ev.Data) == 0 {
		t.Fatalf("truncated VoIP frame: %s", ev.Data)
	}
	raw := []byte(`{"type":"EVENT","headers":{"eventType":"user_voip_call_receive_invite"},"data":"{\"roomCode\":\"sensitive-code\"`)
	if ev := unreadableEvent(raw); leaks(ev.Data) {
		t.Fatalf("unreadable frame: %s", ev.Data)
	}
	d := `{"eventId":"e","eventKey":"user_voip_call_receive_invite","payload":{"body":{"roomCode":"sensitive-code","roomId":"r"}}}`
	ev, err := decodeEvent(frame{Type: "EVENT", Headers: map[string]any{"eventType": "user_event", "messageId": "f3"}, Data: d})
	if err != nil || leaks(ev.Data) || leaks(ev.Body) || !strings.Contains(string(ev.Body), `"roomId":"r"`) {
		t.Fatalf("header/data mismatch: %v %s %s", err, ev.Data, ev.Body)
	}
	m := `{"payload":{"body":{"content":"[1, 2,   3]","n":12345678901234567890,"h":"<b>&"}}}`
	if ev := frameEvent(frame{Type: "EVENT", Headers: map[string]any{"messageId": "f4"}, Data: m}); string(ev.Data) != m {
		t.Fatalf("untouched data was re-encoded: %s", ev.Data)
	}
	if ev := frameEvent(frame{Type: "EVENT", Headers: map[string]any{"messageId": "f5"}, Data: "not json"}); string(ev.Data) != `"not json"` {
		t.Fatalf("plain text without a room code: %s", ev.Data)
	}
}

// dws reads the key and the subscription from several header and data
// names, in any case, trimmed.
func TestDecodeEventAcceptsDWSEnvelopeNames(t *testing.T) {
	cases := []struct {
		name    string
		headers map[string]any
		data    string
		key     string
		sub     string
	}{
		{"event_key header", map[string]any{"event_key": dws.EventIMAt, "subscribe_id": "s1"}, `{"eventId":"e"}`, dws.EventIMAt, "s1"},
		{"eventKey header", map[string]any{"eventKey": dws.EventIMAt, "subscribeId": "s2"}, `{"eventId":"e"}`, dws.EventIMAt, "s2"},
		{"any case, trimmed", map[string]any{"EventType": " " + dws.EventIMAt + " ", "Sub_Id": " s3 "}, `{"eventId":"e"}`, dws.EventIMAt, "s3"},
		{"data names", map[string]any{}, `{"eventId":"e","event_key":"` + dws.EventIMAt + `","subscribe_id":"s4"}`, dws.EventIMAt, "s4"},
		{"data subscribeId", map[string]any{}, `{"eventId":"e","eventKey":"` + dws.EventIMAt + `","subscribeId":"s5"}`, dws.EventIMAt, "s5"},
	}
	for _, c := range cases {
		ev, err := decodeEvent(frame{Type: "EVENT", Headers: c.headers, Data: c.data})
		if err != nil || ev.Key != c.key || ev.SubscriptionID != c.sub || ev.ID != "e" {
			t.Fatalf("%s: %+v %v", c.name, ev, err)
		}
	}
}
