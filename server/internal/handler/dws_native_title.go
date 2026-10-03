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
	nativeTitleTTL      = 10 * time.Minute
	nativeTitleMissTTL  = time.Minute
	nativeTitleTimeout  = 3 * time.Second
	nativeTTLCacheLimit = 4096
)

type nativeTTLEntry struct {
	title   string
	expires time.Time
}

// nativeTTLCache keeps recent native lookups (group titles, unresolved
// senders) per process.
type nativeTTLCache struct {
	mu      sync.Mutex
	entries map[string]nativeTTLEntry
}

var nativeConversationTitles = &nativeTTLCache{entries: map[string]nativeTTLEntry{}}

func (c *nativeTTLCache) get(key string, now time.Time) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok || !now.Before(entry.expires) {
		return "", false
	}
	return entry.title, true
}

func (c *nativeTTLCache) put(key, title string, ttl time.Duration, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) >= nativeTTLCacheLimit {
		for k, entry := range c.entries {
			if !now.Before(entry.expires) {
				delete(c.entries, k)
			}
		}
		if len(c.entries) >= nativeTTLCacheLimit {
			c.entries = map[string]nativeTTLEntry{}
		}
	}
	c.entries[key] = nativeTTLEntry{title: title, expires: now.Add(ttl)}
}

// nativeConversationTitle returns the title of a native group event's
// conversation as id sees it, "" when it cannot be read: the failure is
// logged and the message is dispatched untitled (the scene keeps the title
// it has). The read is detached from the stream's context, like the
// submission, so a stream handover does not turn it into a cached miss; a
// read that answers with no title is kept as long as a title (a refused
// internal group costs a group-list walk per read).
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
	lookupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), nativeTitleTimeout)
	defer cancel()
	title, err := lookup(lookupCtx, id, conversationID)
	if err != nil {
		slog.Warn("DWS native conversation title unavailable", "event", "dws_native_conversation_title_failed",
			"agent_id", id.AgentID, "error", err)
		nativeConversationTitles.put(key, "", nativeTitleMissTTL, nativeClock())
		return ""
	}
	title = clipRunes(strings.TrimSpace(title), 256)
	nativeConversationTitles.put(key, title, nativeTitleTTL, nativeClock())
	return title
}
