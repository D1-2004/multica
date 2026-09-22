package service

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"time"

	"github.com/multica-ai/multica/server/internal/dshhost"
)

// This is a per-replica scheduling hint, never an authorization or readiness cache.
// A new Host generation/sandbox gets a new budget. Interactive access still uses
// its own live authority validation and is not delayed by background retry state.
var ErrDSHInputProbeDeferred = errors.New("DSH input probe is deferred")

type dshInputProbe struct {
	inFlight bool
	retryAt  time.Time
	failures uint
}

type dshInputProbeError struct {
	code  string
	cause error
}

func (e *dshInputProbeError) Error() string { return e.code }
func (e *dshInputProbeError) Unwrap() error { return e.cause }

// Never expose CLI output, transport tokens or sandbox receipt bodies in logs.
func DSHSessionInputFailureCode(err error) string {
	var e *dshInputProbeError
	if errors.As(err, &e) {
		return e.code
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "deadline_exceeded"
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	return "authority_connection_unconfirmed"
}

func (b *dshNativeAuthorityBridge) beginInputProbe(key dshSessionCapabilityKey, now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.inputProbes == nil {
		b.inputProbes = make(map[dshSessionCapabilityKey]*dshInputProbe)
	}
	for k, p := range b.inputProbes {
		if !p.inFlight && now.Sub(p.retryAt) > 10*time.Minute {
			delete(b.inputProbes, k)
		}
	}
	p := b.inputProbes[key]
	if p == nil {
		p = &dshInputProbe{}
		b.inputProbes[key] = p
	}
	if p.inFlight || now.Before(p.retryAt) {
		return false
	}
	p.inFlight = true
	return true
}
func (b *dshNativeAuthorityBridge) finishInputProbe(key dshSessionCapabilityKey, now time.Time, err error, scanCancelled bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	p := b.inputProbes[key]
	p.inFlight = false
	// Scanner cancellation is a local budget/shutdown event, not a failed Host.
	if scanCancelled {
		return
	}
	delay := 30 * time.Second
	if err != nil {
		if p.failures < 3 {
			p.failures++
		}
		delay *= time.Duration(1 << (p.failures - 1)) // 30s, 60s, capped at 120s.
	} else {
		p.failures = 0
	}
	// Even a successful but immediately disconnected transport must not spin.
	if err != nil {
		delay += time.Duration(rand.Int64N(int64(delay / 4)))
	}
	p.retryAt = now.Add(delay)
}
func (l *FCE2BLauncher) refreshDSHSessionInputs(ctx context.Context, host dshhost.Host, manager dshhost.NativeAccessManager, submit DSHNativePromptSubmit) error {
	if l == nil || l.nativeAuthority == nil {
		return errors.New("DSH authority is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	key := sessionCapabilityKey(host)
	if !l.nativeAuthority.beginInputProbe(key, time.Now()) {
		return ErrDSHInputProbeDeferred
	}
	started := time.Now()
	_, err := l.ensureDSHNativeAuthority(ctx, host, manager, submit, true)
	l.nativeAuthority.finishInputProbe(key, time.Now(), err, ctx.Err() != nil)
	result := "connected"
	if err != nil {
		result = DSHSessionInputFailureCode(err)
	}
	slog.Info("DSH background input probe finished", "workspace_id", host.WorkspaceID, "agent_id", host.AgentID, "scope_id", host.ScopeID, "sandbox_id", host.SandboxID, "generation", host.Generation, "elapsed_ms", time.Since(started).Milliseconds(), "result", result)
	return err
}
