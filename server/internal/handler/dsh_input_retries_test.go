package handler

import (
	"errors"
	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/dshhost"
	"testing"
	"time"
)

func TestDSHInputFailureBackoffAndRecovery(t *testing.T) {
	r := newDSHInputRetries()
	h := dshhost.Host{Key: dshhost.Key{WorkspaceID: uuid.New(), AgentID: uuid.New()}, ScopeID: uuid.New(), SandboxID: "sandbox-test", Generation: 1}
	now := time.Unix(1, 0)
	for _, want := range []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 4 * time.Minute, 5 * time.Minute, 5 * time.Minute} {
		if !r.due(h, now) {
			t.Fatal("retry not eligible at deadline")
		}
		got := r.record(h, errors.New("failed"), now)
		if got != want {
			t.Fatalf("delay=%v want %v", got, want)
		}
		if r.due(h, now.Add(want-time.Nanosecond)) {
			t.Fatal("retry before deadline")
		}
		now = now.Add(want)
	}
	r.record(h, nil, now)
	if !r.due(h, now) {
		t.Fatal("success did not clear cooldown")
	}
	if d := r.record(h, errors.New("new failure"), now); d != 30*time.Second {
		t.Fatal("success did not reset failure streak")
	}
}
func TestDSHInputBackoffHostIsolationAndPruning(t *testing.T) {
	r := newDSHInputRetries()
	h := dshhost.Host{Key: dshhost.Key{WorkspaceID: uuid.New(), AgentID: uuid.New()}, ScopeID: uuid.New(), SandboxID: "sandbox-test", Generation: 1}
	now := time.Unix(1, 0)
	r.record(h, errors.New("failed"), now)
	for _, field := range []string{"scope", "sandbox", "generation"} {
		n := h
		switch field {
		case "scope":
			n.ScopeID = uuid.New()
		case "sandbox":
			n.SandboxID = "new"
		case "generation":
			n.Generation++
		}
		if !r.due(n, now) {
			t.Fatalf("old cooldown leaked to %s", field)
		}
	}
	r.retain(nil)
	if len(r.entries) != 0 {
		t.Fatal("stale host retained")
	}
	// Independent replicas must still establish their own transport.
	other := newDSHInputRetries()
	if !other.due(h, now) {
		t.Fatal("replica blocked")
	}
}
