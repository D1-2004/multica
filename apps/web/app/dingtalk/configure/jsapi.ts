import { api } from "@multica/core/api";
import type { DingTalkJsapiConfig } from "@multica/core/context-capabilities";

/** JSAPIs this page asks DingTalk to authorize in `dd.config`. */
export const JSAPI_LIST = ["biz.chat.chooseConversationByCorpId"];

const CONFIG_TIMEOUT_MS = 10_000;

type NavigatorSnapshot = Pick<Navigator, "userAgent">;

interface DingTalkCore {
  config: (params: Record<string, unknown>) => void;
  ready: (callback: () => void) => void;
  error: (callback: (error: unknown) => void) => void;
}

interface ChooseConversationResult {
  chatId?: string;
  title?: string;
}

type ChooseConversation = (params: {
  corpId: string;
  isAllowCreateGroup?: boolean;
}) => Promise<ChooseConversationResult>;

export interface PickedGroup {
  chatId: string;
  openConversationId?: string;
  title: string;
}

export interface JsapiDeps {
  fetchConfig: (url: string) => Promise<DingTalkJsapiConfig | null>;
  loadCore: () => Promise<DingTalkCore>;
  loadChooseConversation: () => Promise<ChooseConversation>;
}

/**
 * DingTalk applies `dd.config` once per page: a second call is ignored and
 * `dd.ready` / `dd.error` keep reporting the first outcome. Once DingTalk
 * rejected (or never confirmed) the signature, only a page reload can sign
 * again. `reloadRequired` is the flag the shared configuration page checks.
 */
export class JsapiReloadRequiredError extends Error {
  readonly reloadRequired = true;

  constructor(message: string, options?: { cause?: unknown }) {
    super(message, options);
    this.name = "JsapiReloadRequiredError";
  }
}

// The dingtalk-jsapi bundle touches `window` at import time, so it is only
// ever loaded lazily from a user gesture inside the DingTalk client. The
// union entry registers every platform, including the DingTalk PC client,
// which opens configuration links in its side panel (`pc_slide=true`); the
// mobile entry has no PC bridge and fails there with not_support_env.
async function loadCore(): Promise<DingTalkCore> {
  const mod = (await import("dingtalk-jsapi/entry/union")) as unknown as
    DingTalkCore & { default?: DingTalkCore };
  return mod.default ?? mod;
}

async function loadChooseConversation(): Promise<ChooseConversation> {
  await import("dingtalk-jsapi/entry/union");
  const { default: chooseConversation } = await import(
    "dingtalk-jsapi/api/biz/chat/chooseConversationByCorpId"
  );
  return chooseConversation;
}

export const defaultJsapiDeps: JsapiDeps = {
  fetchConfig: (url) => api.getDingTalkJsapiConfig(url),
  loadCore,
  loadChooseConversation,
};
const defaultDeps = defaultJsapiDeps;

export function isDingTalk(navigatorSnapshot: NavigatorSnapshot = navigator): boolean {
  return /DingTalk/i.test(navigatorSnapshot.userAgent);
}

/** The URL DingTalk verifies the signature against: the page URL without its
 * fragment. The page strips `?link=` before this is ever read. */
export function signatureUrl(location: Pick<Location, "href"> = window.location): string {
  const hashIndex = location.href.indexOf("#");
  return hashIndex === -1 ? location.href : location.href.slice(0, hashIndex);
}

/**
 * Signs and applies `dd.config` for `url`. Resolves with the server-provided
 * config (its corp id is needed by the group picker) once DingTalk accepts
 * the signature. Rejects with a plain error when the server has no JSAPI
 * configuration or the JSAPI cannot load (retryable), and with a
 * {@link JsapiReloadRequiredError} once `dd.config` was applied but DingTalk
 * rejected or never confirmed it (only a reload can sign again).
 */
export async function configureJsapi(
  url: string,
  deps: JsapiDeps = defaultDeps,
): Promise<DingTalkJsapiConfig> {
  const config = await deps.fetchConfig(url);
  if (!config) throw new Error("DingTalk JSAPI is not available");
  const dd = await deps.loadCore();
  await new Promise<void>((resolve, reject) => {
    const timer = setTimeout(
      () => reject(new JsapiReloadRequiredError("DingTalk JSAPI configuration timed out")),
      CONFIG_TIMEOUT_MS,
    );
    dd.config({
      agentId: config.agentId,
      corpId: config.corpId,
      timeStamp: /^\d+$/.test(config.timeStamp) ? Number(config.timeStamp) : config.timeStamp,
      nonceStr: config.nonceStr,
      signature: config.signature,
      type: 0,
      jsApiList: JSAPI_LIST,
    });
    dd.ready(() => {
      clearTimeout(timer);
      resolve();
    });
    dd.error((error) => {
      clearTimeout(timer);
      reject(new JsapiReloadRequiredError("DingTalk rejected the JSAPI signature", { cause: error }));
    });
  });
  return config;
}

function isUserCancel(error: unknown): boolean {
  if (!error || typeof error !== "object") return false;
  const { errorCode, errorMessage } = error as { errorCode?: unknown; errorMessage?: unknown };
  if (errorCode === -1 || errorCode === "-1") return true;
  return typeof errorMessage === "string" && /cancel|取消/i.test(errorMessage);
}

/**
 * Returns the `pickGroup` adapter for the configuration page. `dd.config` is
 * signed lazily on the first pick and reused afterwards. A failure before
 * `dd.config` was applied (no server signature, JSAPI failed to load) is
 * retried on the next pick; once DingTalk rejected the applied config, every
 * later pick fails fast with the same {@link JsapiReloadRequiredError}
 * instead of fetching signatures DingTalk would ignore. Cancelling the picker
 * resolves to null.
 */
export function createGroupPicker(
  deps: JsapiDeps = defaultDeps,
  currentUrl: () => string = () => signatureUrl(),
): () => Promise<PickedGroup | null> {
  let configured: Promise<DingTalkJsapiConfig> | null = null;
  return async () => {
    if (!configured) {
      configured = configureJsapi(currentUrl(), deps).catch((error: unknown) => {
        if (!(error instanceof JsapiReloadRequiredError)) configured = null;
        throw error;
      });
    }
    const config = await configured;
    const chooseConversation = await deps.loadChooseConversation();
    let result: ChooseConversationResult;
    try {
      result = await chooseConversation({ corpId: config.corpId, isAllowCreateGroup: false });
    } catch (error) {
      if (isUserCancel(error)) return null;
      throw error;
    }
    if (!result?.chatId) return null;
    return { chatId: result.chatId, title: result.title ?? "" };
  };
}
