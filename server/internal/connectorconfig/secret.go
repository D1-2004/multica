package connectorconfig

import (
	"encoding/json"
	"errors"

	"github.com/multica-ai/multica/server/internal/contextcap"
)

// Sealer is the process secret box. Ciphertext is not a secret identifier
// and must not be logged.
type Sealer interface {
	Seal(plaintext []byte) ([]byte, error)
	Open(ciphertext []byte) ([]byte, error)
}

// ErrSecretUnavailable means the ciphertext cannot be opened or the box is
// not configured.
var ErrSecretUnavailable = errors.New("connector secret unavailable")

type tokenPayload struct {
	Bearer string                 `json:"bearer"`
	OAuth  *contextcap.OAuthToken `json:"oauth,omitempty"`
}

// SealString seals a client secret. The caller stores only the ciphertext
// and a display hint.
func SealString(box Sealer, secret string) ([]byte, error) {
	if box == nil || !contextcap.ValidBearer(secret) {
		return nil, ErrSecretUnavailable
	}
	sealed, err := box.Seal([]byte(secret))
	if err != nil {
		return nil, ErrSecretUnavailable
	}
	return sealed, nil
}

// OpenString opens a client secret. The result must not be logged.
func OpenString(box Sealer, ciphertext []byte) (string, error) {
	if box == nil || len(ciphertext) == 0 {
		return "", ErrSecretUnavailable
	}
	plain, err := box.Open(ciphertext)
	if err != nil || !contextcap.ValidBearer(string(plain)) {
		return "", ErrSecretUnavailable
	}
	return string(plain), nil
}

// SealToken seals an access token the relay can send upstream.
func SealToken(box Sealer, token string) ([]byte, error) {
	if box == nil || !contextcap.ValidBearer(token) {
		return nil, ErrSecretUnavailable
	}
	payload, err := json.Marshal(tokenPayload{Bearer: token})
	if err != nil {
		return nil, ErrSecretUnavailable
	}
	sealed, err := box.Seal(payload)
	if err != nil {
		return nil, ErrSecretUnavailable
	}
	return sealed, nil
}

// OpenToken opens an instance token into a credential secret.
func OpenToken(box Sealer, ciphertext []byte) (contextcap.Secret, error) {
	if box == nil || len(ciphertext) == 0 {
		return contextcap.Secret{}, ErrSecretUnavailable
	}
	plain, err := box.Open(ciphertext)
	if err != nil {
		return contextcap.Secret{}, ErrSecretUnavailable
	}
	var payload tokenPayload
	if err := json.Unmarshal(plain, &payload); err != nil || !contextcap.ValidBearer(payload.Bearer) {
		return contextcap.Secret{}, ErrSecretUnavailable
	}
	secret := contextcap.Secret{Bearer: payload.Bearer}
	if payload.OAuth != nil {
		if payload.OAuth.AccessToken != payload.Bearer || !payload.OAuth.Valid() {
			return contextcap.Secret{}, ErrSecretUnavailable
		}
		secret.OAuth = payload.OAuth
	}
	return secret, nil
}

// Hint is the write-only display form of a secret.
func Hint(secret string) string {
	return contextcap.Hint(secret)
}
