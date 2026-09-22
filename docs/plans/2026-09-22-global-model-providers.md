# Global model providers

Scope: environment-wide developer settings, restricted to the existing Diamond stable-publisher allow-list. Preproduction and production remain independent.

## Product

Settings > My account > Developer options contains Model services and Runtime releases. Runtime stable-channel overview and release controls move here; old overview URLs remain usable. Diamond is a read-only built-in `mass` provider. Additional providers have an immutable ID, HTTPS API root, write-only encrypted API key and a manually editable/discovered model catalog. Discovery never automatically publishes a model. Agent choices and default are explicit. Coordinator primary/fallback choices can cross providers. A model probe validates a synthetic tool call without executing tools.

## Persistence and routing

`global_model_configuration` is an append-only versioned PostgreSQL configuration log. AES-GCM uses the deployment-only `MULTICA_MODEL_PROVIDER_SECRET_KEY`. Save uses optimistic revision checks and an advisory transaction lock. Restore creates a new revision. Referenced Agent models cannot be removed without migrating those Agents. Changing an endpoint requires a replacement key. Credentials never appear in configuration reads or browser telemetry.

`MULTICA_MODEL_GATEWAY_ENABLED=true` routes newly launched FC/ASB executions through an active-task/runtime-authenticated gateway. The gateway resolves qualified model references and forwards only the upstream model ID. Runtime daemon credentials replace provider credentials in sandbox bootstrap. Existing processes are not restarted automatically. Model IDs containing slashes remain opaque. Legacy bare IDs resolve only to the Diamond provider. Local user-managed runtimes retain their own model discovery and credentials.

Every Coordinator decision resolves one immutable provider snapshot. All model stages share the candidate chain, including reply generation and review. Each candidate is attempted once per failed call under the existing total deadline and a bounded per-attempt deadline. Successful fallback becomes preferred for the rest of that decision. Business rejection and ordinary invalid-request errors do not trigger provider fallback. Required-to-auto parameter rewriting is removed. No previously executed tool or committed plan is replayed. Actual attempts own token usage; aggregate observations do not double count it.

## Verification

- Local: registry routing/URL/model-reference checks, bounded cross-provider retry and cancellation, Coordinator tests including race checks, handler access checks, server build, FC/ASB launcher regressions, frontend typechecks, schema tests and developer/settings component tests.
- The repository's migration-prefix lint already reports duplicate legacy/develop prefixes unrelated to the new unique 9300/9301 migrations. No existing migration is renamed in this change.
- Preproduction: pending deployment, configuration save/readback, provider probe, actual fallback trace, Agent execution and UI inspection. Pipeline success alone is not acceptance.
