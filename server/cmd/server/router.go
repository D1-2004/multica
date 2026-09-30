package main

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/multica-ai/multica/server/internal/analytics"
	"github.com/multica-ai/multica/server/internal/assoc"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/cloudruntime"
	"github.com/multica-ai/multica/server/internal/daemonws"
	"github.com/multica-ai/multica/server/internal/deploymentfence"
	"github.com/multica-ai/multica/server/internal/dshhost"
	"github.com/multica-ai/multica/server/internal/dwsclient"
	"github.com/multica-ai/multica/server/internal/dwseventsource"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/featureflags"
	"github.com/multica-ai/multica/server/internal/handler"
	a2aintegration "github.com/multica-ai/multica/server/internal/integrations/a2a"
	"github.com/multica-ai/multica/server/internal/integrations/agentidentityhsf"
	"github.com/multica-ai/multica/server/internal/integrations/agentmessagerouter"
	"github.com/multica-ai/multica/server/internal/integrations/channel"
	"github.com/multica-ai/multica/server/internal/integrations/channel/engine"
	composiointeg "github.com/multica-ai/multica/server/internal/integrations/composio"
	"github.com/multica-ai/multica/server/internal/integrations/dingtalk"
	"github.com/multica-ai/multica/server/internal/integrations/lark"
	"github.com/multica-ai/multica/server/internal/integrations/orgemphsf"
	"github.com/multica-ai/multica/server/internal/integrations/slack"
	"github.com/multica-ai/multica/server/internal/integrations/wecom"
	"github.com/multica-ai/multica/server/internal/langfuse"
	"github.com/multica-ai/multica/server/internal/managedagent"
	obsmetrics "github.com/multica-ai/multica/server/internal/metrics"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/realtime"
	"github.com/multica-ai/multica/server/internal/sandboxrelay"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/service/dingtalkresponse"
	"github.com/multica-ai/multica/server/internal/service/inboundcoord"
	"github.com/multica-ai/multica/server/internal/service/scenememory"
	"github.com/multica-ai/multica/server/internal/service/userdecision"
	"github.com/multica-ai/multica/server/internal/sitehosting"
	"github.com/multica-ai/multica/server/internal/storage"
	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/internal/util/secretbox"
	"github.com/multica-ai/multica/server/internal/wsfs"
	composiosdk "github.com/multica-ai/multica/server/pkg/composio"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dws"
	"github.com/multica-ai/multica/server/pkg/featureflag"
	"github.com/multica-ai/multica/server/pkg/protocol"
	"github.com/multica-ai/multica/server/pkg/runtimeconfig"
)

var defaultOrigins = []string{
	"http://localhost:3000", // Next.js dev
	"http://localhost:5173", // electron-vite dev
	"http://localhost:5174", // electron-vite dev (fallback port)
}

// corsAllowedHeaders must list every header the browser clients send. A header
// missing here fails the preflight, so the request never reaches the handler at
// all — the failure looks nothing like "the server ignored my header".
// X-Client-Capabilities in particular was daemon-only (a Go client, never
// preflighted) until the web app started advertising chat-draft-restore-v1 on
// cancel.
var corsAllowedHeaders = []string{
	"Accept",
	"Authorization",
	"Content-Type",
	"X-Workspace-ID",
	"X-Workspace-Slug",
	"X-Request-ID",
	"X-Agent-ID",
	"X-Task-ID",
	"X-CSRF-Token",
	"X-Client-Platform",
	"X-Client-Version",
	"X-Client-OS",
	"X-Client-Capabilities",
}

// corsExposedHeaders lists response headers browser clients are allowed to read.
// Without this a custom response header is silently unreadable from JS on a
// cross-origin request (only the CORS-safelisted response headers are exposed by
// default) — the header arrives on the wire and then disappears, which looks
// exactly like the server never sent it.
//
// Referencing the handler constant rather than re-typing the string keeps a
// rename from quietly switching the signal off (MUL-5492).
var corsExposedHeaders = []string{
	handler.HeaderCommentsTruncated,
	handler.HeaderTimelineTruncated,
}

func allowedOrigins() []string {
	raw := strings.TrimSpace(os.Getenv("CORS_ALLOWED_ORIGINS"))
	if raw == "" {
		raw = strings.TrimSpace(os.Getenv("FRONTEND_ORIGIN"))
	}
	if raw == "" {
		return defaultOrigins
	}

	parts := strings.Split(raw, ",")
	origins := make([]string, 0, len(parts))
	for _, part := range parts {
		origin := strings.TrimSpace(part)
		if origin != "" {
			origins = append(origins, origin)
		}
	}
	if len(origins) == 0 {
		return defaultOrigins
	}
	return origins
}

func dingTalkAccountCallbackCORSMiddleware(appOrigins []string, dbaseOrigin string) func(http.Handler) http.Handler {
	global := cors.Handler(cors.Options{
		AllowedOrigins:   appOrigins,
		AllowedMethods:   []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowedHeaders:   corsAllowedHeaders,
		ExposedHeaders:   corsExposedHeaders,
		AllowCredentials: true,
		MaxAge:           300,
	})
	callback := cors.Handler(cors.Options{
		AllowedOrigins: []string{strings.TrimSpace(dbaseOrigin)},
		AllowedMethods: []string{"POST", "OPTIONS"},
		AllowedHeaders: []string{"Authorization", "Content-Type"},
		MaxAge:         300,
	})
	return func(next http.Handler) http.Handler {
		globalHandler := global(next)
		callbackHandler := callback(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if isDingTalkAccountCallbackPath(r.URL.Path) {
				callbackHandler.ServeHTTP(w, r)
				return
			}
			globalHandler.ServeHTTP(w, r)
		})
	}
}

func dynamicDingTalkAccountCallbackCORSMiddleware(config *appRuntimeConfig) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			middleware := dingTalkAccountCallbackCORSMiddleware(
				config.corsAllowedOrigins(),
				config.dbaseBindingOrigin(),
			)
			middleware(next).ServeHTTP(w, r)
		})
	}
}

func dynamicLoginProviderMiddleware(config *appRuntimeConfig, provider string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !config.loginProviderAllowed(provider) {
				http.NotFound(w, r)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func isDingTalkAccountCallbackPath(path string) bool {
	const suffix = "/callback"
	const prefix = "/api/integrations/dingtalk/account-bindings/"
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, suffix) {
		return false
	}
	id := strings.TrimSuffix(strings.TrimPrefix(path, prefix), suffix)
	return id != "" && !strings.Contains(id, "/")
}

func dBaseBindingURLMatchesOrigin(bindingURL, expectedOrigin string) bool {
	parsed, err := url.Parse(strings.TrimSpace(bindingURL))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return false
	}
	origin, err := handler.NormalizeDingTalkAccountBindingOrigin(
		(&url.URL{Scheme: parsed.Scheme, Host: parsed.Host}).String(),
	)
	return err == nil && origin == expectedOrigin
}

// appURLFromEnv resolves the user-facing web app URL for environment-only
// startups. It prefers MULTICA_APP_URL and falls back to FRONTEND_ORIGIN,
// matching handler.resolveFrontendAppURL and the CLI login flow
// (cmd/multica tryResolveAppURL). Diamond mode overwrites this snapshot
// from web.app_url. Empty when neither is set.
func appURLFromEnv() string {
	if v := strings.TrimRight(strings.TrimSpace(os.Getenv("MULTICA_APP_URL")), "/"); v != "" {
		return v
	}
	return strings.TrimRight(strings.TrimSpace(os.Getenv("FRONTEND_ORIGIN")), "/")
}

// dingTalkStreamConnectionTarget implements the staged ownership cutover for
// DingTalk Stream connections:
//
//   - absent: keep the PostgreSQL singleton lease while running the new durable
//     inbox connector (safe while older binaries still exist);
//   - 1 or 2: make Redis the sole ownership authority with that READY target.
//
// There is deliberately no automatic Redis fallback. Once an operator opts in
// with 1/2, a missing Redis client is a startup configuration error.
func dingTalkStreamConnectionTarget() (target int, coordinated bool, err error) {
	raw := strings.TrimSpace(os.Getenv("MULTICA_DINGTALK_STREAM_CONNECTION_TARGET"))
	if raw == "" {
		return 0, false, nil
	}
	target, err = strconv.Atoi(raw)
	if err != nil || (target != 1 && target != 2) {
		return 0, false, fmt.Errorf("MULTICA_DINGTALK_STREAM_CONNECTION_TARGET must be empty, 1, or 2")
	}
	return target, true, nil
}

func dingTalkStreamCoordinatorFromEnv(rdb *redis.Client) (engine.StreamCoordinator, int, bool, error) {
	target, coordinated, err := dingTalkStreamConnectionTarget()
	if err != nil {
		return nil, 0, false, err
	}
	if !coordinated {
		return nil, 0, false, nil
	}
	if rdb == nil {
		return nil, 0, false, errors.New("MULTICA_DINGTALK_STREAM_CONNECTION_TARGET requires Redis")
	}
	coordinator, err := engine.NewRedisStreamCoordinator(rdb, engine.RedisStreamCoordinatorConfig{
		TargetReady: target,
	})
	if err != nil {
		return nil, 0, false, fmt.Errorf("initialize DingTalk Stream coordinator: %w", err)
	}
	return coordinator, target, true, nil
}

// parseTrustedProxies parses a comma-separated list of CIDR prefixes from the
// MULTICA_TRUSTED_PROXIES env var. Invalid entries are dropped with a single
// warn-line per entry rather than crashing the server — a typo in one CIDR
// shouldn't take the whole API down. Returns nil for empty input, which the
// rate limiter treats as "trust no proxy headers, use RemoteAddr only".
func parseTrustedProxies(raw string) []netip.Prefix {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var out []netip.Prefix
	for _, part := range strings.Split(raw, ",") {
		s := strings.TrimSpace(part)
		if s == "" {
			continue
		}
		p, err := netip.ParsePrefix(s)
		if err != nil {
			slog.Warn("MULTICA_TRUSTED_PROXIES: ignoring invalid CIDR",
				"value", s, "error", err)
			continue
		}
		out = append(out, p)
	}
	return out
}

// normalizeServerVersion maps the unstamped "dev" default (main.go's
// `version` var, unchanged when the binary wasn't built with
// -X main.version=<tag>) to an empty string. handler.Config.ServerVersion
// feeds /api/config's server_version field with omitempty, so an empty
// string hides the Help popover's version row instead of rendering
// "Server version dev" for a local `go build`/`go run` or a self-hosted
// `docker build` without --build-arg VERSION.
func normalizeServerVersion(v string) string {
	if v == "dev" {
		return ""
	}
	return v
}

// NewRouter creates the fully-configured Chi router with all middleware and routes.
// rdb is optional: when non-nil the runtime local-skill request stores are
// swapped for Redis-backed implementations so multiple API nodes share the
// same pending queue (required for multi-node prod). This should be a request
// path Redis client, not the realtime relay's blocking read client. A nil rdb
// keeps the default in-memory stores which are fine for single-node dev and
// tests.
func NewRouter(pool *pgxpool.Pool, hub *realtime.Hub, bus *events.Bus, analyticsClient analytics.Client, rdb *redis.Client) chi.Router {
	r, _ := NewRouterWithOptions(pool, hub, bus, analyticsClient, rdb, RouterOptions{})
	return r
}

type RouterOptions struct {
	HTTPMetrics     *obsmetrics.HTTPMetrics
	BusinessMetrics *obsmetrics.BusinessMetrics
	// WecomMetrics is the WeCom adapter's health sink. Nil discards every
	// counter, which is what a deployment with /metrics turned off gets.
	WecomMetrics *obsmetrics.WecomMetrics
	DaemonHub    *daemonws.Hub
	DaemonWakeup service.TaskWakeupNotifier
	RunnerRelay  realtime.Broadcaster
	FeatureFlags *featureflag.Service
	// HeartbeatScheduler, when non-nil, replaces the default synchronous
	// passthrough scheduler on the constructed Handler. main.go injects a
	// BatchedHeartbeatScheduler here so the caller can also drive Run/Stop;
	// tests leave this nil and get the legacy synchronous behavior.
	HeartbeatScheduler handler.HeartbeatScheduler
	// SandboxRelaySigner is configured only by pre-release. It must be shared
	// with the request-path FC/E2B launcher so synchronous chat sends mint the
	// same task-scoped routing assertion as background launches.
	SandboxRelaySigner *sandboxrelay.Signer
	// SandboxRelay is nil on ordinary deployments. Production injects the
	// signed pre-release sandbox relay here so requests carrying the routing
	// assertion are intercepted before local authentication and routing.
	SandboxRelay    func(http.Handler) http.Handler
	RuntimeConfig   *appRuntimeConfig
	DeploymentFence *deploymentfence.Service
	// Langfuse is the LLM trace exporter shared by the inbound coordinator,
	// the scene memory flusher, and the agent task lifecycle. Nil disables
	// every export.
	Langfuse *langfuse.Client
}

// NewRouterWithOptions builds the fully-configured Chi router and
// returns the *handler.Handler it was constructed from. Callers that
// need to drive background lifecycle on services attached to the
// handler (e.g. starting the Lark inbound Hub under a long-running
// context, calling Wait on shutdown) use the returned handler;
// callers that only need the HTTP handler (tests, the simple
// NewRouter shim) discard the second value.
func NewRouterWithOptions(pool *pgxpool.Pool, hub *realtime.Hub, bus *events.Bus, analyticsClient analytics.Client, rdb *redis.Client, opts RouterOptions) (chi.Router, *handler.Handler) {
	queries := db.New(pool)
	emailSvc := service.NewEmailService()
	if opts.RuntimeConfig != nil {
		emailSvc.SetAppURLProvider(opts.RuntimeConfig.frontendOrigin)
	}
	daemonHub := opts.DaemonHub
	if daemonHub == nil {
		daemonHub = daemonws.NewHub()
	}

	// Initialize storage with S3 as primary, fallback to local
	var store storage.Storage
	var s3Opts []storage.S3Option
	// Managed keys serve internal storage requests; resource-bound STS credentials
	// sign external transfers under the bucket's existing network policy.
	// Absent OSS_AUTHZ_BUCKET this is nil and the storage layer behaves exactly
	// as upstream (static keys, or local disk when S3_BUCKET is unset too).
	ossCredentials, err := ossCredentialsFromEnv()
	if err != nil {
		slog.Error("OSS credential configuration failed", "error", err)
		os.Exit(1)
	}
	if ossCredentials != nil {
		s3Opts = append(s3Opts, storage.WithCredentialsProvider(ossCredentials),
			storage.WithPresignCredentialsProvider(ossPresignCredentials(ossCredentials)))
	}
	s3 := storage.NewS3StorageFromEnv(s3Opts...)
	if s3 != nil {
		store = s3
	} else {
		local := storage.NewLocalStorageFromEnv()
		if local != nil {
			if opts.RuntimeConfig != nil {
				local.SetBaseURLProvider(opts.RuntimeConfig.localUploadBaseURL)
			}
			store = local
		}
	}

	cfSigner := auth.NewCloudFrontSignerFromEnv()
	origins := allowedOrigins()
	stableRuntimePublishers, err := parseStableRuntimePublisherUserIDs(
		os.Getenv("MULTICA_FC_E2B_STABLE_PUBLISHER_USER_IDS"),
	)
	if err != nil {
		slog.Error("FC/E2B stable publisher configuration failed", "error", err)
		os.Exit(1)
	}

	signupConfig := handler.Config{
		AllowSignup:                   os.Getenv("ALLOW_SIGNUP") != "false",
		AllowedEmails:                 splitAndTrim(os.Getenv("ALLOWED_EMAILS")),
		AllowedEmailDomains:           splitAndTrim(os.Getenv("ALLOWED_EMAIL_DOMAINS")),
		A2AOperatorEmails:             splitAndTrim(os.Getenv("MULTICA_A2A_OPERATOR_EMAILS")),
		A2AForwardAllowedOrigins:      splitAndTrim(os.Getenv("MULTICA_A2A_FORWARD_ALLOWED_ORIGINS")),
		A2AForwardRegistryURLs:        splitAndTrim(os.Getenv("MULTICA_A2A_FORWARD_REGISTRY_URLS")),
		A2AForwardRegistrationSecret:  strings.TrimSpace(os.Getenv("MULTICA_A2A_FORWARD_REGISTRATION_SECRET")),
		GitHubPreWebhookURL:           strings.TrimSpace(os.Getenv("GITHUB_PRE_WEBHOOK_URL")),
		GitHubPreWebhookSecret:        strings.TrimSpace(os.Getenv("GITHUB_PRE_WEBHOOK_SECRET")),
		StableRuntimePublisherUserIDs: stableRuntimePublishers,
		DisableWorkspaceCreation:      os.Getenv("DISABLE_WORKSPACE_CREATION") == "true",
		VCSIntegrationEnabled:         os.Getenv("MULTICA_VCS_INTEGRATION_ENABLED") == "true",
		PublicURL:                     strings.TrimRight(strings.TrimSpace(os.Getenv("MULTICA_PUBLIC_URL")), "/"),
		SitePublicURL:                 strings.TrimRight(strings.TrimSpace(os.Getenv("MULTICA_SITE_PUBLIC_URL")), "/"),
		FrontendOrigin:                strings.TrimRight(strings.TrimSpace(os.Getenv("FRONTEND_ORIGIN")), "/"),
		AppURL:                        appURLFromEnv(),
		LoginProviders:                handler.LoginProviders(),
		TrustedProxies:                parseTrustedProxies(os.Getenv("MULTICA_TRUSTED_PROXIES")),
		CloudRuntimeFleetURL:          cloudRuntimeFleetURLFromEnv(),
		CloudRuntimeFleetTimeout:      envDuration("MULTICA_CLOUD_FLEET_TIMEOUT", 35*time.Second),
		AgentIdentityControlBaseURL:   agentIdentityControlBaseURLFromEnv(),
		FCE2B:                         service.FCE2BConfigFromEnv(),
		ASB:                           service.ASBConfigFromEnv(),
		EnterpriseIdentity:            service.EnterpriseIdentityConfigFromEnv(),
		AttachmentDownloadMode:        os.Getenv("ATTACHMENT_DOWNLOAD_MODE"),
		AttachmentDownloadURLTTL:      envDuration("ATTACHMENT_DOWNLOAD_URL_TTL", 30*time.Minute),
		AttachmentFrameAncestors:      origins,
		LLMAPIKey:                     strings.TrimSpace(os.Getenv("MULTICA_LLM_API_KEY")),
		LLMBaseURL:                    strings.TrimSpace(os.Getenv("MULTICA_LLM_BASE_URL")),
		LLMDefaultModel:               strings.TrimSpace(os.Getenv("MULTICA_LLM_DEFAULT_MODEL")),
		ServerVersion:                 normalizeServerVersion(version),
	}
	if opts.RuntimeConfig != nil {
		opts.RuntimeConfig.setBase(signupConfig)
		signupConfig = opts.RuntimeConfig.handlerConfig()
		origins = append([]string(nil), signupConfig.AttachmentFrameAncestors...)
		stableRuntimePublishers = signupConfig.StableRuntimePublisherUserIDs
	}
	var appURLProvider func() string
	var publicURLProvider func() string
	var siteConnectSrcProvider func() []string
	var agentIdentityControlBaseURLProvider func() string
	if opts.RuntimeConfig != nil {
		appURLProvider = opts.RuntimeConfig.appURL
		publicURLProvider = opts.RuntimeConfig.publicURL
		siteConnectSrcProvider = opts.RuntimeConfig.siteConnectSrc
		agentIdentityControlBaseURLProvider = opts.RuntimeConfig.agentIdentityControlBaseURL
	}
	h := handler.New(queries, pool, hub, bus, emailSvc, store, cfSigner, analyticsClient, signupConfig, daemonHub)
	if provision, err := dshStorageProvisioning(opts.RuntimeConfig); err != nil {
		slog.Error("DSH storage provisioning configuration unavailable", "error", err)
	} else if provision != nil {
		h.FCE2BLauncher.ProvisionDSHStorage = func(ctx context.Context, database dshhost.Database, key dshhost.Key) (dshhost.Host, error) {
			host, err := provision(ctx, database, key)
			if err == nil {
				h.TaskService.NotifyDSHReadiness(ctx, database, key, "provisioning_ready")
			}
			return host, err
		}
		if provision != nil {
			h.ProvisionDSHStorage = func(ctx context.Context, key dshhost.Key) (dshhost.Host, error) {
				return h.FCE2BLauncher.ProvisionDSHStorage(ctx, pool, key)
			}
		}
	}
	if ctrl, err := workspaceFSController(opts.RuntimeConfig); err != nil {
		slog.Error("workspace filesystem provisioning configuration unavailable", "error", err)
	} else if ctrl != nil {
		h.FCE2BLauncher.PrepareWorkspaceMount = func(ctx context.Context, db wsfs.Database, workspaceID, agentID uuid.UUID, employee *dshhost.Host) (wsfs.MountDecision, error) {
			cfg, _, err := readDSHStorageConfig(opts.RuntimeConfig)
			if err == nil && cfg != nil {
				live := *ctrl
				live.Spec = cfg.Placement
				return live.PrepareMount(ctx, db, workspaceID, agentID, employee)
			}
			return ctrl.PrepareMount(ctx, db, workspaceID, agentID, employee)
		}
		h.FCE2BLauncher.ReadWorkspaceMount = func(ctx context.Context, db wsfs.Database, workspaceID, agentID uuid.UUID, employee *dshhost.Host) (wsfs.MountDecision, error) {
			return ctrl.ReadMount(ctx, db, workspaceID, agentID, employee)
		}
	}
	h.Assoc = assoc.NewService(assoc.NewSQLStore(pool))
	h.SiteHosting = sitehosting.NewService(
		sitehosting.NewPostgresStore(pool),
		sitehosting.NewStorageObjectStore(store),
		sitehosting.Config{
			APIBaseURL:         signupConfig.PublicURL,
			SitePublicURL:      signupConfig.SitePublicURL,
			ConnectSrcProvider: siteConnectSrcProvider,
			Limits:             sitehosting.DefaultLimits(),
		},
	)
	if pushKey, pushKeyErr := secretbox.LoadKey("MULTICA_A2A_PUSH_SECRET_KEY"); pushKeyErr == nil {
		pushSecrets, boxErr := secretbox.New(pushKey)
		if boxErr != nil {
			slog.Error("A2A push secret configuration is invalid; push notifications disabled", "error", boxErr)
		} else {
			h.A2AService.PushSecrets = pushSecrets
			h.A2APushWorker = service.NewA2APushWorker(queries, h.A2AService)
			h.A2AService.PushNotifier = h.A2APushWorker
		}
	} else {
		slog.Info("A2A push notifications disabled (MULTICA_A2A_PUSH_SECRET_KEY not set)")
	}
	h.A2AProtocol = a2aintegration.NewJSONRPCHandler(h.A2AService)
	h.RunnerRelay = opts.RunnerRelay
	if setter, ok := opts.RunnerRelay.(interface {
		SetRunnerMachineDeliverer(realtime.RunnerMachineDeliverer)
	}); ok {
		setter.SetRunnerMachineDeliverer(h)
	}
	if opts.RuntimeConfig != nil {
		// runtime.use_dws_for_tag moves DingTalk calls off the dws CLI live;
		// replicas then share each identity's token through Redis.
		dwsclient.SetTokenStore(dwsTokenStore(rdb))
		dwsclient.SetSDKSelector(opts.RuntimeConfig.useDWSForTag)
		h.FCE2BLauncher.Runner = service.NewFCE2BRolloutRunner(opts.RuntimeConfig.fcE2BSDKRollout)
		h.SetConfigProvider(opts.RuntimeConfig.handlerConfig)
		h.SetDingTalkAccountBindingOriginProvider(opts.RuntimeConfig.dbaseBindingOrigin)
		h.FCE2BLauncher.ConfigProvider = opts.RuntimeConfig.fce2b
		h.TaskService.RuntimeStartRecoveryConfig = func() service.RuntimeStartRecoveryConfig {
			return opts.RuntimeConfig.quickWins()
		}
	}
	h.FCE2BLauncher.SetSandboxRelaySigner(opts.SandboxRelaySigner)
	asbRuntime, err := service.NewASBEnterpriseRuntimeFromConfig(
		queries,
		h.TaskService,
		h.FCE2BLauncher,
		pool,
		signupConfig.ASB,
		signupConfig.EnterpriseIdentity,
	)
	if err != nil {
		slog.Error(
			"ASB enterprise runtime disabled due to invalid configuration",
			"error", err,
			"fc_e2b_available", true,
		)
	}
	if asbRuntime != nil {
		h.ASBLauncher = asbRuntime.Launcher
		// Every cold launch, including initial task admission, uses the same
		// tenant cooldown. A missing/unavailable Redis keeps tasks queued.
		h.ASBLauncher.Credentials.CapacityGate = service.NewASBCapacityGate(rdb)
		h.EnterpriseIdentity = asbRuntime.Identity
		if opts.RuntimeConfig != nil {
			if err := asbRuntime.SetConfigProviders(opts.RuntimeConfig.asb, opts.RuntimeConfig.enterpriseIdentity); err != nil {
				slog.Error("ASB enterprise runtime dynamic configuration failed", "error", err)
				os.Exit(1)
			}
		}
	}
	h.FCE2BStable = service.NewFCE2BStableService(
		pool,
		h.FCE2BLauncher,
		stableRuntimePublishers,
		h.ASBLauncher,
	)
	if opts.RuntimeConfig != nil {
		h.FCE2BStable.DeveloperUserIDsProvider = opts.RuntimeConfig.stablePublisherUserIDs
	}
	h.EventTriggers = service.NewEventTriggerService(pool, h.AutopilotService)
	h.MessageAutomations = &service.MessageAutomationService{Pool: pool, Autopilot: h.AutopilotService}
	h.TaskService.RuntimeLauncher = service.NewCloudSandboxLauncher(
		queries,
		h.FCE2BLauncher,
		h.ASBLauncher,
	)
	if managed, managedErr := managedagent.New(queries, pool, managedagent.ConfigFromEnv(), slog.Default()); managedErr != nil {
		slog.Error("managed FDE Agent source disabled due to invalid configuration", "error", managedErr)
	} else {
		h.ManagedAgent = managed
	}
	h.Metrics = opts.BusinessMetrics
	var agentMessageRouterClient *agentmessagerouter.Client
	var agentDispatchEndpoints *agentmessagerouter.DispatchEndpointService
	routerClientConfig := agentmessagerouter.ClientConfig{
		BaseURL:           strings.TrimSpace(os.Getenv("AGENT_MESSAGE_ROUTER_INTERNAL_URL")),
		ServiceCredential: strings.TrimSpace(os.Getenv("AGENT_MESSAGE_ROUTER_SERVICE_CREDENTIAL")),
	}
	if opts.RuntimeConfig != nil {
		routerClientConfig.BaseURLProvider = opts.RuntimeConfig.agentMessageRouterInternalURL
	}
	routerClient, routerClientErr := agentmessagerouter.NewClient(routerClientConfig)
	if routerClientErr == nil {
		agentMessageRouterClient = routerClient
		h.AgentMessageRouterLLMTrace = routerClient
		h.TaskCompletionWorker = agentmessagerouter.NewCompletionWorker(
			queries,
			routerClient,
			h.TaskService,
		)
		h.TaskCompletionTargetIdentity = routerClient.TargetIdentity()
		h.TaskService.CompletionNotifier = h.TaskCompletionWorker
		h.EventTriggers.Router = routerClient
		h.DingTalkResponsePolicySync = newDingTalkResponsePolicyWorker(queries, routerClient)
		h.DingTalkResponsePolicyNotifier = h.DingTalkResponsePolicySync
		h.DingTalkBindingTeardownRouter = routerClient
	}
	dispatchKeysRaw := strings.TrimSpace(os.Getenv("MULTICA_AGENT_DISPATCH_KEYS"))
	dispatchCurrentKeyID := strings.TrimSpace(os.Getenv("MULTICA_AGENT_DISPATCH_CURRENT_KEY_ID"))
	if dispatchKeysRaw == "" && dispatchCurrentKeyID == "" {
		slog.Info("agent dispatch credential derivation disabled (keyring not set)")
	} else if keyring, err := agentmessagerouter.ParseDispatchKeyring(dispatchKeysRaw, dispatchCurrentKeyID); err != nil {
		slog.Error("agent dispatch credential derivation disabled", "error", err)
	} else {
		keyring.SetMetrics(opts.BusinessMetrics)
		h.AgentDispatchKeys = keyring
		slog.Info("MULTICA_AGENT_DISPATCH_KEYRING",
			"currentKeyID", keyring.CurrentKeyID(),
			"keyFingerprints", keyring.KeyFingerprints())
		dbaseBindingURL := strings.TrimSpace(os.Getenv("DINGTALK_DBASE_BINDING_PAGE_URL"))
		dbaseBindingOrigin := strings.TrimSpace(os.Getenv("DINGTALK_DBASE_BINDING_ORIGIN"))
		if opts.RuntimeConfig != nil {
			dbaseBindingURL = opts.RuntimeConfig.dbaseBindingPageURL()
			dbaseBindingOrigin = opts.RuntimeConfig.dbaseBindingOrigin()
		}
		dbaseOrigin, originErr := handler.NormalizeDingTalkAccountBindingOrigin(
			dbaseBindingOrigin,
		)
		endpointServiceConfig := agentmessagerouter.DispatchEndpointServiceConfig{
			PublicBaseURL: signupConfig.PublicURL,
			Keyring:       keyring,
			Random:        rand.Reader,
		}
		if opts.RuntimeConfig != nil {
			endpointServiceConfig.PublicBaseURLProvider = opts.RuntimeConfig.publicURL
		}
		endpointService, endpointErr := agentmessagerouter.NewDispatchEndpointService(
			agentmessagerouter.NewDBDispatchEndpointStore(queries),
			endpointServiceConfig,
		)
		if routerClientErr == nil && endpointErr == nil {
			agentDispatchEndpoints = endpointService
		}
		bindingServiceConfig := agentmessagerouter.ServiceConfig{
			ResponsePolicyNotifier: h.DingTalkResponsePolicyNotifier,
			PublicBaseURL:          signupConfig.PublicURL,
			DBaseBindingURL:        dbaseBindingURL,
			Keyring:                keyring,
			Random:                 rand.Reader,
			IdentityStore:          queries,
			Endpoints:              endpointService,
			Metrics:                opts.BusinessMetrics,
		}
		if opts.RuntimeConfig != nil {
			bindingServiceConfig.PublicBaseURLProvider = opts.RuntimeConfig.publicURL
			bindingServiceConfig.DBaseBindingURLProvider = opts.RuntimeConfig.dbaseBindingPageURL
		}
		bindingService, serviceErr := agentmessagerouter.NewService(queries, routerClient, bindingServiceConfig)
		if routerClientErr != nil || endpointErr != nil || serviceErr != nil {
			slog.Error("digital employee binding disabled due to invalid core configuration",
				"router_error", routerClientErr,
				"endpoint_error", endpointErr,
				"service_error", serviceErr,
			)
		} else {
			h.DigitalEmployeeBindingMCPBindings = bindingService
			slog.Info("digital employee MCP binding enabled")
			if originErr != nil || !dBaseBindingURLMatchesOrigin(dbaseBindingURL, dbaseOrigin) {
				slog.Error("dingtalk browser account binding disabled due to invalid DBase configuration",
					"origin_error", originErr,
				)
			} else {
				h.DingTalkAccountBindings = bindingService
				h.DingTalkAccountBindingOrigin = dbaseOrigin
				slog.Info("dingtalk browser account binding enabled")
			}
		}
	}
	if agentBaseURL := strings.TrimSpace(os.Getenv("DINGTALK_AGENT_BASE_URL")); agentBaseURL != "" {
		agentClient := dingtalk.NewAgentClient(dingtalk.AgentClientConfig{
			BaseURL:        agentBaseURL,
			InternalSecret: strings.TrimSpace(os.Getenv("DINGTALK_AGENT_INTERNAL_SECRET")),
			Logger:         slog.Default(),
		})
		h.DingTalk = agentClient
		h.DingTalkNotifications = agentClient
		h.DingTalkOAuth = agentClient
		if h.DingTalk.IsConfigured() {
			slog.Info("dingtalk integration enabled via private agent", "base_url", agentBaseURL)
		} else {
			slog.Info("dingtalk integration disabled (DINGTALK_AGENT_INTERNAL_SECRET not set)")
		}
	} else {
		// AppKey/AppSecret and ClientId/ClientSecret are the same credential
		// pair under the DingTalk console's old and new naming, so the direct
		// client accepts either: deployments already configured for login get
		// directory search and group capabilities without duplicating secrets.
		appKey := strings.TrimSpace(os.Getenv("DINGTALK_APP_KEY"))
		if appKey == "" {
			appKey = strings.TrimSpace(os.Getenv("DINGTALK_CLIENT_ID"))
		}
		appSecret := strings.TrimSpace(os.Getenv("DINGTALK_APP_SECRET"))
		if appSecret == "" {
			appSecret = strings.TrimSpace(os.Getenv("DINGTALK_CLIENT_SECRET"))
		}
		if appKey != "" && appSecret != "" {
			client := dingtalk.NewClient(dingtalk.Config{
				AppKey:      appKey,
				AppSecret:   appSecret,
				OpenAPIBase: strings.TrimSpace(os.Getenv("DINGTALK_OPENAPI_BASE")),
				OAPIBase:    strings.TrimSpace(os.Getenv("DINGTALK_OAPI_BASE")),
				TOPBase:     strings.TrimSpace(os.Getenv("DINGTALK_TOP_BASE")),
				Logger:      slog.Default(),
			})
			h.DingTalk = client
			h.DingTalkOAuth = client
			slog.Info("dingtalk integration enabled via direct client")
		} else {
			slog.Info("dingtalk integration disabled (DINGTALK_CLIENT_ID or DINGTALK_CLIENT_SECRET not set)")
		}
	}
	if h.DingTalkNotifications != nil && h.DingTalkNotifications.IsConfigured() {
		registerDingTalkNotificationListeners(bus, queries, h.DingTalkNotifications, signupConfig.AppURL, appURLProvider)
		slog.Info("dingtalk personal notification listener enabled")
	}
	// Lark (Feishu) login identity resolution — same tiering as DingTalk:
	// prefer the private channel agent so the app secret stays outside this
	// backend, fall back to the direct open-API client for self-host/dev.
	if larkAgentBase := strings.TrimSpace(os.Getenv("LARK_AGENT_BASE_URL")); larkAgentBase != "" {
		agentClient := lark.NewOAuthAgentClient(lark.OAuthAgentClientConfig{
			BaseURL:        larkAgentBase,
			InternalSecret: strings.TrimSpace(os.Getenv("LARK_AGENT_INTERNAL_SECRET")),
			Logger:         slog.Default(),
		})
		h.LarkOAuth = agentClient
		if agentClient.IsConfigured() {
			slog.Info("lark oauth enabled via private agent", "base_url", larkAgentBase)
		} else {
			slog.Info("lark oauth disabled (LARK_AGENT_INTERNAL_SECRET not set)")
		}
	} else if larkClientID, larkClientSecret := strings.TrimSpace(os.Getenv("LARK_CLIENT_ID")), strings.TrimSpace(os.Getenv("LARK_CLIENT_SECRET")); larkClientID != "" && larkClientSecret != "" {
		h.LarkOAuth = lark.NewOAuthHTTPClient(lark.OAuthConfig{
			ClientID:     larkClientID,
			ClientSecret: larkClientSecret,
			APIBase:      strings.TrimSpace(os.Getenv("LARK_OPENAPI_BASE")),
			Logger:       slog.Default(),
		})
		slog.Info("lark oauth enabled via direct client")
	} else {
		slog.Info("lark oauth disabled (LARK_CLIENT_ID or LARK_CLIENT_SECRET not set)")
	}
	h.FeatureFlags = opts.FeatureFlags
	if rdb != nil {
		h.InternalConnectorRedis = rdb
	}
	h.InternalConnectorClient = handler.NewInternalConnectorClient()
	connectorKey, connectorKeySource, connectorKeyErr := internalConnectorCredentialKey()
	if connectorKeyErr != nil {
		if featureflags.DeploymentEnvironment() == "pre" || connectorKeySource == "jwt-derived" {
			slog.Warn("internal connector credential storage unavailable", "source", connectorKeySource, "error", connectorKeyErr)
		} else {
			slog.Info("internal connector credential storage unavailable", "source", connectorKeySource, "error", connectorKeyErr)
		}
	} else if box, err := secretbox.New(connectorKey); err != nil {
		slog.Warn("internal connector credential key invalid", "error", err)
	} else {
		h.InternalConnectorSecretBox = box
		slog.Info("internal connector credential storage configured", "source", connectorKeySource)
	}
	if relay, err := handler.NewSemanticaMCPRelayFromEnv(rdb); err != nil {
		slog.Warn("Semantica MCP relay disabled", "error", err)
	} else {
		h.SemanticaMCPRelay = relay
	}
	h.TaskService.FeatureFlags = opts.FeatureFlags
	h.TaskService.Metrics = opts.BusinessMetrics
	h.IssueService.Metrics = opts.BusinessMetrics
	if opts.BusinessMetrics != nil {
		// Wire the BusinessMetrics receiver into the cloud runtime client
		// so every outbound Fleet/Gateway request feeds the
		// multica_cloudruntime_request_* histograms.
		if client, ok := h.CloudRuntime.(*cloudruntime.Client); ok {
			client.SetRecorder(opts.BusinessMetrics)
		}
	}
	if opts.DaemonWakeup != nil {
		h.TaskService.Wakeup = opts.DaemonWakeup
		if notifier, ok := opts.DaemonWakeup.(handler.RuntimeProfileRefreshNotifier); ok {
			h.DaemonProfileRefresh = notifier
		}
		if notifier, ok := opts.DaemonWakeup.(handler.WorkspaceSetRefreshNotifier); ok {
			h.DaemonWorkspaceRefresh = notifier
		}
		if notifier, ok := opts.DaemonWakeup.(handler.DaemonPendingWorkNotifier); ok {
			h.DaemonPendingWork = notifier
		}
	}
	if rdb != nil {
		h.UpdateStore = handler.NewRedisUpdateStore(rdb)
		h.ModelListStore = handler.NewRedisModelListStore(rdb)
		h.ModelCatalogCache = handler.NewRedisModelCatalogCache(rdb)
		h.LocalSkillListStore = handler.NewRedisLocalSkillListStore(rdb)
		h.LocalSkillImportStore = handler.NewRedisLocalSkillImportStore(rdb)
		h.LivenessStore = handler.NewRedisLivenessStore(rdb)
		h.WebhookRateLimiter = handler.NewRedisWebhookRateLimiter(rdb, handler.DefaultWebhookRateLimit())
		h.WebhookIPRateLimiter = handler.NewRedisWebhookIPRateLimiter(rdb, handler.DefaultWebhookIPRateLimit())
		h.WebhookAbsoluteIPRateLimiter = handler.NewRedisWebhookAbsoluteIPRateLimiter(rdb, handler.DefaultWebhookAbsoluteIPRateLimit())
	}

	// Channel engine (MUL-3620): the platform-agnostic inbound runtime.
	// Built UNCONDITIONALLY — it drives any channel.Channel, not just
	// Feishu, so it must not depend on the Lark master key (a future
	// Slack-only deployment has no Lark key). Platform adapters register a
	// Factory + ResolverSet into it below; the Supervisor enumerates active
	// installations across ALL channel types and routes each to its
	// registered platform's Factory. Installations whose channel_type has no
	// registered Factory are skipped by the Supervisor — either no platform is
	// configured, or (Slack/B2) the platform drives ONE deployment-level
	// connection of its own outside the per-installation supervisor. The Router
	// is the single shared inbound handler injected into every Channel.
	channelRegistry := channel.NewRegistry()
	channelSupervisorConfig := engine.Config{}
	dingtalkStreamCoordinator, dingtalkStreamTarget, dingtalkStreamCoordinated, coordinatorErr := dingTalkStreamCoordinatorFromEnv(rdb)
	if coordinatorErr != nil {
		// NewRouterWithOptions predates startup-error returns. Panicking here is
		// intentional: an explicit Redis ownership setting must never leave the
		// process healthy with its DingTalk connector silently disabled.
		slog.Error("dingtalk stream coordinator startup configuration failed",
			"event", "dingtalk_stream_coordinator_startup_failed",
			"error_class", "configuration",
			"error", coordinatorErr,
		)
		panic(coordinatorErr)
	}
	if dingtalkStreamCoordinated {
		channelSupervisorConfig.StreamCoordinators = map[channel.Type]engine.StreamCoordinator{
			dingtalk.TypeDingtalk: dingtalkStreamCoordinator,
		}
		slog.Info("dingtalk stream coordinator configured",
			"event", "dingtalk_stream_coordinator_configured",
			"coordination_mode", "redis",
			"target_ready", dingtalkStreamTarget,
			"lease_ttl_ms", dingtalkStreamCoordinator.LeaseTTL().Milliseconds(),
		)
	} else {
		slog.Info("dingtalk stream coordinator using rollout-safe singleton lease",
			"event", "dingtalk_stream_coordinator_configured",
			"coordination_mode", "postgres_singleton",
			"target_ready", 1,
		)
	}
	channelRouter := engine.NewRouter(h.IssueService, h.TaskService, queries, engine.RouterConfig{Logger: slog.Default()})
	coordinator := inboundcoord.New(h.LLM, queries, h.Assoc)
	if opts.RuntimeConfig != nil {
		coordinator.ModelProvider = func() string { return opts.RuntimeConfig.current().Runtime.LLM.CoordinatorModel }
		performanceAgent := func(agentID pgtype.UUID) bool {
			raw := opts.RuntimeConfig.current().Runtime
			if raw.PerformanceOptimization != nil {
				return raw.PerformanceOptimization.AllowsAgent(util.UUIDToString(agentID))
			}
			return false
		}
		// A collect-window read happens before the claim; the claimed
		// decision takes the switch from its own single snapshot below.
		coordinator.HistoryPrefetchAgentProvider = performanceAgent
		coordinator.DecisionConfigProvider = func(agentID pgtype.UUID) inboundcoord.DecisionConfig {
			return opts.RuntimeConfig.coordinatorDecisionConfig(util.UUIDToString(agentID))
		}
		coordinator.BuildID = version + "@" + commit
	}
	if opts.DeploymentFence != nil {
		coordinator.Ready = func(ctx context.Context) (bool, error) {
			return opts.DeploymentFence.AllLiveReplicasSupport(ctx, inboundcoord.ReplicaPlanMarker)
		}
	}
	h.ConfigureGlobalModels(pool)
	if opts.RuntimeConfig != nil {
		opts.RuntimeConfig.models = h.Models
	}
	coordinator.RouteProvider = h.Models.CoordinatorSnapshot
	coordinator.SetIssueCommentWriter(handler.NewInboundCoordinatorIssueCommentWriter(h))
	coordinator.DWSHistory = inboundcoord.NewDWSHistoryLoader(inboundcoord.DWSHistoryConfig{
		MCPBaseURL:            strings.TrimSpace(os.Getenv("MULTICA_DWS_HISTORY_MCP_URL")),
		CrossOrgRenewAgentIDs: strings.Split(os.Getenv("MULTICA_DWS_HISTORY_CROSS_ORG_RENEW_AGENT_IDS"), ","),
		AgentIdentity:         agentidentityhsf.NewClient(),
		BaseURL:               signupConfig.FCE2B.AgentIdentityControlBaseURL,
		BaseURLProvider:       agentIdentityControlBaseURLProvider,
		ClientSecret:          signupConfig.FCE2B.DWSClientSecret,
	})
	if h.TaskCompletionWorker != nil {
		h.TaskCompletionWorker.SetDWSReplySender(agentmessagerouter.NewDWSReplySender(agentmessagerouter.DWSReplySenderConfig{
			AgentIdentity: agentidentityhsf.NewClient(),
			Redeemer: dwsclient.Redeemer{
				BaseURL:         signupConfig.FCE2B.AgentIdentityControlBaseURL,
				BaseURLProvider: agentIdentityControlBaseURLProvider,
			},
			ClientSecret: signupConfig.FCE2B.DWSClientSecret,
		}))
	}
	h.InboundCoordinator = coordinator
	if opts.RuntimeConfig != nil {
		h.CoordinatorCollectQuiet = func(agentID pgtype.UUID) time.Duration {
			return opts.RuntimeConfig.coordinatorCollectQuiet(util.UUIDToString(agentID))
		}
	}
	h.InboundCoordinatorWorker = handler.NewInboundCoordinatorJobWorker(h)
	decisionMCP := strings.TrimSpace(os.Getenv("MULTICA_DWS_HISTORY_MCP_URL"))
	decisionEnv := "production"
	if strings.Contains(decisionMCP, "pre-mcp.") {
		decisionEnv = "staging"
	}
	h.UserDecisions = &userdecision.Service{Pool: pool, Store: &userdecision.Store{DB: pool, Environment: decisionEnv, Blobs: h.Storage}, Transport: dingtalkresponse.NewDecisionTransport(dingtalkresponse.DWSConfig{AgentIdentity: agentidentityhsf.NewClient(), BaseURL: signupConfig.FCE2B.AgentIdentityControlBaseURL, BaseURLProvider: agentIdentityControlBaseURLProvider, ClientSecret: signupConfig.FCE2B.DWSClientSecret}, decisionMCP), Wake: h.InboundCoordinatorWorker.Notify}
	// With runtime.use_dws_for_tag, card actions arrive over the server's
	// DWS event source: one event stream per identity across replicas.
	if rdb != nil {
		if sessions, mint, ok := dingtalkresponse.DecisionSessions(h.UserDecisions.Transport); ok {
			decisions := h.UserDecisions
			source, err := dwseventsource.New(dwseventsource.Config{
				Redis: rdb, Sessions: sessions, Mint: mint,
				Deployment: strings.TrimRight(strings.TrimSpace(os.Getenv("MULTICA_PUBLIC_URL")), "/"),
				Enabled:    opts.RuntimeConfig.useDWSForTag,
				Consumers: []dwseventsource.Consumer{{
					EventKey: dws.EventCardAction,
					Identities: func(ctx context.Context) ([]dwsclient.Identity, error) {
						requests, err := decisions.ConsumerIdentities(ctx)
						ids := make([]dwsclient.Identity, 0, len(requests))
						for _, r := range requests {
							ids = append(ids, dwsclient.Identity{AgentID: r.AgentID, UID: r.SenderUID, OrgID: r.SenderOrgID})
						}
						return ids, err
					},
					Handle: func(ctx context.Context, id dwsclient.Identity, line []byte) error {
						return decisions.HandleCardEvent(ctx, id.AgentID, id.UID, id.OrgID, line)
					},
				}},
			})
			if err != nil {
				slog.Error("DWS event source disabled", "event", "dws_event_source_disabled", "error", err)
			} else {
				h.DWSEvents = source
				dingtalkresponse.SetDecisionEventConnections(h.UserDecisions.Transport, source)
			}
		}
	}
	h.UserDecisions.NotifyAlert = func(alert userdecision.Alert) {
		var item map[string]any
		if json.Unmarshal(alert.Item, &item) == nil {
			bus.Publish(events.Event{Type: protocol.EventInboxNew, WorkspaceID: alert.WorkspaceID, ActorType: "system", Payload: map[string]any{"item": item}})
		}
	}
	h.UserDecisions.Resolve = func(ctx context.Context, r userdecision.Request) (json.RawMessage, json.RawMessage, error) {
		raw, err := h.UserDecisions.Store.Snapshot(ctx, r)
		if err != nil {
			return nil, nil, err
		}
		var snapshot inboundcoord.UserDecisionSnapshot
		if err = json.Unmarshal(raw, &snapshot); err != nil {
			return nil, nil, err
		}
		if r.Submission == nil {
			return nil, nil, fmt.Errorf("missing accepted submission")
		}
		plan, resolutionAudit, err := coordinator.ResolveUserDecisionWithAudit(ctx, snapshot, *r.Submission)
		audit := map[string]any{"resolved_plan": plan, "proposal_model": snapshot.Proposal.Model, "resolution": resolutionAudit}
		if err != nil {
			audit["error"] = err.Error()
		}
		interpretation, _ := json.Marshal(audit)
		if err != nil {
			return nil, interpretation, err
		}
		raw, err = json.Marshal(plan)
		return raw, interpretation, err
	}

	if agentMessageRouterClient != nil {
		h.DingTalkResponses = dingtalkresponse.NewService(pool, dingtalkresponse.NewDWSProvider(dingtalkresponse.DWSConfig{
			AgentIdentity:   agentidentityhsf.NewClient(),
			BaseURL:         signupConfig.FCE2B.AgentIdentityControlBaseURL,
			BaseURLProvider: agentIdentityControlBaseURLProvider,
			ClientSecret:    signupConfig.FCE2B.DWSClientSecret,
		}), handler.RouterResponseReceiptSender{Client: agentMessageRouterClient, Handler: h})
		h.TaskCompletionWorker.ResponseActions = h
		h.DingTalkResponses.OnSandboxDelivered = h.BindVerifiedDingTalkSend
	}
	h.SceneMemoryStore = scenememory.NewStore(queries)
	coordinator.SceneMemory = h.SceneMemoryStore
	sceneFlusher := &scenememory.MemoryFlusher{
		Store: h.SceneMemoryStore,
		History: scenememory.NewDWSRangeReader(scenememory.DWSRangeConfig{
			Queries:         queries,
			AgentIdentity:   agentidentityhsf.NewClient(),
			BaseURL:         signupConfig.FCE2B.AgentIdentityControlBaseURL,
			BaseURLProvider: agentIdentityControlBaseURLProvider,
			ClientSecret:    signupConfig.FCE2B.DWSClientSecret,
		}),
		LLM:    h.LLM,
		Agents: queries,
	}
	// Langfuse tracing: the coordinator loop, the memory loop, and the agent
	// task lifecycle share one exporter. The sandbox LLM relay fans out to it
	// too, and capable FC runtime images capture model bodies for every task
	// once the exporter exists (docs/langfuse-observability.md).
	if opts.Langfuse.Enabled() {
		coordinator.Langfuse = opts.Langfuse
		h.UserDecisions.Observe = coordinator.ObserveUserDecision
		sceneFlusher.Langfuse = opts.Langfuse
		h.TaskService.Langfuse = opts.Langfuse
		h.LLMTraceObserver = handler.NewLangfuseLLMTraceObserver(opts.Langfuse)
		if h.FCE2BLauncher != nil {
			h.FCE2BLauncher.LLMTraceCaptureAlways = true
		}
	}
	h.SceneMemoryWorker = scenememory.NewWorker(h.SceneMemoryStore, sceneFlusher, func() bool {
		if opts.DeploymentFence == nil {
			return true
		}
		return opts.DeploymentFence.Snapshot().State == deploymentfence.StateNormal
	})
	channelRouter.SetInboundCoordinator(coordinator)
	channelRouter.SetSceneAssociator(h.Assoc)
	// So an inbound DingTalk/Slack/Lark message appears in a web client
	// watching the same chat without a reload: the engine writes through the
	// service layer and inherits no handler broadcast of its own.
	channelRouter.SetEventBus(bus)
	// Debounce the per-session run trigger so a burst of messages collapses
	// into one agent run instead of one per message (MUL-2968).
	channelRouter.EnableRunBatching(engine.DefaultChatRunBatchWindow)
	h.ChannelRouter = channelRouter
	// Media intent-ledger reconciler: settles uploaded-but-unbound objects.
	// Built ONLY when a storage backend exists — store is nil when S3 is not
	// configured and the local upload dir failed to initialize, and a
	// reconciler with nil Storage would panic the worker goroutine on the
	// first unreferenced row (ledger rows can pre-exist from a boot where
	// storage WAS configured). Without storage the resolver skips every
	// upload, so no new rows appear and the ledger simply waits for a boot
	// with working storage. Started from main.go as its own worker.
	if store != nil {
		h.ChannelMediaReconciler = &service.ChannelMediaReconciler{
			Queries: queries,
			Storage: store,
			Logger:  slog.Default(),
		}
	}
	h.ChannelSupervisor = engine.NewSupervisor(
		lark.NewChannelInstallationStore(queries),
		channelRegistry,
		channelRouter.Handle,
		channelSupervisorConfig,
	)
	// A freshly-installed bot must not wait out the sweep PollInterval
	// (default 30s) before its connection opens: platform pushes in that
	// window are silently dropped (Stream/WS transports do not queue for
	// absent subscribers), which reads as "the bot ignored my first
	// message". Kick the Supervisor at every installation-lifecycle commit
	// so connect (and revoke teardown) happens immediately.
	channelKick := func(events.Event) { h.ChannelSupervisor.Kick() }
	for _, evt := range []string{
		protocol.EventDingTalkInstallationCreated,
		protocol.EventDingTalkInstallationRevoked,
		protocol.EventLarkInstallationCreated,
		protocol.EventLarkInstallationRevoked,
	} {
		bus.Subscribe(evt, channelKick)
	}

	// Lark integration. Only wired when MULTICA_LARK_SECRET_KEY is set:
	// the InstallationService refuses to fall back to plaintext storage
	// for app_secret, and the BindingTokenService cannot mint usable
	// tokens without it either. When the key is absent the Lark
	// handlers return 503 with a clear message; the rest of the server
	// continues to start so self-host deployments that have not opted
	// in to Lark are unaffected. Feishu registers its Factory + ResolverSet
	// into the channel engine above.
	if larkKey, err := secretbox.LoadKey("MULTICA_LARK_SECRET_KEY"); err == nil {
		box, err := secretbox.New(larkKey)
		if err != nil {
			slog.Error("lark: secretbox.New failed; lark integration disabled", "error", err)
		} else {
			installSvc, err := lark.NewInstallationService(queries, box)
			if err != nil {
				slog.Error("lark: InstallationService init failed; lark integration disabled", "error", err)
			} else {
				h.LarkInstallations = installSvc
				h.LarkBindingTokens = lark.NewBindingTokenService(queries, pool)
				slog.Info("lark integration enabled")

				// APIClient: wire the real Lark Open Platform HTTP client
				// (IM v1 send/patch + binding-prompt + bot info). Setting
				// MULTICA_LARK_SECRET_KEY is the operator's opt-in for
				// the integration as a whole; we don't expose a separate
				// "HTTP enabled" knob because the inbound dispatcher
				// without outbound replies is not a useful production
				// state, and CI / integration tests that want to avoid
				// real Lark traffic can point MULTICA_LARK_HTTP_BASE_URL
				// at a mock server.
				//
				// MULTICA_LARK_HTTP_BASE_URL is an OPTIONAL deployment-wide
				// override. Normal operation leaves it empty: each call then
				// resolves its open-platform host from the installation's
				// region (open.feishu.cn vs open.larksuite.com), so one
				// deployment serves both clouds. Set it only to force every
				// installation onto one host — a proxy, a mock for tests, or
				// a single-cloud staging setup.
				larkClient := lark.NewHTTPAPIClient(lark.HTTPClientConfig{
					BaseURL: strings.TrimSpace(os.Getenv("MULTICA_LARK_HTTP_BASE_URL")),
					Logger:  slog.Default(),
				})
				h.LarkAPIClient = larkClient

				// Channel-backed store: routes the lark package's DB seams
				// onto the channel_* tables (MUL-3515). Interface-wired
				// consumers (patcher, typing indicator, dispatcher, hub,
				// backfills) take it directly; the constructor-based services
				// wrap *db.Queries internally, so they keep taking queries.
				cs := lark.NewChannelStore(queries)
				patcher := lark.NewPatcher(cs, installSvc, larkClient, lark.PatcherConfig{})
				patcher.Register(bus)

				// Typing indicator: shows a "processing" reaction on the user's
				// message while the agent is working, then removes it before the
				// reply is sent. Best-effort; failures are logged only.
				typingIndicator := lark.NewTypingIndicatorManager(larkClient, installSvc, cs, slog.Default())
				// Pending reactions live in the DB, not in this process: the
				// replica that clears one is whichever served the daemon's
				// completion POST, not the lease holder that ingested.
				typingIndicator.SetStore(queries)
				patcher.SetTypingIndicatorManager(typingIndicator)

				// Inbound pipeline seams: lark_inbound_audit logger and the
				// shared channel-agnostic chat-session service. They back the
				// Feishu ResolverSet that the engine.Router runs through,
				// sharing the same IssueService + TaskService that back HTTP, so
				// /issue-created issues share counter, dup guard, project
				// boundary, broadcast, analytics and agent-enqueue with the rest
				// of the product. Feishu is just another consumer of the shared
				// engine.ChatSession (channel_type-keyed); the Lark session
				// titles preserve the pre-cutover wording.
				auditLogger := lark.NewAuditLogger(queries)
				feishuSession := engine.NewChatSession(queries, pool, channel.TypeFeishu, engine.SessionTitles{
					Group:    "Lark group chat",
					Direct:   "Lark direct message",
					Fallback: "Lark chat",
				})

				// OutcomeReplier wires the outbound side: NeedsBinding /
				// AgentOffline / AgentArchived / issue-created translate to a
				// Lark-side reply card. Requires the real APIClient and the
				// binding token service; otherwise it falls back to the noop
				// replier (outcomes logged, not delivered). We only register
				// it on the ResolverSet when it can actually deliver, so a
				// pre-outbound deployment pays no reply-goroutine cost.
				replier := lark.NewLarkOutcomeReplier(lark.OutcomeReplierConfig{
					APIClient:      larkClient,
					BindingSvc:     h.LarkBindingTokens,
					Credentials:    installSvc,
					Queries:        queries,
					AppURL:         signupConfig.AppURL,
					AppURLProvider: appURLProvider,
					Logger:         slog.Default(),
				})
				var resolverReplier lark.OutcomeReplier
				if larkClient.IsConfigured() {
					resolverReplier = replier
				}

				// Run cards: issue tasks born from a Lark `/issue` get one
				// interactive card per run — sent when the task enqueues,
				// patched on every status transition plus a throttled
				// task:message cadence, and finalized on the terminal
				// event. The terminate button round-trips through
				// card.action.trigger on the same long connection (the
				// developer console must enable that callback under
				// 事件与回调 → 回调配置 → 使用长连接接收回调).
				runCards := lark.NewRunCardPublisher(cs, installSvc, larkClient, lark.RunCardPublisherConfig{
					AppURL:         signupConfig.AppURL,
					AppURLProvider: appURLProvider,
					Logger:         slog.Default(),
				})
				runCards.Register(bus)
				var cardSink lark.CardActionSink
				if cardActions, caErr := lark.NewRunCardActionHandler(lark.RunCardActionHandlerConfig{
					Queries:   cs,
					Tasks:     h.TaskService,
					Publisher: runCards,
					Replier:   replier,
					Logger:    slog.Default(),
				}); caErr != nil {
					slog.Error("lark: run-card action handler init failed; card actions disabled", "error", caErr)
				} else {
					cardSink = cardActions
				}

				// Feishu adapter (MUL-3620): the WSLongConnConnector talks
				// Lark's long-conn protocol over gorilla/websocket and wraps
				// every read with a ctx-cancel watchdog so lease loss /
				// shutdown breaks the blocking ReadMessage in bounded time —
				// the invariant §4.4 leans on. If the endpoint fetcher fails
				// to initialize (bad MULTICA_LARK_CALLBACK_BASE_URL or
				// similar), buildLarkConnector logs and falls back to the
				// NoopConnector so the lease / supervisor lifecycle still runs
				// against real DB rows — inbound messages are silently dropped
				// until the config is fixed, with the boot log labelling the
				// mode "noop".
				//
				// Registering the Factory (connect/send) + ResolverSet
				// (inbound pipeline seams) is all it takes to add the platform
				// to the engine — no engine edit.
				connector, connectorLabel := buildLarkConnector(installSvc, larkClient, cardSink)
				lark.RegisterFeishu(channelRegistry, lark.FeishuChannelDeps{
					Connector:   connector,
					APIClient:   larkClient,
					Credentials: installSvc,
					Logger:      slog.Default(),
				})
				mediaResolver := lark.NewFeishuMediaResolver(larkClient, installSvc, store, engine.NewDBMediaIntentLedger(queries), slog.Default())
				channelRouter.Register(channel.TypeFeishu, lark.NewFeishuResolverSet(
					cs, feishuSession, auditLogger, resolverReplier, typingIndicator, mediaResolver,
				))
				slog.Info("lark inbound pipeline wired", "connector", connectorLabel)

				// One-shot union_id backfill for installations created
				// before migration 112 added bot_union_id. Runs off the
				// hot startup path so a slow Lark round-trip cannot block
				// HTTP listener boot. New installs already write
				// bot_union_id during the device-flow finalize, so this
				// is bridge code — it will simply find no rows to update
				// on a fresh deployment and exit. MUL-2671.
				go lark.BackfillBotUnionIDs(context.Background(), cs, larkClient, installSvc, slog.Default())

				// Upgrade repair for deployments that ran the whole
				// integration against Lark international via the deployment-
				// wide base-URL override before per-installation region
				// existed: migration 116 backfilled their rows to 'feishu',
				// so relabel them to 'lark' (their true cloud) before the
				// operator clears the override. No-op on mainland / fresh
				// deployments. Off the hot startup path like the union_id
				// backfill. MUL-3083.
				go lark.BackfillRegionFromLegacyOverride(context.Background(), cs,
					strings.TrimSpace(os.Getenv("MULTICA_LARK_HTTP_BASE_URL")),
					strings.TrimSpace(os.Getenv("MULTICA_LARK_CALLBACK_BASE_URL")),
					slog.Default())

				// Device-flow registration service: end-to-end install
				// pipeline that talks to accounts.feishu.cn (RFC 8628)
				// for the QR-scan handshake and then commits the
				// resulting Bot credentials + the installer's
				// lark_user_binding in one DB transaction. The optional
				// MULTICA_LARK_REGISTRATION_DOMAIN / _LARK_DOMAIN env
				// vars override the protocol hosts for staging / dev.
				regCfg := lark.RegistrationConfig{
					Domain:     strings.TrimSpace(os.Getenv("MULTICA_LARK_REGISTRATION_DOMAIN")),
					LarkDomain: strings.TrimSpace(os.Getenv("MULTICA_LARK_REGISTRATION_LARK_DOMAIN")),
				}
				regClient := lark.NewRegistrationClient(regCfg)
				regSvc, rerr := lark.NewRegistrationService(
					lark.RegistrationServiceConfig{Logger: slog.Default()},
					regClient,
					larkClient,
					queries,
					pool,
					installSvc,
					h.LarkBindingTokens,
				)
				if rerr != nil {
					slog.Error("lark: RegistrationService init failed; install disabled", "error", rerr)
				} else {
					// Publish lark_installation:created at row-commit time so the
					// connection badge refreshes on every workspace client, not just
					// the tab that polls the install status to success.
					regSvc.SetEventBus(bus)
					h.LarkRegistration = regSvc
					slog.Info("lark device-flow install enabled")
				}
			}
		}
	} else {
		slog.Info("lark integration disabled (MULTICA_LARK_SECRET_KEY not set)")
	}

	// DingTalk bot installations — the scan-to-create device flow
	// ("一键创建钉钉应用扫码接入", oapi.dingtalk.com /app/registration/*).
	// Gated on MULTICA_DINGTALK_SECRET_KEY, the at-rest key for the
	// per-installation client_secret. Distinct from the deployment-wide
	// DingTalk capability/OAuth clients wired above: those carry ONE
	// operator-configured corp app, while each installation here is a
	// per-agent app minted in the scanning user's own org. The inbound
	// transport (DingTalk Stream Mode) is a follow-up; installs are
	// created and managed now so credentials are already provisioned
	// when it lands.
	if dtKey, err := secretbox.LoadKey("MULTICA_DINGTALK_SECRET_KEY"); err == nil {
		box, err := secretbox.New(dtKey)
		if err != nil {
			slog.Error("dingtalk: secretbox.New failed; dingtalk bot integration disabled", "error", err)
		} else {
			installSvc, ierr := dingtalk.NewInstallationService(queries, pool, box)
			if ierr != nil {
				slog.Error("dingtalk: InstallationService init failed; dingtalk bot integration disabled", "error", ierr)
			} else {
				h.DingTalkInstallations = installSvc

				// Inbound channel (DingTalk Stream Mode). The rollout starts on the
				// PostgreSQL singleton lease and explicitly cuts over to Redis global
				// coordination. Both modes commit each encrypted callback to the same
				// durable inbox before ACK.
				dtMessenger := dingtalk.NewRobotMessenger(os.Getenv("DINGTALK_OPENAPI_BASE"), os.Getenv("DINGTALK_OAPI_BASE"), nil)
				dtBindingSvc := dingtalk.NewBindingTokenService(queries, pool)
				h.DingTalkBindingTokens = dtBindingSvc
				dtReplier := dingtalk.NewOutboundReplier(dingtalk.OutboundReplierConfig{
					Binding:   dtBindingSvc,
					Messenger: dtMessenger,
					Decrypt:   box.Open,
					// Names the bot in the bind prompt ("要开始与「<bot>」对话…").
					AgentNamer: queries,
					// The bind link (/dingtalk/bind) is a web-app page, so it must
					// use the app URL, NOT MULTICA_PUBLIC_URL. Mirrors Slack/Lark.
					AppURL:         signupConfig.AppURL,
					AppURLProvider: appURLProvider,
					Logger:         slog.Default(),
				})
				// "Processing" emotion on ingested messages, cleared when the
				// reply lands (chat-done / task-failed via Outbound below).
				dtTyping := dingtalk.NewTypingIndicatorManager(dtMessenger, box.Open, queries, slog.Default())
				// Directory auto-bind: an unbound org member is matched to
				// their Multica account by unionid, so the explicit
				// "click to bind" prompt is only the fallback.
				dtAutoBinder := dingtalk.NewAutoBinder(queries, dtMessenger, box.Open, slog.Default())
				channelRouter.Register(dingtalk.TypeDingtalk, dingtalk.NewDingTalkResolverSet(
					queries,
					pool,
					dtReplier,
					dingtalk.NewTypingNotifier(dtTyping),
					dtAutoBinder,
					orgemphsf.NewClient(),
					service.NewExternalAttachmentService(queries, store, nil),
					box.Open,
					dtMessenger,
				))
				dingtalk.NewOutbound(queries, box.Open, dtMessenger, dtTyping, slog.Default()).Register(bus)
				streamInbox := dingtalk.NewStreamInboxWorker(
					pool,
					channelRouter.HandleResult,
					box.Seal,
					box.Open,
					dtTyping,
					slog.Default(),
				)
				h.DingTalkStreamInbox = streamInbox
				dingtalk.RegisterDingTalk(channelRegistry, dingtalk.ChannelDeps{
					Decrypt:     box.Open,
					Logger:      slog.Default(),
					Inbox:       streamInbox,
					OpenAPIBase: os.Getenv("DINGTALK_OPENAPI_BASE"),
					Messenger:   dtMessenger,
				})
				coordinationMode := "postgres_singleton"
				effectiveTarget := 1
				if dingtalkStreamCoordinated {
					coordinationMode = "redis"
					effectiveTarget = dingtalkStreamTarget
				}
				slog.Info("dingtalk inbound pipeline wired",
					"event", "dingtalk_stream_connector_wired",
					"connector", "stream-mode",
					"coordination_mode", coordinationMode,
					"target_ready", effectiveTarget,
				)

				// Device-flow registration. The base URL override exists for
				// staging/mock endpoints. The DingTalk product source is fixed
				// by the registration client so deployments cannot drift.
				regConfig := dingtalk.RegistrationConfig{
					BaseURL:     strings.TrimSpace(os.Getenv("MULTICA_DINGTALK_REGISTRATION_BASE_URL")),
					OutgoingURL: strings.TrimSpace(os.Getenv("MULTICA_DINGTALK_REGISTRATION_OUTGOING_URL")),
				}
				if opts.RuntimeConfig != nil {
					regConfig.BaseURLProvider = opts.RuntimeConfig.dingTalkRegistrationBaseURL
					regConfig.OutgoingURLProvider = opts.RuntimeConfig.dingTalkRegistrationOutgoingURL
				}
				regClient := dingtalk.NewRegistrationClient(regConfig)
				// Verifier: exchange the freshly minted credentials for an app
				// access token before committing them, so a half-created app
				// surfaces as a clean install error instead of a dead row.
				verifier := dingtalk.NewCredentialVerifier(os.Getenv("DINGTALK_OPENAPI_BASE"), nil)
				// The manual install path reuses the same verifier to
				// validate operator-supplied credentials. It is set here
				// (independent of the device-flow RegistrationService below)
				// so manual install keeps working even when the scan flow
				// fails to construct.
				h.DingTalkCredentialVerifier = verifier
				regSvc, rerr := dingtalk.NewRegistrationService(
					dingtalk.RegistrationServiceConfig{
						Logger: slog.Default(),
					},
					regClient,
					installSvc,
					queries,
					verifier,
				)
				if rerr != nil {
					slog.Error("dingtalk: RegistrationService init failed; install disabled", "error", rerr)
				} else {
					if agentMessageRouterClient != nil && agentDispatchEndpoints != nil {
						httpCallbackRouter, callbackRouterErr := agentmessagerouter.NewHTTPCallbackRouterService(
							agentMessageRouterClient,
							agentDispatchEndpoints,
						)
						if callbackRouterErr != nil {
							slog.Error("dingtalk HTTP callback router disabled", "error", callbackRouterErr)
						} else {
							regSvc.SetHTTPCallbackRouter(httpCallbackRouter)
						}
					}
					// Publish dingtalk_installation:created at row-commit time so
					// the connection badge refreshes on every workspace client, not
					// just the tab that polls the install status to success.
					regSvc.SetEventBus(bus)
					h.DingTalkRegistration = regSvc
					slog.Info("dingtalk device-flow install enabled")
				}
			}
		}
	} else {
		slog.Info("dingtalk bot integration disabled (MULTICA_DINGTALK_SECRET_KEY not set)")
	}

	// Slack integration. Multi-tenant B2 model (MUL-3666): Multica hosts ONE
	// Slack app, workspaces self-install via OAuth, and inbound runs on a single
	// deployment-level Socket Mode connection routed by team_id — replacing the
	// stage-3 per-installation connection model (MUL-3516).
	//
	// Two deployment-level env vars gate the two halves:
	//   - MULTICA_SLACK_SECRET_KEY decrypts the per-installation bot token
	//     (xoxb-) stored on the channel_installation row. It gates the inbound
	//     ResolverSet + the outbound reply subscriber, so without it there is no
	//     Slack at all.
	//   - MULTICA_SLACK_APP_TOKEN is the app-level token (xapp-) authorizing the
	//     single Socket Mode connection. It cannot be obtained via OAuth, so it
	//     is a one-time operator config. Without it, inbound is disabled (the
	//     ResolverSet + outbound are still wired so an existing install's replies
	//     keep flowing, but no new events are received).
	//
	// The ResolverSet/Outbound share the same engine.ChatSession, channel_*
	// tables, IssueService and TaskService as Feishu, so /issue, dedup, and
	// run-triggering behave identically. Feishu is untouched. Each Slack
	// installation is a bring-your-own-app (BYO) install carrying its OWN
	// app-level token, so a per-installation Slack Factory is registered and the
	// Supervisor drives one Socket Mode connection per installation (like Feishu).
	if slackKey, err := secretbox.LoadKey("MULTICA_SLACK_SECRET_KEY"); err == nil {
		box, err := secretbox.New(slackKey)
		if err != nil {
			slog.Error("slack: secretbox.New failed; slack integration disabled", "error", err)
		} else {
			// Outbound replier (MUL-3666): delivers NeedsBinding prompt /
			// AgentOffline / AgentArchived / issue-created notices. The binding
			// token service mints the single-use token embedded in the prompt's
			// redeem link; the redeem endpoint (registered below, public) binds
			// the Slack user to their Multica account.
			slackBindingSvc := slack.NewBindingTokenService(queries, pool)
			h.SlackBindingTokens = slackBindingSvc
			slackReplier := slack.NewOutboundReplier(slack.OutboundReplierConfig{
				Binding: slackBindingSvc,
				Decrypt: box.Open,
				// The bind link (/slack/bind) is a web-app page, so it must use the
				// app URL (MULTICA_APP_URL ?? FRONTEND_ORIGIN), NOT MULTICA_PUBLIC_URL
				// (the backend/API URL). Mirrors the Lark replier (appURLFromEnv).
				AppURL:         signupConfig.AppURL,
				AppURLProvider: appURLProvider,
				Logger:         slog.Default(),
			})
			// Typing indicator (MUL-3874): a 👀 reaction on the user's message
			// while the agent works, cleared when the run finishes or fails.
			// Best-effort; failures are logged only. Registered before the
			// outbound reply subscriber so, on EventChatDone, the reaction clears
			// ahead of the reply (bus delivery is synchronous, in subscription
			// order). Subscribing here is also the only path that clears the
			// reaction on a failed run, which the outbound replier does not handle.
			slackTyping := slack.NewTypingIndicatorManager(queries, box.Open, slog.Default())
			slackTyping.Register(bus)
			channelRouter.Register(slack.TypeSlack, slack.NewSlackResolverSet(queries, pool, slackReplier, slackTyping))
			slack.NewOutbound(queries, box.Open, slog.Default()).Register(bus)

			// On-demand history reader behind the unified `multica chat history`
			// command (MUL-3871): pull the session's Slack conversation when the
			// agent asks, instead of force-assembling it on every inbound.
			h.SlackHistory = slack.NewHistory(queries, box.Open, slog.Default())

			// `/issue` slash command (MUL-3908): a real Slack slash command,
			// delivered over the same Socket Mode connection. It is a quick-create
			// entry point — the invoker's natural-language description is enqueued as
			// a quick-create task (no chat session or chat run) and the agent authors
			// the well-formed issue in the background — reusing the shared TaskService
			// + binding service. The invoker gets a private ephemeral acknowledgement
			// and a Multica notification when the issue lands.
			slackSlash := slack.NewSlashCommandProcessor(slack.SlashCommandConfig{
				Queries:        queries,
				Tasks:          h.TaskService,
				Binding:        slackBindingSvc,
				AppURL:         signupConfig.AppURL,
				AppURLProvider: appURLProvider,
				Logger:         slog.Default(),
			})

			// Per-installation inbound: the Supervisor builds + supervises one
			// Socket Mode connection per active Slack installation, authenticated
			// with that installation's OWN app-level token (xapp-, pasted at BYO
			// install) — no deployment-level app token, no single connection.
			slack.RegisterSlack(channelRegistry, slack.ChannelDeps{Decrypt: box.Open, Logger: slog.Default(), Slash: slackSlash})

			// BYO self-serve install (paste bot token + app-level token). The
			// InstallService needs only the at-rest encryption key — there is no
			// hosted OAuth client credential.
			installSvc, ierr := slack.NewInstallService(queries, pool, box, slog.Default())
			if ierr != nil {
				slog.Error("slack: InstallService init failed; install disabled", "error", ierr)
			} else {
				h.SlackInstall = installSvc
			}
			slog.Info("slack integration enabled (BYO per-installation socket mode)")
		}
	} else {
		slog.Info("slack integration disabled (MULTICA_SLACK_SECRET_KEY not set)")
	}

	// WeCom smart-bot integration ("智能机器人" / aibot). Per-installation
	// WebSocket long connection to wss://openws.work.weixin.qq.com; the
	// Supervisor drives one connection per active wecom installation, gated
	// by the shared ws_lease_token so multi-replica deployments still hold
	// at most one active socket per bot (WeCom itself only permits one).
	//
	// Gated by MULTICA_WECOM_SECRET_KEY. Without it, the whole block is
	// skipped and the wecom Web-UI endpoints return 503; existing deployments
	// are unaffected. The smart-bot flow does NOT require any public HTTP
	// callback, so nothing else needs to be exposed to the internet.
	if wecomKey, err := secretbox.LoadKey("MULTICA_WECOM_SECRET_KEY"); err == nil {
		box, err := secretbox.New(wecomKey)
		if err != nil {
			slog.Error("wecom: secretbox.New failed; wecom integration disabled", "error", err)
		} else {
			credsResolver, err := wecom.NewSecretboxCredentialsResolver(box)
			if err != nil {
				slog.Error("wecom: credentials resolver init failed; wecom integration disabled", "error", err)
			} else {
				wecomStore := wecom.NewStore(queries)
				h.WecomStore = wecomStore
				h.WecomCredentials = credsResolver

				// Binding tokens back the per-user "link your Multica account"
				// prompt sent to first-time WeCom senders. aibot userids are
				// anonymized T-prefixed ids with no relation to real userids
				// or emails, so an explicit binding table is the only correct
				// answer — see wecom/binding.go for the rationale.
				wecomBinding := wecom.NewBindingTokenService(queries, pool)
				h.WecomBindingTokens = wecomBinding

				// Senders registry: the wecom OutboundReplier is created here
				// at boot, but the live wsSender it needs to push
				// aibot_send_msg only exists inside a running wecomChannel.
				// wecom.NewSendersRegistry mints a shared map; the
				// ChannelDeps write side and the Replier read side both
				// receive it, and each Channel.Connect self-registers on
				// entry and clears on exit.
				wecomSenders := wecom.NewSendersRegistry()

				wecomReplier := wecom.NewOutboundReplier(wecom.OutboundReplierConfig{
					Binding: wecomBinding,
					Senders: wecomSenders,
					AppURL:  appURLFromEnv(),
					Logger:  slog.Default(),
				})

				// Wecom shares the engine.ChatSession (channel_type-keyed) so
				// /issue, dedup, and run-triggering behave identically across
				// platforms. Session titles use the wecom-flavored wording
				// (Chinese product voice — wecom deployments are China-only).
				wecomSession := engine.NewChatSession(queries, pool, wecom.TypeWecom, engine.SessionTitles{
					Group:    "企业微信群聊",
					Direct:   "企业微信单聊",
					Fallback: "企业微信会话",
				})

				wecom.RegisterWecom(channelRegistry, wecom.ChannelDeps{
					Credentials: credsResolver,
					Senders:     wecomSenders,
					Metrics:     wecomMetricsOrNil(opts.WecomMetrics),
					Logger:      slog.Default(),
				})
				// Inbound media: a callback carries a pre-signed COS url and
				// a per-url key, so the resolver needs no WeCom credential —
				// only somewhere durable to put the bytes. Without an object
				// store there is nothing to point an attachment at, so the
				// resolver is left nil and attachments stay as their
				// placeholder text. Same nil-guard as DingTalk above.
				var wecomMedia engine.MediaResolver
				if store != nil {
					wecomMedia = wecom.NewMediaResolver(
						store,
						engine.NewDBMediaIntentLedger(queries),
						wecomSenders,
						slog.Default(),
					)
				}
				channelRouter.Register(wecom.TypeWecom, wecom.NewResolverSet(
					wecomStore, wecomSession, wecomReplier, wecomMedia,
				))

				// EventChatDone subscriber: pushes the agent's chat reply
				// back over the same aibot WebSocket the inbound loop owns.
				// Mirrors slack.NewOutbound(...).Register(bus). Without it
				// the agent's reply lands only in Multica's web UI — the
				// user in WeCom sees no response.
				//
				// WithAttachments adds the second hop: the files the agent
				// bound to that reply are read back out of object storage and
				// sent into the chat behind it. Passed only when this
				// deployment configured storage — with none there is nothing
				// to read an attachment out of, and the option is what the
				// delivery path checks for.
				//
				// DeclareChannelFileDelivery is the same condition said to the
				// agent: a run only gets told it can send a file where this
				// branch actually built the hop that sends it. The two lines
				// sit together on purpose — a deployment that has the storage
				// and a deployment whose agents are promised delivery must be
				// the same deployment, and the only way to keep that true is
				// for one `if` to decide both.
				wecomOutboundOpts := []wecom.OutboundOption{}
				if store != nil {
					wecomOutboundOpts = append(wecomOutboundOpts, wecom.WithAttachments(store))
					h.DeclareChannelFileDelivery(string(wecom.TypeWecom))
				}
				wecom.NewOutbound(queries, wecomSenders, slog.Default(), wecomOutboundOpts...).Register(bus)

				// Ranges the media fetcher may dial despite looking reserved.
				// Empty by default, which leaves the SSRF guard exactly as
				// strict as it ships. A deployment behind a fake-IP proxy
				// needs it: there, every public hostname resolves into the
				// proxy's pool (198.18.0.0/15 is the common one), so WeCom's
				// own COS host is indistinguishable from a metadata endpoint
				// by address alone and every attachment is refused.
				if raw := strings.TrimSpace(os.Getenv("MULTICA_WECOM_MEDIA_ALLOW_CIDRS")); raw != "" {
					for _, err := range wecom.SetMediaAllowedPrefixes(strings.Split(raw, ",")) {
						slog.Error("wecom: ignoring malformed media allow cidr", "error", err)
					}
					slog.Warn("wecom: media guard has an operator allow-list; those ranges are reachable by a URL WeCom supplies",
						"cidrs", raw)
				}

				// Frame tracing: off unless an operator asks for it. It
				// records a bounded prefix of message text, so the fact that
				// it is on has to be visible in the log it is writing into —
				// otherwise a session gets left switched on and nobody
				// notices message content accumulating.
				if wecom.SetTrace(os.Getenv("MULTICA_WECOM_TRACE") == "1") {
					slog.Warn("wecom: frame tracing ON — records message text; unset MULTICA_WECOM_TRACE when done")
				}

				slog.Info("wecom integration enabled (smart bot, long connection)")
				// SINGLE-REPLICA CONSTRAINT: WeCom outbound (agent replies +
				// inbox pushes) is delivered only by the replica holding each
				// bot's in-process WebSocket lease. On a multi-replica
				// deployment, an EventChatDone/EventInboxNew published on another
				// replica cannot reach the lease holder, so those replies are
				// dropped. This is stated conditionally rather than gated on a
				// replica-count signal: the server has no reliable count here,
				// and REDIS_URL means "Redis configured" (it also gates rate
				// limiting), not "more than one replica". See wecom/outbound.go
				// and SELF_HOSTING.md. Remove once outbound routes to the lease
				// holder.
				slog.Warn("wecom integration: WeCom agent replies and inbox pushes are delivered only by the replica holding each bot's WebSocket lease. If you run more than one backend replica, responses produced on a replica that does not hold the lease will be dropped — run the WeCom-enabled backend as a single replica until cross-replica outbound routing is implemented.")
			}
		}
	} else {
		slog.Info("wecom integration disabled (MULTICA_WECOM_SECRET_KEY not set)")
	}

	// Composio integration (MUL-3720). Gated by COMPOSIO_API_KEY plus the
	// composio_mcp_apps feature flag. The env var is the project-scoped key the
	// standalone SDK authenticates Composio with (sent as x-api-key; the project
	// is resolved from the key, so NO project id is configured). When unset or
	// flag-disabled the whole block is skipped and the composio HTTP handlers
	// return 503; existing deployments are unaffected. An operator opts in by
	// setting COMPOSIO_API_KEY plus a callback base
	// (COMPOSIO_CALLBACK_BASE_URL, falling back to MULTICA_PUBLIC_URL). The
	// toolkit→auth-config mapping is NOT configured here — it is resolved
	// dynamically from the project's /auth_configs at request time, so enabling
	// a toolkit is a dashboard action, not a redeploy. State signing uses
	// COMPOSIO_STATE_SECRET, or a key derived from JWT_SECRET when that is unset.
	if composioAPIKey := strings.TrimSpace(os.Getenv("COMPOSIO_API_KEY")); composioAPIKey != "" {
		if !featureflags.ComposioMCPAppsEnabled(context.Background(), opts.FeatureFlags) {
			slog.Info("composio integration disabled (feature flag off)")
		} else {
			sdkClient, err := composiosdk.NewClient(composiosdk.Options{APIKey: composioAPIKey})
			if err != nil {
				slog.Error("composio: SDK client init failed; composio integration disabled", "error", err)
			} else {
				stateSecret := composioStateSecret()
				callbackBase := composioCallbackBaseURL(signupConfig.PublicURL)
				if publicURLProvider != nil {
					callbackBase = publicURLProvider()
				}
				switch {
				case len(stateSecret) == 0:
					slog.Error("composio: no state secret (set COMPOSIO_STATE_SECRET or JWT_SECRET); composio integration disabled")
				case callbackBase == "":
					slog.Error("composio: no callback base url (set COMPOSIO_CALLBACK_BASE_URL or MULTICA_PUBLIC_URL); composio integration disabled")
				default:
					svc, serr := composiointeg.NewService(sdkClient, queries, composiointeg.Config{
						StateSecret:             stateSecret,
						CallbackBaseURL:         callbackBase,
						CallbackBaseURLProvider: publicURLProvider,
						FrontendBaseURL:         signupConfig.AppURL,
						FrontendBaseURLProvider: appURLProvider,
					})
					if serr != nil {
						slog.Error("composio: service init failed; composio integration disabled", "error", serr)
					} else {
						h.Composio = svc
						// Stage 3 (MUL-3721) hook: feed the per-task MCP
						// overlay builder into TaskService so every Enqueue*
						// path attaches the initiator user's Composio session
						// URL to the task row before the daemon claims it.
						// taskSvc already exists by this point — it was
						// constructed inside NewHandler — and exposes its
						// Composio field for exactly this kind of late wiring,
						// so no Handler-level mutation is needed.
						if h.TaskService != nil {
							h.TaskService.Composio = svc
						}
						slog.Info("composio integration enabled")
					}
				}
			}
		}
	} else {
		slog.Info("composio integration disabled (COMPOSIO_API_KEY not set)")
	}

	// VCS at-rest encryption: the box encrypts per-workspace access tokens and
	// webhook secrets for token-based providers (Forgejo / Gitea / GitLab).
	// Without it, connect/webhook handlers return 503 (so a misconfigured
	// self-host never stores plaintext secrets).
	if vcsKey, err := secretbox.LoadKey("MULTICA_VCS_SECRET_KEY"); err == nil {
		box, err := secretbox.New(vcsKey)
		if err != nil {
			slog.Error("vcs: secretbox.New failed; vcs integration disabled", "error", err)
		} else {
			h.VCSSecretBox = box
			slog.Info("vcs integration enabled")
		}
	} else {
		slog.Info("vcs integration disabled (MULTICA_VCS_SECRET_KEY not set)")
	}

	if opts.HeartbeatScheduler != nil {
		h.HeartbeatScheduler = opts.HeartbeatScheduler
	}
	// Auth caches: PAT cache is shared between the regular Auth middleware,
	// the DaemonAuth fallback (mul_) path, and the revoke handler
	// (invalidate). DaemonTokenCache backs the DaemonAuth mdt_ path. Both
	// constructors return nil when rdb is nil — every consumer handles that
	// as "no cache, always hit DB".
	patCache := auth.NewPATCache(rdb)
	daemonTokenCache := auth.NewDaemonTokenCache(rdb)
	h.PATCache = patCache
	h.DaemonTokenCache = daemonTokenCache
	h.MembershipCache = auth.NewMembershipCache(rdb)

	// Cloud PAT verifier: validates mcn_ tokens against Multica Cloud
	// Fleet. Returns nil when no Fleet URL is configured — the Auth /
	// DaemonAuth middlewares treat nil as "mcn_ not supported" and
	// reject with 401, instead of falling through to mul_/JWT paths.
	// Reuses MULTICA_CLOUD_FLEET_URL (the same URL the cloud-runtime
	// proxy uses) so a deployment doesn't need a second config knob.
	cloudPATVerifier := auth.NewCloudPATVerifier(auth.CloudPATVerifierConfig{
		FleetBaseURL: signupConfig.CloudRuntimeFleetURL,
		Redis:        rdb,
	})

	// Empty-claim cache: lets the daemon poll path skip a Postgres
	// scan when a recent check confirmed the runtime had no queued
	// task. Returns nil when rdb is nil — TaskService treats that
	// as "no cache, always hit DB" (existing behavior).
	h.TaskService.EmptyClaim = service.NewEmptyClaimCache(rdb)

	// Wire WS heartbeat after stores are finalized so the WS path uses the
	// same (possibly Redis-backed) stores as the HTTP path.
	daemonHub.SetHeartbeatHandler(h.HandleDaemonWSHeartbeat)
	// WS-first claim (MUL-4257): route daemon:rpc_request frames (e.g.
	// tasks.claim) through the same handlers as the HTTP endpoints.
	daemonHub.SetRPCHandler(h.DaemonRPCHandler)
	health := newServerHealth(pool)

	r := chi.NewRouter()

	// Global middleware
	r.Use(chimw.RequestID)
	r.Use(middleware.ClientMetadata)
	r.Use(middleware.RequestLogger)
	if opts.HTTPMetrics != nil {
		r.Use(opts.HTTPMetrics.Middleware)
	}
	r.Use(chimw.Recoverer)
	if opts.SandboxRelay != nil {
		r.Use(opts.SandboxRelay)
	}
	r.Use(middleware.ContentSecurityPolicy)

	// Share allowed origins with WebSocket origin checker.
	realtime.SetAllowedOrigins(origins)
	if opts.RuntimeConfig != nil {
		opts.RuntimeConfig.subscribe(func(snapshot runtimeconfig.Snapshot) {
			realtime.SetAllowedOrigins(append([]string(nil), snapshot.Config.Web.CORSAllowedOrigins...))
		})
	}

	// Share the same trusted-proxy CIDRs (MULTICA_TRUSTED_PROXIES) so the
	// WebSocket origin check honors X-Forwarded-Host only from trusted proxies,
	// using one config source instead of a parallel one.
	realtime.SetTrustedProxies(signupConfig.TrustedProxies)

	if opts.RuntimeConfig != nil {
		r.Use(dynamicDingTalkAccountCallbackCORSMiddleware(opts.RuntimeConfig))
	} else {
		r.Use(dingTalkAccountCallbackCORSMiddleware(
			origins,
			h.DingTalkAccountBindingOrigin,
		))
	}
	if opts.DeploymentFence != nil {
		r.Use(opts.DeploymentFence.Middleware)
	}

	// Health / readiness checks
	r.Get("/health", health.liveHandler)
	r.Get("/readyz", health.readyHandler)
	r.Get("/healthz", health.readyHandler)

	// Realtime subsystem metrics — connection counts, slow-client evictions,
	// and per-event-type send QPS counters. Exposed as JSON so it can be
	// scraped by ops or surfaced in the admin UI without adding a Prometheus
	// dependency. See MUL-1138 (Phase 0).
	//
	// Access is restricted (MUL-1342): when REALTIME_METRICS_TOKEN is set,
	// callers must present it via Authorization: Bearer <token>. When the
	// env var is unset the handler only serves loopback callers so local
	// dev keeps working without exposing the metrics on a public listener.
	r.Get("/health/realtime", realtimeMetricsHandler(os.Getenv("REALTIME_METRICS_TOKEN")))

	// Runtime log tail for operators — lets deployments without pod shell
	// access (Aone) inspect backend/frontend logs remotely. Reachable through
	// the public /api/ proxy route; requires MULTICA_LOG_TAIL_TOKEN via
	// Authorization: Bearer, and is disabled unless MULTICA_LOG_DIR is set
	// (main.sh exports it in containerized deployments).
	r.Get("/api/internal/logs/tail", logTailHandler(os.Getenv("MULTICA_LOG_TAIL_TOKEN"), os.Getenv("MULTICA_LOG_DIR")))
	r.Post("/api/internal/coordinator/card-events", func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimSpace(os.Getenv("MULTICA_LOG_TAIL_TOKEN"))
		if token == "" || !hasBearerToken(r, token) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		h.ReceiveUserDecisionEvent(w, r)
	})

	if opts.DeploymentFence != nil {
		fenceToken := os.Getenv("MULTICA_LOG_TAIL_TOKEN")
		r.Get("/api/internal/deployment-fence", deploymentFenceStatusHandler(fenceToken, opts.DeploymentFence))
		r.Put("/api/internal/deployment-fence", deploymentFenceTransitionHandler(fenceToken, opts.DeploymentFence))
	}

	// Deployment-only HSF diagnostic. This proves that the application can
	// reach Agent Identity through its local Dapr sidecar without ever exposing
	// the short-lived ContextToken returned by the provider.
	r.Post("/api/internal/agent-identity/hsf-check", agentIdentityHSFCheckHandler(
		os.Getenv("MULTICA_LOG_TAIL_TOKEN"),
		agentidentityhsf.NewClient(),
	))
	r.Post("/api/internal/features/releases", internalProductFeaturePublishHandler(
		os.Getenv("MULTICA_LOG_TAIL_TOKEN"),
		h.PublishProductFeatureRelease,
	))

	// WebSocket
	mc := &membershipChecker{queries: queries}
	pr := &patResolver{queries: queries, cache: patCache}
	slugResolver := realtime.SlugResolver(func(ctx context.Context, slug string) (string, error) {
		ws, err := queries.GetWorkspaceBySlug(ctx, slug)
		if err != nil {
			return "", err
		}
		return util.UUIDToString(ws.ID), nil
	})
	r.Get("/ws", func(w http.ResponseWriter, r *http.Request) {
		realtime.HandleWebSocket(hub, mc, pr, slugResolver, w, r)
	})

	// Local file serving (when using local storage). Served through the
	// handler so /uploads/* carries the same preview security headers as the
	// /api/attachments download endpoint; self-hosted split-origin/same-origin
	// clients can then iframe-preview PDFs/HTML fetched straight from the
	// static route instead of hitting the global frame-ancestors 'none' CSP.
	// See MUL-3821 / #4477.
	if _, ok := store.(*storage.LocalStorage); ok {
		r.Get("/uploads/*", h.ServeLocalUpload)
	}

	// Capability-authenticated attachment download (MUL-5292). Public by
	// necessity: a native download (Electron's webContents.downloadURL, a
	// cross-site webview <img>) carries neither Authorization nor a session
	// cookie, so there is nothing here for middleware.Auth to read. The
	// short-lived, single-attachment signature in the query is the credential,
	// and it is only ever minted by the AUTHENTICATED GET
	// /api/attachments/{id} after that request's membership check passed.
	// The authenticated /api/attachments/{id}/download route below is
	// unchanged — this one is purely additive.
	r.Get("/api/attachments/{id}/signed-download", h.DownloadAttachmentWithCapability)

	// A stored DSH plugin package, fetched by a sandbox process that sends no
	// Authorization header. Same reasoning as the capability download above:
	// the short-lived, single-plugin signature in the query is the credential,
	// and it is only minted while composing a task for an agent already bound
	// to that plugin.
	r.Get("/api/dsh-plugins/{id}/artifact", h.DownloadDshPluginArtifact)

	// Avatar serving. Public for the same reason as the capability download
	// above: the auth cookie is SameSite=Strict, so an auth-gated URL cannot
	// be a native <img src> from Desktop / mobile webview or a split-origin
	// self-hosted web app. The HMAC signature in the path is the credential.
	// It covers the storage key, only image keys resolve, and the object must
	// be avatar-class — see server/internal/handler/avatar.go (MUL-5393 /
	// #6024).
	r.Get("/api/avatars/{sig}/*", h.ServeAvatar)

	// Auth (public) — per-IP rate limiting.
	if rdb == nil {
		slog.Warn("rate limiting disabled: REDIS_URL not configured")
	}
	trustedProxies := middleware.ParseTrustedProxies(os.Getenv("RATE_LIMIT_TRUSTED_PROXIES"))
	authRL := middleware.RateLimit(rdb, envPositiveInt("RATE_LIMIT_AUTH", 5), time.Minute, trustedProxies)
	authVerifyRL := middleware.RateLimit(rdb, envPositiveInt("RATE_LIMIT_AUTH_VERIFY", 20), time.Minute, trustedProxies)
	runnerDeviceBeginRL := middleware.RateLimit(rdb, envPositiveInt("RATE_LIMIT_RUNNER_DEVICE_BEGIN", 30), time.Minute, trustedProxies)
	runnerDevicePollRL := middleware.RateLimit(rdb, envPositiveInt("RATE_LIMIT_RUNNER_DEVICE_POLL", 600), time.Minute, trustedProxies)
	runnerChallengeRL := middleware.RateLimit(rdb, envPositiveInt("RATE_LIMIT_RUNNER_CHALLENGE", 120), time.Minute, trustedProxies)
	contactSalesRL := middleware.RateLimit(rdb, envPositiveInt("RATE_LIMIT_CONTACT_SALES", 5), time.Hour, trustedProxies)
	// Official app (connector) OAuth callback: public, hit by the provider's
	// browser redirect; the hashed single-use state is the credential.
	connectorOAuthCallbackRL := middleware.RateLimit(rdb, envPositiveInt("RATE_LIMIT_CONNECTOR_OAUTH_CALLBACK", 60), time.Minute, trustedProxies)
	// LOGIN_PROVIDERS (with LOGIN_DINGTALK_ONLY as its legacy alias) closes
	// the login paths of unlisted providers entirely — not merely hidden in
	// the UI: the routes simply aren't registered, so a direct POST 404s.
	// Because the DingTalk/Feishu apps are 企业内部应用, their OAuth logins only
	// admit members of that organization, which is the access restriction we
	// want from an OAuth-only allowlist like "dingtalk,lark".
	if opts.RuntimeConfig != nil {
		r.With(dynamicLoginProviderMiddleware(opts.RuntimeConfig, "email"), authRL).Post("/auth/send-code", h.SendCode)
		r.With(dynamicLoginProviderMiddleware(opts.RuntimeConfig, "email"), authVerifyRL).Post("/auth/verify-code", h.VerifyCode)
		r.With(dynamicLoginProviderMiddleware(opts.RuntimeConfig, "google"), authRL).Post("/auth/google", h.GoogleLogin)
		r.With(dynamicLoginProviderMiddleware(opts.RuntimeConfig, "lark"), authRL).Post("/auth/lark", h.LarkLogin)
		r.With(dynamicLoginProviderMiddleware(opts.RuntimeConfig, "dingtalk"), authRL).Post("/auth/dingtalk", h.DingTalkLogin)
	} else {
		if handler.LoginProviderAllowed("email") {
			r.With(authRL).Post("/auth/send-code", h.SendCode)
			r.With(authVerifyRL).Post("/auth/verify-code", h.VerifyCode)
		}
		if handler.LoginProviderAllowed("google") {
			r.With(authRL).Post("/auth/google", h.GoogleLogin)
		}
		if handler.LoginProviderAllowed("lark") {
			r.With(authRL).Post("/auth/lark", h.LarkLogin)
		}
		if handler.LoginProviderAllowed("dingtalk") {
			r.With(authRL).Post("/auth/dingtalk", h.DingTalkLogin)
		}
	}
	// The FDE mobile entry is intentionally separate from the ordinary login
	// provider allowlist. It always authenticates the current DingTalk user;
	// the handler still returns 503 unless the internal DingTalk app is wired.
	r.With(authRL).Post("/auth/fde/dingtalk", h.DingTalkLogin)
	r.Post("/auth/logout", h.Logout)

	// Public API
	r.Get("/api/config", h.GetConfig)
	r.With(contactSalesRL).Post("/api/contact-sales", h.CreateContactSales)
	// Static Site uploads authenticate with their own short-lived, single-use
	// capability. Public reads are intentionally unlisted and do not use a
	// Multica session. Neither route derives tenant context from the URL.
	r.Put("/api/sitehosting/uploads/{uploadId}", h.UploadStaticSite)
	r.Get("/api/sitehosting/runtime/fetch-proxy.js", h.ServeStaticSiteFetchProxyRuntime)
	r.Head("/api/sitehosting/runtime/fetch-proxy.js", h.ServeStaticSiteFetchProxyRuntime)
	r.Post("/api/sitehosting/sites/{publicSiteId}/fetch-proxy", h.ProxyStaticSiteFetch)
	r.Get("/sites/{publicSiteId}", h.ServeStaticSite)
	r.Get("/sites/{publicSiteId}/", h.ServeStaticSite)
	r.Get("/sites/{publicSiteId}/*", h.ServeStaticSite)
	r.Head("/sites/{publicSiteId}", h.ServeStaticSite)
	r.Head("/sites/{publicSiteId}/", h.ServeStaticSite)
	r.Head("/sites/{publicSiteId}/*", h.ServeStaticSite)
	// DingTalk interactive-card HTTP callback. The flow id selects the fixed
	// DingTalk connector path while the original body bytes remain unchanged.
	r.Post(dingTalkCardCallbackPath, dingTalkCardCallbackHandler(
		&http.Client{Timeout: dingTalkCardAITableTimeout},
	))
	// Per-Agent A2A discovery is public metadata; JSON-RPC uses an endpoint-
	// specific Bearer credential and derives all tenant context server-side.
	r.Get("/api/a2a/agents/{publicAgentId}/.well-known/agent-card.json", h.GetAgentA2ACard)
	r.Post("/api/a2a/agents/{publicAgentId}/v1", h.HandleAgentA2ARPC)
	// Pre-release -> production forward registrations; authenticated only by
	// the shared-secret signature.
	r.Post("/api/internal/a2a/forward-registrations", h.HandleA2AForwardRegistration)
	// The header-authenticated URL is canonical. The secret-bearing connect URL
	// exists so a local Coding Agent can be configured with one copied command.
	r.Post("/api/mcp/agents/{publicAgentId}", h.HandleAgentMCP)
	r.Post("/api/mcp/connect/{accessToken}", h.HandleAgentMCP)
	r.With(middleware.WorkspaceMCPLinkCredential, middleware.Auth(queries, patCache, cloudPATVerifier, opts.FeatureFlags)).
		Post("/api/mcp/workspaces/{workspaceId}/connect/{accessToken}", h.WorkspaceMCP)

	// Webhook ingress for autopilots. Outside the authenticated group on
	// purpose: the bearer token in the URL path IS the credential. Workspace
	// context is derived from the trigger row, never from request headers.
	r.Post("/api/webhooks/autopilots/{token}", h.HandleAutopilotWebhook)
	// External message-router dispatch ingress. The non-secret endpoint id
	// resolves binding context server-side; a separate Bearer delivery secret
	// authenticates the caller and is never embedded in the callback URL.
	r.Post("/api/webhooks/agent-dispatch/{endpointId}", h.HandleAgentDispatch)
	r.Get("/api/webhooks/agent-dispatch/{endpointId}/tasks/{taskId}/summary", h.GetAgentDispatchTaskSummary)
	r.Get("/api/webhooks/agent-dispatch/{endpointId}/tasks/{taskId}/messages", h.ListAgentDispatchTaskMessages)
	// DBase completes a DingTalk account binding without a Multica session.
	// The path-aware CORS policy above admits only the configured DBase origin;
	// this handler also requires that exact Origin and the per-attempt callback
	// Bearer token before it verifies the Router subscription.
	r.Post("/api/integrations/dingtalk/account-bindings/{bindingId}/callback", h.CompleteDingTalkAccountBindingCallback)
	// GitHub App webhook (no Multica auth — requests are authenticated via
	// HMAC-SHA256 signature in the handler) and post-install setup callback.
	r.Post("/api/webhooks/github", h.HandleGitHubWebhook)
	r.Post("/api/webhooks/github/pre", h.ForwardGitHubPreWebhook)
	r.Get("/api/github/setup", h.GitHubSetupCallback)
	r.Get("/api/github/install", h.GitHubInstallStart)
	// Also completes GitHub official app (MCP connector) connects: states with
	// the "mcpc." prefix are delegated to the connector OAuth callback and
	// rate-limited like it; other states keep the install flow unchanged.
	githubAuthorize := http.HandlerFunc(h.GitHubAuthorizeCallback)
	githubConnectorAuthorize := connectorOAuthCallbackRL(githubAuthorize)
	r.Get("/api/github/authorize", func(w http.ResponseWriter, req *http.Request) {
		if handler.IsConnectorOAuthCallback(req) {
			githubConnectorAuthorize.ServeHTTP(w, req)
			return
		}
		githubAuthorize(w, req)
	})
	// Official app OAuth callback for dynamically registered clients (Notion,
	// Linear, ...). No Multica session: the single-use state and the browser
	// binding cookie set by the start response are the proof.
	r.With(connectorOAuthCallbackRL).Get(handler.ConnectorOAuthCallbackPath, h.ConnectorOAuthCallback)
	// Slack OAuth callback (no Multica auth in the path — it is hit by Slack's
	// browser redirect; the workspace/agent/initiator are recovered from the
	// sealed state). It exchanges the code, upserts the install, then bounces
	// the browser back to Settings → Integrations.
	// VCS webhook for token-based providers (Forgejo / Gitea / GitLab). No Multica
	// auth — authenticated per-connection by the provider's signature scheme;
	// the connection id in the path selects the workspace, provider, and
	// decryption secret.
	r.Post("/api/webhooks/vcs/{connectionId}", h.HandleVCSWebhook)
	// Stripe webhook (no Multica auth — Stripe signs the raw body
	// with a shared secret, the multica-cloud upstream verifies. We
	// only forward the bytes + the Stripe-Signature header; see
	// HandleCloudBillingStripeWebhook for the rationale).
	r.Post("/api/webhooks/stripe", h.HandleCloudBillingStripeWebhook)

	// Composio OAuth callback (MUL-3843). NOT under the Auth group on purpose:
	// Composio 302-redirects the user's browser here at the end of the OAuth
	// flow, and the cookie session is frequently absent (expired session,
	// SameSite=Strict / Safari ITP stripping cross-site cookies, private
	// windows, self-hosted callbacks on a different subdomain). Identity is NOT
	// taken from the session — it comes from the HMAC-signed `state` query
	// param, which CompleteCallback verifies (signature, expiry, replay) before
	// doing anything. h.Composio == nil still returns 503. Keeping it inside the
	// Auth group made a missing cookie a hard 401, breaking the flow for exactly
	// the browsers above; the other four composio endpoints stay session-gated.
	r.Get("/api/integrations/composio/callback", h.ComposioCallback)
	// BUC redirects here after employee consent. The one-time, hashed OAuth
	// state resolves workspace, agent, actor, and the validated relative return
	// path; the callback never trusts identity coordinates from query params.
	r.Get("/api/agent-enterprise-identity/buc/callback", h.CompleteAgentEnterpriseIdentityBinding)

	// Runner installation and OAuth device authorization are public by design.
	// The short-lived pairing token scopes device registration to an Agent; a
	// logged-in owner must still approve it through the protected routes below.
	r.Get("/api/runner/install", h.ServeRunnerInstall)
	r.Get("/api/runner/binaries/{os}/{arch}", h.ServeRunnerBinary)
	r.Get("/api/runner/binaries/{os}/{arch}/checksum", h.ServeRunnerBinaryChecksum)
	r.With(runnerDeviceBeginRL).Post("/api/runner/device-authorizations", h.BeginRunnerDeviceAuthorization)
	r.With(runnerDevicePollRL).Post("/api/runner/device-authorizations/token", h.PollRunnerDeviceAuthorization)
	r.With(runnerChallengeRL).Post("/api/runner/machines/{machineId}/challenges", h.CreateRunnerChallenge)
	r.With(runnerChallengeRL).Post("/api/runner/machines/{machineId}/reconnect", h.ReconnectRunnerBinding)
	r.Get("/api/runner/ws", h.RunnerWebSocket)

	// Daemon API routes (require daemon token or valid user token)
	r.Route("/api/daemon", func(r chi.Router) {
		r.Use(middleware.DaemonAuth(queries, patCache, daemonTokenCache, cloudPATVerifier))

		r.Get("/runtimes/{runtimeId}/model-tasks/{taskId}/v1/{modelPath:models}", h.ProxyRuntimeModel)
		r.Post("/runtimes/{runtimeId}/model-tasks/{taskId}/v1/*", h.ProxyRuntimeModel)
		r.Post("/register", h.DaemonRegister)
		r.Post("/deregister", h.DaemonDeregister)
		r.Post("/heartbeat", h.DaemonHeartbeat)
		r.Get("/ws", h.DaemonWebSocket)
		r.Get("/workspaces", h.ListDaemonWorkspaces)
		r.Get("/workspaces/{workspaceId}/repos", h.GetDaemonWorkspaceRepos)
		r.Get("/workspaces/{workspaceId}/runtime-profiles", h.DaemonListRuntimeProfiles)

		r.Post("/runtimes/{runtimeId}/tasks/claim", h.ClaimTaskByRuntime)
		r.Post("/runtimes/{runtimeId}/tasks/{taskId}/runtime-start-events", h.RecordRuntimeStartEvent)
		// Canonical machine-level batch claim (MUL-4257). `/claim` is a
		// transitional alias; the daemon coordinator targets the canonical
		// path.
		r.Post("/tasks/claim", h.ClaimTasksByRuntime)
		r.Post("/claim", h.ClaimTasksByRuntime)
		r.Post("/runtimes/{runtimeId}/tasks/{taskId}/prepare-lease", h.ExtendTaskPrepareLease)
		r.Post("/runtimes/{runtimeId}/tasks/{taskId}/skill-bundles/resolve", h.ResolveTaskSkillBundles)
		r.Get("/runtimes/{runtimeId}/tasks/pending", h.ListPendingTasksByRuntime)
		r.Post("/runtimes/{runtimeId}/update/{updateId}/result", h.ReportUpdateResult)
		r.Post("/runtimes/{runtimeId}/models/{requestId}/result", h.ReportModelListResult)
		r.Post("/runtimes/{runtimeId}/local-skills/{requestId}/result", h.ReportLocalSkillListResult)
		r.Post("/runtimes/{runtimeId}/local-skills/import/{requestId}/result", h.ReportLocalSkillImportResult)

		r.Get("/tasks/{taskId}/status", h.GetTaskStatus)
		r.Post("/tasks/{taskId}/start", h.StartTask)
		r.Post("/tasks/{taskId}/wait-local-directory", h.MarkTaskWaitingLocalDirectory)
		r.Post("/tasks/{taskId}/progress", h.ReportTaskProgress)
		r.Post("/tasks/{taskId}/complete", h.CompleteTask)
		r.Post("/tasks/{taskId}/fail", h.FailTask)
		r.Post("/tasks/{taskId}/usage", h.ReportTaskUsage)
		r.Post("/tasks/{taskId}/messages", h.ReportTaskMessages)
		r.Post("/tasks/{taskId}/a2a-control", h.ControlA2ATask)
		r.Get("/tasks/{taskId}/a2a-attachments/{attachmentId}", h.DownloadDaemonA2AAttachment)
		r.Get("/tasks/{taskId}/messages", h.ListTaskMessages)
		r.Post("/tasks/{taskId}/llm-traces", h.RelayTaskLLMTrace)
		r.Post("/tasks/{taskId}/cancel-ack", h.AckTaskCancelled)

		r.Post("/workspaces/{workspaceId}/issues/gc-check", h.BatchIssueGCCheck)
		r.Get("/issues/{issueId}/gc-check", h.GetIssueGCCheck)
		r.Get("/chat-sessions/{sessionId}/gc-check", h.GetChatSessionGCCheck)
		r.Get("/autopilot-runs/{runId}/gc-check", h.GetAutopilotRunGCCheck)
		r.Get("/tasks/{taskId}/gc-check", h.GetTaskGCCheck)

		r.Post("/runtimes/{runtimeId}/recover-orphans", h.RecoverOrphanedTasks)
		r.Post("/tasks/{taskId}/session", h.PinTaskSession)
	})

	// Protected API routes
	r.Group(func(r chi.Router) {
		r.Use(middleware.Auth(queries, patCache, cloudPATVerifier, opts.FeatureFlags))
		r.Use(middleware.RefreshCloudFrontCookies(cfSigner))

		// --- User-scoped routes (no workspace context required) ---
		r.With(handler.RequireHumanActor).Get("/api/me", h.GetMe)
		r.Get("/api/workspace-access/self", h.GetWorkspaceAccessSelf)
		r.With(handler.RequireHumanActor).Patch("/api/me", h.UpdateMe)
		r.With(handler.RequireHumanActor).Get("/api/me/runner-bindings", h.ListMyRunnerBindings)
		r.With(handler.RequireHumanActor).Post("/api/me/runner-pairings", h.CreateMyRunnerPairing)
		r.With(handler.RequireHumanActor).Patch("/api/me/runner-machines/{machineId}", h.RenameMyRunnerMachine)
		r.With(handler.RequireHumanActor).Delete("/api/me/runner-machines/{machineId}", h.RevokeMyRunnerMachine)
		r.With(handler.RequireHumanActor).Post("/api/me/runner-bindings/{bindingId}/disconnect", h.DisconnectMyRunnerBinding)
		r.With(handler.RequireHumanActor).Post("/api/me/runner-bindings/{bindingId}/reconnect-command", h.CreateMyRunnerReconnectCommand)
		r.With(handler.RequireHumanActor).Delete("/api/me/runner-bindings/{bindingId}", h.RevokeMyRunnerBinding)
		r.With(handler.RequireHumanActor).Patch("/api/me/onboarding", h.PatchOnboarding)
		r.With(handler.RequireHumanActor).Post("/api/me/onboarding/complete", h.CompleteOnboarding)
		r.With(handler.RequireHumanActor).Post("/api/me/onboarding/cloud-waitlist", h.JoinCloudWaitlist)
		r.With(handler.RequireHumanActor).Get("/api/runner/device-authorizations/{userCode}", h.GetRunnerDeviceAuthorization)
		r.With(handler.RequireHumanActor).Post("/api/runner/device-authorizations/{userCode}/approve", h.ApproveRunnerDeviceAuthorization)
		r.With(handler.RequireHumanActor).Post("/api/runner/device-authorizations/{userCode}/deny", h.DenyRunnerDeviceAuthorization)
		// DEPRECATED — shim routes for desktop < v3 during the rollout
		// window. v3 frontend creates the Helper agent + starter issue
		// via generic CreateAgent / CreateIssue and only calls /complete
		// here. Remove once X-Client-Version telemetry confirms zero
		// pre-v3 desktops are still calling these. Handlers live in
		// server/internal/handler/onboarding_shim.go.
		r.With(handler.RequireHumanActor).Post("/api/me/onboarding/runtime-bootstrap", h.BootstrapOnboardingRuntime)
		r.With(handler.RequireHumanActor).Post("/api/me/onboarding/no-runtime-bootstrap", h.BootstrapOnboardingNoRuntime)
		r.With(handler.RequireHumanActor).Post("/api/cli-token", h.IssueCliToken)
		r.With(handler.RequireHumanActor).Get("/api/developer/capabilities", h.DeveloperCapabilities)
		r.With(handler.RequireHumanActor).Get("/api/developer/models", h.GetGlobalModels)
		r.With(handler.RequireHumanActor).Put("/api/developer/models", h.SaveGlobalModels)
		r.With(handler.RequireHumanActor).Post("/api/developer/models/discover", h.DiscoverProviderModels)
		r.With(handler.RequireHumanActor).Post("/api/developer/models/restore", h.RestoreGlobalModels)
		r.With(handler.RequireHumanActor).Post("/api/developer/models/test", h.TestProviderModel)
		r.With(handler.RequireHumanActor).Get("/api/sitehosting/sites", h.ListStaticSites)
		r.With(handler.RequireHumanActor).Delete("/api/sitehosting/sites/{siteId}", h.DeleteStaticSite)
		r.Post("/api/upload-file", h.UploadFile)
		r.Post("/api/feedback", h.CreateFeedback)
		r.Get("/api/features", h.ListProductFeatureReleases)
		r.Get("/api/features/{id}", h.GetProductFeatureRelease)
		r.Get("/api/runtimes/fc-e2b/stable-channel", h.GetFCE2BStableChannel)
		r.Post("/api/runtimes/fc-e2b/stable-releases", h.CreateFCE2BStableRelease)
		r.Get("/api/runtimes/fc-e2b/stable-releases", h.ListFCE2BStableReleases)
		r.Get("/api/runtimes/fc-e2b/stable-releases/{releaseId}", h.GetFCE2BStableRelease)
		r.Get("/api/runtimes/fc-e2b/stable-runtimes", h.ListFCE2BStableRuntimes)
		r.Post("/api/runtimes/fc-e2b/stable-releases/{releaseId}/pause", h.PauseFCE2BStableRelease)
		r.Post("/api/runtimes/fc-e2b/stable-releases/{releaseId}/resume", h.ResumeFCE2BStableRelease)
		r.Post("/api/runtimes/fc-e2b/stable-releases/{releaseId}/start-rollout", h.StartFCE2BStableRollout)
		r.Post("/api/runtimes/fc-e2b/stable-releases/{releaseId}/advance-rollout", h.AdvanceFCE2BStableRollout)
		r.Post("/api/runtimes/fc-e2b/stable-releases/{releaseId}/complete-observation", h.CompleteFCE2BStableObservation)
		r.Post("/api/runtimes/fc-e2b/stable-releases/{releaseId}/terminate", h.TerminateFCE2BStableRelease)
		r.Post("/api/runtimes/fc-e2b/stable-releases/{releaseId}/rollback", h.RollbackFCE2BStableRelease)
		r.Get("/api/runtimes/cloud-sandbox/stable-channel", h.GetCloudSandboxStableChannel)
		r.Post("/api/runtimes/cloud-sandbox/stable-releases", h.CreateCloudSandboxStableRelease)
		r.Get("/api/runtimes/cloud-sandbox/stable-releases", h.ListCloudSandboxStableReleases)
		r.Get("/api/runtimes/cloud-sandbox/stable-releases/{releaseId}", h.GetFCE2BStableRelease)
		r.Get("/api/runtimes/cloud-sandbox/stable-runtimes", h.ListCloudSandboxStableRuntimes)
		r.Post("/api/runtimes/cloud-sandbox/stable-releases/{releaseId}/pause", h.PauseFCE2BStableRelease)
		r.Post("/api/runtimes/cloud-sandbox/stable-releases/{releaseId}/resume", h.ResumeFCE2BStableRelease)
		r.Post("/api/runtimes/cloud-sandbox/stable-releases/{releaseId}/start-rollout", h.StartFCE2BStableRollout)
		r.Post("/api/runtimes/cloud-sandbox/stable-releases/{releaseId}/advance-rollout", h.AdvanceFCE2BStableRollout)
		r.Post("/api/runtimes/cloud-sandbox/stable-releases/{releaseId}/complete-observation", h.CompleteFCE2BStableObservation)
		r.Post("/api/runtimes/cloud-sandbox/stable-releases/{releaseId}/terminate", h.TerminateFCE2BStableRelease)
		r.Post("/api/runtimes/cloud-sandbox/stable-releases/{releaseId}/rollback", h.RollbackFCE2BStableRelease)
		r.With(handler.RequireDingTalkHumanActor).Get("/api/fde/onboarding", h.GetFDEOnboarding)
		r.With(handler.RequireDingTalkHumanActor).Post("/api/fde/onboarding", h.ProvisionFDEOnboarding)
		// Context capability configuration (mobile H5 /dingtalk/configure).
		// Not workspace-scoped: authority comes from the caller's
		// context_config_grant rows or, for an agent's scenes, from managing
		// the agent (workspace owner/admin or agent owner); plain workspace
		// membership grants nothing.
		r.Route("/api/context-capabilities", func(r chi.Router) {
			r.Use(handler.RequireDingTalkHumanActor)
			r.Post("/links/redeem", h.RedeemContextConfigLink)
			r.Get("/agents", h.ListContextConfigAgents)
			r.Get("/agents/{agentId}", h.GetContextConfigAgent)
			r.Get("/agents/{agentId}/scenes/{sceneKey}", h.GetContextConfigScene)
			r.Post("/agents/{agentId}/scenes/resolve", h.ResolveContextConfigScene)
			r.Put("/agents/{agentId}/bindings", h.PutContextConfigBinding)
			r.Put("/agents/{agentId}/credentials", h.PutContextConfigCredential)
			r.Delete("/agents/{agentId}/credentials", h.DeleteContextConfigCredential)
			r.Post("/agents/{agentId}/connections/start", h.StartContextConfigConnection)
		})
		r.With(handler.RequireHumanActor).Get("/api/dingtalk/jsapi-config", h.GetDingTalkJSAPIConfig)
		r.With(handler.RequireHumanActor).Post("/api/client-usage", h.UpsertClientUsage)

		// Note (MUL-4309): the generic OpenAI-compatible passthrough endpoints
		// (POST /api/llm/v1/chat/completions[/stream]) were intentionally
		// removed. Exposing a general LLM proxy backed by the deployment's own
		// key let any logged-in user run arbitrary completions on our dime.
		// LLM access is now server-internal only (see pkg/llm); anything the
		// web/client needs must go through a purpose-built business endpoint
		// that fixes the prompt/model server-side (e.g. chat title generation).

		// Attachment download — user-scoped (auth-only), NOT
		// workspace-scoped. The handler self-resolves the workspace
		// from the attachment row and enforces membership inside, so
		// this route is callable as a native browser <img>/<video>
		// src that cannot attach X-Workspace-Slug / X-Workspace-ID
		// headers. Persisting `/api/attachments/<id>/download` into
		// comment markdown depends on this — see MUL-3130. The
		// metadata / delete endpoints below stay workspace-scoped
		// because they are JSON-API consumers that always have
		// workspace context.
		r.Get("/api/attachments/{id}/download", h.DownloadAttachment)

		// Server-hosted MCP is auth-scoped rather than request-workspace-scoped.
		// Task Tokens carry an authoritative workspace. PAT calls derive it from
		// session_id or agent_id, then enforce membership and Agent permissions in
		// the handler, so generic MCP clients need no custom workspace header.
		r.Handle("/api/mcp", http.HandlerFunc(h.MulticaMCP))
		r.Post("/api/internal-connectors/{connectorId}/mcp", h.CallInternalConnector)
		r.Post("/api/mcp/workspaces/{workspaceId}", h.WorkspaceMCP)
		r.Handle("/api/runner-mcp", http.HandlerFunc(h.RunnerMCP))
		r.Post("/api/runner-mcp/mounts/{mountId}/servers/{serverName}", h.RunnerMountedMCP)

		r.Route("/api/workspaces", func(r chi.Router) {
			r.Get("/", h.ListWorkspaces)
			r.With(handler.RequireHumanActor).Post("/", h.CreateWorkspace)
			r.Route("/{id}", func(r chi.Router) {
				// DTA Tokens are workspace-bound service-member identities. Only a
				// workspace owner (including an all-permission service) may manage
				// credentials. Restricted DSH tokens cannot reach this group.
				r.Group(func(r chi.Router) {
					r.Use(func(next http.Handler) http.Handler {
						return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
							if !featureflags.WorkspaceAccessTokensEnabled(req.Context(), opts.FeatureFlags) {
								http.NotFound(w, req)
								return
							}
							next.ServeHTTP(w, req)
						})
					})
					r.Use(middleware.RequireWorkspaceRoleFromURL(queries, "id", "owner"))
					r.Route("/access-tokens", func(r chi.Router) {
						r.Get("/", h.ListWorkspaceAccessTokens)
						r.Post("/", h.CreateWorkspaceAccessToken)
						r.Route("/{tokenId}", func(r chi.Router) {
							r.Get("/", h.GetWorkspaceAccessToken)
							r.Patch("/", h.UpdateWorkspaceAccessToken)
							r.Post("/regenerate", h.RegenerateWorkspaceAccessToken)
							r.Post("/revoke", h.RevokeWorkspaceAccessToken)
							r.Delete("/", h.DeleteWorkspaceAccessToken)
						})
					})
				})
				r.Group(func(r chi.Router) {
					r.Use(func(next http.Handler) http.Handler {
						return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
							if !featureflags.WorkspaceMCPEndpointEnabled(req.Context(), opts.FeatureFlags) {
								http.NotFound(w, req)
								return
							}
							next.ServeHTTP(w, req)
						})
					})
					r.Use(handler.RequireWorkspaceMCPHumanIssuer)
					r.Use(middleware.RequireWorkspaceRoleFromURL(queries, "id", "owner", "admin"))
					r.Route("/mcp-tokens", func(r chi.Router) {
						r.Get("/", h.ListWorkspaceMCPTokens)
						r.Post("/", h.CreateWorkspaceMCPToken)
						r.Post("/{tokenId}/revoke", h.RevokeWorkspaceMCPToken)
					})
				})
				// Member-level access
				r.Group(func(r chi.Router) {
					r.Use(middleware.RequireWorkspaceMemberFromURL(queries, "id"))
					r.Get("/", h.GetWorkspace)
					r.Get("/semantica-mcp-relay", h.GetSemanticaMCPRelayStatus)
					r.Get("/internal-connectors/available", h.ListAvailableInternalConnectors)
					r.Get("/mcp", h.GetWorkspaceMCPDiscovery)
					r.Get("/members", h.ListMembersWithUser)
					r.With(handler.RequireHumanActor).Post("/leave", h.LeaveWorkspace)
					r.Get("/invitations", h.ListWorkspaceInvitations)
					// Listing GitHub installations is member-visible so the
					// integrations tab no longer renders blank for non-admins;
					// the handler strips the management handle and adds a
					// can_manage hint so the UI can gate connect/disconnect.
					r.Get("/github/installations", h.ListGitHubInstallations)
					r.Get("/git/connections", h.ListGitConnections)
					r.Get("/git/repository", h.ResolveGitRepository)
					// VCS connections (Forgejo / Gitea / GitLab) — member-visible
					// for the same reason as GitHub installations; connect /
					// disconnect are admin-gated in the group below.
					r.Get("/vcs/connections", h.ListVCSConnections)
					// Custom runtime profiles — listing/reading is member-visible
					// (the Runtime page renders for everyone; create/edit/delete
					// are admin-gated below).
					r.Get("/runtime-profiles", h.ListRuntimeProfiles)
					r.Get("/runtime-profiles/{profileId}", h.GetRuntimeProfile)
				})
				// Admin-level access
				r.Group(func(r chi.Router) {
					r.Use(middleware.RequireWorkspaceRoleFromURL(queries, "id", "owner", "admin"))
					r.Put("/", h.UpdateWorkspace)
					r.Patch("/", h.UpdateWorkspace)
					r.Post("/members", h.CreateInvitation)
					r.With(handler.RequireWorkspaceMCPHumanIssuer).Get("/internal-connectors", h.ListInternalConnectors)
					r.With(handler.RequireWorkspaceMCPHumanIssuer).Post("/internal-connectors", h.CreateInternalConnector)
					r.With(handler.RequireWorkspaceMCPHumanIssuer).Patch("/internal-connectors/{connectorId}", h.UpdateInternalConnector)
					r.With(handler.RequireWorkspaceMCPHumanIssuer).Put("/internal-connectors/{connectorId}/credential", h.PutInternalConnectorCredential)
					r.With(handler.RequireWorkspaceMCPHumanIssuer).Delete("/internal-connectors/{connectorId}/credential", h.DeleteInternalConnectorCredential)
					r.With(handler.RequireWorkspaceMCPHumanIssuer).Post("/internal-connectors/{connectorId}/test", h.TestInternalConnector)
					r.With(handler.RequireWorkspaceMCPHumanIssuer).Post("/internal-connectors/{connectorId}/oauth/start", h.StartInternalConnectorOAuth)
					r.With(handler.RequireWorkspaceMCPHumanIssuer).Post("/internal-connectors/{connectorId}/tools/refresh", h.RefreshInternalConnectorTools)
					r.With(handler.RequireWorkspaceMCPHumanIssuer).Get("/connector-catalog", h.ListConnectorCatalog)
					r.With(handler.RequireWorkspaceMCPHumanIssuer).Post("/connector-catalog/{slug}", h.AddCatalogConnector)
					r.Get("/dingtalk/users/search", h.SearchDingTalkUsers)
					r.Post("/dingtalk/members", h.AddDingTalkWorkspaceMembers)
					r.Post("/dingtalk/group-members", h.AddDingTalkGroupMembers)
					r.Route("/members/{memberId}", func(r chi.Router) {
						r.Patch("/", h.UpdateMember)
						r.Delete("/", h.DeleteMember)
					})
					r.Delete("/invitations/{invitationId}", h.RevokeInvitation)
					// Custom runtime profile mutations (admin-only).
					r.Post("/runtime-profiles", h.CreateRuntimeProfile)
					r.Patch("/runtime-profiles/{profileId}", h.UpdateRuntimeProfile)
					r.Put("/runtime-profiles/{profileId}", h.UpdateRuntimeProfile)
					r.Delete("/runtime-profiles/{profileId}", h.DeleteRuntimeProfile)
					r.With(handler.RequireHumanActor).Delete("/git/connections/{connectionId}", h.DeleteGitConnection)
					r.Get("/git/refs", h.ListGitAgentBranches)
					r.Post("/git/agent-preview", h.PreviewGitAgent)
					r.Post("/agent-packages/preview", h.PreviewAgentPackage)
					r.Post("/agent-packages/prepare", h.PrepareAgentPackage)
					r.Get("/agent-packages/{previewId}/download", h.DownloadPreparedAgentPackage)
					r.Post("/agent-packages", h.CreateAgentFromPackage)
				})
				// Owner-only access
				r.With(middleware.RequireWorkspaceRoleFromURL(queries, "id", "owner")).Delete("/", h.DeleteWorkspace)

				// GitHub integration — connect / disconnect remain admin-only;
				// the read-only list endpoint lives in the member-level group
				// above so non-admins can see the workspace's connection state.
				r.Group(func(r chi.Router) {
					r.Use(middleware.RequireWorkspaceRoleFromURL(queries, "id", "owner", "admin"))
					r.With(handler.RequireHumanActor).Get("/github/connect", h.GitHubConnect)
					r.With(handler.RequireHumanActor).Post("/github/installations/reuse", h.ReuseGitHubInstallation)
					r.Get("/github/installations/{installationId}/repositories", h.ListGitHubInstallationRepositories)
					r.Delete("/github/installations/{installationId}", h.DeleteGitHubInstallation)
					// VCS connect / disconnect / webhook regeneration (admin-only).
					r.Post("/vcs/connections", h.ConnectVCS)
					r.Post("/vcs/connections/{connectionId}/rotate-webhook", h.RotateVCSConnectionWebhook)
					r.Delete("/vcs/connections/{connectionId}", h.DeleteVCSConnection)
				})

				// Lark integration. Every endpoint here only requires
				// workspace membership at the router; the real authorization
				// is per-agent and enforced inside each handler via
				// canManageAgent (agent owner OR workspace owner/admin), so an
				// agent's owner can bind/manage their own agent's Bot without
				// being a workspace admin (MUL-4213). The router can't make
				// that call itself: begin identifies the agent by an
				// `agent_id` query param and revoke by an installation id,
				// neither of which is a URL param the role middleware sees.
				//   - Listing stays member-visible (same rationale as GitHub:
				//     the Integrations tab must render for non-admins so they
				//     see "wired up by whom").
				//   - Begin / status / revoke each load the target agent and
				//     run canManageAgent (status gates on the session
				//     initiator or an admin) before doing anything.
				r.Group(func(r chi.Router) {
					r.Use(middleware.RequireWorkspaceMemberFromURL(queries, "id"))
					r.Get("/lark/installations", h.ListLarkInstallations)
					r.Delete("/lark/installations/{installationId}", h.RevokeLarkInstallation)
					// Device-flow scan-to-install. Begin opens a new
					// registration session against Lark and returns
					// the QR-code URL; the frontend dialog then polls
					// /install/{sessionId}/status until success or
					// terminal failure.
					r.Post("/lark/install/begin", h.BeginLarkInstall)
					r.Get("/lark/install/{sessionId}/status", h.GetLarkInstallStatus)
				})

				// DingTalk bot installation and account binding are member-visible.
				// Mutation handlers authorize against the target Agent: its owner or
				// a workspace owner/admin may install, poll, retry, or revoke.
				r.Group(func(r chi.Router) {
					r.Use(middleware.RequireWorkspaceMemberFromURL(queries, "id"))
					r.Get("/dingtalk/installations", h.ListDingTalkInstallations)
					r.Delete("/dingtalk/installations/{installationId}", h.RevokeDingTalkInstallation)
					r.Post("/dingtalk/installations/{installationId}/router/retry", h.RetryDingTalkRouterRegistration)
					r.Post("/dingtalk/install/begin", h.BeginDingTalkInstall)
					r.Get("/dingtalk/install/{sessionId}/status", h.GetDingTalkInstallStatus)
					r.Post("/dingtalk/install/manual", h.ManualInstallDingTalk)
					r.Get("/dingtalk/account-bindings", h.ListDingTalkAccountBindings)
					r.With(handler.RequireHumanActor).Get("/dingtalk/execution-identities", h.ListReusableDingTalkIdentities)
					r.With(handler.RequireHumanActor).Post("/dingtalk/execution-identities/reuse", h.ReuseDingTalkIdentity)
					r.Get("/dingtalk/account-bindings/{agentId}/status", h.GetDingTalkAccountBindingStatus)
					r.Post("/dingtalk/account-bindings/begin", h.BeginDingTalkAccountBinding)
					r.Patch("/dingtalk/account-bindings/{agentId}/surface", h.UpdateDingTalkAccountBindingSurface)
					r.Delete("/dingtalk/account-bindings/{agentId}", h.UnbindDingTalkAccountBinding)
					r.Get("/agent-identity/github/status", h.GetAgentIdentityGitHubStatus)
					r.Post("/agent-identity/github/oauth/start", h.BeginAgentIdentityGitHubOAuth)
					r.Post("/agent-identity/github/{connectionId}/test", h.TestAgentIdentityGitHubConnection)
					r.Delete("/agent-identity/github/{connectionId}", h.DisconnectAgentIdentityGitHubConnection)
					r.Get("/agent-identity/enterprise/status", h.GetAgentEnterpriseIdentityStatus)
					r.Post("/agent-identity/enterprise/oauth/start", h.BeginAgentEnterpriseIdentityBinding)
					r.With(handler.RequireHumanActor).Post("/agent-identity/enterprise/source/rotate", h.RotateAgentEnterpriseIdentitySource)
					r.Delete("/agent-identity/enterprise", h.RevokeAgentEnterpriseIdentity)
				})
				// Slack integration (MUL-3666). Same admin/member split as
				// Lark: listing is member-visible; OAuth begin + revoke are
				// admin-only. The OAuth callback itself is a public route (it is
				// hit by Slack's browser redirect with no workspace in the path)
				// and is registered outside this workspace group.
				r.Group(func(r chi.Router) {
					r.Use(middleware.RequireWorkspaceMemberFromURL(queries, "id"))
					r.Get("/slack/installations", h.ListSlackInstallations)
					r.Get("/wecom/installations", h.ListWecomInstallations)
				})
				r.Group(func(r chi.Router) {
					r.Use(middleware.RequireWorkspaceRoleFromURL(queries, "id", "owner", "admin"))
					r.Delete("/slack/installations/{installationId}", h.RevokeSlackInstallation)
					r.Post("/slack/install/byo", h.RegisterSlackBYO)
					r.Delete("/wecom/installations/{installationId}", h.RevokeWecomInstallation)
					r.Post("/wecom/install/byo", h.RegisterWecomBYO)
				})

			})
		})

		// Lark binding-token redemption. NOT workspace-scoped because
		// the redeemer hits this BEFORE they have any workspace
		// context — the redemption itself is what mints their
		// lark_user_binding row. Identity comes from the session;
		// the token only proves "this open_id requested binding," and
		// is combined with the logged-in user to create the mapping.
		r.Post("/api/lark/binding/redeem", h.RedeemLarkBindingToken)
		// Slack binding-token redemption. Same rationale as Lark: NOT
		// workspace-scoped because the redeemer hits this before they have any
		// workspace context — the redemption itself mints their binding row. The
		// logged-in user (from the session) is bound to the Slack id the token
		// carries.
		r.Post("/api/slack/binding/redeem", h.RedeemSlackBindingToken)
		// DingTalk binding redemption is user-scoped for the same reason as
		// Slack: the token is redeemed before workspace context is selected.
		r.Post("/api/dingtalk/binding/redeem", h.RedeemDingTalkBindingToken)
		// WeCom smart-bot binding-token redemption. Same rationale as
		// Lark/Slack: the session is the source of truth for the redeemer's
		// Multica identity; the token only carries the WeCom userid to bind.
		r.Post("/api/wecom/binding/redeem", h.RedeemWecomBindingToken)

		// Composio integration (MUL-3720). User-scoped (no workspace context):
		// a connection belongs to a user. These four require a logged-in
		// session; the OAuth callback is the outlier and lives outside the Auth
		// group (registered above with the other public OAuth/webhook routes —
		// see MUL-3843). All return 503 when COMPOSIO_API_KEY is unset.
		r.Route("/api/integrations/composio", func(r chi.Router) {
			r.Post("/connect/init", h.ComposioConnectInit)
			r.Get("/toolkits", h.ListComposioToolkits)
			r.Get("/connections", h.ListComposioConnections)
			r.Delete("/connections/{id}", h.DeleteComposioConnection)
		})

		// User-scoped invitation routes (no workspace context required)
		r.Get("/api/invitations", h.ListMyInvitations)
		r.Get("/api/invitations/{id}", h.GetMyInvitation)
		r.Post("/api/invitations/{id}/accept", h.AcceptInvitation)
		r.Post("/api/invitations/{id}/decline", h.DeclineInvitation)

		r.Route("/api/tokens", func(r chi.Router) {
			r.Use(handler.RequireHumanActor)
			r.Get("/", h.ListPersonalAccessTokens)
			r.Post("/", h.CreatePersonalAccessToken)
			r.Post("/current/renew", h.RenewCurrentPersonalAccessToken)
			r.Delete("/{id}", h.RevokePersonalAccessToken)
		})

		// Cloud Billing proxy. Same upstream service / port as
		// cloud-runtime — multica-cloud's Fleet and Billing share
		// :8080 and the same chi router. All routes here forward
		// to /api/v1/billing/* with X-User-ID stamped from the
		// authenticated context.
		//
		// User-scoped (account-level), NOT workspace-scoped — sits
		// outside the RequireWorkspaceMember group so a user can
		// inspect their balance, top up, and open the Billing Portal
		// without an active workspace selected. The upstream owner
		// model is single-user; X-Workspace-ID would be ignored even
		// if we sent it. The Stripe webhook is the public outlier
		// and lives outside the entire Auth group (see above).
		//
		// IMPORTANT — task-token actors are blocked here. The Auth
		// middleware happily turns an mat_ task token into a normal
		// X-User-ID stamp (so agents can comment, claim issues, etc.
		// as their owner), but billing is account-level and a running
		// agent reading its owner's balance / opening a checkout
		// session is the kind of lateral-movement we're explicitly
		// trying to prevent. handler.RequireHumanActor checks the
		// authoritative server-set X-Actor-Source header and 403s
		// any task-token request. See actor_guards.go for the full
		// rationale.
		r.Route("/api/cloud-billing", func(r chi.Router) {
			r.Use(handler.RequireHumanActor)

			r.Get("/balance", h.GetCloudBillingBalance)
			r.Get("/transactions", h.ListCloudBillingTransactions)
			r.Get("/batches", h.ListCloudBillingBatches)
			r.Get("/topups", h.ListCloudBillingTopups)
			r.Get("/price-tiers", h.ListCloudBillingPriceTiers)
			r.Post("/checkout-sessions", h.CreateCloudBillingCheckoutSession)
			r.Get("/checkout-sessions/{sessionId}", h.GetCloudBillingCheckoutSession)
			r.Post("/portal-sessions", h.CreateCloudBillingPortalSession)
		})

		// --- Workspace-scoped routes (all require workspace membership) ---
		r.Group(func(r chi.Router) {
			r.Use(middleware.RequireWorkspaceMember(queries))

			// Assignee frequency
			r.Get("/api/assignee-frequency", h.GetAssigneeFrequency)
			r.Get("/api/assoc/recall", h.RecallAssoc)
			r.Get("/api/assoc/events", h.ListAssocEvents)
			r.Post("/api/assoc/bind-outbound", h.BindAssocOutbound)
			r.Post("/api/tasks/{taskID}/dingtalk-send-receipts", h.RecordDingTalkSendReceipt)

			r.Route("/api/filesystem", func(r chi.Router) {
				r.Use(handler.RequireHumanActor)
				r.Get("/roots", h.GetWorkspaceFilesystemRoots)
				r.Get("/entries", h.GetWorkspaceFilesystemEntries)
				r.Get("/content", h.GetWorkspaceFilesystemContent)
				r.Post("/mkdir", h.PostWorkspaceFilesystemMkdir)
				r.Post("/upload", h.PostWorkspaceFilesystemUpload)
				r.Post("/rename", h.PostWorkspaceFilesystemRename)
				r.Delete("/entries", h.DeleteWorkspaceFilesystemEntry)
				r.Get("/grants", h.GetWorkspaceFilesystemGrants)
				r.Put("/grants", h.PutWorkspaceFilesystemGrant)
			})

			// Issues
			r.Route("/api/issues", func(r chi.Router) {
				r.Post("/table/groups", h.ListIssueTableGroups)
				r.Post("/table/rows", h.ListIssueTableRows)
				r.Post("/table/facets", h.ListIssueTableFacets)
				r.Get("/search", h.SearchIssues)
				r.Get("/child-progress", h.ChildIssueProgress)
				r.Get("/children", h.ListChildrenByParents)
				r.Get("/grouped", h.ListGroupedIssues)
				r.Get("/", h.ListIssues)
				// POST twin of GET /api/issues for oversized filter sets
				// (agents-working ids facet) — see QueryIssues.
				r.Post("/query", h.QueryIssues)
				r.Post("/", h.CreateIssue)
				r.Post("/quick-create", h.QuickCreateIssue)
				r.Post("/preview-trigger", h.PreviewIssueTrigger)
				r.Post("/batch-update", h.BatchUpdateIssues)
				r.Post("/batch-delete", h.BatchDeleteIssues)
				r.Route("/{id}", func(r chi.Router) {
					r.Get("/", h.GetIssue)
					r.Put("/", h.UpdateIssue)
					r.Post("/move", h.MoveIssue)
					r.Delete("/", h.DeleteIssue)
					r.Post("/comments/trigger-preview", h.PreviewCommentTriggers)
					r.Post("/comments", h.CreateComment)
					r.Get("/comments", h.ListComments)
					r.Get("/timeline", h.ListTimeline)
					r.Get("/subscribers", h.ListIssueSubscribers)
					r.Post("/subscribe", h.SubscribeToIssue)
					r.Post("/unsubscribe", h.UnsubscribeFromIssue)
					r.Post("/unsubscribe/subtree", h.UnsubscribeFromIssueSubtree)
					r.Get("/active-task", h.GetActiveTaskForIssue)
					r.Post("/tasks/{taskId}/cancel", h.CancelTask)
					r.Post("/rerun", h.RerunIssue)
					r.Post("/quick-actions/{quickActionId}/run", h.RunQuickAction)
					r.Post("/quick-actions/{quickActionId}/render", h.RenderQuickAction)
					r.Get("/task-runs", h.ListTasksByIssue)
					r.Get("/usage", h.GetIssueUsage)
					r.Post("/reactions", h.AddIssueReaction)
					r.Delete("/reactions", h.RemoveIssueReaction)
					r.Get("/attachments", h.ListAttachments)
					r.Get("/children", h.ListChildIssues)
					r.Get("/labels", h.ListLabelsForIssue)
					r.Post("/labels", h.AttachLabel)
					r.Delete("/labels/{labelId}", h.DetachLabel)
					r.Get("/metadata", h.ListIssueMetadata)
					r.Put("/metadata/{key}", h.SetIssueMetadataKey)
					r.Delete("/metadata/{key}", h.DeleteIssueMetadataKey)
					r.Put("/properties/{propertyId}", h.SetIssueProperty)
					r.Delete("/properties/{propertyId}", h.DeleteIssueProperty)
					r.Get("/pull-requests", h.ListPullRequestsForIssue)
				})
			})

			// User-readable task artifacts plus the task-token-only DSH upload.
			// Each handler re-applies its own transcript/trajectory authorization.
			r.Get("/api/tasks/{taskId}/messages", h.ListTaskMessagesByUser)
			r.Put("/api/tasks/{taskId}/dsh-trajectory", h.UploadDSHTrajectory)
			r.Get("/api/tasks/{taskId}/dsh/schedules", h.DSHSchedules)
			r.Get("/api/tasks/{taskId}/dsh/schedules/{scheduleId}", h.GetDSHSchedule)
			r.Post("/api/tasks/{taskId}/dsh/schedules", h.DSHSchedules)
			r.Delete("/api/tasks/{taskId}/dsh/schedules/{scheduleId}", h.DeleteDSHSchedule)
			r.Get("/api/tasks/{taskId}/dsh-trajectory", h.GetDSHTrajectory)

			// DTA deployment load verification. These endpoints expose only
			// server-stamped smoke Issues, never generic Issue or Chat CRUD.
			r.Route("/api/dta/load-smokes", func(r chi.Router) {
				r.Get("/", h.GetDTALoadSmokeByOperation)
				r.Post("/", h.CreateDTALoadSmoke)
				r.Route("/{issueId}", func(r chi.Router) {
					r.Get("/runs", h.ListDTALoadSmokeRuns)
					r.Get("/runs/{taskId}/messages", h.ListDTALoadSmokeMessages)
					r.Get("/comments", h.ListDTALoadSmokeComments)
					r.Post("/retry", h.RetryDTALoadSmoke)
				})
			})

			// Task-scoped Chat -> Issue background handoff. The handler requires
			// a task token and rejects ordinary member credentials.
			r.Post("/api/issue-delegations", h.DelegateIssue)

			// Issue quick actions (definitions; running one lives under
			// /api/issues/{id}/quick-actions/{quickActionId}/run)
			r.Route("/api/quick-actions", func(r chi.Router) {
				r.Get("/", h.ListQuickActions)
				r.Post("/", h.CreateQuickAction)
				r.Route("/{id}", func(r chi.Router) {
					r.Patch("/", h.UpdateQuickAction)
					r.Delete("/", h.DeleteQuickAction)
				})
			})

			// Custom issue properties (definitions; values live under /api/issues/{id}/properties)
			r.Route("/api/properties", func(r chi.Router) {
				r.Get("/", h.ListProperties)
				r.Post("/", h.CreateProperty)
				r.Route("/{id}", func(r chi.Router) {
					r.Get("/", h.GetProperty)
					r.Patch("/", h.UpdateProperty)
				})
			})

			// Labels
			r.Route("/api/labels", func(r chi.Router) {
				r.Get("/", h.ListLabels)
				r.Post("/", h.CreateLabel)
				r.Route("/{id}", func(r chi.Router) {
					r.Get("/", h.GetLabel)
					r.Get("/usage", h.GetLabelUsage)
					r.Put("/", h.UpdateLabel)
					r.Delete("/", h.DeleteLabel)
				})
			})

			// Projects
			r.Route("/api/projects", func(r chi.Router) {
				r.Get("/search", h.SearchProjects)
				r.Get("/", h.ListProjects)
				r.Post("/", h.CreateProject)
				r.Route("/{id}", func(r chi.Router) {
					r.Get("/", h.GetProject)
					r.Put("/", h.UpdateProject)
					r.Delete("/", h.DeleteProject)
					r.Get("/resources", h.ListProjectResources)
					r.Post("/resources", h.CreateProjectResource)
					r.Put("/resources/{resourceId}", h.UpdateProjectResource)
					r.Delete("/resources/{resourceId}", h.DeleteProjectResource)
				})
			})

			// Squads
			r.Route("/api/squads", func(r chi.Router) {
				r.Get("/", h.ListSquads)
				r.Post("/", h.CreateSquad)
				r.Route("/{id}", func(r chi.Router) {
					r.Get("/", h.GetSquad)
					r.Put("/", h.UpdateSquad)
					r.With(handler.RequireHumanActor).Put("/owner", h.TransferSquadOwner)
					r.Delete("/", h.DeleteSquad)
					r.Get("/members", h.ListSquadMembers)
					r.Get("/members/status", h.ListSquadMemberStatus)
					r.Post("/members", h.AddSquadMember)
					r.Delete("/members", h.RemoveSquadMember)
					r.Patch("/members/role", h.UpdateSquadMemberRole)
				})
			})

			// Squad leader evaluation (writes to activity_log)
			r.Post("/api/issues/{id}/squad-evaluated", h.RecordSquadLeaderEvaluation)

			// Autopilots
			r.Route("/api/autopilots", func(r chi.Router) {
				r.Get("/", h.ListAutopilots)
				r.Post("/", h.CreateAutopilot)
				r.Get("/cron-preview", h.CronPreview)
				r.Route("/{id}", func(r chi.Router) {
					r.Get("/", h.GetAutopilot)
					r.Patch("/", h.UpdateAutopilot)
					r.With(handler.RequireHumanActor).Put("/owner", h.TransferAutopilotOwner)
					r.Delete("/", h.DeleteAutopilot)
					r.Post("/trigger", h.TriggerAutopilot)
					r.Get("/runs", h.ListAutopilotRuns)
					r.Get("/runs/{runId}", h.GetAutopilotRun)
					r.Get("/deliveries", h.ListAutopilotDeliveries)
					r.Get("/deliveries/{deliveryId}", h.GetAutopilotDelivery)
					r.Post("/deliveries/{deliveryId}/replay", h.ReplayAutopilotDelivery)
					r.Post("/triggers", h.CreateAutopilotTrigger)
					r.Route("/triggers/{triggerId}", func(r chi.Router) {
						r.Patch("/", h.UpdateAutopilotTrigger)
						r.Delete("/", h.DeleteAutopilotTrigger)
						r.Post("/rotate-webhook-token", h.RotateAutopilotTriggerWebhookToken)
						r.Put("/signing-secret", h.SetAutopilotTriggerSigningSecret)
					})
					r.Post("/collaborators", h.AddAutopilotCollaborator)
					r.Delete("/collaborators/{userId}", h.RemoveAutopilotCollaborator)
				})
			})

			// Pins
			r.Route("/api/pins", func(r chi.Router) {
				r.Get("/", h.ListPins)
				r.Post("/", h.CreatePin)
				r.Put("/reorder", h.ReorderPins)
				r.Delete("/{itemType}/{itemId}", h.DeletePin)
			})

			// Saved issue views (MUL-4796).
			r.Get("/api/issue-view-preferences", h.GetIssueViewPreference)
			r.Put("/api/issue-view-preferences", h.PutIssueViewPreference)
			r.Route("/api/issue-views", func(r chi.Router) {
				r.Get("/", h.ListIssueViews)
				r.Post("/", h.CreateIssueView)
				r.Route("/{id}", func(r chi.Router) {
					r.Get("/", h.GetIssueViewByID)
					r.Patch("/", h.UpdateIssueView)
					r.Delete("/", h.DeleteIssueView)
				})
			})

			// Attachments
			r.Get("/api/attachments/{id}", h.GetAttachmentByID)
			// /api/attachments/{id}/download is registered in the
			// outer Auth-only group above so it can be loaded as a
			// native <img>/<video> src without workspace headers
			// (MUL-3130). The handler self-resolves the workspace
			// from the attachment row.
			r.Get("/api/attachments/{id}/content", h.GetAttachmentContent)
			r.Delete("/api/attachments/{id}", h.DeleteAttachment)

			// Comments
			r.Route("/api/comments/{commentId}", func(r chi.Router) {
				r.Put("/", h.UpdateComment)
				r.Delete("/", h.DeleteComment)
				r.Post("/resolve", h.ResolveComment)
				r.Delete("/resolve", h.UnresolveComment)
				r.Post("/reactions", h.AddReaction)
				r.Delete("/reactions", h.RemoveReaction)
			})

			// Agents
			r.Get("/api/agent-schema", h.DownloadAgentSchema)
			r.Route("/api/agents", func(r chi.Router) {
				r.Get("/", h.ListAgents)
				r.Post("/", h.CreateAgent)
				// Agent templates: pre-configured instructions + skill refs.
				// Picking a template imports the referenced skills into the
				// workspace (find-or-create by name) and creates the agent
				// with the template's instructions in one transaction.
				r.Post("/from-template", h.CreateAgentFromTemplate)
				// The workspace's built-in Chief of Staff. Server-owned: the
				// caller supplies only a runtime and a language, so a client
				// cannot mint an agent carrying `system_key` and thereby claim
				// the system instruction layer. Idempotent per workspace.
				r.Post("/mika", h.CreateMikaAgent)
				r.Route("/{id}", func(r chi.Router) {
					r.Get("/", h.GetAgent)
					r.Get("/source", h.GetAgentSource)
					r.Get("/decisions/export", h.ExportUserDecisions)
					r.Get("/decisions/health", h.UserDecisionHealth)
					r.Get("/export", h.ExportAgent)
					r.Get("/package-bindings", h.GetAgentPackageBindings)
					r.With(handler.RequireHumanActor).Post("/package-bindings/confirm", h.ConfirmAgentPackageBinding)
					r.Get("/source/branches", h.ListAgentSourceBranches)
					r.Get("/source/publications", h.ListAgentPublications)
					r.Post("/source/preview", h.PreviewAgentSourceSync)
					// The composed inbound prompt structure for this agent.
					// Agent-scoped and manage-gated: the managed policy is
					// deployment configuration, so it does not belong on the
					// public /api/config.
					r.Get("/dispatch-prompt-preview", h.GetAgentDispatchPromptPreview)
					r.Post("/extract-voice", h.ExtractAgentVoice)
					r.Post("/source/sync", h.SyncAgentSource)
					r.Put("/", h.UpdateAgent)
					r.With(handler.RequireHumanActor).Put("/owner", h.TransferAgentOwner)
					r.Post("/archive", h.ArchiveAgent)
					r.Post("/restore", h.RestoreAgent)
					r.Post("/cancel-tasks", h.CancelAgentTasks)
					r.Get("/event-batches", h.ListAgentEventBatches)
					r.Post("/event-batches/{batchId}/retry", h.RetryAgentEventBatch)
					r.Get("/tasks", h.ListAgentTasks)
					r.Get("/coordinator-sessions", h.ListAgentCoordinatorSessions)
					r.Get("/coordinator-conversations", h.ListAgentCoordinatorConversations)
					r.Get("/coordinator-conversations/{sessionId}/messages", h.ListAgentCoordinatorConversationMessages)
					r.Get("/scene-memory", h.ListAgentSceneMemory)
					r.Get("/scene-memory/{memoryId}", h.GetAgentSceneMemory)
					r.Put("/scene-memory/{memoryId}", h.UpdateAgentSceneMemory)
					r.Post("/scene-memory/{memoryId}/reset", h.ResetAgentSceneMemory)
					r.Post("/scene-memory/{memoryId}/relations/clear", h.ClearAgentSceneRelations)
					// Scene and personal capability layers: offer catalog and
					// read-only scope summaries (docs/context-capabilities.md).
					r.With(handler.RequireHumanActor).Get("/context-capabilities", h.GetAgentContextCapabilities)
					r.With(handler.RequireHumanActor).Put("/context-capabilities/offers", h.PutAgentContextCapabilityOffers)
					// Official apps of the agent's 连接器 tab (连接应用): status,
					// usage and tools per app. Read-only; actions use the
					// catalog, connector, offer and credential routes.
					r.With(handler.RequireHumanActor).Get("/connected-apps", h.ListAgentConnectedApps)
					r.With(handler.RequireHumanActor).Get("/connected-apps/{slug}", h.GetAgentConnectedApp)
					// IM scenes (group and 1:1 chats): scene list, scene
					// prompt and scene bindings. Configuration only; the
					// scene key is a percent-encoded openConversationId.
					r.With(handler.RequireHumanActor).Get("/scenes", h.ListAgentScenes)
					r.With(handler.RequireHumanActor).Get("/scenes/{sceneKey}", h.GetAgentScene)
					r.With(handler.RequireHumanActor).Put("/scenes/{sceneKey}/prompt", h.PutAgentScenePrompt)
					r.With(handler.RequireHumanActor).Put("/scenes/{sceneKey}/bindings", h.PutAgentSceneBinding)
					r.Get("/skills", h.ListAgentSkills)
					r.Put("/skills", h.SetAgentSkills)
					r.Post("/skills/add", h.AddAgentSkills)
					// Which DSH plugins this agent boots with. The daemon
					// composes these into the profile the sandbox builds.
					r.Get("/dsh-plugins", h.ListAgentDshPlugins)
					r.With(handler.RequireHumanActor).Get("/dsh-plugins/{pluginId}/config", h.GetAgentDshPluginConfig)
					r.With(handler.RequireHumanActor).Put("/dsh-plugins/{pluginId}/config", h.UpdateAgentDshPluginConfig)
					r.With(handler.RequireHumanActor).Get("/dsh-profile", h.GetDSHProfile)
					r.With(handler.RequireHumanActor).Post("/dsh-profile", h.PrepareDSHProfile)
					r.With(handler.RequireHumanActor).Post("/dsh-profile/retry", h.RetryDSHProfileBuild)
					r.With(handler.RequireHumanActor).Get("/dsh-home", h.GetDSHHome)
					r.With(handler.RequireHumanActor).Post("/dsh-home", h.EnsureDSHHome)
					r.With(handler.RequireHumanActor).Get("/filesystem", h.GetDSHHome)
					r.With(handler.RequireHumanActor).Post("/filesystem", h.EnsureDSHHome)
					r.Put("/dsh-plugins", h.SetAgentDshPlugins)
					r.Delete("/dsh-plugins/{pluginId}", h.RemoveAgentDshPlugin)
					// OKRs materialize as workspace labels the agent tags
					// issues with; the catalog is injected into its prompt.
					r.Get("/okrs", h.ListAgentOKRs)
					r.Put("/okrs", h.SetAgentOKRs)
					r.Get("/labels", h.ListLabelsForAgent)
					r.Post("/labels", h.AttachLabelToAgent)
					r.Delete("/labels/{labelId}", h.DetachLabelFromAgent)
					r.Put("/skills/{skillId}/enabled", h.SetAgentSkillEnabled)
					r.Put("/runtime-skills/enabled", h.SetAgentRuntimeSkillEnabled)
					r.Delete("/skills/{skillId}", h.RemoveAgentSkill)
					r.Route("/a2a", func(r chi.Router) {
						r.Use(handler.RequireHumanActor)
						r.Get("/", h.GetAgentA2AConfig)
						r.Put("/", h.UpdateAgentA2AConfig)
						r.Get("/operator", h.GetAgentA2AOperatorConfig)
						r.Put("/operator/dws-identity", h.UpdateAgentA2AOperatorIdentity)
						r.Delete("/operator/dws-identity", h.DeleteAgentA2AOperatorIdentity)
						r.Put("/operator/prod-forward", h.UpdateAgentA2AProdForward)
						r.Post("/clients", h.CreateAgentA2AClient)
						r.Patch("/clients/{clientId}", h.UpdateAgentA2AClient)
						r.Post("/clients/{clientId}/credentials", h.CreateAgentA2ACredential)
						r.Delete("/clients/{clientId}/credentials/{credentialId}", h.DeleteAgentA2ACredential)
					})
					// Dedicated env-management endpoint. Admits the agent
					// owner or a workspace owner/admin; agent actors are
					// denied. Every reveal / write is audited to
					// activity_log. See MUL-2600, MUL-5438 and
					// internal/handler/agent_env.go.
					r.Get("/env", h.GetAgentEnv)
					r.Put("/env", h.UpdateAgentEnv)
					r.With(handler.RequireHumanActor).Get("/runner-bindings", h.ListAgentRunnerBindings)
					r.With(handler.RequireHumanActor).Put("/runner-mount", h.MountAgentRunnerMachine)
					r.With(handler.RequireHumanActor).Post("/runner-pairings", h.CreateAgentRunnerPairing)
					r.With(handler.RequireHumanActor).Post("/runner-bindings/{bindingId}/disconnect", h.DisconnectAgentRunnerBinding)
					r.With(handler.RequireHumanActor).Post("/runner-bindings/{bindingId}/reconnect-command", h.CreateAgentRunnerReconnectCommand)
					r.With(handler.RequireHumanActor).Delete("/runner-bindings/{bindingId}", h.RevokeAgentRunnerBinding)
				})
			})

			// Agent templates catalog (browse + detail). The Create flow
			// lives under /api/agents/from-template above; this route is for
			// the picker UI to list available templates.
			r.Route("/api/agent-templates", func(r chi.Router) {
				r.Get("/", h.ListAgentTemplates)
				r.Get("/{slug}", h.GetAgentTemplate)
			})
			r.Route("/api/agent-builder/sessions", func(r chi.Router) {
				// The creation studio's unfinished drafts. Builder sessions are
				// invisible to every chat list (their carrier is kind='system'),
				// so this is the only route back to one.
				r.Get("/", h.ListAgentBuilderSessions)
				r.Post("/", h.CreateAgentBuilderSession)
				r.Patch("/{sessionId}/runtime", h.SwitchAgentBuilderRuntime)
				// Autosaved configuration, including edits the user has typed
				// but not sent. Read back through the list above.
				r.Put("/{sessionId}/draft", h.SaveAgentBuilderDraft)
			})

			// DSH plugins. DeepSeek Harness has no registry of its own —
			// `dsh plugin add` forwards to pnpm — so browse reads a cached
			// community index plus the npm registry's own search endpoint,
			// and import records a pinned package reference.
			r.Route("/api/dsh-plugins", func(r chi.Router) {
				r.Get("/", h.ListDshPlugins)
				r.Post("/", h.ImportDshPlugin)
				r.Get("/bindings", h.ListDshPluginBindings)
				r.Get("/catalog", h.BrowseDshPluginCatalog)
				r.Get("/catalog/categories", h.ListDshPluginCatalogCategories)
				r.With(handler.RequireHumanActor).Post("/catalog/refresh", h.RefreshDshPluginCatalog)
				r.Get("/registry-search", h.SearchDshPluginRegistry)
				// A package that arrives as a file rather than a reference.
				r.Post("/upload", h.UploadDshPlugin)
				r.Route("/{id}", func(r chi.Router) {
					r.Get("/", h.GetDshPlugin)
					r.Put("/", h.UpdateDshPlugin)
					r.Delete("/", h.DeleteDshPlugin)
					r.Get("/update", h.CheckDshPluginUpdate)
					// What is actually inside the package. Read from the
					// stored bytes, so a viewer and the sandbox can never
					// disagree about what was imported.
					r.Get("/files", h.ListDshPluginFiles)
					r.Get("/file", h.GetDshPluginFile)
				})
			})

			// Skills
			r.Route("/api/skills", func(r chi.Router) {
				r.Get("/", h.ListSkills)
				r.Post("/", h.CreateSkill)
				r.Get("/search", h.SearchSkills)
				r.Post("/import", h.ImportSkill)
				r.Route("/{id}", func(r chi.Router) {
					r.Get("/", h.GetSkill)
					r.Put("/", h.UpdateSkill)
					r.With(handler.RequireHumanActor).Put("/owner", h.TransferSkillOwner)
					r.Delete("/", h.DeleteSkill)
					r.Get("/labels", h.ListLabelsForSkill)
					r.Post("/labels", h.AttachLabelToSkill)
					r.Delete("/labels/{labelId}", h.DetachLabelFromSkill)
					r.Get("/files", h.ListSkillFiles)
					r.Put("/files", h.UpsertSkillFile)
					r.Delete("/files/{fileId}", h.DeleteSkillFile)
				})
			})

			// Dashboard — workspace-wide token + run-time rollups for the
			// "/{slug}/dashboard" page. Optional ?project_id filter scopes
			// the rollup to a single project.
			r.Route("/api/dashboard", func(r chi.Router) {
				r.Get("/usage/daily", h.GetDashboardUsageDaily)
				r.Get("/usage/by-agent", h.GetDashboardUsageByAgent)
				r.Get("/agent-runtime", h.GetDashboardAgentRunTime)
				r.Get("/runtime/daily", h.GetDashboardRunTimeDaily)
				r.Get("/failures/daily", h.GetDashboardFailuresDaily)
				r.Get("/failures/by-agent", h.GetDashboardFailuresByAgent)
			})

			// Runtimes
			r.Route("/api/runtimes", func(r chi.Router) {
				r.Get("/", h.ListAgentRuntimes)
				r.Get("/fc-e2b/templates", h.ListFCE2BTemplates)
				r.Post("/fc-e2b", h.CreateFCE2BRuntime)
				r.Post("/cloud-sandbox", h.CreateCloudSandboxRuntime)
				r.Post("/asb-credential/validate", h.ValidateASBRuntimeCredential)
				r.Route("/{runtimeId}", func(r chi.Router) {
					r.Patch("/", h.UpdateAgentRuntime)
					r.With(handler.RequireHumanActor).Put("/owner", h.TransferRuntimeOwner)
					r.Patch("/fc-e2b-template", h.UpdateFCE2BRuntimeTemplate)
					r.Patch("/cloud-sandbox-artifact", h.UpdateCloudSandboxRuntimeArtifact)
					r.Get("/asb-network-policy", h.GetASBRuntimeNetworkPolicy)
					r.Get("/asb-regions", h.GetASBRuntimeRegions)
					r.Put("/asb-network-policy", h.UpdateASBRuntimeNetworkPolicy)
					r.Get("/asb-credential", h.GetASBRuntimeCredential)
					r.Patch("/asb-credential", h.UpdateASBRuntimeCredential)
					r.Get("/usage", h.GetRuntimeUsage)
					r.Get("/usage/by-agent", h.GetRuntimeUsageByAgent)
					r.Get("/usage/by-hour", h.GetRuntimeUsageByHour)
					r.Get("/activity", h.GetRuntimeTaskActivity)
					r.Post("/update", h.InitiateUpdate)
					r.Get("/update/{updateId}", h.GetUpdate)
					r.Post("/models", h.InitiateListModels)
					r.Get("/models/{requestId}", h.GetModelListRequest)
					r.Post("/local-skills", h.InitiateListLocalSkills)
					r.Get("/local-skills/{requestId}", h.GetLocalSkillListRequest)
					r.Post("/local-skills/import", h.InitiateImportLocalSkill)
					r.Get("/local-skills/import/{requestId}", h.GetLocalSkillImportRequest)
					r.Delete("/", h.DeleteAgentRuntime)
					// Confirmed variant of DELETE: unbind every agent bound to
					// this runtime (they keep their configuration and chats and
					// need a new runtime to run again), cancel their tasks,
					// detach their task history, then delete the runtime — all
					// in one transaction. Used by the DeleteRuntimeDialog when
					// the strict DELETE refused with
					// `runtime_has_active_agents` and the user confirmed.
					r.Post("/unbind-agents-and-delete", h.UnbindAgentsAndDeleteRuntime)
					// Legacy path for installed clients built against the
					// archive-and-delete contract (MUL-5559 renamed the
					// behaviour, not just the route). Same handler.
					r.Post("/archive-agents-and-delete", h.UnbindAgentsAndDeleteRuntime)
				})
			})

			// Cloud Runtime fleet proxy. The remote service URL is configured
			// on SaaS API nodes only; self-hosted deployments return 503.
			r.Route("/api/cloud-runtime", func(r chi.Router) {
				r.Get("/", h.GetCloudRuntimeService)
				r.Get("/healthz", h.GetCloudRuntimeHealth)
				r.Get("/readyz", h.GetCloudRuntimeReady)
				r.Get("/nodes", h.ListCloudRuntimeNodes)
				r.Post("/nodes", h.CreateCloudRuntimeNode)
				r.Delete("/nodes", h.DeleteCloudRuntimeNode)
				r.Post("/nodes/start", h.StartCloudRuntimeNode)
				r.Post("/nodes/stop", h.StopCloudRuntimeNode)
				r.Post("/nodes/reboot", h.RebootCloudRuntimeNode)
				r.Post("/nodes/status", h.GetCloudRuntimeNodeStatus)
				r.Post("/nodes/exec", h.ExecCloudRuntimeNode)
			})

			// Tasks (user-facing, with ownership check)
			r.Post("/api/tasks/{taskId}/cancel", h.CancelTaskByUser)

			// Workspace-wide agent task snapshot for presence derivation:
			// every active task + each agent's most recent terminal task.
			r.Get("/api/agent-task-snapshot", h.ListWorkspaceAgentTaskSnapshot)

			// Independent workspace-level list backing the issues-header
			// "agents working" chip and its assignee-id Table filter.
			r.Get("/api/working-agents", h.ListWorkspaceWorkingAgents)

			// Workspace-wide daily agent activity (last 30d, anchored on
			// completed_at). Backs the Agents-list sparkline (trailing 7d
			// slice) AND the agent detail "Last 30 days" panel.
			r.Get("/api/agent-activity-30d", h.GetWorkspaceAgentActivity30d)

			// Workspace-wide 30-day run counts per agent for the Agents-list RUNS column.
			r.Get("/api/agent-run-counts", h.GetWorkspaceAgentRunCounts)

			r.Route("/api/chat/sessions", func(r chi.Router) {
				r.Post("/", h.CreateChatSession)
				r.Get("/", h.ListChatSessions)
				r.Route("/{sessionId}", func(r chi.Router) {
					r.Get("/", h.GetChatSession)
					r.Patch("/", h.UpdateChatSession)
					r.Patch("/pin", h.SetChatSessionPinned)
					r.Patch("/archive", h.SetChatSessionArchived)
					r.Delete("/", h.DeleteChatSession)
					r.Post("/messages", h.SendChatMessage)
					r.Post("/messages/{messageId}/received", h.AcknowledgeChatMessageReceived)
					r.Post("/onboarding", h.StartMikaOnboarding)
					// Explicit "refresh" of a turn's quick actions: re-runs the
					// daemon suggestion pass for the latest assistant reply (MUL-5149).
					r.Post("/quick-actions/regenerate", h.RegenerateChatQuickActions)
					r.Get("/messages", h.ListChatMessages)
					r.Get("/messages/page", h.ListChatMessagesPage)
					r.Get("/pending-task", h.GetPendingChatTask)
					r.Delete("/queued-tasks", h.ClearQueuedChatTasks)
					r.Post("/queued-tasks/{taskId}/prioritize", h.PrioritizeQueuedChatTask)
					r.Post("/read", h.MarkChatSessionRead)
					// Deferred-cancellation draft restores (#5219):
					// creator-only fetch + idempotent consume.
					r.Get("/draft-restores", h.ListChatDraftRestores)
					r.Delete("/draft-restores/{restoreId}", h.ConsumeChatDraftRestore)
				})
			})
			r.Get("/api/chat/pending-tasks", h.ListPendingChatTasks)
			r.Get("/api/chat/pending-tasks/has-any", h.HasPendingChatTasks)

			// Quick-agent bar: per-user pinned agents for one-tap new chats.
			r.Get("/api/chat/pinned-agents", h.ListChatPinnedAgents)
			r.Post("/api/chat/pinned-agents", h.PinChatAgent)
			r.Delete("/api/chat/pinned-agents/{agentId}", h.UnpinChatAgent)

			// Agent-facing channel reads (MUL-3871). The caller's task-scoped token
			// resolves to its own chat session; no session/channel id is passed, so
			// an agent can only read its own conversation. `history` is the channel
			// overview (top-level messages + thread metadata); `thread` reads one
			// thread (?id for a specific one, else the thread the session is in).
			r.Get("/api/chat/history", h.GetChatChannelHistory)
			r.Get("/api/chat/thread", h.GetChatThread)

			// Inbox
			r.Route("/api/inbox", func(r chi.Router) {
				r.Get("/", h.ListInbox)
				// Archived notifications, for the inbox's "Archived" sub-view.
				// Separate from "/" so the main list keeps its contract and
				// never carries the unbounded archive.
				r.Get("/archived", h.ListArchivedInbox)
				r.Get("/unread-count", h.CountUnreadInbox)
				// Cross-workspace unread summary: account-level, keyed on the
				// user. Backs the workspace-switcher dot for OTHER workspaces.
				r.Get("/unread-summary", h.UnreadInboxSummary)
				r.Post("/mark-all-read", h.MarkAllInboxRead)
				r.Post("/archive-all", h.ArchiveAllInbox)
				r.Post("/archive-all-read", h.ArchiveAllReadInbox)
				r.Post("/archive-completed", h.ArchiveCompletedInbox)
				r.Post("/{id}/read", h.MarkInboxRead)
				r.Post("/{id}/unread", h.MarkInboxUnread)
				r.Post("/{id}/archive", h.ArchiveInboxItem)
				r.Post("/{id}/unarchive", h.UnarchiveInboxItem)
			})

			// Notification preferences
			r.Route("/api/notification-preferences", func(r chi.Router) {
				r.Get("/", h.GetNotificationPreferences)
				r.Patch("/", h.PatchNotificationPreferences)
				r.Put("/", h.UpdateNotificationPreferences)
			})
		})
	})

	h.WorkspaceMCPDispatcher = r
	return r, h
}

// buildLarkConnector wires the real WS long-conn connector that talks
// to /callback/ws/endpoint directly with app_id/app_secret. The
// connector wraps every read with a ctx-cancel watchdog so lease loss /
// shutdown breaks the blocking ReadMessage in bounded time — the
// invariant §4.4 leans on. A single connector instance serves every
// installation; its Run is parameterized by the installation, so the
// feishuChannel hands it the per-installation row.
//
// If the endpoint fetcher fails to initialize (typically a malformed
// MULTICA_LARK_CALLBACK_BASE_URL), we log and fall back to the
// NoopConnector so the lease / supervisor lifecycle still exercises
// against real DB rows. Inbound messages are silently dropped until
// the config is fixed; the boot log labels the mode "noop" so the
// degraded state is visible.
//
// Returns the connector plus a short label for the boot log:
// "ws-long-conn" in the healthy case, "noop" in the fallback case.
func buildLarkConnector(installSvc *lark.InstallationService, apiClient lark.APIClient, cardActions lark.CardActionSink) (lark.EventConnector, string) {
	endpointFetcher, err := lark.NewHTTPConnectionTokenFetcher(lark.HTTPConnectionTokenConfig{
		BaseURL: strings.TrimSpace(os.Getenv("MULTICA_LARK_CALLBACK_BASE_URL")),
		Logger:  slog.Default(),
	})
	if err != nil {
		slog.Error("lark ws: endpoint fetcher init failed; falling back to noop", "error", err)
		return lark.NewNoopConnector(slog.Default()), "noop"
	}
	decoder := lark.NewLarkJSONFrameDecoder()
	dialer := lark.NewGorillaDialer()
	if proxyURL := strings.TrimSpace(os.Getenv("MULTICA_LARK_WS_PROXY_URL")); proxyURL != "" {
		dialer.ProxyURL = proxyURL
	}
	credsProvider := lark.CredentialsProviderFunc(func(ctx context.Context, inst lark.Installation) (lark.InstallationCredentials, error) {
		secret, err := installSvc.DecryptAppSecret(inst)
		if err != nil {
			return lark.InstallationCredentials{}, err
		}
		creds := lark.InstallationCredentials{
			AppID:     inst.AppID,
			AppSecret: secret,
			Region:    lark.RegionOrDefault(inst.Region),
		}
		if inst.TenantKey.Valid {
			creds.TenantKey = inst.TenantKey.String
		}
		return creds, nil
	})
	// Inbound enricher: expands quoted replies / forwarded bundles AND
	// prefetches a window of surrounding group history (MUL-3084) into the
	// agent's body via the IM API before dispatch. It shares the
	// connector's resolved credentials and runs under the connector's
	// EnrichTimeout so it cannot overrun the Lark long-conn ACK budget.
	enricher := lark.NewInboundEnricher(apiClient, lark.InboundEnricherConfig{
		RecentContextSize: lark.DefaultRecentContextSize,
		Logger:            slog.Default(),
	})
	conn, err := lark.NewWSLongConnConnector(lark.WSConnectorConfig{
		Dialer:              dialer,
		EndpointFetcher:     endpointFetcher,
		FrameDecoder:        decoder,
		Enricher:            enricher,
		CardActions:         cardActions,
		CredentialsProvider: credsProvider,
		Logger:              slog.Default(),
	})
	if err != nil {
		slog.Error("lark ws: connector init failed; falling back to noop", "error", err)
		return lark.NewNoopConnector(slog.Default()), "noop"
	}
	return conn, "ws-long-conn"
}

// membershipChecker implements realtime.MembershipChecker using database queries.
type membershipChecker struct {
	queries *db.Queries
}

func (mc *membershipChecker) IsMember(ctx context.Context, userID, workspaceID string) bool {
	_, err := mc.queries.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{
		UserID:      parseUUID(userID),
		WorkspaceID: parseUUID(workspaceID),
	})
	return err == nil
}

// patResolver implements realtime.PATResolver using database queries.
// patCache is shared with the Auth and DaemonAuth middlewares so a token
// revoke through any path invalidates the cache for all of them. Nil
// cache is supported and degrades to direct DB lookups.
type patResolver struct {
	queries *db.Queries
	cache   *auth.PATCache
}

func (pr *patResolver) ResolveToken(ctx context.Context, token string) (string, bool) {
	hash := auth.HashToken(token)

	if userID, ok := pr.cache.Get(ctx, hash); ok {
		return userID, true
	}

	pat, err := pr.queries.GetPersonalAccessTokenByHash(ctx, hash)
	if err != nil {
		return "", false
	}

	userID := util.UUIDToString(pat.UserID)

	var expiresAt time.Time
	if pat.ExpiresAt.Valid {
		expiresAt = pat.ExpiresAt.Time
	}
	pr.cache.Set(ctx, hash, userID, auth.TTLForExpiry(time.Now(), expiresAt))

	// Cache miss = first WS auth in this TTL window. Refresh last_used_at;
	// subsequent connects within the window skip the write.
	middleware.TouchPATLastUsed(pr.queries, pat.ID)

	return userID, true
}

// parseUUID is a thin alias for util.MustParseUUID. Call sites here are all
// internal round-trips of DB-sourced UUIDs (e.g. issue.ID, e.ActorID), so an
// invalid value indicates a programming error and should panic loudly.
func parseUUID(s string) pgtype.UUID {
	return util.MustParseUUID(s)
}

// optionalUUID returns a NULL pgtype.UUID for an empty string and otherwise
// behaves like parseUUID. Use this for actor IDs on events where the producer
// may legitimately be a "system" actor with no member/agent attribution
// (e.g. GitHub webhook auto-status sync) — the activity_log and inbox_item
// tables both allow actor_id to be NULL.
func optionalUUID(s string) pgtype.UUID {
	if s == "" {
		return pgtype.UUID{}
	}
	return util.MustParseUUID(s)
}

func splitAndTrim(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	res := make([]string, 0, len(parts))
	for _, p := range parts {
		trimmed := strings.TrimSpace(p)
		if trimmed != "" {
			res = append(res, trimmed)
		}
	}
	return res
}

func parseStableRuntimePublisherUserIDs(raw string) (map[string]struct{}, error) {
	values := splitAndTrim(raw)
	publishers := make(map[string]struct{}, len(values))
	for _, value := range values {
		id, err := util.ParseUUID(value)
		if err != nil {
			return nil, fmt.Errorf(
				"invalid MULTICA_FC_E2B_STABLE_PUBLISHER_USER_IDS entry %q: %w",
				value,
				err,
			)
		}
		publishers[util.UUIDToString(id)] = struct{}{}
	}
	return publishers, nil
}

func cloudRuntimeFleetURLFromEnv() string {
	if url := strings.TrimSpace(os.Getenv("MULTICA_CLOUD_FLEET_URL")); url != "" {
		return url
	}
	return strings.TrimSpace(os.Getenv("MULTICA_FLEET_URL"))
}

func agentIdentityControlBaseURLFromEnv() string {
	if baseURL := strings.TrimRight(strings.TrimSpace(os.Getenv("MULTICA_AGENT_IDENTITY_CONTROL_BASE_URL")), "/"); baseURL != "" {
		return baseURL
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("AONE_ENV_TYPE"))) {
	case "pre", "prepub", "staging":
		return "https://pre-agent-identity.dingtalk.com"
	default:
		return ""
	}
}

// composioStateSecret resolves the HMAC key for the connect-state. Prefers an
// explicit COMPOSIO_STATE_SECRET; otherwise derives a composio-specific key
// from JWT_SECRET via SHA-256 so the two signing domains never share an
// identical key. Returns nil when neither is set (composio stays disabled).
// internalConnectorCredentialKey selects one stable encryption domain on every
// replica. The explicit selector prevents a partially injected dedicated key
// from making replicas encrypt the same workspace with different keys.
func internalConnectorCredentialKey() ([]byte, string, error) {
	source := strings.TrimSpace(os.Getenv("MULTICA_INTERNAL_MCP_KEY_SOURCE"))
	switch source {
	case "", "dedicated":
		key, err := secretbox.LoadKey("MULTICA_INTERNAL_MCP_SECRET_KEY")
		return key, "dedicated", err
	case "jwt-derived":
		jwtSecret := os.Getenv("JWT_SECRET")
		if len(jwtSecret) < 32 {
			return nil, source, errors.New("JWT_SECRET must be set to at least 32 bytes")
		}
		mac := hmac.New(sha256.New, []byte(jwtSecret))
		_, _ = mac.Write([]byte("multica/internal-mcp-credential/v1"))
		return mac.Sum(nil), source, nil
	default:
		return nil, source, errors.New("unsupported internal connector key source")
	}
}

func composioStateSecret() []byte {
	if v := strings.TrimSpace(os.Getenv("COMPOSIO_STATE_SECRET")); v != "" {
		return []byte(v)
	}
	if v := strings.TrimSpace(os.Getenv("JWT_SECRET")); v != "" {
		sum := sha256.Sum256([]byte("composio-state:" + v))
		return sum[:]
	}
	return nil
}

// composioCallbackBaseURL resolves the public API base used to build the
// Composio callback URL. Prefers COMPOSIO_CALLBACK_BASE_URL, then the
// already-resolved MULTICA_PUBLIC_URL, then the app URL.
func composioCallbackBaseURL(publicURL string) string {
	if v := strings.TrimRight(strings.TrimSpace(os.Getenv("COMPOSIO_CALLBACK_BASE_URL")), "/"); v != "" {
		return v
	}
	if publicURL != "" {
		return publicURL
	}
	return appURLFromEnv()
}

// wecomMetricsOrNil keeps a typed nil out of the adapter's interface field.
// A *WecomMetrics that is nil still satisfies wecom.Metrics, so assigning it
// directly would give the adapter a non-nil interface holding a nil pointer —
// and the first counter call would panic on a deployment with /metrics off.
func wecomMetricsOrNil(m *obsmetrics.WecomMetrics) wecom.Metrics {
	if m == nil {
		return nil
	}
	return m
}
