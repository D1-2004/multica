package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// SceneTokenPrefix starts a scene token: the config-qwen-tag-scene credential
// the claim issues to one task for its one Agent work scene.
const SceneTokenPrefix = "sct_"

// SceneTokenTTL bounds a scene token; the task token it travels with lives as
// long, and the endpoint also refuses a token once its task has ended.
const SceneTokenTTL = 24 * time.Hour

const sceneTokenVersion = 1

// SceneTokenClaims is what a scene token binds: one task of one agent in one
// workspace, to one scene, for a bounded time.
type SceneTokenClaims struct {
	Version     int    `json:"v"`
	WorkspaceID string `json:"ws"`
	AgentID     string `json:"agent"`
	TaskID      string `json:"task"`
	SceneID     string `json:"scene"`
	IssuedAt    int64  `json:"iat"`
	ExpiresAt   int64  `json:"exp"`
	Nonce       string `json:"nonce"`
}

// ErrInvalidSceneToken: the token is malformed, forged, of another version
// or expired.
var ErrInvalidSceneToken = errors.New("scene token is invalid or expired")

// sceneTokenKey derives the scene-token signing key from JWT_SECRET, which
// every replica shares, so a token issued by one replica verifies on all.
func sceneTokenKey() []byte {
	mac := hmac.New(sha256.New, JWTSecret())
	mac.Write([]byte("multica/scene-config-token/v1"))
	return mac.Sum(nil)
}

func signSceneToken(body string) string {
	mac := hmac.New(sha256.New, sceneTokenKey())
	mac.Write([]byte(SceneTokenPrefix + body))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// IssueSceneToken signs claims for now and SceneTokenTTL: "sct_" +
// base64url(payload) + "." + base64url(HMAC). It is URL-path safe. The token
// alone grants nothing: the endpoint also requires the same task's task token
// and re-resolves the task's scene on every call.
func IssueSceneToken(claims SceneTokenClaims, now time.Time) (string, error) {
	nonce := make([]byte, 12)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	claims.Version = sceneTokenVersion
	claims.IssuedAt = now.Unix()
	claims.ExpiresAt = now.Add(SceneTokenTTL).Unix()
	claims.Nonce = hex.EncodeToString(nonce)
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	body := base64.RawURLEncoding.EncodeToString(payload)
	return SceneTokenPrefix + body + "." + signSceneToken(body), nil
}

// VerifySceneToken checks a scene token's signature, version and expiry at
// now and returns its claims.
func VerifySceneToken(token string, now time.Time) (SceneTokenClaims, error) {
	rest, ok := strings.CutPrefix(token, SceneTokenPrefix)
	if !ok {
		return SceneTokenClaims{}, ErrInvalidSceneToken
	}
	body, signature, ok := strings.Cut(rest, ".")
	if !ok || body == "" || signature == "" {
		return SceneTokenClaims{}, ErrInvalidSceneToken
	}
	if !hmac.Equal([]byte(signature), []byte(signSceneToken(body))) {
		return SceneTokenClaims{}, ErrInvalidSceneToken
	}
	payload, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return SceneTokenClaims{}, ErrInvalidSceneToken
	}
	var claims SceneTokenClaims
	if err := json.Unmarshal(payload, &claims); err != nil || claims.Version != sceneTokenVersion {
		return SceneTokenClaims{}, ErrInvalidSceneToken
	}
	if claims.TaskID == "" || claims.SceneID == "" || claims.AgentID == "" || claims.WorkspaceID == "" ||
		now.Unix() >= claims.ExpiresAt || claims.IssuedAt > now.Add(time.Minute).Unix() {
		return SceneTokenClaims{}, ErrInvalidSceneToken
	}
	return claims, nil
}
