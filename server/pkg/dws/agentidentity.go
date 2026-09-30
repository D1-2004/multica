package dws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// AgentIdentity redeems an Agent Identity ContextToken for the DWS auth code
// of that agent: POST {BaseURL}/api/agent-identity/v1/credentials/redeem.
// The ContextToken itself comes from HSF createAgentIdentityContext.
type AgentIdentity struct {
	// BaseURL is e.g. https://pre-agent-identity.dingtalk.com.
	BaseURL string
	HTTP    *http.Client
	// Header is added to the redeem call, e.g. a sandbox relay token.
	Header http.Header
}

// Redeem returns the agent's single-use DWS auth code, ready for New.
func (a AgentIdentity) Redeem(ctx context.Context, contextToken string) (AuthCode, error) {
	base := strings.TrimRight(strings.TrimSpace(a.BaseURL), "/")
	if base == "" || strings.TrimSpace(contextToken) == "" {
		return AuthCode{}, errors.New("agent identity: redeem is not configured")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/agent-identity/v1/credentials/redeem",
		strings.NewReader(`{"identityKey":"dws","credentialType":"DWS_AUTH_CODE"}`))
	if err != nil {
		return AuthCode{}, fmt.Errorf("agent identity: build redeem request: %w", err)
	}
	for k, vs := range a.Header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(contextToken))
	req.Header.Set("Content-Type", "application/json")
	resp, err := Config{HTTP: a.HTTP}.httpClient().Do(req)
	if err != nil {
		return AuthCode{}, fmt.Errorf("agent identity: redeem: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var payload struct {
		OK        bool   `json:"ok"`
		ErrorCode string `json:"errorCode"`
		Identity  struct {
			Key      string `json:"key"`
			Type     string `json:"type"`
			UID      string `json:"uid"`
			ClientID string `json:"clientId"`
		} `json:"identity"`
		Credential struct {
			Type     string `json:"type"`
			AuthCode string `json:"authCode"`
		} `json:"credential"`
	}
	if json.Unmarshal(raw, &payload) != nil {
		return AuthCode{}, fmt.Errorf("agent identity: redeem: HTTP %d with unreadable body", resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 || !payload.OK {
		return AuthCode{}, fmt.Errorf("agent identity: redeem rejected: HTTP %d %s", resp.StatusCode, clip(payload.ErrorCode, 64))
	}
	code := AuthCode{Code: strings.TrimSpace(payload.Credential.AuthCode), ClientID: strings.TrimSpace(payload.Identity.ClientID)}
	if payload.Identity.Key != "dws" || payload.Identity.Type != "DWS_UID" || payload.Credential.Type != "DWS_AUTH_CODE" ||
		code.Code == "" || code.ClientID == "" {
		return AuthCode{}, errors.New("agent identity: unexpected DWS credential")
	}
	// The uid is in the Agent Identity id space, not the contact userId one;
	// it is validated but not used as an identity expectation.
	if _, err := strconv.ParseUint(strings.TrimSpace(payload.Identity.UID), 10, 64); err != nil {
		return AuthCode{}, errors.New("agent identity: invalid DWS uid")
	}
	return code, nil
}
