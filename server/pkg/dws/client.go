package dws

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
)

const maxResponseBytes = 4 << 20

// Client is one authenticated DingTalk identity. It is safe for concurrent
// use and keeps its token alive with the refresh token.
type Client struct {
	Messages *MessageService
	Groups   *GroupService
	Cards    *CardService
	Contacts *ContactService
	Events   *EventService

	cfg      Config
	identity Profile

	mu    ctxLock
	token Token
	// current mirrors token for Token, which must not wait behind a refresh.
	current atomic.Pointer[Token]
	// expired is atomic so Alive never waits behind a refresh holding mu.
	expired atomic.Bool
	// shared is set when a Pool with a TokenStore made the Client: its
	// token is renewed through the store, under the store's lock.
	shared *sharedToken
}

// New creates a Client from a single-use auth code in three stages:
// validate the input, exchange the code, and confirm the identity with one
// tool call (unless Config.SkipVerify). Any failure is an *InitError.
func New(ctx context.Context, cfg Config, code AuthCode) (*Client, error) {
	if err := cfg.validate(); err != nil {
		return nil, &InitError{Stage: StageConfig, kind: ErrInvalidConfig, cause: err}
	}
	if err := code.validate(); err != nil {
		return nil, &InitError{Stage: StageConfig, kind: ErrInvalidConfig, cause: err}
	}
	token, ierr := exchange(ctx, cfg, code)
	if ierr != nil {
		return nil, ierr
	}
	c := newClient(cfg, token)
	if !cfg.SkipVerify {
		if err := c.verify(ctx, code.ExpectCorpID, code.ExpectUserID); err != nil {
			return nil, err
		}
	}
	return c, nil
}

// NewWithToken creates a Client from a token the caller already holds, e.g.
// a TAG/DEAP task token. Without a RefreshToken the session ends when the
// token expires. Failures are *InitError with CodeSpent false.
func NewWithToken(ctx context.Context, cfg Config, token Token) (*Client, error) {
	if err := cfg.validate(); err != nil {
		return nil, &InitError{Stage: StageConfig, kind: ErrInvalidConfig, cause: err}
	}
	if strings.TrimSpace(token.AccessToken) == "" || strings.ContainsAny(token.AccessToken, " \t\r\n") {
		return nil, &InitError{Stage: StageConfig, kind: ErrInvalidConfig, cause: errors.New("access token is empty or malformed")}
	}
	if token.RefreshToken != "" && token.ClientID == "" {
		return nil, &InitError{Stage: StageConfig, kind: ErrInvalidConfig, cause: errors.New("a refresh token needs its client id")}
	}
	c := newClient(cfg, token)
	if !cfg.SkipVerify {
		if err := c.verify(ctx, "", ""); err != nil {
			err.(*InitError).CodeSpent = false
			return nil, err
		}
	}
	return c, nil
}

func newClient(cfg Config, token Token) *Client {
	c := &Client{cfg: cfg, token: token, mu: newCtxLock()}
	c.current.Store(&token)
	c.Messages = &MessageService{c: c}
	c.Groups = &GroupService{c: c}
	c.Cards = &CardService{c: c}
	c.Contacts = &ContactService{c: c}
	c.Events = &EventService{c: c}
	return c
}

func (c *Client) verify(ctx context.Context, corpID, userID string) error {
	fail := func(kind error, temporary bool, cause error) error {
		return &InitError{Stage: StageVerify, CodeSpent: true, Temporary: temporary, kind: kind, cause: cause}
	}
	me, err := c.Contacts.Me(ctx)
	if err != nil {
		// A rejected token will not start working; anything else may pass later.
		return fail(ErrIdentityUnverified, !IsAuth(err) && !errors.Is(err, ErrSessionExpired), err)
	}
	if (corpID != "" && me.CorpID != corpID) || (userID != "" && me.UserID != userID) {
		return fail(ErrIdentityMismatch, false, fmt.Errorf("token belongs to %s:%s", me.CorpID, me.UserID))
	}
	c.identity = me
	return nil
}

// Identity is who the Client acts as, confirmed by New (empty with SkipVerify).
func (c *Client) Identity() Profile { return c.identity }

// String names the Client without its token or the Config's secret, so
// logging a Client never leaks them.
func (c *Client) String() string {
	if c == nil {
		return "dws.Client(nil)"
	}
	who := c.identity.CorpID + ":" + c.identity.UserID
	if who == ":" {
		who = "unverified"
	}
	return fmt.Sprintf("dws.Client{%s, alive=%v}", who, c.Alive())
}

// GoString keeps %#v as safe as %v.
func (c *Client) GoString() string { return c.String() }

// Token returns the credential the Client currently holds. The refresh
// token rotates, so a caller that persists a session saves this after each
// use and restores it with NewWithToken; the saved value holds secrets.
func (c *Client) Token() Token { return *c.current.Load() }

// Alive reports whether the session can still be used; false after
// ErrSessionExpired.
func (c *Client) Alive() bool { return !c.expired.Load() }

// accessToken returns a usable token, refreshing it near expiry.
func (c *Client) accessToken(ctx context.Context) (string, error) {
	return c.tokenFor(ctx, "")
}

// afterRejection returns a token to retry with after the gateway rejected
// rejected: the current token when another call already replaced it, else a
// refreshed one. Without a refresh token the session is over.
func (c *Client) afterRejection(ctx context.Context, rejected string) (string, error) {
	return c.tokenFor(ctx, rejected)
}

func (c *Client) tokenFor(ctx context.Context, rejected string) (string, error) {
	if err := c.mu.lock(ctx); err != nil {
		return "", fmt.Errorf("dws: waiting for a token refresh: %w", err)
	}
	defer c.mu.unlock()
	if c.expired.Load() {
		return "", ErrSessionExpired
	}
	if rejected != "" && c.token.AccessToken != rejected {
		// Another call refreshed while this one was in flight.
		return c.token.AccessToken, nil
	}
	now := c.cfg.now()
	if rejected == "" && (c.token.ExpiresAt.IsZero() || now.Add(c.cfg.skew()).Before(c.token.ExpiresAt)) {
		return c.token.AccessToken, nil
	}
	stillValid := rejected == "" && now.Before(c.token.ExpiresAt)
	if c.shared != nil {
		return c.renewShared(ctx, rejected, stillValid)
	}
	return c.renewOwn(ctx, stillValid)
}

// renewOwn refreshes this Client's own lineage. Called with c.mu held.
func (c *Client) renewOwn(ctx context.Context, stillValid bool) (string, error) {
	if c.token.RefreshToken == "" {
		if stillValid {
			return c.token.AccessToken, nil
		}
		c.expired.Store(true)
		return "", ErrSessionExpired
	}
	// Refreshing under the lock collapses concurrent refreshes into one; the
	// refresh token rotates, so two parallel refreshes would kill the session.
	next, err := refresh(ctx, c.cfg, c.token)
	switch {
	case err == nil:
		c.token = next
		c.current.Store(&next)
		return next.AccessToken, nil
	case errors.Is(err, errRefreshRejected):
		c.expired.Store(true)
		return "", fmt.Errorf("%w: %v", ErrSessionExpired, err)
	case stillValid:
		// A transient refresh failure: keep using the token until it expires.
		return c.token.AccessToken, nil
	default:
		return "", err
	}
}

// Call runs one tool and returns the payload's "result" field when the tool
// uses the {success, errorCode, errorMsg, result} envelope, or the whole
// payload otherwise. A token rejection is retried once with a refreshed
// token; the gateway did not execute the rejected call.
func (c *Client) Call(ctx context.Context, server Server, tool string, args any) (json.RawMessage, error) {
	return c.callRetrying(ctx, server, tool, args, false)
}

// CallRaw runs one tool like Call but returns the tool's whole JSON payload,
// business failures included: success, errorCode and result are left to the
// caller, for a host that reproduces another client's handling of them (see
// clicompat). A rejected token is still an error, retried once like Call, and
// so are HTTP, JSON-RPC and isError failures. A text answer that is not
// JSON comes back as a JSON string.
func (c *Client) CallRaw(ctx context.Context, server Server, tool string, args any) (json.RawMessage, error) {
	return c.callRetrying(ctx, server, tool, args, true)
}

func (c *Client) callRetrying(ctx context.Context, server Server, tool string, args any, whole bool) (json.RawMessage, error) {
	token, err := c.accessToken(ctx)
	if err != nil {
		return nil, err
	}
	raw, err := c.call(ctx, token, server, tool, args, whole)
	if err == nil || !IsAuth(err) {
		return raw, err
	}
	fresh, ferr := c.afterRejection(ctx, token)
	if ferr != nil {
		// Keep the gateway's rejection visible next to ErrSessionExpired.
		return nil, fmt.Errorf("%w: %w", ferr, err)
	}
	return c.call(ctx, fresh, server, tool, args, whole)
}

type rpcRequest struct {
	JSONRPC string    `json:"jsonrpc"`
	ID      int       `json:"id"`
	Method  string    `json:"method"`
	Params  rpcParams `json:"params"`
}

type rpcParams struct {
	Name      string `json:"name"`
	Arguments any    `json:"arguments"`
}

type rpcResponse struct {
	Result *struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	} `json:"result"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// envelope is the business wrapper most DWS tools put in their text block.
type envelope struct {
	Success   *bool           `json:"success"`
	ErrorCode json.RawMessage `json:"errorCode"`
	ErrorMsg  *string         `json:"errorMsg"`
	Result    json.RawMessage `json:"result"`
}

func (c *Client) call(ctx context.Context, token string, server Server, tool string, args any, whole bool) (json.RawMessage, error) {
	fail := func(kind ErrorKind, status int, code, msg string) error {
		// Provider text is copied into errors; never let it echo the token.
		// Redact before clipping, or a token cut at the limit leaks a prefix.
		msg = strings.ReplaceAll(msg, token, "[redacted]")
		code = strings.ReplaceAll(code, token, "[redacted]")
		return &Error{Server: server, Tool: tool, Kind: kind, Status: status, Code: clip(code, 64), Message: clip(msg, 300)}
	}
	path, err := serverPath(server)
	if err != nil {
		return nil, err
	}
	if args == nil {
		args = map[string]any{}
	}
	body, err := json.Marshal(rpcRequest{JSONRPC: "2.0", ID: 1, Method: "tools/call", Params: rpcParams{Name: tool, Arguments: args}})
	if err != nil {
		return nil, fmt.Errorf("dws: encode %s arguments: %w", tool, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.gatewayURL()+path, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("dws: build request: %w", err)
	}
	for k, vs := range c.cfg.Header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	req.Header.Set("Content-Type", "application/json")
	// The gateway answers 406 without an explicit JSON Accept.
	req.Header.Set("Accept", "application/json")
	req.Header.Set("x-user-access-token", token)

	resp, err := c.cfg.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("dws: %s/%s: %w", server, tool, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("dws: %s/%s: read response: %w", server, tool, err)
	}
	if len(raw) > maxResponseBytes {
		return nil, fail(KindDecode, resp.StatusCode, "", "response too large")
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var plain struct {
			Error string `json:"error"`
		}
		msg := string(raw)
		if json.Unmarshal(raw, &plain) == nil && plain.Error != "" {
			msg = plain.Error
		}
		return nil, fail(KindHTTP, resp.StatusCode, "", msg)
	}
	var rpc rpcResponse
	if err := json.Unmarshal(raw, &rpc); err != nil {
		return nil, fail(KindDecode, resp.StatusCode, "", "invalid JSON-RPC response")
	}
	if rpc.Error != nil {
		return nil, fail(KindRPC, 0, fmt.Sprint(rpc.Error.Code), rpc.Error.Message)
	}
	if rpc.Result == nil {
		return nil, fail(KindDecode, 0, "", "JSON-RPC response has no result")
	}
	text := ""
	for _, block := range rpc.Result.Content {
		if block.Type == "text" {
			text = block.Text
			break
		}
	}
	if rpc.Result.IsError {
		return nil, fail(KindTool, 0, "", text)
	}
	payload := []byte(strings.TrimSpace(text))
	if len(payload) == 0 || !json.Valid(payload) {
		// A few tools answer in plain text; hand it back as a JSON string.
		quoted, _ := json.Marshal(text)
		return quoted, nil
	}
	var env envelope
	if payload[0] != '{' || json.Unmarshal(payload, &env) != nil {
		return payload, nil
	}
	if whole {
		// The token was refused inside the payload: the call did not run.
		if code := payloadAuthCode(payload); code != "" {
			return nil, fail(KindBusiness, 0, code, "")
		}
		return payload, nil
	}
	if env.Success == nil {
		return payload, nil
	}
	if !*env.Success {
		msg := ""
		if env.ErrorMsg != nil {
			msg = *env.ErrorMsg
		}
		return nil, fail(KindBusiness, 0, rawCode(env.ErrorCode), msg)
	}
	if len(env.Result) == 0 || string(env.Result) == "null" {
		return json.RawMessage(`{}`), nil
	}
	return env.Result, nil
}

// payloadAuthCode is a gateway token-rejection code anywhere dws looks for
// one (errorCode, error_code, code), whatever success says.
func payloadAuthCode(payload []byte) string {
	var codes struct {
		ErrorCode json.RawMessage `json:"errorCode"`
		ErrorCd   json.RawMessage `json:"error_code"`
		Code      json.RawMessage `json:"code"`
	}
	if json.Unmarshal(payload, &codes) != nil {
		return ""
	}
	for _, raw := range []json.RawMessage{codes.ErrorCode, codes.ErrorCd, codes.Code} {
		if code := rawCode(raw); authCodes[code] {
			return code
		}
	}
	return ""
}

// rawCode renders errorCode, which DWS sends as a string, a number or null.
func rawCode(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "null" {
		return ""
	}
	return trimmed
}
