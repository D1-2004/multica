package sandboxrelay

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/multica-ai/multica/server/internal/auth"
)

const (
	issuer   = "multica-prepub"
	audience = "multica-sandbox-relay"

	TargetMultica       = "multica"
	TargetAgentIdentity = "agent_identity"

	signingKeyIDEnv   = "MULTICA_SANDBOX_RELAY_SIGNING_KEY_ID"
	signingPrivateEnv = "MULTICA_SANDBOX_RELAY_SIGNING_PRIVATE_KEY"
	verifyKeysEnv     = "MULTICA_SANDBOX_RELAY_VERIFY_KEYS"
	defaultTokenTTL   = time.Hour
)

type Claims struct {
	TaskID                   string   `json:"task_id"`
	AgentID                  string   `json:"agent_id"`
	RuntimeID                string   `json:"runtime_id"`
	SandboxID                string   `json:"sandbox_id"`
	DaemonTokenSHA256        string   `json:"daemon_token_sha256"`
	AgentIdentityTokenSHA256 string   `json:"agent_identity_token_sha256,omitempty"`
	Targets                  []string `json:"targets"`
	jwt.RegisteredClaims
}

type MintRequest struct {
	TaskID             string
	AgentID            string
	RuntimeID          string
	SandboxID          string
	DaemonToken        string
	AgentIdentityToken string
	ExpiresAt          time.Time
}

type Signer struct {
	keyID      string
	privateKey ed25519.PrivateKey
	now        func() time.Time
}

func LoadSignerFromEnv() (*Signer, error) {
	keyID := strings.TrimSpace(os.Getenv(signingKeyIDEnv))
	encodedPrivateKey := strings.TrimSpace(os.Getenv(signingPrivateEnv))
	if keyID == "" && encodedPrivateKey == "" {
		return nil, nil
	}
	if keyID == "" || encodedPrivateKey == "" {
		return nil, fmt.Errorf("%s and %s must be configured together", signingKeyIDEnv, signingPrivateEnv)
	}
	privateKey, err := parsePrivateKey(encodedPrivateKey)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", signingPrivateEnv, err)
	}
	return NewSigner(keyID, privateKey)
}

func NewSigner(keyID string, privateKey ed25519.PrivateKey) (*Signer, error) {
	keyID = strings.TrimSpace(keyID)
	if keyID == "" {
		return nil, errors.New("sandbox relay signing key ID is empty")
	}
	if len(privateKey) != ed25519.PrivateKeySize {
		return nil, errors.New("sandbox relay signing key is not an Ed25519 private key")
	}
	keyCopy := append(ed25519.PrivateKey(nil), privateKey...)
	return &Signer{keyID: keyID, privateKey: keyCopy, now: time.Now}, nil
}

func (s *Signer) Mint(req MintRequest) (string, error) {
	if s == nil {
		return "", errors.New("sandbox relay signer is nil")
	}
	req.TaskID = strings.TrimSpace(req.TaskID)
	req.AgentID = strings.TrimSpace(req.AgentID)
	req.RuntimeID = strings.TrimSpace(req.RuntimeID)
	req.SandboxID = strings.TrimSpace(req.SandboxID)
	req.DaemonToken = strings.TrimSpace(req.DaemonToken)
	req.AgentIdentityToken = strings.TrimSpace(req.AgentIdentityToken)
	if req.TaskID == "" || req.AgentID == "" || req.RuntimeID == "" || req.SandboxID == "" || req.DaemonToken == "" {
		return "", errors.New("sandbox relay mint request is incomplete")
	}
	now := s.now().UTC()
	expiresAt := req.ExpiresAt.UTC()
	if expiresAt.IsZero() {
		expiresAt = now.Add(defaultTokenTTL)
	}
	if !expiresAt.After(now) {
		return "", errors.New("sandbox relay token expiry must be in the future")
	}
	tokenID, err := randomTokenID()
	if err != nil {
		return "", err
	}
	targets := []string{TargetMultica}
	identityHash := ""
	if req.AgentIdentityToken != "" {
		targets = append(targets, TargetAgentIdentity)
		identityHash = auth.HashToken(req.AgentIdentityToken)
	}
	claims := Claims{
		TaskID:                   req.TaskID,
		AgentID:                  req.AgentID,
		RuntimeID:                req.RuntimeID,
		SandboxID:                req.SandboxID,
		DaemonTokenSHA256:        auth.HashToken(req.DaemonToken),
		AgentIdentityTokenSHA256: identityHash,
		Targets:                  targets,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    issuer,
			Subject:   req.TaskID,
			Audience:  jwt.ClaimStrings{audience},
			ExpiresAt: jwt.NewNumericDate(expiresAt),
			NotBefore: jwt.NewNumericDate(now.Add(-5 * time.Second)),
			IssuedAt:  jwt.NewNumericDate(now),
			ID:        tokenID,
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims)
	token.Header["kid"] = s.keyID
	signed, err := token.SignedString(s.privateKey)
	if err != nil {
		return "", fmt.Errorf("sign sandbox relay token: %w", err)
	}
	return signed, nil
}

type Verifier struct {
	keys map[string]ed25519.PublicKey
	now  func() time.Time
}

func loadVerifierFromEnv() (*Verifier, error) {
	raw := strings.TrimSpace(os.Getenv(verifyKeysEnv))
	if raw == "" {
		return nil, nil
	}
	var encodedKeys map[string]string
	if err := json.Unmarshal([]byte(raw), &encodedKeys); err != nil {
		return nil, fmt.Errorf("%s must be a JSON object: %w", verifyKeysEnv, err)
	}
	if len(encodedKeys) == 0 {
		return nil, fmt.Errorf("%s must contain at least one key", verifyKeysEnv)
	}
	keys := make(map[string]ed25519.PublicKey, len(encodedKeys))
	for rawKeyID, encodedKey := range encodedKeys {
		keyID := strings.TrimSpace(rawKeyID)
		if keyID == "" || keyID != rawKeyID {
			return nil, fmt.Errorf("%s contains an invalid key ID", verifyKeysEnv)
		}
		publicKey, err := parsePublicKey(strings.TrimSpace(encodedKey))
		if err != nil {
			return nil, fmt.Errorf("%s[%q]: %w", verifyKeysEnv, keyID, err)
		}
		keys[keyID] = publicKey
	}
	return NewVerifier(keys)
}

func NewVerifier(keys map[string]ed25519.PublicKey) (*Verifier, error) {
	if len(keys) == 0 {
		return nil, errors.New("sandbox relay verification key set is empty")
	}
	copied := make(map[string]ed25519.PublicKey, len(keys))
	for rawKeyID, key := range keys {
		keyID := strings.TrimSpace(rawKeyID)
		if keyID == "" || keyID != rawKeyID {
			return nil, errors.New("sandbox relay verification key ID is invalid")
		}
		if len(key) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("sandbox relay verification key %q is not Ed25519", keyID)
		}
		copied[keyID] = append(ed25519.PublicKey(nil), key...)
	}
	return &Verifier{keys: copied, now: time.Now}, nil
}

func (v *Verifier) Verify(raw string) (*Claims, error) {
	if v == nil {
		return nil, errors.New("sandbox relay verifier is nil")
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("sandbox relay token is empty")
	}
	claims := &Claims{}
	token, err := jwt.ParseWithClaims(
		raw,
		claims,
		func(token *jwt.Token) (any, error) {
			if token.Method != jwt.SigningMethodEdDSA {
				return nil, errors.New("sandbox relay token signing method is invalid")
			}
			keyID, ok := token.Header["kid"].(string)
			if !ok || strings.TrimSpace(keyID) == "" || keyID != strings.TrimSpace(keyID) {
				return nil, errors.New("sandbox relay token key ID is invalid")
			}
			key, ok := v.keys[keyID]
			if !ok {
				return nil, errors.New("sandbox relay token key ID is unknown")
			}
			return key, nil
		},
		jwt.WithValidMethods([]string{jwt.SigningMethodEdDSA.Alg()}),
		jwt.WithIssuer(issuer),
		jwt.WithAudience(audience),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
		jwt.WithLeeway(30*time.Second),
		jwt.WithTimeFunc(v.now),
	)
	if err != nil {
		return nil, fmt.Errorf("verify sandbox relay token: %w", err)
	}
	if !token.Valid {
		return nil, errors.New("sandbox relay token is invalid")
	}
	if err := validateClaims(claims); err != nil {
		return nil, err
	}
	return claims, nil
}

func validateClaims(claims *Claims) error {
	if claims == nil {
		return errors.New("sandbox relay claims are missing")
	}
	if strings.TrimSpace(claims.TaskID) == "" ||
		strings.TrimSpace(claims.AgentID) == "" ||
		strings.TrimSpace(claims.RuntimeID) == "" ||
		strings.TrimSpace(claims.SandboxID) == "" ||
		claims.Subject != claims.TaskID ||
		!isSHA256Hex(claims.DaemonTokenSHA256) {
		return errors.New("sandbox relay claims are incomplete")
	}
	seen := make(map[string]struct{}, len(claims.Targets))
	for _, target := range claims.Targets {
		if _, exists := seen[target]; exists {
			return errors.New("sandbox relay claims contain duplicate targets")
		}
		seen[target] = struct{}{}
		switch target {
		case TargetMultica:
		case TargetAgentIdentity:
			if !isSHA256Hex(claims.AgentIdentityTokenSHA256) {
				return errors.New("sandbox relay Agent Identity claim is incomplete")
			}
		default:
			return errors.New("sandbox relay claims contain an unknown target")
		}
	}
	if _, ok := seen[TargetMultica]; !ok {
		return errors.New("sandbox relay Multica target is missing")
	}
	if claims.AgentIdentityTokenSHA256 != "" {
		if _, ok := seen[TargetAgentIdentity]; !ok {
			return errors.New("sandbox relay Agent Identity target is missing")
		}
	}
	return nil
}

func (c *Claims) Allows(target string) bool {
	if c == nil {
		return false
	}
	for _, allowed := range c.Targets {
		if allowed == target {
			return true
		}
	}
	return false
}

func parsePrivateKey(encoded string) (ed25519.PrivateKey, error) {
	der, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil {
		return nil, errors.New("expected standard base64-encoded PKCS#8 DER")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, errors.New("expected an Ed25519 PKCS#8 private key")
	}
	key, ok := parsed.(ed25519.PrivateKey)
	if !ok || len(key) != ed25519.PrivateKeySize {
		return nil, errors.New("expected an Ed25519 PKCS#8 private key")
	}
	return append(ed25519.PrivateKey(nil), key...), nil
}

func parsePublicKey(encoded string) (ed25519.PublicKey, error) {
	der, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil {
		return nil, errors.New("expected standard base64-encoded PKIX DER")
	}
	parsed, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return nil, errors.New("expected an Ed25519 PKIX public key")
	}
	key, ok := parsed.(ed25519.PublicKey)
	if !ok || len(key) != ed25519.PublicKeySize {
		return nil, errors.New("expected an Ed25519 PKIX public key")
	}
	return append(ed25519.PublicKey(nil), key...), nil
}

func randomTokenID() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate sandbox relay token ID: %w", err)
	}
	return hex.EncodeToString(raw), nil
}

func isSHA256Hex(value string) bool {
	if len(value) != sha256HexLength {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

const sha256HexLength = 64
