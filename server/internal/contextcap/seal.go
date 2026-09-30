package contextcap

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/google/uuid"
)

// Box seals and opens credential payloads. It matches the method set of
// *secretbox.Box (internal/util/secretbox). Pass a nil interface, not a typed
// nil *secretbox.Box, when no key is configured.
type Box interface {
	Seal(plaintext []byte) ([]byte, error)
	Open(sealed []byte) ([]byte, error)
}

var (
	// ErrInvalidBearer rejects an empty, oversized or header-unsafe Bearer.
	ErrInvalidBearer = errors.New("contextcap: invalid bearer credential")
	// ErrInvalidCredentialBinding rejects a malformed credential scope.
	ErrInvalidCredentialBinding = errors.New("contextcap: invalid credential binding")
	// ErrCredentialKeyUnavailable means no secret box is configured.
	ErrCredentialKeyUnavailable = errors.New("contextcap: credential key unavailable")
	// ErrCredentialUnavailable means a stored credential cannot be used: it
	// does not open, belongs to another scope, or holds an unusable Bearer.
	ErrCredentialUnavailable = errors.New("contextcap: credential unavailable")
)

// MaxBearerLength bounds a scene or personal Bearer credential in bytes.
const MaxBearerLength = 4096

// CredentialBinding identifies one scene or personal connector credential. It
// is both the row key of context_connector_credential and the scope sealed
// with the Bearer; secretbox has no associated data, so every field is
// re-checked after Open.
type CredentialBinding struct {
	WorkspaceID string
	AgentID     string
	ConnectorID string
	ScopeType   string
	OrgID       string
	ScopeKey    string
}

type sealedCredential struct {
	WorkspaceID string `json:"workspace_id"`
	AgentID     string `json:"agent_id"`
	ConnectorID string `json:"connector_id"`
	ScopeType   string `json:"scope_type"`
	OrgID       string `json:"org_id"`
	ScopeKey    string `json:"scope_key"`
	// Bearer is the token sent upstream. For an OAuth credential it mirrors
	// OAuth.AccessToken, so an older binary that only reads "bearer" can
	// still use an unexpired token.
	Bearer string      `json:"bearer"`
	OAuth  *OAuthToken `json:"oauth,omitempty"`
}

// ValidBearer accepts 1..MaxBearerLength bytes without CR, LF or NUL and
// without surrounding whitespace, matching the workspace credential rule.
func ValidBearer(bearer string) bool {
	return bearer != "" && len(bearer) <= MaxBearerLength &&
		strings.TrimSpace(bearer) == bearer && !strings.ContainsAny(bearer, "\r\n\x00")
}

// Hint returns the write-only display hint of a Bearer: "••••" followed by
// its last four characters, or just "••••" when it is shorter than eight
// characters.
func Hint(bearer string) string {
	const mask = "••••"
	runes := []rune(bearer)
	if len(runes) < 8 {
		return mask
	}
	return mask + string(runes[len(runes)-4:])
}

// SealCredential seals bearer together with its full scope binding.
func SealCredential(box Box, binding CredentialBinding, bearer string) ([]byte, error) {
	if box == nil {
		return nil, ErrCredentialKeyUnavailable
	}
	if !ValidBearer(bearer) {
		return nil, ErrInvalidBearer
	}
	normalized, err := binding.normalized()
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(sealedCredential{
		WorkspaceID: normalized.WorkspaceID,
		AgentID:     normalized.AgentID,
		ConnectorID: normalized.ConnectorID,
		ScopeType:   normalized.ScopeType,
		OrgID:       normalized.OrgID,
		ScopeKey:    normalized.ScopeKey,
		Bearer:      bearer,
	})
	if err != nil {
		return nil, err
	}
	return box.Seal(payload)
}

// OpenCredential opens ciphertext and returns its Bearer only when every
// sealed scope field equals binding and the Bearer is usable (non-empty, no
// CR, LF or NUL). Any mismatch returns ErrCredentialUnavailable. For an
// OAuth credential the Bearer is the current access token; use
// OpenCredentialSecret to also read the OAuth part.
func OpenCredential(box Box, binding CredentialBinding, ciphertext []byte) (string, error) {
	secret, err := OpenCredentialSecret(box, binding, ciphertext)
	if err != nil {
		return "", err
	}
	return secret.Bearer, nil
}

// OpenCredentialSecret is OpenCredential returning the whole secret,
// including the optional OAuth token. An OAuth part whose access token does
// not equal the Bearer, or that is malformed, makes the credential
// unavailable.
func OpenCredentialSecret(box Box, binding CredentialBinding, ciphertext []byte) (Secret, error) {
	if box == nil {
		return Secret{}, ErrCredentialKeyUnavailable
	}
	normalized, err := binding.normalized()
	if err != nil || len(ciphertext) == 0 {
		return Secret{}, ErrCredentialUnavailable
	}
	plain, err := box.Open(ciphertext)
	if err != nil {
		return Secret{}, ErrCredentialUnavailable
	}
	var sealed sealedCredential
	if err := json.Unmarshal(plain, &sealed); err != nil {
		return Secret{}, ErrCredentialUnavailable
	}
	if sealed.WorkspaceID != normalized.WorkspaceID || sealed.AgentID != normalized.AgentID ||
		sealed.ConnectorID != normalized.ConnectorID || sealed.ScopeType != normalized.ScopeType ||
		sealed.OrgID != normalized.OrgID || sealed.ScopeKey != normalized.ScopeKey {
		return Secret{}, ErrCredentialUnavailable
	}
	return secretFromSealed(sealed.Bearer, sealed.OAuth)
}

// normalized canonicalizes UUID fields so textual case differences cannot
// defeat the binding comparison, and validates the scope.
func (b CredentialBinding) normalized() (CredentialBinding, error) {
	out := b
	for _, field := range []*string{&out.WorkspaceID, &out.AgentID, &out.ConnectorID} {
		parsed, err := uuid.Parse(*field)
		if err != nil || parsed == uuid.Nil {
			return CredentialBinding{}, ErrInvalidCredentialBinding
		}
		*field = parsed.String()
	}
	if (out.ScopeType != ScopeScene && out.ScopeType != ScopePerson) || out.ScopeKey == "" {
		return CredentialBinding{}, ErrInvalidCredentialBinding
	}
	return out, nil
}
