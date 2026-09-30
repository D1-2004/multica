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

import { ContextConfigPage, type ContextConfigPageProps } from "./context-config-page";

const copy = enAgents.context_config;

const agentSummary: ContextConfigAgentSummary = {
  id: "agent-1",
  name: "Helper",
  avatarUrl: null,
  workspaceId: "ws-1",
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
    },
    scenes: [{ scopeKey: "cid-1", scopeTitle: "Sales team", source: "agent_link", expiresAt: "", kind: "group" }],
    jsapiAvailable: false,
    ...overrides,
  };
}

const sceneDetail: ContextConfigSceneDetail = {
  scene: { scopeKey: "cid-1", scopeTitle: "Sales team", source: "agent_link", expiresAt: "", kind: "group" },
  bindings: [{ resourceType: "connector", resourceId: "conn-wiki", enabled: true, shareInGroups: false }],
  credentials: [{ connectorId: "conn-wiki", hint: "••••abcd", updatedAt: "", kind: "bearer" }],
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
      scopeKey: "cid-1",
      scopeTitle: "Sales team",
    });
    const user = userEvent.setup();
    renderPage({ linkToken: "link-token" });

    expect(await screen.findByRole("region", { name: "Sales team" })).toBeInTheDocument();
    expect(api.redeemContextConfigLink).toHaveBeenCalledTimes(1);
    expect(api.redeemContextConfigLink).toHaveBeenCalledWith("link-token");
    expect(api.getContextConfigScene).toHaveBeenCalledWith("agent-1", "cid-1");

    const wikiToggle = screen.getByRole("switch", { name: "Turn Wiki on or off" });
    expect(wikiToggle).toBeChecked();
    expect(screen.getByText("Credential saved (••••abcd)")).toBeInTheDocument();
    // Globally granted items are listed read-only.
    expect(screen.getByText("Docs")).toBeInTheDocument();

    await user.click(screen.getByRole("switch", { name: "Turn Weekly report on or off" }));

    await waitFor(() =>
      expect(api.setContextCapabilityBinding).toHaveBeenCalledWith("agent-1", {
        scopeType: "scene",
        scopeKey: "cid-1",
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
        scopeKey: "cid-1",
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
      scopeKey: "cid-2",
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

    await waitFor(() => expect(api.getContextConfigAgent).toHaveBeenCalledWith("agent-2"));
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
          { agentId: "agent-1", scopeType: "scene", scopeKey: "cid-1" },
        ),
      );
      expect(api.startContextConnectorConnection).toHaveBeenCalledWith("agent-1", {
        scopeType: "scene",
        scopeKey: "cid-1",
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
          { scopeKey: "cid-1", scopeTitle: "Sales team", source: "agent_link", expiresAt: "", kind: "group" },
          { scopeKey: "cid-dm", scopeTitle: "", source: "agent_link", expiresAt: "", kind: "dm" },
        ],
      }),
    );
    api.getContextConfigScene.mockImplementation(async (_agentId: string, sceneKey: string) =>
      sceneKey === "cid-dm"
        ? {
            scene: { scopeKey: "cid-dm", scopeTitle: "", source: "agent_link", expiresAt: "", kind: "dm" },
            bindings: [],
            credentials: [],
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
    await user.selectOptions(picker, "cid-dm");

    const dmRegion = await screen.findByRole("region", { name: copy.scene_untitled_dm });
    expect(within(dmRegion).getByText(copy.kind_dm)).toBeInTheDocument();
    expect(within(dmRegion).getByText(copy.scene_scope_hint_dm)).toBeInTheDocument();
    expect(within(dmRegion).getByText(copy.credential_required)).toBeInTheDocument();
    // A 1:1 chat scene is stored only this round; the page must not promise
    // runtime effect.
    expect(within(dmRegion).getByText(copy.dm_pending_note)).toBeInTheDocument();
    expect(within(groupRegion).queryByText(copy.dm_pending_note)).not.toBeInTheDocument();
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

  it("lets a person with several agents choose one", async () => {
    api.listContextConfigAgents.mockResolvedValue([
      agentSummary,
      { ...agentSummary, id: "agent-2", name: "Planner" },
    ]);
    const user = userEvent.setup();
    renderPage();

    await user.click(await screen.findByRole("button", { name: /Helper/ }));

    await waitFor(() => expect(api.getContextConfigAgent).toHaveBeenCalledWith("agent-1"));
    expect(await screen.findByRole("button", { name: copy.switch_agent })).toBeInTheDocument();
  });
});
