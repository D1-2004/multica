package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	agentIdentityRedeemPath       = "/api/agent-identity/v1/credentials/redeem"
	agentIdentityResponseMaxBytes = 64 << 10
)

type AgentIdentityCredential struct {
	UID      string
	AuthCode string
}

type AgentIdentityRedeemer interface {
	RedeemDWSAuthCode(ctx context.Context, contextToken string) (AgentIdentityCredential, error)
}

type AgentIdentityRedeemError struct {
	StatusCode int
	Code       string
}

func (e *AgentIdentityRedeemError) Error() string {
	if e.Code == "" {
		return fmt.Sprintf("agent identity credential redeem failed (status=%d)", e.StatusCode)
	}
	return fmt.Sprintf("agent identity credential redeem failed (status=%d, code=%s)", e.StatusCode, e.Code)
}

type HTTPAgentIdentityRedeemer struct {
	baseURL string
	client  *http.Client
}

func NewHTTPAgentIdentityRedeemer(baseURL string, timeout time.Duration) *HTTPAgentIdentityRedeemer {
	return &HTTPAgentIdentityRedeemer{
		baseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		client:  &http.Client{Timeout: timeout},
	}
}

func (r *HTTPAgentIdentityRedeemer) RedeemDWSAuthCode(ctx context.Context, contextToken string) (AgentIdentityCredential, error) {
	payload, err := json.Marshal(map[string]string{
		"identityKey":    "dws",
		"credentialType": "DWS_AUTH_CODE",
	})
	if err != nil {
		return AgentIdentityCredential{}, errorsWithoutSecret("encode agent identity request", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.baseURL+agentIdentityRedeemPath, bytes.NewReader(payload))
	if err != nil {
		return AgentIdentityCredential{}, errorsWithoutSecret("create agent identity request", err)
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(contextToken))
	req.Header.Set("Content-Type", "application/json")

	resp, err := r.client.Do(req)
	if err != nil {
		return AgentIdentityCredential{}, errorsWithoutSecret("agent identity request failed", err)
	}
	defer resp.Body.Close()

	var body struct {
		OK        bool   `json:"ok"`
		ErrorCode string `json:"errorCode"`
		Identity  struct {
			Key string `json:"key"`
			UID string `json:"uid"`
		} `json:"identity"`
		Credential struct {
			Type     string `json:"type"`
			AuthCode string `json:"authCode"`
		} `json:"credential"`
	}
	limited := io.LimitReader(resp.Body, agentIdentityResponseMaxBytes)
	if err := json.NewDecoder(limited).Decode(&body); err != nil {
		return AgentIdentityCredential{}, errorsWithoutSecret("decode agent identity response", err)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices || !body.OK {
		return AgentIdentityCredential{}, &AgentIdentityRedeemError{
			StatusCode: resp.StatusCode,
			Code:       strings.TrimSpace(body.ErrorCode),
		}
	}
	if body.Identity.Key != "dws" {
		return AgentIdentityCredential{}, fmt.Errorf("agent identity response has unexpected identity key")
	}
	if strings.TrimSpace(body.Identity.UID) == "" {
		return AgentIdentityCredential{}, fmt.Errorf("agent identity response is missing identity uid")
	}
	if body.Credential.Type != "DWS_AUTH_CODE" {
		return AgentIdentityCredential{}, fmt.Errorf("agent identity response has unexpected credential type")
	}
	if strings.TrimSpace(body.Credential.AuthCode) == "" {
		return AgentIdentityCredential{}, fmt.Errorf("agent identity response is missing auth code")
	}
	return AgentIdentityCredential{
		UID:      strings.TrimSpace(body.Identity.UID),
		AuthCode: strings.TrimSpace(body.Credential.AuthCode),
	}, nil
}

func errorsWithoutSecret(prefix string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", prefix, err)
}
