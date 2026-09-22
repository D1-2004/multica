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
		b.finishInputProbe(key, now, errors.New("offline"), false)
		if b.beginInputProbe(key, now.Add(delay-time.Nanosecond)) {
			t.Fatal("early retry")
		}
		retryAt := b.inputProbes[key].retryAt
		if retryAt.Before(now.Add(delay)) || !retryAt.Before(now.Add(delay+delay/4)) {
			t.Fatal("retry jitter out of bounds")
		}
		now = retryAt
		if !b.beginInputProbe(key, now) {
			t.Fatal("recovery suppressed")
		}
	}
	b.finishInputProbe(key, now, nil, false)
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
	if !l.DSHSessionInputsNeedProbe(host) {
		t.Fatal("new host not due")
	}
	err := l.EnsureDSHSessionInputs(context.Background(), host, dshhost.NativeAccessManager{}, nil)
	if DSHSessionInputFailureCode(err) != "gateway_exec_failed" || strings.Contains(err.Error(), "secret") {
		t.Fatalf("unsafe error: %v", err)
	}
	if l.DSHSessionInputsNeedProbe(host) {
		t.Fatal("failed host remained due")
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

func TestDSHInputProbeSchedulingKeepsReplicaAndLiveTransportIndependent(t *testing.T) {
	host := dshhost.Host{Key: dshhost.Key{WorkspaceID: uuid.New(), AgentID: uuid.New()}, SandboxID: "fixture", Generation: 1}
	a := &FCE2BLauncher{nativeAuthority: newDSHNativeAuthorityBridge("fixture")}
	b := &FCE2BLauncher{nativeAuthority: newDSHNativeAuthorityBridge("fixture")}
	key := sessionCapabilityKey(host)
	a.nativeAuthority.beginInputProbe(key, time.Now())
	if a.DSHSessionInputsNeedProbe(host) || !b.DSHSessionInputsNeedProbe(host) {
		t.Fatal("replica probe budgets coupled")
	}
	worker := &dshAuthorityWorker{until: time.Now().Add(-time.Minute)}
	b.nativeAuthority.workers["true/"+host.WorkspaceID.String()+"/"+host.AgentID.String()+"/1/authority/fixture/origin/token"] = worker
	if b.DSHSessionInputsNeedProbe(host) || worker.until.Before(time.Now()) {
		t.Fatal("live input transport not renewed")
	}
	worker.failed = true
	if !b.DSHSessionInputsNeedProbe(host) {
		t.Fatal("failed input transport cannot recover")
	}
}

func TestDSHInputProbeCancellationDoesNotPenalizeHost(t *testing.T) {
	b := newDSHNativeAuthorityBridge("fixture")
	key := dshSessionCapabilityKey{sandbox: "cancel-test"}
	now := time.Now()
	b.beginInputProbe(key, now)
	b.finishInputProbe(key, now, context.Canceled, true)
	if !b.beginInputProbe(key, now) {
		t.Fatal("cancelled scan delayed recovery")
	}
	b.finishInputProbe(key, now, errors.New("real failure"), false)
	retryAt := b.inputProbes[key].retryAt
	if !b.beginInputProbe(key, retryAt) {
		t.Fatal("failed host not due")
	}
	b.finishInputProbe(key, retryAt, context.DeadlineExceeded, true)
	if b.inputProbes[key].failures != 1 || !b.beginInputProbe(key, retryAt) {
		t.Fatal("scan timeout changed failure budget")
	}
}
