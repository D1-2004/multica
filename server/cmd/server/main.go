package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/analytics"
	"github.com/multica-ai/multica/server/internal/daemonws"
	"github.com/multica-ai/multica/server/internal/deploymentfence"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/handler"
	"github.com/multica-ai/multica/server/internal/logger"
	obsmetrics "github.com/multica-ai/multica/server/internal/metrics"
	"github.com/multica-ai/multica/server/internal/realtime"
	"github.com/multica-ai/multica/server/internal/sandboxrelay"
	"github.com/multica-ai/multica/server/internal/scheduler"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/featureflag"
	"github.com/multica-ai/multica/server/pkg/runtimeconfig"
	"github.com/redis/go-redis/v9"
)

var (
	version = "dev"
	commit  = "unknown"
)

func newNamedRedisClient(base *redis.Options, suffix string) *redis.Client {
	opts := *base
	if envBool("REDIS_DISABLE_CLIENT_NAME", false) {
		opts.ClientName = ""
	} else {
		opts.ClientName = redisClientName(opts.ClientName, suffix)
	}
	return redis.NewClient(&opts)
}

func redisClientName(existing, suffix string) string {
	if suffix == "" {
		return existing
	}
	if existing != "" {
		return existing + ":" + suffix
	}
	return "multica-api:" + suffix
}

func closeRedisClient(label string, client *redis.Client) {
	if client == nil {
		return
	}
	if err := client.Close(); err != nil {
		slog.Warn("redis client close failed", "client", label, "error", err)
	}
}

func shardedRelayConfigFromEnv() realtime.ShardedStreamRelayConfig {
	cfg := realtime.DefaultShardedStreamRelayConfig()
	cfg.Shards = envPositiveInt("REALTIME_RELAY_SHARDS", cfg.Shards)
	cfg.StreamMaxLen = envPositiveInt64("REALTIME_RELAY_STREAM_MAXLEN", cfg.StreamMaxLen)
	cfg.ReadCount = envPositiveInt64("REALTIME_RELAY_XREAD_COUNT", cfg.ReadCount)
	cfg.ReadBlock = envDuration("REALTIME_RELAY_XREAD_BLOCK", cfg.ReadBlock)
	cfg.ReplayGrace = envDuration("REALTIME_RELAY_REPLAY_GRACE", cfg.ReplayGrace)
	return cfg
}

func realtimeRelayModeFromEnv() string {
	const defaultMode = "sharded"
	raw := strings.ToLower(strings.TrimSpace(os.Getenv("REALTIME_RELAY_MODE")))
	if raw == "" {
		return defaultMode
	}
	switch raw {
	case "sharded", "dual", "legacy":
		return raw
	default:
		slog.Warn("invalid env var, using default", "name", "REALTIME_RELAY_MODE", "value", raw, "default", defaultMode)
		return defaultMode
	}
}

func envPositiveInt(name string, def int) int {
	raw := os.Getenv(name)
	if raw == "" {
		return def
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v <= 0 {
		slog.Warn("invalid env var, using default", "name", name, "value", raw, "default", def, "error", err)
		return def
	}
	return v
}

func envPositiveInt64(name string, def int64) int64 {
	raw := os.Getenv(name)
	if raw == "" {
		return def
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || v <= 0 {
		slog.Warn("invalid env var, using default", "name", name, "value", raw, "default", def, "error", err)
		return def
	}
	return v
}

func envDuration(name string, def time.Duration) time.Duration {
	raw := os.Getenv(name)
	if raw == "" {
		return def
	}
	v, err := time.ParseDuration(raw)
	if err != nil || v <= 0 {
		slog.Warn("invalid env var, using default", "name", name, "value", raw, "default", def.String(), "error", err)
		return def
	}
	return v
}

func envNonNegativeDuration(name string, def time.Duration) time.Duration {
	raw := os.Getenv(name)
	if raw == "" {
		return def
	}
	v, err := time.ParseDuration(raw)
	if err != nil || v < 0 {
		slog.Warn("invalid env var, using default", "name", name, "value", raw, "default", def.String(), "error", err)
		return def
	}
	return v
}

func holdBeforeShutdown(sig os.Signal, signals <-chan os.Signal, duration time.Duration) {
	if duration <= 0 {
		return
	}
	slog.Info("termination signal received; holding before shutdown",
		"signal", sig.String(),
		"duration", duration.String(),
	)
	timer := time.NewTimer(duration)
	defer timer.Stop()

	select {
	case <-timer.C:
		slog.Info("shutdown hold complete", "duration", duration.String())
	case interruptSig := <-signals:
		slog.Info("shutdown hold interrupted by signal",
			"signal", interruptSig.String(),
			"configured_duration", duration.String(),
		)
	}
}

func envBool(name string, def bool) bool {
	raw := os.Getenv(name)
	if raw == "" {
		return def
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		slog.Warn("invalid env var, using default", "name", name, "value", raw, "default", def, "error", err)
		return def
	}
	return v
}

func backgroundServices(h *handler.Handler) (*service.TaskService, *service.AutopilotService) {
	return h.TaskService, h.AutopilotService
}

func main() {
	logger.Init()

	// Warn about missing configuration
	if os.Getenv("JWT_SECRET") == "" {
		slog.Warn("JWT_SECRET is not set — using insecure default. Set JWT_SECRET for production use.")
	}
	if os.Getenv("RESEND_API_KEY") == "" && strings.TrimSpace(os.Getenv("SMTP_HOST")) == "" {
		slog.Warn("no email backend configured (RESEND_API_KEY and SMTP_HOST both empty) — verification codes will be printed to the log instead of emailed.")
	}
	if os.Getenv("MULTICA_DEV_VERIFICATION_CODE") != "" {
		if strings.EqualFold(strings.TrimSpace(os.Getenv("APP_ENV")), "production") {
			slog.Warn("MULTICA_DEV_VERIFICATION_CODE is set but ignored because APP_ENV=production.")
		} else {
			slog.Warn("MULTICA_DEV_VERIFICATION_CODE is enabled. Use it only for local development or private test instances.")
		}
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	shutdownHoldDuration := envNonNegativeDuration("MULTICA_SHUTDOWN_HOLD_DURATION", 0)

	remoteRuntimeConfig, err := runtimeconfig.NewFromEnv(slog.Default())
	if err != nil {
		slog.Error("required runtime configuration failed to load", "error", err)
		os.Exit(1)
	}
	appRuntimeConfig, err := newAppRuntimeConfig(remoteRuntimeConfig)
	if err != nil {
		slog.Error("required runtime configuration is incomplete", "error", err)
		_ = remoteRuntimeConfig.Close()
		os.Exit(1)
	}

	flagOptions := []featureflag.Option{featureflag.WithLogger(slog.Default())}
	if appRuntimeConfig != nil {
		flagOptions = append(flagOptions, featureflag.WithProvider(runtimeFeatureFlagProvider{config: appRuntimeConfig}))
	}
	flags, err := featureflag.NewServiceFromEnv(flagOptions...)
	if err != nil {
		slog.Error("feature flag configuration failed to load", "error", err)
		_ = remoteRuntimeConfig.Close()
		os.Exit(1)
	}
	sandboxRelaySigner, err := sandboxrelay.LoadSignerFromEnv()
	if err != nil {
		slog.Error("sandbox relay signing configuration failed", "error", err)
		closeConfigResourcesAndExit(flags, remoteRuntimeConfig, 1)
	}
	sandboxHTTPRelay, err := sandboxrelay.LoadRelayFromEnv()
	if err != nil {
		slog.Error("sandbox relay forwarding configuration failed", "error", err)
		closeConfigResourcesAndExit(flags, remoteRuntimeConfig, 1)
	}
	if sandboxRelaySigner != nil && sandboxHTTPRelay != nil {
		slog.Error("sandbox relay signer and forwarding relay must not be enabled on the same deployment")
		closeConfigResourcesAndExit(flags, remoteRuntimeConfig, 1)
	}
	if sandboxRelaySigner != nil {
		slog.Info("sandbox relay signing enabled")
	}
	var sandboxRelayMiddleware func(http.Handler) http.Handler
	if sandboxHTTPRelay != nil {
		sandboxRelayMiddleware = sandboxHTTPRelay.Middleware
		slog.Info("sandbox relay forwarding enabled")
	}

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://multica:multica@localhost:5432/multica?sslmode=disable"
	}

	// Connect to database
	ctx := context.Background()
	pool, err := newDBPool(ctx, dbURL)
	if err != nil {
		slog.Error("unable to connect to database", "error", err)
		closeConfigResourcesAndExit(flags, remoteRuntimeConfig, 1)
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		slog.Error("unable to ping database", "error", err)
		closeConfigResourcesAndExit(flags, remoteRuntimeConfig, 1)
	}
	slog.Info("connected to database")
	logPoolConfig(pool)

	bus := events.New()
	hub := realtime.NewHub()
	go hub.Run()
	daemonHub := daemonws.NewHub()
	var daemonWakeup service.TaskWakeupNotifier = daemonHub

	// MUL-1138: when REDIS_URL is set, route fanout through a Redis relay so
	// multiple API nodes can deliver each other's events. Without it the hub
	// is the sole broadcaster and the server stays single-node (legacy).
	// Runtime local-skill stores and realtime relay traffic use separate Redis
	// clients so blocking stream consumers cannot starve request-path Redis
	// operations.
	relayCtx, relayCancel := context.WithCancel(context.Background())
	var broadcaster realtime.Broadcaster = hub
	var storeRedis *redis.Client
	var relayWriteRedis *redis.Client
	var relayReadRedis *redis.Client
	var shardedReadRedis *redis.Client
	var legacyReadRedis *redis.Client
	var relay realtime.ManagedRelay
	defer func() {
		if relay != nil {
			relay.Stop()
		}
		relayCancel()
		if relay != nil {
			relay.Wait()
		}
		closeRedisClient("realtime-read-legacy", legacyReadRedis)
		closeRedisClient("realtime-read-sharded", shardedReadRedis)
		closeRedisClient("realtime-read", relayReadRedis)
		closeRedisClient("realtime-write", relayWriteRedis)
		closeRedisClient("store", storeRedis)
	}()
	redisOpts, redisSource, err := redisOptionsFromEnv()
	if err != nil {
		slog.Error("Redis configuration failed", "error", err)
		closeConfigResourcesAndExit(flags, remoteRuntimeConfig, 1)
	}
	if redisOpts != nil {
		if envBool("REDIS_DISABLE_CLIENT_NAME", false) {
			slog.Info("redis: CLIENT SETNAME disabled (REDIS_DISABLE_CLIENT_NAME=true) for managed Redis compatibility")
		}
		storeRedis = newNamedRedisClient(redisOpts, "store")
		if err := storeRedis.Ping(ctx).Err(); err != nil {
			slog.Error("unable to ping Redis", "source", redisSource, "error", err)
			closeConfigResourcesAndExit(flags, remoteRuntimeConfig, 1)
		}
		slog.Info("connected to Redis", "source", redisSource)
		relayWriteRedis = newNamedRedisClient(redisOpts, "realtime-write")

		relayMode := realtimeRelayModeFromEnv()
		relayConfig := shardedRelayConfigFromEnv()
		switch relayMode {
		case "legacy":
			relayReadRedis = newNamedRedisClient(redisOpts, "realtime-read")
			relay = realtime.NewRedisRelayWithClients(hub, relayWriteRedis, relayReadRedis)
			slog.Info("daemon websocket wakeup: Redis fanout disabled in legacy realtime relay mode")
		case "dual":
			shardedReadRedis = newNamedRedisClient(redisOpts, "realtime-read-sharded")
			legacyReadRedis = newNamedRedisClient(redisOpts, "realtime-read-legacy")
			sharded := realtime.NewShardedStreamRelay(hub, relayWriteRedis, shardedReadRedis, relayConfig)
			sharded.SetDaemonRuntimeDeliverer(daemonHub)
			legacy := realtime.NewRedisRelayWithClients(hub, relayWriteRedis, legacyReadRedis)
			relay = realtime.NewMirroredRelay(sharded, legacy)
			daemonWakeup = daemonws.NewRelayNotifier(daemonHub, sharded)
		default:
			relayReadRedis = newNamedRedisClient(redisOpts, "realtime-read")
			sharded := realtime.NewShardedStreamRelay(hub, relayWriteRedis, relayReadRedis, relayConfig)
			sharded.SetDaemonRuntimeDeliverer(daemonHub)
			relay = sharded
			daemonWakeup = daemonws.NewRelayNotifier(daemonHub, sharded)
		}
		relay.Start(relayCtx)
		broadcaster = realtime.NewDualWriteBroadcaster(hub, relay)
		slog.Info(
			"realtime: Redis relay enabled",
			"node_id", relay.NodeID(),
			"mode", relayMode,
			"shards", relayConfig.Shards,
			"stream_max_len", relayConfig.StreamMaxLen,
			"xread_count", relayConfig.ReadCount,
			"xread_block", relayConfig.ReadBlock.String(),
			"store_pool_size", redisOpts.PoolSize,
			"realtime_write_pool_size", redisOpts.PoolSize,
			"realtime_read_pool_size", redisOpts.PoolSize,
		)
	} else {
		slog.Info("realtime: REDIS_URL not set — using in-memory hub (single-node mode)")
		// Realtime browser fanout stays single-node, but daemon wakeups cannot:
		// a daemon's wakeup socket is pinned to one replica while the task that
		// should wake it is enqueued by whichever replica served the request, so
		// with the bare hub roughly every other wakeup is delivered to a node
		// that does not hold the socket and the daemon waits out a full poll
		// interval instead. Relay the hint through Postgres LISTEN/NOTIFY, which
		// every deployment already has, so cross-node delivery works without
		// Redis. Redis, when present, keeps using the richer stream relay above.
		pgNotifier := daemonws.NewPGNotifier(daemonHub, pool, slog.Default())
		go pgNotifier.Listen(relayCtx)
		daemonWakeup = pgNotifier
		slog.Info("daemon wakeup: relaying through Postgres LISTEN/NOTIFY",
			"channel", daemonws.PGNotifyChannel)
	}
	registerListeners(bus, broadcaster)

	analyticsClient := analytics.NewFromEnv()
	defer analyticsClient.Close()

	queries := db.New(pool)
	hub.SetAuthorizer(newScopeAuthorizer(queries))
	// Order matters: subscriber listeners must register BEFORE notification listeners.
	// The notification listener queries the subscriber table to determine recipients,
	// so subscribers must be written first within the same synchronous event dispatch.
	registerSubscriberListeners(bus, pool)
	registerActivityListeners(bus, queries)
	registerNotificationListeners(bus, queries)

	metricsConfig := obsmetrics.ConfigFromEnv()
	var metricsServer *http.Server
	var httpMetrics *obsmetrics.HTTPMetrics
	var businessMetrics *obsmetrics.BusinessMetrics
	var samplerPool *pgxpool.Pool
	var channelMediaMetrics *obsmetrics.ChannelMediaReconcilerMetrics
	var wecomMetrics *obsmetrics.WecomMetrics
	if metricsConfig.Enabled() {
		// Build a dedicated tiny pool for the BusinessSamplerCollector
		// so a stalled scrape can never starve business traffic. If the
		// pool fails to construct we log and continue without the
		// sampler — the rest of /metrics is still useful.
		var err error
		samplerPool, err = newSamplerDBPool(ctx, dbURL)
		if err != nil {
			slog.Warn("metrics: failed to build sampler pgxpool; sampler disabled", "error", err)
			samplerPool = nil
		}

		metricsRegistry := obsmetrics.NewRegistry(obsmetrics.RegistryOptions{
			Pool:     pool,
			Realtime: realtime.M,
			DaemonWS: daemonws.M,
			Version:  version,
			Commit:   commit,
			BusinessSampler: func() *obsmetrics.BusinessSamplerOptions {
				if samplerPool == nil {
					return nil
				}
				return &obsmetrics.BusinessSamplerOptions{Pool: samplerPool}
			}(),
		})
		httpMetrics = metricsRegistry.HTTP
		businessMetrics = metricsRegistry.Business
		channelMediaMetrics = metricsRegistry.ChannelMedia
		wecomMetrics = metricsRegistry.Wecom
		// Forward inbound daemon WS frames into the per-kind counter so
		// dashboards can split heartbeat / unknown / invalid traffic.
		if daemonHub != nil {
			daemonHub.SetMessageKindRecorder(businessMetrics)
		}
		metricsServer = obsmetrics.NewServer(metricsConfig.Addr, metricsRegistry.Gatherer)
		if !obsmetrics.IsLoopbackAddr(metricsConfig.Addr) {
			slog.Warn(
				"metrics listener is not loopback-only; restrict access with private networking, allowlists, or proxy auth",
				"addr", metricsConfig.Addr,
			)
		}
	}
	if samplerPool != nil {
		defer samplerPool.Close()
	}

	// Construct the BatchedHeartbeatScheduler before the router so it can
	// be injected into the Handler. The Run goroutine starts below
	// alongside the sweeper, and Stop is called explicitly during graceful
	// shutdown so any pending bumps are flushed before we exit.
	heartbeatScheduler := handler.NewBatchedHeartbeatScheduler(queries, handler.DefaultHeartbeatBatchInterval)
	instanceID, err := os.Hostname()
	if err != nil {
		slog.Error("deployment fence instance identity failed", "error", err)
		closeConfigResourcesAndExit(flags, remoteRuntimeConfig, 1)
	}
	deploymentFence, err := deploymentfence.New(ctx, pool, instanceID, version+"@"+commit)
	if err != nil {
		slog.Error("deployment fence initialization failed", "error", err)
		closeConfigResourcesAndExit(flags, remoteRuntimeConfig, 1)
	}
	go deploymentFence.Run(relayCtx)

	r, h := NewRouterWithOptions(pool, hub, bus, analyticsClient, storeRedis, RouterOptions{
		HTTPMetrics:        httpMetrics,
		BusinessMetrics:    businessMetrics,
		WecomMetrics:       wecomMetrics,
		DaemonHub:          daemonHub,
		DaemonWakeup:       daemonWakeup,
		RunnerRelay:        relay,
		FeatureFlags:       flags,
		HeartbeatScheduler: heartbeatScheduler,
		SandboxRelaySigner: sandboxRelaySigner,
		SandboxRelay:       sandboxRelayMiddleware,
		RuntimeConfig:      appRuntimeConfig,
		DeploymentFence:    deploymentFence,
	})

	srv := &http.Server{
		Addr:    ":" + port,
		Handler: r,
	}

	// Start background workers.
	sweepCtx, sweepCancel := context.WithCancel(context.Background())
	autopilotCtx, autopilotCancel := context.WithCancel(context.Background())
	// Reuse the router's services here. In particular, the router wires the
	// EmptyClaim cache into TaskService; constructing a second TaskService for
	// scheduled Autopilot dispatch would send the daemon wakeup without bumping
	// that cache's version, so an idle runtime could keep returning an empty
	// claim until the cache TTL expires.
	taskSvc, autopilotSvc := backgroundServices(h)
	registerAutopilotListeners(bus, autopilotSvc)

	// Construct a LivenessStore that mirrors the one wired into the HTTP
	// handler. Both the heartbeat write path (handler) and the sweeper read
	// path (here) must agree on the same Redis-or-Noop choice; if they
	// disagree, online runtimes get falsely marked offline.
	var liveness handler.LivenessStore = handler.NewNoopLivenessStore()
	if storeRedis != nil {
		liveness = handler.NewRedisLivenessStore(storeRedis)
	}

	// Start background sweeper to mark stale runtimes as offline.
	go runRuntimeSweeper(sweepCtx, queries, liveness, taskSvc, bus)
	go runRunnerArtifactCleanup(sweepCtx, queries)
	go taskSvc.RunDeferredChannelTaskPromoter(sweepCtx)
	if h.ASBLauncher != nil {
		go h.ASBLauncher.RunCapacityWaiter(sweepCtx)
	}
	go heartbeatScheduler.Run(sweepCtx)
	if h.FCE2BStable != nil {
		go h.FCE2BStable.Run(sweepCtx)
	}
	if h.EnterpriseIdentity != nil {
		go h.EnterpriseIdentity.Run(sweepCtx)
	}
	go runAutopilotFailureMonitor(autopilotCtx, queries, bus, envFailureMonitorConfig())
	go runDBStatsLogger(sweepCtx, pool)
	if h.WebhookDeliveryWorker != nil {
		go h.WebhookDeliveryWorker.Run(sweepCtx)
	}
	if h.A2APushWorker != nil {
		go h.A2APushWorker.Run(sweepCtx)
	}
	if h.TaskCompletionWorker != nil {
		go h.TaskCompletionWorker.Run(sweepCtx)
	}
	if h.InboundCoordinatorWorker != nil {
		go h.InboundCoordinatorWorker.Run(sweepCtx)
	}
	if h.DingTalkStreamInbox != nil {
		go h.DingTalkStreamInbox.Run(sweepCtx)
	}
	if h.ManagedAgent != nil && h.ManagedAgent.Enabled() {
		go h.ManagedAgent.Run(sweepCtx)
	}
	// GitHub PR-card API snapshot pipeline (MUL-5265): worker pool + TTL sweeper.
	// No-op when unconfigured (no App private key).
	h.PRRefresh.Start(sweepCtx)

	// Channel inbound supervisor (MUL-3620): holds the §4.4 WS lease per
	// installation and drives each channel.Channel. It is built
	// unconditionally (it is channel-agnostic, not Lark-specific), so it
	// always exists here; with no platform registered or no installation
	// rows it simply idles. Lifecycle is bound to sweepCtx so it winds down
	// alongside the other long-running workers, AFTER the HTTP server has
	// drained.
	if h.ChannelSupervisor != nil {
		go h.ChannelSupervisor.Run(sweepCtx)
	}

	// Media intent-ledger reconciler (PR #5580): settles uploaded-but-unbound
	// channel media objects. An independent worker so object-storage latency
	// spikes cannot starve any other sweeper's cadence.
	if h.ChannelMediaReconciler != nil {
		h.ChannelMediaReconciler.Metrics = channelMediaMetrics
		go h.ChannelMediaReconciler.Run(sweepCtx)
	}

	// MUL-2957: DB-backed execution scheduler. The scheduler turns the
	// `sys_cron_executions` table into the distributed lease + audit
	// log for internal periodic jobs. The first job is
	// `rollup_task_usage_hourly`, which replaces the previously
	// operator-registered `pg_cron` entry (still safe to run
	// concurrently — the SQL function holds advisory lock 4246).
	//
	// A failure to register the job is treated as fatal here only at
	// the registration step (a duplicate name is the only realistic
	// cause and indicates a code bug). Once running, the manager
	// surfaces transient errors — DB unreachable, sys_cron_executions
	// missing because of an unusual partial-migration state — by
	// logging them on the tick that fails and retrying on the next
	// cycle, so a temporary outage does not crash the server.
	schedulerMgr := scheduler.NewManager(pool, scheduler.Options{})
	if err := schedulerMgr.Register(scheduler.TaskUsageHourlyJob(pool)); err != nil {
		slog.Warn("scheduler: failed to register task_usage_hourly rollup job", "error", err)
	}
	// MUL-3551: scheduled-Autopilot dispatch runs on the same DB-backed
	// scheduler. The job owns its plan_times via PlansForScope (each
	// trigger has its own cron expression, so the Cadence planner does
	// not fit). Crash recovery, occurrence-level idempotency, lease
	// theft, and retry are all reused from the manager + sys_cron_executions
	// — there is no separate goroutine for scheduled Autopilot anymore.
	if err := schedulerMgr.Register(scheduler.AutopilotScheduleDispatchJob(pool, queries, autopilotSvc)); err != nil {
		slog.Warn("scheduler: failed to register autopilot_schedule_dispatch job", "error", err)
	}
	go func() {
		_ = schedulerMgr.Run(sweepCtx)
	}()

	if metricsServer != nil {
		go func() {
			slog.Info("metrics server starting", "addr", metricsConfig.Addr)
			if err := metricsServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				slog.Error("metrics server disabled after startup error", "error", err)
			}
		}()
	}

	go func() {
		slog.Info("server starting", "port", port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server error", "error", err)
			closeConfigResourcesAndExit(flags, remoteRuntimeConfig, 1)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	sig := <-quit
	holdBeforeShutdown(sig, quit, shutdownHoldDuration)
	// Restore the default behavior so another signal during graceful shutdown
	// can still terminate the process instead of being left unread in quit.
	signal.Stop(quit)

	slog.Info("shutting down server")
	// Start Stream handoff immediately while HTTP and background workers are
	// still alive. DRAINING connections continue durable admission until
	// replacement READY capacity exists; no new supervisors start afterwards.
	if h.ChannelSupervisor != nil {
		h.ChannelSupervisor.BeginShutdown()
	}
	autopilotCancel()

	// Order matters: drain in-flight HTTP first so any heartbeat handlers
	// finish calling Schedule() before we stop the scheduler. Otherwise a
	// late heartbeat could enqueue a pending ID after Run has already
	// drained and exited, and Stop() would not flush it.
	apiShutdownCtx, apiShutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	if err := srv.Shutdown(apiShutdownCtx); err != nil {
		apiShutdownCancel()
		slog.Error("server forced to shutdown", "error", err)
		closeConfigResourcesAndExit(flags, remoteRuntimeConfig, 1)
	}
	apiShutdownCancel()
	if h.ChannelSupervisor != nil {
		if !h.ChannelSupervisor.WaitForHandoffs(h.ChannelSupervisor.StreamDrainTimeout()) {
			slog.Warn("channel supervisor: graceful Stream handoff timed out; forcing shutdown",
				"event", "channel_stream_handoff_timeout",
				"timeout", h.ChannelSupervisor.StreamDrainTimeout().String(),
			)
		}
	}

	// HTTP is fully drained — safe to stop the sweeper and flush the
	// final batch of queued heartbeat bumps.
	sweepCancel()
	heartbeatScheduler.Stop()
	if h.WebhookDeliveryWorker != nil && !h.WebhookDeliveryWorker.WaitWithTimeout(5*time.Second) {
		slog.Warn("webhook delivery worker did not exit within shutdown timeout")
	}
	if h.A2APushWorker != nil && !h.A2APushWorker.WaitWithTimeout(5*time.Second) {
		slog.Warn("A2A push worker did not exit within shutdown timeout")
	}
	if h.TaskCompletionWorker != nil && !h.TaskCompletionWorker.WaitWithTimeout(5*time.Second) {
		slog.Warn("task completion worker did not exit within shutdown timeout")
	}
	if h.InboundCoordinatorWorker != nil && !h.InboundCoordinatorWorker.WaitWithTimeout(5*time.Second) {
		slog.Warn("inbound coordinator worker did not exit within shutdown timeout")
	}
	if h.DingTalkStreamInbox != nil && !h.DingTalkStreamInbox.WaitWithTimeout(5*time.Second) {
		slog.Warn("dingtalk stream inbox worker did not exit within shutdown timeout",
			"event", "dingtalk_stream_inbox_shutdown_timeout",
		)
	}

	// Join the channel supervisor's per-installation goroutines so the
	// lease renewer can issue a final release before process exit;
	// otherwise the next replica would have to wait the full LeaseTTL
	// before picking up the installation on the other side of the
	// redeploy. The wait is bounded — if a supervisor is wedged (DB
	// pool stalled, a connector ignoring ctx, etc.) the fallback is the
	// natural LeaseTTL expiry on the other side, which is strictly better
	// than holding shutdown open forever. Then drain the Feishu runtime:
	// the supervisors have stopped delivering inbound events, so flush the
	// debounced run triggers and join any in-flight outbound replies
	// (each bounded by ReplyTimeout) so a binding card / offline notice is
	// not lost on shutdown.
	if h.ChannelSupervisor != nil {
		if !h.ChannelSupervisor.WaitWithTimeout(h.ChannelSupervisor.ShutdownTimeout()) {
			slog.Warn("channel supervisor: connections did not exit within shutdown timeout; proceeding",
				"timeout", h.ChannelSupervisor.ShutdownTimeout().String(),
			)
		}
		if h.ChannelRouter != nil {
			drainCtx, drainCancel := context.WithTimeout(context.Background(), 10*time.Second)
			if !h.ChannelRouter.Drain(drainCtx) {
				slog.Warn("channel router: drain deadline reached; deferred media fallback remains durable")
			}
			drainCancel()
		}
	}

	if metricsServer != nil {
		metricsShutdownCtx, metricsShutdownCancel := context.WithTimeout(context.Background(), 3*time.Second)
		if err := metricsServer.Shutdown(metricsShutdownCtx); err != nil {
			slog.Error("metrics server forced to shutdown", "error", err)
		}
		metricsShutdownCancel()
	}
	_ = flags.Close()
	_ = remoteRuntimeConfig.Close()
	slog.Info("server stopped")
}

func closeConfigResourcesAndExit(flags *featureflag.Service, runtime *runtimeconfig.Service, code int) {
	_ = flags.Close()
	_ = runtime.Close()
	os.Exit(code)
}
