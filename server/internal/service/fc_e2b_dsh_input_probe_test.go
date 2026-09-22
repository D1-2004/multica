package service

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/dshhost"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestDSHInputProbeBoundsConcurrentAndFailedRetries(t *testing.T) {
	b := newDSHNativeAuthorityBridge("fixture")
	key := dshSessionCapabilityKey{workspace: uuid.New(), agent: uuid.New(), generation: 1, sandbox: "fixture"}
	now := time.Now()
	var wins atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if b.beginInputProbe(key, now) {
				wins.Add(1)
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("concurrent probes=%d", wins.Load())
	}
	for _, delay := range []time.Duration{30 * time.Second, 60 * time.Second, 120 * time.Second, 120 * time.Second} {
		b.finishInputProbe(key, now, errors.New("offline"))
		if b.beginInputProbe(key, now.Add(delay-time.Nanosecond)) {
			t.Fatal("early retry")
		}
		now = now.Add(delay)
		if !b.beginInputProbe(key, now) {
			t.Fatal("recovery suppressed")
		}
	}
	b.finishInputProbe(key, now, nil)
	if !b.beginInputProbe(key, now.Add(30*time.Second)) {
		t.Fatal("success did not reset delay")
	}
	replacement := key
	replacement.generation++
	if !b.beginInputProbe(replacement, now) {
		t.Fatal("generation inherited failure")
	}
	replacement = key
	replacement.scope = uuid.New()
	if !b.beginInputProbe(replacement, now) {
		t.Fatal("scope inherited failure")
	}
	replacement = key
	replacement.sandbox = "replacement"
	if !b.beginInputProbe(replacement, now) {
		t.Fatal("sandbox inherited failure")
	}
}
func TestDSHSessionInputsFailedExecIsNotRepeatedAndDoesNotLeakOutput(t *testing.T) {
	host := dshhost.Host{Key: dshhost.Key{WorkspaceID: uuid.New(), AgentID: uuid.New()}, State: "running", Generation: 1, SandboxID: "fixture"}
	runner := &fakeCommandRunner{errs: []error{errors.New("secret transport body"), errors.New("secret transport body")}}
	l := &FCE2BLauncher{nativeAuthority: newDSHNativeAuthorityBridge("fixture"), Runner: runner, Config: FCE2BConfig{DSHNativeAuthority: "https://pre.multica.test", Domain: "fc.test", APIKey: "fixture", APIURL: "https://api.test"}}
	err := l.EnsureDSHSessionInputs(context.Background(), host, dshhost.NativeAccessManager{}, nil)
	if DSHSessionInputFailureCode(err) != "gateway_exec_failed" || strings.Contains(err.Error(), "secret") {
		t.Fatalf("unsafe error: %v", err)
	}
	for i := 0; i < 100; i++ {
		if err = l.EnsureDSHSessionInputs(context.Background(), host, dshhost.NativeAccessManager{}, nil); !errors.Is(err, ErrDSHInputProbeDeferred) {
			t.Fatalf("got %v", err)
		}
	}
	if len(runner.calls) != 1 {
		t.Fatalf("spawned %d", len(runner.calls))
	}
	host.Generation++
	_ = l.EnsureDSHSessionInputs(context.Background(), host, dshhost.NativeAccessManager{}, nil)
	if len(runner.calls) != 2 {
		t.Fatal("new generation not probed")
	}
}
