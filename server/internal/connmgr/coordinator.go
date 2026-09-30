package connmgr

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	// maxGroupLen bounds a coordinator group (Target.Key). Groups are embedded
	// in Redis keys and log lines, so they must stay short.
	maxGroupLen = 200

	// maxTokenLen bounds a lease token.
	maxTokenLen = 512
)

// State is the coordinator-visible lifecycle of one physical connection.
// Transitions are monotonic:
//
//	CONNECTING -> READY -> DRAINING
//
// A DRAINING member remains connected and consumes events until the
// coordinator reports that the configured READY target is met without it.
type State string

const (
	StateConnecting State = "CONNECTING"
	StateReady      State = "READY"
	StateDraining   State = "DRAINING"
)

var (
	// ErrLeaseLost means the token is no longer present. The caller must tear
	// its connection down; continuing after this error can exceed the globally
	// coordinated connection limit.
	ErrLeaseLost = errors.New("connmgr: lease lost")

	// ErrLeaseStateMismatch means the token still exists but is not in the
	// state required by the requested transition.
	ErrLeaseStateMismatch = errors.New("connmgr: lease state mismatch")

	// ErrDrainInProgress serializes rolling handoffs. Only one member per group
	// may be DRAINING, which caps a target-two handoff at three physical
	// connections.
	ErrDrainInProgress = errors.New("connmgr: another drain is in progress")

	// ErrCorruptState indicates that the shared record contains an unknown
	// state, an unindexed member, or more than one DRAINING member.
	ErrCorruptState = errors.New("connmgr: corrupt shared state")

	// ErrConfig is the sentinel for invalid constructor or call inputs. It is
	// intentionally distinct from transport errors.
	ErrConfig = errors.New("connmgr: invalid configuration")
)

// CoordinatorError carries machine-readable failure information. It never
// carries the lease token.
type CoordinatorError struct {
	Op       string
	Group    string
	Expected State
	Actual   State
	Kind     error
}

func (e *CoordinatorError) Error() string {
	if e == nil {
		return "connmgr coordinator: <nil>"
	}
	message := "connmgr coordinator " + e.Op
	if e.Group != "" {
		message += " for group " + e.Group
	}
	if e.Expected != "" || e.Actual != "" {
		message += fmt.Sprintf(": expected state %q, actual %q", e.Expected, e.Actual)
	}
	if e.Kind != nil {
		message += ": " + e.Kind.Error()
	}
	return message
}

func (e *CoordinatorError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Kind
}

// Lease is the token-fenced identity of one coordinated connection. Callers
// must use the value returned by MarkReady/BeginDrain after a state
// transition; Renew checks both Token and State.
type Lease struct {
	Group string
	Token string
	State State
}

// Snapshot is returned by every successful coordination operation. ServerNow
// and ExpiresAt are both derived from the coordinator's clock (Redis TIME), so
// their difference is safe to turn into a local monotonic deadline even when
// app-node clocks differ.
type Snapshot struct {
	TargetReady int
	Total       int
	Connecting  int
	Ready       int
	Draining    int
	State       State
	ServerNow   time.Time
	ExpiresAt   time.Time
}

// ReadyTargetMet reports whether a DRAINING caller can close without reducing
// the remaining READY population below the configured target.
func (s Snapshot) ReadyTargetMet() bool {
	return s.TargetReady > 0 && s.Ready >= s.TargetReady
}

// RemainingTTL converts coordinator timestamps into a duration. Callers add it
// to their local clock and fail closed when that local deadline elapses
// without a successful Renew.
func (s Snapshot) RemainingTTL() time.Duration {
	if s.ServerNow.IsZero() || s.ExpiresAt.IsZero() || !s.ExpiresAt.After(s.ServerNow) {
		return 0
	}
	return s.ExpiresAt.Sub(s.ServerNow)
}

// Coordinator is the ownership authority for keyed connections. Capacity
// exhaustion is a normal Claim result (acquired=false), not an error.
type Coordinator interface {
	Claim(ctx context.Context, group, token string) (Lease, Snapshot, bool, error)
	MarkReady(ctx context.Context, l Lease) (Lease, Snapshot, error)
	Renew(ctx context.Context, l Lease) (Snapshot, error)
	BeginDrain(ctx context.Context, l Lease) (Lease, Snapshot, error)
	Release(ctx context.Context, l Lease) error
	TargetReady() int
	LeaseTTL() time.Duration
}

// validateGroup enforces the Target.Key / coordinator group contract:
// non-empty, at most maxGroupLen bytes, and free of '{' and '}' so it cannot
// break the Redis Cluster hash tag that wraps it.
func validateGroup(group string) error {
	switch {
	case group == "":
		return fmt.Errorf("%w: key is required", ErrConfig)
	case len(group) > maxGroupLen:
		return fmt.Errorf("%w: key exceeds %d bytes", ErrConfig, maxGroupLen)
	case strings.ContainsAny(group, "{}"):
		return fmt.Errorf("%w: key must not contain '{' or '}'", ErrConfig)
	}
	return nil
}

func validateToken(token string) error {
	if token == "" || len(token) > maxTokenLen {
		return fmt.Errorf("%w: lease token length is invalid", ErrConfig)
	}
	return nil
}

func validateState(state State) error {
	switch state {
	case StateConnecting, StateReady, StateDraining:
		return nil
	default:
		return fmt.Errorf("%w: unknown lease state", ErrConfig)
	}
}
