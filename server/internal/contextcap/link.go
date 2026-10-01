package contextcap

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
)

const linkTokenBytes = 32

// linkTokenLength is the base64url (no padding) length of a 32-byte token.
var linkTokenLength = base64.RawURLEncoding.EncodedLen(linkTokenBytes)

// NewLinkToken returns a fresh configuration link token: 32 random bytes,
// base64url encoded without padding. Only HashLinkToken(token) is stored.
func NewLinkToken() (string, error) {
	raw := make([]byte, linkTokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// HashLinkToken returns the hex SHA-256 of token, the primary key of
// context_config_link.
func HashLinkToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// ValidLinkTokenFormat reports whether token has the shape NewLinkToken
// produces. Callers may reject malformed input before any database lookup.
func ValidLinkTokenFormat(token string) bool {
	if len(token) != linkTokenLength {
		return false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	return err == nil && len(decoded) == linkTokenBytes
}
