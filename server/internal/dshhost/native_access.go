package dshhost

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
)

var ErrNativeAccessDenied = errors.New("DSH native access is unavailable or no longer authorized")

const (
	NativeEntryLifetime   = time.Minute
	NativeSessionLifetime = 15 * time.Minute
)

// NativeAccess binds a browser credential to one human and one exact Host.
// It is never a lease permitting another sandbox to write the employee Home.
type NativeAccess struct {
	ID uuid.UUID
	// ParentID marks a backend-only routed capability, not a browser reservation.
	ParentID uuid.UUID
	Key
	UserID     uuid.UUID
	Generation int64
	SandboxID  string
	Kind       string
	ExpiresAt  time.Time
}

// NativeAccessStore must enforce expiry using its authoritative clock and
// require the same running Host on insert, lookup and atomic exchange.
type NativeAccessStore interface {
	InsertNativeAccess(context.Context, NativeAccess, string) (NativeAccess, error)
	GetNativeAccess(context.Context, string, string) (NativeAccess, error)
	ExchangeNativeAccess(context.Context, NativeAccess, string, string) (NativeAccess, error)
	RevokeNativeAccess(context.Context, Key, uuid.UUID) error
}

type NativeAccessManager struct {
	Store NativeAccessStore
	// CheckManage must resolve current membership and employee management
	// permission every time, including on existing WebSocket revalidation.
	CheckManage func(context.Context, Key, uuid.UUID) error
}

func newNativeAccessToken(kind string) (string, string, error) {
	prefix := "dnge_"
	if kind == "session" {
		prefix = "dngs_"
	} else if kind != "entry" {
		return "", "", ErrNativeAccessDenied
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return "", "", err
	}
	token := prefix + base64.RawURLEncoding.EncodeToString(secret)
	hash, err := nativeAccessHash(token, kind)
	return token, hash, err
}

func nativeAccessHash(token, kind string) (string, error) {
	prefix := "dnge_"
	if kind == "session" {
		prefix = "dngs_"
	} else if kind != "entry" {
		return "", ErrNativeAccessDenied
	}
	if len(token) != len(prefix)+43 || !strings.HasPrefix(token, prefix) {
		return "", ErrNativeAccessDenied
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(token[len(prefix):])
	if err != nil || len(raw) != 32 {
		return "", ErrNativeAccessDenied
	}
	digest := sha256.Sum256([]byte(token))
	return hex.EncodeToString(digest[:]), nil
}

func (m NativeAccessManager) allowed(ctx context.Context, access NativeAccess) error {
	if m.Store == nil || m.CheckManage == nil || access.ID == uuid.Nil || access.WorkspaceID == uuid.Nil || access.AgentID == uuid.Nil || access.UserID == uuid.Nil || access.Generation < 1 || !sandboxIDPattern.MatchString(access.SandboxID) {
		return ErrNativeAccessDenied
	}
	if err := m.CheckManage(ctx, access.Key, access.UserID); err != nil {
		return ErrNativeAccessDenied
	}
	return nil
}

// Issue is called only after human authentication and native gateway readiness.
// The store independently requires the exact Host to still be running.
func (m NativeAccessManager) Issue(ctx context.Context, host Host, userID uuid.UUID) (NativeAccess, string, error) {
	return m.issue(ctx, host, userID, uuid.Nil)
}

// IssueRouted derives an exact-Host capability from a human browser grant.
// The store atomically validates the parent and bounds the child's lifetime.
func (m NativeAccessManager) IssueRouted(ctx context.Context, host Host, parent NativeAccess) (NativeAccess, string, error) {
	if parent.ID == uuid.Nil || parent.ParentID != uuid.Nil || parent.Kind != "session" || parent.Key != host.Key {
		return NativeAccess{}, "", ErrNativeAccessDenied
	}
	return m.issue(ctx, host, parent.UserID, parent.ID)
}

func (m NativeAccessManager) issue(ctx context.Context, host Host, userID, parentID uuid.UUID) (NativeAccess, string, error) {
	access := NativeAccess{ID: uuid.New(), ParentID: parentID, Key: host.Key, UserID: userID, Generation: host.Generation, SandboxID: host.SandboxID, Kind: "entry"}
	if host.State != "running" || m.allowed(ctx, access) != nil {
		return NativeAccess{}, "", ErrNativeAccessDenied
	}
	token, hash, err := newNativeAccessToken("entry")
	if err != nil {
		return NativeAccess{}, "", err
	}
	access, err = m.Store.InsertNativeAccess(ctx, access, hash)
	if err != nil {
		return NativeAccess{}, "", err
	}
	return access, token, nil
}

func sameNativeHost(access NativeAccess, host Host) bool {
	return access.Key == host.Key && access.Generation == host.Generation && access.SandboxID == host.SandboxID
}

// Exchange consumes the entry exactly once. An uncertain receipt is never
// retried as another exchange; the human must request a fresh entry.
func (m NativeAccessManager) Exchange(ctx context.Context, entry string, host Host) (NativeAccess, string, error) {
	hash, err := nativeAccessHash(entry, "entry")
	if err != nil || m.Store == nil {
		return NativeAccess{}, "", ErrNativeAccessDenied
	}
	access, err := m.Store.GetNativeAccess(ctx, hash, "entry")
	if err != nil || !sameNativeHost(access, host) || m.allowed(ctx, access) != nil {
		return NativeAccess{}, "", ErrNativeAccessDenied
	}
	token, sessionHash, err := newNativeAccessToken("session")
	if err != nil {
		return NativeAccess{}, "", err
	}
	access, err = m.Store.ExchangeNativeAccess(ctx, access, hash, sessionHash)
	if err != nil {
		return NativeAccess{}, "", err
	}
	return access, token, nil
}

// Authorize is required before each HTTP operation and periodically for each
// open WebSocket. Successful handshakes do not grant permanent authorization.
func (m NativeAccessManager) Authorize(ctx context.Context, token string, host Host) (NativeAccess, error) {
	hash, err := nativeAccessHash(token, "session")
	if err != nil || m.Store == nil {
		return NativeAccess{}, ErrNativeAccessDenied
	}
	access, err := m.Store.GetNativeAccess(ctx, hash, "session")
	if err != nil || !sameNativeHost(access, host) || m.allowed(ctx, access) != nil {
		return NativeAccess{}, ErrNativeAccessDenied
	}
	return access, nil
}

func (m NativeAccessManager) Revoke(ctx context.Context, key Key, grantID, userID uuid.UUID) error {
	if m.Store == nil || m.CheckManage == nil || key.WorkspaceID == uuid.Nil || key.AgentID == uuid.Nil || grantID == uuid.Nil || userID == uuid.Nil {
		return ErrNativeAccessDenied
	}
	if err := m.CheckManage(ctx, key, userID); err != nil {
		return ErrNativeAccessDenied
	}
	return m.Store.RevokeNativeAccess(ctx, key, grantID)
}
