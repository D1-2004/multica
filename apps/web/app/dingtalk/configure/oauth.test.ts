import { describe, expect, it } from "vitest";
import {
  beginOAuthState,
  buildDingTalkOAuthUrl,
  cleanConfigureUrl,
  clearOAuthState,
  oauthStateMatches,
  readConnectResult,
  savePendingConnect,
  savePendingParams,
  takePendingConnect,
  takePendingParams,
} from "./oauth";

function memoryStorage() {
  const store = new Map<string, string>();
  return {
    getItem: (key: string) => store.get(key) ?? null,
    setItem: (key: string, value: string) => void store.set(key, value),
    removeItem: (key: string) => void store.delete(key),
    store,
  };
}

describe("configure OAuth helpers", () => {
  it("redirects back to the configure page", () => {
    const url = new URL(buildDingTalkOAuthUrl("ding-client", "https://app.example", "state-1"));
    expect(url.origin + url.pathname).toBe("https://login.dingtalk.com/oauth2/auth");
    expect(url.searchParams.get("redirect_uri")).toBe("https://app.example/dingtalk/configure");
    expect(url.searchParams.get("client_id")).toBe("ding-client");
    expect(url.searchParams.get("state")).toBe("state-1");
  });

  it("keeps only the agent in the cleaned URL", () => {
    expect(cleanConfigureUrl()).toBe("/dingtalk/configure");
    expect(cleanConfigureUrl("agent 1")).toBe("/dingtalk/configure?agent=agent+1");
  });

  it("round-trips link and agent once through either storage", () => {
    const session = memoryStorage();
    const local = memoryStorage();
    savePendingParams({ linkToken: "tok", agentId: "agent-1" }, [session, local], 1_000);
    session.store.clear();

    expect(takePendingParams([session, local], 2_000)).toEqual({ linkToken: "tok", agentId: "agent-1" });
    expect(takePendingParams([session, local], 2_000)).toEqual({});
    expect(local.store.size).toBe(0);
  });

  it("drops saved parameters after the TTL", () => {
    const local = memoryStorage();
    savePendingParams({ linkToken: "tok" }, [local], 0);
    expect(takePendingParams([local], 31 * 60 * 1000)).toEqual({});
  });

  it("matches the OAuth state from either storage and clears it", () => {
    const session = memoryStorage();
    const local = memoryStorage();
    const state = beginOAuthState([session, local]);
    session.store.clear();

    expect(oauthStateMatches(state, [session, local])).toBe(true);
    expect(oauthStateMatches("forged", [session, local])).toBe(false);
    expect(oauthStateMatches("", [session, local])).toBe(false);
    clearOAuthState([session, local]);
    expect(oauthStateMatches(state, [session, local])).toBe(false);
  });
});

describe("connector OAuth round trip helpers", () => {
  it("reads the outcome the connector callback appended", () => {
    expect(readConnectResult(new URLSearchParams({ connected: "github" }))).toEqual({
      kind: "connected",
      slug: "github",
    });
    expect(readConnectResult(new URLSearchParams({ connect_error: "access_denied" }))).toEqual({
      kind: "error",
      code: "access_denied",
    });
    expect(readConnectResult(new URLSearchParams({ agent: "a" }))).toBeNull();
  });

  it("reports an unrecognizable slug or code as a generic failure", () => {
    expect(readConnectResult(new URLSearchParams({ connected: "<script>" }))).toEqual({
      kind: "error",
      code: "unknown",
    });
    expect(readConnectResult(new URLSearchParams({ connect_error: "bad code!" }))).toEqual({
      kind: "error",
      code: "unknown",
    });
    // An error wins over a success in the same URL.
    expect(
      readConnectResult(new URLSearchParams({ connected: "github", connect_error: "exchange_failed" })),
    ).toEqual({ kind: "error", code: "exchange_failed" });
  });

  it("round-trips the connecting scope once through either storage", () => {
    const session = memoryStorage();
    const local = memoryStorage();
    savePendingConnect({ agentId: "agent-1", scopeType: "scene", scopeKey: "cid+1" }, [session, local], 1_000);
    session.store.clear();

    expect(takePendingConnect([session, local], 2_000)).toEqual({
      agentId: "agent-1",
      scopeType: "scene",
      scopeKey: "cid+1",
    });
    expect(takePendingConnect([session, local], 2_000)).toBeNull();
    expect(local.store.size).toBe(0);
  });

  it("drops an expired or corrupt connecting scope", () => {
    const local = memoryStorage();
    savePendingConnect({ agentId: "agent-1", scopeType: "person", scopeKey: "staff-1" }, [local], 0);
    expect(takePendingConnect([local], 31 * 60 * 1000)).toBeNull();

    local.setItem(
      "multica_context_config_connect",
      JSON.stringify({ agent: "agent-1", scope_type: "offer", scope_key: "x", saved_at: 0 }),
    );
    expect(takePendingConnect([local], 1)).toBeNull();
    local.setItem("multica_context_config_connect", "{not json");
    expect(takePendingConnect([local], 1)).toBeNull();
  });
});
