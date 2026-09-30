/**
 * DingTalk sign-in round trip for /dingtalk/configure. Mirrors the FDE start
 * page: the OAuth `state` lives in both session and local storage (DingTalk
 * WebViews do not always keep sessionStorage across the redirect), and the
 * page's own parameters survive the redirect the same way with a short TTL.
 */

export const CONFIGURE_PATH = "/dingtalk/configure";

const OAUTH_STATE_KEY = "multica_context_config_oauth_state";
const PENDING_KEY = "multica_context_config_pending";
const PENDING_TTL_MS = 30 * 60 * 1000;

export interface ConfigureParams {
  linkToken?: string;
  agentId?: string;
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

/** The page URL with the link token removed; `agent` is kept so a reload
 * reopens the same agent. */
export function cleanConfigureUrl(agentId?: string): string {
  if (!agentId) return CONFIGURE_PATH;
  return `${CONFIGURE_PATH}?${new URLSearchParams({ agent: agentId }).toString()}`;
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
      const parsed = JSON.parse(raw) as { link?: unknown; agent?: unknown; saved_at?: unknown };
      if (typeof parsed.saved_at !== "number" || now - parsed.saved_at > PENDING_TTL_MS) continue;
      result = {
        linkToken: typeof parsed.link === "string" && parsed.link ? parsed.link : undefined,
        agentId: typeof parsed.agent === "string" && parsed.agent ? parsed.agent : undefined,
      };
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
  scopeType: "scene" | "person";
  scopeKey: string;
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

/** Remembers which scope a connection was started from, so the page reopens
 * it when the provider redirects back (the server's return URL only carries
 * the agent). */
export function savePendingConnect(
  target: ConnectTarget,
  storages: StorageLike[] = browserStorages(),
  now: number = Date.now(),
): void {
  const value = JSON.stringify({
    agent: target.agentId,
    scope_type: target.scopeType,
    scope_key: target.scopeKey,
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
        saved_at?: unknown;
      };
      if (typeof parsed.saved_at !== "number" || now - parsed.saved_at > CONNECT_TTL_MS) continue;
      if (typeof parsed.agent !== "string" || !parsed.agent) continue;
      if (parsed.scope_type !== "scene" && parsed.scope_type !== "person") continue;
      if (typeof parsed.scope_key !== "string" || !parsed.scope_key) continue;
      result = { agentId: parsed.agent, scopeType: parsed.scope_type, scopeKey: parsed.scope_key };
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
