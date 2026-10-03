package handler

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/multica-ai/multica/server/internal/dwsclient"
)

// A native IM event names its conversation only by id, so a native group
// scene would stay untitled (the configure page shows 「群聊」). Before a
// group event is dispatched its title is read as the identity itself sees
// the group (DWSNativeConversationTitle) and carried as the dispatch's
// conversation title, which scene.Resolve stores in agent_scene.title like
// a Router delivery's. The cache below only saves a lookup per message; the
// scene directory is where the title lives.

const (
	nativeTitleTTL        = 10 * time.Minute
	nativeTitleMissTTL    = time.Minute
	nativeTitleTimeout    = 3 * time.Second
	nativeTitleCacheLimit = 4096
)

type nativeTitleEntry struct {
	title   string
	expires time.Time
}

// nativeTitleCache keeps recently read titles per process.
type nativeTitleCache struct {
	mu      sync.Mutex
	entries map[string]nativeTitleEntry
}

var nativeConversationTitles = &nativeTitleCache{entries: map[string]nativeTitleEntry{}}

func (c *nativeTitleCache) get(key string, now time.Time) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok || !now.Before(entry.expires) {
		return "", false
	}
	return entry.title, true
}

func (c *nativeTitleCache) put(key, title string, ttl time.Duration, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) >= nativeTitleCacheLimit {
		for k, entry := range c.entries {
			if !now.Before(entry.expires) {
				delete(c.entries, k)
			}
		}
		if len(c.entries) >= nativeTitleCacheLimit {
			c.entries = map[string]nativeTitleEntry{}
		}
	}
	c.entries[key] = nativeTitleEntry{title: title, expires: now.Add(ttl)}
}

// nativeConversationTitle returns the title of a native group event's
// conversation as id sees it, "" when it cannot be read: the failure is
// logged and the message is dispatched untitled (the scene keeps the title
// it has).
func (h *Handler) nativeConversationTitle(ctx context.Context, id dwsclient.Identity, conversationID string) string {
	lookup := h.DWSNativeConversationTitle
	conversationID = strings.TrimSpace(conversationID)
	if lookup == nil || conversationID == "" {
		return ""
	}
	key := strings.Join([]string{id.AgentID, id.UID, id.OrgID, conversationID}, "\x00")
	if title, ok := nativeConversationTitles.get(key, nativeClock()); ok {
		return title
	}
	lookupCtx, cancel := context.WithTimeout(ctx, nativeTitleTimeout)
	defer cancel()
	title, err := lookup(lookupCtx, id, conversationID)
	if err != nil {
		slog.Warn("DWS native conversation title unavailable", "event", "dws_native_conversation_title_failed",
			"agent_id", id.AgentID, "error", err)
		nativeConversationTitles.put(key, "", nativeTitleMissTTL, nativeClock())
		return ""
	}
	title = clipRunes(strings.TrimSpace(title), 256)
	ttl := nativeTitleTTL
	if title == "" {
		ttl = nativeTitleMissTTL
	}
	nativeConversationTitles.put(key, title, ttl, nativeClock())
	return title
}
