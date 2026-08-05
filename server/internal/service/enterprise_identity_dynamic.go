package service

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"time"
)

type enterpriseIdentityClientSet struct {
	buc   BUCOAuthClient
	authX EnterpriseAuthX
	idem  EnterpriseIdem
}

type dynamicEnterpriseIdentityClients struct {
	provider func() EnterpriseIdentityConfig

	mu      sync.Mutex
	config  EnterpriseIdentityConfig
	clients enterpriseIdentityClientSet
}

// ValidateEnterpriseIdentityClients verifies that all provider transports can
// be constructed for a candidate dynamic snapshot. It performs no network IO.
func ValidateEnterpriseIdentityClients(config EnterpriseIdentityConfig) error {
	_, err := buildEnterpriseIdentityClientSet(config)
	return err
}

func buildEnterpriseIdentityClientSet(config EnterpriseIdentityConfig) (enterpriseIdentityClientSet, error) {
	buc, err := NewHTTPBUCOAuthClient(
		config.BUCTokenURL,
		config.BUCIssuer,
		config.BUCJWKSURL,
		config.BUCClientID,
		config.BUCClientSecret,
		config.BUCRedirectURL,
		nil,
	)
	if err != nil {
		return enterpriseIdentityClientSet{}, err
	}
	authX, err := NewNormandyAuthXClient(
		config.AuthXServiceID,
		config.AuthXAudience,
		config.AuthXTTL,
		config.AuthXEnvironment,
	)
	if err != nil {
		return enterpriseIdentityClientSet{}, err
	}
	idem, err := NewIdemEnterpriseClient(config.IdemBaseURL, config.IdemTimeout)
	if err != nil {
		return enterpriseIdentityClientSet{}, err
	}
	return enterpriseIdentityClientSet{buc: buc, authX: authX, idem: idem}, nil
}

func newDynamicEnterpriseIdentityClients(
	provider func() EnterpriseIdentityConfig,
) (*dynamicEnterpriseIdentityClients, error) {
	if provider == nil {
		return nil, errors.New("enterprise identity config provider is required")
	}
	clients := &dynamicEnterpriseIdentityClients{provider: provider}
	if _, err := clients.current(); err != nil {
		return nil, err
	}
	return clients, nil
}

func (c *dynamicEnterpriseIdentityClients) current() (enterpriseIdentityClientSet, error) {
	if c == nil || c.provider == nil {
		return enterpriseIdentityClientSet{}, errors.New("enterprise identity dynamic clients are unavailable")
	}
	config := c.provider()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.clients.buc != nil && reflect.DeepEqual(c.config, config) {
		return c.clients, nil
	}
	clients, err := buildEnterpriseIdentityClientSet(config)
	if err != nil {
		return enterpriseIdentityClientSet{}, err
	}
	c.config = config
	c.clients = clients
	return c.clients, nil
}

func (c *dynamicEnterpriseIdentityClients) ExchangeCode(ctx context.Context, code string) (BUCIdentityTokens, error) {
	clients, err := c.current()
	if err != nil {
		return BUCIdentityTokens{}, err
	}
	return clients.buc.ExchangeCode(ctx, code)
}

func (c *dynamicEnterpriseIdentityClients) Refresh(ctx context.Context, token string) (BUCIdentityTokens, error) {
	clients, err := c.current()
	if err != nil {
		return BUCIdentityTokens{}, err
	}
	return clients.buc.Refresh(ctx, token)
}

func (c *dynamicEnterpriseIdentityClients) GenerateSSOTicket(ctx context.Context, token string) (string, error) {
	clients, err := c.current()
	if err != nil {
		return "", err
	}
	return clients.buc.GenerateSSOTicket(ctx, token)
}

func (c *dynamicEnterpriseIdentityClients) VerifyIDToken(
	ctx context.Context,
	token string,
	nonceHash []byte,
	now time.Time,
) (bucIDTokenClaims, error) {
	clients, err := c.current()
	if err != nil {
		return bucIDTokenClaims{}, err
	}
	return clients.buc.VerifyIDToken(ctx, token, nonceHash, now)
}

func (c *dynamicEnterpriseIdentityClients) IssueFromSSOTicket(ctx context.Context, ticket string) (EnterpriseOIDCToken, error) {
	clients, err := c.current()
	if err != nil {
		return EnterpriseOIDCToken{}, err
	}
	return clients.authX.IssueFromSSOTicket(ctx, ticket)
}

func (c *dynamicEnterpriseIdentityClients) Renew(ctx context.Context, token string) (EnterpriseOIDCToken, error) {
	clients, err := c.current()
	if err != nil {
		return EnterpriseOIDCToken{}, err
	}
	return clients.authX.Renew(ctx, token)
}

func (c *dynamicEnterpriseIdentityClients) EnsureAgent(
	ctx context.Context,
	registration EnterpriseAgentRegistration,
) (string, bool, error) {
	clients, err := c.current()
	if err != nil {
		return "", false, err
	}
	return clients.idem.EnsureAgent(ctx, registration)
}

func (c *dynamicEnterpriseIdentityClients) IssueAIT(
	ctx context.Context,
	oidcToken string,
	agentSPIFFEID string,
	operatorSPIFFEID string,
	ttl int64,
) (string, error) {
	clients, err := c.current()
	if err != nil {
		return "", err
	}
	return clients.idem.IssueAIT(ctx, oidcToken, agentSPIFFEID, operatorSPIFFEID, ttl)
}

func (c *dynamicEnterpriseIdentityClients) DeleteAgent(ctx context.Context, aipID, operatorSPIFFEID string) error {
	clients, err := c.current()
	if err != nil {
		return err
	}
	return clients.idem.DeleteAgent(ctx, aipID, operatorSPIFFEID)
}
