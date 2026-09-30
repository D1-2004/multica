package events

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/pkg/dws"
)

func leaksCode(raw json.RawMessage) bool { return strings.Contains(string(raw), "sensitive-code") }

func jsonString(s string) string { b, _ := json.Marshal(s); return string(b) }

// A truncated VoIP frame whose data arrives string-encoded twice (a form
// unwrapJSON accepts): unwrapString peels one level, the rest is a valid JSON
// string, so safeRawData's json.Valid gate skips the fail-closed drop.
func TestDoubleEncodedTruncatedVoIPFrameKeepsNoRoomCode(t *testing.T) {
	trunc := `{"eventKey":"user_voip_call_receive_invite","payload":{"body":{"roomCode":"sensitive-code","roomId":"r"`
	data := jsonString(jsonString(trunc))
	ev := frameEvent(frame{Type: "EVENT", Headers: map[string]any{"eventType": dws.EventVoIPInvite, "messageId": "m"}, Data: data})
	t.Logf("malformed=%v data=%s", ev.Malformed, ev.Data)
	if leaksCode(ev.Data) {
		t.Errorf("room code leaked from a double-encoded truncated VoIP frame")
	}
}

// A decodable VoIP frame whose payload is a string holding truncated JSON:
// the redactor cannot parse the string and keeps it.
func TestUnparsableStringPayloadKeepsNoRoomCode(t *testing.T) {
	inner := `{"body":{"roomCode":"sensitive-code","roomId":"r"`
	data := `{"eventId":"e","eventKey":"user_voip_call_receive_invite","payload":` + jsonString(inner) + `}`
	ev, err := decodeEvent(frame{Type: "EVENT", Headers: map[string]any{"messageId": "m"}, Data: data})
	t.Logf("err=%v malformed=%v data=%s", err, ev.Malformed, ev.Data)
	if leaksCode(ev.Data) {
		t.Errorf("room code leaked from an unparseable string payload")
	}
}

// A body string-encoded twice: unwrapJSON(payload.Body) peels both levels,
// the redactor descends only one.
func TestDoubleEncodedBodyKeepsNoRoomCode(t *testing.T) {
	body := `{"roomCode":"sensitive-code","roomId":"r"}`
	data := `{"eventId":"e","eventKey":"user_voip_call_receive_invite","payload":{"body":` + jsonString(jsonString(body)) + `}}`
	ev, err := decodeEvent(frame{Type: "EVENT", Headers: map[string]any{"messageId": "m"}, Data: data})
	t.Logf("err=%v body=%s data=%s", err, ev.Body, ev.Data)
	if leaksCode(ev.Body) || leaksCode(ev.Data) {
		t.Errorf("room code leaked from a double-encoded body")
	}
}

// Header event_id is used by decodeEvent but not by the malformed path.
func TestMalformedFramesUseTheEventIDHeader(t *testing.T) {
	good := frameEvent(frame{Type: "EVENT", Headers: map[string]any{"event_id": "E1", "messageId": "m1", "eventType": dws.EventIMAt}, Data: `{}`})
	bad := frameEvent(frame{Type: "EVENT", Headers: map[string]any{"event_id": "E1", "messageId": "m2", "eventType": dws.EventIMAt}, Data: `not json`})
	t.Logf("good.ID=%s bad.ID=%s", good.ID, bad.ID)
	if bad.ID != "E1" {
		t.Errorf("malformed id = %s; redelivery with a new messageId is not deduped", bad.ID)
	}
}
