package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/attributionbackfill"
	"github.com/multica-ai/multica/server/internal/logger"
	"github.com/multica-ai/multica/server/internal/migrations"
	"github.com/multica-ai/multica/server/internal/taskusagebackfill"
)

// preMigrationHook runs work that must happen before a specific
// migration is applied during `migrate up`. Hooks are idempotent and
// must not depend on the migration loop's session-pinned advisory lock
// — they run on the pool, not on the loop's pinned conn, so they can
// safely acquire other session-level locks (e.g. advisory lock 4246
// for the task_usage hourly rollup).
//
// Returning an error aborts the migration run. The corresponding
// migration is NOT recorded in schema_migrations, so the next run will
// retry the hook + migration.
type preMigrationHook func(ctx context.Context, pool *pgxpool.Pool) error

// preMigrationHooks wires migration version → hook. The version key is
// the file basename without the `.up.sql` suffix, matching what
// `migrations.ExtractVersion` returns.
//
// MUL-2957: the v0.3.4 → current direct-upgrade path needs the hourly
// rollup seeded BEFORE migration 103 evaluates its fail-closed lag
// guard, because at `cmd/migrate up` time the server has not yet
// started so neither the legacy pg_cron job nor the new app scheduler
// can advance the watermark. The hook runs the same idempotent
// monthly-slice backfill that
// `cmd/backfill_task_usage_hourly` exposes to operators.
//
// MUL-4897 / GH #5544: migration 198 VALIDATEs the strict attribution
// constraint installed by 197, which drops migration 190's
// originator_source IS NULL exemption. Self-hosted databases never ran the
// out-of-band backfill that Multica's cloud did, so their legacy rows make
// 198 fail closed and the backend refuses to start. The hook reconciles
// those rows (accountable_user_id := originator_user_id) idempotently BEFORE
// VALIDATE, so a stuck-at-197 instance auto-heals on `migrate up` with no
// manual SQL. A higher-numbered migration cannot help — the instance never
// reaches a version above the failing 198.
//
// GH #6388: migration 257 builds a replacement unique index concurrently. A
// failed build can leave an INVALID relation that IF NOT EXISTS would otherwise
// mistake for a successful retry. The hook removes only that invalid leftover;
// migration 257 can then rebuild it while the valid v1 index remains in place.
//
// MUL-5823: migration 261 replaces the terminal-task partial index the same
// way, so it carries the same hazard — an INVALID v2 leftover recorded as
// success would let migration 262 drop the still-valid v1, leaving all four
// dashboard rollups on a full table scan.
//
// Internal environments may already contain origin_type='agent_mcp' rows from
// the historical 271_agent_mcp_issue_delegation migration. Upstream migration
// 259 does not know that fork-owned value and installs a NOT VALID constraint
// without it; the hook widens the constraint before migration 260 validates it.
var preMigrationHooks = map[string]preMigrationHook{
	"103_drop_legacy_daily_rollups":                         runTaskUsageHourlyHook,
	"198_agent_task_attribution_strict_constraint_validate": runAttributionStrictHook,
	"257_agent_task_queue_channel_media_pending_unique_v2":  cleanupInvalidConcurrentIndexHook("idx_one_pending_task_per_issue_agent_v2"),
	"260_issue_origin_dingtalk_chat_validate":               repairIssueOriginTypeConstraintHook,
	"261_agent_task_queue_terminal_completed_at_v2":         cleanupInvalidConcurrentIndexHook("idx_agent_task_queue_terminal_completed_at_v2"),
	// Context capability unique indexes are ON CONFLICT arbiters. An INVALID
	// leftover would satisfy IF NOT EXISTS yet never serve as an arbiter.
	"9401_context_capability_binding_scope_idx":    cleanupInvalidConcurrentIndexHook("context_capability_binding_scope_idx"),
	"9402_context_capability_binding_resource_idx": cleanupInvalidConcurrentIndexHook("context_capability_binding_resource_idx"),
	"9404_context_connector_credential_scope_idx":  cleanupInvalidConcurrentIndexHook("context_connector_credential_scope_idx"),
	"9406_context_config_grant_scope_idx":          cleanupInvalidConcurrentIndexHook("context_config_grant_scope_idx"),
	// One official app connector per workspace and catalog slug.
	"9410_internal_connector_catalog_slug_idx": cleanupInvalidConcurrentIndexHook("internal_connector_catalog_slug_idx"),
	// One scene configuration row per agent scene (ON CONFLICT arbiter).
	"9414_agent_scene_config_scene_idx": cleanupInvalidConcurrentIndexHook("agent_scene_config_scene_idx"),
	// One custom MCP server configuration per scene or person scope (ON
	// CONFLICT arbiter).
	"9419_context_scope_mcp_config_scope_idx": cleanupInvalidConcurrentIndexHook("context_scope_mcp_config_scope_idx"),
	// One tenant row per agent and org, and one prompt component per scope
	// and name (ON CONFLICT arbiters).
	"9421_agent_tenant_org_idx":              cleanupInvalidConcurrentIndexHook("agent_tenant_org_idx"),
	"9423_context_prompt_component_name_idx": cleanupInvalidConcurrentIndexHook("context_prompt_component_name_idx"),
}

func repairIssueOriginTypeConstraintHook(ctx context.Context, pool *pgxpool.Pool) error {
	if _, err := pool.Exec(ctx, `
		ALTER TABLE issue DROP CONSTRAINT IF EXISTS issue_origin_type_check;
		ALTER TABLE issue ADD CONSTRAINT issue_origin_type_check
			CHECK (origin_type IN (
				'autopilot',
				'quick_create',
				'lark_chat',
				'slack_chat',
				'agent_create',
				'dingtalk_chat',
				'agent_mcp'
			))
			NOT VALID
	`); err != nil {
		return fmt.Errorf("repair issue origin type constraint before validation: %w", err)
	}
	return nil
}

// cleanupInvalidConcurrentIndexHook removes an INVALID index left by an
// interrupted or failed CREATE INDEX CONCURRENTLY before the migration retries.
// Without this guard, CREATE INDEX ... IF NOT EXISTS would treat the leftover
// relation as success and allow a later migration to drop the still-valid old
// index. Non-index relations fail closed instead of being dropped implicitly.
func cleanupInvalidConcurrentIndexHook(indexRegclass string) preMigrationHook {
	return func(ctx context.Context, pool *pgxpool.Pool) error {
		var schemaName, relationName string
		var isIndex, isValid bool
		err := pool.QueryRow(ctx, `
			SELECT n.nspname, c.relname, c.relkind = 'i', COALESCE(i.indisvalid, FALSE)
			FROM pg_class c
			JOIN pg_namespace n ON n.oid = c.relnamespace
			LEFT JOIN pg_index i ON i.indexrelid = c.oid
			WHERE c.oid = to_regclass($1)
		`, indexRegclass).Scan(&schemaName, &relationName, &isIndex, &isValid)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("inspect concurrent index %q: %w", indexRegclass, err)
		}
		if !isIndex {
			return fmt.Errorf("relation %q exists but is not an index", indexRegclass)
		}
		if isValid {
			return nil
		}

		qualifiedName := pgx.Identifier{schemaName, relationName}.Sanitize()
		if _, err := pool.Exec(ctx, "DROP INDEX CONCURRENTLY IF EXISTS "+qualifiedName); err != nil {
			return fmt.Errorf("drop invalid concurrent index %s: %w", qualifiedName, err)
		}
		slog.Warn("removed invalid index before migration retry", "index", qualifiedName)
		return nil
	}
}

type migrationVersionAlias struct {
	Legacy  string
	Current string
}

// migrationVersionAliases preserves databases that applied fork migrations
// before they moved into the reserved 9000+ namespace. Reconciliation keeps
// both stems: the new binary skips replaying the migration, while a binary
// rollback still sees the historical stem it understands.
var migrationVersionAliases = []migrationVersionAlias{
	{Legacy: "175_webhook_delivery_worker", Current: "176_webhook_delivery_worker"},
	{Legacy: "176_autopilot_run_webhook_delivery_index", Current: "177_autopilot_run_webhook_delivery_index"},
	{Legacy: "177_webhook_delivery_queue_index", Current: "178_webhook_delivery_queue_index"},
	{Legacy: "178_fc_e2b_sandbox_session", Current: "9000_fc_e2b_sandbox_session"},
	{Legacy: "179_dws_auth_profile", Current: "9001_dws_auth_profile"},
	{Legacy: "180_issue_origin_dingtalk_chat", Current: "9002_issue_origin_dingtalk_chat"},
	{Legacy: "181_dingtalk_install_session", Current: "9003_dingtalk_install_session"},
	{Legacy: "182_channel_typing_indicator", Current: "9004_channel_typing_indicator"},
	{Legacy: "183_github_agent_source", Current: "9005_github_agent_source"},
	{Legacy: "184_agent_task_runtime_launch_lease", Current: "9006_agent_task_runtime_launch_lease"},
	{Legacy: "185_agent_dispatch_endpoint", Current: "9007_agent_dispatch_endpoint"},
	{Legacy: "186_dingtalk_account_binding_status", Current: "9008_dingtalk_account_binding_status"},
	{Legacy: "187_agent_dingtalk_identity", Current: "9009_agent_dingtalk_identity"},
	{Legacy: "188_agent_dingtalk_identity_backend_ids", Current: "9010_agent_dingtalk_identity_backend_ids"},
	{Legacy: "189_managed_agent_source", Current: "9011_managed_agent_source"},
	{Legacy: "190_dingtalk_stream_inbox", Current: "9012_dingtalk_stream_inbox"},
	{Legacy: "191_agent_task_deferred_chat_unique", Current: "9013_agent_task_deferred_chat_unique"},
	{Legacy: "192_agent_task_deferred_chat_dispatch_index", Current: "9014_agent_task_deferred_chat_dispatch_index"},
	{Legacy: "193_agent_dingtalk_identity_organization_name", Current: "9015_agent_dingtalk_identity_organization_name"},
	{Legacy: "194_dingtalk_stream_receiver_hostname", Current: "9016_dingtalk_stream_receiver_hostname"},
	{Legacy: "195_chat_message_client_receipt", Current: "9017_chat_message_client_receipt"},
	{Legacy: "196_chat_message_source_payload", Current: "9018_chat_message_source_payload"},
	{Legacy: "197_fde_onboarding_workspace", Current: "9019_fde_onboarding_workspace"},
	{Legacy: "198_unified_dingtalk_router_registration", Current: "9020_unified_dingtalk_router_registration"},
	{Legacy: "199_dingtalk_install_transport_generation", Current: "9021_dingtalk_install_transport_generation"},
	{Legacy: "200_restore_legacy_dingtalk_stream_installations", Current: "9022_restore_legacy_dingtalk_stream_installations"},
	{Legacy: "201_dingtalk_processing_emotion_lifecycle", Current: "9023_dingtalk_processing_emotion_lifecycle"},
	{Legacy: "202_chat_session_pending_fresh", Current: "9024_chat_session_pending_fresh"},
	{Legacy: "203_task_completion_outbox", Current: "9025_task_completion_outbox"},
	{Legacy: "249_fc_e2b_stable_channel", Current: "9026_fc_e2b_stable_channel"},
	{Legacy: "250_fc_e2b_stable_release_evidence", Current: "9027_fc_e2b_stable_release_evidence"},
	{Legacy: "251_fc_e2b_stable_developer_rollout", Current: "9028_fc_e2b_stable_developer_rollout"},
	{Legacy: "252_unify_agent_source_github", Current: "9029_unify_agent_source_github"},
	{Legacy: "255_issue_delegated_task_completion", Current: "9030_issue_delegated_task_completion"},
	{Legacy: "256_task_execution_update_outbox", Current: "9031_task_execution_update_outbox"},
	{Legacy: "257_delegated_comment_completion_fanout", Current: "9032_delegated_comment_completion_fanout"},
	{Legacy: "258_asb_enterprise_runtime", Current: "9033_asb_enterprise_runtime"},
	{Legacy: "259_buc_identity_from_callback", Current: "9034_buc_identity_from_callback"},
	{Legacy: "260_asb_artifact_build_time", Current: "9035_asb_artifact_build_time"},
	{Legacy: "261_asb_identity_anchor_renewal", Current: "9036_asb_identity_anchor_renewal"},
	{Legacy: "262_platform_asb_credentials", Current: "9037_platform_asb_credentials"},
	{Legacy: "263_asb_paused_identity_source", Current: "9038_asb_paused_identity_source"},
	{Legacy: "264_asb_shared_identity_source", Current: "9039_asb_shared_identity_source"},
	{Legacy: "265_drop_legacy_fc_e2b_environment_scope_index", Current: "9040_drop_legacy_fc_e2b_environment_scope_index"},
	{Legacy: "266_runtime_start_attempt_observability", Current: "9041_runtime_start_attempt_observability"},
	{Legacy: "266_workspace_access_token", Current: "9042_workspace_access_token"},
	{Legacy: "267_workspace_access_native_ownership", Current: "9043_workspace_access_native_ownership"},
	{Legacy: "268_dta_load_smoke_operation_idempotency", Current: "9044_dta_load_smoke_operation_idempotency"},
	{Legacy: "269_workspace_access_service_member", Current: "9045_workspace_access_service_member"},
	{Legacy: "270_task_completion_execution_summary", Current: "9046_task_completion_execution_summary"},
	{Legacy: "253_asb_enterprise_runtime", Current: "9033_asb_enterprise_runtime"},
	{Legacy: "257_asb_enterprise_runtime", Current: "9033_asb_enterprise_runtime"},
	{Legacy: "254_buc_identity_from_callback", Current: "9034_buc_identity_from_callback"},
	{Legacy: "258_buc_identity_from_callback", Current: "9034_buc_identity_from_callback"},
	{Legacy: "259_asb_artifact_build_time", Current: "9035_asb_artifact_build_time"},
	{Legacy: "260_asb_identity_anchor_renewal", Current: "9036_asb_identity_anchor_renewal"},
	{Legacy: "261_platform_asb_credentials", Current: "9037_platform_asb_credentials"},
	{Legacy: "262_asb_paused_identity_source", Current: "9038_asb_paused_identity_source"},
	{Legacy: "263_asb_shared_identity_source", Current: "9039_asb_shared_identity_source"},
	{Legacy: "270_pinned_item_view", Current: "9062_pinned_item_view"},
	{Legacy: "271_channel_chat_pending_fresh", Current: "9063_channel_chat_pending_fresh"},
	{Legacy: "9062_agent_task_dsh_trajectory", Current: "9064_agent_task_dsh_trajectory"},
	{Legacy: "9063_fc_e2b_stable_release_target_five_providers", Current: "9065_fc_e2b_stable_release_target_five_providers"},
	{Legacy: "9079_fc_template_id_identity", Current: "9090_fc_template_id_identity_compat"},
	{Legacy: "9080_clear_fc_artifact_build_ids", Current: "9091_clear_fc_artifact_build_ids_deferred"},
}

func runTaskUsageHourlyHook(ctx context.Context, pool *pgxpool.Pool) error {
	res, err := taskusagebackfill.Hook(ctx, pool, taskusagebackfill.HookOptions{})
	if err != nil {
		return fmt.Errorf("task_usage_hourly pre-103 hook: %w", err)
	}
	if res.Skipped != "" {
		slog.Info("task_usage hourly rollup hook: skipped",
			"reason", res.Skipped,
			"watermark_stamped", res.WatermarkStamped)
		return nil
	}
	slog.Info("task_usage hourly rollup hook: backfill complete",
		"slices", res.SlicesProcessed,
		"rows_touched", res.RowsTouched,
		"from", res.From.Format("2006-01-02T15:04:05Z07:00"),
		"to", res.To.Format("2006-01-02T15:04:05Z07:00"))
	return nil
}

// runAttributionStrictHook backfills accountable_user_id from
// originator_user_id before migration 198 validates the strict attribution
// constraint, so self-hosted upgrades that never ran the out-of-band
// backfill recover automatically (GH #5544 / MUL-4897).
func runAttributionStrictHook(ctx context.Context, pool *pgxpool.Pool) error {
	res, err := attributionbackfill.Hook(ctx, pool, attributionbackfill.HookOptions{})
	if err != nil {
		return fmt.Errorf("attribution strict-constraint pre-198 hook: %w", err)
	}
	slog.Info("attribution backfill hook: complete",
		"rows_backfilled", res.RowsBackfilled,
		"batches", res.Batches,
		"mismatch_normalized", res.MismatchNormalized)
	return nil
}

// migrationAdvisoryLockKey is the int64 identifier used with Postgres
// pg_advisory_lock to serialize the migration loop across concurrent
// runners (multi-replica backend Deployment, scale-up, or a manual
// `migrate up` overlapping with pod startup). The exact value is
// arbitrary — it just needs to be stable across every process that runs
// migrations against the same database. See GitHub multica-ai/multica#3647.
const migrationAdvisoryLockKey int64 = 7244554146635925501

// defaultSchemaMigrationsTable is the unqualified name of the bookkeeping
// table that tracks which migrations have been applied. Tests override
// this so a concurrent-race harness can run against the same shared
// Postgres without colliding with the production table.
const defaultSchemaMigrationsTable = "schema_migrations"

// runOptions carries everything runMigrations needs that is not the
// pool itself. Tests use it to inject a hermetic migrations directory,
// a unique per-test bookkeeping table, and a unique advisory-lock key
// that doesn't collide with any other migration runner sharing the same
// Postgres instance.
type runOptions struct {
	// Direction is "up" or "down".
	Direction string
	// Files is the ordered list of .sql files to apply. Production callers
	// pass migrations.Files(direction); tests pass a curated set written
	// to a t.TempDir().
	Files []string
	// SchemaMigrationsTable is the bookkeeping table to read/write.
	// May be schema-qualified (e.g. "migrate_test_xyz.schema_migrations").
	// Empty means defaultSchemaMigrationsTable.
	SchemaMigrationsTable string
	// AdvisoryLockKey is the int64 used with pg_advisory_lock. Zero means
	// migrationAdvisoryLockKey. Tests pass a unique key per run so
	// concurrent test workers do not block on the production migration
	// runner if it happens to share the database.
	AdvisoryLockKey int64
	// Hooks maps migration version → pre-migration hook. The hook
	// receives the pool (not the loop's pinned conn) so it can take
	// its own session-level locks. nil or missing entries mean "no
	// hook" and the migration runs straight through. Production main()
	// passes preMigrationHooks; tests leave this nil.
	Hooks map[string]preMigrationHook
}

func main() {
	logger.Init()

	if len(os.Args) < 2 {
		fmt.Println("Usage: go run ./cmd/migrate <up|down>")
		os.Exit(1)
	}

	direction := os.Args[1]
	if direction != "up" && direction != "down" {
		fmt.Println("Usage: go run ./cmd/migrate <up|down>")
		os.Exit(1)
	}

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://multica:multica@localhost:5432/multica?sslmode=disable"
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		slog.Error("unable to connect to database", "error", err)
		os.Exit(1)
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		slog.Error("unable to ping database", "error", err)
		os.Exit(1)
	}

	files, err := migrations.Files(direction)
	if err != nil {
		slog.Error("failed to find migration files", "error", err)
		os.Exit(1)
	}

	if err := runMigrations(ctx, pool, runOptions{
		Direction: direction,
		Files:     files,
		Hooks:     preMigrationHooks,
	}); err != nil {
		slog.Error("migration run failed", "error", err)
		os.Exit(1)
	}

	fmt.Println("Done.")
}

// runMigrations applies (direction="up") or rolls back (direction="down")
// the given file list against the supplied pool, serialized through a
// Postgres session-level advisory lock so multiple concurrent runners
// (multi-replica startup, scale-up, manual migrate overlap) take turns
// instead of racing each other.
//
// It is safe to invoke concurrently from multiple goroutines or
// processes against the same database with the same options: every
// caller blocks on pg_advisory_lock, and once it is their turn the
// already-applied EXISTS check turns each finished migration into a
// no-op skip. See GitHub multica-ai/multica#3647 / MUL-2923.
func runMigrations(ctx context.Context, pool *pgxpool.Pool, opts runOptions) error {
	switch opts.Direction {
	case "up", "down":
		// ok
	default:
		return fmt.Errorf("invalid direction %q (want \"up\" or \"down\")", opts.Direction)
	}

	table := opts.SchemaMigrationsTable
	if table == "" {
		table = defaultSchemaMigrationsTable
	}
	tableIdent, err := quoteQualifiedIdentifier(table)
	if err != nil {
		return fmt.Errorf("invalid schema migrations table %q: %w", table, err)
	}
	lockKey := opts.AdvisoryLockKey
	if lockKey == 0 {
		lockKey = migrationAdvisoryLockKey
	}

	// pg_advisory_lock is scoped to a single session, so we must pin one
	// *pgxpool.Conn for the whole run — calling pool.Exec would attach the
	// lock to a random connection that pgxpool could hand back out before
	// the loop finishes, making the lock effectively a no-op. We use the
	// blocking pg_advisory_lock (not pg_try_*) so a late-arriving runner
	// queues behind the current one instead of crash-looping; once it
	// acquires the lock the EXISTS checks below turn finished migrations
	// into no-op skips.
	//
	// We deliberately do NOT wrap the loop in a single transaction: the
	// repo already ships migrations using CREATE INDEX CONCURRENTLY,
	// which Postgres rejects inside a transaction block.
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire migration connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", lockKey); err != nil {
		return fmt.Errorf("acquire migration advisory lock: %w", err)
	}
	// Migrations are the only writes allowed while the deployment fence is
	// frozen. The bypass is scoped to this pinned migration-runner session and
	// is never set by the application server.
	if _, err := conn.Exec(ctx, "SELECT set_config('multica.deployment_fence_bypass', 'migration-runner', false)"); err != nil {
		return fmt.Errorf("enable deployment fence migration bypass: %w", err)
	}
	// Best-effort explicit unlock on the success path. On error returns
	// the defer still runs; on os.Exit error paths in main() it does not,
	// but session-level advisory locks are released automatically when
	// the connection closes at process exit, so the next runner is never
	// permanently blocked.
	defer func() {
		if _, err := conn.Exec(ctx, "SELECT pg_advisory_unlock($1)", lockKey); err != nil {
			slog.Warn("failed to release migration advisory lock", "error", err)
		}
	}()

	// Create migrations tracking table.
	if _, err := conn.Exec(ctx, fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			version TEXT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)
	`, tableIdent)); err != nil {
		return fmt.Errorf("create migrations table: %w", err)
	}
	if opts.Direction == "up" {
		if err := reconcileMigrationVersionAliases(ctx, conn, tableIdent, opts.Files); err != nil {
			return err
		}
	}

	existsSQL := fmt.Sprintf("SELECT EXISTS(SELECT 1 FROM %s WHERE version = $1)", tableIdent)
	insertSQL := fmt.Sprintf("INSERT INTO %s (version) VALUES ($1)", tableIdent)
	deleteSQL := fmt.Sprintf("DELETE FROM %s WHERE version = $1", tableIdent)

	for _, file := range opts.Files {
		version := migrations.ExtractVersion(file)

		var exists bool
		if err := conn.QueryRow(ctx, existsSQL, version).Scan(&exists); err != nil {
			return fmt.Errorf("check migration %q: %w", version, err)
		}

		if opts.Direction == "up" {
			if exists {
				fmt.Printf("  skip  %s (already applied)\n", version)
				continue
			}
		} else {
			if !exists {
				fmt.Printf("  skip  %s (not applied)\n", version)
				continue
			}
		}

		sql, err := os.ReadFile(file)
		if err != nil {
			return fmt.Errorf("read migration %q: %w", file, err)
		}

		// Run any pre-migration hook before the SQL file. Hooks
		// receive the *pgxpool.Pool (not the loop's pinned conn), so
		// they can acquire other session-level locks without
		// colliding with migrationAdvisoryLockKey. Hook failures
		// abort the run before schema_migrations is updated, so the
		// same version retries cleanly on the next invocation.
		if opts.Direction == "up" {
			if hook, ok := opts.Hooks[version]; ok && hook != nil {
				slog.Info("running pre-migration hook", "version", version)
				if err := hook(ctx, pool); err != nil {
					return fmt.Errorf("pre-migration hook for %q: %w", version, err)
				}
			}
		}

		if _, err := conn.Exec(ctx, string(sql)); err != nil {
			return fmt.Errorf("apply migration %q: %w", file, err)
		}

		if opts.Direction == "up" {
			_, err = conn.Exec(ctx, insertSQL, version)
		} else {
			_, err = conn.Exec(ctx, deleteSQL, version)
		}
		if err != nil {
			return fmt.Errorf("record migration %q: %w", version, err)
		}

		fmt.Printf("  %s  %s\n", opts.Direction, version)
	}

	// Upstream syncs can add tables with migration numbers below the fork's
	// 9000+ range, so migration 9047 may already be recorded before those new
	// files arrive. Reinstall the idempotent triggers after every production
	// up-run to cover every table that now exists before the server starts.
	if opts.Direction == "up" && table == defaultSchemaMigrationsTable {
		if err := refreshDeploymentFenceTriggers(ctx, conn); err != nil {
			return err
		}
	}

	return nil
}

func refreshDeploymentFenceTriggers(ctx context.Context, conn *pgxpool.Conn) error {
	var available bool
	if err := conn.QueryRow(ctx, `
		SELECT to_regprocedure('multica_install_deployment_fence_triggers()') IS NOT NULL
	`).Scan(&available); err != nil {
		return fmt.Errorf("detect deployment fence trigger installer: %w", err)
	}
	if !available {
		return nil
	}
	if _, err := conn.Exec(ctx, "SELECT multica_install_deployment_fence_triggers()"); err != nil {
		return fmt.Errorf("refresh deployment fence triggers: %w", err)
	}
	return nil
}

func reconcileMigrationVersionAliases(
	ctx context.Context,
	conn *pgxpool.Conn,
	tableIdent string,
	files []string,
) error {
	available := make(map[string]struct{}, len(files))
	for _, file := range files {
		available[migrations.ExtractVersion(file)] = struct{}{}
	}

	statement := fmt.Sprintf(`
		INSERT INTO %s (version, applied_at)
		SELECT $2, applied_at
		FROM %s
		WHERE version = $1
		ON CONFLICT (version) DO NOTHING
	`, tableIdent, tableIdent)
	for _, alias := range migrationVersionAliases {
		if _, ok := available[alias.Current]; !ok {
			continue
		}
		if _, err := conn.Exec(ctx, statement, alias.Legacy, alias.Current); err != nil {
			return fmt.Errorf(
				"reconcile migration version %q to %q: %w",
				alias.Legacy,
				alias.Current,
				err,
			)
		}
	}
	return nil
}

// quoteQualifiedIdentifier safely quotes either an unqualified table
// name ("foo") or a schema-qualified name ("schema.foo") for embedding
// into a SQL statement. Postgres does not let parametrized queries
// supply identifiers, so we have to interpolate, but pgx.Identifier
// does the right escaping (double-quotes, embedded-quote handling).
//
// The accepted shape is exactly one or two dot-separated components.
// Names containing more than one dot are rejected outright rather than
// silently sanitized into a "schema"."b.c" reference, which is valid
// SQL but almost certainly not what the caller meant.
func quoteQualifiedIdentifier(name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("empty identifier")
	}
	parts := strings.Split(name, ".")
	if len(parts) > 2 {
		return "", fmt.Errorf("identifier %q has more than one dot; only schema.table is supported", name)
	}
	for _, p := range parts {
		if p == "" {
			return "", fmt.Errorf("empty component in %q", name)
		}
	}
	return pgx.Identifier(parts).Sanitize(), nil
}
