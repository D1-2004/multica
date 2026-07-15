package agentmessagerouter

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"time"
)

const (
	ChannelTypeDingTalkAccount = "dingtalk_account"
	dingTalkAccountSchema      = 1
	callbackTokenDomain        = "dingtalk-account-callback:v1:"
)

type DingTalkAccountConfig struct {
	SchemaVersion      int        `json:"schema_version"`
	DispatchEndpointID string     `json:"dispatch_endpoint_id"`
	DispatchKeyID      string     `json:"dispatch_key_id"`
	DispatchURL        string     `json:"dispatch_url"`
	CallbackTokenHash  string     `json:"callback_token_hash,omitempty"`
	CallbackExpiresAt time.Time   `json:"callback_expires_at,omitempty"`
	RouterSourceID     string     `json:"router_source_id,omitempty"`
	AccountDisplayName string     `json:"account_display_name,omitempty"`
	AccountAvatarURL   string     `json:"account_avatar_url,omitempty"`
	BoundAt            *time.Time `json:"bound_at,omitempty"`
}

type PublicDingTalkAccountBinding struct {
	ID           string                       `json:"id"`
	WorkspaceID  string                       `json:"workspace_id"`
	AgentID      string                       `json:"agent_id"`
	DWSIdentity  PublicDingTalkBindingOutcome `json:"dws_identity"`
	MessageRoute PublicDingTalkBindingOutcome `json:"message_route"`
}

type PublicDingTalkBindingOutcome struct {
	Status             string     `json:"status"`
	AccountDisplayName string     `json:"account_display_name,omitempty"`
	AccountAvatarURL   string     `json:"account_avatar_url,omitempty"`
	BoundAt            *time.Time `json:"bound_at,omitempty"`
}

func NewPendingDingTalkAccountConfig(endpointID, dispatchURL, callbackHash string, callbackExpiresAt time.Time) DingTalkAccountConfig {
	keyID, _, _ := stringsCutEndpointID(endpointID)
	return DingTalkAccountConfig{
		SchemaVersion:      dingTalkAccountSchema,
		DispatchEndpointID: endpointID,
		DispatchKeyID:      keyID,
		DispatchURL:        dispatchURL,
		CallbackTokenHash:  callbackHash,
		CallbackExpiresAt: callbackExpiresAt.UTC(),
	}
}

func (c DingTalkAccountConfig) Marshal() ([]byte, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(c)
}

func ParseDingTalkAccountConfig(raw []byte) (DingTalkAccountConfig, error) {
	var config DingTalkAccountConfig
	if err := json.Unmarshal(raw, &config); err != nil {
		return DingTalkAccountConfig{}, fmt.Errorf("decode dingtalk account config: %w", err)
	}
	if err := config.Validate(); err != nil {
		return DingTalkAccountConfig{}, err
	}
	return config, nil
}

func (c DingTalkAccountConfig) Validate() error {
	if c.SchemaVersion != dingTalkAccountSchema {
		return errors.New("unsupported dingtalk account config schema")
	}
	keyID, err := parseEndpointID(c.DispatchEndpointID)
	if err != nil || keyID != c.DispatchKeyID {
		return errors.New("dingtalk account dispatch endpoint is invalid")
	}
	parsedDispatchURL, err := url.Parse(c.DispatchURL)
	if err != nil || parsedDispatchURL.Scheme == "" || parsedDispatchURL.Host == "" {
		return errors.New("dingtalk account dispatch url is invalid")
	}
	origin := (&url.URL{Scheme: parsedDispatchURL.Scheme, Host: parsedDispatchURL.Host}).String()
	expectedDispatchURL, err := BuildDispatchURL(origin, c.DispatchEndpointID)
	if err != nil || expectedDispatchURL != c.DispatchURL {
		return errors.New("dingtalk account dispatch url is invalid")
	}
	if (c.CallbackTokenHash == "") != c.CallbackExpiresAt.IsZero() {
		return errors.New("dingtalk account callback credential is invalid")
	}
	if c.CallbackTokenHash != "" {
		decoded, err := hex.DecodeString(c.CallbackTokenHash)
		if err != nil || len(decoded) != sha256.Size {
			return errors.New("dingtalk account callback credential is invalid")
		}
	}
	if c.RouterSourceID != "" && c.BoundAt == nil {
		return errors.New("dingtalk account bound time is required")
	}
	return nil
}

func (c DingTalkAccountConfig) PublicBinding(
	id,
	workspaceID,
	agentID,
	messageRouteStatus string,
	dwsIdentity PublicDingTalkBindingOutcome,
) PublicDingTalkAccountBinding {
	return PublicDingTalkAccountBinding{
		ID:          id,
		WorkspaceID: workspaceID,
		AgentID:     agentID,
		DWSIdentity: dwsIdentity,
		MessageRoute: PublicDingTalkBindingOutcome{
			Status:             messageRouteStatus,
			AccountDisplayName: c.AccountDisplayName,
			AccountAvatarURL:   c.AccountAvatarURL,
			BoundAt:            c.BoundAt,
		},
	}
}

func GenerateCallbackToken(random io.Reader) (string, string, error) {
	if random == nil {
		return "", "", errors.New("callback token random source is required")
	}
	value := make([]byte, 32)
	if _, err := io.ReadFull(random, value); err != nil {
		return "", "", fmt.Errorf("generate callback token: %w", err)
	}
	raw := base64.RawURLEncoding.EncodeToString(value)
	return raw, HashCallbackToken(raw), nil
}

func HashCallbackToken(raw string) string {
	if !isCanonicalCallbackToken(raw) {
		return ""
	}
	sum := sha256.Sum256([]byte(callbackTokenDomain + raw))
	return hex.EncodeToString(sum[:])
}

func VerifyCallbackToken(raw, expectedHash string) bool {
	actual := HashCallbackToken(raw)
	if actual == "" || len(actual) != len(expectedHash) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(actual), []byte(expectedHash)) == 1
}

func isCanonicalCallbackToken(raw string) bool {
	if len(raw) != 43 {
		return false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	return err == nil && len(decoded) == 32 && base64.RawURLEncoding.EncodeToString(decoded) == raw
}

func stringsCutEndpointID(endpointID string) (string, string, bool) {
	for i := 0; i < len(endpointID); i++ {
		if endpointID[i] == '_' {
			return endpointID[:i], endpointID[i+1:], true
		}
	}
	return "", "", false
}
