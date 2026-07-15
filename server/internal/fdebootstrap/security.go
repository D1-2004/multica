package fdebootstrap

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/multica-ai/multica/server/internal/auth"
)

const (
	ProductKey         = "fde-agent"
	IntentTTL          = 15 * time.Minute
	AssertionTTL       = 5 * time.Minute
	TokenPrefix        = "fdeb_"
	identityKeyDomain  = "multica:fde-bootstrap:identity:v1"
	assertionKeyDomain = "multica:fde-bootstrap:assertion:v1"
)

var (
	ErrInvalidAssertion = errors.New("invalid FDE bootstrap identity assertion")
	ErrInvalidIdentity  = errors.New("invalid DingTalk identity")
)

type IdentityAssertion struct {
	UserID       string
	IdentityHMAC []byte
	ExpiresAt    time.Time
}

type assertionClaims struct {
	IdentityHMAC string `json:"identity_hmac"`
	jwt.RegisteredClaims
}

func GenerateToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate FDE bootstrap token: %w", err)
	}
	return TokenPrefix + base64.RawURLEncoding.EncodeToString(raw), nil
}

func HashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

func IdentityHMAC(unionID string) ([]byte, error) {
	unionID = strings.TrimSpace(unionID)
	if unionID == "" {
		return nil, ErrInvalidIdentity
	}
	mac := hmac.New(sha256.New, deriveKey(identityKeyDomain))
	mac.Write([]byte(unionID))
	return mac.Sum(nil), nil
}

func IdentityMatches(expected, actual []byte) bool {
	return len(expected) == sha256.Size && len(actual) == sha256.Size && hmac.Equal(expected, actual)
}

func SignIdentityAssertion(userID string, identityHMAC []byte, now time.Time) (string, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" || len(identityHMAC) != sha256.Size {
		return "", ErrInvalidAssertion
	}
	claims := assertionClaims{
		IdentityHMAC: base64.RawURLEncoding.EncodeToString(identityHMAC),
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(AssertionTTL)),
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(deriveKey(assertionKeyDomain))
}

func ParseIdentityAssertion(raw string, now time.Time) (IdentityAssertion, error) {
	claims := assertionClaims{}
	token, err := jwt.ParseWithClaims(raw, &claims, func(token *jwt.Token) (any, error) {
		if token.Method != jwt.SigningMethodHS256 {
			return nil, ErrInvalidAssertion
		}
		return deriveKey(assertionKeyDomain), nil
	}, jwt.WithTimeFunc(func() time.Time { return now }), jwt.WithExpirationRequired())
	if err != nil || !token.Valid || strings.TrimSpace(claims.Subject) == "" || claims.ExpiresAt == nil {
		return IdentityAssertion{}, ErrInvalidAssertion
	}
	identityHMAC, err := base64.RawURLEncoding.DecodeString(claims.IdentityHMAC)
	if err != nil || len(identityHMAC) != sha256.Size {
		return IdentityAssertion{}, ErrInvalidAssertion
	}
	return IdentityAssertion{UserID: claims.Subject, IdentityHMAC: identityHMAC, ExpiresAt: claims.ExpiresAt.Time}, nil
}

func deriveKey(domain string) []byte {
	mac := hmac.New(sha256.New, auth.JWTSecret())
	mac.Write([]byte(domain))
	return mac.Sum(nil)
}
