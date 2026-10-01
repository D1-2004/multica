package dws

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// AuthCode is a single-use OAuth authorization code for a DWS client app,
// e.g. from Agent Identity (AgentIdentity.Redeem) or an OAuth redirect.
type AuthCode struct {
	Code     string
	ClientID string
	// Optional expectations, checked at the verify stage.
	ExpectCorpID string
	ExpectUserID string
}

func (a AuthCode) validate() error {
	if strings.TrimSpace(a.Code) == "" || strings.TrimSpace(a.ClientID) == "" {
		return errors.New("auth code and client id are required")
	}
	if strings.ContainsAny(a.Code+a.ClientID, " \t\r\n") {
		return errors.New("auth code or client id contains whitespace")
	}
	return nil
}

// Token is a credential the caller already holds (NewWithToken).
type Token struct {
	AccessToken string
	// RefreshToken enables refresh; it needs ClientID.
	RefreshToken string
	ClientID     string
	// ExpiresAt zero means unknown: the token is used until it is rejected.
	ExpiresAt time.Time
	// CorpID is the organization the token acts in, as the exchange
	// reported it; empty when unknown. Event subscriptions send it.
	CorpID string
	// MintedAt is when the auth code behind this token lineage was
	// exchanged; refreshes keep it. Zero means unknown.
	MintedAt time.Time
}

// String describes the Token without its secrets.
func (t Token) String() string {
	return fmt.Sprintf("dws.Token{clientId=%s, corpId=%s, refreshable=%v, expiresAt=%s}", t.ClientID, t.CorpID,
		t.RefreshToken != "", t.ExpiresAt.Format(time.RFC3339))
}

// GoString keeps %#v as safe as %v.
func (t Token) GoString() string { return t.String() }

// String describes the AuthCode without the single-use code.
func (a AuthCode) String() string { return "dws.AuthCode{clientId=" + a.ClientID + "}" }

// GoString keeps %#v as safe as %v.
func (a AuthCode) GoString() string { return a.String() }

const defaultTokenLifetime = 2 * time.Hour

type tokenResponse struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	// The DWS-hosted endpoint and the dws CLI use expiresIn; DingTalk
	// documents expireIn.
	ExpiresIn int64  `json:"expiresIn"`
	ExpireIn  int64  `json:"expireIn"`
	CorpID    string `json:"corpId"`
	// Error shapes: DWS-hosted {errorCode, errorMsg}; DingTalk {code, message}.
	ErrorCode string `json:"errorCode"`
	Code      string `json:"code"`
}

func (r tokenResponse) errorCode() string {
	if r.ErrorCode != "" {
		return clip(r.ErrorCode, 64)
	}
	return clip(r.Code, 64)
}

func (r tokenResponse) token(clientID string, now time.Time) Token {
	lifetime := time.Duration(max(r.ExpiresIn, r.ExpireIn)) * time.Second
	if lifetime <= 0 {
		lifetime = defaultTokenLifetime
	}
	return Token{AccessToken: r.AccessToken, RefreshToken: r.RefreshToken, ClientID: clientID, ExpiresAt: now.Add(lifetime), CorpID: r.CorpID, MintedAt: now}
}

// exchange trades the code for a token. The code is single-use: a failure
// is never retried here.
func exchange(ctx context.Context, cfg Config, code AuthCode) (Token, *InitError) {
	fail := func(kind error, spent, temporary bool, providerCode string, cause error) (Token, *InitError) {
		return Token{}, &InitError{Stage: StageExchange, Code: providerCode, CodeSpent: spent, Temporary: temporary, kind: kind, cause: cause}
	}
	if err := ctx.Err(); err != nil {
		return fail(ErrExchangeUnavailable, false, true, "", err)
	}
	url, body := cfg.authURL()+"/oauth2/getToken", map[string]string{
		"clientId": code.ClientID, "authCode": code.Code, "grantType": "authorization_code",
	}
	if cfg.ClientSecret != "" {
		url, body = cfg.tokenURL(), map[string]string{
			"clientId": code.ClientID, "clientSecret": cfg.ClientSecret, "code": code.Code, "grantType": "authorization_code",
		}
	}
	status, resp, err := postToken(ctx, cfg, url, body)
	switch {
	case err != nil && status == 0:
		// Only a connection that was never made proves the code is unused.
		return fail(ErrExchangeUnavailable, !neverSent(err), true, "", err)
	case status == http.StatusTooManyRequests || status >= 500:
		return fail(ErrExchangeUnavailable, true, true, resp.errorCode(), fmt.Errorf("HTTP %d", status))
	case resp.errorCode() != "" || (status >= 400 && status < 500):
		return fail(ErrAuthCodeRejected, true, false, resp.errorCode(), fmt.Errorf("HTTP %d", status))
	case err != nil || resp.AccessToken == "" || status < 200 || status > 299:
		// An unreadable or token-less answer says nothing about the code; it
		// may have been consumed, so a retry needs a new one.
		return fail(ErrExchangeUnavailable, true, true, "", fmt.Errorf("HTTP %d without a usable token", status))
	}
	return resp.token(code.ClientID, cfg.now()), nil
}

// errRefreshRejected: the refresh token itself was refused; the session is over.
var errRefreshRejected = errors.New("refresh token rejected")

// refresh renews tok with its refresh token. The refresh token rotates: the
// caller must replace tok with the result.
func refresh(ctx context.Context, cfg Config, tok Token) (Token, error) {
	url, body := cfg.authURL()+"/oauth2/refreshToken", map[string]string{
		"clientId": tok.ClientID, "refreshToken": tok.RefreshToken, "grantType": "refresh_token",
	}
	if cfg.ClientSecret != "" {
		url, body = cfg.tokenURL(), map[string]string{
			"clientId": tok.ClientID, "clientSecret": cfg.ClientSecret, "refreshToken": tok.RefreshToken, "grantType": "refresh_token",
		}
	}
	status, resp, err := postToken(ctx, cfg, url, body)
	switch {
	case err != nil && status == 0:
		return Token{}, fmt.Errorf("dws: refresh: %w", err)
	case status == http.StatusTooManyRequests || status >= 500:
		return Token{}, fmt.Errorf("dws: refresh: HTTP %d", status)
	case resp.errorCode() != "" || (status >= 400 && status < 500):
		return Token{}, fmt.Errorf("dws: refresh: HTTP %d %s: %w", status, resp.errorCode(), errRefreshRejected)
	case err != nil || resp.AccessToken == "" || status < 200 || status > 299:
		// Unreadable: transient, the session may still be fine.
		return Token{}, fmt.Errorf("dws: refresh: HTTP %d without a usable token", status)
	}
	next := resp.token(tok.ClientID, cfg.now())
	if next.RefreshToken == "" {
		next.RefreshToken = tok.RefreshToken
	}
	if next.CorpID == "" {
		next.CorpID = tok.CorpID
	}
	// A refresh continues the lineage the exchange started; a lineage of
	// unknown age starts counting now, so MaxLineage bounds it too.
	next.MintedAt = tok.MintedAt
	if next.MintedAt.IsZero() {
		next.MintedAt = cfg.now()
	}
	return next, nil
}

func postToken(ctx context.Context, cfg Config, url string, body map[string]string) (int, tokenResponse, error) {
	raw, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return 0, tokenResponse{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := cfg.httpClient().Do(req)
	if err != nil {
		return 0, tokenResponse{}, err
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return resp.StatusCode, tokenResponse{}, err
	}
	var out tokenResponse
	if err := json.Unmarshal(payload, &out); err != nil {
		return resp.StatusCode, tokenResponse{}, fmt.Errorf("unreadable token response: %w", err)
	}
	return resp.StatusCode, out, nil
}

// neverSent reports a failure before any byte reached the server: DNS
// resolution or connection setup.
func neverSent(err error) bool {
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return true
	}
	var opErr *net.OpError
	return errors.As(err, &opErr) && opErr.Op == "dial"
}
