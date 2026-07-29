package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/internal/util/secretbox"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	idemapi "gitlab.alibaba-inc.com/idem/idem-api-client-golang"
)

const (
	defaultBUCAuthorizeURL               = "https://login.alibaba-inc.com/oauth2/auth.htm"
	defaultBUCTokenURL                   = "https://login.alibaba-inc.com/rpc/oauth2/access_token.json"
	defaultBUCIssuer                     = "https://login.alibaba-inc.com/oauth2"
	defaultBUCJWKSURL                    = "https://login.alibaba-inc.com/oauth2/v1/keys"
	defaultEnterpriseAuthXTTL            = int64(3600)
	defaultEnterpriseAITTTL              = int64(900)
	defaultEnterpriseIdemTimeout         = 15 * time.Second
	defaultEnterpriseOAuthAttemptTTL     = 10 * time.Minute
	defaultEnterpriseIdentityProbePeriod = time.Second
	defaultEnterpriseMaintenanceInterval = 5 * time.Minute
	defaultEnterpriseRefreshBefore       = 30 * time.Minute
	defaultEnterpriseAnchorRenewInterval = 24 * time.Hour
	defaultEnterpriseMaintenanceBatch    = int32(50)
)

var (
	ErrEnterpriseIdentityDisabled          = errors.New("enterprise identity is not configured")
	ErrEnterpriseIdentityNeedsReauth       = errors.New("enterprise identity requires employee reauthorization")
	ErrEnterpriseIdentityAnchorUnavailable = errors.New("enterprise identity anchor is unavailable")
	ErrEnterpriseIdentityEmployeeConflict  = errors.New("enterprise identity must be revoked before binding another employee")

	enterpriseEmployeeIDPattern = regexp.MustCompile(`^[1-9][0-9]*$`)
	enterprisePathPartPattern   = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
	enterpriseOCIDigestPattern  = regexp.MustCompile(`^.+@sha256:[0-9a-f]{64}$`)
)

type EnterpriseIdentityConfig struct {
	Enabled             bool
	BUCAuthorizeURL     string
	BUCTokenURL         string
	BUCIssuer           string
	BUCJWKSURL          string
	BUCClientID         string
	BUCClientSecret     string
	BUCAgentID          string
	BUCRedirectURL      string
	BUCAuthorizeApps    []string
	AuthXServiceID      string
	AuthXAudience       string
	AuthXTTL            int64
	AuthXEnvironment    string
	IdemBaseURL         string
	IdemTimeout         time.Duration
	OperatorTrustDomain string
	AgentTrustDomain    string
	AgentNamespace      string
	AITTTL              int64
	OAuthAttemptTTL     time.Duration
	ParseError          error
}

func EnterpriseIdentityConfigFromEnv() EnterpriseIdentityConfig {
	cfg := EnterpriseIdentityConfig{
		Enabled:             envBool("MULTICA_ENTERPRISE_IDENTITY_ENABLED"),
		BUCAuthorizeURL:     firstNonEmptyString(os.Getenv("MULTICA_BUC_AUTHORIZE_URL"), defaultBUCAuthorizeURL),
		BUCTokenURL:         firstNonEmptyString(os.Getenv("MULTICA_BUC_TOKEN_URL"), defaultBUCTokenURL),
		BUCIssuer:           firstNonEmptyString(os.Getenv("MULTICA_BUC_ISSUER"), defaultBUCIssuer),
		BUCJWKSURL:          firstNonEmptyString(os.Getenv("MULTICA_BUC_JWKS_URL"), defaultBUCJWKSURL),
		BUCClientID:         strings.TrimSpace(os.Getenv("MULTICA_BUC_CLIENT_ID")),
		BUCClientSecret:     strings.TrimSpace(os.Getenv("MULTICA_BUC_CLIENT_SECRET")),
		BUCAgentID:          strings.TrimSpace(os.Getenv("MULTICA_BUC_AGENT_ID")),
		BUCRedirectURL:      strings.TrimSpace(os.Getenv("MULTICA_BUC_REDIRECT_URL")),
		AuthXServiceID:      strings.TrimSpace(os.Getenv("MULTICA_AUTHX_SERVICE_ID")),
		AuthXAudience:       strings.TrimSpace(os.Getenv("MULTICA_AUTHX_AUDIENCE")),
		AuthXTTL:            defaultEnterpriseAuthXTTL,
		AuthXEnvironment:    firstNonEmptyString(os.Getenv("MULTICA_AUTHX_ENVIRONMENT"), "production"),
		IdemBaseURL:         strings.TrimRight(strings.TrimSpace(os.Getenv("MULTICA_IDEM_BASE_URL")), "/"),
		IdemTimeout:         defaultEnterpriseIdemTimeout,
		OperatorTrustDomain: strings.TrimSpace(os.Getenv("MULTICA_IDEM_OPERATOR_TRUST_DOMAIN")),
		AgentTrustDomain:    strings.TrimSpace(os.Getenv("MULTICA_IDEM_AGENT_TRUST_DOMAIN")),
		AgentNamespace:      strings.TrimSpace(os.Getenv("MULTICA_IDEM_AGENT_NAMESPACE")),
		AITTTL:              defaultEnterpriseAITTTL,
		OAuthAttemptTTL:     defaultEnterpriseOAuthAttemptTTL,
	}
	apps, err := parseStringListEnv("MULTICA_BUC_AUTHORIZE_APPS", os.Getenv("MULTICA_BUC_AUTHORIZE_APPS"))
	if err != nil {
		cfg.ParseError = errors.Join(cfg.ParseError, err)
	} else {
		cfg.BUCAuthorizeApps = apps
	}
	parsePositiveInt64Env("MULTICA_AUTHX_TTL_SECONDS", &cfg.AuthXTTL, &cfg.ParseError)
	parsePositiveInt64Env("MULTICA_IDEM_AIT_TTL_SECONDS", &cfg.AITTTL, &cfg.ParseError)
	parsePositiveDurationEnv("MULTICA_IDEM_TIMEOUT", &cfg.IdemTimeout, &cfg.ParseError)
	parsePositiveDurationEnv("MULTICA_ENTERPRISE_IDENTITY_OAUTH_ATTEMPT_TTL", &cfg.OAuthAttemptTTL, &cfg.ParseError)
	return cfg
}

func parsePositiveInt64Env(name string, target *int64, parseErr *error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value <= 0 {
		*parseErr = errors.Join(*parseErr, fmt.Errorf("invalid %s", name))
		return
	}
	*target = value
}

func (c EnterpriseIdentityConfig) Validate(asb ASBConfig) error {
	if !c.Enabled {
		return ErrEnterpriseIdentityDisabled
	}
	if c.ParseError != nil {
		return c.ParseError
	}
	var missing []string
	required := []struct {
		name  string
		value string
	}{
		{"MULTICA_BUC_CLIENT_ID", c.BUCClientID},
		{"MULTICA_BUC_CLIENT_SECRET", c.BUCClientSecret},
		{"MULTICA_BUC_AGENT_ID", c.BUCAgentID},
		{"MULTICA_BUC_REDIRECT_URL", c.BUCRedirectURL},
		{"MULTICA_AUTHX_SERVICE_ID", c.AuthXServiceID},
		{"MULTICA_AUTHX_AUDIENCE", c.AuthXAudience},
		{"MULTICA_IDEM_BASE_URL", c.IdemBaseURL},
		{"MULTICA_IDEM_OPERATOR_TRUST_DOMAIN", c.OperatorTrustDomain},
		{"MULTICA_IDEM_AGENT_TRUST_DOMAIN", c.AgentTrustDomain},
		{"MULTICA_IDEM_AGENT_NAMESPACE", c.AgentNamespace},
		{"MULTICA_ASB_IDENTITY_ANCHOR_IMAGE", asb.IdentityAnchorImageRef},
	}
	for _, item := range required {
		if strings.TrimSpace(item.value) == "" {
			missing = append(missing, item.name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing enterprise identity config: %s", strings.Join(missing, ", "))
	}
	for name, value := range map[string]string{
		"operator trust domain": c.OperatorTrustDomain,
		"agent trust domain":    c.AgentTrustDomain,
		"agent namespace":       c.AgentNamespace,
	} {
		if !enterprisePathPartPattern.MatchString(value) {
			return fmt.Errorf("invalid %s", name)
		}
	}
	for name, raw := range map[string]string{
		"BUC authorize URL": c.BUCAuthorizeURL,
		"BUC token URL":     c.BUCTokenURL,
		"BUC redirect URL":  c.BUCRedirectURL,
		"BUC issuer":        c.BUCIssuer,
		"BUC JWKS URL":      c.BUCJWKSURL,
	} {
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
			return fmt.Errorf("%s must be an absolute HTTPS URL", name)
		}
	}
	if len(c.BUCAuthorizeApps) > 40 {
		return errors.New("MULTICA_BUC_AUTHORIZE_APPS must contain at most 40 applications")
	}
	for _, app := range c.BUCAuthorizeApps {
		if strings.TrimSpace(app) == "" || strings.ContainsAny(app, "\r\n") {
			return errors.New("MULTICA_BUC_AUTHORIZE_APPS contains an invalid application")
		}
	}
	if c.AuthXTTL <= 0 || c.AITTTL <= 0 || c.IdemTimeout <= 0 || c.OAuthAttemptTTL <= 0 {
		return errors.New("enterprise identity durations must be positive")
	}
	if !enterpriseOCIDigestPattern.MatchString(asb.IdentityAnchorImageRef) {
		return errors.New("MULTICA_ASB_IDENTITY_ANCHOR_IMAGE must use an immutable sha256 OCI digest")
	}
	if asb.IdentityAnchorTimeout <= 0 {
		return errors.New("MULTICA_ASB_IDENTITY_ANCHOR_TIMEOUT must be positive")
	}
	return nil
}

type enterpriseIdentityStore interface {
	CreateAgentEnterpriseIdentityAttempt(context.Context, db.CreateAgentEnterpriseIdentityAttemptParams) (db.AgentEnterpriseIdentityAttempt, error)
	ConsumeAgentEnterpriseIdentityAttempt(context.Context, []byte) (db.AgentEnterpriseIdentityAttempt, error)
	GetAgent(context.Context, pgtype.UUID) (db.Agent, error)
	GetAgentEnterpriseIdentity(context.Context, db.GetAgentEnterpriseIdentityParams) (db.AgentEnterpriseIdentity, error)
	GetActiveAgentEnterpriseIdentity(context.Context, db.GetActiveAgentEnterpriseIdentityParams) (db.AgentEnterpriseIdentity, error)
	UpsertAgentEnterpriseIdentity(context.Context, db.UpsertAgentEnterpriseIdentityParams) (db.AgentEnterpriseIdentity, error)
	CompareAndSwapAgentEnterpriseIdentityToken(context.Context, db.CompareAndSwapAgentEnterpriseIdentityTokenParams) (db.AgentEnterpriseIdentity, error)
	ListAgentEnterpriseIdentitiesForMaintenance(context.Context, db.ListAgentEnterpriseIdentitiesForMaintenanceParams) ([]db.AgentEnterpriseIdentity, error)
	TouchAgentEnterpriseIdentityMaintenance(context.Context, db.TouchAgentEnterpriseIdentityMaintenanceParams) (int64, error)
	MarkAgentEnterpriseIdentityNeedsReauth(context.Context, db.MarkAgentEnterpriseIdentityNeedsReauthParams) (int64, error)
	RevokeAgentEnterpriseIdentity(context.Context, db.RevokeAgentEnterpriseIdentityParams) (db.AgentEnterpriseIdentity, error)
	ListActiveCloudSandboxSessionsByAgentIdentity(context.Context, db.ListActiveCloudSandboxSessionsByAgentIdentityParams) ([]db.FcE2bSandboxSession, error)
	MarkCloudSandboxSessionStale(context.Context, db.MarkCloudSandboxSessionStaleParams) error
}

type EnterpriseIdentityAnchor interface {
	Create(context.Context, pgtype.UUID, pgtype.UUID, string, BUCIdentityTokens) (string, error)
	EnsureAvailable(context.Context, string) error
	Delete(context.Context, string) error
}

type EnterpriseIdentityService struct {
	Store     enterpriseIdentityStore
	Config    EnterpriseIdentityConfig
	BUC       BUCOAuthClient
	AuthX     EnterpriseAuthX
	Idem      EnterpriseIdem
	Anchor    EnterpriseIdentityAnchor
	Secrets   *secretbox.Box
	Now       func() time.Time
	Authorize *url.URL

	MaintenanceInterval time.Duration
	RefreshBefore       time.Duration
	AnchorRenewInterval time.Duration
	MaintenanceBatch    int32
}

func NewEnterpriseIdentityService(
	store enterpriseIdentityStore,
	config EnterpriseIdentityConfig,
	buc BUCOAuthClient,
	authX EnterpriseAuthX,
	idem EnterpriseIdem,
	anchor EnterpriseIdentityAnchor,
	secrets *secretbox.Box,
) (*EnterpriseIdentityService, error) {
	if store == nil || buc == nil || authX == nil || idem == nil || anchor == nil || secrets == nil {
		return nil, errors.New("enterprise identity service dependencies are incomplete")
	}
	authorizeURL, err := url.Parse(config.BUCAuthorizeURL)
	if err != nil {
		return nil, errors.New("parse BUC authorize URL")
	}
	return &EnterpriseIdentityService{
		Store:     store,
		Config:    config,
		BUC:       buc,
		AuthX:     authX,
		Idem:      idem,
		Anchor:    anchor,
		Secrets:   secrets,
		Now:       time.Now,
		Authorize: authorizeURL,

		MaintenanceInterval: defaultEnterpriseMaintenanceInterval,
		RefreshBefore:       defaultEnterpriseRefreshBefore,
		AnchorRenewInterval: defaultEnterpriseAnchorRenewInterval,
		MaintenanceBatch:    defaultEnterpriseMaintenanceBatch,
	}, nil
}

type StartEnterpriseIdentityBindingInput struct {
	WorkspaceID  pgtype.UUID
	AgentID      pgtype.UUID
	ActorUserID  pgtype.UUID
	EmployeeID   string
	RedirectPath string
}

type StartEnterpriseIdentityBindingResult struct {
	AuthorizeURL string
	ExpiresAt    time.Time
}

func (s *EnterpriseIdentityService) StartBinding(
	ctx context.Context,
	input StartEnterpriseIdentityBindingInput,
) (StartEnterpriseIdentityBindingResult, error) {
	employeeID := strings.TrimSpace(input.EmployeeID)
	if !enterpriseEmployeeIDPattern.MatchString(employeeID) {
		return StartEnterpriseIdentityBindingResult{}, errors.New("employee ID must contain digits without a leading zero")
	}
	if err := validateEnterpriseRedirectPath(input.RedirectPath); err != nil {
		return StartEnterpriseIdentityBindingResult{}, err
	}
	if _, _, err := s.loadReplaceableIdentity(ctx, input.WorkspaceID, input.AgentID, employeeID); err != nil {
		return StartEnterpriseIdentityBindingResult{}, err
	}
	state, err := randomEnterpriseToken()
	if err != nil {
		return StartEnterpriseIdentityBindingResult{}, errors.New("generate enterprise identity OAuth state")
	}
	nonce, err := randomEnterpriseToken()
	if err != nil {
		return StartEnterpriseIdentityBindingResult{}, errors.New("generate enterprise identity OAuth nonce")
	}
	now := s.Now()
	expiresAt := now.Add(s.Config.OAuthAttemptTTL)
	if _, err := s.Store.CreateAgentEnterpriseIdentityAttempt(ctx, db.CreateAgentEnterpriseIdentityAttemptParams{
		WorkspaceID:       input.WorkspaceID,
		AgentID:           input.AgentID,
		ActorUserID:       input.ActorUserID,
		RequestedRawEmpID: employeeID,
		StateHash:         sha256Bytes(state),
		NonceHash:         sha256Bytes(nonce),
		RedirectPath:      input.RedirectPath,
		ExpiresAt:         pgtype.Timestamptz{Time: expiresAt, Valid: true},
	}); err != nil {
		return StartEnterpriseIdentityBindingResult{}, fmt.Errorf("create enterprise identity binding attempt: %w", err)
	}
	authorize := *s.Authorize
	query := authorize.Query()
	scope := "profile openid employee"
	if len(s.Config.BUCAuthorizeApps) > 0 {
		scope += " user_authorize"
		query.Set("authorize_app", strings.Join(s.Config.BUCAuthorizeApps, ","))
	}
	query.Set("scope", scope)
	query.Set("prompt", "consent")
	query.Set("response_type", "code")
	query.Set("client_id", s.Config.BUCClientID)
	query.Set("redirect_uri", s.Config.BUCRedirectURL)
	query.Set("agent_id", s.Config.BUCAgentID)
	query.Set("version", "1.0")
	query.Set("state", state)
	query.Set("nonce", nonce)
	authorize.RawQuery = query.Encode()
	return StartEnterpriseIdentityBindingResult{
		AuthorizeURL: authorize.String(),
		ExpiresAt:    expiresAt,
	}, nil
}

type CompleteEnterpriseIdentityBindingResult struct {
	Identity     db.AgentEnterpriseIdentity
	RedirectPath string
}

func (s *EnterpriseIdentityService) CompleteBinding(
	ctx context.Context,
	state string,
	code string,
) (CompleteEnterpriseIdentityBindingResult, error) {
	state = strings.TrimSpace(state)
	code = strings.TrimSpace(code)
	if state == "" || code == "" {
		return CompleteEnterpriseIdentityBindingResult{}, errors.New("enterprise identity OAuth callback is incomplete")
	}
	attempt, err := s.Store.ConsumeAgentEnterpriseIdentityAttempt(ctx, sha256Bytes(state))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return CompleteEnterpriseIdentityBindingResult{}, errors.New("enterprise identity OAuth attempt is invalid, expired, or already used")
		}
		return CompleteEnterpriseIdentityBindingResult{}, fmt.Errorf("consume enterprise identity OAuth attempt: %w", err)
	}
	bucTokens, err := s.BUC.ExchangeCode(ctx, code)
	if err != nil {
		return CompleteEnterpriseIdentityBindingResult{}, err
	}
	claims, err := s.BUC.VerifyIDToken(
		ctx,
		bucTokens.IDToken,
		attempt.RequestedRawEmpID,
		s.Config.BUCAgentID,
		attempt.NonceHash,
		s.Now(),
	)
	if err != nil {
		return CompleteEnterpriseIdentityBindingResult{}, err
	}
	agent, err := s.Store.GetAgent(ctx, attempt.AgentID)
	if err != nil || agent.WorkspaceID != attempt.WorkspaceID {
		return CompleteEnterpriseIdentityBindingResult{}, errors.New("enterprise identity target agent no longer exists")
	}
	oldIdentity, oldIdentityFound, err := s.loadReplaceableIdentity(
		ctx,
		attempt.WorkspaceID,
		attempt.AgentID,
		attempt.RequestedRawEmpID,
	)
	if err != nil {
		return CompleteEnterpriseIdentityBindingResult{}, err
	}
	authXToken, err := s.AuthX.IssueFromBUCIDToken(ctx, bucTokens.IDToken)
	if err != nil {
		return CompleteEnterpriseIdentityBindingResult{}, err
	}
	agentSPIFFEID, err := s.agentSPIFFEID(attempt.AgentID)
	if err != nil {
		return CompleteEnterpriseIdentityBindingResult{}, err
	}
	operatorSPIFFEID, err := s.operatorSPIFFEID(attempt.RequestedRawEmpID)
	if err != nil {
		return CompleteEnterpriseIdentityBindingResult{}, err
	}
	model := ""
	if agent.Model.Valid {
		model = agent.Model.String
	}
	aipID, aipCreated, err := s.Idem.EnsureAgent(ctx, EnterpriseAgentRegistration{
		SPIFFEID:    agentSPIFFEID,
		OperatorID:  operatorSPIFFEID,
		EmployeeID:  attempt.RequestedRawEmpID,
		DisplayName: agent.Name,
		AgentModel:  model,
		BindingAt:   s.Now(),
	})
	if err != nil {
		return CompleteEnterpriseIdentityBindingResult{}, err
	}
	cleanupAIP := func() {
		if aipCreated {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			_ = s.Idem.DeleteAgent(cleanupCtx, aipID, operatorSPIFFEID)
		}
	}
	if _, err := s.Idem.IssueAIT(ctx, authXToken.IDToken, agentSPIFFEID, operatorSPIFFEID, s.Config.AITTTL); err != nil {
		cleanupAIP()
		return CompleteEnterpriseIdentityBindingResult{}, err
	}
	anchorID, err := s.Anchor.Create(
		ctx,
		attempt.WorkspaceID,
		attempt.AgentID,
		attempt.RequestedRawEmpID,
		bucTokens,
	)
	if err != nil {
		cleanupAIP()
		return CompleteEnterpriseIdentityBindingResult{}, err
	}
	cleanupAnchor := func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = s.Anchor.Delete(cleanupCtx, anchorID)
	}
	sealedRefresh, err := s.Secrets.Seal([]byte(authXToken.RefreshToken))
	if err != nil {
		cleanupAnchor()
		cleanupAIP()
		return CompleteEnterpriseIdentityBindingResult{}, errors.New("encrypt AuthX refresh token")
	}
	identity, err := s.Store.UpsertAgentEnterpriseIdentity(ctx, db.UpsertAgentEnterpriseIdentityParams{
		WorkspaceID:                attempt.WorkspaceID,
		AgentID:                    attempt.AgentID,
		RawEmpID:                   attempt.RequestedRawEmpID,
		DisplayName:                strings.TrimSpace(claims.Name),
		BucAgentID:                 s.Config.BUCAgentID,
		AgentSpiffeID:              agentSPIFFEID,
		AipID:                      aipID,
		BucAnchorSandboxID:         pgtype.Text{String: anchorID, Valid: true},
		AuthxRefreshTokenEncrypted: sealedRefresh,
		AuthxRefreshExpiresAt:      pgtype.Timestamptz{Time: authXToken.RefreshExpiresAt, Valid: true},
		BoundBy:                    attempt.ActorUserID,
	})
	if err != nil {
		cleanupAnchor()
		cleanupAIP()
		return CompleteEnterpriseIdentityBindingResult{}, fmt.Errorf("persist enterprise identity binding: %w", err)
	}
	if oldIdentityFound &&
		oldIdentity.BucAnchorSandboxID.Valid &&
		oldIdentity.BucAnchorSandboxID.String != anchorID {
		if err := s.retireActiveSandboxes(ctx, attempt.WorkspaceID, attempt.AgentID, oldIdentity); err != nil {
			slog.Warn("failed to retire sandboxes from replaced enterprise identity",
				"agent_id", util.UUIDToString(attempt.AgentID),
				"error", err,
			)
		}
		if err := s.Anchor.Delete(ctx, oldIdentity.BucAnchorSandboxID.String); err != nil {
			slog.Warn("failed to delete replaced enterprise identity anchor",
				"agent_id", util.UUIDToString(attempt.AgentID),
				"sandbox_id", oldIdentity.BucAnchorSandboxID.String,
				"error", err,
			)
		}
	}
	return CompleteEnterpriseIdentityBindingResult{
		Identity:     identity,
		RedirectPath: attempt.RedirectPath,
	}, nil
}

func (s *EnterpriseIdentityService) loadReplaceableIdentity(
	ctx context.Context,
	workspaceID pgtype.UUID,
	agentID pgtype.UUID,
	employeeID string,
) (db.AgentEnterpriseIdentity, bool, error) {
	identity, err := s.Store.GetAgentEnterpriseIdentity(ctx, db.GetAgentEnterpriseIdentityParams{
		WorkspaceID: workspaceID,
		AgentID:     agentID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.AgentEnterpriseIdentity{}, false, nil
	}
	if err != nil {
		return db.AgentEnterpriseIdentity{}, false, fmt.Errorf("load existing enterprise identity: %w", err)
	}
	if identity.ID.Valid &&
		identity.Status != "revoked" &&
		identity.RawEmpID != strings.TrimSpace(employeeID) {
		return db.AgentEnterpriseIdentity{}, false, ErrEnterpriseIdentityEmployeeConflict
	}
	return identity, identity.ID.Valid, nil
}

func (s *EnterpriseIdentityService) ResolveASBTaskIdentity(
	ctx context.Context,
	workspaceID pgtype.UUID,
	agentID pgtype.UUID,
) (ASBResolvedIdentity, error) {
	for attempt := 0; attempt < 2; attempt++ {
		identity, err := s.Store.GetActiveAgentEnterpriseIdentity(ctx, db.GetActiveAgentEnterpriseIdentityParams{
			WorkspaceID: workspaceID,
			AgentID:     agentID,
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ASBResolvedIdentity{}, ErrEnterpriseIdentityNeedsReauth
			}
			return ASBResolvedIdentity{}, fmt.Errorf("load enterprise identity: %w", err)
		}
		if !identity.BucAnchorSandboxID.Valid ||
			strings.TrimSpace(identity.BucAnchorSandboxID.String) == "" ||
			!identity.AuthxRefreshExpiresAt.Valid ||
			!identity.AuthxRefreshExpiresAt.Time.After(s.Now()) {
			s.markNeedsReauth(ctx, identity)
			return ASBResolvedIdentity{}, ErrEnterpriseIdentityNeedsReauth
		}
		if err := s.Anchor.EnsureAvailable(ctx, identity.BucAnchorSandboxID.String); err != nil {
			if errors.Is(err, ErrEnterpriseIdentityAnchorUnavailable) {
				s.markNeedsReauth(ctx, identity)
				return ASBResolvedIdentity{}, ErrEnterpriseIdentityNeedsReauth
			}
			return ASBResolvedIdentity{}, err
		}
		updated, refreshed, err := s.rotateAuthXToken(ctx, identity)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return ASBResolvedIdentity{}, fmt.Errorf("rotate AuthX refresh token: %w", err)
		}
		operatorSPIFFEID, err := s.operatorSPIFFEID(updated.RawEmpID)
		if err != nil {
			return ASBResolvedIdentity{}, err
		}
		ait, err := s.Idem.IssueAIT(ctx, refreshed.IDToken, updated.AgentSpiffeID, operatorSPIFFEID, s.Config.AITTTL)
		if err != nil {
			return ASBResolvedIdentity{}, err
		}
		return ASBResolvedIdentity{
			RawEmployeeID:      updated.RawEmpID,
			BUCAgentID:         updated.BucAgentID,
			AgentSPIFFEID:      updated.AgentSpiffeID,
			AIPID:              updated.AipID,
			AnchorSandboxID:    updated.BucAnchorSandboxID.String,
			AgentIdentityToken: ait,
			Fingerprint:        enterpriseIdentityFingerprint(updated),
		}, nil
	}
	return ASBResolvedIdentity{}, errors.New("enterprise identity token was concurrently rotated")
}

func (s *EnterpriseIdentityService) Run(ctx context.Context) {
	runCycle := func() {
		if err := s.maintainActiveIdentities(ctx); err != nil && !errors.Is(err, context.Canceled) {
			slog.Warn("enterprise identity maintenance failed", "error", err)
		}
	}
	runCycle()
	ticker := time.NewTicker(s.MaintenanceInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			runCycle()
		}
	}
}

func (s *EnterpriseIdentityService) maintainActiveIdentities(ctx context.Context) error {
	now := s.Now()
	identities, err := s.Store.ListAgentEnterpriseIdentitiesForMaintenance(
		ctx,
		db.ListAgentEnterpriseIdentitiesForMaintenanceParams{
			RotateBefore:      pgtype.Timestamptz{Time: now.Add(s.RefreshBefore), Valid: true},
			AnchorRenewBefore: pgtype.Timestamptz{Time: now.Add(-s.AnchorRenewInterval), Valid: true},
			BatchSize:         s.MaintenanceBatch,
		},
	)
	if err != nil {
		return fmt.Errorf("list enterprise identities for maintenance: %w", err)
	}
	for _, identity := range identities {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !identity.BucAnchorSandboxID.Valid ||
			strings.TrimSpace(identity.BucAnchorSandboxID.String) == "" ||
			!identity.AuthxRefreshExpiresAt.Valid ||
			!identity.AuthxRefreshExpiresAt.Time.After(now) {
			s.markNeedsReauth(ctx, identity)
			continue
		}
		if err := s.Anchor.EnsureAvailable(ctx, identity.BucAnchorSandboxID.String); err != nil {
			if errors.Is(err, ErrEnterpriseIdentityAnchorUnavailable) {
				s.markNeedsReauth(ctx, identity)
				continue
			}
			slog.Warn("enterprise identity anchor maintenance failed",
				"agent_id", util.UUIDToString(identity.AgentID),
				"error", err,
			)
			continue
		}
		if !identity.AuthxRefreshExpiresAt.Time.After(now.Add(s.RefreshBefore)) {
			if _, _, err := s.rotateAuthXToken(ctx, identity); err != nil && !errors.Is(err, pgx.ErrNoRows) {
				slog.Warn("enterprise identity token maintenance failed",
					"agent_id", util.UUIDToString(identity.AgentID),
					"error", err,
				)
			}
			continue
		}
		if _, err := s.Store.TouchAgentEnterpriseIdentityMaintenance(
			ctx,
			db.TouchAgentEnterpriseIdentityMaintenanceParams{
				ID:                   identity.ID,
				ExpectedTokenVersion: identity.TokenVersion,
			},
		); err != nil {
			slog.Warn("enterprise identity maintenance timestamp update failed",
				"agent_id", util.UUIDToString(identity.AgentID),
				"error", err,
			)
		}
	}
	return nil
}

func (s *EnterpriseIdentityService) rotateAuthXToken(
	ctx context.Context,
	identity db.AgentEnterpriseIdentity,
) (db.AgentEnterpriseIdentity, EnterpriseOIDCToken, error) {
	refreshToken, err := s.Secrets.Open(identity.AuthxRefreshTokenEncrypted)
	if err != nil {
		return db.AgentEnterpriseIdentity{}, EnterpriseOIDCToken{}, errors.New("decrypt AuthX refresh token")
	}
	defer clear(refreshToken)
	refreshed, err := s.AuthX.Renew(ctx, string(refreshToken))
	if err != nil {
		return db.AgentEnterpriseIdentity{}, EnterpriseOIDCToken{}, err
	}
	sealedRefresh, err := s.Secrets.Seal([]byte(refreshed.RefreshToken))
	if err != nil {
		return db.AgentEnterpriseIdentity{}, EnterpriseOIDCToken{}, errors.New("encrypt renewed AuthX refresh token")
	}
	updated, err := s.Store.CompareAndSwapAgentEnterpriseIdentityToken(ctx, db.CompareAndSwapAgentEnterpriseIdentityTokenParams{
		AuthxRefreshTokenEncrypted: sealedRefresh,
		AuthxRefreshExpiresAt:      pgtype.Timestamptz{Time: refreshed.RefreshExpiresAt, Valid: true},
		ID:                         identity.ID,
		ExpectedTokenVersion:       identity.TokenVersion,
	})
	if err != nil {
		return db.AgentEnterpriseIdentity{}, EnterpriseOIDCToken{}, fmt.Errorf("rotate AuthX refresh token: %w", err)
	}
	return updated, refreshed, nil
}

func (s *EnterpriseIdentityService) Revoke(
	ctx context.Context,
	workspaceID pgtype.UUID,
	agentID pgtype.UUID,
) error {
	current, err := s.Store.GetAgentEnterpriseIdentity(ctx, db.GetAgentEnterpriseIdentityParams{
		WorkspaceID: workspaceID,
		AgentID:     agentID,
	})
	if err != nil {
		return err
	}
	operatorSPIFFEID, err := s.operatorSPIFFEID(current.RawEmpID)
	if err != nil {
		return err
	}
	revoked, err := s.Store.RevokeAgentEnterpriseIdentity(ctx, db.RevokeAgentEnterpriseIdentityParams{
		WorkspaceID: workspaceID,
		AgentID:     agentID,
	})
	if err != nil {
		return err
	}
	var cleanupErr error
	cleanupErr = errors.Join(cleanupErr, s.retireActiveSandboxes(ctx, workspaceID, agentID, current))
	if current.BucAnchorSandboxID.Valid {
		cleanupErr = errors.Join(cleanupErr, s.Anchor.Delete(ctx, current.BucAnchorSandboxID.String))
	}
	cleanupErr = errors.Join(cleanupErr, s.Idem.DeleteAgent(ctx, revoked.AipID, operatorSPIFFEID))
	if cleanupErr != nil {
		return fmt.Errorf("enterprise identity was revoked but remote cleanup failed: %w", cleanupErr)
	}
	return nil
}

func (s *EnterpriseIdentityService) retireActiveSandboxes(
	ctx context.Context,
	workspaceID pgtype.UUID,
	agentID pgtype.UUID,
	identity db.AgentEnterpriseIdentity,
) error {
	fingerprint := enterpriseIdentityFingerprint(identity)
	sessions, err := s.Store.ListActiveCloudSandboxSessionsByAgentIdentity(
		ctx,
		db.ListActiveCloudSandboxSessionsByAgentIdentityParams{
			WorkspaceID:         workspaceID,
			AgentID:             agentID,
			IdentityFingerprint: fingerprint,
		},
	)
	if err != nil {
		return fmt.Errorf("list active ASB sessions for inactive identity: %w", err)
	}
	var cleanupErr error
	for _, session := range sessions {
		if err := s.Store.MarkCloudSandboxSessionStale(ctx, db.MarkCloudSandboxSessionStaleParams{
			RuntimeID:           session.RuntimeID,
			ScopeType:           session.ScopeType,
			ScopeID:             session.ScopeID,
			SandboxID:           session.SandboxID,
			SandboxBackend:      string(SandboxBackendASB),
			IdentityFingerprint: fingerprint,
		}); err != nil {
			cleanupErr = errors.Join(
				cleanupErr,
				fmt.Errorf("mark inactive ASB sandbox %s stale: %w", session.SandboxID, err),
			)
		}
		if err := s.Anchor.Delete(ctx, session.SandboxID); err != nil {
			cleanupErr = errors.Join(
				cleanupErr,
				fmt.Errorf("delete inactive ASB sandbox %s: %w", session.SandboxID, err),
			)
		}
	}
	return cleanupErr
}

func (s *EnterpriseIdentityService) markNeedsReauth(ctx context.Context, identity db.AgentEnterpriseIdentity) {
	affected, err := s.Store.MarkAgentEnterpriseIdentityNeedsReauth(ctx, db.MarkAgentEnterpriseIdentityNeedsReauthParams{
		ID:                   identity.ID,
		ExpectedTokenVersion: identity.TokenVersion,
	})
	if err != nil || affected == 0 {
		return
	}
	if err := s.retireActiveSandboxes(ctx, identity.WorkspaceID, identity.AgentID, identity); err != nil {
		slog.Warn("failed to retire sandboxes for enterprise identity requiring reauthorization",
			"agent_id", util.UUIDToString(identity.AgentID),
			"error", err,
		)
	}
	if identity.BucAnchorSandboxID.Valid {
		if err := s.Anchor.Delete(ctx, identity.BucAnchorSandboxID.String); err != nil {
			slog.Warn("failed to delete enterprise identity anchor requiring reauthorization",
				"agent_id", util.UUIDToString(identity.AgentID),
				"sandbox_id", identity.BucAnchorSandboxID.String,
				"error", err,
			)
		}
	}
}

func (s *EnterpriseIdentityService) agentSPIFFEID(agentID pgtype.UUID) (string, error) {
	return idemapi.GenerateSpiffeIdUri(&idemapi.IdentitySpec{
		TrustDomainName: s.Config.AgentTrustDomain,
		WorkloadPathHeader: idemapi.WorkloadPathField{
			Name:  "ns",
			Value: s.Config.AgentNamespace,
		},
		WorkloadPathPayloads: []idemapi.WorkloadPathField{{
			Name:  "agents",
			Value: util.UUIDToString(agentID),
		}},
	})
}

func (s *EnterpriseIdentityService) operatorSPIFFEID(employeeID string) (string, error) {
	if !enterpriseEmployeeIDPattern.MatchString(employeeID) {
		return "", errors.New("enterprise identity employee ID is invalid")
	}
	return idemapi.GenerateSpiffeIdUri(&idemapi.IdentitySpec{
		TrustDomainName: s.Config.OperatorTrustDomain,
		WorkloadPathHeader: idemapi.WorkloadPathField{
			Name:  "ns",
			Value: "buc",
		},
		WorkloadPathPayloads: []idemapi.WorkloadPathField{{
			Name:  "employees",
			Value: employeeID,
		}},
	})
}

func validateEnterpriseRedirectPath(path string) error {
	if !strings.HasPrefix(path, "/") ||
		strings.HasPrefix(path, "//") ||
		strings.ContainsAny(path, "\r\n") {
		return errors.New("enterprise identity redirect path is invalid")
	}
	return nil
}

func randomEnterpriseToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func sha256Bytes(value string) []byte {
	digest := sha256.Sum256([]byte(value))
	return digest[:]
}

func enterpriseIdentityFingerprint(identity db.AgentEnterpriseIdentity) string {
	hasher := sha256.New()
	anchorSandboxID := ""
	if identity.BucAnchorSandboxID.Valid {
		anchorSandboxID = identity.BucAnchorSandboxID.String
	}
	for _, value := range []string{
		util.UUIDToString(identity.WorkspaceID),
		util.UUIDToString(identity.AgentID),
		identity.RawEmpID,
		identity.AgentSpiffeID,
		identity.BucAgentID,
		anchorSandboxID,
	} {
		_, _ = hasher.Write([]byte(value))
		_, _ = hasher.Write([]byte{0})
	}
	return fmt.Sprintf("%x", hasher.Sum(nil))
}

type ASBIdentityAnchorManager struct {
	Client *ASBClient
	Config ASBConfig
}

func (m *ASBIdentityAnchorManager) Create(
	ctx context.Context,
	workspaceID pgtype.UUID,
	agentID pgtype.UUID,
	employeeID string,
	tokens BUCIdentityTokens,
) (string, error) {
	if m == nil || m.Client == nil {
		return "", errors.New("ASB identity anchor manager is unavailable")
	}
	if m.Config.IdentityAnchorTimeout < time.Duration(asbMinCreateTimeout)*time.Second ||
		m.Config.IdentityAnchorTimeout > asbMaxRenewalDuration ||
		!enterpriseOCIDigestPattern.MatchString(m.Config.IdentityAnchorImageRef) {
		return "", errors.New("ASB identity anchor configuration is invalid")
	}
	createTimeout := min(m.Config.IdentityAnchorTimeout, time.Duration(asbMaxCreateTimeout)*time.Second)
	timeoutSeconds := int(createTimeout / time.Second)
	sandbox, err := m.Client.CreateSandbox(ctx, ASBCreateSandboxInput{
		ImageURI:       m.Config.IdentityAnchorImageRef,
		TimeoutSeconds: timeoutSeconds,
		ResourceCPU:    m.Config.ResourceCPU,
		ResourceMemory: m.Config.ResourceMemory,
		Entrypoint:     []string{"sleep infinity"},
		Metadata: map[string]string{
			"multica.identity_anchor": "true",
			"multica.workspace_id":    util.UUIDToString(workspaceID),
			"multica.agent_id":        util.UUIDToString(agentID),
		},
		Extensions: map[string]string{
			"wireguard.lazyAuth": "true",
		},
	})
	if err != nil {
		return "", fmt.Errorf("create ASB identity anchor: %w", err)
	}
	keep := false
	defer func() {
		if !keep {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			_ = m.Client.DeleteSandbox(cleanupCtx, sandbox.ID)
		}
	}()
	if err := waitForASBSandboxRunning(ctx, m.Client, sandbox.ID, m.Config.ReadyTimeout); err != nil {
		return "", err
	}
	if err := m.Client.AttachBUCIdentity(ctx, sandbox.ID, ASBBUCIdentityGrant{
		EmployeeID:           employeeID,
		BUCAccessToken:       tokens.AccessToken,
		BUCRefreshToken:      tokens.RefreshToken,
		BUCIDToken:           tokens.IDToken,
		WireGuardCredentials: m.Config.WireGuardCredentials,
	}, true); err != nil {
		return "", fmt.Errorf("attach BUC identity to ASB anchor: %w", err)
	}
	if err := probeASBBUCIdentity(ctx, m.Client, sandbox.ID, m.Config.IdentityProbeTimeout); err != nil {
		return "", err
	}
	if m.Config.IdentityAnchorTimeout > createTimeout {
		if err := m.Client.RenewSandbox(ctx, sandbox.ID, time.Now().Add(m.Config.IdentityAnchorTimeout)); err != nil {
			return "", fmt.Errorf("renew ASB identity anchor after BUC attachment: %w", err)
		}
	}
	keep = true
	return sandbox.ID, nil
}

func (m *ASBIdentityAnchorManager) EnsureAvailable(ctx context.Context, sandboxID string) error {
	sandbox, err := m.Client.GetSandbox(ctx, sandboxID)
	if err != nil {
		var httpErr *ASBHTTPError
		if errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusNotFound {
			return ErrEnterpriseIdentityAnchorUnavailable
		}
		return fmt.Errorf("check ASB identity anchor: %w", err)
	}
	if !strings.EqualFold(strings.TrimSpace(sandbox.Status.State), "running") {
		return ErrEnterpriseIdentityAnchorUnavailable
	}
	if err := m.Client.RenewSandbox(ctx, sandboxID, time.Now().Add(m.Config.IdentityAnchorTimeout)); err != nil {
		return fmt.Errorf("renew ASB identity anchor: %w", err)
	}
	return nil
}

func (m *ASBIdentityAnchorManager) Delete(ctx context.Context, sandboxID string) error {
	if strings.TrimSpace(sandboxID) == "" {
		return nil
	}
	err := m.Client.DeleteSandbox(ctx, sandboxID)
	var httpErr *ASBHTTPError
	if errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusNotFound {
		return nil
	}
	return err
}

func waitForASBSandboxRunning(
	ctx context.Context,
	client *ASBClient,
	sandboxID string,
	timeout time.Duration,
) error {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		sandbox, err := client.GetSandbox(ctx, sandboxID)
		if err == nil {
			switch strings.ToLower(strings.TrimSpace(sandbox.Status.State)) {
			case "running":
				return nil
			case "failed", "terminated", "error":
				return fmt.Errorf("ASB identity anchor entered terminal state %q", sandbox.Status.State)
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			if err != nil {
				return fmt.Errorf("ASB identity anchor was not running within %s: %w", timeout, err)
			}
			return fmt.Errorf("ASB identity anchor was not running within %s", timeout)
		case <-ticker.C:
		}
	}
}

func probeASBBUCIdentity(
	ctx context.Context,
	client *ASBClient,
	sandboxID string,
	timeout time.Duration,
) error {
	endpoint, err := client.GetEndpoint(ctx, sandboxID, asbExecPort)
	if err != nil {
		return fmt.Errorf("resolve ASB identity anchor command endpoint: %w", err)
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(defaultEnterpriseIdentityProbePeriod)
	defer ticker.Stop()
	for {
		result, execErr := client.Exec(ctx, endpoint, ASBExecInput{
			Command: "curl -fsS --max-time 10 -X POST https://login.alibaba-inc.com/rpc/cli/v1/get_zt_identity.json >/dev/null",
			CWD:     "/",
			Timeout: 15 * time.Second,
			Envs: map[string]string{
				"HOME":    asbRunnerHome,
				"USER":    "user",
				"LOGNAME": "user",
			},
		})
		if execErr == nil && result.ExitCode != nil && *result.ExitCode == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return errors.New("ASB BUC identity probe did not become ready")
		case <-ticker.C:
		}
	}
}
