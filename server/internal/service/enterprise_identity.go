package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
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
	defaultEnterpriseMaintenanceInterval = 5 * time.Minute
	defaultEnterpriseRefreshBefore       = 24 * time.Hour
	// A short-lived inherited sandbox requests a WireGuard ticket immediately
	// on startup. The zero-trust service validates the cached BUC ID token at
	// that point and rotates its refresh-token family when renewal is due. Run
	// this well inside the server-side credential cache lifetime so inactive
	// Agents are refreshed without retaining a running sandbox.
	defaultEnterpriseSourceRefreshInterval = 12 * time.Hour
	defaultEnterpriseMaintenanceBatch      = int32(50)
	enterpriseIdentityTokenLockClass       = int32(0x4549544b) // "EITK"
)

var (
	ErrEnterpriseIdentityDisabled         = errors.New("enterprise identity is not configured")
	ErrEnterpriseIdentityNeedsReauth      = errors.New("enterprise identity requires employee reauthorization")
	ErrEnterpriseIdentityEmployeeConflict = errors.New("enterprise identity must be revoked before binding another employee")

	enterpriseEmployeeIDPattern = regexp.MustCompile(`^[1-9][0-9]*$`)
	enterprisePathPartPattern   = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
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

func (c EnterpriseIdentityConfig) Validate(_ ASBConfig) error {
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
	return nil
}

type enterpriseIdentityStore interface {
	CreateAgentEnterpriseIdentityAttempt(context.Context, db.CreateAgentEnterpriseIdentityAttemptParams) (db.AgentEnterpriseIdentityAttempt, error)
	ConsumeAgentEnterpriseIdentityAttempt(context.Context, []byte) (db.AgentEnterpriseIdentityAttempt, error)
	GetAgent(context.Context, pgtype.UUID) (db.Agent, error)
	GetAgentEnterpriseIdentity(context.Context, db.GetAgentEnterpriseIdentityParams) (db.AgentEnterpriseIdentity, error)
	GetActiveAgentEnterpriseIdentity(context.Context, db.GetActiveAgentEnterpriseIdentityParams) (db.AgentEnterpriseIdentity, error)
	GetReusableAgentEnterpriseIdentitySource(context.Context, db.GetReusableAgentEnterpriseIdentitySourceParams) (db.AgentEnterpriseIdentity, error)
	ListActiveAgentEnterpriseIdentitySourceReferences(context.Context, db.ListActiveAgentEnterpriseIdentitySourceReferencesParams) ([]db.AgentEnterpriseIdentity, error)
	CountActiveAgentEnterpriseIdentitySourceReferences(context.Context, db.CountActiveAgentEnterpriseIdentitySourceReferencesParams) (int64, error)
	UpsertAgentEnterpriseIdentity(context.Context, db.UpsertAgentEnterpriseIdentityParams) (db.AgentEnterpriseIdentity, error)
	CompareAndSwapAgentEnterpriseIdentitySource(context.Context, db.CompareAndSwapAgentEnterpriseIdentitySourceParams) (db.AgentEnterpriseIdentity, error)
	TouchActiveAgentEnterpriseIdentitySourceReferences(context.Context, db.TouchActiveAgentEnterpriseIdentitySourceReferencesParams) (int64, error)
	CompareAndSwapAgentEnterpriseIdentityToken(context.Context, db.CompareAndSwapAgentEnterpriseIdentityTokenParams) (db.AgentEnterpriseIdentity, error)
	ListAgentEnterpriseIdentitiesForMaintenance(context.Context, db.ListAgentEnterpriseIdentitiesForMaintenanceParams) ([]db.AgentEnterpriseIdentity, error)
	MarkAgentEnterpriseIdentityNeedsReauth(context.Context, db.MarkAgentEnterpriseIdentityNeedsReauthParams) (int64, error)
	MarkAgentEnterpriseIdentitiesNeedsReauthBySource(context.Context, db.MarkAgentEnterpriseIdentitiesNeedsReauthBySourceParams) ([]db.AgentEnterpriseIdentity, error)
	RevokeAgentEnterpriseIdentity(context.Context, db.RevokeAgentEnterpriseIdentityParams) (db.AgentEnterpriseIdentity, error)
	ListActiveCloudSandboxSessionsByAgentIdentity(context.Context, db.ListActiveCloudSandboxSessionsByAgentIdentityParams) ([]db.FcE2bSandboxSession, error)
	MarkCloudSandboxSessionStale(context.Context, db.MarkCloudSandboxSessionStaleParams) error
}

type EnterpriseIdentitySandboxController interface {
	DeleteRuntimeSandbox(context.Context, pgtype.UUID, string) error
}

type EnterpriseIdentityASBTenantResolver interface {
	RuntimeCredentialScope(context.Context, pgtype.UUID) (ASBTenantCredentialScope, error)
}

type EnterpriseIdentityTokenRotationLocker interface {
	Lock(context.Context, pgtype.UUID) (func(), error)
}

type postgresEnterpriseIdentityTokenRotationLocker struct {
	pool *pgxpool.Pool
}

func newPostgresEnterpriseIdentityTokenRotationLocker(
	pool *pgxpool.Pool,
) *postgresEnterpriseIdentityTokenRotationLocker {
	return &postgresEnterpriseIdentityTokenRotationLocker{pool: pool}
}

func (l *postgresEnterpriseIdentityTokenRotationLocker) Lock(
	ctx context.Context,
	identityID pgtype.UUID,
) (func(), error) {
	if l == nil || l.pool == nil || !identityID.Valid {
		return nil, errors.New("enterprise identity token rotation lock is unavailable")
	}
	conn, err := l.pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire connection for enterprise identity token rotation lock: %w", err)
	}
	key := enterpriseIdentityTokenRotationLockKey(identityID)
	if _, err := conn.Exec(
		ctx,
		"SELECT pg_advisory_lock($1, $2)",
		enterpriseIdentityTokenLockClass,
		key,
	); err != nil {
		conn.Release()
		return nil, fmt.Errorf("acquire enterprise identity token rotation lock: %w", err)
	}
	return func() {
		unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var unlocked bool
		err := conn.QueryRow(
			unlockCtx,
			"SELECT pg_advisory_unlock($1, $2)",
			enterpriseIdentityTokenLockClass,
			key,
		).Scan(&unlocked)
		if err != nil || !unlocked {
			slog.Error(
				"enterprise identity token rotation lock release failed",
				"error", err,
				"unlocked", unlocked,
			)
			_ = conn.Conn().Close(unlockCtx)
		}
		conn.Release()
	}, nil
}

func enterpriseIdentityTokenRotationLockKey(identityID pgtype.UUID) int32 {
	hasher := fnv.New32a()
	_, _ = hasher.Write(identityID.Bytes[:])
	return int32(hasher.Sum32())
}

type EnterpriseIdentityService struct {
	Store          enterpriseIdentityStore
	Config         EnterpriseIdentityConfig
	ConfigProvider func() EnterpriseIdentityConfig
	BUC            BUCOAuthClient
	AuthX          EnterpriseAuthX
	Idem           EnterpriseIdem
	Sandboxes      EnterpriseIdentitySandboxController
	Tenants        EnterpriseIdentityASBTenantResolver
	Source         EnterpriseIdentitySource
	Secrets        *secretbox.Box
	TokenLock      EnterpriseIdentityTokenRotationLocker
	SourceLock     EnterpriseIdentitySourceLocker
	RuntimeLock    EnterpriseIdentityRuntimeLocker
	Now            func() time.Time
	Authorize      *url.URL

	MaintenanceInterval   time.Duration
	RefreshBefore         time.Duration
	SourceRefreshInterval time.Duration
	MaintenanceBatch      int32
}

func (s *EnterpriseIdentityService) currentConfig() EnterpriseIdentityConfig {
	if s != nil && s.ConfigProvider != nil {
		return s.ConfigProvider()
	}
	if s == nil {
		return EnterpriseIdentityConfig{}
	}
	return s.Config
}

func NewEnterpriseIdentityService(
	store enterpriseIdentityStore,
	config EnterpriseIdentityConfig,
	buc BUCOAuthClient,
	authX EnterpriseAuthX,
	idem EnterpriseIdem,
	sandboxes EnterpriseIdentitySandboxController,
	tenants EnterpriseIdentityASBTenantResolver,
	source EnterpriseIdentitySource,
	secrets *secretbox.Box,
	tokenLock EnterpriseIdentityTokenRotationLocker,
	sourceLock EnterpriseIdentitySourceLocker,
	runtimeLock EnterpriseIdentityRuntimeLocker,
) (*EnterpriseIdentityService, error) {
	if store == nil ||
		buc == nil ||
		authX == nil ||
		idem == nil ||
		sandboxes == nil ||
		tenants == nil ||
		source == nil ||
		secrets == nil ||
		tokenLock == nil ||
		sourceLock == nil ||
		runtimeLock == nil {
		return nil, errors.New("enterprise identity service dependencies are incomplete")
	}
	authorizeURL, err := url.Parse(config.BUCAuthorizeURL)
	if err != nil {
		return nil, errors.New("parse BUC authorize URL")
	}
	return &EnterpriseIdentityService{
		Store:       store,
		Config:      config,
		BUC:         buc,
		AuthX:       authX,
		Idem:        idem,
		Sandboxes:   sandboxes,
		Tenants:     tenants,
		Source:      source,
		Secrets:     secrets,
		TokenLock:   tokenLock,
		SourceLock:  sourceLock,
		RuntimeLock: runtimeLock,
		Now:         time.Now,
		Authorize:   authorizeURL,

		MaintenanceInterval:   defaultEnterpriseMaintenanceInterval,
		RefreshBefore:         defaultEnterpriseRefreshBefore,
		SourceRefreshInterval: defaultEnterpriseSourceRefreshInterval,
		MaintenanceBatch:      defaultEnterpriseMaintenanceBatch,
	}, nil
}

type StartEnterpriseIdentityBindingInput struct {
	WorkspaceID  pgtype.UUID
	AgentID      pgtype.UUID
	ActorUserID  pgtype.UUID
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
	config := s.currentConfig()
	if err := validateEnterpriseRedirectPath(input.RedirectPath); err != nil {
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
	expiresAt := now.Add(config.OAuthAttemptTTL)
	if _, err := s.Store.CreateAgentEnterpriseIdentityAttempt(ctx, db.CreateAgentEnterpriseIdentityAttemptParams{
		WorkspaceID:       input.WorkspaceID,
		AgentID:           input.AgentID,
		ActorUserID:       input.ActorUserID,
		RequestedRawEmpID: pgtype.Text{},
		StateHash:         sha256Bytes(state),
		NonceHash:         sha256Bytes(nonce),
		RedirectPath:      input.RedirectPath,
		ExpiresAt:         pgtype.Timestamptz{Time: expiresAt, Valid: true},
	}); err != nil {
		return StartEnterpriseIdentityBindingResult{}, fmt.Errorf("create enterprise identity binding attempt: %w", err)
	}
	authorizeURL, err := url.Parse(config.BUCAuthorizeURL)
	if err != nil {
		return StartEnterpriseIdentityBindingResult{}, errors.New("parse BUC authorize URL")
	}
	authorize := *authorizeURL
	query := authorize.Query()
	scope := "profile openid employee"
	if len(config.BUCAuthorizeApps) > 0 {
		scope += " user_authorize"
		query.Set("authorize_app", strings.Join(config.BUCAuthorizeApps, ","))
	}
	query.Set("scope", scope)
	query.Set("prompt", "consent")
	query.Set("response_type", "code")
	query.Set("client_id", config.BUCClientID)
	query.Set("redirect_uri", config.BUCRedirectURL)
	query.Set("agent_id", config.BUCAgentID)
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

// PreparedEnterpriseIdentityBinding is the consumed, one-time OAuth attempt
// used by the browser callback. Preparing is intentionally fast so the
// handler can finish the navigation response before ASB source creation starts.
// The database row stays private to this package; callers only receive the
// coordinates required to render and poll the progress page.
type PreparedEnterpriseIdentityBinding struct {
	WorkspaceID  pgtype.UUID
	AgentID      pgtype.UUID
	RedirectPath string

	attempt db.AgentEnterpriseIdentityAttempt
	started time.Time
}

func (s *EnterpriseIdentityService) PrepareBindingCompletion(
	ctx context.Context,
	state string,
) (PreparedEnterpriseIdentityBinding, error) {
	bindingStarted := time.Now()
	state = strings.TrimSpace(state)
	if state == "" {
		err := errors.New("enterprise identity OAuth callback is incomplete")
		logEnterpriseIdentityBindingStageFailure("validate_oauth_callback", bindingStarted, err)
		return PreparedEnterpriseIdentityBinding{}, err
	}
	stageStarted := time.Now()
	attempt, err := s.Store.ConsumeAgentEnterpriseIdentityAttempt(ctx, sha256Bytes(state))
	if err != nil {
		logEnterpriseIdentityBindingStageFailure("consume_oauth_attempt", stageStarted, err)
		if errors.Is(err, pgx.ErrNoRows) {
			return PreparedEnterpriseIdentityBinding{}, errors.New("enterprise identity OAuth attempt is invalid, expired, or already used")
		}
		return PreparedEnterpriseIdentityBinding{}, fmt.Errorf("consume enterprise identity OAuth attempt: %w", err)
	}
	bindingAttrs := []any{
		"workspace_id", util.UUIDToString(attempt.WorkspaceID),
		"agent_id", util.UUIDToString(attempt.AgentID),
	}
	logEnterpriseIdentityBindingStageSuccess("consume_oauth_attempt", stageStarted, bindingAttrs...)
	return PreparedEnterpriseIdentityBinding{
		WorkspaceID:  attempt.WorkspaceID,
		AgentID:      attempt.AgentID,
		RedirectPath: attempt.RedirectPath,
		attempt:      attempt,
		started:      bindingStarted,
	}, nil
}

func (s *EnterpriseIdentityService) CompleteBinding(
	ctx context.Context,
	state string,
	code string,
) (CompleteEnterpriseIdentityBindingResult, error) {
	prepared, err := s.PrepareBindingCompletion(ctx, state)
	if err != nil {
		return CompleteEnterpriseIdentityBindingResult{}, err
	}
	return s.CompletePreparedBinding(ctx, prepared, code)
}

func (s *EnterpriseIdentityService) CompletePreparedBinding(
	ctx context.Context,
	prepared PreparedEnterpriseIdentityBinding,
	code string,
) (CompleteEnterpriseIdentityBindingResult, error) {
	config := s.currentConfig()
	bindingStarted := prepared.started
	if bindingStarted.IsZero() {
		bindingStarted = time.Now()
	}
	code = strings.TrimSpace(code)
	if code == "" {
		err := errors.New("enterprise identity OAuth callback is incomplete")
		logEnterpriseIdentityBindingStageFailure("validate_oauth_callback", bindingStarted, err)
		return CompleteEnterpriseIdentityBindingResult{}, err
	}
	attempt := prepared.attempt
	bindingAttrs := []any{
		"workspace_id", util.UUIDToString(attempt.WorkspaceID),
		"agent_id", util.UUIDToString(attempt.AgentID),
	}

	stageStarted := time.Now()
	bucTokens, err := s.BUC.ExchangeCode(ctx, code)
	if err != nil {
		logEnterpriseIdentityBindingStageFailure("exchange_buc_code", stageStarted, err, bindingAttrs...)
		return CompleteEnterpriseIdentityBindingResult{}, err
	}
	logEnterpriseIdentityBindingStageSuccess("exchange_buc_code", stageStarted, bindingAttrs...)

	stageStarted = time.Now()
	claims, err := s.BUC.VerifyIDToken(
		ctx,
		bucTokens.IDToken,
		attempt.NonceHash,
		s.Now(),
	)
	if err != nil {
		logEnterpriseIdentityBindingStageFailure("verify_buc_id_token", stageStarted, err, bindingAttrs...)
		return CompleteEnterpriseIdentityBindingResult{}, err
	}
	logEnterpriseIdentityBindingStageSuccess("verify_buc_id_token", stageStarted, bindingAttrs...)

	employeeID := claims.EmployeeID
	stageStarted = time.Now()
	agent, err := s.Store.GetAgent(ctx, attempt.AgentID)
	if err != nil || agent.WorkspaceID != attempt.WorkspaceID {
		targetErr := errors.New("enterprise identity target agent no longer exists")
		if err != nil {
			targetErr = fmt.Errorf("load enterprise identity target agent: %w", err)
		}
		logEnterpriseIdentityBindingStageFailure("load_target_agent", stageStarted, targetErr, bindingAttrs...)
		return CompleteEnterpriseIdentityBindingResult{}, targetErr
	}
	logEnterpriseIdentityBindingStageSuccess("load_target_agent", stageStarted, bindingAttrs...)

	stageStarted = time.Now()
	oldIdentity, oldIdentityFound, err := s.loadReplaceableIdentity(
		ctx,
		attempt.WorkspaceID,
		attempt.AgentID,
		employeeID,
	)
	if err != nil {
		logEnterpriseIdentityBindingStageFailure("validate_existing_binding", stageStarted, err, bindingAttrs...)
		return CompleteEnterpriseIdentityBindingResult{}, err
	}
	logEnterpriseIdentityBindingStageSuccess("validate_existing_binding", stageStarted, bindingAttrs...)

	stageStarted = time.Now()
	ssoTicket, err := s.BUC.GenerateSSOTicket(ctx, bucTokens.AccessToken)
	if err != nil {
		logEnterpriseIdentityBindingStageFailure("generate_buc_sso_ticket", stageStarted, err, bindingAttrs...)
		return CompleteEnterpriseIdentityBindingResult{}, err
	}
	logEnterpriseIdentityBindingStageSuccess(
		"generate_buc_sso_ticket",
		stageStarted,
		append(bindingAttrs, "ticket_ttl_seconds", enterpriseBUCSSOTicketExpiresIn)...,
	)

	stageStarted = time.Now()
	authXToken, err := s.AuthX.IssueFromSSOTicket(ctx, ssoTicket)
	if err != nil {
		logEnterpriseIdentityBindingStageFailure("issue_authx_oidc_token", stageStarted, err, bindingAttrs...)
		return CompleteEnterpriseIdentityBindingResult{}, err
	}
	logEnterpriseIdentityBindingStageSuccess(
		"issue_authx_oidc_token",
		stageStarted,
		append(
			bindingAttrs,
			"id_token_ttl_seconds", enterpriseIdentityTTLSeconds(authXToken.ExpiresAt, s.Now()),
			"refresh_token_ttl_seconds", enterpriseIdentityTTLSeconds(authXToken.RefreshExpiresAt, s.Now()),
		)...,
	)

	stageStarted = time.Now()
	agentSPIFFEID, err := s.agentSPIFFEID(attempt.AgentID)
	if err != nil {
		logEnterpriseIdentityBindingStageFailure("derive_spiffe_identity", stageStarted, err, bindingAttrs...)
		return CompleteEnterpriseIdentityBindingResult{}, err
	}
	operatorSPIFFEID, err := s.operatorSPIFFEID(employeeID)
	if err != nil {
		logEnterpriseIdentityBindingStageFailure("derive_spiffe_identity", stageStarted, err, bindingAttrs...)
		return CompleteEnterpriseIdentityBindingResult{}, err
	}
	logEnterpriseIdentityBindingStageSuccess("derive_spiffe_identity", stageStarted, bindingAttrs...)

	model := ""
	if agent.Model.Valid {
		model = agent.Model.String
	}
	stageStarted = time.Now()
	aipID, aipCreated, err := s.Idem.EnsureAgent(ctx, EnterpriseAgentRegistration{
		SPIFFEID:    agentSPIFFEID,
		OperatorID:  operatorSPIFFEID,
		EmployeeID:  employeeID,
		DisplayName: agent.Name,
		AgentModel:  model,
		BindingAt:   s.Now(),
	})
	if err != nil {
		logEnterpriseIdentityBindingStageFailure("ensure_idem_agent", stageStarted, err, bindingAttrs...)
		return CompleteEnterpriseIdentityBindingResult{}, err
	}
	logEnterpriseIdentityBindingStageSuccess(
		"ensure_idem_agent",
		stageStarted,
		append(bindingAttrs, "idem_agent_created", aipCreated)...,
	)
	cleanupAIP := func() {
		if aipCreated {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			_ = s.Idem.DeleteAgent(cleanupCtx, aipID, operatorSPIFFEID)
		}
	}
	stageStarted = time.Now()
	if _, err := s.Idem.IssueAIT(ctx, authXToken.IDToken, agentSPIFFEID, operatorSPIFFEID, config.AITTTL); err != nil {
		logEnterpriseIdentityBindingStageFailure("issue_idem_ait", stageStarted, err, bindingAttrs...)
		cleanupAIP()
		return CompleteEnterpriseIdentityBindingResult{}, err
	}
	logEnterpriseIdentityBindingStageSuccess("issue_idem_ait", stageStarted, bindingAttrs...)

	stageStarted = time.Now()
	if !agent.RuntimeID.Valid {
		err := errors.New("enterprise identity target Agent has no Runtime")
		logEnterpriseIdentityBindingStageFailure("validate_asb_runtime", stageStarted, err, bindingAttrs...)
		cleanupAIP()
		return CompleteEnterpriseIdentityBindingResult{}, err
	}
	tenantScope, unlockRuntime, err := s.lockASBTenantCredentialScope(
		ctx,
		agent.RuntimeID,
	)
	if err != nil {
		logEnterpriseIdentityBindingStageFailure("lock_asb_runtime", stageStarted, err, bindingAttrs...)
		cleanupAIP()
		return CompleteEnterpriseIdentityBindingResult{}, err
	}
	defer func() {
		if unlockRuntime != nil {
			unlockRuntime()
		}
	}()
	logEnterpriseIdentityBindingStageSuccess("lock_asb_runtime", stageStarted, bindingAttrs...)
	stageStarted = time.Now()
	sourceKey := enterpriseIdentitySourceKey{
		WorkspaceID:   attempt.WorkspaceID,
		BoundBy:       attempt.ActorUserID,
		RuntimeID:     agent.RuntimeID,
		TenantLockKey: tenantScope.LockKey,
		RawEmployeeID: employeeID,
		BUCAgentID:    config.BUCAgentID,
	}
	unlockSource, err := s.SourceLock.Lock(ctx, sourceKey)
	if err != nil {
		logEnterpriseIdentityBindingStageFailure(
			"lock_asb_identity_source",
			stageStarted,
			err,
			bindingAttrs...,
		)
		cleanupAIP()
		return CompleteEnterpriseIdentityBindingResult{}, err
	}
	defer func() {
		if unlockSource != nil {
			unlockSource()
		}
	}()
	source, sourceCreated, err := s.reuseOrCreateIdentitySource(
		ctx,
		sourceKey,
		tenantScope.RuntimeIDs,
		bucTokens,
	)
	if err != nil {
		logEnterpriseIdentityBindingStageFailure(
			"establish_asb_identity_source",
			stageStarted,
			err,
			bindingAttrs...,
		)
		cleanupAIP()
		return CompleteEnterpriseIdentityBindingResult{}, err
	}
	logEnterpriseIdentityBindingStageSuccess(
		"establish_asb_identity_source",
		stageStarted,
		append(
			bindingAttrs,
			"source_sandbox_id", source.SandboxID,
			"source_reused", !sourceCreated,
		)...,
	)
	cleanupSource := func() {
		if !sourceCreated {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if cleanupErr := s.Source.Delete(
			cleanupCtx,
			source.RuntimeID,
			source.SandboxID,
		); cleanupErr != nil {
			slog.Warn(
				"failed to delete uncommitted ASB enterprise identity source",
				"agent_id", util.UUIDToString(attempt.AgentID),
				"sandbox_id", source.SandboxID,
				"error", cleanupErr,
			)
		}
	}

	stageStarted = time.Now()
	sealedRefresh, err := s.Secrets.Seal([]byte(authXToken.RefreshToken))
	if err != nil {
		logEnterpriseIdentityBindingStageFailure("encrypt_authx_refresh_token", stageStarted, err, bindingAttrs...)
		cleanupSource()
		cleanupAIP()
		return CompleteEnterpriseIdentityBindingResult{}, errors.New("encrypt AuthX refresh token")
	}
	logEnterpriseIdentityBindingStageSuccess("encrypt_platform_credentials", stageStarted, bindingAttrs...)

	stageStarted = time.Now()
	var unlockExistingIdentity func()
	defer func() {
		if unlockExistingIdentity != nil {
			unlockExistingIdentity()
		}
	}()
	if oldIdentityFound && oldIdentity.ID.Valid {
		unlockExistingIdentity, err = s.TokenLock.Lock(ctx, oldIdentity.ID)
		if err != nil {
			logEnterpriseIdentityBindingStageFailure(
				"lock_existing_binding",
				stageStarted,
				err,
				bindingAttrs...,
			)
			cleanupSource()
			cleanupAIP()
			return CompleteEnterpriseIdentityBindingResult{}, fmt.Errorf(
				"lock existing enterprise identity binding: %w",
				err,
			)
		}
		current, reloadErr := s.Store.GetAgentEnterpriseIdentity(
			ctx,
			db.GetAgentEnterpriseIdentityParams{
				WorkspaceID: attempt.WorkspaceID,
				AgentID:     attempt.AgentID,
			},
		)
		if reloadErr != nil {
			logEnterpriseIdentityBindingStageFailure(
				"reload_existing_binding",
				stageStarted,
				reloadErr,
				bindingAttrs...,
			)
			cleanupSource()
			cleanupAIP()
			return CompleteEnterpriseIdentityBindingResult{}, fmt.Errorf(
				"reload existing enterprise identity binding: %w",
				reloadErr,
			)
		}
		if current.ID != oldIdentity.ID ||
			(current.Status != "revoked" && current.RawEmpID != employeeID) {
			cleanupSource()
			cleanupAIP()
			return CompleteEnterpriseIdentityBindingResult{},
				ErrEnterpriseIdentityEmployeeConflict
		}
		oldIdentity = current
	}

	stageStarted = time.Now()
	identity, err := s.Store.UpsertAgentEnterpriseIdentity(ctx, db.UpsertAgentEnterpriseIdentityParams{
		WorkspaceID:                attempt.WorkspaceID,
		AgentID:                    attempt.AgentID,
		RawEmpID:                   employeeID,
		DisplayName:                strings.TrimSpace(claims.Name),
		BucAgentID:                 config.BUCAgentID,
		AgentSpiffeID:              agentSPIFFEID,
		AipID:                      aipID,
		BucIdentitySourceSandboxID: pgtype.Text{String: source.SandboxID, Valid: true},
		BucIdentitySourceRuntimeID: source.RuntimeID,
		AuthxRefreshTokenEncrypted: sealedRefresh,
		AuthxRefreshExpiresAt:      pgtype.Timestamptz{Time: authXToken.RefreshExpiresAt, Valid: true},
		BoundBy:                    attempt.ActorUserID,
	})
	if err != nil {
		logEnterpriseIdentityBindingStageFailure("persist_binding", stageStarted, err, bindingAttrs...)
		cleanupSource()
		cleanupAIP()
		return CompleteEnterpriseIdentityBindingResult{}, fmt.Errorf("persist enterprise identity binding: %w", err)
	}
	logEnterpriseIdentityBindingStageSuccess("persist_binding", stageStarted, bindingAttrs...)
	sourceCreated = false
	if unlockExistingIdentity != nil {
		unlockExistingIdentity()
		unlockExistingIdentity = nil
	}
	unlockSource()
	unlockSource = nil
	unlockRuntime()
	unlockRuntime = nil
	if oldIdentityFound {
		if err := s.retireActiveSandboxes(ctx, attempt.WorkspaceID, attempt.AgentID, oldIdentity); err != nil {
			slog.Warn("failed to retire sandboxes from replaced enterprise identity",
				"agent_id", util.UUIDToString(attempt.AgentID),
				"error", err,
			)
		}
		if oldIdentity.BucIdentitySourceSandboxID.Valid &&
			oldIdentity.BucIdentitySourceRuntimeID.Valid &&
			oldIdentity.BucIdentitySourceSandboxID.String != source.SandboxID {
			if err := s.deleteIdentitySourceIfUnreferenced(ctx, oldIdentity); err != nil {
				slog.Warn(
					"failed to release replaced ASB enterprise identity source",
					"agent_id", util.UUIDToString(attempt.AgentID),
					"sandbox_id", oldIdentity.BucIdentitySourceSandboxID.String,
					"error", err,
				)
			}
		}
	}
	slog.Info(
		"enterprise identity binding completed",
		append(
			bindingAttrs,
			"duration_ms", enterpriseIdentityDurationMilliseconds(bindingStarted),
			"credential_mode", "asb_shared_source",
		)...,
	)
	return CompleteEnterpriseIdentityBindingResult{
		Identity:     identity,
		RedirectPath: attempt.RedirectPath,
	}, nil
}

func logEnterpriseIdentityBindingStageSuccess(stage string, started time.Time, attributes ...any) {
	base := []any{
		"stage", stage,
		"duration_ms", enterpriseIdentityDurationMilliseconds(started),
	}
	slog.Info("enterprise identity binding stage completed", append(base, attributes...)...)
}

func logEnterpriseIdentityBindingStageFailure(
	stage string,
	started time.Time,
	err error,
	attributes ...any,
) {
	base := []any{
		"stage", stage,
		"duration_ms", enterpriseIdentityDurationMilliseconds(started),
		"error_class", enterpriseIdentityErrorClass(err),
		"error_type", fmt.Sprintf("%T", err),
	}
	var providerErr *enterpriseIdentityProviderError
	if errors.As(err, &providerErr) {
		base = append(
			base,
			"provider", providerErr.Provider,
			"operation", providerErr.Operation,
		)
		if providerErr.StatusCode > 0 {
			base = append(base, "http_status", providerErr.StatusCode)
		}
		if providerErr.Code != "" {
			base = append(base, "provider_error_code", providerErr.Code)
		}
	}
	slog.Warn("enterprise identity binding stage failed", append(base, attributes...)...)
}

func enterpriseIdentityErrorClass(err error) string {
	if err == nil {
		return "none"
	}
	var providerErr *enterpriseIdentityProviderError
	if errors.As(err, &providerErr) && providerErr.Class != "" {
		return providerErr.Class
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	case errors.Is(err, context.Canceled):
		return "context_canceled"
	case errors.Is(err, pgx.ErrNoRows):
		return "not_found"
	default:
		return "internal_error"
	}
}

func enterpriseIdentityDurationMilliseconds(started time.Time) int64 {
	duration := time.Since(started)
	if duration < 0 {
		return 0
	}
	return duration.Milliseconds()
}

func enterpriseIdentityTTLSeconds(expiresAt time.Time, now time.Time) int64 {
	remaining := expiresAt.Sub(now)
	if remaining <= 0 {
		return 0
	}
	return int64(remaining / time.Second)
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

func (s *EnterpriseIdentityService) reuseOrCreateIdentitySource(
	ctx context.Context,
	key enterpriseIdentitySourceKey,
	runtimeIDs []pgtype.UUID,
	tokens BUCIdentityTokens,
) (EnterpriseIdentitySourceAvailability, bool, error) {
	reusable, err := s.Store.GetReusableAgentEnterpriseIdentitySource(
		ctx,
		db.GetReusableAgentEnterpriseIdentitySourceParams{
			WorkspaceID: key.WorkspaceID,
			BoundBy:     key.BoundBy,
			RawEmpID:    key.RawEmployeeID,
			BucAgentID:  key.BUCAgentID,
			RuntimeIds:  runtimeIDs,
		},
	)
	if err == nil {
		sandboxID := strings.TrimSpace(reusable.BucIdentitySourceSandboxID.String)
		sourceRuntimeID := reusable.BucIdentitySourceRuntimeID
		prepareErr := s.Source.Prepare(
			ctx,
			sourceRuntimeID,
			sandboxID,
			key.RawEmployeeID,
			key.BUCAgentID,
		)
		if prepareErr == nil {
			prepareErr = s.Source.Park(ctx, sourceRuntimeID, sandboxID)
		}
		if prepareErr == nil {
			return EnterpriseIdentitySourceAvailability{
				SandboxID: sandboxID,
				RuntimeID: sourceRuntimeID,
			}, false, nil
		}
		if !errors.Is(prepareErr, ErrEnterpriseIdentityNeedsReauth) {
			return EnterpriseIdentitySourceAvailability{}, false, prepareErr
		}
		if err := s.markIdentitySourceNeedsReauthUnderLock(ctx, reusable); err != nil {
			return EnterpriseIdentitySourceAvailability{}, false, err
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return EnterpriseIdentitySourceAvailability{}, false, fmt.Errorf(
			"find reusable ASB enterprise identity source: %w",
			err,
		)
	}

	source, err := s.Source.Create(ctx, key, tokens)
	if err != nil {
		return EnterpriseIdentitySourceAvailability{}, false, err
	}
	return source, true, nil
}

func (s *EnterpriseIdentityService) deleteIdentitySourceIfUnreferenced(
	ctx context.Context,
	identity db.AgentEnterpriseIdentity,
) error {
	if !identity.BucIdentitySourceSandboxID.Valid ||
		strings.TrimSpace(identity.BucIdentitySourceSandboxID.String) == "" ||
		!identity.BucIdentitySourceRuntimeID.Valid {
		return nil
	}
	tenantScope, unlockRuntime, err := s.lockASBTenantCredentialScope(
		ctx,
		identity.BucIdentitySourceRuntimeID,
	)
	if err != nil {
		return err
	}
	defer unlockRuntime()
	key, err := enterpriseIdentitySourceKeyFromIdentity(identity, tenantScope.LockKey)
	if err != nil {
		return err
	}
	unlock, err := s.SourceLock.Lock(ctx, key)
	if err != nil {
		return err
	}
	defer unlock()
	return s.deleteIdentitySourceIfUnreferencedUnderLock(ctx, identity)
}

func (s *EnterpriseIdentityService) deleteIdentitySourceIfUnreferencedUnderLock(
	ctx context.Context,
	identity db.AgentEnterpriseIdentity,
) error {
	references, err := s.Store.CountActiveAgentEnterpriseIdentitySourceReferences(
		ctx,
		db.CountActiveAgentEnterpriseIdentitySourceReferencesParams{
			WorkspaceID: identity.WorkspaceID,
			RuntimeID:   identity.BucIdentitySourceRuntimeID,
			SandboxID:   identity.BucIdentitySourceSandboxID,
		},
	)
	if err != nil {
		return fmt.Errorf("count ASB enterprise identity source references: %w", err)
	}
	if references > 0 {
		return nil
	}
	return s.Source.Delete(
		ctx,
		identity.BucIdentitySourceRuntimeID,
		identity.BucIdentitySourceSandboxID.String,
	)
}

func (s *EnterpriseIdentityService) markIdentitySourceNeedsReauth(
	ctx context.Context,
	identity db.AgentEnterpriseIdentity,
) error {
	tenantScope, unlockRuntime, err := s.lockASBTenantCredentialScope(
		ctx,
		identity.BucIdentitySourceRuntimeID,
	)
	if err != nil {
		return err
	}
	defer unlockRuntime()
	key, err := enterpriseIdentitySourceKeyFromIdentity(identity, tenantScope.LockKey)
	if err != nil {
		return err
	}
	unlock, err := s.SourceLock.Lock(ctx, key)
	if err != nil {
		return err
	}
	defer unlock()
	return s.markIdentitySourceNeedsReauthUnderLock(ctx, identity)
}

func (s *EnterpriseIdentityService) lockASBTenantCredentialScope(
	ctx context.Context,
	runtimeID pgtype.UUID,
) (ASBTenantCredentialScope, func(), error) {
	if !runtimeID.Valid {
		return ASBTenantCredentialScope{}, nil,
			errors.New("ASB Runtime credential scope target is incomplete")
	}
	for attempt := 0; attempt < 3; attempt++ {
		tenantScope, err := s.Tenants.RuntimeCredentialScope(ctx, runtimeID)
		if err != nil {
			return ASBTenantCredentialScope{}, nil, err
		}
		unlockRuntime, err := s.RuntimeLock.LockSharedMany(ctx, tenantScope.RuntimeIDs)
		if err != nil {
			return ASBTenantCredentialScope{}, nil, err
		}
		currentScope, err := s.Tenants.RuntimeCredentialScope(ctx, runtimeID)
		if err != nil {
			unlockRuntime()
			return ASBTenantCredentialScope{}, nil, err
		}
		if sameASBTenantCredentialScope(tenantScope, currentScope) {
			return currentScope, unlockRuntime, nil
		}
		unlockRuntime()
	}
	return ASBTenantCredentialScope{}, nil, errors.New(
		"ASB Runtime credential scope changed while acquiring its read lock",
	)
}

func sameASBTenantCredentialScope(left, right ASBTenantCredentialScope) bool {
	if left.LockKey != right.LockKey || len(left.RuntimeIDs) != len(right.RuntimeIDs) {
		return false
	}
	for _, runtimeID := range left.RuntimeIDs {
		if !right.Contains(runtimeID) {
			return false
		}
	}
	return true
}

func (s *EnterpriseIdentityService) markIdentitySourceNeedsReauthUnderLock(
	ctx context.Context,
	identity db.AgentEnterpriseIdentity,
) error {
	params := db.ListActiveAgentEnterpriseIdentitySourceReferencesParams{
		WorkspaceID: identity.WorkspaceID,
		RuntimeID:   identity.BucIdentitySourceRuntimeID,
		SandboxID:   identity.BucIdentitySourceSandboxID,
	}
	references, err := s.Store.ListActiveAgentEnterpriseIdentitySourceReferences(ctx, params)
	if err != nil {
		return fmt.Errorf("list invalid ASB enterprise identity source references: %w", err)
	}
	if _, err := s.Store.MarkAgentEnterpriseIdentitiesNeedsReauthBySource(
		ctx,
		db.MarkAgentEnterpriseIdentitiesNeedsReauthBySourceParams(params),
	); err != nil {
		return fmt.Errorf("mark shared ASB enterprise identity source for reauthorization: %w", err)
	}
	var cleanupErr error
	for _, reference := range references {
		cleanupErr = errors.Join(
			cleanupErr,
			s.retireActiveSandboxes(
				ctx,
				reference.WorkspaceID,
				reference.AgentID,
				reference,
			),
		)
	}
	cleanupErr = errors.Join(
		cleanupErr,
		s.Source.Delete(
			ctx,
			identity.BucIdentitySourceRuntimeID,
			identity.BucIdentitySourceSandboxID.String,
		),
	)
	if cleanupErr != nil {
		return fmt.Errorf("retire invalid shared ASB enterprise identity source: %w", cleanupErr)
	}
	return nil
}

func (s *EnterpriseIdentityService) ResolveASBTaskIdentity(
	ctx context.Context,
	workspaceID pgtype.UUID,
	agentID pgtype.UUID,
	runtimeID pgtype.UUID,
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
		if !identity.BucIdentitySourceSandboxID.Valid ||
			strings.TrimSpace(identity.BucIdentitySourceSandboxID.String) == "" ||
			!identity.BucIdentitySourceRuntimeID.Valid ||
			!identity.AuthxRefreshExpiresAt.Valid ||
			!identity.AuthxRefreshExpiresAt.Time.After(s.Now()) {
			s.markNeedsReauth(ctx, identity)
			return ASBResolvedIdentity{}, ErrEnterpriseIdentityNeedsReauth
		}
		tenantScope, err := s.Tenants.RuntimeCredentialScope(ctx, runtimeID)
		if err != nil {
			return ASBResolvedIdentity{}, fmt.Errorf("resolve ASB Runtime credential scope: %w", err)
		}
		if !tenantScope.Contains(identity.BucIdentitySourceRuntimeID) {
			s.markNeedsReauth(ctx, identity)
			return ASBResolvedIdentity{}, ErrEnterpriseIdentityNeedsReauth
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
		ait, err := s.Idem.IssueAIT(ctx, refreshed.IDToken, updated.AgentSpiffeID, operatorSPIFFEID, s.currentConfig().AITTTL)
		if err != nil {
			return ASBResolvedIdentity{}, err
		}
		return ASBResolvedIdentity{
			Mode:               asbIdentityModeBound,
			RawEmployeeID:      updated.RawEmpID,
			BUCAgentID:         updated.BucAgentID,
			AgentSPIFFEID:      updated.AgentSpiffeID,
			AIPID:              updated.AipID,
			SourceSandboxID:    updated.BucIdentitySourceSandboxID.String,
			SourceRuntimeID:    updated.BucIdentitySourceRuntimeID,
			AgentIdentityToken: ait,
			Fingerprint:        enterpriseIdentityFingerprint(updated),
		}, nil
	}
	return ASBResolvedIdentity{}, errors.New("enterprise identity token was concurrently rotated")
}

func (s *EnterpriseIdentityService) AcquireASBTaskIdentitySource(
	ctx context.Context,
	workspaceID pgtype.UUID,
	agentID pgtype.UUID,
	runtimeID pgtype.UUID,
	expectedSourceSandboxID string,
) (func(context.Context) error, error) {
	expectedSourceSandboxID = strings.TrimSpace(expectedSourceSandboxID)
	if expectedSourceSandboxID == "" {
		return nil, errors.New("ASB enterprise identity source coordinate is incomplete")
	}
	identity, err := s.Store.GetActiveAgentEnterpriseIdentity(
		ctx,
		db.GetActiveAgentEnterpriseIdentityParams{
			WorkspaceID: workspaceID,
			AgentID:     agentID,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("load enterprise identity before source lease: %w", err)
	}
	tenantScope, unlockRuntime, err := s.lockASBTenantCredentialScope(ctx, runtimeID)
	if err != nil {
		return nil, fmt.Errorf("lock ASB Runtime credential scope before source lease: %w", err)
	}
	if !tenantScope.Contains(identity.BucIdentitySourceRuntimeID) {
		unlockRuntime()
		return nil, ErrEnterpriseIdentityNeedsReauth
	}
	sourceKey, err := enterpriseIdentitySourceKeyFromIdentity(identity, tenantScope.LockKey)
	if err != nil {
		unlockRuntime()
		return nil, err
	}
	unlockSource, err := s.SourceLock.LockShared(ctx, sourceKey)
	if err != nil {
		unlockRuntime()
		return nil, fmt.Errorf("lock shared ASB enterprise identity source: %w", err)
	}
	unlockIdentity, err := s.TokenLock.Lock(ctx, identity.ID)
	if err != nil {
		unlockSource()
		unlockRuntime()
		return nil, fmt.Errorf("lock Agent enterprise identity: %w", err)
	}
	releaseLocks := func() {
		unlockIdentity()
		unlockSource()
		unlockRuntime()
	}
	current, err := s.Store.GetActiveAgentEnterpriseIdentity(
		ctx,
		db.GetActiveAgentEnterpriseIdentityParams{
			WorkspaceID: workspaceID,
			AgentID:     agentID,
		},
	)
	if err != nil {
		releaseLocks()
		return nil, fmt.Errorf("reload enterprise identity under source lock: %w", err)
	}
	if current.ID != identity.ID ||
		!sameEnterpriseIdentitySourceSnapshot(identity, current) ||
		!current.BucIdentitySourceSandboxID.Valid ||
		current.BucIdentitySourceSandboxID.String != expectedSourceSandboxID {
		releaseLocks()
		return nil, ErrEnterpriseIdentityNeedsReauth
	}
	if err := s.Source.Prepare(
		ctx,
		current.BucIdentitySourceRuntimeID,
		expectedSourceSandboxID,
		current.RawEmpID,
		current.BucAgentID,
	); err != nil {
		releaseLocks()
		if errors.Is(err, ErrEnterpriseIdentityNeedsReauth) {
			_ = s.markIdentitySourceNeedsReauth(ctx, current)
		}
		return nil, fmt.Errorf("prepare ASB enterprise identity source: %w", err)
	}
	return func(closeCtx context.Context) error {
		defer releaseLocks()
		if err := s.Source.Park(
			closeCtx,
			current.BucIdentitySourceRuntimeID,
			expectedSourceSandboxID,
		); err != nil {
			return fmt.Errorf("park ASB enterprise identity source: %w", err)
		}
		return nil
	}, nil
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
			SourceCheckBefore: pgtype.Timestamptz{Time: now.Add(-s.SourceRefreshInterval), Valid: true},
			BatchSize:         s.MaintenanceBatch,
		},
	)
	if err != nil {
		return fmt.Errorf("list enterprise identities for maintenance: %w", err)
	}
	maintainedSources := make(map[string]struct{})
	for _, identity := range identities {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !identity.BucIdentitySourceSandboxID.Valid ||
			strings.TrimSpace(identity.BucIdentitySourceSandboxID.String) == "" ||
			!identity.BucIdentitySourceRuntimeID.Valid ||
			!identity.BucIdentitySourceUpdatedAt.Valid ||
			!identity.AuthxRefreshExpiresAt.Valid ||
			!identity.AuthxRefreshExpiresAt.Time.After(now) {
			s.markNeedsReauth(ctx, identity)
			continue
		}
		current := identity
		if !current.AuthxRefreshExpiresAt.Time.After(now.Add(s.RefreshBefore)) {
			updated, _, err := s.rotateAuthXToken(ctx, current)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				slog.Warn("enterprise identity token maintenance failed",
					"agent_id", util.UUIDToString(current.AgentID),
					"error", err,
				)
				continue
			}
			if err == nil {
				current = updated
			}
		}
		if !current.BucIdentitySourceUpdatedAt.Time.After(now.Add(-s.SourceRefreshInterval)) {
			sourceCoordinate := util.UUIDToString(current.BucIdentitySourceRuntimeID) + ":" +
				current.BucIdentitySourceSandboxID.String
			if _, maintained := maintainedSources[sourceCoordinate]; maintained {
				continue
			}
			maintainedSources[sourceCoordinate] = struct{}{}
			if err := s.refreshIdentitySource(ctx, current); err != nil {
				if errors.Is(err, ErrEnterpriseIdentityNeedsReauth) {
					if markErr := s.markIdentitySourceNeedsReauth(ctx, current); markErr != nil {
						slog.Warn(
							"failed to invalidate shared ASB enterprise identity source",
							"sandbox_id", current.BucIdentitySourceSandboxID.String,
							"error", markErr,
						)
					}
					continue
				}
				slog.Warn(
					"ASB enterprise identity source maintenance failed",
					"agent_id", util.UUIDToString(current.AgentID),
					"error", err,
				)
			}
		}
	}
	return nil
}

func (s *EnterpriseIdentityService) refreshIdentitySource(
	ctx context.Context,
	identity db.AgentEnterpriseIdentity,
) error {
	current, releaseLocks, err := s.lockEnterpriseIdentityForSourceMutation(
		ctx,
		identity.WorkspaceID,
		identity.AgentID,
	)
	if err != nil {
		return err
	}
	defer releaseLocks()
	if !sameEnterpriseIdentitySourceSnapshot(identity, current) {
		return nil
	}
	if err := s.Source.Prepare(
		ctx,
		current.BucIdentitySourceRuntimeID,
		current.BucIdentitySourceSandboxID.String,
		current.RawEmpID,
		current.BucAgentID,
	); err != nil {
		return err
	}
	parked := false
	defer func() {
		if parked {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = s.Source.Park(
			cleanupCtx,
			current.BucIdentitySourceRuntimeID,
			current.BucIdentitySourceSandboxID.String,
		)
	}()
	if err := s.Source.Park(
		ctx,
		current.BucIdentitySourceRuntimeID,
		current.BucIdentitySourceSandboxID.String,
	); err != nil {
		return err
	}
	parked = true
	_, err = s.Store.TouchActiveAgentEnterpriseIdentitySourceReferences(
		ctx,
		db.TouchActiveAgentEnterpriseIdentitySourceReferencesParams{
			WorkspaceID: current.WorkspaceID,
			RuntimeID:   current.BucIdentitySourceRuntimeID,
			SandboxID:   current.BucIdentitySourceSandboxID,
		},
	)
	return err
}

func (s *EnterpriseIdentityService) rotateAuthXToken(
	ctx context.Context,
	identity db.AgentEnterpriseIdentity,
) (db.AgentEnterpriseIdentity, EnterpriseOIDCToken, error) {
	unlock, err := s.TokenLock.Lock(ctx, identity.ID)
	if err != nil {
		return db.AgentEnterpriseIdentity{}, EnterpriseOIDCToken{}, err
	}
	defer unlock()

	// Normandy refresh tokens are single-use and rotate on every renewal.
	// Reload after taking the cross-replica lock so a waiter consumes the
	// winner's newly persisted token instead of reusing the already consumed
	// token and revoking the whole token family.
	current, err := s.Store.GetAgentEnterpriseIdentity(
		ctx,
		db.GetAgentEnterpriseIdentityParams{
			WorkspaceID: identity.WorkspaceID,
			AgentID:     identity.AgentID,
		},
	)
	if err != nil {
		return db.AgentEnterpriseIdentity{}, EnterpriseOIDCToken{}, err
	}
	if current.ID != identity.ID ||
		current.Status != "active" ||
		!current.AuthxRefreshExpiresAt.Valid ||
		!current.AuthxRefreshExpiresAt.Time.After(s.Now()) ||
		len(current.AuthxRefreshTokenEncrypted) == 0 {
		return db.AgentEnterpriseIdentity{}, EnterpriseOIDCToken{}, ErrEnterpriseIdentityNeedsReauth
	}

	refreshToken, err := s.Secrets.Open(current.AuthxRefreshTokenEncrypted)
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
		ID:                         current.ID,
		ExpectedTokenVersion:       current.TokenVersion,
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
	current, releaseLocks, err := s.lockEnterpriseIdentityForSourceMutation(
		ctx,
		workspaceID,
		agentID,
	)
	if err != nil {
		return err
	}
	defer releaseLocks()
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
	if current.BucIdentitySourceSandboxID.Valid &&
		current.BucIdentitySourceRuntimeID.Valid {
		cleanupErr = errors.Join(
			cleanupErr,
			s.deleteIdentitySourceIfUnreferencedUnderLock(ctx, current),
		)
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
		if err := s.Sandboxes.DeleteRuntimeSandbox(ctx, session.RuntimeID, session.SandboxID); err != nil {
			cleanupErr = errors.Join(
				cleanupErr,
				fmt.Errorf("delete inactive ASB sandbox %s: %w", session.SandboxID, err),
			)
		}
	}
	return cleanupErr
}

func (s *EnterpriseIdentityService) markNeedsReauth(ctx context.Context, identity db.AgentEnterpriseIdentity) {
	current, releaseLocks, err := s.lockEnterpriseIdentityForSourceMutation(
		ctx,
		identity.WorkspaceID,
		identity.AgentID,
	)
	if err != nil {
		slog.Warn(
			"failed to lock current enterprise identity requiring reauthorization",
			"agent_id", util.UUIDToString(identity.AgentID),
			"error", err,
		)
		return
	}
	defer releaseLocks()
	if !sameEnterpriseIdentitySourceSnapshot(identity, current) ||
		current.Status != "active" {
		return
	}
	affected, err := s.Store.MarkAgentEnterpriseIdentityNeedsReauth(ctx, db.MarkAgentEnterpriseIdentityNeedsReauthParams{
		ID:                   current.ID,
		ExpectedTokenVersion: current.TokenVersion,
	})
	if err != nil || affected == 0 {
		return
	}
	if err := s.retireActiveSandboxes(ctx, current.WorkspaceID, current.AgentID, current); err != nil {
		slog.Warn("failed to retire sandboxes for enterprise identity requiring reauthorization",
			"agent_id", util.UUIDToString(current.AgentID),
			"error", err,
		)
	}
	if current.BucIdentitySourceSandboxID.Valid &&
		current.BucIdentitySourceRuntimeID.Valid {
		if err := s.deleteIdentitySourceIfUnreferencedUnderLock(ctx, current); err != nil {
			slog.Warn(
				"failed to release ASB source requiring enterprise reauthorization",
				"agent_id", util.UUIDToString(current.AgentID),
				"sandbox_id", current.BucIdentitySourceSandboxID.String,
				"error", err,
			)
		}
	}
}

func (s *EnterpriseIdentityService) lockEnterpriseIdentityForSourceMutation(
	ctx context.Context,
	workspaceID pgtype.UUID,
	agentID pgtype.UUID,
) (db.AgentEnterpriseIdentity, func(), error) {
	for attempt := 0; attempt < 3; attempt++ {
		identity, err := s.Store.GetAgentEnterpriseIdentity(
			ctx,
			db.GetAgentEnterpriseIdentityParams{
				WorkspaceID: workspaceID,
				AgentID:     agentID,
			},
		)
		if err != nil {
			return db.AgentEnterpriseIdentity{}, nil, err
		}
		var unlockRuntime func()
		var unlockSource func()
		if identity.BucIdentitySourceSandboxID.Valid &&
			identity.BucIdentitySourceRuntimeID.Valid {
			tenantScope, runtimeUnlock, keyErr := s.lockASBTenantCredentialScope(
				ctx,
				identity.BucIdentitySourceRuntimeID,
			)
			if keyErr != nil {
				return db.AgentEnterpriseIdentity{}, nil, keyErr
			}
			unlockRuntime = runtimeUnlock
			sourceKey, err := enterpriseIdentitySourceKeyFromIdentity(
				identity,
				tenantScope.LockKey,
			)
			if err != nil {
				unlockRuntime()
				return db.AgentEnterpriseIdentity{}, nil, fmt.Errorf(
					"build enterprise identity source key: %w",
					err,
				)
			}
			unlockSource, err = s.SourceLock.Lock(ctx, sourceKey)
			if err != nil {
				unlockRuntime()
				return db.AgentEnterpriseIdentity{}, nil, fmt.Errorf(
					"lock shared enterprise identity source: %w",
					err,
				)
			}
		}
		unlockIdentity, err := s.TokenLock.Lock(ctx, identity.ID)
		if err != nil {
			if unlockSource != nil {
				unlockSource()
			}
			if unlockRuntime != nil {
				unlockRuntime()
			}
			return db.AgentEnterpriseIdentity{}, nil, fmt.Errorf(
				"lock Agent enterprise identity: %w",
				err,
			)
		}
		release := func() {
			unlockIdentity()
			if unlockSource != nil {
				unlockSource()
			}
			if unlockRuntime != nil {
				unlockRuntime()
			}
		}
		current, err := s.Store.GetAgentEnterpriseIdentity(
			ctx,
			db.GetAgentEnterpriseIdentityParams{
				WorkspaceID: workspaceID,
				AgentID:     agentID,
			},
		)
		if err != nil {
			release()
			return db.AgentEnterpriseIdentity{}, nil, err
		}
		if sameEnterpriseIdentitySourceSnapshot(identity, current) {
			return current, release, nil
		}
		release()
	}
	return db.AgentEnterpriseIdentity{}, nil, errors.New(
		"enterprise identity source changed while acquiring its mutation lock",
	)
}

func (s *EnterpriseIdentityService) agentSPIFFEID(agentID pgtype.UUID) (string, error) {
	return idemapi.GenerateSpiffeIdUri(&idemapi.IdentitySpec{
		TrustDomainName: s.currentConfig().AgentTrustDomain,
		WorkloadPathHeader: idemapi.WorkloadPathField{
			Name:  "ns",
			Value: s.currentConfig().AgentNamespace,
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
		TrustDomainName: s.currentConfig().OperatorTrustDomain,
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
	// Rotating platform-managed OAuth credentials does not change the logical
	// user/Agent identity, so warm task sandboxes remain reusable.
	for _, value := range []string{
		util.UUIDToString(identity.WorkspaceID),
		util.UUIDToString(identity.AgentID),
		identity.RawEmpID,
		identity.AgentSpiffeID,
		identity.BucAgentID,
	} {
		_, _ = hasher.Write([]byte(value))
		_, _ = hasher.Write([]byte{0})
	}
	return fmt.Sprintf("%x", hasher.Sum(nil))
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
				return fmt.Errorf("ASB sandbox entered terminal state %q", sandbox.Status.State)
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			if err != nil {
				return fmt.Errorf("ASB sandbox was not running within %s: %w", timeout, err)
			}
			return fmt.Errorf("ASB sandbox was not running within %s", timeout)
		case <-ticker.C:
		}
	}
}
