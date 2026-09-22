package service

import (
	"context"
	"errors"
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
func (b *dshNativeAuthorityBridge) finishInputProbe(key dshSessionCapabilityKey, now time.Time, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	p := b.inputProbes[key]
	p.inFlight = false
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
	_, err := l.ensureDSHNativeAuthority(ctx, host, manager, submit, true)
	l.nativeAuthority.finishInputProbe(key, time.Now(), err)
	return err
}
