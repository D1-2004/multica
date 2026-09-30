import { describe, expect, it, vi } from "vitest";

const sdk = vi.hoisted(() => ({
  unionCore: { config: () => {}, ready: () => {}, error: () => {} },
  choose: () => Promise.resolve({ chatId: "chat-pc", title: "PC" }),
}));

vi.mock("@multica/core/api", () => ({ api: { getDingTalkJsapiConfig: vi.fn() } }));
// The union entry carries the PC bridge; the mobile entry must not be used.
vi.mock("dingtalk-jsapi/entry/union", () => ({ default: sdk.unionCore }));
vi.mock("dingtalk-jsapi/entry/mobile", () => {
  throw new Error("the mobile entry has no DingTalk PC bridge");
});
vi.mock("dingtalk-jsapi/api/biz/chat/chooseConversationByCorpId", () => ({ default: sdk.choose }));

import {
  JSAPI_LIST,
  JsapiReloadRequiredError,
  configureJsapi,
  createGroupPicker,
  defaultJsapiDeps,
  isDingTalk,
  signatureUrl,
  type JsapiDeps,
} from "./jsapi";

const signedConfig = {
  corpId: "ding-corp",
  agentId: "123456",
  timeStamp: "1700000000",
  nonceStr: "nonce",
  signature: "sig",
};

// Mirrors dingtalk-jsapi 3.x: only the first config() is applied, and ready()
// / error() always report that first outcome.
function fakeCore(outcome: "ready" | "error") {
  let applied = false;
  const config = vi.fn(() => {
    applied = true;
  });
  return {
    config,
    ready: vi.fn((callback: () => void) => {
      if (applied && outcome === "ready") queueMicrotask(callback);
    }),
    error: vi.fn((callback: (error: unknown) => void) => {
      if (applied && outcome === "error") queueMicrotask(() => callback({ errorCode: "3" }));
    }),
  };
}

function deps(overrides: Partial<JsapiDeps> = {}): JsapiDeps & {
  core: ReturnType<typeof fakeCore>;
  choose: ReturnType<typeof vi.fn>;
} {
  const core = fakeCore("ready");
  const choose = vi.fn().mockResolvedValue({ chatId: "chat-1", title: "Sales" });
  return {
    core,
    choose,
    fetchConfig: vi.fn().mockResolvedValue(signedConfig),
    loadCore: vi.fn().mockResolvedValue(core),
    loadChooseConversation: vi.fn().mockResolvedValue(choose),
    ...overrides,
  };
}

describe("DingTalk JSAPI helpers", () => {
  it("detects the DingTalk client from the user agent", () => {
    expect(isDingTalk({ userAgent: "Mozilla/5.0 AliApp(DingTalk/7.6.50)" })).toBe(true);
    expect(
      isDingTalk({
        userAgent:
          "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) DingTalk(7.6.10-macOS-x64) nw Channel/201200",
      }),
    ).toBe(true);
    expect(isDingTalk({ userAgent: "Mozilla/5.0 Safari/605.1.15" })).toBe(false);
  });

  it("loads the union SDK entry, which includes the DingTalk PC bridge", async () => {
    await expect(defaultJsapiDeps.loadCore()).resolves.toBe(sdk.unionCore);
    await expect(defaultJsapiDeps.loadChooseConversation()).resolves.toBe(sdk.choose);
  });

  it("signs the page URL without its fragment", () => {
    expect(signatureUrl({ href: "https://app.example/dingtalk/configure?agent=a#top" })).toBe(
      "https://app.example/dingtalk/configure?agent=a",
    );
  });

  it("configures dd with the server signature and the chooser JSAPI only", async () => {
    const d = deps();
    await expect(configureJsapi("https://app.example/dingtalk/configure", d)).resolves.toEqual(signedConfig);
    expect(d.fetchConfig).toHaveBeenCalledWith("https://app.example/dingtalk/configure");
    expect(d.core.config).toHaveBeenCalledWith({
      agentId: "123456",
      corpId: "ding-corp",
      timeStamp: 1700000000,
      nonceStr: "nonce",
      signature: "sig",
      type: 0,
      jsApiList: JSAPI_LIST,
    });
    expect(JSAPI_LIST).toEqual(["biz.chat.chooseConversationByCorpId"]);
  });

  it("does not load the JSAPI when the server cannot sign", async () => {
    const d = deps({ fetchConfig: vi.fn().mockResolvedValue(null) });
    await expect(configureJsapi("https://app.example/x", d)).rejects.toThrow();
    expect(d.loadCore).not.toHaveBeenCalled();
  });

  it("rejects with a reload-required error when DingTalk refuses the signature", async () => {
    const core = fakeCore("error");
    const d = deps({ loadCore: vi.fn().mockResolvedValue(core) });
    const failure = configureJsapi("https://app.example/x", d);
    await expect(failure).rejects.toBeInstanceOf(JsapiReloadRequiredError);
    await expect(failure).rejects.toMatchObject({ reloadRequired: true });
  });
});

describe("createGroupPicker", () => {
  it("signs once and returns the picked chat", async () => {
    const d = deps();
    const pick = createGroupPicker(d, () => "https://app.example/dingtalk/configure");

    await expect(pick()).resolves.toEqual({ chatId: "chat-1", title: "Sales" });
    await expect(pick()).resolves.toEqual({ chatId: "chat-1", title: "Sales" });

    expect(d.fetchConfig).toHaveBeenCalledTimes(1);
    expect(d.choose).toHaveBeenCalledWith({ corpId: "ding-corp", isAllowCreateGroup: false });
  });

  it("treats a cancelled picker as no selection", async () => {
    const d = deps();
    d.choose.mockRejectedValueOnce({ errorCode: "-1", errorMessage: "用户取消" });
    const pick = createGroupPicker(d, () => "https://app.example/x");

    await expect(pick()).resolves.toBeNull();
  });

  it("retries the signature when it never reached dd.config", async () => {
    const fetchConfig = vi.fn().mockRejectedValueOnce(new Error("503")).mockResolvedValue(signedConfig);
    const d = deps({ fetchConfig });
    const pick = createGroupPicker(d, () => "https://app.example/x");

    await expect(pick()).rejects.toThrow("503");
    await expect(pick()).resolves.toEqual({ chatId: "chat-1", title: "Sales" });
    expect(fetchConfig).toHaveBeenCalledTimes(2);
  });

  it("does not re-sign after DingTalk rejected the applied config", async () => {
    const core = fakeCore("error");
    const d = deps({ loadCore: vi.fn().mockResolvedValue(core) });
    const pick = createGroupPicker(d, () => "https://app.example/x");

    await expect(pick()).rejects.toBeInstanceOf(JsapiReloadRequiredError);
    await expect(pick()).rejects.toBeInstanceOf(JsapiReloadRequiredError);
    // DingTalk ignores a second dd.config, so no new signature is fetched.
    expect(d.fetchConfig).toHaveBeenCalledTimes(1);
    expect(core.config).toHaveBeenCalledTimes(1);
    expect(d.choose).not.toHaveBeenCalled();
  });
});
