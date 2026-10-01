package contextcap

import (
	"encoding/json"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Credential kinds reported to clients. A credential is "oauth" when it was
// stored by an OAuth connect flow and "bearer" when a person pasted a token
// (including a GitHub Personal Access Token).
const (
	CredentialKindBearer = "bearer"
	CredentialKindOAuth  = "oauth"
)

// Bounds of the OAuth part of a sealed credential.
const (
	MaxRefreshTokenLength = 8192
	MaxAccountLength      = 128
	MaxOAuthScopeLength   = 2048
)

// OAuthToken is the OAuth part of a sealed connector credential (scene,
// person or workspace). The sealed payload keeps "bearer" equal to
// AccessToken so older binaries keep working with an unexpired token.
type OAuthToken struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token,omitempty"`
	// ExpiresAt is the access token expiry in Unix seconds; 0 = none.
	ExpiresAt int64  `json:"expires_at,omitempty"`
	TokenType string `json:"token_type,omitempty"`
	Scope     string `json:"scope,omitempty"`
	// Account is the provider account label (for example a GitHub login).
	Account string `json:"account,omitempty"`
	// ClientID is the OAuth client the tokens were issued to (a connector's
	// dynamic registration, or the GitHub App). A refresh must use that
	// client: providers reject a refresh token presented by another one.
	// Empty for tokens stored before the client was recorded.
	ClientID string `json:"client_id,omitempty"`
}

// MaxOAuthClientIDLength bounds OAuthToken.ClientID.
const MaxOAuthClientIDLength = 512

// Valid reports whether t can be sealed: a usable access token and bounded,
// header-safe optional fields.
func (t OAuthToken) Valid() bool {
	if !ValidBearer(t.AccessToken) {
		return false
	}
	if t.RefreshToken != "" && (len(t.RefreshToken) > MaxRefreshTokenLength || strings.TrimSpace(t.RefreshToken) != t.RefreshToken ||
		strings.ContainsAny(t.RefreshToken, "\r\n\x00")) {
		return false
	}
	if t.ExpiresAt < 0 || len(t.Scope) > MaxOAuthScopeLength || strings.ContainsAny(t.Scope, "\r\n\x00") ||
		len(t.TokenType) > 32 || strings.ContainsAny(t.TokenType, "\r\n\x00") ||
		len(t.ClientID) > MaxOAuthClientIDLength || strings.ContainsAny(t.ClientID, "\r\n\x00") {
		return false
	}
	return t.Account == SanitizeAccount(t.Account)
}

// ExpiresWithin reports whether the access token expires within d of now.
// A token without an expiry never does.
func (t OAuthToken) ExpiresWithin(now time.Time, d time.Duration) bool {
	return t.ExpiresAt > 0 && !now.Add(d).Before(time.Unix(t.ExpiresAt, 0))
}

// Refreshable reports whether t carries a refresh token.
func (t OAuthToken) Refreshable() bool { return t.RefreshToken != "" }

// Secret is an opened connector credential.
type Secret struct {
	Bearer string
	OAuth  *OAuthToken
}

// Kind returns CredentialKindOAuth or CredentialKindBearer.
func (s Secret) Kind() string {
	if s.OAuth != nil {
		return CredentialKindOAuth
	}
	return CredentialKindBearer
}

// Usable reports whether the secret can authenticate a request at now: a
// Bearer always can; an OAuth token can while unexpired or refreshable.
func (s Secret) Usable(now time.Time) bool {
	if s.OAuth == nil {
		return s.Bearer != ""
	}
	return s.OAuth.Refreshable() || !s.OAuth.ExpiresWithin(now, 0)
}

// SanitizeAccount returns account trimmed, or "" when it is too long or
// contains control or format characters. Only printable labels reach hints.
func SanitizeAccount(account string) string {
	account = strings.TrimSpace(account)
	if account == "" || len(account) > MaxAccountLength || !utf8.ValidString(account) {
		return ""
	}
	for _, r := range account {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
			return ""
		}
	}
	return account
}

// OAuthHint is the write-only display hint of an OAuth credential:
// "@<account>" when the account is known, else "OAuth". It never contains
// token material.
func OAuthHint(account string) string {
	if account = SanitizeAccount(account); account != "" {
		return "@" + account
	}
	return "OAuth"
}

// CredentialKindFromHint derives the credential kind from a stored hint.
// Hints are only ever produced by Hint (Bearer, "••••…") and OAuthHint
// ("@…" or "OAuth").
func CredentialKindFromHint(hint string) string {
	if hint == "OAuth" || strings.HasPrefix(hint, "@") {
		return CredentialKindOAuth
	}
	return CredentialKindBearer
}

// SealOAuthCredential seals an OAuth token together with its full scope
// binding. The payload's Bearer is the access token.
func SealOAuthCredential(box Box, binding CredentialBinding, token OAuthToken) ([]byte, error) {
	if box == nil {
		return nil, ErrCredentialKeyUnavailable
	}
	token.Account = SanitizeAccount(token.Account)
	if !token.Valid() {
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
		Bearer:      token.AccessToken,
		OAuth:       &token,
	})
	if err != nil {
		return nil, err
	}
	return box.Seal(payload)
}

// OpenSecretPayload validates the credential part of an opened payload
// (shared with the workspace-level sealed credential of internal
// connectors): a usable Bearer and, when present, a valid OAuth token whose
// access token equals the Bearer.
func OpenSecretPayload(bearer string, oauth *OAuthToken) (Secret, error) {
	return secretFromSealed(bearer, oauth)
}

func secretFromSealed(bearer string, oauth *OAuthToken) (Secret, error) {
	if bearer == "" || strings.ContainsAny(bearer, "\r\n\x00") {
		return Secret{}, ErrCredentialUnavailable
	}
	if oauth == nil {
		return Secret{Bearer: bearer}, nil
	}
	if oauth.AccessToken != bearer || !oauth.Valid() {
		return Secret{}, ErrCredentialUnavailable
	}
	token := *oauth
	return Secret{Bearer: bearer, OAuth: &token}, nil
}
