// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type {
  ContextConfigAgentDetail,
  ContextConfigAgentSummary,
  ContextConfigSceneDetail,
  ContextOfferedConnector,
} from "@multica/core/context-capabilities";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../locales/en/common.json";
import enAgents from "../locales/en/agents.json";

const TEST_RESOURCES = { en: { common: enCommon, agents: enAgents } };

const api = vi.hoisted(() => ({
  redeemContextConfigLink: vi.fn(),
  listContextConfigAgents: vi.fn(),
  getContextConfigAgent: vi.fn(),
  getContextConfigScene: vi.fn(),
  setContextCapabilityBinding: vi.fn(),
  setContextConnectorCredential: vi.fn(),
  deleteContextConnectorCredential: vi.fn(),
  resolveContextConfigScene: vi.fn(),
  startContextConnectorConnection: vi.fn(),
  setContextConfigPrompts: vi.fn(),
  setContextConfigMcpConfig: vi.fn(),
}));

const { ApiError, errorCode } = vi.hoisted(() => {
  class ApiError extends Error {
    status: number;
    statusText: string;
    body?: unknown;
    constructor(message: string, status: number, statusText = "", body?: unknown) {
      super(message);
      this.status = status;
      this.statusText = statusText;
      this.body = body;
    }
  }
  // Mirrors errorCode from @multica/core/api.
  function errorCode(err: unknown): string | undefined {
    if (err instanceof ApiError && err.body && typeof err.body === "object") {
      const code = (err.body as { code?: unknown }).code;
      if (typeof code === "string" && code.length > 0) return code;
    }
    return undefined;
  }
  return { ApiError, errorCode };
});

vi.mock("@multica/core/api", () => ({ api, ApiError, errorCode }));

vi.mock("sonner", () => ({
  toast: { error: vi.fn(), success: vi.fn() },
}));

import {
  ContextConfigPage,
  type ContextConfigBinding,
  type ContextConfigPageProps,
} from "./context-config-page";

const copy = enAgents.context_config;

// A scene's scope key is its scene_id (a group chat or a 1:1 chat alike).
const SALES_SCENE = "66666666-6666-4666-8666-666666666666";
const OPS_SCENE = "77777777-7777-4777-8777-777777777777";
const DM_SCENE = "88888888-8888-4888-8888-888888888888";
const PARTNER_SCENE = "99999999-9999-4999-8999-999999999999";

/** Scope content from a backend without rights, prompts or MCP servers. */
const noContent = { rights: null, prompts: [], mcpConfig: null, mcpConfigRedacted: false };

const agentSummary: ContextConfigAgentSummary = {
  id: "agent-1",
  name: "Helper",
  avatarUrl: null,
  workspaceId: "ws-1",
  access: "grant",
  scopes: [],
};

function agentDetail(overrides: Partial<ContextConfigAgentDetail> = {}): ContextConfigAgentDetail {
  return {
    agent: { id: "agent-1", name: "Helper", avatarUrl: null, workspaceId: "ws-1" },
    global: { connectors: [{ id: "conn-global", name: "Docs", catalogSlug: "" }], skills: [] },
    offers: {
      connectors: [
        {
          id: "conn-wiki",
          name: "Wiki",
          tools: ["search", "read"],
          acceptsCredential: true,
          credentialRequired: true,
          catalogSlug: "",
          authMode: "bearer",
          acceptsPat: false,
          oauthAvailable: false,
          installUrl: "",
        },
      ],
      skills: [{ id: "skill-report", name: "Weekly report", description: "Writes reports" }],
    },
    person: {
      scopeKey: "staff-1",
      scopeTitle: "Alice",
      source: "agent_link",
      expiresAt: "",
      bindings: [],
      credentials: [],
      ...noContent,
    },
    scenes: [{ scopeKey: SALES_SCENE, scopeTitle: "Sales team", source: "agent_link", expiresAt: "", kind: "group", orgId: "" }],
    tenant: null,
    tenants: [],
    org: null,
    jsapiAvailable: false,
    access: "grant",
    ...overrides,
  };
}

const sceneDetail: ContextConfigSceneDetail = {
  scene: { scopeKey: SALES_SCENE, scopeTitle: "Sales team", source: "agent_link", expiresAt: "", kind: "group", orgId: "" },
  bindings: [{ resourceType: "connector", resourceId: "conn-wiki", enabled: true, shareInGroups: false }],
  credentials: [{ connectorId: "conn-wiki", hint: "••••abcd", updatedAt: "", kind: "bearer" }],
  scope: { type: "scene", key: SALES_SCENE, title: "Sales team" },
  canConnect: true,
  ...noContent,
};

const githubConnector: ContextOfferedConnector = {
  id: "conn-github",
  name: "GitHub",
  tools: ["get_me", "search_code"],
  acceptsCredential: false,
  credentialRequired: true,
  catalogSlug: "github",
  authMode: "oauth",
  acceptsPat: true,
  oauthAvailable: true,
  installUrl: "https://github.com/apps/multica/installations/new",
};

function oauthDetail(overrides: Partial<ContextConfigAgentDetail> = {}): ContextConfigAgentDetail {
  const detail = agentDetail(overrides);
  return {
    ...detail,
    offers: { connectors: [githubConnector], skills: [] },
    ...overrides,
  };
}

function renderPage(props: Partial<ContextConfigPageProps> = {}) {
  const onAuthRequired = props.onAuthRequired ?? vi.fn();
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  render(
    <I18nProvider locale="en" resources={TEST_RESOURCES}>
      <QueryClientProvider client={queryClient}>
        <ContextConfigPage {...props} onAuthRequired={onAuthRequired} />
      </QueryClientProvider>
    </I18nProvider>,
  );
  return { onAuthRequired };
}

beforeEach(() => {
  vi.clearAllMocks();
  api.listContextConfigAgents.mockResolvedValue([agentSummary]);
  api.getContextConfigAgent.mockResolvedValue(agentDetail());
  api.getContextConfigScene.mockResolvedValue(sceneDetail);
  api.setContextCapabilityBinding.mockImplementation(
    async (
      _agentId: string,
      input: { resourceType: string; resourceId: string; enabled: boolean; shareInGroups?: boolean },
    ) => ({
      resourceType: input.resourceType,
      resourceId: input.resourceId,
      enabled: input.enabled,
      shareInGroups: input.shareInGroups === true,
    }),
  );
});

describe("ContextConfigPage", () => {
  it("redeems a group link once and opens that group's settings", async () => {
    api.redeemContextConfigLink.mockResolvedValue({
      agentId: "agent-1",
      workspaceId: "ws-1",
      scopeType: "scene",
      scopeKey: SALES_SCENE,
      scopeTitle: "Sales team",
    });
    const user = userEvent.setup();
    const onBind = vi.fn();
    renderPage({ linkToken: "link-token", onBind });

    expect(await screen.findByRole("region", { name: "Sales team" })).toBeInTheDocument();
    expect(api.redeemContextConfigLink).toHaveBeenCalledTimes(1);
    expect(api.redeemContextConfigLink).toHaveBeenCalledWith("link-token");
    expect(api.getContextConfigScene).toHaveBeenCalledWith("agent-1", SALES_SCENE, "");
    expect(onBind).toHaveBeenCalledWith({ agentId: "agent-1", scopeType: "scene", scopeKey: SALES_SCENE, orgId: "" });

    const wikiToggle = screen.getByRole("switch", { name: "Turn Wiki on or off" });
    expect(wikiToggle).toBeChecked();
    expect(screen.getByText("Credential saved (••••abcd)")).toBeInTheDocument();

    await user.click(screen.getByRole("switch", { name: "Turn Weekly report on or off" }));

    await waitFor(() =>
      expect(api.setContextCapabilityBinding).toHaveBeenCalledWith("agent-1", {
        scopeType: "scene",
        scopeKey: SALES_SCENE,
        resourceType: "skill",
        resourceId: "skill-report",
        enabled: true,
      }),
    );
  });

  it("asks the platform to sign in when the session is rejected", async () => {
    api.listContextConfigAgents.mockRejectedValue(new ApiError("unauthorized", 401));
    const { onAuthRequired } = renderPage();

    await waitFor(() => expect(onAuthRequired).toHaveBeenCalledTimes(1));
  });

  it("treats a non-DingTalk session as signed out", async () => {
    api.listContextConfigAgents.mockRejectedValue(
      new ApiError("this endpoint requires DingTalk user authentication", 403),
    );
    const { onAuthRequired } = renderPage();

    await waitFor(() => expect(onAuthRequired).toHaveBeenCalledTimes(1));
  });

  it("does not restart sign-in when a grant is missing", async () => {
    api.getContextConfigAgent.mockRejectedValue(new ApiError("no grant", 403));
    const { onAuthRequired } = renderPage({ initialAgentId: "agent-1" });

    expect(await screen.findByText(copy.no_access_title)).toBeInTheDocument();
    expect(onAuthRequired).not.toHaveBeenCalled();
  });

  it("explains an expired link and still lists existing access", async () => {
    api.redeemContextConfigLink.mockRejectedValue(new ApiError("gone", 410));
    api.listContextConfigAgents.mockResolvedValue([]);
    renderPage({ linkToken: "old" });

    expect(await screen.findByText(copy.link_expired)).toBeInTheDocument();
    expect(await screen.findByText(copy.no_access_title)).toBeInTheDocument();
  });

  it("explains a personal link whose scope already belongs to another account", async () => {
    api.redeemContextConfigLink.mockRejectedValue(new ApiError("conflict", 409));
    api.listContextConfigAgents.mockResolvedValue([]);
    renderPage({ linkToken: "forwarded" });

    expect(await screen.findByText(copy.link_taken)).toBeInTheDocument();
    expect(screen.queryByText(copy.link_failed)).not.toBeInTheDocument();
  });

  it("saves a personal credential write-only and clears the input", async () => {
    api.setContextConnectorCredential.mockResolvedValue({
      connectorId: "conn-wiki",
      hint: "••••cret",
      updatedAt: "",
    });
    const user = userEvent.setup();
    renderPage({ initialAgentId: "agent-1" });

    await user.click(await screen.findByRole("tab", { name: copy.tab_person }));
    const region = await screen.findByRole("region", { name: "Alice" });
    expect(within(region).getByText(copy.credential_required)).toBeInTheDocument();

    await user.click(within(region).getByRole("button", { name: copy.set_credential }));
    const input = within(region).getByLabelText("Bearer token for Wiki");
    expect(input).toHaveAttribute("type", "password");
    await user.type(input, "  super-secret  ");
    await user.click(within(region).getByRole("button", { name: copy.save }));

    await waitFor(() =>
      expect(api.setContextConnectorCredential).toHaveBeenCalledWith("agent-1", {
        scopeType: "person",
        scopeKey: "staff-1",
        connectorId: "conn-wiki",
        bearer: "super-secret",
      }),
    );
    await waitFor(() =>
      expect(within(region).queryByLabelText("Bearer token for Wiki")).not.toBeInTheDocument(),
    );
    expect(screen.queryByDisplayValue(/super-secret/)).not.toBeInTheDocument();
  });

  it("confirms before removing a group credential", async () => {
    api.deleteContextConnectorCredential.mockResolvedValue(undefined);
    const user = userEvent.setup();
    renderPage({ initialAgentId: "agent-1" });

    const region = await screen.findByRole("region", { name: "Sales team" });
    await user.click(within(region).getByRole("button", { name: copy.remove_credential }));
    expect(api.deleteContextConnectorCredential).not.toHaveBeenCalled();
    await user.click(within(region).getByRole("button", { name: copy.confirm_remove }));

    await waitFor(() =>
      expect(api.deleteContextConnectorCredential).toHaveBeenCalledWith("agent-1", {
        scopeType: "scene",
        scopeKey: SALES_SCENE,
        connectorId: "conn-wiki",
      }),
    );
  });

  it("explains how to get a personal link when none is granted", async () => {
    api.getContextConfigAgent.mockResolvedValue(agentDetail({ person: null }));
    const user = userEvent.setup();
    renderPage({ initialAgentId: "agent-1" });

    await user.click(await screen.findByRole("tab", { name: copy.tab_person }));

    expect(screen.getByText(copy.person_empty_title)).toBeInTheDocument();
    expect(screen.getByText(copy.person_empty_hint)).toBeInTheDocument();
  });

  it("links a group through the DingTalk picker when a personal grant exists", async () => {
    api.getContextConfigAgent.mockResolvedValue(agentDetail({ scenes: [], jsapiAvailable: true }));
    api.resolveContextConfigScene.mockResolvedValue({
      scopeKey: OPS_SCENE,
      scopeTitle: "Ops",
      source: "jsapi",
      expiresAt: "",
    });
    const pickGroup = vi.fn().mockResolvedValue({ chatId: "chat-9" });
    const user = userEvent.setup();
    renderPage({ initialAgentId: "agent-1", pickGroup });

    // With only a personal grant the page opens on "Mine".
    await user.click(await screen.findByRole("tab", { name: copy.tab_scene }));
    expect(screen.getByText(copy.scene_empty_title)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: copy.pick_group }));

    await waitFor(() =>
      expect(api.resolveContextConfigScene).toHaveBeenCalledWith("agent-1", { chatId: "chat-9" }),
    );
  });

  it("links a picked group in the page's tenant", async () => {
    const beta = { orgId: "ding2", name: "Beta", source: "created" };
    api.getContextConfigAgent.mockResolvedValue(
      agentDetail({ scenes: [], jsapiAvailable: true, tenant: beta, tenants: [beta] }),
    );
    api.resolveContextConfigScene.mockResolvedValue({
      scopeKey: OPS_SCENE,
      scopeTitle: "Ops",
      source: "jsapi",
      expiresAt: "",
      kind: "group",
      orgId: "ding2",
    });
    const pickGroup = vi.fn().mockResolvedValue({ chatId: "chat-9" });
    const user = userEvent.setup();
    renderPage({ initialAgentId: "agent-1", pickGroup });

    await user.click(await screen.findByRole("tab", { name: copy.tab_scene }));
    await user.click(screen.getByRole("button", { name: copy.pick_group }));

    await waitFor(() =>
      expect(api.resolveContextConfigScene).toHaveBeenCalledWith("agent-1", { chatId: "chat-9", orgId: "ding2" }),
    );
  });

  it("needs a picked chatId; a bare openConversationId proves nothing", async () => {
    api.getContextConfigAgent.mockResolvedValue(agentDetail({ scenes: [], jsapiAvailable: true }));
    const pickGroup = vi.fn().mockResolvedValue({ openConversationId: "cid-guess" });
    const user = userEvent.setup();
    renderPage({ initialAgentId: "agent-1", pickGroup });

    await user.click(await screen.findByRole("tab", { name: copy.tab_scene }));
    await user.click(screen.getByRole("button", { name: copy.pick_group }));

    await waitFor(() => expect(pickGroup).toHaveBeenCalledTimes(1));
    expect(api.resolveContextConfigScene).not.toHaveBeenCalled();
  });

  it("asks for a reload when the picker cannot sign again on this page", async () => {
    const { toast } = await import("sonner");
    api.getContextConfigAgent.mockResolvedValue(agentDetail({ scenes: [], jsapiAvailable: true }));
    const reload = Object.assign(new Error("DingTalk rejected the JSAPI signature"), { reloadRequired: true });
    const pickGroup = vi.fn().mockRejectedValue(reload);
    const user = userEvent.setup();
    renderPage({ initialAgentId: "agent-1", pickGroup });

    await user.click(await screen.findByRole("tab", { name: copy.tab_scene }));
    await user.click(screen.getByRole("button", { name: copy.pick_group }));

    await waitFor(() => expect(toast.error).toHaveBeenCalledWith(copy.pick_group_reload));
  });

  it("keeps each row pending until its own write settles", async () => {
    const pending = new Map<string, () => void>();
    api.setContextCapabilityBinding.mockImplementation(
      (_agentId: string, input: { resourceType: string; resourceId: string; enabled: boolean }) =>
        new Promise((resolve) => {
          pending.set(input.resourceId, () =>
            resolve({ resourceType: input.resourceType, resourceId: input.resourceId, enabled: input.enabled }),
          );
        }),
    );
    const user = userEvent.setup();
    renderPage({ initialAgentId: "agent-1" });

    await screen.findByRole("region", { name: "Sales team" });
    await user.click(screen.getByRole("switch", { name: "Turn Weekly report on or off" }));
    await user.click(screen.getByRole("switch", { name: "Turn Wiki on or off" }));
    await waitFor(() => expect(pending.size).toBe(2));
    expect(screen.queryByRole("switch", { name: "Turn Weekly report on or off" })).not.toBeInTheDocument();
    expect(screen.queryByRole("switch", { name: "Turn Wiki on or off" })).not.toBeInTheDocument();

    pending.get("conn-wiki")!();
    await waitFor(() =>
      expect(screen.getByRole("switch", { name: "Turn Wiki on or off" })).toBeInTheDocument(),
    );
    // The skill write is still in flight, so its row stays busy.
    expect(screen.queryByRole("switch", { name: "Turn Weekly report on or off" })).not.toBeInTheDocument();

    pending.get("skill-report")!();
    await waitFor(() =>
      expect(screen.getByRole("switch", { name: "Turn Weekly report on or off" })).toBeInTheDocument(),
    );
    expect(api.setContextCapabilityBinding).toHaveBeenCalledTimes(2);
  });

  it("offers switching when the preselected agent has no grant but another one does", async () => {
    api.listContextConfigAgents.mockResolvedValue([{ ...agentSummary, id: "agent-2", name: "Planner" }]);
    api.getContextConfigAgent.mockImplementation(async (agentId: string) => {
      if (agentId === "agent-1") throw new ApiError("no grant", 403);
      return agentDetail({ agent: { id: "agent-2", name: "Planner", avatarUrl: null, workspaceId: "ws-1" } });
    });
    const user = userEvent.setup();
    renderPage({ initialAgentId: "agent-1" });

    expect(await screen.findByText(copy.no_access_title)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: copy.switch_agent }));

    await waitFor(() => expect(api.getContextConfigAgent).toHaveBeenCalledWith("agent-2", ""));
  });

  it("hides the DingTalk picker outside the DingTalk client", async () => {
    api.getContextConfigAgent.mockResolvedValue(agentDetail({ scenes: [], jsapiAvailable: true }));
    const user = userEvent.setup();
    renderPage({ initialAgentId: "agent-1" });

    await user.click(await screen.findByRole("tab", { name: copy.tab_scene }));
    expect(screen.getByText(copy.scene_empty_title)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: copy.pick_group })).not.toBeInTheDocument();
  });

  describe("official app (OAuth) connectors", () => {
    const person = agentDetail().person!;

    beforeEach(() => {
      api.getContextConfigScene.mockResolvedValue({ ...sceneDetail, bindings: [], credentials: [] });
    });

    it("connects through the provider sign-in for the open scope", async () => {
      api.getContextConfigAgent.mockResolvedValue(oauthDetail());
      api.startContextConnectorConnection.mockResolvedValue(
        "https://github.com/login/oauth/authorize?state=mcpc.x",
      );
      const openAuthorizeUrl = vi.fn();
      const user = userEvent.setup();
      renderPage({ initialAgentId: "agent-1", openAuthorizeUrl });

      const region = await screen.findByRole("region", { name: "Sales team" });
      expect(within(region).getByText(copy.connect_required)).toBeInTheDocument();
      expect(region.querySelector('[data-connector-mark="github"]')).not.toBeNull();
      // OAuth connectors never show the raw Bearer input.
      expect(within(region).queryByRole("button", { name: copy.set_credential })).not.toBeInTheDocument();
      expect(within(region).getByRole("link", { name: copy.install_link })).toHaveAttribute(
        "href",
        githubConnector.installUrl,
      );

      await user.click(within(region).getByRole("button", { name: copy.connect }));

      await waitFor(() =>
        expect(openAuthorizeUrl).toHaveBeenCalledWith(
          "https://github.com/login/oauth/authorize?state=mcpc.x",
          { agentId: "agent-1", scopeType: "scene", scopeKey: SALES_SCENE },
        ),
      );
      expect(api.startContextConnectorConnection).toHaveBeenCalledWith("agent-1", {
        scopeType: "scene",
        scopeKey: SALES_SCENE,
        connectorId: "conn-github",
      });
      // The page is leaving for the provider; a second tap cannot start another flow.
      expect(within(region).getByRole("button", { name: copy.connecting })).toBeDisabled();
    });

    it("does not navigate when the server returns no usable authorization URL", async () => {
      const { toast } = await import("sonner");
      api.getContextConfigAgent.mockResolvedValue(oauthDetail());
      api.startContextConnectorConnection.mockResolvedValue("");
      const openAuthorizeUrl = vi.fn();
      const user = userEvent.setup();
      renderPage({ initialAgentId: "agent-1", openAuthorizeUrl });

      const region = await screen.findByRole("region", { name: "Sales team" });
      await user.click(within(region).getByRole("button", { name: copy.connect }));

      await waitFor(() => expect(toast.error).toHaveBeenCalledWith(copy.connect_failed));
      expect(openAuthorizeUrl).not.toHaveBeenCalled();
      expect(within(region).getByRole("button", { name: copy.connect })).toBeEnabled();
    });

    it("shows the connected account and disconnects only after confirmation", async () => {
      api.getContextConfigAgent.mockResolvedValue(
        oauthDetail({
          person: {
            ...person,
            credentials: [{ connectorId: "conn-github", hint: "@octocat", updatedAt: "", kind: "oauth" }],
          },
        }),
      );
      api.deleteContextConnectorCredential.mockResolvedValue(undefined);
      const user = userEvent.setup();
      renderPage({ initialAgentId: "agent-1", openAuthorizeUrl: vi.fn() });

      await user.click(await screen.findByRole("tab", { name: copy.tab_person }));
      const region = await screen.findByRole("region", { name: "Alice" });
      expect(within(region).getByText("Connected @octocat")).toBeInTheDocument();
      expect(within(region).queryByRole("button", { name: copy.connect })).not.toBeInTheDocument();

      await user.click(within(region).getByRole("button", { name: copy.disconnect }));
      expect(api.deleteContextConnectorCredential).not.toHaveBeenCalled();
      await user.click(within(region).getByRole("button", { name: copy.confirm_disconnect }));

      await waitFor(() =>
        expect(api.deleteContextConnectorCredential).toHaveBeenCalledWith("agent-1", {
          scopeType: "person",
          scopeKey: "staff-1",
          connectorId: "conn-github",
        }),
      );
    });

    it("never shows a generic OAuth hint as an account name", async () => {
      api.getContextConfigAgent.mockResolvedValue(
        oauthDetail({
          person: {
            ...person,
            credentials: [{ connectorId: "conn-github", hint: "OAuth", updatedAt: "", kind: "oauth" }],
          },
        }),
      );
      const user = userEvent.setup();
      renderPage({ initialAgentId: "agent-1" });

      await user.click(await screen.findByRole("tab", { name: copy.tab_person }));
      const region = await screen.findByRole("region", { name: "Alice" });
      expect(within(region).getByText(copy.connected)).toBeInTheDocument();
    });

    it("offers a Personal Access Token for GitHub as a secondary option", async () => {
      api.getContextConfigAgent.mockResolvedValue(oauthDetail());
      api.setContextConnectorCredential.mockResolvedValue({
        connectorId: "conn-github",
        hint: "••••1234",
        updatedAt: "",
        kind: "bearer",
      });
      const user = userEvent.setup();
      // Without a platform navigator the provider sign-in is unavailable,
      // but the token path still works.
      renderPage({ initialAgentId: "agent-1" });

      await user.click(await screen.findByRole("tab", { name: copy.tab_person }));
      const region = await screen.findByRole("region", { name: "Alice" });
      expect(within(region).queryByRole("button", { name: copy.connect })).not.toBeInTheDocument();

      await user.click(within(region).getByRole("button", { name: copy.pat_connect }));
      const input = within(region).getByLabelText("Personal Access Token for GitHub");
      expect(input).toHaveAttribute("type", "password");
      await user.type(input, "ghp_example");
      await user.click(within(region).getByRole("button", { name: copy.save }));

      await waitFor(() =>
        expect(api.setContextConnectorCredential).toHaveBeenCalledWith("agent-1", {
          scopeType: "person",
          scopeKey: "staff-1",
          connectorId: "conn-github",
          bearer: "ghp_example",
        }),
      );
      await waitFor(() =>
        expect(within(region).queryByLabelText("Personal Access Token for GitHub")).not.toBeInTheDocument(),
      );
    });

    it("hides the token option for OAuth apps that do not accept one", async () => {
      api.getContextConfigAgent.mockResolvedValue(
        oauthDetail({
          offers: {
            connectors: [{ ...githubConnector, id: "conn-notion", name: "Notion", catalogSlug: "notion", acceptsPat: false, installUrl: "" }],
            skills: [],
          },
        }),
      );
      renderPage({ initialAgentId: "agent-1", openAuthorizeUrl: vi.fn() });

      const region = await screen.findByRole("region", { name: "Sales team" });
      expect(within(region).getByRole("button", { name: copy.connect })).toBeInTheDocument();
      expect(within(region).queryByRole("button", { name: copy.use_pat })).not.toBeInTheDocument();
      expect(within(region).queryByText(copy.install_hint)).not.toBeInTheDocument();
    });

    it("reopens the scope a connection started from and reports the outcome", async () => {
      api.getContextConfigAgent.mockResolvedValue(oauthDetail());
      renderPage({
        initialAgentId: "agent-1",
        initialScope: { scopeType: "person", scopeKey: "staff-1" },
        connectResult: { kind: "connected", slug: "github" },
      });

      expect(await screen.findByRole("region", { name: "Alice" })).toBeInTheDocument();
      expect(screen.getByText("GitHub is connected.")).toBeInTheDocument();
    });

    it("explains a cancelled provider sign-in", async () => {
      api.getContextConfigAgent.mockResolvedValue(oauthDetail());
      renderPage({ initialAgentId: "agent-1", connectResult: { kind: "error", code: "access_denied" } });

      expect(await screen.findByText(copy.returned_denied)).toBeInTheDocument();
    });

    it("explains a sign-in that came back in another browser", async () => {
      api.getContextConfigAgent.mockResolvedValue(oauthDetail());
      renderPage({ initialAgentId: "agent-1", connectResult: { kind: "error", code: "browser_mismatch" } });

      expect(await screen.findByText(copy.returned_browser_mismatch)).toBeInTheDocument();
    });

    it("offers only the token when the server cannot run the app's sign-in", async () => {
      api.getContextConfigAgent.mockResolvedValue(
        oauthDetail({ offers: { connectors: [{ ...githubConnector, oauthAvailable: false }], skills: [] } }),
      );
      renderPage({ initialAgentId: "agent-1", openAuthorizeUrl: vi.fn() });

      const region = await screen.findByRole("region", { name: "Sales team" });
      expect(within(region).queryByRole("button", { name: copy.connect })).not.toBeInTheDocument();
      expect(within(region).getByRole("button", { name: copy.pat_connect })).toBeInTheDocument();
      expect(
        within(region).getByText("Sign-in for GitHub isn't available on this server. You can use a Personal Access Token instead."),
      ).toBeInTheDocument();
    });

    it("reports sign-in errors that a retry cannot fix", async () => {
      const { toast } = await import("sonner");
      api.getContextConfigAgent.mockResolvedValue(oauthDetail());
      api.startContextConnectorConnection
        .mockRejectedValueOnce(
          new ApiError("OAuth is not configured for this app", 503, "", { code: "oauth_unavailable" }),
        )
        .mockRejectedValueOnce(new ApiError("not allowed", 403, "", { code: "forbidden" }));
      const openAuthorizeUrl = vi.fn();
      const user = userEvent.setup();
      renderPage({ initialAgentId: "agent-1", openAuthorizeUrl });

      const region = await screen.findByRole("region", { name: "Sales team" });
      await user.click(within(region).getByRole("button", { name: copy.connect }));
      await waitFor(() =>
        expect(toast.error).toHaveBeenCalledWith(
          "Sign-in for GitHub isn't available on this server. You can use a Personal Access Token instead.",
        ),
      );
      await user.click(within(region).getByRole("button", { name: copy.connect }));
      await waitFor(() => expect(toast.error).toHaveBeenCalledWith(copy.connect_forbidden));
      expect(toast.error).not.toHaveBeenCalledWith(copy.connect_failed);
      expect(openAuthorizeUrl).not.toHaveBeenCalled();
    });
  });

  it("labels the chat kind of each scene and explains a 1:1 chat scene", async () => {
    api.getContextConfigAgent.mockResolvedValue(
      agentDetail({
        scenes: [
          { scopeKey: SALES_SCENE, scopeTitle: "Sales team", source: "agent_link", expiresAt: "", kind: "group", orgId: "" },
          { scopeKey: DM_SCENE, scopeTitle: "", source: "agent_link", expiresAt: "", kind: "dm", orgId: "" },
        ],
      }),
    );
    api.getContextConfigScene.mockImplementation(async (_agentId: string, sceneId: string) =>
      sceneId === DM_SCENE
        ? {
            scene: { scopeKey: DM_SCENE, scopeTitle: "", source: "agent_link", expiresAt: "", kind: "dm", orgId: "" },
            bindings: [],
            credentials: [],
            // A 1:1 chat is its own scene, never its person's scope.
            scope: { type: "scene", key: DM_SCENE, title: "" },
            canConnect: true,
            ...noContent,
          }
        : sceneDetail,
    );
    const user = userEvent.setup();
    renderPage({ initialAgentId: "agent-1" });

    expect(await screen.findByRole("tab", { name: copy.tab_scene })).toBeInTheDocument();
    const groupRegion = await screen.findByRole("region", { name: "Sales team" });
    expect(within(groupRegion).getByText(copy.kind_group)).toBeInTheDocument();
    expect(within(groupRegion).getByText(copy.scene_scope_hint)).toBeInTheDocument();

    const picker = screen.getByRole("combobox");
    expect(within(picker).getByRole("option", { name: `${copy.kind_dm} · ${copy.scene_untitled_dm}` })).toBeInTheDocument();
    await user.selectOptions(picker, DM_SCENE);

    const dmRegion = await screen.findByRole("region", { name: copy.scene_untitled_dm });
    expect(within(dmRegion).getByText(copy.kind_dm)).toBeInTheDocument();
    expect(within(dmRegion).getByText(copy.scene_scope_hint_dm)).toBeInTheDocument();
    expect(within(dmRegion).getByText(copy.credential_required)).toBeInTheDocument();
    // An older backend without rights that lets the caller connect: the
    // chat's own token is set here.
    expect(within(dmRegion).getByRole("button", { name: copy.set_credential })).toBeInTheDocument();
    expect(within(dmRegion).queryByText(copy.owner_connects)).not.toBeInTheDocument();
    expect(api.getContextConfigScene).toHaveBeenCalledWith("agent-1", DM_SCENE, "");
  });

  it("writes a 1:1 chat's switches to the chat's own scene, with the manager's full rights", async () => {
    api.getContextConfigAgent.mockResolvedValue(
      agentDetail({
        access: "manager",
        scenes: [{ scopeKey: DM_SCENE, scopeTitle: "Bob", source: "manager", expiresAt: "", kind: "dm", orgId: "" }],
      }),
    );
    api.getContextConfigScene.mockResolvedValue({
      scene: { scopeKey: DM_SCENE, scopeTitle: "Bob", source: "manager", expiresAt: "", kind: "dm", orgId: "" },
      bindings: [],
      credentials: [],
      scope: { type: "scene", key: DM_SCENE, title: "Bob" },
      canConnect: true,
      ...noContent,
      rights: allRights,
    });
    const user = userEvent.setup();
    renderPage({ initialAgentId: "agent-1" });

    const region = await screen.findByRole("region", { name: "Bob" });
    expect(within(region).getByText(copy.kind_dm)).toBeInTheDocument();
    expect(within(region).getByText(copy.scene_scope_hint_dm)).toBeInTheDocument();
    expect(within(region).queryByText(copy.owner_connects)).not.toBeInTheDocument();
    expect(within(region).getByRole("button", { name: copy.set_credential })).toBeInTheDocument();
    await user.click(within(region).getByRole("switch", { name: "Turn Wiki on or off" }));

    await waitFor(() =>
      expect(api.setContextCapabilityBinding).toHaveBeenCalledWith("agent-1", {
        scopeType: "scene",
        scopeKey: DM_SCENE,
        resourceType: "connector",
        resourceId: "conn-wiki",
        enabled: true,
      }),
    );
  });

  it("saves the group-chat switch of a personal connector and says it is not applied yet", async () => {
    const person = agentDetail().person!;
    api.getContextConfigAgent.mockResolvedValue(
      agentDetail({
        person: {
          ...person,
          bindings: [
            { resourceType: "connector", resourceId: "conn-wiki", enabled: true, shareInGroups: false },
            { resourceType: "skill", resourceId: "skill-report", enabled: true, shareInGroups: false },
          ],
        },
      }),
    );
    const user = userEvent.setup();
    renderPage({ initialAgentId: "agent-1" });

    await user.click(await screen.findByRole("tab", { name: copy.tab_person }));
    const region = await screen.findByRole("region", { name: "Alice" });
    // One switch per enabled personal connector; skills never get one.
    const share = within(region).getByRole("switch", { name: copy.share_in_groups });
    expect(share).not.toBeChecked();
    expect(within(region).getAllByRole("switch", { name: copy.share_in_groups })).toHaveLength(1);
    expect(within(region).getByText(copy.share_in_groups_hint)).toBeInTheDocument();
    // The runtime does not read the switch yet, so the page says so and the
    // person scope hint does not claim group chats are excluded.
    expect(within(region).getByText(copy.share_in_groups_pending_note)).toBeInTheDocument();
    expect(share).toHaveAccessibleDescription(
      `${copy.share_in_groups_hint} ${copy.share_in_groups_pending_note}`,
    );
    expect(within(region).getByText(copy.person_scope_hint)).toBeInTheDocument();

    await user.click(share);

    await waitFor(() =>
      expect(api.setContextCapabilityBinding).toHaveBeenCalledWith("agent-1", {
        scopeType: "person",
        scopeKey: "staff-1",
        resourceType: "connector",
        resourceId: "conn-wiki",
        enabled: true,
        shareInGroups: true,
      }),
    );
  });

  it("shows the group-chat switch only for enabled personal connectors", async () => {
    const user = userEvent.setup();
    renderPage({ initialAgentId: "agent-1" });

    // The scene editor never offers it.
    const sceneRegion = await screen.findByRole("region", { name: "Sales team" });
    expect(within(sceneRegion).queryByRole("switch", { name: copy.share_in_groups })).not.toBeInTheDocument();

    // A personal connector that is off has nothing to share.
    await user.click(screen.getByRole("tab", { name: copy.tab_person }));
    const region = await screen.findByRole("region", { name: "Alice" });
    expect(within(region).queryByRole("switch", { name: copy.share_in_groups })).not.toBeInTheDocument();
  });

  it("reflects a connector already shared with group chats", async () => {
    const person = agentDetail().person!;
    api.getContextConfigAgent.mockResolvedValue(
      agentDetail({
        person: {
          ...person,
          bindings: [{ resourceType: "connector", resourceId: "conn-wiki", enabled: true, shareInGroups: true }],
        },
      }),
    );
    const user = userEvent.setup();
    renderPage({ initialAgentId: "agent-1" });

    await user.click(await screen.findByRole("tab", { name: copy.tab_person }));
    const region = await screen.findByRole("region", { name: "Alice" });
    expect(within(region).getByRole("switch", { name: copy.share_in_groups })).toBeChecked();
  });

  it("uses a centered column that widens on desktop screens", async () => {
    renderPage({ initialAgentId: "agent-1" });
    await screen.findByRole("region", { name: "Sales team" });
    const column = screen.getByRole("main").firstElementChild;
    expect(column?.className).toContain("mx-auto");
    expect(column?.className).toContain("sm:max-w-2xl");
  });

  describe("agent managers", () => {
    const managed: ContextConfigAgentSummary = { ...agentSummary, access: "manager" };

    it("opens the admin configure link and configures every scene of a managed agent", async () => {
      api.listContextConfigAgents.mockResolvedValue([managed]);
      api.getContextConfigAgent.mockResolvedValue(
        agentDetail({
          person: null,
          access: "manager",
          scenes: [
            { scopeKey: SALES_SCENE, scopeTitle: "Sales team", source: "manager", expiresAt: "", kind: "group", orgId: "" },
            { scopeKey: DM_SCENE, scopeTitle: "Bob", source: "manager", expiresAt: "", kind: "dm", orgId: "" },
          ],
        }),
      );
      renderPage({ initialAgentId: "agent-1" });

      expect(await screen.findByText(copy.manager_hint)).toBeInTheDocument();
      expect(screen.getByText(copy.manager_badge)).toBeInTheDocument();
      expect(screen.queryByText(copy.no_access_title)).not.toBeInTheDocument();
      const picker = screen.getByRole("combobox");
      expect(within(picker).getByRole("option", { name: `${copy.kind_group} · Sales team` })).toBeInTheDocument();
      expect(within(picker).getByRole("option", { name: `${copy.kind_dm} · Bob` })).toBeInTheDocument();
      expect(await screen.findByRole("region", { name: "Sales team" })).toBeInTheDocument();
    });

    it("lets a manager connect a 1:1 chat's own account: nothing is left to its person", async () => {
      api.getContextConfigAgent.mockResolvedValue(
        oauthDetail({
          person: null,
          access: "manager",
          scenes: [{ scopeKey: DM_SCENE, scopeTitle: "Bob", source: "manager", expiresAt: "", kind: "dm", orgId: "" }],
        }),
      );
      api.getContextConfigScene.mockResolvedValue({
        scene: { scopeKey: DM_SCENE, scopeTitle: "Bob", source: "manager", expiresAt: "", kind: "dm", orgId: "" },
        bindings: [],
        credentials: [{ connectorId: "conn-github", hint: "@bob", updatedAt: "", kind: "oauth" }],
        scope: { type: "scene", key: DM_SCENE, title: "Bob" },
        canConnect: true,
        ...noContent,
        rights: allRights,
      });
      renderPage({ initialAgentId: "agent-1" });

      const region = await screen.findByRole("region", { name: "Bob" });
      expect(within(region).queryByText(copy.owner_connects)).not.toBeInTheDocument();
      expect(within(region).getByText("Connected @bob")).toBeInTheDocument();
      expect(within(region).getByRole("button", { name: copy.disconnect })).toBeInTheDocument();
      expect(within(region).getByRole("switch", { name: "Turn GitHub on or off" })).toBeInTheDocument();
    });

    it("hides connecting whenever the server says the caller may not connect", async () => {
      api.getContextConfigAgent.mockResolvedValue(
        oauthDetail({
          scenes: [{ scopeKey: DM_SCENE, scopeTitle: "Bob", source: "agent_link", expiresAt: "", kind: "dm", orgId: "" }],
        }),
      );
      api.getContextConfigScene.mockResolvedValue({
        scene: { scopeKey: DM_SCENE, scopeTitle: "Bob", source: "agent_link", expiresAt: "", kind: "dm", orgId: "" },
        bindings: [],
        credentials: [],
        scope: { type: "scene", key: DM_SCENE, title: "Bob" },
        canConnect: false,
      });
      renderPage({ initialAgentId: "agent-1" });

      const region = await screen.findByRole("region", { name: "Bob" });
      expect(within(region).getByText(copy.owner_connects)).toBeInTheDocument();
      expect(within(region).queryByRole("button", { name: copy.connect })).not.toBeInTheDocument();
    });

    it("takes manager access from the agent detail, even when the agent list fails", async () => {
      api.listContextConfigAgents.mockRejectedValue(new Error("list unavailable"));
      api.getContextConfigAgent.mockResolvedValue(agentDetail({ person: null, scenes: [], access: "manager" }));
      const user = userEvent.setup();
      renderPage({ initialAgentId: "agent-1" });

      expect(await screen.findByText(copy.manager_hint)).toBeInTheDocument();
      expect(screen.getByText(copy.scene_empty_manager_title)).toBeInTheDocument();
      await user.click(screen.getByRole("tab", { name: copy.tab_person }));
      expect(screen.getByText(copy.person_manager_note)).toBeInTheDocument();
    });

    it("does not show manager copy for a grant-only detail", async () => {
      // The list claims manager access, but the detail is authoritative.
      api.listContextConfigAgents.mockResolvedValue([managed]);
      api.getContextConfigAgent.mockResolvedValue(agentDetail());
      renderPage({ initialAgentId: "agent-1" });

      expect(await screen.findByRole("region", { name: "Sales team" })).toBeInTheDocument();
      expect(screen.queryByText(copy.manager_hint)).not.toBeInTheDocument();
    });

    it("lists managed agents in the picker with the manager label", async () => {
      api.listContextConfigAgents.mockResolvedValue([
        managed,
        { ...agentSummary, id: "agent-2", name: "Planner", scopes: [{ scopeType: "person", scopeKey: "staff-1", scopeTitle: "", source: "agent_link", expiresAt: "" }] },
      ]);
      renderPage();

      const helper = await screen.findByRole("button", { name: /Helper/ });
      expect(within(helper).getByText(copy.manager_badge)).toBeInTheDocument();
      expect(within(helper).getByText(copy.manager_all_scenes)).toBeInTheDocument();
      const planner = screen.getByRole("button", { name: /Planner/ });
      expect(within(planner).queryByText(copy.manager_badge)).not.toBeInTheDocument();
    });

    it("explains an empty scene list and that 我的 still needs a personal link", async () => {
      api.listContextConfigAgents.mockResolvedValue([managed]);
      api.getContextConfigAgent.mockResolvedValue(agentDetail({ person: null, scenes: [], access: "manager" }));
      const user = userEvent.setup();
      renderPage({ initialAgentId: "agent-1" });

      expect(await screen.findByText(copy.scene_empty_manager_title)).toBeInTheDocument();
      expect(screen.getByText(copy.scene_empty_manager_hint)).toBeInTheDocument();
      await user.click(screen.getByRole("tab", { name: copy.tab_person }));
      expect(screen.getByText(copy.person_empty_hint)).toBeInTheDocument();
      expect(screen.getByText(copy.person_manager_note)).toBeInTheDocument();
    });
  });

  describe("three levels", () => {
    const acme = { orgId: "dingA", name: "Acme", source: "identity" };
    const beta = { orgId: "dingB", name: "Beta", source: "created" };
    const orgLevel = {
      scopeKey: "dingA",
      scopeTitle: "Acme",
      bindings: [{ resourceType: "skill" as const, resourceId: "skill-report", enabled: true, shareInGroups: false }],
      credentials: [{ connectorId: "conn-wiki", hint: "", updatedAt: "", kind: "bearer" as const }],
      canEdit: false,
      ...noContent,
    };

    it("shows the enterprise level read-only to members", async () => {
      api.getContextConfigAgent.mockResolvedValue(
        agentDetail({ tenant: acme, tenants: [acme], org: orgLevel }),
      );
      const user = userEvent.setup();
      renderPage({ initialAgentId: "agent-1" });

      await user.click(await screen.findByRole("tab", { name: copy.tab_org }));
      const region = await screen.findByRole("region", { name: "Acme" });
      expect(within(region).getByText(copy.org_read_only)).toBeInTheDocument();
      expect(within(region).getByRole("switch", { name: "Turn Weekly report on or off" })).toBeChecked();
      // Base UI marks a disabled switch with aria-disabled.
      for (const toggle of within(region).getAllByRole("switch")) {
        expect(toggle).toHaveAttribute("aria-disabled", "true");
      }
      // A stored enterprise token shows, but nothing can be changed.
      expect(within(region).getByText(copy.credential_set.replace("{{hint}}", "••••"))).toBeInTheDocument();
      expect(within(region).queryByRole("button")).not.toBeInTheDocument();
    });

    it("says when there is no enterprise level to show", async () => {
      const user = userEvent.setup();
      renderPage({ initialAgentId: "agent-1" });

      await user.click(await screen.findByRole("tab", { name: copy.tab_org }));
      expect(await screen.findByText(copy.org_unavailable)).toBeInTheDocument();
    });

    it("lets a manager switch and store tokens at the enterprise level", async () => {
      api.getContextConfigAgent.mockResolvedValue(
        agentDetail({
          person: null,
          access: "manager",
          tenant: acme,
          tenants: [acme],
          org: { ...orgLevel, bindings: [], credentials: [], canEdit: true },
        }),
      );
      api.setContextConnectorCredential.mockResolvedValue({
        connectorId: "conn-wiki",
        hint: "••••cret",
        updatedAt: "",
        kind: "bearer",
      });
      const user = userEvent.setup();
      renderPage({ initialAgentId: "agent-1" });

      await user.click(await screen.findByRole("tab", { name: copy.tab_org }));
      const region = await screen.findByRole("region", { name: "Acme" });
      expect(within(region).getByText(copy.org_scope_hint)).toBeInTheDocument();
      await user.click(within(region).getByRole("switch", { name: "Turn Weekly report on or off" }));
      await waitFor(() =>
        expect(api.setContextCapabilityBinding).toHaveBeenCalledWith("agent-1", {
          scopeType: "org",
          scopeKey: "dingA",
          orgId: "dingA",
          resourceType: "skill",
          resourceId: "skill-report",
          enabled: true,
        }),
      );

      await user.click(within(region).getByRole("button", { name: copy.set_credential }));
      expect(within(region).getByText(copy.credential_org_note)).toBeInTheDocument();
      await user.type(within(region).getByLabelText("Bearer token for Wiki"), "secret");
      await user.click(within(region).getByRole("button", { name: copy.save }));
      await waitFor(() =>
        expect(api.setContextConnectorCredential).toHaveBeenCalledWith("agent-1", {
          scopeType: "org",
          scopeKey: "dingA",
          orgId: "dingA",
          connectorId: "conn-wiki",
          bearer: "secret",
        }),
      );
    });

    it("reopens the enterprise level of a tenant and connects an app there", async () => {
      api.getContextConfigAgent.mockResolvedValue(
        agentDetail({
          person: null,
          access: "manager",
          tenant: beta,
          tenants: [acme, beta],
          org: { ...orgLevel, scopeKey: "dingB", scopeTitle: "Beta", bindings: [], credentials: [], canEdit: true },
          offers: {
            connectors: [...agentDetail().offers.connectors, githubConnector],
            skills: agentDetail().offers.skills,
          },
        }),
      );
      api.startContextConnectorConnection.mockResolvedValue("https://github.com/login/oauth/authorize?state=y");
      const openAuthorizeUrl = vi.fn();
      const user = userEvent.setup();
      renderPage({
        initialAgentId: "agent-1",
        initialScope: { scopeType: "org", scopeKey: "dingB", orgId: "dingB" },
        openAuthorizeUrl,
      });

      const region = await screen.findByRole("region", { name: "Beta" });
      expect(api.getContextConfigAgent).toHaveBeenCalledWith("agent-1", "dingB");
      expect(screen.getByRole("tab", { name: copy.tab_org })).toHaveAttribute("aria-selected", "true");

      await user.click(within(region).getByRole("button", { name: copy.connect }));
      await waitFor(() =>
        expect(openAuthorizeUrl).toHaveBeenCalledWith("https://github.com/login/oauth/authorize?state=y", {
          agentId: "agent-1",
          scopeType: "org",
          scopeKey: "dingB",
          orgId: "dingB",
        }),
      );
      expect(api.startContextConnectorConnection).toHaveBeenCalledWith("agent-1", {
        scopeType: "org",
        scopeKey: "dingB",
        orgId: "dingB",
        connectorId: "conn-github",
      });
    });

    it("marks what the enterprise turned on in a chat", async () => {
      api.getContextConfigAgent.mockResolvedValue(
        agentDetail({ tenant: acme, tenants: [acme], org: orgLevel }),
      );
      renderPage({ initialAgentId: "agent-1" });

      const region = await screen.findByRole("region", { name: "Sales team" });
      const skill = within(region).getByText("Weekly report").closest("li");
      expect(skill).not.toBeNull();
      expect(within(skill as HTMLElement).getByText(copy.on_for_org)).toBeInTheDocument();
      // The chat's own switch stays its own.
      expect(within(skill as HTMLElement).getByRole("switch")).not.toBeChecked();
    });

    it("switches the tenant and sends it with every request there", async () => {
      api.getContextConfigAgent.mockImplementation(async (_agentId: string, orgId = "") =>
        orgId === "dingB"
          ? agentDetail({
              tenant: beta,
              tenants: [acme, beta],
              scenes: [
                { scopeKey: PARTNER_SCENE, scopeTitle: "Partner", source: "agent_link", expiresAt: "", kind: "group", orgId: "dingB" },
              ],
            })
          : agentDetail({ tenant: acme, tenants: [acme, beta] }),
      );
      api.getContextConfigScene.mockImplementation(async (_agentId: string, sceneKey: string) =>
        sceneKey === PARTNER_SCENE
          ? {
              ...sceneDetail,
              scene: { scopeKey: PARTNER_SCENE, scopeTitle: "Partner", source: "agent_link", expiresAt: "", kind: "group", orgId: "dingB" },
              scope: { type: "scene", key: PARTNER_SCENE, title: "Partner" },
              bindings: [],
            }
          : sceneDetail,
      );
      const user = userEvent.setup();
      renderPage({ initialAgentId: "agent-1" });

      await screen.findByRole("region", { name: "Sales team" });
      expect(api.getContextConfigAgent).toHaveBeenCalledWith("agent-1", "");
      const picker = screen.getByRole("combobox", { name: copy.tab_org });
      expect(picker).toHaveValue("dingA");
      await user.selectOptions(picker, "dingB");

      const region = await screen.findByRole("region", { name: "Partner" });
      expect(api.getContextConfigAgent).toHaveBeenCalledWith("agent-1", "dingB");
      expect(api.getContextConfigScene).toHaveBeenCalledWith("agent-1", PARTNER_SCENE, "dingB");
      await user.click(within(region).getByRole("switch", { name: "Turn Weekly report on or off" }));
      await waitFor(() =>
        expect(api.setContextCapabilityBinding).toHaveBeenCalledWith("agent-1", {
          scopeType: "scene",
          scopeKey: PARTNER_SCENE,
          orgId: "dingB",
          resourceType: "skill",
          resourceId: "skill-report",
          enabled: true,
        }),
      );
    });

    it("falls back to the server's tenant when the link's tenant is gone", async () => {
      api.redeemContextConfigLink.mockResolvedValue({
        agentId: "agent-1",
        workspaceId: "ws-1",
        scopeType: "scene",
        scopeKey: SALES_SCENE,
        scopeTitle: "Sales team",
        orgId: "dingGone",
      });
      api.getContextConfigAgent.mockImplementation(async (_agentId: string, orgId = "") => {
        if (orgId === "dingGone") throw new ApiError("tenant not found", 404);
        return agentDetail({ tenant: acme, tenants: [acme] });
      });
      renderPage({ linkToken: "group" });

      expect(await screen.findByRole("region", { name: "Sales team" })).toBeInTheDocument();
      expect(api.getContextConfigAgent).toHaveBeenCalledWith("agent-1", "dingGone");
      expect(api.getContextConfigAgent).toHaveBeenLastCalledWith("agent-1", "");
    });

    it("opens 我的 in the link's tenant after a personal link and switches, stores a token and connects there", async () => {
      api.redeemContextConfigLink.mockResolvedValue({
        agentId: "agent-1",
        workspaceId: "ws-1",
        scopeType: "person",
        scopeKey: "staff-1",
        scopeTitle: "Alice",
        orgId: "dingB",
      });
      api.getContextConfigAgent.mockResolvedValue(
        agentDetail({
          tenant: beta,
          tenants: [beta],
          offers: {
            connectors: [...agentDetail().offers.connectors, githubConnector],
            skills: agentDetail().offers.skills,
          },
        }),
      );
      api.setContextConnectorCredential.mockResolvedValue({
        connectorId: "conn-wiki",
        hint: "••••cret",
        updatedAt: "",
        kind: "bearer",
      });
      api.startContextConnectorConnection.mockResolvedValue("https://github.com/login/oauth/authorize?state=x");
      const openAuthorizeUrl = vi.fn();
      const user = userEvent.setup();
      renderPage({ linkToken: "personal", openAuthorizeUrl });

      const region = await screen.findByRole("region", { name: "Alice" });
      expect(api.getContextConfigAgent).toHaveBeenCalledWith("agent-1", "dingB");
      // Bound to the person: no other level to open.
      expect(screen.queryByRole("tab", { name: copy.tab_person })).not.toBeInTheDocument();

      await user.click(within(region).getByRole("switch", { name: "Turn Weekly report on or off" }));
      await waitFor(() =>
        expect(api.setContextCapabilityBinding).toHaveBeenCalledWith("agent-1", {
          scopeType: "person",
          scopeKey: "staff-1",
          orgId: "dingB",
          resourceType: "skill",
          resourceId: "skill-report",
          enabled: true,
        }),
      );

      await user.click(within(region).getByRole("button", { name: copy.set_credential }));
      await user.type(within(region).getByLabelText("Bearer token for Wiki"), "secret");
      await user.click(within(region).getByRole("button", { name: copy.save }));
      await waitFor(() =>
        expect(api.setContextConnectorCredential).toHaveBeenCalledWith("agent-1", {
          scopeType: "person",
          scopeKey: "staff-1",
          orgId: "dingB",
          connectorId: "conn-wiki",
          bearer: "secret",
        }),
      );

      await user.click(within(region).getByRole("button", { name: copy.connect }));
      await waitFor(() =>
        expect(openAuthorizeUrl).toHaveBeenCalledWith("https://github.com/login/oauth/authorize?state=x", {
          agentId: "agent-1",
          scopeType: "person",
          scopeKey: "staff-1",
          orgId: "dingB",
        }),
      );
      expect(api.startContextConnectorConnection).toHaveBeenCalledWith("agent-1", {
        scopeType: "person",
        scopeKey: "staff-1",
        orgId: "dingB",
        connectorId: "conn-github",
      });
    });
  });

  it("lets a person with several agents choose one", async () => {
    api.listContextConfigAgents.mockResolvedValue([
      agentSummary,
      { ...agentSummary, id: "agent-2", name: "Planner" },
    ]);
    const user = userEvent.setup();
    renderPage();

    await user.click(await screen.findByRole("button", { name: /Helper/ }));

    await waitFor(() => expect(api.getContextConfigAgent).toHaveBeenCalledWith("agent-1", ""));
    expect(await screen.findByRole("button", { name: copy.switch_agent })).toBeInTheDocument();
  });
});

const allRights = { toggle: true, connect: true, editPrompts: true, editMcp: true };
const noRights = { toggle: false, connect: false, editPrompts: false, editMcp: false };
const groupBinding: ContextConfigBinding = { agentId: "agent-1", scopeType: "scene", scopeKey: SALES_SCENE, orgId: "" };
const personBinding: ContextConfigBinding = { agentId: "agent-1", scopeType: "person", scopeKey: "staff-1", orgId: "" };
const toneprompt = { id: "p1", name: "Tone", order: 1, text: "Be brief.", enabled: true, updatedByName: "", updatedAt: "" };

function personDetail(overrides: Partial<NonNullable<ContextConfigAgentDetail["person"]>> = {}) {
  const detail = agentDetail();
  return agentDetail({ person: { ...detail.person!, ...overrides } });
}

describe("bound configuration page", () => {
  it("shows only the bound group: no agent, tenant, level, scene or group switchers", async () => {
    const acme = { orgId: "dingA", name: "Acme", source: "identity" };
    const beta = { orgId: "dingB", name: "Beta", source: "created" };
    api.getContextConfigAgent.mockResolvedValue(
      agentDetail({
        tenant: acme,
        tenants: [acme, beta],
        jsapiAvailable: true,
        scenes: [
          { scopeKey: SALES_SCENE, scopeTitle: "Sales team", source: "agent_link", expiresAt: "", kind: "group", orgId: "" },
          { scopeKey: OPS_SCENE, scopeTitle: "Ops", source: "agent_link", expiresAt: "", kind: "group", orgId: "" },
        ],
      }),
    );
    api.listContextConfigAgents.mockResolvedValue([agentSummary, { ...agentSummary, id: "agent-2", name: "Planner" }]);
    renderPage({ binding: groupBinding, pickGroup: vi.fn() });

    expect(await screen.findByRole("region", { name: "Sales team" })).toBeInTheDocument();
    expect(api.redeemContextConfigLink).not.toHaveBeenCalled();
    expect(api.listContextConfigAgents).not.toHaveBeenCalled();
    expect(api.getContextConfigAgent).toHaveBeenCalledWith("agent-1", "");
    for (const level of [copy.tab_org, copy.tab_scene, copy.tab_person]) {
      expect(screen.queryByRole("tab", { name: level })).not.toBeInTheDocument();
    }
    expect(screen.queryByRole("combobox")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: copy.switch_agent })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: copy.pick_group })).not.toBeInTheDocument();
    expect(screen.queryByRole("region", { name: "Ops" })).not.toBeInTheDocument();
  });

  it("binds a personal link to the person only", async () => {
    renderPage({ binding: personBinding });

    expect(await screen.findByRole("region", { name: "Alice" })).toBeInTheDocument();
    expect(api.getContextConfigScene).not.toHaveBeenCalled();
    expect(screen.queryByRole("region", { name: "Sales team" })).not.toBeInTheDocument();
  });

  it("opens no other person's scope", async () => {
    renderPage({ binding: { ...personBinding, scopeKey: "staff-2" } });

    expect(await screen.findByText(copy.scene_no_access)).toBeInTheDocument();
    expect(screen.queryByRole("region", { name: "Alice" })).not.toBeInTheDocument();
  });

  it("adds the enterprise level for the agent's managers only", async () => {
    const acme = { orgId: "dingA", name: "Acme", source: "identity" };
    const org = { scopeKey: "dingA", scopeTitle: "Acme", bindings: [], credentials: [], canEdit: true, ...noContent };
    api.getContextConfigAgent.mockResolvedValue(
      agentDetail({ access: "manager", tenant: acme, tenants: [acme], org: { ...org, rights: allRights } }),
    );
    renderPage({ binding: groupBinding });

    expect(await screen.findByRole("heading", { name: copy.org_section_title })).toBeInTheDocument();
    expect(screen.getByRole("region", { name: "Acme" })).toBeInTheDocument();
    expect(await screen.findByRole("region", { name: "Sales team" })).toBeInTheDocument();
  });

  it("never shows the enterprise level to a member", async () => {
    const acme = { orgId: "dingA", name: "Acme", source: "identity" };
    api.getContextConfigAgent.mockResolvedValue(
      agentDetail({
        tenant: acme,
        tenants: [acme],
        org: { scopeKey: "dingA", scopeTitle: "Acme", bindings: [], credentials: [], canEdit: false, ...noContent },
      }),
    );
    renderPage({ binding: groupBinding });

    expect(await screen.findByRole("region", { name: "Sales team" })).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: copy.org_section_title })).not.toBeInTheDocument();
    expect(screen.queryByRole("region", { name: "Acme" })).not.toBeInTheDocument();
  });

  it("lets a group link holder only view: the agent's managers maintain group settings", async () => {
    api.getContextConfigScene.mockResolvedValue({
      ...sceneDetail,
      rights: noRights,
      prompts: [toneprompt],
      mcpConfig: { mcpServers: { docs: { url: "https://mcp.example/docs" } } },
    });
    renderPage({ binding: groupBinding });

    const region = await screen.findByRole("region", { name: "Sales team" });
    expect(within(region).getByText(copy.scene_read_only)).toBeInTheDocument();
    for (const toggle of within(region).getAllByRole("switch")) {
      expect(toggle).toHaveAttribute("aria-disabled", "true");
    }
    // The stored account shows; nothing can be stored, removed or added.
    expect(within(region).getByText("Credential saved (••••abcd)")).toBeInTheDocument();
    expect(within(region).queryByRole("button")).not.toBeInTheDocument();
    expect(within(region).getByRole("listitem", { name: "Tone" })).toBeInTheDocument();
    expect(within(region).getByRole("listitem", { name: "docs" })).toBeInTheDocument();
  });

  it("lets a 1:1 chat link holder only view: the agent's managers maintain the chat", async () => {
    api.getContextConfigAgent.mockResolvedValue(
      agentDetail({
        scenes: [{ scopeKey: DM_SCENE, scopeTitle: "Bob", source: "agent_link", expiresAt: "", kind: "dm", orgId: "" }],
      }),
    );
    api.getContextConfigScene.mockResolvedValue({
      scene: { scopeKey: DM_SCENE, scopeTitle: "Bob", source: "agent_link", expiresAt: "", kind: "dm", orgId: "" },
      bindings: [],
      credentials: [],
      scope: { type: "scene", key: DM_SCENE, title: "Bob" },
      canConnect: false,
      ...noContent,
      rights: noRights,
    });
    renderPage({ binding: { ...groupBinding, scopeKey: DM_SCENE } });

    const region = await screen.findByRole("region", { name: "Bob" });
    expect(within(region).getByText(copy.scene_read_only_dm)).toBeInTheDocument();
    expect(within(region).queryByText(copy.person_read_only)).not.toBeInTheDocument();
    expect(within(region).queryByRole("button")).not.toBeInTheDocument();
    expect(api.getContextConfigScene).toHaveBeenCalledWith("agent-1", DM_SCENE, "");
  });

  it("returns a provider sign-in to the bound page", async () => {
    api.getContextConfigAgent.mockResolvedValue(oauthDetail({ person: { ...personDetail().person!, rights: allRights } }));
    api.startContextConnectorConnection.mockResolvedValue("https://github.com/login/oauth/authorize?state=b");
    const openAuthorizeUrl = vi.fn();
    const returnTo = "/dingtalk/configure?agent=agent-1&scope_type=person&scope_key=staff-1";
    const user = userEvent.setup();
    renderPage({ binding: personBinding, openAuthorizeUrl, connectReturnTo: returnTo });

    const region = await screen.findByRole("region", { name: "Alice" });
    await user.click(within(region).getByRole("button", { name: copy.connect }));
    await waitFor(() =>
      expect(api.startContextConnectorConnection).toHaveBeenCalledWith("agent-1", {
        scopeType: "person",
        scopeKey: "staff-1",
        connectorId: "conn-github",
        returnTo,
      }),
    );
  });
});

describe("top-level tabs", () => {
  it("opens 公开能力 from the tab parameter with common and published capabilities", async () => {
    api.getContextConfigAgent.mockResolvedValue(
      agentDetail({
        global: {
          connectors: [{ id: "conn-global", name: "Docs", catalogSlug: "" }],
          skills: [{ id: "skill-report", name: "Weekly report", description: "Writes reports" }],
        },
      }),
    );
    renderPage({ binding: groupBinding, initialTab: "public" });

    const common = await screen.findByRole("region", { name: copy.public_common_title });
    const published = screen.getByRole("region", { name: copy.public_org_title });
    expect(screen.getByRole("tab", { name: copy.tab_public })).toHaveAttribute("aria-selected", "true");
    expect(within(common).getByText("Docs")).toBeInTheDocument();
    expect(within(common).getByText("Weekly report")).toBeInTheDocument();
    // An offer that is also granted is a common capability, listed once.
    expect(within(published).getByText("Wiki")).toBeInTheDocument();
    expect(within(published).queryByText("Weekly report")).not.toBeInTheDocument();
    expect(within(published).queryByText("Docs")).not.toBeInTheDocument();
    // Read-only: no switches on this tab.
    expect(screen.queryByRole("switch")).not.toBeInTheDocument();
  });

  it("opens the default tab for an unknown id and reports switching", async () => {
    const onTabChange = vi.fn();
    const user = userEvent.setup();
    renderPage({ binding: groupBinding, initialTab: "routines", onTabChange });

    expect(await screen.findByRole("region", { name: "Sales team" })).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: copy.tab_scope })).toHaveAttribute("aria-selected", "true");
    await user.click(screen.getByRole("tab", { name: copy.tab_public }));
    expect(onTabChange).toHaveBeenLastCalledWith("public");
    expect(await screen.findByRole("region", { name: copy.public_common_title })).toBeInTheDocument();
    expect(screen.queryByRole("region", { name: "Sales team" })).not.toBeInTheDocument();
  });

  it("keeps the browse page's levels under 场域能力", async () => {
    const user = userEvent.setup();
    renderPage({ initialAgentId: "agent-1" });

    expect(await screen.findByRole("tab", { name: copy.tab_scene })).toBeInTheDocument();
    await user.click(screen.getByRole("tab", { name: copy.tab_public }));
    expect(screen.queryByRole("tab", { name: copy.tab_scene })).not.toBeInTheDocument();
    await user.click(screen.getByRole("tab", { name: copy.tab_scope }));
    expect(await screen.findByRole("region", { name: "Sales team" })).toBeInTheDocument();
  });
});

describe("scope prompts", () => {
  beforeEach(() => {
    api.getContextConfigScene.mockResolvedValue({ ...sceneDetail, rights: allRights, prompts: [toneprompt] });
    api.setContextConfigPrompts.mockResolvedValue([]);
  });

  it("adds a prompt after checking it, saving the whole list", async () => {
    const user = userEvent.setup();
    renderPage({ binding: groupBinding });

    const region = await screen.findByRole("region", { name: "Sales team" });
    await user.click(within(region).getByRole("button", { name: copy.prompt_add }));
    await user.click(within(region).getByRole("button", { name: copy.save }));
    expect(within(region).getByRole("alert")).toHaveTextContent(
      enAgents.tab_body.context_builder.prompt_name_required,
    );
    await user.type(within(region).getByLabelText(copy.prompt_name), "Tone");
    await user.type(within(region).getByLabelText(copy.prompt_text), "x");
    await user.click(within(region).getByRole("button", { name: copy.save }));
    expect(within(region).getByRole("alert")).toHaveTextContent(
      enAgents.tab_body.context_builder.prompt_name_duplicate,
    );
    expect(api.setContextConfigPrompts).not.toHaveBeenCalled();

    await user.clear(within(region).getByLabelText(copy.prompt_name));
    await user.type(within(region).getByLabelText(copy.prompt_name), "Format");
    await user.clear(within(region).getByLabelText(copy.prompt_text));
    await user.type(within(region).getByLabelText(copy.prompt_text), "Use lists.");
    await user.click(within(region).getByRole("button", { name: copy.save }));
    await waitFor(() =>
      expect(api.setContextConfigPrompts).toHaveBeenCalledWith("agent-1", { scopeType: "scene", scopeKey: SALES_SCENE }, [
        { name: "Tone", order: 1, text: "Be brief.", enabled: true },
        { name: "Format", order: 2, text: "Use lists.", enabled: true },
      ]),
    );
    await waitFor(() => expect(within(region).queryByLabelText(copy.prompt_name)).not.toBeInTheDocument());
  });

  it("switches, edits and deletes a prompt", async () => {
    const user = userEvent.setup();
    renderPage({ binding: groupBinding });

    const region = await screen.findByRole("region", { name: "Sales team" });
    const row = within(region).getByRole("listitem", { name: "Tone" });
    await user.click(within(row).getByRole("switch", { name: "Turn Tone on or off" }));
    await waitFor(() =>
      expect(api.setContextConfigPrompts).toHaveBeenLastCalledWith(
        "agent-1",
        { scopeType: "scene", scopeKey: SALES_SCENE },
        [{ name: "Tone", order: 1, text: "Be brief.", enabled: false }],
      ),
    );

    await user.click(
      await within(region).findByRole("button", { name: copy.prompt_edit.replace("{{name}}", "Tone") }),
    );
    const text = within(region).getByLabelText(copy.prompt_text);
    expect(text).toHaveValue("Be brief.");
    await user.clear(text);
    await user.type(text, "Be very brief.");
    await user.click(within(region).getByRole("button", { name: copy.save }));
    await waitFor(() =>
      expect(api.setContextConfigPrompts).toHaveBeenLastCalledWith(
        "agent-1",
        { scopeType: "scene", scopeKey: SALES_SCENE },
        [{ name: "Tone", order: 1, text: "Be very brief.", enabled: true }],
      ),
    );

    await user.click(
      await within(region).findByRole("button", { name: copy.prompt_delete.replace("{{name}}", "Tone") }),
    );
    const confirm = await screen.findByRole("alertdialog");
    await user.click(within(confirm).getByRole("button", { name: copy.prompt_delete.replace("{{name}}", "Tone") }));
    await waitFor(() =>
      expect(api.setContextConfigPrompts).toHaveBeenLastCalledWith(
        "agent-1",
        { scopeType: "scene", scopeKey: SALES_SCENE },
        [],
      ),
    );
  });

  it("offers no prompt or MCP editing on a backend without rights", async () => {
    api.getContextConfigScene.mockResolvedValue(sceneDetail);
    renderPage({ binding: groupBinding });

    const region = await screen.findByRole("region", { name: "Sales team" });
    expect(within(region).queryByText(copy.prompts_title)).not.toBeInTheDocument();
    expect(within(region).queryByText(copy.mcp_title)).not.toBeInTheDocument();
  });
});

describe("scope MCP servers", () => {
  beforeEach(() => {
    api.getContextConfigAgent.mockResolvedValue(personDetail({ rights: allRights }));
    api.setContextConfigMcpConfig.mockImplementation(
      async (_agentId: string, _scope: unknown, config: Record<string, unknown> | null) => config,
    );
  });

  it("adds a remote URL server only", async () => {
    const user = userEvent.setup();
    renderPage({ binding: personBinding });

    const region = await screen.findByRole("region", { name: "Alice" });
    await user.click(within(region).getByRole("button", { name: copy.mcp_add }));
    // A remote server only: no command, arguments or environment.
    expect(within(region).queryByLabelText(/command/i)).not.toBeInTheDocument();
    expect(within(region).getByText(copy.mcp_remote_only)).toBeInTheDocument();

    await user.type(within(region).getByLabelText(copy.mcp_name), "multica");
    await user.type(within(region).getByLabelText(copy.mcp_url), "https://mcp.example/docs");
    await user.click(within(region).getByRole("button", { name: copy.save }));
    expect(within(region).getByRole("alert")).toHaveTextContent(copy.mcp_name_reserved);

    await user.clear(within(region).getByLabelText(copy.mcp_name));
    await user.type(within(region).getByLabelText(copy.mcp_name), "docs");
    await user.clear(within(region).getByLabelText(copy.mcp_url));
    await user.type(within(region).getByLabelText(copy.mcp_url), "file:///usr/bin/server");
    await user.click(within(region).getByRole("button", { name: copy.save }));
    expect(within(region).getByRole("alert")).toHaveTextContent(copy.mcp_url_invalid);
    expect(api.setContextConfigMcpConfig).not.toHaveBeenCalled();

    await user.clear(within(region).getByLabelText(copy.mcp_url));
    await user.type(within(region).getByLabelText(copy.mcp_url), "https://mcp.example/docs");
    await user.click(within(region).getByRole("button", { name: copy.mcp_header_add }));
    await user.type(within(region).getByLabelText(copy.mcp_header_name), "Authorization");
    await user.type(within(region).getByLabelText(copy.mcp_header_value), "Bearer t");
    await user.click(within(region).getByRole("button", { name: copy.save }));
    await waitFor(() =>
      expect(api.setContextConfigMcpConfig).toHaveBeenCalledWith(
        "agent-1",
        { scopeType: "person", scopeKey: "staff-1" },
        {
          mcpServers: {
            docs: { type: "http", url: "https://mcp.example/docs", headers: { Authorization: "Bearer t" } },
          },
        },
      ),
    );
  });

  it("switches a server off and deletes it", async () => {
    api.getContextConfigAgent.mockResolvedValue(
      personDetail({ rights: allRights, mcpConfig: { mcpServers: { docs: { url: "https://mcp.example/docs" } } } }),
    );
    const user = userEvent.setup();
    renderPage({ binding: personBinding });

    const region = await screen.findByRole("region", { name: "Alice" });
    await user.click(within(region).getByRole("switch", { name: "Turn docs on or off" }));
    await waitFor(() =>
      expect(api.setContextConfigMcpConfig).toHaveBeenLastCalledWith(
        "agent-1",
        { scopeType: "person", scopeKey: "staff-1" },
        { mcpServers: { docs: { url: "https://mcp.example/docs", disabled: true } } },
      ),
    );

    await user.click(
      await within(region).findByRole("button", { name: copy.mcp_delete.replace("{{name}}", "docs") }),
    );
    const confirm = await screen.findByRole("alertdialog");
    await user.click(within(confirm).getByRole("button", { name: copy.mcp_delete.replace("{{name}}", "docs") }));
    await waitFor(() =>
      expect(api.setContextConfigMcpConfig).toHaveBeenLastCalledWith(
        "agent-1",
        { scopeType: "person", scopeKey: "staff-1" },
        { mcpServers: {} },
      ),
    );
  });

  it("lists a document holding a local server read-only", async () => {
    api.getContextConfigAgent.mockResolvedValue(
      personDetail({
        rights: allRights,
        mcpConfig: {
          mcpServers: { docs: { url: "https://mcp.example/docs" }, local: { command: "npx", args: ["server"] } },
        },
      }),
    );
    renderPage({ binding: personBinding });

    const region = await screen.findByRole("region", { name: "Alice" });
    expect(within(region).getByRole("listitem", { name: "local" })).toBeInTheDocument();
    expect(within(region).getByText(copy.mcp_locked)).toBeInTheDocument();
    expect(within(region).queryByRole("button", { name: copy.mcp_add })).not.toBeInTheDocument();
    expect(within(region).getByRole("switch", { name: "Turn docs on or off" })).toHaveAttribute(
      "aria-disabled",
      "true",
    );
  });
});
