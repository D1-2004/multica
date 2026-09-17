package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestNativeSessionIdentity(t *testing.T) {
	const a = "session-9fc30af6-b6e3-42b5-bed9-beddaa0156db"
	const b = "9fc30af6-b6e3-42b5-bed9-beddaa0156db"
	for _, tc := range []struct {
		body, want string
		bad        bool
	}{
		{`{"args":{"request":{"sessionId":"` + a + `"}}}`, a, false},
		{`{"args":{"request":{"address":{"kind":"subagent","parentSessionId":"` + a + `","childSessionId":"` + b + `"}}}}`, a, false},
		{`{"args":{"request":{"address":{"kind":"session","sessionId":"` + b + `"}}}}`, b, false},
		{`{"args":{"request":{"sessionId":"` + a + `","address":{"kind":"session","sessionId":"` + b + `"}}}}`, "", true},
		{`{"args":{"request":{"sessionId":"../../other"}}}`, "", true},
		{`{"args":{"request":{"address":{"kind":"unknown"}}}}`, "", true},
		{`{"args":{"request":{}}, "host":"https://untrusted","sessionId":"` + a + `"}`, "", false},
	} {
		got, err := nativeSessionID(json.RawMessage(tc.body))
		if (err != nil) != tc.bad || got != tc.want {
			t.Fatalf("identity: got %q error %v", got, err)
		}
	}
}

func TestNativeMuxRoutesOwnersAndCancelsIndependently(t *testing.T) {
	closed := make(chan string, 4)
	var cleaned atomic.Int32
	upstream := func(name string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/remote.mux" || r.Header.Get("Origin") != "http://"+r.Host {
				t.Error("invalid upstream boundary")
				return
			}
			cookie, err := r.Cookie(dshNativeGatewayCookie)
			if err != nil || cookie.Value != name {
				t.Error("wrong owner capability")
				return
			}
			c, err := (&websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}).Upgrade(w, r, nil)
			if err != nil {
				return
			}
			defer c.Close()
			var f nativeMuxOpen
			if c.ReadJSON(&f) != nil {
				return
			}
			_ = c.WriteJSON(map[string]any{"type": "item", "streamId": f.StreamID, "value": name})
			for {
				if _, _, err = c.ReadMessage(); err != nil {
					closed <- name
					return
				}
			}
		}))
	}
	a, b := upstream("a"), upstream("b")
	defer a.Close()
	defer b.Close()
	permitted := atomic.Bool{}
	permitted.Store(true)
	resolve := func(ctx context.Context, sid string) (string, string, func(), error) {
		url, name := a.URL, "a"
		if strings.HasPrefix(sid, "session-") {
			url, name = b.URL, "b"
		}
		return url, name, func() { cleaned.Add(1) }, nil
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serveNativeMux(w, r, time.Now().Add(time.Minute), 10*time.Millisecond, func(context.Context) error {
			if !permitted.Load() {
				return errors.New("revoked")
			}
			return nil
		}, resolve)
	}))
	defer server.Close()
	c, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	send := func(id, sid string) {
		t.Helper()
		if err := c.WriteJSON(map[string]any{"type": "open", "streamId": id, "endpoint": "session/follow", "payload": map[string]any{"args": map[string]any{"request": map[string]any{"sessionId": sid}}}}); err != nil {
			t.Fatal(err)
		}
	}
	send("one", "9fc30af6-b6e3-42b5-bed9-beddaa0156db")
	send("two", "session-9fc30af6-b6e3-42b5-bed9-beddaa0156db")
	seen := map[string]string{}
	for i := 0; i < 2; i++ {
		var f struct{ StreamID, Value string }
		if c.ReadJSON(&f) != nil {
			t.Fatal("missing stream")
		}
		seen[f.StreamID] = f.Value
	}
	if seen["one"] != "a" || seen["two"] != "b" {
		t.Fatal("crossed owners", seen)
	}
	_ = c.WriteJSON(map[string]string{"type": "cancel", "streamId": "one"})
	select {
	case name := <-closed:
		if name != "a" {
			t.Fatal("wrong stream cancelled")
		}
	case <-time.After(time.Second):
		t.Fatal("cancel did not close upstream")
	}
	permitted.Store(false)
	if _, _, err = c.ReadMessage(); err == nil {
		t.Fatal("revocation did not close browser")
	}
	select {
	case name := <-closed:
		if name != "b" {
			t.Fatal(name)
		}
	case <-time.After(time.Second):
		t.Fatal("revocation left upstream open")
	}
	deadline := time.Now().Add(time.Second)
	for cleaned.Load() != 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if cleaned.Load() != 2 {
		t.Fatal("routed grants not revoked")
	}
}

func TestNativeTitleSurvivesLiveFrames(t *testing.T) {
	titles := map[string]string{"s": "Platform chat"}
	for _, body := range []string{
		`{"type":"item","streamId":"a","value":{"type":"snapshot","projections":{"asOfSeq":57,"values":{"title":"stale","other":true}}}}`,
		`{"type":"item","streamId":"a","value":{"type":"projection","sessionId":"s","key":"title","value":"stale","seq":58}}`,
		`{"type":"item","streamId":"a","value":{"type":"baseline","value":{"projections":{"s":{"asOfSeq":57,"values":{"title":"stale","other":true}}}}}}`,
	} {
		got := string(nativeFrameTitle("s", []byte(body), titles))
		if strings.Contains(got, "stale") || !strings.Contains(got, "Platform chat") {
			t.Fatal(got)
		}
	}
	raw := `{"type":"item","streamId":"a","value":{"type":"plugin-event","title":"opaque"}}`
	if got := string(nativeFrameTitle("s", []byte(raw), titles)); got != raw {
		t.Fatal("changed opaque frame")
	}
}

func TestNativePluginRPCsStayOpaque(t *testing.T) {
	for _, method := range []string{"market/install", "custom/session/prompt", "dws/authorize", "commands/execute", "session/create", "session/search"} {
		if nativeSessionMethod(method) {
			t.Fatalf("plugin/global RPC was intercepted: %s", method)
		}
	}
	for _, method := range []string{"session/page", "session/prompt", "session/cancel", "session/updateQueue", "subagents/prompt"} {
		if !nativeSessionMethod(method) {
			t.Fatalf("owner RPC omitted: %s", method)
		}
	}
	// A plugin-owned stream can use unrelated identities without DSH parsing.
	sid, err := nativeStreamSession(nativeMuxOpen{Endpoint: "plugin/follow", Payload: json.RawMessage(`{"args":{"request":{"sessionId":"opaque-plugin-id"}}}`)})
	if err != nil || sid != "" {
		t.Fatal("plugin stream identity was interpreted", sid, err)
	}
}
