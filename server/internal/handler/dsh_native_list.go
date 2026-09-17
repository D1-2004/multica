package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/multica-ai/multica/server/internal/dshhost"
)

func (h *Handler) nativeSessionTitles(ctx context.Context, access dshhost.NativeAccess) (map[string]string, error) {
	rows, err := h.DB.Query(ctx, `SELECT s.session_id,COALESCE(c.title,i.title,'') FROM dsh_employee_session s
 LEFT JOIN chat_session c ON s.scope_kind='chat' AND c.id=s.scope_id AND c.workspace_id=s.workspace_id AND c.agent_id=s.agent_id
 LEFT JOIN issue i ON s.scope_kind='issue' AND i.id=s.scope_id AND i.workspace_id=s.workspace_id
 WHERE s.workspace_id=$1 AND s.agent_id=$2`, access.WorkspaceID, access.AgentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	titles := map[string]string{}
	for rows.Next() {
		var id, title string
		if err = rows.Scan(&id, &title); err != nil {
			return nil, err
		}
		titles[id] = title
	}
	return titles, rows.Err()
}

func nativeProjectionTitle(projection map[string]any, title string) {
	if title == "" || projection == nil {
		return
	}
	values, ok := projection["values"].(map[string]any)
	if !ok {
		values = map[string]any{}
		projection["values"] = values
	}
	values["title"] = title
}

// Read one official snapshot without creating/resuming an Agent or taking its
// write handle. Cold reads therefore use DSH's revision-keyed persistence seam.
func (h *Handler) nativeSessionSnapshot(ctx context.Context, access dshhost.NativeAccess, token, sid string) (map[string]any, error) {
	host, _, err := h.nativeSessionHost(ctx, access, sid)
	if err != nil {
		return nil, err
	}
	upstream, child, cleanup, err := h.nativeTarget(ctx, access, token, host)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	c, err := nativeDial(ctx, upstream, child)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			c.Close()
		case <-done:
		}
	}()
	id := uuid.NewString()
	err = c.WriteJSON(map[string]any{"type": "open", "streamId": id, "endpoint": "session/follow", "payload": map[string]any{"args": map[string]any{"request": map[string]any{"address": map[string]any{"kind": "session", "sessionId": sid}, "maxMessages": 1}}}})
	if err != nil {
		return nil, err
	}
	kind, b, err := c.ReadMessage()
	if err != nil {
		return nil, err
	}
	var reply struct {
		Type     string `json:"type"`
		StreamID string `json:"streamId"`
		Value    struct {
			Type        string         `json:"type"`
			Projections map[string]any `json:"projections"`
		} `json:"value"`
	}
	if kind != websocket.TextMessage || json.Unmarshal(b, &reply) != nil || reply.Type != "item" || reply.StreamID != id || reply.Value.Type != "snapshot" || reply.Value.Projections == nil {
		return nil, errors.New("native snapshot unavailable")
	}
	return reply.Value.Projections, nil
}

func (h *Handler) serveNativeSessionList(w http.ResponseWriter, r *http.Request, access dshhost.NativeAccess, token string, body []byte) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	host := dshhost.Host{Key: access.Key, SandboxID: access.SandboxID, Generation: access.Generation, State: "running"}
	upstream, _, err := h.FCE2BLauncher.DSHNativeProxyAddress(host)
	if err != nil {
		writeError(w, 503, "native list unavailable")
		return
	}
	req, err := http.NewRequestWithContext(ctx, "POST", upstream+"/api/session/list", bytes.NewReader(body))
	if err != nil {
		writeError(w, 503, "native list unavailable")
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", upstream)
	req.AddCookie(&http.Cookie{Name: dshNativeGatewayCookie, Value: token})
	res, err := (&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
	if err != nil {
		writeError(w, 503, "native list unavailable")
		return
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, (8<<20)+1))
	if err != nil || len(raw) > 8<<20 {
		writeError(w, 503, "native list unavailable")
		return
	}
	if res.StatusCode != 200 {
		w.WriteHeader(res.StatusCode)
		_, _ = w.Write(raw)
		return
	}
	var reply map[string]any
	if json.Unmarshal(raw, &reply) != nil {
		writeError(w, 503, "native list unavailable")
		return
	}
	result, _ := reply["result"].(map[string]any)
	value, _ := result["value"].(map[string]any)
	items, ok := value["items"].([]any)
	if !ok {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(raw)
		return
	}
	titles, err := h.nativeSessionTitles(ctx, access)
	if err != nil {
		writeError(w, 503, "native titles unavailable")
		return
	}
	// Bounded concurrent cold reads. Only persisted platform sessions need
	// cross-process refresh; native-only rows remain owned by this page's Host.
	var group sync.WaitGroup
	slots := make(chan struct{}, 4)
	for _, item := range items {
		row, ok := item.(map[string]any)
		if !ok {
			continue
		}
		sid, _ := row["sessionId"].(string)
		title, known := titles[sid]
		if !known || !dshhost.ValidSessionID(sid) {
			continue
		}
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			writeError(w, 503, "native list refresh timed out")
			group.Wait()
			return
		}
		group.Add(1)
		go func(row map[string]any, sid, title string) {
			defer group.Done()
			defer func() { <-slots }()
			projection, err := h.nativeSessionSnapshot(ctx, access, token, sid)
			if err != nil {
				// Do not present an old projection as current. Keep the discoverable row
				// and authoritative title; opening the Session reports owner availability.
				projection = map[string]any{"asOfSeq": float64(-1), "values": map[string]any{}}
			}
			nativeProjectionTitle(projection, title)
			row["projections"] = projection
			if v, ok := projection["values"].(map[string]any); ok {
				if metadata, ok := v["sessionListMetadata"].(map[string]any); ok {
					if at, ok := metadata["lastPromptAt"].(float64); ok {
						row["updatedAt"] = at
					}
					if blank, ok := metadata["blank"].(bool); ok {
						row["blank"] = blank
					}
				}
			}
		}(row, sid, title)
	}
	group.Wait()
	sort.SliceStable(items, func(i, j int) bool {
		a, _ := items[i].(map[string]any)
		b, _ := items[j].(map[string]any)
		x, _ := a["updatedAt"].(float64)
		y, _ := b["updatedAt"].(float64)
		return x > y
	})
	writeJSON(w, 200, reply)
}

// Preserve platform titles across both the initial snapshot and live projections.
// Unrelated plugin payloads are left byte-for-byte intact.
func nativeFrameTitle(sid string, body []byte, titles map[string]string) []byte {
	var frame map[string]any
	if json.Unmarshal(body, &frame) != nil || frame["type"] != "item" {
		return body
	}
	value, _ := frame["value"].(map[string]any)
	changed := false
	switch value["type"] {
	case "snapshot":
		if title := titles[sid]; title != "" {
			p, _ := value["projections"].(map[string]any)
			if p != nil {
				nativeProjectionTitle(p, title)
				changed = true
			}
		}
	case "projection":
		id, _ := value["sessionId"].(string)
		if value["key"] == "title" && titles[id] != "" {
			value["value"] = titles[id]
			changed = true
		}
	case "baseline":
		baseline, _ := value["value"].(map[string]any)
		projections, _ := baseline["projections"].(map[string]any)
		for id, p := range projections {
			if title := titles[id]; title != "" {
				if projection, ok := p.(map[string]any); ok {
					nativeProjectionTitle(projection, title)
					changed = true
				}
			}
		}
	}
	if !changed {
		return body
	}
	result, err := json.Marshal(frame)
	if err != nil {
		return body
	}
	return result
}
