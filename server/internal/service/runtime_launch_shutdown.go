package service

import (
	"context"
	"errors"
	"sync"
)

var errRuntimeLaunchShutdown = errors.New("runtime launcher stopped for server shutdown")

type runtimeLaunchShutdown struct {
	mu      sync.Mutex
	closing bool
	active  map[*runtimeLaunchHandle]struct{}
	wg      sync.WaitGroup
}
type runtimeLaunchHandle struct{ cancel context.CancelCauseFunc }

func (s *TaskService) CurrentRuntimeStartRecoveryConfig() RuntimeStartRecoveryConfig {
	if s != nil && s.RuntimeStartRecoveryConfig != nil {
		return s.RuntimeStartRecoveryConfig()
	}
	return RuntimeStartRecoveryConfig{}
}

func (s *TaskService) trackRuntimeLaunch(parent context.Context) (context.Context, func(), bool) {
	if !s.CurrentRuntimeStartRecoveryConfig().RecoverAbandonedLaunches {
		return parent, func() {}, true
	}
	state := &s.launchShutdown
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.closing {
		return parent, func() {}, false
	}
	ctx, cancel := context.WithCancelCause(parent)
	h := &runtimeLaunchHandle{cancel: cancel}
	if state.active == nil {
		state.active = make(map[*runtimeLaunchHandle]struct{})
	}
	state.active[h] = struct{}{}
	state.wg.Add(1)
	return ctx, func() {
		cancel(context.Canceled)
		state.mu.Lock()
		delete(state.active, h)
		state.mu.Unlock()
		state.wg.Done()
	}, true
}

// Stop admission, cancel owned launchers, then join their existing lease release.
// A launcher which cannot stop keeps its lease until expiry; never release a
// lease underneath an operation which may still submit a runner.
func (s *TaskService) ShutdownRuntimeLaunches(ctx context.Context) error {
	if s == nil || !s.CurrentRuntimeStartRecoveryConfig().RecoverAbandonedLaunches {
		return nil
	}
	state := &s.launchShutdown
	state.mu.Lock()
	state.closing = true
	for h := range state.active {
		h.cancel(errRuntimeLaunchShutdown)
	}
	state.mu.Unlock()
	done := make(chan struct{})
	go func() { state.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
