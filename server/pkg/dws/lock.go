package dws

import "context"

// ctxLock is a mutex whose waiters give up when their context ends, so a
// caller with a deadline never waits out someone else's slow refresh or mint.
type ctxLock struct{ ch chan struct{} }

func newCtxLock() ctxLock { return ctxLock{ch: make(chan struct{}, 1)} }

func (l ctxLock) lock(ctx context.Context) error {
	select {
	case l.ch <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (l ctxLock) tryLock() bool {
	select {
	case l.ch <- struct{}{}:
		return true
	default:
		return false
	}
}

func (l ctxLock) unlock() { <-l.ch }
