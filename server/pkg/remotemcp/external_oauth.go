package remotemcp

// Fork-only (dt-fde-multica): MCP OAuth through an ExternalClient. It reuses
// the upstream discovery helpers of oauth.go but sends every request through
// the proxy-aware, host-pinned client, tolerates the metadata shapes the
// official servers publish (a protected resource that is the origin, or no
// protected resource document at all) and reports OAuth error codes, so the
// caller can tell a revoked grant from a transient failure.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// OAuthError is an error answer of an OAuth endpoint. Code is the RFC 6749
// "error" value when the body carried one. The description is never kept:
// providers may echo request material into it.
type OAuthError struct {
	StatusCode int
	Code       string
}

func (e *OAuthError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("OAuth endpoint returned HTTP %d (%s)", e.StatusCode, e.Code)
	}
	return fmt.Sprintf("OAuth endpoint returned HTTP %d", e.StatusCode)
}

// IsInvalidGrant reports whether err says the refresh token or authorization
// code is no longer valid (the user must connect again). GitHub answers
// "bad_refresh_token" / "bad_verification_code" with HTTP 200.
func IsInvalidGrant(err error) bool {
	var oauthErr *OAuthError
	if !errors.As(err, &oauthErr) {
		return false
	}
	switch oauthErr.Code {
	case "invalid_grant", "bad_refresh_token", "bad_verification_code":
		return true
	default:
		return false
	}
}

// DiscoverOAuth follows the MCP authorization discovery chain for endpoint:
// the resource_metadata named by an unauthenticated 401 challenge, then the
// path-aware and origin-level protected resource documents (RFC 9728), then
// the authorization server metadata (RFC 8414 / OIDC). When no protected
// resource document exists, the authorization server is the MCP origin
// (MCP 2025-03-26 fallback). ResourceEndpoint is the protected resource the
// server declares (it may be the origin of endpoint), which is the RFC 8707
// resource indicator to send.
func (c *ExternalClient) DiscoverOAuth(ctx context.Context, endpoint string) (OAuthMetadata, error) {
	endpointURL, err := c.CheckURL(endpoint)
	if err != nil {
		return OAuthMetadata{}, err
	}
	if endpointURL.RawQuery != "" {
		return OAuthMetadata{}, errors.New("MCP endpoint must not carry a query")
	}
	origin := url.URL{Scheme: endpointURL.Scheme, Host: endpointURL.Host}
	candidates := []string{}
	if challenge := c.probeResourceMetadata(ctx, endpointURL); challenge != "" {
		candidates = append(candidates, challenge)
	}
	candidates = append(candidates, protectedResourceMetadataURL(endpointURL), origin.String()+"/.well-known/oauth-protected-resource")

	var resource protectedResourceMetadata
	found := false
	seen := map[string]bool{}
	for _, candidate := range candidates {
		if seen[candidate] {
			continue
		}
		seen[candidate] = true
		candidateURL, err := c.CheckURL(candidate)
		if err != nil {
			continue
		}
		var loaded protectedResourceMetadata
		if c.getJSON(ctx, candidateURL, nil, &loaded) == nil && len(loaded.AuthorizationServers) > 0 {
			resource, found = loaded, true
			break
		}
	}

	resourceID := endpointURL.String()
	issuer := origin.String()
	if found {
		if resource.Resource != "" {
			if !resourceCoversEndpoint(resource.Resource, endpointURL) {
				return OAuthMetadata{}, errors.New("protected resource metadata does not match the MCP endpoint")
			}
			resourceID = resource.Resource
		}
		issuer = resource.AuthorizationServers[0]
	}
	issuerURL, err := c.CheckURL(issuer)
	if err != nil {
		return OAuthMetadata{}, fmt.Errorf("authorization server: %w", err)
	}
	var server authorizationServerMetadata
	var discoveryErr error = errors.New("no authorization server metadata")
	for _, candidate := range authorizationMetadataURLs(issuerURL) {
		candidateURL, err := c.CheckURL(candidate)
		if err != nil {
			discoveryErr = err
			continue
		}
		var loaded authorizationServerMetadata
		if err := c.getJSON(ctx, candidateURL, nil, &loaded); err != nil {
			discoveryErr = err
			continue
		}
		server, discoveryErr = loaded, nil
		break
	}
	if discoveryErr != nil {
		return OAuthMetadata{}, fmt.Errorf("load authorization server metadata: %w", discoveryErr)
	}
	if server.AuthorizationEndpoint == "" || server.TokenEndpoint == "" {
		return OAuthMetadata{}, errors.New("authorization server metadata is missing required endpoints")
	}
	if len(server.CodeChallengeMethodsSupported) > 0 && !containsString(server.CodeChallengeMethodsSupported, "S256") {
		return OAuthMetadata{}, errors.New("authorization server does not support PKCE S256")
	}
	for _, raw := range []string{server.AuthorizationEndpoint, server.TokenEndpoint, server.RegistrationEndpoint} {
		if raw == "" {
			continue
		}
		if _, err := c.CheckURL(raw); err != nil {
			return OAuthMetadata{}, fmt.Errorf("authorization server endpoint: %w", err)
		}
	}
	return OAuthMetadata{
		ResourceEndpoint:      resourceID,
		AuthorizationEndpoint: server.AuthorizationEndpoint,
		TokenEndpoint:         server.TokenEndpoint,
		RegistrationEndpoint:  server.RegistrationEndpoint,
		Scopes:                append([]string(nil), resource.ScopesSupported...),
		TokenAuthMethods:      append([]string(nil), server.TokenEndpointAuthMethodsSupport...),
	}, nil
}

// resourceCoversEndpoint accepts a declared protected resource that is the
// endpoint itself or a path prefix of it on the same origin.
func resourceCoversEndpoint(raw string, endpoint *url.URL) bool {
	resource, err := url.Parse(raw)
	if err != nil || resource.RawQuery != "" || resource.Fragment != "" || resource.User != nil ||
		!strings.EqualFold(resource.Scheme, endpoint.Scheme) || !strings.EqualFold(resource.Host, endpoint.Host) {
		return false
	}
	resourcePath := strings.TrimSuffix(resource.EscapedPath(), "/")
	endpointPath := strings.TrimSuffix(endpoint.EscapedPath(), "/")
	return resourcePath == "" || endpointPath == resourcePath || strings.HasPrefix(endpointPath, resourcePath+"/")
}

func (c *ExternalClient) probeResourceMetadata(ctx context.Context, endpoint *url.URL) string {
	payload := []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"` + DefaultSessionProtocolVersion + `","capabilities":{},"clientInfo":{"name":"multica","version":"1"}}}`)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(payload))
	if err != nil {
		return ""
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	response, err := c.http.Do(request)
	if err != nil {
		return ""
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxOAuthResponseBytes))
	for _, challenge := range response.Header.Values("WWW-Authenticate") {
		if match := resourceMetadataParameter.FindStringSubmatch(challenge); len(match) == 2 {
			return match[1]
		}
	}
	return ""
}

// GetJSON fetches rawURL (an allowed host) and decodes a JSON object into
// target. Non-2xx answers return *OAuthError without the body.
func (c *ExternalClient) GetJSON(ctx context.Context, rawURL string, headers http.Header, target any) error {
	targetURL, err := c.CheckURL(rawURL)
	if err != nil {
		return err
	}
	return c.getJSON(ctx, targetURL, headers, target)
}

func (c *ExternalClient) getJSON(ctx context.Context, endpoint *url.URL, headers http.Header, target any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return err
	}
	for key, values := range headers {
		for _, value := range values {
			request.Header.Add(key, value)
		}
	}
	request.Header.Set("Accept", "application/json")
	return c.doJSON(request, target)
}

// doJSON decodes a bounded JSON answer. An error answer (non-2xx, or a
// 2xx body with an "error" member) becomes *OAuthError.
func (c *ExternalClient) doJSON(request *http.Request, target any) error {
	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxOAuthResponseBytes+1))
	if err != nil {
		return err
	}
	if len(raw) > maxOAuthResponseBytes {
		return errors.New("OAuth response exceeds size limit")
	}
	var envelope struct {
		Error json.RawMessage `json:"error"`
	}
	_ = json.Unmarshal(raw, &envelope)
	code := ""
	if len(envelope.Error) > 0 {
		var text string
		if json.Unmarshal(envelope.Error, &text) == nil {
			code = oauthErrorCode(text)
		} else if string(envelope.Error) != "null" {
			code = "error"
		}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 || code != "" {
		return &OAuthError{StatusCode: response.StatusCode, Code: code}
	}
	return json.Unmarshal(raw, target)
}

// oauthErrorCode keeps an error code only when it is a plain token.
func oauthErrorCode(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 64 {
		return ""
	}
	for _, r := range value {
		if !(r == '_' || r == '-' || r == '.' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')) {
			return "error"
		}
	}
	return value
}

// RegisterOAuthClient performs RFC 7591 dynamic client registration. It asks
// for a public client ("none") when the server allows one, otherwise for a
// confidential client (client_secret_basic, then client_secret_post).
func (c *ExternalClient) RegisterOAuthClient(ctx context.Context, metadata OAuthMetadata, redirectURI, clientName string) (OAuthClientRegistration, error) {
	if metadata.RegistrationEndpoint == "" {
		return OAuthClientRegistration{}, errors.New("authorization server requires a pre-registered OAuth client")
	}
	endpoint, err := c.CheckURL(metadata.RegistrationEndpoint)
	if err != nil {
		return OAuthClientRegistration{}, err
	}
	method := chooseTokenEndpointAuthMethod(metadata.TokenAuthMethods)
	if strings.TrimSpace(clientName) == "" {
		clientName = "Multica"
	}
	body, err := json.Marshal(map[string]any{
		"client_name":                clientName,
		"redirect_uris":              []string{redirectURI},
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": method,
	})
	if err != nil {
		return OAuthClientRegistration{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return OAuthClientRegistration{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	var response struct {
		ClientID                string `json:"client_id"`
		ClientSecret            string `json:"client_secret"`
		TokenEndpointAuthMethod string `json:"token_endpoint_auth_method"`
	}
	if err := c.doJSON(request, &response); err != nil {
		return OAuthClientRegistration{}, fmt.Errorf("dynamic client registration: %w", err)
	}
	if strings.TrimSpace(response.ClientID) == "" {
		return OAuthClientRegistration{}, errors.New("dynamic client registration returned no client id")
	}
	if response.TokenEndpointAuthMethod == "" {
		response.TokenEndpointAuthMethod = method
	}
	if response.TokenEndpointAuthMethod != "none" && response.ClientSecret == "" {
		return OAuthClientRegistration{}, errors.New("dynamic client registration returned no client secret for a confidential client")
	}
	return OAuthClientRegistration(response), nil
}

func chooseTokenEndpointAuthMethod(supported []string) string {
	if len(supported) == 0 || containsString(supported, "none") {
		return "none"
	}
	for _, method := range []string{"client_secret_basic", "client_secret_post"} {
		if containsString(supported, method) {
			return method
		}
	}
	return "none"
}

// ExchangeOAuthCode redeems an authorization code with its PKCE verifier.
// resource is omitted when empty (pre-registered clients such as GitHub).
func (c *ExternalClient) ExchangeOAuthCode(ctx context.Context, tokenEndpoint, resource, code, redirectURI, verifier string, registration OAuthClientRegistration) (OAuthTokenResponse, error) {
	values := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"client_id":     {registration.ClientID},
		"code_verifier": {verifier},
	}
	if resource != "" {
		values.Set("resource", resource)
	}
	return c.requestToken(ctx, tokenEndpoint, values, registration)
}

// RefreshOAuthToken exchanges a refresh token. IsInvalidGrant(err) means the
// grant is gone and the user must connect again.
func (c *ExternalClient) RefreshOAuthToken(ctx context.Context, tokenEndpoint, resource, refreshToken string, registration OAuthClientRegistration) (OAuthTokenResponse, error) {
	values := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {registration.ClientID},
	}
	if resource != "" {
		values.Set("resource", resource)
	}
	return c.requestToken(ctx, tokenEndpoint, values, registration)
}

func (c *ExternalClient) requestToken(ctx context.Context, rawEndpoint string, values url.Values, registration OAuthClientRegistration) (OAuthTokenResponse, error) {
	endpoint, err := c.CheckURL(rawEndpoint)
	if err != nil {
		return OAuthTokenResponse{}, err
	}
	if registration.ClientSecret != "" && registration.TokenEndpointAuthMethod == "client_secret_post" {
		values.Set("client_secret", registration.ClientSecret)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), strings.NewReader(values.Encode()))
	if err != nil {
		return OAuthTokenResponse{}, err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	if registration.ClientSecret != "" && registration.TokenEndpointAuthMethod == "client_secret_basic" {
		request.SetBasicAuth(url.QueryEscape(registration.ClientID), url.QueryEscape(registration.ClientSecret))
	}
	var response struct {
		AccessToken  string          `json:"access_token"`
		TokenType    string          `json:"token_type"`
		ExpiresIn    json.RawMessage `json:"expires_in"`
		RefreshToken string          `json:"refresh_token"`
		Scope        string          `json:"scope"`
	}
	if err := c.doJSON(request, &response); err != nil {
		return OAuthTokenResponse{}, err
	}
	if response.AccessToken == "" || !oauthBearerTokenType(response.TokenType) {
		return OAuthTokenResponse{}, errors.New("token endpoint did not return a Bearer access token")
	}
	expiresIn := int64(0)
	if len(response.ExpiresIn) > 0 {
		if err := json.Unmarshal(response.ExpiresIn, &expiresIn); err != nil {
			var text string
			if json.Unmarshal(response.ExpiresIn, &text) == nil {
				_, _ = fmt.Sscan(text, &expiresIn)
			}
		}
	}
	return OAuthTokenResponse{
		AccessToken: response.AccessToken, TokenType: "Bearer", ExpiresIn: expiresIn,
		RefreshToken: response.RefreshToken, Scope: response.Scope,
	}, nil
}

// oauthBearerTokenType reports whether tokenType is sent as
// Authorization: Bearer. Empty and "bearer" follow RFC 6749. Slack's user
// token endpoint (oauth.v2.user.access) returns "user", and its bot endpoint
// returns "bot"; both tokens are still bearer credentials.
func oauthBearerTokenType(tokenType string) bool {
	switch strings.ToLower(strings.TrimSpace(tokenType)) {
	case "", "bearer", "user", "bot":
		return true
	default:
		return false
	}
}
