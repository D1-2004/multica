package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/multica-ai/multica/server/internal/dshhost"
)

type nativeMuxOpen struct {
	Type     string          `json:"type"`
	StreamID string          `json:"streamId"`
	Endpoint string          `json:"endpoint"`
	Payload  json.RawMessage `json:"payload"`
}

func nativeStreamSession(frame nativeMuxOpen) (string, error) {
	// Only session follow owns a session-bound stream. Global Cordis/control
	// streams retain their original Host; their unrelated identities are opaque.
	if frame.Endpoint != "session/follow" {
		return "", nil
	}
	return nativeSessionID(frame.Payload)
}

func nativeDial(ctx context.Context, upstream, token string) (*websocket.Conn, error) {
	u, err := url.Parse(upstream)
	if err != nil {
		return nil, err
	}
	origin := u.Scheme + "://" + u.Host
	if u.Scheme == "https" {
		u.Scheme = "wss"
	} else {
		u.Scheme = "ws"
	}
	u.Path = "/api/remote.mux"
	u.RawQuery = ""
	u.Fragment = ""
	headers := http.Header{"Origin": {origin}, "Cookie": {dshNativeGatewayCookie + "=" + token}}
	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	c, res, err := dialer.DialContext(ctx, u.String(), headers)
	if res != nil && res.Body != nil {
		res.Body.Close()
	}
	if err == nil {
		c.SetReadLimit(8 << 20)
	}
	return c, err
}

// One upstream carrier per logical stream avoids mixing host-owned stream IDs,
// event acknowledgements, and cancellation when two sessions have different owners.
func (h *Handler) serveNativeSessionMux(w http.ResponseWriter, r *http.Request, access dshhost.NativeAccess, token string) {
	resolve := func(ctx context.Context, sid string) (string, string, func(), error) {
		host, _, err := h.nativeSessionHost(ctx, access, sid)
		if err != nil {
			return "", "", nil, err
		}
		return h.nativeReadTarget(ctx, access, token, host)
	}
	authorize := func(ctx context.Context) error {
		_, err := h.nativeProxyAuthorize(r.Clone(ctx), access)
		return err
	}
	titles, err := h.nativeSessionTitles(r.Context(), access)
	if err != nil {
		writeError(w, 503, "native session metadata unavailable")
		return
	}
	serveNativeMux(w, r, access.ExpiresAt, 5*time.Second, authorize, resolve, func(sid string, frame []byte) []byte { return nativeFrameTitle(sid, frame, titles) })
}

type nativeMuxResolver func(context.Context, string) (string, string, func(), error)

// The caller checks the browser origin and grant before upgrading.
func serveNativeMux(w http.ResponseWriter, r *http.Request, expires time.Time, interval time.Duration, authorize func(context.Context) error, resolve nativeMuxResolver, transform ...func(string, []byte) []byte) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	browser, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer browser.Close()
	browser.SetReadLimit(4 << 20)
	ctx, cancel := context.WithDeadline(r.Context(), expires)
	defer cancel()
	var writeMu, sessionsMu sync.Mutex
	streams := map[string]context.CancelFunc{}
	write := func(body []byte) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		_ = browser.SetWriteDeadline(time.Now().Add(10 * time.Second))
		return browser.WriteMessage(websocket.TextMessage, body)
	}
	failure := func(id string) {
		b, _ := json.Marshal(map[string]any{"type": "error", "streamId": id, "error": map[string]any{"code": "gateway/unavailable", "message": "Session owner is unavailable; reconnect from the workbench.", "details": map[string]any{}}})
		_ = write(b)
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				browser.Close()
				return
			case <-ticker.C:
				if err := authorize(ctx); err != nil {
					cancel()
					browser.Close()
					return
				}
			}
		}
	}()
	defer func() {
		sessionsMu.Lock()
		defer sessionsMu.Unlock()
		for _, stop := range streams {
			stop()
		}
	}()
	for {
		kind, body, err := browser.ReadMessage()
		if err != nil {
			return
		}
		var frame nativeMuxOpen
		if kind != websocket.TextMessage || json.Unmarshal(body, &frame) != nil || frame.StreamID == "" || len(frame.StreamID) > 128 {
			return
		}
		if frame.Type == "cancel" {
			sessionsMu.Lock()
			stop := streams[frame.StreamID]
			sessionsMu.Unlock()
			if stop != nil {
				stop()
			}
			continue
		}
		if frame.Type != "open" || frame.Endpoint == "" {
			return
		}
		sid, err := nativeStreamSession(frame)
		if err != nil {
			failure(frame.StreamID)
			continue
		}
		sessionsMu.Lock()
		if _, exists := streams[frame.StreamID]; exists || len(streams) >= 32 {
			sessionsMu.Unlock()
			return
		}
		streamCtx, stop := context.WithCancel(ctx)
		streams[frame.StreamID] = stop
		sessionsMu.Unlock()
		go func(frame nativeMuxOpen, body []byte, sid string) {
			defer stop()
			defer func() { sessionsMu.Lock(); delete(streams, frame.StreamID); sessionsMu.Unlock() }()
			upstream, child, cleanup, err := resolve(streamCtx, sid)
			if err != nil {
				failure(frame.StreamID)
				return
			}
			defer cleanup()
			conn, err := nativeDial(streamCtx, upstream, child)
			if err != nil {
				failure(frame.StreamID)
				return
			}
			defer conn.Close()
			done := make(chan struct{})
			defer close(done)
			go func() {
				select {
				case <-streamCtx.Done():
					conn.Close()
				case <-done:
				}
			}()
			if conn.WriteMessage(websocket.TextMessage, body) != nil {
				failure(frame.StreamID)
				return
			}
			for {
				kind, b, err := conn.ReadMessage()
				if err != nil {
					if streamCtx.Err() == nil {
						failure(frame.StreamID)
					}
					return
				}
				var reply struct {
					Type     string `json:"type"`
					StreamID string `json:"streamId"`
				}
				if kind != websocket.TextMessage || json.Unmarshal(b, &reply) != nil || reply.StreamID != frame.StreamID {
					failure(frame.StreamID)
					return
				}
				if len(transform) != 0 {
					b = transform[0](sid, b)
				}
				if write(b) != nil {
					cancel()
					return
				}
				if reply.Type == "end" || reply.Type == "error" {
					return
				}
			}
		}(frame, body, sid)
	}
}
