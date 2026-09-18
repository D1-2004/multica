
## DSH startup and stable release observations

- `server/internal/handler/dsh_profile.go`: excludes disabled bindings from executable Profile source while preserving saved configuration.
- `server/internal/service/fc_e2b_dsh_plugin_sync.go`: supplies the acknowledged snapshot revision and emits a single failure receipt.
- `server/internal/service/fc_e2b.go`: keeps successful command stdout separate from stderr diagnostics.
- `server/internal/service/fc_e2b_stable.go`: startup failure thresholds warn without blocking release progression; target and state checks remain enforced.

### Native session consistency

- `server/internal/handler/dsh_native_routing.go`: scoped authorization and persisted session owner lookup for native RPCs.
- `server/internal/handler/dsh_native_mux.go`: independently cancellable owner follow streams; parent grant expiry/revocation.
- `server/internal/handler/dsh_native_list.go`: official read-only snapshot refresh and platform title overlays.
- `server/internal/handler/dsh_native_routing_test.go`: distinct-owner routing, cancellation, revocation and projection regression tests.

- `server/migrations/9265_dsh_native_access_parent.up.sql` and `server/internal/dshhost/native_access_postgres.go`: nullable parent linkage; existing browser grants retain their semantics and routed capabilities inherit revocation/expiry.
- `server/internal/service/fc_e2b_dsh_host.go` and `fc_e2b_dsh_plugin_sync.go`: routed reads are excluded from browser reservations and native plugin-source preference.

## Managed restart and conversation reset

- `server/internal/integrations/dingtalk/inbound.go`: preserves canonical `/new` and `/reset` command text for Router's durable pending-fresh handoff.
- `server/internal/dshhost/session_epoch.go`, `session.go`: immutable task bindings, per-reset native session epochs, and explicit historical native scope resolution.
- `server/internal/service/fc_e2b.go`: resolves the task epoch before binding native execution and its sandbox scope.
- `server/internal/service/dsh_native_chat.go`, `dsh_native_session.go`, `dsh_schedule_dispatch.go`, `server/internal/dshschedule/execution.go`: retain the exact native epoch for browser admission and schedules.
- `server/internal/handler/chat_context_reset.go`, `server/pkg/db/queries/chat_context_reset.sql`: retain visible transcripts while excluding pre-reset task history and messages owned by another DSH session from model context.
- `server/migrations/9268_dsh_session_epoch.up.sql`, `9269_dsh_session_epoch_identity.up.sql`, `9270_dsh_session_epoch_scope.up.sql`: expansion followed by activation after every replica supports epochs.
- `server/internal/handler/dsh_plugin.go`, `server/internal/service/fc_e2b_dsh_profile.go`: readable persisted configuration with explicit pending-native-sync status during recovery.
- `docs/plans/2026-09-18-dsh-managed-restart.md`: runtime supervisor commit, preproduction release IDs, real restart/role/reset evidence, and browser insertion acceptance.
