package handler

import (
	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/dshhost"
	"sync"
	"time"
)

type dshInputRetryKey struct {
	workspace, agent, scope uuid.UUID
	sandbox                 string
	generation              int64
}
type dshInputRetry struct {
	failures int
	next     time.Time
}

// Per-replica retry throttling only, not Host ownership or authoritative state.
// Each replica must still maintain its own input transport; a restart resets
// this optimization without changing any persisted Host lifecycle.
type dshInputRetries struct {
	mu      sync.Mutex
	entries map[dshInputRetryKey]dshInputRetry
}

func newDSHInputRetries() *dshInputRetries {
	return &dshInputRetries{entries: make(map[dshInputRetryKey]dshInputRetry)}
}
func dshInputKey(h dshhost.Host) dshInputRetryKey {
	return dshInputRetryKey{h.WorkspaceID, h.AgentID, h.ScopeID, h.SandboxID, h.Generation}
}
func (r *dshInputRetries) due(h dshhost.Host, now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return !now.Before(r.entries[dshInputKey(h)].next)
}
func (r *dshInputRetries) record(h dshhost.Host, err error, now time.Time) time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	k := dshInputKey(h)
	if err == nil {
		delete(r.entries, k)
		return 0
	}
	e := r.entries[k]
	if e.failures < 5 {
		e.failures++
	}
	delay := 30 * time.Second * time.Duration(1<<(e.failures-1))
	if delay > 5*time.Minute {
		delay = 5 * time.Minute
	}
	e.next = now.Add(delay)
	r.entries[k] = e
	return delay
}
func (r *dshInputRetries) retain(hosts []dshhost.Host) {
	live := make(map[dshInputRetryKey]bool, len(hosts))
	for _, h := range hosts {
		live[dshInputKey(h)] = true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for k := range r.entries {
		if !live[k] {
			delete(r.entries, k)
		}
	}
}
