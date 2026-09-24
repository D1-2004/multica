
## DSH startup and stable release observations

- `server/internal/handler/dsh_profile.go`: excludes disabled bindings from executable Profile source while preserving saved configuration.
- `server/internal/service/fc_e2b.go`: keeps successful command stdout separate from stderr diagnostics.
- `server/internal/service/fc_e2b_stable.go`: startup failure thresholds warn without blocking release progression; target and state checks remain enforced.

## Conversation reset

- `server/internal/integrations/dingtalk/inbound.go`: preserves canonical `/new` and `/reset` command text for Router's durable pending-fresh handoff.
- `server/internal/dshhost/session_epoch.go`, `session.go`: immutable task bindings, per-reset native session epochs, and explicit historical native scope resolution.
- `server/internal/service/fc_e2b.go`: resolves the task epoch before binding native execution and its sandbox scope.
- `server/internal/service/dsh_schedule_dispatch.go`, `server/internal/dshschedule/execution.go`: retain the exact native epoch for task-backed schedules; browser-to-task admission has been removed.
- `server/internal/handler/chat_context_reset.go`, `server/pkg/db/queries/chat_context_reset.sql`: retain visible transcripts while excluding pre-reset task history and messages owned by another DSH session from model context.
- `server/migrations/9268_dsh_session_epoch.up.sql`, `9269_dsh_session_epoch_identity.up.sql`, `9270_dsh_session_epoch_scope.up.sql`: expansion followed by activation after every replica supports epochs.
- `server/internal/handler/dsh_plugin.go`, `server/internal/service/fc_e2b_dsh_profile.go`: persisted workbench configuration; ordinary task startup applies prepared revisions.
- `docs/plans/2026-09-18-dsh-managed-restart.md`: runtime supervisor commit, preproduction release IDs, real restart/role/reset evidence, and historical browser insertion acceptance (browser functionality has since been removed).
