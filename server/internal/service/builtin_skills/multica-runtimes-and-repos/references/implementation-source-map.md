
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
