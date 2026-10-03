package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

const (
	// SceneSessionCookie is the HttpOnly cookie a GitHub connect sets so the
	// scene configuration page can open without a DingTalk login.
	SceneSessionCookie = "multica_scene_cfg"
	// SceneSessionAuthMethod is the X-Auth-Method a verified scene session
	// carries. It is not a DingTalk login.
	SceneSessionAuthMethod = "scene_link"
	// SceneSessionTTL is how long the page stays open after a connect. The
	// OAuth state itself expires sooner and is single use.
	SceneSessionTTL = 30 * time.Minute
	// SceneSessionCookiePath is the only API tree the cookie is sent to.
	SceneSessionCookiePath = "/api/context-capabilities"
)

// SceneSession names one digital employee, tenant and scene (or person) the
// bearer may open. It is HMAC-signed, expires, and is not a user session.
type SceneSession struct {
	WorkspaceID string `json:"w"`
	AgentID     string `json:"a"`
	OrgID       string `json:"o"`
	ScopeType   string `json:"t"`
	ScopeKey    string `json:"k"`
	UserID      string `json:"u"`
	ExpiresAt   int64  `json:"e"`
}

type sceneSessionContextKey struct{}

// WithSceneSession returns ctx carrying sess.
func WithSceneSession(ctx context.Context, sess SceneSession) context.Context {
	return context.WithValue(ctx, sceneSessionContextKey{}, sess)
}

// SceneSessionFromContext returns the session Auth attached, if any.
func SceneSessionFromContext(ctx context.Context) (SceneSession, bool) {
	sess, ok := ctx.Value(sceneSessionContextKey{}).(SceneSession)
	if !ok || sess.AgentID == "" {
		return SceneSession{}, false
	}
	return sess, true
}

func sceneSessionKey() []byte {
	return append([]byte("scene-config-session:"), JWTSecret()...)
}

// SignSceneSession returns the token the configure page exchanges for a cookie.
func SignSceneSession(sess SceneSession, now time.Time) (string, error) {
	if err := sess.validate(now); err != nil {
		return "", err
	}
	raw, err := json.Marshal(sess)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, sceneSessionKey())
	mac.Write(raw)
	return base64.RawURLEncoding.EncodeToString(raw) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

// OpenSceneSession checks the signature and the expiry. A bad token is refused
// without saying which check failed.
func OpenSceneSession(token string, now time.Time) (SceneSession, error) {
	encoded, sig, ok := strings.Cut(token, ".")
	if !ok || encoded == "" || sig == "" {
		return SceneSession{}, errors.New("invalid scene session")
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return SceneSession{}, errors.New("invalid scene session")
	}
	want, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil {
		return SceneSession{}, errors.New("invalid scene session")
	}
	mac := hmac.New(sha256.New, sceneSessionKey())
	mac.Write(raw)
	if !hmac.Equal(want, mac.Sum(nil)) {
		return SceneSession{}, errors.New("invalid scene session")
	}
	var sess SceneSession
	if err := json.Unmarshal(raw, &sess); err != nil {
		return SceneSession{}, errors.New("invalid scene session")
	}
	if err := sess.validate(now); err != nil {
		return SceneSession{}, err
	}
	return sess, nil
}

func (s SceneSession) validate(now time.Time) error {
	if s.WorkspaceID == "" || s.AgentID == "" || s.UserID == "" || s.OrgID == "" || s.ScopeKey == "" {
		return errors.New("invalid scene session")
	}
	if s.ScopeType != "scene" && s.ScopeType != "person" {
		return errors.New("invalid scene session")
	}
	if s.ExpiresAt <= now.Unix() {
		return errors.New("invalid scene session")
	}
	// A token minted for longer than the page TTL is not one of ours.
	if s.ExpiresAt > now.Add(SceneSessionTTL+time.Minute).Unix() {
		return errors.New("invalid scene session")
	}
	return nil
}

// SceneSessionFromRequest reads and verifies the cookie.
func SceneSessionFromRequest(r *http.Request, now time.Time) (SceneSession, bool) {
	cookie, err := r.Cookie(SceneSessionCookie)
	if err != nil || cookie.Value == "" {
		return SceneSession{}, false
	}
	sess, err := OpenSceneSession(cookie.Value, now)
	if err != nil {
		return SceneSession{}, false
	}
	return sess, true
}

// NewSceneSessionCookie is the cookie the configure page sends on later calls.
// An empty token clears it.
func NewSceneSessionCookie(token, origin string, now time.Time) *http.Cookie {
	cookie := &http.Cookie{
		Name: SceneSessionCookie, Value: token, Path: SceneSessionCookiePath, HttpOnly: true,
		Secure: strings.HasPrefix(origin, "https://"), SameSite: http.SameSiteLaxMode,
		MaxAge: int(SceneSessionTTL.Seconds()),
	}
	if token == "" {
		cookie.MaxAge = -1
		cookie.Expires = now.Add(-time.Hour)
	}
	return cookie
}
