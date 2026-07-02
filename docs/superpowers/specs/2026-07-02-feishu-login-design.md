# Feishu (Lark) Login — Design

Date: 2026-07-02
Status: approved (user confirmed in session)
Related: `docs/dingtalk-private-agent-integration-plan.md`, commits `b1229511b` (DingTalk OAuth login), `075caff52` (login identity via private agent)

## Goal

Add "Sign in with Feishu" to Multica, mirroring the existing DingTalk login end to end: login button → browser OAuth redirect → `/auth/callback` → backend code exchange → existing email-identity account system. Login only; Feishu org member search / group invite are out of scope for this round.

## Feasibility verdict on lark-cli

`lark-cli` (`@larksuite/cli`) cannot be the production login path: its `auth login` is a Device Flow bound to the local keychain and a single identity. It is, however, the development harness for this work:

- It proved the Feishu app `cli_aab264b001f8dbcf` (brand `feishu`) has valid credentials (bot identity ready) and 181 user scopes — more than enough for `user_info`.
- `lark-cli api` / raw curl probing confirmed the endpoint contracts below.
- The app secret lives in the macOS keychain (`appsecret:cli_aab264b001f8dbcf`); local runs read it from there rather than committing it anywhere.

The production flow is two plain HTTP calls, implemented directly (no new SDK dependency), matching how the DingTalk adapter is hand-rolled.

## Verified endpoint contracts (brand = feishu)

- Authorize (browser): `https://accounts.feishu.cn/open-apis/authen/v1/authorize?client_id={app_id}&redirect_uri={uri}&response_type=code&state={state}`
- Token exchange: `POST https://open.feishu.cn/open-apis/authen/v2/oauth/token` with JSON `{grant_type:"authorization_code", client_id, client_secret, code, redirect_uri}`.
  - Error body is RFC 6749 style: `{"error":"...","error_description":"...","code":20063}` — NOT the Feishu `{code,msg}` envelope. Parse `access_token` presence as success; surface `error_description` on failure.
- User info: `GET https://open.feishu.cn/open-apis/authen/v1/user_info` with `Authorization: Bearer {user_access_token}` → standard envelope `{"code":0,"msg":"success","data":{union_id, open_id, user_id?, name, en_name?, avatar_url, email?, enterprise_email?, mobile?, tenant_key}}`.

Two deltas vs DingTalk that shape the design:

1. Feishu returns the grant as `code` (same query param as Google; DingTalk uses `authCode`). Provider detection therefore relies on the existing `provider:*` marker in OAuth `state` — already how the callback page works.
2. Feishu's token exchange requires the same `redirect_uri` as the authorize step (DingTalk does not; Google does). So `POST /auth/lark` and the agent route both carry `{code, redirect_uri}`, and the frontend API signature mirrors `googleLogin`, not `dingtalkLogin`.

## Architecture (user-selected): agent-preferred + direct fallback, mirroring DingTalk

### Go backend (this repo)

- `server/internal/integrations/lark/` (existing bot-integration package) gains:
  - `oauth.go` — shared `OAuthUser` type (`UnionID`, `OpenID`, `Name`, `AvatarURL`, `Email`) and the direct client: token exchange + user_info using `LARK_CLIENT_ID`/`LARK_CLIENT_SECRET`, base URL override via `LARK_OPENAPI_BASE` (default `https://open.feishu.cn`).
  - `oauth_agent_client.go` — private-agent proxy mirroring `dingtalk/agent_client.go`: `POST {LARK_AGENT_BASE_URL}/internal/lark/oauth/user` with `x-internal-secret: LARK_AGENT_INTERNAL_SECRET`, body `{code, redirect_uri}`.
  - Both satisfy a `ResolveOAuthUser(ctx, code, redirectURI) (OAuthUser, error)` interface consumed by the handler as `h.LarkOAuth`.
- `server/internal/handler/auth.go` — `LarkLogin` mirrors `DingTalkLogin`:
  - Reject when `h.LarkOAuth` is nil/unconfigured (503).
  - Resolve user; missing `union_id` → 502.
  - Identity = synthetic email `<union_id>@feishu.cn` (mirror of `<unionId>@dingtalk.com`; real email is NOT used even when present, to keep identity stable).
  - Backfill display name/avatar on first login; analytics `auth_method="lark"`.
- `server/internal/handler/config.go` — `/api/config` adds `lark_client_id` (from `LARK_CLIENT_ID`, non-secret; must be set on the backend even in agent mode so the login page can build the authorize URL — same as `DINGTALK_CLIENT_ID`).
- `server/cmd/server/router.go`:
  - Wiring precedence: `LARK_AGENT_BASE_URL`+`LARK_AGENT_INTERNAL_SECRET` → agent client; else `LARK_CLIENT_ID`+`LARK_CLIENT_SECRET` → direct client; else disabled (log each, mirroring DingTalk's tiered block).
  - Register `POST /auth/lark` with `authRL`, inside the same "not LOGIN_DINGTALK_ONLY" block as email/Google routes: the DingTalk-only lock keeps meaning "DingTalk is the sole way in". No new LARK-only lock.

### dingtalk-native-agent (`/Users/yuanzhan/d1/dingtalk-native-agent`, separate repo)

- `src/adapters/lark.ts` — `resolveOAuthUser(code, redirectUri)`: the two HTTP calls above; returns `{union_id, open_id, name, avatar_url, email?}`; distinguishes RFC 6749 error bodies from envelope errors in logs.
- `src/routes/internal.ts` — `POST /internal/lark/oauth/user`, guarded by the existing `requireInternalApi` (same internal secret; Multica's `LARK_AGENT_INTERNAL_SECRET` is deployed with the same value).
- `src/config.ts` — `LARK_APP_ID`, `LARK_APP_SECRET`, `LARK_OPENAPI_BASE` (default `https://open.feishu.cn`). Lark section is optional: when unset, the route answers 503 and the rest of the agent is unaffected.
- `src/routes/health.ts` — advertise the new route.

### Frontend (web + desktop shared)

- `packages/core`:
  - `api/client.ts` — `larkLogin(code: string, redirectUri: string)` → `POST /auth/lark`.
  - `api/schemas.ts` — `lark_client_id` optional in the config schema (`parseWithFallback`).
  - `auth/store.ts` — `loginWithLark(code, redirectUri)`.
- `packages/views/auth/login-page.tsx` — `lark?: LarkAuthConfig {clientId, redirectUri, state}` + `onLarkLogin` override; button builds the accounts.feishu.cn authorize URL with `provider:lark` stamped into state; hidden in `dingtalkOnlyMode`.
- `apps/web/app/auth/callback/page.tsx` — `stateParts.includes("provider:lark")` branch → `api.larkLogin(code, redirectUri)` across the CLI, desktop, and web sub-flows, mirroring the DingTalk branches.
- Desktop login page mirrors its DingTalk wiring.
- i18n copy per `apps/docs/content/docs/developers/conventions.zh.mdx` glossary (飞书 for zh; Feishu/Lark for en).

## Error handling

- Agent unreachable / 5xx → handler logs and returns 502 "failed to resolve Feishu user".
- Token endpoint RFC 6749 error → agent/direct client wraps `error_description` into the error; handler → 502.
- Missing `union_id` → 502 (mirror DingTalk's guard).
- Config missing → 503 "Feishu login is not configured"; button simply absent when `lark_client_id` missing from `/api/config`.

## Testing

- Go: `LarkLogin` handler tests with a mocked OAuth client (success / missing union_id / upstream failure); direct + agent client tests with `httptest` covering the RFC 6749 error body and the envelope user_info body.
- `packages/core`: config-schema malformed-response test for `lark_client_id`; login-response parsing unchanged (reuses existing schema).
- `packages/views`: login page renders the Feishu button from config and hides it under `dingtalkOnly`.
- native-agent repo: `pnpm typecheck` (no test infra there) + curl smoke against the new internal route.
- Real `code` can only be exercised via the browser OAuth flow (same limitation recorded for DingTalk).

## Deployment / console prerequisites (operator actions)

- Feishu open-platform console, app `cli_aab264b001f8dbcf`: add redirect URLs `http://localhost:3000/auth/callback` (dev) and the production web origin's `/auth/callback`. A dedicated production app can replace it later — only env values change.
- Railway `dingtalk-native-agent` service: `LARK_APP_ID`, `LARK_APP_SECRET`.
- Railway `multica-backend`: `LARK_AGENT_BASE_URL` (private domain), `LARK_AGENT_INTERNAL_SECRET` (same value as the agent's internal secret), `LARK_CLIENT_ID`.
- Local dev: secret retrieved from the lark-cli keychain entry on demand; never committed.

## Out of scope

- Feishu org member search / group-member invite (DingTalk parity feature — separate round).
- LOGIN_LARK_ONLY lock.
- Lark international brand (accounts.larksuite.com) — base URLs are env-overridable, but only feishu-brand is wired and verified now.

## Result (2026-07-02)

Implemented across both repos, every task spec-reviewed and quality-reviewed:

- multica (develop): `09210cbe2` + `609c9ad71` + `fde8e0505` (lark OAuth clients, 8 httptest tests), `d4e86ee0e` (`/auth/lark` handler + config + router tiering, 5 handler tests), `627e77e33` (core plumbing, 3 schema drift tests), `0f862fc3a` (login button + 4-locale i18n, 2 component tests), `14ee0b0ba` (web/desktop wiring).
- dingtalk-native-agent: `f80c3539` (`POST /internal/lark/oauth/user`, LarkClient adapter, config/container/health).

Verified:

- Go: build/vet/gofmt clean; 8 lark OAuth client tests pass. TS: core 707, views 1540 (incl. 196 login-page+parity), desktop 249 tests pass; web/desktop/core/views typecheck clean.
- End-to-end smoke against the REAL Feishu API: local agent (`LARK_APP_ID=cli_aab264b001f8dbcf`) → `POST /internal/lark/oauth/user` with a fake code → agent log `Feishu token exchange failed (HTTP 400): The authorization code is not found...` (real round-trip + RFC 6749 error_description parsing confirmed) → route answers 502. A real `lark.OAuthAgentClient` (Go) against the live agent surfaced `lark oauth agent: HTTP 502: {"ok":false,...}` — cross-service contract verified. Feishu validates the code before the client secret, so this smoke is deployment-equivalent for the fake-code path.

Known gaps / environment notes:

- The 5 `TestLarkLogin*` handler tests compile but SKIP locally: no Postgres available on this machine (Docker Hub and mirrors unreachable from this network). CI runs them (pgvector service).
- Go-server-mode smoke (`/api/config` + `/auth/lark` on a running backend) blocked by the same missing local Postgres.
- `apps/web/app/(auth)/login/page.test.tsx` has 6 PRE-EXISTING failures (missing `useConfigStore` mock vs the `authConfigLoaded` gate from `360922277`) — reproduced identically without the Feishu diff; follow-up fix recommended, unrelated to this feature.
- A real authorization code can only be exercised via the browser flow (same limitation as DingTalk).

Remaining operator steps:

1. Feishu console (app `cli_aab264b001f8dbcf` or a dedicated prod app): whitelist redirect URLs `http://localhost:3000/auth/callback` (dev) and `https://<web-domain>/auth/callback` (prod).
2. Railway `dingtalk-native-agent`: set `LARK_APP_ID`, `LARK_APP_SECRET`.
3. Railway `multica-backend`: set `LARK_AGENT_BASE_URL` (private domain), `LARK_AGENT_INTERNAL_SECRET` (= the agent's `INTERNAL_API_SECRET`), `LARK_CLIENT_ID` (non-secret, for the login button).
4. Push both repos' commits (nothing was pushed by the implementation session).
