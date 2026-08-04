# Feature Flags

Multica ships a framework-level feature flag implementation:

- **Backend**: `server/pkg/featureflag` — Go package with environment, Diamond surface-prompt, YAML, and chain providers.
- **Frontend**: `@multica/core/feature-flags` — TypeScript module with React hooks.

Both sides share the same vocabulary (`Decision`, `EvalContext`, `Rule`, `PercentRollout`) and the same FNV-1a percent bucketing, so a flag evaluated on the server and on the client lands in the same bucket for the same user.

The package is designed so new features can adopt feature flags without writing any infrastructure code — drop a rule into the static config, call `Service.IsEnabled` / `useFlag`, done.

---

## Core concepts

```
[Toggle Point] --query--> [Service / Router] --read--> [Provider / Configuration]
   business code                                          env / Diamond / YAML / chain
```

- A **Toggle Point** is the single `if` in business code. It always calls the Service, never the provider directly.
- The **Service** (`Service` in Go, `FeatureFlagService` in TS) is the router. Business code never depends on which provider is behind it.
- A **Provider** is the configuration backend. The Go server ships `StaticProvider` (in-memory/YAML rules), `EnvProvider`, `DiamondProvider` (dynamic immutable surface-prompt snapshots), and `ChainProvider`. The frontend uses its own static provider.
- A **Decision** is the structured result: `{ enabled, variant, reason, source }`. `IsEnabled` is the boolean projection, `Variant` is the raw string. Use `Decision` for diagnostic endpoints.

Four flag categories (Martin Fowler):

| Category | Lifetime | Owner | Example |
|---|---|---|---|
| **Release** | Days–weeks | Engineering | Hide a half-finished page behind `flags_release_v2` |
| **Experiment** | Hours–weeks | Product / Data | A/B test `checkout_algo` between `control` and `experiment-v2` |
| **Ops** | Short or evergreen | SRE | Kill switch `billing_disable_invoice_pdf` |
| **Permission** | Years | Product | `plan_gate_enterprise_dashboard` |

Manage them in the same provider but treat them differently: Release flags get deleted; Ops flags need fast override paths (`FF_<KEY>` env var); Permission flags use `Allow` lists; Experiment flags use `PercentRollout`.

---

## Backend (Go)

### Wiring at startup

The server constructs a `featureflag.Service` once in `cmd/server/main.go` via the standard helper:

```go
flags, err := featureflag.NewServiceFromEnv(featureflag.WithLogger(slog.Default()))
if err != nil {
    slog.Error("feature flag configuration failed to load", "error", err)
    os.Exit(1)
}
```

`NewServiceFromEnv` composes the following configuration sources:

| Env var | Role |
|---|---|
| `MULTICA_FEATURE_FLAGS_FILE` | Path to the YAML rule set (optional; absent = no static rules). |
| `MULTICA_DIAMOND_DATA_ID` | Diamond Data ID. Defaults to `dt-fde-multica.json`. |
| `MULTICA_DIAMOND_GROUP` | Diamond Group. Defaults to `DEFAULT_GROUP`. |
| `FF_<FLAG_KEY>` | Per-flag runtime override. `FF_BILLING_NEW_INVOICE_EMAIL=false` / `25%` / `experiment-v2`. Beats the YAML, no redeploy. |

The provider chain is `EnvProvider → DiamondProvider → YAML StaticProvider → caller default`. Earlier providers win. Diamond has no enable switch and is started for every server process; if it is unavailable, fail-open behavior preserves the remaining provider chain.

At startup Diamond performs `GetConfig` and then registers `ListenConfig`. Its JSON contract is the fixed `common/issue/chat/auto.prompt` dispatch-prompt document described in [Alibaba Diamond configuration](diamond.md), not the generic YAML rule schema. Each complete document is validated before an atomic snapshot replacement. `{}` is valid. Invalid JSON leaves the last valid Diamond snapshot untouched. Diamond client, fetch, and listener failures are fail-open and do not prevent the server from using environment overrides, YAML, or caller defaults.

### YAML schema

```yaml
# /etc/multica/feature-flags.yaml
billing_new_invoice_email:
  default: true

checkout_algo:
  default: false
  variant: experiment-v2
  percent:
    percent: 25
    by: user_id

ops_disable_recommendations:
  default: false
  allow: ["user-internal-1", "user-internal-2"]
  allow_by: user_id
```

Every field except `default` is optional. `variant` is the on-variant — see the multi-arm note below. An empty file is a valid "no flags yet" state. Malformed YAML fails startup the same way `DATABASE_URL` parse errors do, so misconfig surfaces loudly.

### Attaching evaluation context to the request

```go
func middleware(flags *featureflag.Service, next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        ec := featureflag.EvalContext{
            UserID:      currentUserID(r),
            WorkspaceID: currentWorkspaceID(r),
            Attributes:  map[string]string{"plan": currentPlan(r)},
        }
        ctx := featureflag.WithEvalContext(r.Context(), ec)
        next.ServeHTTP(w, r.WithContext(ctx))
    })
}
```

### Toggle point in business code

```go
if flags.IsEnabled(ctx, "billing_new_invoice_email", false) {
    return s.sendNewInvoiceEmail(ctx, invoice)
}
return s.sendLegacyInvoiceEmail(ctx, invoice)
```

For multi-arm flags:

```go
switch flags.Variant(ctx, "checkout_algo", "control") {
case "experiment-v2":
    return checkoutV2(ctx, order)
case "experiment-v3":
    return checkoutV3(ctx, order)
default:
    return checkoutControl(ctx, order)
}
```

`Rule.Variant` is the **on-variant**: it is only returned when the rule evaluates to enabled=true (allow hit, percent hit, default-on). When the rule evaluates to disabled (deny hit, percent miss, default-off) the Service returns `"off"` so callers branching on `Variant()` cannot route control users into the experiment arm. This is exercised by `TestStaticProviderVariantOnlyWhenEnabled` and is the same on the TS side.

The Service is nil-safe and missing-key-safe: `(*Service)(nil).IsEnabled(ctx, "any", true)` returns `true`. Business code never needs to guard against a missing flag.

---

## Frontend (TypeScript / React)

### Mounting once at the root

```tsx
// apps/web/app/_providers.tsx (or the equivalent root)
import {
  FeatureFlagsProvider,
  FeatureFlagService,
  StaticProvider,
} from "@multica/core/feature-flags";

const service = new FeatureFlagService(
  new StaticProvider({
    billing_v2_dashboard: { default: false, allow: ["user-internal"] },
    checkout_algo: { default: true, variant: "experiment-v2",
                     percent: { percent: 25 } },
  }),
);

export function Providers({ children }: { children: ReactNode }) {
  const userId = useCurrentUserId();
  return (
    <FeatureFlagsProvider service={service} context={{ userId }}>
      {children}
    </FeatureFlagsProvider>
  );
}
```

When the backend pushes a fresh rule set (via an API response or WebSocket), call `service.setProvider(new StaticProvider(remoteRules))` and the whole tree re-evaluates.

### Toggle point in a component

```tsx
import { useFlag, useVariant } from "@multica/core/feature-flags";

function BillingPage() {
  const showV2 = useFlag("billing_v2_dashboard", false);
  return showV2 ? <BillingV2 /> : <BillingV1 />;
}

function Checkout() {
  const variant = useVariant("checkout_algo", "control");
  switch (variant) {
    case "experiment-v2": return <CheckoutV2 />;
    case "experiment-v3": return <CheckoutV3 />;
    default:              return <CheckoutControl />;
  }
}
```

Outside a `FeatureFlagsProvider` (Storybook, unit tests, error pages) `useFlag` / `useVariant` return the supplied default. You never have to mount the provider just to render a component in isolation.

### v0.3.44 compatibility rollout

The following release flags default to `false` so the schema can ship before
the new persisted states are visible to older server pods or a rollback:

```yaml
# Enable only after every v0.3.43 server pod has drained and rollback reads
# have been validated against the migrated database.
agents_agent_builder:
  default: true
settings_resource_labels:
  default: true
```

Keep both off for v0.3.44: it is a schema-only deployment for these
features. A later rollout may enable them only after it ships and verifies a
rollback normalizer for builder agents and resource-label rows. Do not rely on
turning the flags off to make a database safe for an older binary; it prevents
new writes but cannot remove states that already exist. Until that normalizer
exists, rollbacks must target a version that understands these states or happen
before either flag is enabled.

Agent skill toggles have completed this rollout and are now always available.
Current clients render the switch without a release flag, and the backend no
longer gates the write endpoint. `/api/config` still reports
`agents_skill_toggles: true` so installed v0.4.0 desktop clients also expose the
switch; this is a client-compatibility decision, not an operator-controlled
flag.

### Security note: never rely on the frontend alone

A frontend feature flag controls what the user *sees*. It does NOT enforce access. Any API route exposing the same capability MUST evaluate the matching backend flag independently. The two flags can share a key but they live in two `Service` instances and the backend value is the source of truth.

---

## Best-practice checklist

Adopted from Martin Fowler, ConfigCat and Octopus.

- **Naming**: `{team}_{area}_{behavior}`, e.g. `billing_checkout_new_payment_flow`. No `enable_` / `disable_` prefixes (redundant).
- **One flag, one purpose**: never repurpose an old flag for a new feature. Add a new flag and delete the old one.
- **Plan the death of the flag at birth**: open a follow-up issue to remove the flag when the rollout completes. Release flags should live days, not quarters.
- **Convention**: `Off` is the legacy / safe state, `On` is the new behavior. Lets CI test "all-off (today)" and "all-on (tomorrow)".
- **Kill switch fast path**: ops-critical flags should be exposed via `EnvProvider` so SREs can flip them without a deploy.
- **Backend protection**: anything controlling access goes through the backend Service; the frontend flag is presentation only.
- **No secrets in flags**: variant values are not Secrets Manager / KMS. Database URLs, JWT secrets, service credentials, signing keys, tokens, and passwords must not be stored in YAML or Diamond.

See `docs/design.md` and `docs/timezone-architecture-rfc.md` for prior examples of how this pattern is used across the codebase.

## Change history

| Date | Change | Reason |
|---|---|---|
| 2026-08-04 | Added Diamond `common.prompt` and documented claim-time `common + mode + Router contextPrompt` instruction composition. | Separate fixed policy configuration from dynamic delivery context and keep private instructions out of user-visible content. |
| 2026-08-04 | Made the Diamond provider always active and removed its deployment enable switch. | Ensure dynamic surface prompts are not accidentally bypassed when only the fixed Diamond coordinates are configured. |
| 2026-08-01 | Defined Diamond as the dynamic source for `issue/chat/auto.prompt` and removed binary-embedded surface prompt fallback. | Keep changeable Agent behavior policy in configuration while retaining security and outbound protocol constraints in code. |
| 2026-08-01 | Added Diamond to the backend provider chain and documented the dynamic snapshot lifecycle, precedence, and security boundary. | Support runtime configuration updates across all replicas while retaining the environment, YAML, and caller-default fallback chain. |
