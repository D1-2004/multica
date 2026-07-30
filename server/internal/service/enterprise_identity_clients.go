package service

import (
	"context"
	"crypto/rsa"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	idemapi "gitlab.alibaba-inc.com/idem/idem-api-client-golang"
)

const enterpriseIdentityMaxResponseBytes = 1 << 20

const (
	idemEnterpriseAgentType        = "multica"
	idemEnterpriseFrameworkName    = "Aone Sandbox"
	idemEnterpriseFrameworkVersion = "1.0.0"
)

type EnterpriseOIDCToken struct {
	IDToken          string
	RefreshToken     string
	ExpiresAt        time.Time
	RefreshExpiresAt time.Time
}

type EnterpriseAuthX interface {
	IssueForBUCIdentity(context.Context, BUCIdentityTokens) (EnterpriseOIDCToken, error)
	Renew(context.Context, string) (EnterpriseOIDCToken, error)
}

type BUCEnterpriseAuthXClient struct {
	client         BUCOAuthClient
	refreshHorizon time.Duration
	now            func() time.Time
}

func NewBUCEnterpriseAuthXClient(
	client BUCOAuthClient,
	refreshHorizon time.Duration,
) (*BUCEnterpriseAuthXClient, error) {
	if client == nil || refreshHorizon <= 0 {
		return nil, errors.New("BUC OIDC client and positive refresh horizon are required")
	}
	return &BUCEnterpriseAuthXClient{
		client:         client,
		refreshHorizon: refreshHorizon,
		now:            time.Now,
	}, nil
}

func (c *BUCEnterpriseAuthXClient) IssueForBUCIdentity(
	ctx context.Context,
	tokens BUCIdentityTokens,
) (EnterpriseOIDCToken, error) {
	if err := ctx.Err(); err != nil {
		return EnterpriseOIDCToken{}, err
	}
	return c.normalize(tokens)
}

func (c *BUCEnterpriseAuthXClient) Renew(ctx context.Context, refreshToken string) (EnterpriseOIDCToken, error) {
	if err := ctx.Err(); err != nil {
		return EnterpriseOIDCToken{}, err
	}
	if strings.TrimSpace(refreshToken) == "" {
		return EnterpriseOIDCToken{}, errors.New("BUC refresh token is required")
	}
	tokens, err := c.client.Refresh(ctx, refreshToken)
	if err != nil {
		return EnterpriseOIDCToken{}, err
	}
	return c.normalize(tokens)
}

func (c *BUCEnterpriseAuthXClient) normalize(tokens BUCIdentityTokens) (EnterpriseOIDCToken, error) {
	if strings.TrimSpace(tokens.IDToken) == "" ||
		strings.TrimSpace(tokens.RefreshToken) == "" ||
		tokens.ExpiresIn <= 0 {
		return EnterpriseOIDCToken{}, errors.New("BUC returned an incomplete OIDC token")
	}
	now := c.now()
	return EnterpriseOIDCToken{
		IDToken:          tokens.IDToken,
		RefreshToken:     tokens.RefreshToken,
		ExpiresAt:        now.Add(time.Duration(tokens.ExpiresIn) * time.Second),
		RefreshExpiresAt: now.Add(c.refreshHorizon),
	}, nil
}

type EnterpriseAgentRegistration struct {
	SPIFFEID    string
	OperatorID  string
	EmployeeID  string
	DisplayName string
	AgentModel  string
	BindingAt   time.Time
}

type EnterpriseIdem interface {
	EnsureAgent(context.Context, EnterpriseAgentRegistration) (aipID string, created bool, err error)
	IssueAIT(context.Context, string, string, string, int64) (string, error)
	DeleteAgent(context.Context, string, string) error
}

type IdemEnterpriseClient struct {
	client *idemapi.IdemApiClient
}

func NewIdemEnterpriseClient(baseURL string, timeout time.Duration) (*IdemEnterpriseClient, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" || timeout <= 0 {
		return nil, errors.New("Idem base URL and positive timeout are required")
	}
	client, err := idemapi.NewBuilder().
		BaseUrl(baseURL).
		ConnectTimeout(timeout).
		ReadTimeout(timeout).
		WriteTimeout(timeout).
		Build()
	if err != nil {
		return nil, fmt.Errorf("create Idem client: %w", err)
	}
	return &IdemEnterpriseClient{client: client}, nil
}

func (c *IdemEnterpriseClient) EnsureAgent(
	ctx context.Context,
	registration EnterpriseAgentRegistration,
) (string, bool, error) {
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	profiles, err := c.client.AgentIdentityClient.QueryAgentIdentities(idemapi.QueryAgentIdentitiesParams{
		Page:     0,
		Size:     2,
		SpiffeId: registration.SPIFFEID,
		State:    "ACTIVE",
	})
	if err != nil {
		return "", false, fmt.Errorf("query Idem Agent Identity: %w", err)
	}
	if len(profiles) > 1 {
		return "", false, errors.New("Idem returned multiple active identities for one SPIFFE ID")
	}
	if len(profiles) == 1 {
		if err := validateExistingIdemAgent(profiles[0], registration); err != nil {
			return "", false, err
		}
		aipID := strings.TrimSpace(profiles[0].Metadata.AgentIdentityProfileId)
		if aipID == "" {
			return "", false, errors.New("Idem returned an Agent Identity without an AIP ID")
		}
		return aipID, false, nil
	}
	registered, err := c.client.AgentIdentityClient.RegisterAgentIdentity(&idemapi.AgentIdentityRegistry{
		AgentId:      registration.SPIFFEID,
		AgentType:    idemEnterpriseAgentType,
		AipAgentType: "assistant",
		DisplayName:  registration.DisplayName,
		AgentModel:   registration.AgentModel,
		Framework: &idemapi.AgentIdentityProfileFramework{
			Name:    idemEnterpriseFrameworkName,
			Version: idemEnterpriseFrameworkVersion,
		},
		OwnerBinding: &idemapi.OwnerBinding{
			OwnerType:    "user",
			OwnerEmpId:   registration.EmployeeID,
			BindingModel: "server_mediated",
			BindingProof: &idemapi.OwnerBindingProof{
				BindingAt: registration.BindingAt.UnixMilli(),
			},
		},
		Capabilities: &idemapi.AgentIdentityProfileCapabilities{
			Declared:      []idemapi.CapabilityDeclaration{},
			AutonomyLevel: "human_on_the_loop",
		},
	}, registration.OperatorID)
	if err != nil {
		return "", false, fmt.Errorf("register Idem Agent Identity: %w", err)
	}
	aipID := strings.TrimSpace(registered.Metadata.AgentIdentityProfileId)
	if aipID == "" {
		return "", false, errors.New("Idem registered an Agent Identity without an AIP ID")
	}
	return aipID, true, nil
}

func validateExistingIdemAgent(
	profile idemapi.AgentIdentityProfile,
	registration EnterpriseAgentRegistration,
) error {
	owner := profile.Spec.OwnerBinding
	if profile.Spec.AgentId != registration.SPIFFEID ||
		profile.Spec.AgentType != idemEnterpriseAgentType ||
		profile.Spec.AipAgentType != "assistant" ||
		profile.Spec.Framework == nil ||
		profile.Spec.Framework.Name != idemEnterpriseFrameworkName ||
		profile.Spec.Framework.Version != idemEnterpriseFrameworkVersion ||
		owner == nil ||
		owner.OwnerType != "user" ||
		owner.OwnerEmpId != registration.EmployeeID ||
		owner.BindingModel != "server_mediated" {
		return errors.New("existing Idem Agent Identity does not match the requested employee binding")
	}
	return nil
}

func (c *IdemEnterpriseClient) IssueAIT(
	ctx context.Context,
	oidcToken string,
	agentSPIFFEID string,
	operatorSPIFFEID string,
	ttl int64,
) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if strings.TrimSpace(oidcToken) == "" ||
		strings.TrimSpace(agentSPIFFEID) == "" ||
		strings.TrimSpace(operatorSPIFFEID) == "" ||
		ttl <= 0 {
		return "", errors.New("Idem AIT request is incomplete")
	}
	result, err := c.client.AgentIdentityTokenClient.IssueAit(&idemapi.IssueAitRequest{
		OidcToken:       oidcToken,
		AgentId:         agentSPIFFEID,
		AgentPublicKey:  "",
		ValiditySeconds: &ttl,
	}, operatorSPIFFEID)
	if err != nil {
		return "", fmt.Errorf("issue Idem Agent Identity Token: %w", err)
	}
	if result == nil || strings.TrimSpace(result.Ait) == "" {
		return "", errors.New("Idem returned an empty Agent Identity Token")
	}
	return result.Ait, nil
}

func (c *IdemEnterpriseClient) DeleteAgent(ctx context.Context, aipID, operatorSPIFFEID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(aipID) == "" || strings.TrimSpace(operatorSPIFFEID) == "" {
		return errors.New("Idem delete request is incomplete")
	}
	if err := c.client.AgentIdentityClient.DeleteAgentIdentity(aipID, operatorSPIFFEID); err != nil {
		return fmt.Errorf("delete Idem Agent Identity: %w", err)
	}
	return nil
}

type BUCIdentityTokens struct {
	AccessToken  string
	RefreshToken string
	IDToken      string
	ExpiresIn    int64
}

type BUCOAuthClient interface {
	ExchangeCode(context.Context, string) (BUCIdentityTokens, error)
	Refresh(context.Context, string) (BUCIdentityTokens, error)
	VerifyIDToken(context.Context, string, []byte, time.Time) (bucIDTokenClaims, error)
}

type HTTPBUCOAuthClient struct {
	tokenURL     *url.URL
	issuer       string
	jwksURL      *url.URL
	clientID     string
	clientSecret string
	redirectURL  string
	httpClient   *http.Client
}

func NewHTTPBUCOAuthClient(
	tokenURL string,
	issuer string,
	jwksURL string,
	clientID string,
	clientSecret string,
	redirectURL string,
	source *http.Client,
) (*HTTPBUCOAuthClient, error) {
	parsedTokenURL, err := parseBUCHTTPEndpoint("token", tokenURL)
	if err != nil {
		return nil, err
	}
	parsedIssuer, err := parseBUCHTTPEndpoint("issuer", issuer)
	if err != nil {
		return nil, err
	}
	parsedJWKSURL, err := parseBUCHTTPEndpoint("JWKS", jwksURL)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(clientID) == "" ||
		strings.TrimSpace(clientSecret) == "" ||
		strings.TrimSpace(redirectURL) == "" {
		return nil, errors.New("BUC OAuth client configuration is incomplete")
	}
	client := &http.Client{Timeout: 20 * time.Second}
	if source != nil {
		*client = *source
		if client.Timeout <= 0 {
			client.Timeout = 20 * time.Second
		}
	}
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &HTTPBUCOAuthClient{
		tokenURL:     parsedTokenURL,
		issuer:       parsedIssuer.String(),
		jwksURL:      parsedJWKSURL,
		clientID:     clientID,
		clientSecret: clientSecret,
		redirectURL:  redirectURL,
		httpClient:   client,
	}, nil
}

func parseBUCHTTPEndpoint(name, raw string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return nil, fmt.Errorf("BUC %s URL must be an absolute HTTPS URL", name)
	}
	return parsed, nil
}

func (c *HTTPBUCOAuthClient) ExchangeCode(ctx context.Context, code string) (BUCIdentityTokens, error) {
	code = strings.TrimSpace(code)
	if code == "" {
		return BUCIdentityTokens{}, errors.New("BUC authorization code is required")
	}
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {c.redirectURL},
		"client_id":     {c.clientID},
		"client_secret": {c.clientSecret},
	}
	return c.requestTokens(ctx, form, "exchange BUC authorization code")
}

func (c *HTTPBUCOAuthClient) Refresh(ctx context.Context, refreshToken string) (BUCIdentityTokens, error) {
	refreshToken = strings.TrimSpace(refreshToken)
	if refreshToken == "" {
		return BUCIdentityTokens{}, errors.New("BUC refresh token is required")
	}
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {c.clientID},
		"client_secret": {c.clientSecret},
	}
	return c.requestTokens(ctx, form, "refresh BUC OIDC token")
}

func (c *HTTPBUCOAuthClient) requestTokens(
	ctx context.Context,
	form url.Values,
	operation string,
) (BUCIdentityTokens, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.tokenURL.String(), strings.NewReader(form.Encode()))
	if err != nil {
		return BUCIdentityTokens{}, errors.New("build BUC token request")
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	response, err := c.httpClient.Do(request)
	if err != nil {
		return BUCIdentityTokens{}, fmt.Errorf("%s: %w", operation, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, enterpriseIdentityMaxResponseBytes))
		return BUCIdentityTokens{}, fmt.Errorf("BUC token endpoint returned HTTP %d", response.StatusCode)
	}
	var payload struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		IDToken      string `json:"id_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, enterpriseIdentityMaxResponseBytes))
	if err := decoder.Decode(&payload); err != nil {
		return BUCIdentityTokens{}, errors.New("decode BUC token response")
	}
	if strings.TrimSpace(payload.AccessToken) == "" ||
		strings.TrimSpace(payload.RefreshToken) == "" ||
		strings.TrimSpace(payload.IDToken) == "" {
		return BUCIdentityTokens{}, errors.New("BUC token response is incomplete")
	}
	return BUCIdentityTokens{
		AccessToken:  payload.AccessToken,
		RefreshToken: payload.RefreshToken,
		IDToken:      payload.IDToken,
		ExpiresIn:    payload.ExpiresIn,
	}, nil
}

type bucIDTokenClaims struct {
	jwt.RegisteredClaims
	EmployeeID string `json:"empid"`
	Nonce      string `json:"nonce"`
	Name       string `json:"name"`
}

func (c *HTTPBUCOAuthClient) VerifyIDToken(
	ctx context.Context,
	rawToken string,
	expectedNonceHash []byte,
	now time.Time,
) (bucIDTokenClaims, error) {
	var unverifiedClaims bucIDTokenClaims
	unverifiedToken, _, err := jwt.NewParser().ParseUnverified(rawToken, &unverifiedClaims)
	if err != nil || unverifiedToken == nil || unverifiedToken.Method == nil {
		return bucIDTokenClaims{}, errors.New("BUC ID token is malformed")
	}
	algorithm := unverifiedToken.Method.Alg()
	if algorithm != jwt.SigningMethodHS256.Alg() &&
		algorithm != jwt.SigningMethodRS256.Alg() {
		return bucIDTokenClaims{}, errors.New("BUC ID token uses an unsupported signing algorithm")
	}

	var claims bucIDTokenClaims
	parser := jwt.NewParser(
		jwt.WithValidMethods([]string{algorithm}),
		jwt.WithIssuer(c.issuer),
		jwt.WithAudience(c.clientID),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
		jwt.WithTimeFunc(func() time.Time { return now }),
	)
	token, err := parser.ParseWithClaims(rawToken, &claims, func(token *jwt.Token) (any, error) {
		switch algorithm {
		case jwt.SigningMethodHS256.Alg():
			return []byte(c.clientSecret), nil
		case jwt.SigningMethodRS256.Alg():
			kid, ok := token.Header["kid"].(string)
			if !ok || strings.TrimSpace(kid) == "" {
				return nil, errors.New("BUC ID token is missing its signing key ID")
			}
			return c.fetchBUCRSAPublicKey(ctx, kid)
		default:
			return nil, errors.New("BUC ID token uses an unsupported signing algorithm")
		}
	})
	if err != nil || token == nil || !token.Valid {
		return bucIDTokenClaims{}, errors.New("BUC ID token signature or registered claims are invalid")
	}
	claims.EmployeeID = strings.TrimSpace(claims.EmployeeID)
	if strings.TrimSpace(claims.Subject) == "" ||
		strings.TrimSpace(claims.Name) == "" ||
		!enterpriseEmployeeIDPattern.MatchString(claims.EmployeeID) {
		return claims, errors.New("BUC ID token claims do not match the binding request")
	}
	actualNonceHash := sha256Bytes(claims.Nonce)
	if len(expectedNonceHash) != len(actualNonceHash) ||
		subtle.ConstantTimeCompare(expectedNonceHash, actualNonceHash) != 1 {
		return claims, errors.New("BUC ID token nonce does not match the binding request")
	}
	return claims, nil
}

type bucJWKSet struct {
	Keys []struct {
		KeyType   string `json:"kty"`
		KeyID     string `json:"kid"`
		Algorithm string `json:"alg"`
		Modulus   string `json:"n"`
		Exponent  string `json:"e"`
	} `json:"keys"`
}

func (c *HTTPBUCOAuthClient) fetchBUCRSAPublicKey(ctx context.Context, keyID string) (*rsa.PublicKey, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.jwksURL.String(), nil)
	if err != nil {
		return nil, errors.New("build BUC JWKS request")
	}
	request.Header.Set("Accept", "application/json")
	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("fetch BUC JWKS: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, enterpriseIdentityMaxResponseBytes))
		return nil, fmt.Errorf("BUC JWKS endpoint returned HTTP %d", response.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, enterpriseIdentityMaxResponseBytes+1))
	if err != nil {
		return nil, errors.New("read BUC JWKS response")
	}
	if len(raw) > enterpriseIdentityMaxResponseBytes {
		return nil, errors.New("BUC JWKS response is too large")
	}
	var keySet bucJWKSet
	if err := json.Unmarshal(raw, &keySet); err != nil {
		return nil, errors.New("decode BUC JWKS response")
	}

	var selected *rsa.PublicKey
	for _, candidate := range keySet.Keys {
		if candidate.KeyID != keyID {
			continue
		}
		if candidate.KeyType != "RSA" ||
			(candidate.Algorithm != "" && candidate.Algorithm != jwt.SigningMethodRS256.Alg()) {
			return nil, errors.New("BUC JWKS signing key has an incompatible type or algorithm")
		}
		if selected != nil {
			return nil, errors.New("BUC JWKS contains duplicate signing key IDs")
		}
		modulusBytes, err := base64.RawURLEncoding.DecodeString(candidate.Modulus)
		if err != nil {
			return nil, errors.New("BUC JWKS RSA modulus is invalid")
		}
		exponentBytes, err := base64.RawURLEncoding.DecodeString(candidate.Exponent)
		if err != nil {
			return nil, errors.New("BUC JWKS RSA exponent is invalid")
		}
		modulus := new(big.Int).SetBytes(modulusBytes)
		exponentBig := new(big.Int).SetBytes(exponentBytes)
		if modulus.BitLen() < 2048 ||
			!exponentBig.IsInt64() ||
			exponentBig.Int64() < 3 ||
			exponentBig.Int64() > int64(^uint32(0)>>1) ||
			exponentBig.Int64()%2 == 0 {
			return nil, errors.New("BUC JWKS RSA key parameters are invalid")
		}
		selected = &rsa.PublicKey{
			N: modulus,
			E: int(exponentBig.Int64()),
		}
	}
	if selected == nil {
		return nil, errors.New("BUC JWKS does not contain the requested signing key")
	}
	return selected, nil
}
