
## DSH startup and stable release observations

- `server/internal/handler/dsh_profile.go`: excludes disabled bindings from executable Profile source while preserving saved configuration.
- `server/internal/service/fc_e2b_dsh_plugin_sync.go`: supplies the acknowledged snapshot revision and emits a single failure receipt.
- `server/internal/service/fc_e2b.go`: keeps successful command stdout separate from stderr diagnostics.
- `server/internal/service/fc_e2b_stable.go`: startup failure thresholds warn without blocking release progression; target and state checks remain enforced.
