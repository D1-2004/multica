import { act, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "@multica/views/locales/en/common.json";
import enAgents from "@multica/views/locales/en/agents.json";
import type { ContextConfigPageProps } from "@multica/views/dingtalk";

const {
  mockDingTalkLogin,
  mockGetConfig,
  mockReplaceCurrentPage,
  mockNavigateToAuthorization,
  mockSearchParams,
  mockSetToken,
  mockSetUser,
  mockIsDingTalk,
  mockPicker,
  pageProps,
} = vi.hoisted(() => ({
  mockDingTalkLogin: vi.fn(),
  mockGetConfig: vi.fn(),
  mockReplaceCurrentPage: vi.fn(),
  mockNavigateToAuthorization: vi.fn(),
  mockSearchParams: { current: new URLSearchParams() },
  mockSetToken: vi.fn(),
  mockSetUser: vi.fn(),
  mockIsDingTalk: vi.fn(),
  mockPicker: vi.fn(),
  pageProps: { current: null as ContextConfigPageProps | null },
}));

vi.mock("next/navigation", () => ({
  useSearchParams: () => mockSearchParams.current,
}));

vi.mock("@multica/core/auth", () => ({
  useAuthStore: (selector: (state: { setUser: typeof mockSetUser }) => unknown) =>
    selector({ setUser: mockSetUser }),
}));

vi.mock("@multica/core/api", () => ({
  api: {
    fdeDingtalkLogin: mockDingTalkLogin,
    getConfig: mockGetConfig,
    setToken: mockSetToken,
  },
}));

vi.mock("@multica/views/dingtalk", () => ({
  ContextConfigPage: (props: ContextConfigPageProps) => {
    pageProps.current = props;
    return <div data-testid="context-config-page" />;
  },
}));

vi.mock("./jsapi", () => ({
  isDingTalk: mockIsDingTalk,
  createGroupPicker: () => mockPicker,
}));

vi.mock("./oauth", async () => {
  const actual = await vi.importActual<typeof import("./oauth")>("./oauth");
  return {
    ...actual,
    replaceCurrentPage: mockReplaceCurrentPage,
    navigateToAuthorization: mockNavigateToAuthorization,
  };
});

import DingTalkConfigurePage from "./page";

const resources = { en: { common: enCommon, agents: enAgents } };
const web = enAgents.context_config.web;
// A scene's scope key is its scene_id.
const SCENE_ID = "66666666-6666-4666-8666-666666666666";

function renderPage() {
  return render(
    <I18nProvider locale="en" resources={resources}>
      <DingTalkConfigurePage />
    </I18nProvider>,
  );
}

let replaceState: ReturnType<typeof vi.spyOn>;

describe("DingTalk configure route", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    localStorage.clear();
    sessionStorage.clear();
    pageProps.current = null;
    mockSearchParams.current = new URLSearchParams();
    mockIsDingTalk.mockReturnValue(false);
    mockGetConfig.mockResolvedValue({ dingtalk_client_id: "ding-client" });
    mockDingTalkLogin.mockResolvedValue({ token: "session-token", user: { id: "user-1" } });
    replaceState = vi.spyOn(window.history, "replaceState");
  });

  it("strips the link token from the URL before handing it to the page", async () => {
    mockSearchParams.current = new URLSearchParams({ link: "secret-link", agent: "agent-1" });
    renderPage();

    expect(await screen.findByTestId("context-config-page")).toBeInTheDocument();
    expect(replaceState).toHaveBeenCalledWith({}, "", "/dingtalk/configure?agent=agent-1");
    expect(pageProps.current).toMatchObject({ linkToken: "secret-link", initialAgentId: "agent-1" });
    expect(pageProps.current?.pickGroup).toBeUndefined();
    expect(mockReplaceCurrentPage).not.toHaveBeenCalled();
  });

  it("offers the group picker inside DingTalk", async () => {
    mockIsDingTalk.mockReturnValue(true);
    renderPage();

    await waitFor(() => expect(pageProps.current?.pickGroup).toBe(mockPicker));
  });

  it("extends the sRGB token fallback to <html> while mounted, so toasts stay legible", async () => {
    const before = new Set(document.documentElement.classList);
    const { unmount } = renderPage();
    await screen.findByTestId("context-config-page");

    const added = [...document.documentElement.classList].filter((name) => !before.has(name));
    expect(added).toHaveLength(1);
    expect(added[0]).toMatch(/legacyColorRoot/);
    unmount();
    expect(document.documentElement.classList.contains(added[0]!)).toBe(false);
  });

  it("starts DingTalk OAuth and keeps the link across the redirect", async () => {
    mockSearchParams.current = new URLSearchParams({ link: "secret-link" });
    renderPage();
    await screen.findByTestId("context-config-page");

    await act(async () => {
      pageProps.current?.onAuthRequired();
    });

    await waitFor(() => expect(mockReplaceCurrentPage).toHaveBeenCalledOnce());
    const oauthUrl = new URL(mockReplaceCurrentPage.mock.calls[0]![0] as string);
    expect(oauthUrl.origin).toBe("https://login.dingtalk.com");
    expect(oauthUrl.searchParams.get("redirect_uri")).toBe(`${window.location.origin}/dingtalk/configure`);
    expect(sessionStorage.getItem("multica_context_config_oauth_state")).toBe(
      oauthUrl.searchParams.get("state"),
    );
    expect(localStorage.getItem("multica_context_config_pending")).toContain("secret-link");
  });

  it("signs in with the returned code and restores the saved link", async () => {
    sessionStorage.setItem("multica_context_config_oauth_state", "state-1");
    localStorage.setItem(
      "multica_context_config_pending",
      JSON.stringify({ link: "secret-link", agent: "agent-1", saved_at: Date.now() }),
    );
    mockSearchParams.current = new URLSearchParams({ authCode: "code-1", state: "state-1" });
    renderPage();

    expect(await screen.findByTestId("context-config-page")).toBeInTheDocument();
    expect(mockDingTalkLogin).toHaveBeenCalledWith("code-1");
    expect(mockSetToken).toHaveBeenCalledWith("session-token");
    expect(mockSetUser).toHaveBeenCalledWith({ id: "user-1" });
    expect(replaceState).toHaveBeenCalledWith({}, "", "/dingtalk/configure?agent=agent-1");
    expect(pageProps.current).toMatchObject({ linkToken: "secret-link", initialAgentId: "agent-1" });
    expect(localStorage.getItem("multica_context_config_pending")).toBeNull();
  });

  it("restarts OAuth when the returned state does not match", async () => {
    sessionStorage.setItem("multica_context_config_oauth_state", "state-1");
    mockSearchParams.current = new URLSearchParams({ authCode: "code-1", state: "forged" });
    renderPage();

    await waitFor(() => expect(mockReplaceCurrentPage).toHaveBeenCalledOnce());
    expect(mockDingTalkLogin).not.toHaveBeenCalled();
  });

  it("explains a cancelled authorization", async () => {
    mockSearchParams.current = new URLSearchParams({ error: "access_denied" });
    renderPage();

    expect(await screen.findByText(web.oauth_denied)).toBeInTheDocument();
    expect(mockDingTalkLogin).not.toHaveBeenCalled();
  });

  it("sends connector sign-ins to the provider after remembering the scope", async () => {
    mockSearchParams.current = new URLSearchParams({ agent: "agent-1" });
    renderPage();
    await screen.findByTestId("context-config-page");

    act(() => {
      pageProps.current?.openAuthorizeUrl?.("https://github.com/login/oauth/authorize?state=mcpc.x", {
        agentId: "agent-1",
        scopeType: "scene",
        scopeKey: SCENE_ID,
      });
    });

    expect(mockNavigateToAuthorization).toHaveBeenCalledWith(
      "https://github.com/login/oauth/authorize?state=mcpc.x",
    );
    expect(JSON.parse(localStorage.getItem("multica_context_config_connect") ?? "{}")).toMatchObject({
      agent: "agent-1",
      scope_type: "scene",
      scope_key: SCENE_ID,
    });
  });

  it("strips the connect outcome before any signing and reopens the scope it started from", async () => {
    localStorage.setItem(
      "multica_context_config_connect",
      JSON.stringify({ agent: "agent-1", scope_type: "person", scope_key: "staff-1", saved_at: Date.now() }),
    );
    mockSearchParams.current = new URLSearchParams({ agent: "agent-1", connected: "github" });
    renderPage();

    expect(await screen.findByTestId("context-config-page")).toBeInTheDocument();
    expect(replaceState).toHaveBeenCalledWith({}, "", "/dingtalk/configure?agent=agent-1");
    expect(pageProps.current).toMatchObject({
      initialAgentId: "agent-1",
      initialScope: { scopeType: "person", scopeKey: "staff-1" },
      connectResult: { kind: "connected", slug: "github" },
    });
    expect(localStorage.getItem("multica_context_config_connect")).toBeNull();
    expect(mockReplaceCurrentPage).not.toHaveBeenCalled();
  });

  it("reopens the enterprise level of the tenant a connection started from", async () => {
    localStorage.setItem(
      "multica_context_config_connect",
      JSON.stringify({
        agent: "agent-1",
        scope_type: "org",
        scope_key: "dingB",
        org_id: "dingB",
        saved_at: Date.now(),
      }),
    );
    mockSearchParams.current = new URLSearchParams({ agent: "agent-1", connected: "github" });
    renderPage();

    expect(await screen.findByTestId("context-config-page")).toBeInTheDocument();
    expect(pageProps.current?.initialScope).toEqual({ scopeType: "org", scopeKey: "dingB", orgId: "dingB" });
  });

  it("reports a failed connection without reopening another agent's scope", async () => {
    localStorage.setItem(
      "multica_context_config_connect",
      JSON.stringify({ agent: "agent-2", scope_type: "person", scope_key: "staff-1", saved_at: Date.now() }),
    );
    mockSearchParams.current = new URLSearchParams({ agent: "agent-1", connect_error: "access_denied" });
    renderPage();

    expect(await screen.findByTestId("context-config-page")).toBeInTheDocument();
    expect(replaceState).toHaveBeenCalledWith({}, "", "/dingtalk/configure?agent=agent-1");
    expect(pageProps.current?.connectResult).toEqual({ kind: "error", code: "access_denied" });
    expect(pageProps.current?.initialScope).toBeUndefined();
  });

  it("keeps a connect outcome and its scope across a DingTalk sign-in the page needs first", async () => {
    localStorage.setItem(
      "multica_context_config_connect",
      JSON.stringify({ agent: "agent-1", scope_type: "person", scope_key: "staff-1", saved_at: Date.now() }),
    );
    mockSearchParams.current = new URLSearchParams({ agent: "agent-1", connected: "github" });
    const first = renderPage();
    await screen.findByTestId("context-config-page");

    // The session turns out to be rejected: the page signs in again.
    await act(async () => {
      pageProps.current?.onAuthRequired();
    });
    await waitFor(() => expect(mockReplaceCurrentPage).toHaveBeenCalledOnce());
    const state = new URL(mockReplaceCurrentPage.mock.calls[0]![0] as string).searchParams.get("state") ?? "";
    first.unmount();
    pageProps.current = null;

    mockSearchParams.current = new URLSearchParams({ authCode: "code-1", state });
    renderPage();

    expect(await screen.findByTestId("context-config-page")).toBeInTheDocument();
    expect(mockDingTalkLogin).toHaveBeenCalledWith("code-1");
    expect(pageProps.current).toMatchObject({
      initialAgentId: "agent-1",
      initialScope: { scopeType: "person", scopeKey: "staff-1" },
      connectResult: { kind: "connected", slug: "github" },
    });
    expect(localStorage.getItem("multica_context_config_connect")).toBeNull();
    expect(localStorage.getItem("multica_context_config_connect_result")).toBeNull();
  });

  it("keeps a redeemed link's scope in the URL and returns provider sign-ins there", async () => {
    mockSearchParams.current = new URLSearchParams({ link: "secret-link" });
    renderPage();
    await screen.findByTestId("context-config-page");
    expect(pageProps.current?.binding).toBeNull();

    act(() => {
      pageProps.current?.onBind?.({ agentId: "agent-1", scopeType: "scene", scopeKey: "cid+1", orgId: "dingB" });
    });

    const bound = "/dingtalk/configure?agent=agent-1&org=dingB&scope_type=scene&scope_key=cid%2B1";
    expect(replaceState).toHaveBeenLastCalledWith({}, "", bound);
    await waitFor(() =>
      expect(pageProps.current).toMatchObject({
        binding: { agentId: "agent-1", scopeType: "scene", scopeKey: "cid+1", orgId: "dingB" },
        connectReturnTo: bound,
        linkToken: undefined,
      }),
    );

    act(() => {
      pageProps.current?.onTabChange?.("routines");
    });
    expect(replaceState).toHaveBeenLastCalledWith({}, "", `${bound}&tab=routines`);
  });

  it("reopens the bound scope and tab from the URL on reload", async () => {
    mockSearchParams.current = new URLSearchParams({
      agent: "agent-1",
      scope_type: "person",
      scope_key: "staff-1",
      tab: "routines",
    });
    renderPage();

    expect(await screen.findByTestId("context-config-page")).toBeInTheDocument();
    expect(pageProps.current).toMatchObject({
      initialAgentId: "agent-1",
      binding: { agentId: "agent-1", scopeType: "person", scopeKey: "staff-1", orgId: "" },
      initialTab: "routines",
      connectReturnTo: "/dingtalk/configure?agent=agent-1&scope_type=person&scope_key=staff-1",
    });
    expect(pageProps.current?.linkToken).toBeUndefined();
    // Nothing to strip from the address bar.
    expect(replaceState).not.toHaveBeenCalled();
  });

  it("keeps the bound scope when a provider sign-in returns, dropping only the outcome", async () => {
    mockSearchParams.current = new URLSearchParams({
      agent: "agent-1",
      org: "dingB",
      scope_type: "scene",
      scope_key: SCENE_ID,
      connected: "github",
    });
    renderPage();

    expect(await screen.findByTestId("context-config-page")).toBeInTheDocument();
    expect(replaceState).toHaveBeenCalledWith(
      {},
      "",
      `/dingtalk/configure?agent=agent-1&org=dingB&scope_type=scene&scope_key=${SCENE_ID}`,
    );
    expect(pageProps.current).toMatchObject({
      binding: { agentId: "agent-1", scopeType: "scene", scopeKey: SCENE_ID, orgId: "dingB" },
      connectResult: { kind: "connected", slug: "github" },
    });
  });

  it("keeps the bound scope across a DingTalk sign-in", async () => {
    mockSearchParams.current = new URLSearchParams({ agent: "agent-1", scope_type: "person", scope_key: "staff-1" });
    const first = renderPage();
    await screen.findByTestId("context-config-page");

    await act(async () => {
      pageProps.current?.onAuthRequired();
    });
    await waitFor(() => expect(mockReplaceCurrentPage).toHaveBeenCalledOnce());
    const state = new URL(mockReplaceCurrentPage.mock.calls[0]![0] as string).searchParams.get("state") ?? "";
    first.unmount();
    pageProps.current = null;

    mockSearchParams.current = new URLSearchParams({ authCode: "code-1", state });
    renderPage();

    expect(await screen.findByTestId("context-config-page")).toBeInTheDocument();
    expect(replaceState).toHaveBeenLastCalledWith(
      {},
      "",
      "/dingtalk/configure?agent=agent-1&scope_type=person&scope_key=staff-1",
    );
    expect(pageProps.current).toMatchObject({
      binding: { agentId: "agent-1", scopeType: "person", scopeKey: "staff-1", orgId: "" },
    });
  });

  it("does not loop back to DingTalk when a fresh session is still rejected", async () => {
    sessionStorage.setItem("multica_context_config_oauth_state", "state-1");
    mockSearchParams.current = new URLSearchParams({ authCode: "code-1", state: "state-1" });
    renderPage();
    await screen.findByTestId("context-config-page");

    await act(async () => {
      pageProps.current?.onAuthRequired();
    });

    expect(await screen.findByText(web.auth_loop)).toBeInTheDocument();
    expect(mockReplaceCurrentPage).not.toHaveBeenCalled();
  });
});
