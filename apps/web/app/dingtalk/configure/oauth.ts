/**
 * DingTalk sign-in round trip for /dingtalk/configure. Mirrors the FDE start
 * page: the OAuth `state` lives in both session and local storage (DingTalk
 * WebViews do not always keep sessionStorage across the redirect), and the
 * page's own parameters survive the redirect the same way with a short TTL.
 */

import { isOrgId } from "@multica/core/context-capabilities";

export const CONFIGURE_PATH = "/dingtalk/configure";

const OAUTH_STATE_KEY = "multica_context_config_oauth_state";
const PENDING_KEY = "multica_context_config_pending";
const PENDING_TTL_MS = 30 * 60 * 1000;

/** The one scope a page opened from a configuration link stays bound to:
 * a group chat or a person of `agentId`. */
export interface ConfigureBinding {
  scopeType: "scene" | "person";
  scopeKey: string;
  /** Tenant (DingTalk OrgId); omitted for the agent's own org. */
  orgId?: string;
}

export interface ConfigureParams {
  linkToken?: string;
  agentId?: string;
  /** Kept in the URL after a link is redeemed, so a reload, a DingTalk
   * sign-in or a provider sign-in reopens the same scope only. */
  binding?: ConfigureBinding;
  /** Top-level tab (`?tab=`); the page opens its default for an unknown
   * one. */
  tab?: string;
}

const SCOPE_KEY_MAX_LENGTH = 512;
const TAB_PATTERN = /^[a-z][a-z0-9_-]{0,31}$/;

/** A binding from untrusted input (the URL or storage), or undefined. A
 * malformed tenant drops the whole binding: a person's key only means that
 * person within its own org. */
export function configureBindingOf(
  scopeType: unknown,
  scopeKey: unknown,
  orgId: unknown,
): ConfigureBinding | undefined {
  if (scopeType !== "scene" && scopeType !== "person") return undefined;
  if (
    typeof scopeKey !== "string" ||
    scopeKey === "" ||
    scopeKey.length > SCOPE_KEY_MAX_LENGTH ||
    /[\s\p{Cc}]/u.test(scopeKey)
  ) {
    return undefined;
  }
  if (orgId === undefined || orgId === null || orgId === "") return { scopeType, scopeKey };
  return isOrgId(orgId) ? { scopeType, scopeKey, orgId } : undefined;
}

function tabOf(value: unknown): string | undefined {
  return typeof value === "string" && TAB_PATTERN.test(value) ? value : undefined;
}

/** The page parameters of a configure URL: `link`, `agent`, the bound
 * scope (`org`, `scope_type`, `scope_key`; only with an agent) and `tab`. */
export function readConfigureParams(params: Pick<URLSearchParams, "get">): ConfigureParams {
  const agentId = params.get("agent") || undefined;
  const binding = agentId
    ? configureBindingOf(params.get("scope_type"), params.get("scope_key"), params.get("org"))
    : undefined;
  const result: ConfigureParams = {};
  const linkToken = params.get("link") || undefined;
  if (linkToken) result.linkToken = linkToken;
  if (agentId) result.agentId = agentId;
  if (binding) result.binding = binding;
  const tab = tabOf(params.get("tab"));
  if (tab) result.tab = tab;
  return result;
}

type StorageLike = Pick<Storage, "getItem" | "setItem" | "removeItem">;

function browserStorages(): StorageLike[] {
  const storages: StorageLike[] = [];
  try {
    storages.push(window.sessionStorage);
  } catch {
    // Storage can be unavailable in hardened WebViews.
  }
  try {
    storages.push(window.localStorage);
  } catch {
    // Same as above.
  }
  return storages;
}

function safeSet(storage: StorageLike, key: string, value: string) {
  try {
    storage.setItem(key, value);
  } catch {
    // Quota or privacy mode: the other storage may still work.
  }
}

function safeGet(storage: StorageLike, key: string): string | null {
  try {
    return storage.getItem(key);
  } catch {
    return null;
  }
}

function safeRemove(storage: StorageLike, key: string) {
  try {
    storage.removeItem(key);
  } catch {
    // Nothing else to do.
  }
}

/** The page URL without the link token: `agent`, the bound scope and the
 * tab are kept so a reload reopens the same page. */
export function cleanConfigureUrl(params: Omit<ConfigureParams, "linkToken"> = {}): string {
  const query = new URLSearchParams();
  if (params.agentId) {
    query.set("agent", params.agentId);
    if (params.binding) {
      if (params.binding.orgId) query.set("org", params.binding.orgId);
      query.set("scope_type", params.binding.scopeType);
      query.set("scope_key", params.binding.scopeKey);
    }
  }
  if (params.tab) query.set("tab", params.tab);
  const search = query.toString();
  return search ? `${CONFIGURE_PATH}?${search}` : CONFIGURE_PATH;
}

export function savePendingParams(
  params: ConfigureParams,
  storages: StorageLike[] = browserStorages(),
  now: number = Date.now(),
): void {
  if (!params.linkToken && !params.agentId) {
    for (const storage of storages) safeRemove(storage, PENDING_KEY);
    return;
  }
  const value = JSON.stringify({
    link: params.linkToken ?? "",
    agent: params.agentId ?? "",
    scope_type: params.binding?.scopeType ?? "",
    scope_key: params.binding?.scopeKey ?? "",
    org: params.binding?.orgId ?? "",
    tab: params.tab ?? "",
    saved_at: now,
  });
  for (const storage of storages) safeSet(storage, PENDING_KEY, value);
}

/** Reads and clears the parameters saved before the OAuth redirect. */
export function takePendingParams(
  storages: StorageLike[] = browserStorages(),
  now: number = Date.now(),
): ConfigureParams {
  let result: ConfigureParams = {};
  for (const storage of storages) {
    const raw = safeGet(storage, PENDING_KEY);
    safeRemove(storage, PENDING_KEY);
    if (!raw || result.linkToken || result.agentId) continue;
    try {
      const parsed = JSON.parse(raw) as {
        link?: unknown;
        agent?: unknown;
        scope_type?: unknown;
        scope_key?: unknown;
        org?: unknown;
        tab?: unknown;
        saved_at?: unknown;
      };
      if (typeof parsed.saved_at !== "number" || now - parsed.saved_at > PENDING_TTL_MS) continue;
      const agentId = typeof parsed.agent === "string" && parsed.agent ? parsed.agent : undefined;
      result = {
        linkToken: typeof parsed.link === "string" && parsed.link ? parsed.link : undefined,
        agentId,
      };
      const binding = agentId ? configureBindingOf(parsed.scope_type, parsed.scope_key, parsed.org) : undefined;
      if (binding) result.binding = binding;
      const tab = tabOf(parsed.tab);
      if (tab) result.tab = tab;
    } catch {
      // Ignore corrupt entries.
    }
  }
  return result;
}

export function beginOAuthState(storages: StorageLike[] = browserStorages()): string {
  const state = crypto.randomUUID();
  for (const storage of storages) safeSet(storage, OAUTH_STATE_KEY, state);
  return state;
}

export function oauthStateMatches(
  returned: string,
  storages: StorageLike[] = browserStorages(),
): boolean {
  if (!returned) return false;
  return storages.some((storage) => safeGet(storage, OAUTH_STATE_KEY) === returned);
}

export function clearOAuthState(storages: StorageLike[] = browserStorages()): void {
  for (const storage of storages) safeRemove(storage, OAUTH_STATE_KEY);
}

export function buildDingTalkOAuthUrl(clientId: string, origin: string, state: string): string {
  const params = new URLSearchParams({
    client_id: clientId,
    redirect_uri: `${origin}${CONFIGURE_PATH}`,
    response_type: "code",
    scope: "openid",
    prompt: "consent",
    state,
  });
  return `https://login.dingtalk.com/oauth2/auth?${params.toString()}`;
}

export function replaceCurrentPage(url: string): void {
  window.location.replace(url);
}

// ---------------------------------------------------------------------------
// Connector OAuth round trip (official apps). The page leaves for the
// provider's authorization URL; the server callback redirects back to
// /dingtalk/configure?agent=<id>&connected=<slug> (or &connect_error=<code>).
// ---------------------------------------------------------------------------

const CONNECT_KEY = "multica_context_config_connect";
const CONNECT_TTL_MS = 30 * 60 * 1000;
const SLUG_PATTERN = /^[a-z0-9][a-z0-9_-]{0,63}$/;
const ERROR_CODE_PATTERN = /^[A-Za-z0-9_.-]{1,64}$/;

export interface ConnectTarget {
  agentId: string;
  scopeType: "org" | "scene" | "person";
  scopeKey: string;
  /** Tenant (DingTalk OrgId) of the scope; omitted for the server's default. */
  orgId?: string;
}

export type ConnectResult =
  | { kind: "connected"; slug: string }
  | { kind: "error"; code: string };

/** Outcome parameters the connector OAuth callback appended, or null. An
 * unrecognizable slug or code is reported as a generic failure. */
export function readConnectResult(params: Pick<URLSearchParams, "get">): ConnectResult | null {
  const error = params.get("connect_error");
  if (error !== null) {
    return { kind: "error", code: ERROR_CODE_PATTERN.test(error) ? error : "unknown" };
  }
  const connected = params.get("connected");
  if (connected === null) return null;
  return SLUG_PATTERN.test(connected)
    ? { kind: "connected", slug: connected }
    : { kind: "error", code: "unknown" };
}

/** Remembers which scope (and tenant) a connection was started from, so the
 * page reopens it when the provider redirects back (the server's return URL
 * only carries the agent). */
export function savePendingConnect(
  target: ConnectTarget,
  storages: StorageLike[] = browserStorages(),
  now: number = Date.now(),
): void {
  const value = JSON.stringify({
    agent: target.agentId,
    scope_type: target.scopeType,
    scope_key: target.scopeKey,
    org_id: target.orgId ?? "",
    saved_at: now,
  });
  for (const storage of storages) safeSet(storage, CONNECT_KEY, value);
}

/** Reads and clears the scope saved before the provider redirect. */
export function takePendingConnect(
  storages: StorageLike[] = browserStorages(),
  now: number = Date.now(),
): ConnectTarget | null {
  let result: ConnectTarget | null = null;
  for (const storage of storages) {
    const raw = safeGet(storage, CONNECT_KEY);
    safeRemove(storage, CONNECT_KEY);
    if (!raw || result) continue;
    try {
      const parsed = JSON.parse(raw) as {
        agent?: unknown;
        scope_type?: unknown;
        scope_key?: unknown;
        org_id?: unknown;
        saved_at?: unknown;
      };
      if (typeof parsed.saved_at !== "number" || now - parsed.saved_at > CONNECT_TTL_MS) continue;
      if (typeof parsed.agent !== "string" || !parsed.agent) continue;
      if (
        parsed.scope_type !== "org" &&
        parsed.scope_type !== "scene" &&
        parsed.scope_type !== "person"
      ) {
        continue;
      }
      if (typeof parsed.scope_key !== "string" || !parsed.scope_key) continue;
      // A malformed tenant reads as the server's default.
      const orgId = isOrgId(parsed.org_id) ? parsed.org_id : "";
      // The enterprise level is only meaningful with its tenant.
      if (parsed.scope_type === "org" && !orgId) continue;
      result = {
        agentId: parsed.agent,
        scopeType: parsed.scope_type,
        scopeKey: parsed.scope_key,
        ...(orgId ? { orgId } : {}),
      };
    } catch {
      // Ignore corrupt entries.
    }
  }
  return result;
}

const CONNECT_RESULT_KEY = "multica_context_config_connect_result";

/** Keeps a connect outcome the page could not show yet across a DingTalk
 * sign-in redirect (the session was rejected right after the provider
 * returned). Pair it with savePendingConnect for the scope. */
export function savePendingConnectResult(
  result: ConnectResult,
  storages: StorageLike[] = browserStorages(),
  now: number = Date.now(),
): void {
  const value = JSON.stringify({
    ...(result.kind === "connected" ? { connected: result.slug } : { connect_error: result.code }),
    saved_at: now,
  });
  for (const storage of storages) safeSet(storage, CONNECT_RESULT_KEY, value);
}

/** Reads and clears the outcome saved before a DingTalk sign-in redirect,
 * validated like the callback's own parameters. */
export function takePendingConnectResult(
  storages: StorageLike[] = browserStorages(),
  now: number = Date.now(),
): ConnectResult | null {
  let result: ConnectResult | null = null;
  for (const storage of storages) {
    const raw = safeGet(storage, CONNECT_RESULT_KEY);
    safeRemove(storage, CONNECT_RESULT_KEY);
    if (!raw || result) continue;
    try {
      const parsed = JSON.parse(raw) as { connected?: unknown; connect_error?: unknown; saved_at?: unknown };
      if (typeof parsed.saved_at !== "number" || now - parsed.saved_at > CONNECT_TTL_MS) continue;
      result = readConnectResult({
        get: (name: string) => {
          const value = name === "connected" ? parsed.connected : name === "connect_error" ? parsed.connect_error : undefined;
          return typeof value === "string" ? value : null;
        },
      });
    } catch {
      // Ignore corrupt entries.
    }
  }
  return result;
}

/** Hands the current WebView to the provider's authorization page. `assign`
 * (not `replace`) keeps this page in history so Back returns here. */
export function navigateToAuthorization(url: string): void {
  window.location.assign(url);
}
