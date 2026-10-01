// @vitest-environment jsdom

import { describe, it, expect, vi, beforeEach } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { Agent, AgentRuntime, AgentSource } from "@multica/core/types";

import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../locales/en/common.json";
import enAgents from "../../locales/en/agents.json";
import { NavigationProvider, type NavigationAdapter } from "../../navigation";

const TEST_RESOURCES = { en: { common: enCommon, agents: enAgents } };

// AgentOverviewPane pulls in ActorIssuesPanel which in turn touches the api
// layer. The test only cares about which top-of-pane tab buttons render,
// not what each tab does, so we stub the heavy children.
vi.mock("./tabs/activity-tab", () => ({
  ActivityTab: () => <div>activity-tab</div>,
  AgentPerformanceSummary: () => <div>performance-summary</div>,
}));
vi.mock("./agent-overview-summary", () => ({
  AgentOverviewSummary: () => <div>agent-overview-summary</div>,
}));
vi.mock("./agent-access-settings", () => ({
  AgentAccessSettings: () => <div>agent-access-settings</div>,
}));
vi.mock("./tabs/instructions-tab", () => ({
  InstructionsTab: ({
    onDirtyChange,
  }: {
    onDirtyChange?: (dirty: boolean) => void;
  }) => (
    <button type="button" onClick={() => onDirtyChange?.(true)}>
      Mark instructions dirty
    </button>
  ),
}));
vi.mock("./tabs/okr-tab", () => ({
  OKRTab: () => <div>okr-tab</div>,
}));
vi.mock("./tabs/skills-tab", () => ({
  SkillsTab: () => <div>skills-tab</div>,
}));
vi.mock("./tabs/env-tab", () => ({
  EnvTab: () => <div>env-tab</div>,
}));
vi.mock("./tabs/custom-args-tab", () => ({
  CustomArgsTab: () => <div>custom-args-tab</div>,
}));
vi.mock("./tabs/connectors-tab", () => ({
  ConnectorsTab: ({ supportsOwnMcpConfig }: { supportsOwnMcpConfig?: boolean }) => (
    <div>connectors-tab own-mcp:{String(supportsOwnMcpConfig)}</div>
  ),
}));
vi.mock("./tabs/integrations-tab", () => ({
  IntegrationsTab: () => <div>integrations-tab</div>,
}));
vi.mock("./tabs/llm-trace-tab", () => ({
  LLMTraceTab: () => <div>llm-trace-tab</div>,
}));
vi.mock("./tabs/a2a-tab", () => ({
  A2ATab: () => <div>a2a-tab</div>,
}));
vi.mock("./tabs/dsh-plugins-tab", () => ({
  DshPluginsTab: () => <div>dsh-plugins-tab</div>,
}));
vi.mock("./tabs/dsh-home-tab", () => ({
  DshHomeTab: () => <div>dsh-home-tab</div>,
}));
vi.mock("../../common/actor-issues-panel", () => ({
  ActorIssuesPanel: () => <div>actor-issues-panel</div>,
}));
vi.mock("./tabs/coordinator-sessions-tab", () => ({ CoordinatorSessionsTab: () => <div>coordinator-sessions-tab</div> }));
vi.mock("./tabs/export-tab", () => ({ ExportTab: () => <div>export-tab</div> }));
vi.mock("./tabs/publish-tab", () => ({
  PublishTab: () => <div>publish-tab</div>,
}));
vi.mock("./tabs/scenes-tab", () => ({
  ScenesTab: () => <div>scenes-tab</div>,
}));
vi.mock("./tabs/digital-employee-tab", () => ({
  DigitalEmployeeTab: () => <div>digital-employee-tab</div>,
}));
vi.mock("./tabs/mcp-access-tab", () => ({
  AgentMCPAccessTab: () => <div>mcp-access-tab</div>,
}));

// The pane now reads workspace context to decide whether the Integrations
// tab is worth showing (it queries Lark installations to learn whether the
// deployment has the feature configured). Provide a stable workspace id and
// a listing query backed by a ref so each test can flip `configured`.
const larkListingRef = vi.hoisted(() => ({
  current: { installations: [] as unknown[], configured: false },
}));
const slackListingRef = vi.hoisted(() => ({
  current: { installations: [] as unknown[], configured: false },
}));
const dingtalkListingRef = vi.hoisted(() => ({
  current: { installations: [] as unknown[], configured: false },
}));
const wecomListingRef = vi.hoisted(() => ({
  current: { installations: [] as unknown[], configured: false },
}));
vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "ws-1",
}));
vi.mock("@multica/core/lark", () => ({
  larkInstallationsOptions: () => ({
    queryKey: ["lark", "installations"],
    queryFn: () => Promise.resolve(larkListingRef.current),
  }),
}));
vi.mock("@multica/core/slack", () => ({
  slackInstallationsOptions: () => ({
    queryKey: ["slack", "installations"],
    queryFn: () => Promise.resolve(slackListingRef.current),
  }),
}));
vi.mock("@multica/core/dingtalk", () => ({
  dingtalkInstallationsOptions: () => ({
    queryKey: ["dingtalk", "installations"],
    queryFn: () => Promise.resolve(dingtalkListingRef.current),
  }),
}));
vi.mock("@multica/core/wecom", () => ({
  wecomInstallationsOptions: () => ({
    queryKey: ["wecom", "installations"],
    queryFn: () => Promise.resolve(wecomListingRef.current),
  }),
}));

import { AgentOverviewPane } from "./agent-overview-pane";

vi.mock("./tabs/runner-tab", () => ({ RunnerTab: () => <div>Execution machine</div> }));

const baseAgent: Agent = {
  id: "agent-1",
  workspace_id: "ws-1",
  runtime_id: "runtime-1",
  name: "Agent",
  description: "",
  instructions: "",
  avatar_url: null,
  runtime_mode: "local",
  runtime_config: {},
  custom_args: [],
  visibility: "workspace",
  permission_mode: "public_to",
  invocation_targets: [{ target_type: "workspace", target_id: null }],
  status: "idle",
  max_concurrent_tasks: 1,
  model: "",
  owner_id: "user-1",
  skills: [],
  created_at: "2026-05-28T00:00:00Z",
  updated_at: "2026-05-28T00:00:00Z",
  archived_at: null,
  archived_by: null,
};

function makeRuntime(provider: string, capabilities?: string[]): AgentRuntime {
  return {
    id: "runtime-1",
    workspace_id: "ws-1",
    daemon_id: null,
    name: "Runtime",
    runtime_mode: "local",
    provider,
    launch_header: "",
    status: "online",
    device_info: "",
    metadata: capabilities ? { capabilities } : {},
    owner_id: null,
    visibility: "private",
    last_seen_at: null,
    created_at: "2026-05-28T00:00:00Z",
    updated_at: "2026-05-28T00:00:00Z",
  };
}

function renderPane(
  runtimes: AgentRuntime[],
  options: {
    currentUserId?: string;
    agent?: Agent;
    agentOverrides?: Partial<Agent>;
    canEdit?: boolean;
    initialView?: string;
    search?: Record<string, string>;
    source?: AgentSource;
    tagRole?: "template" | "employee";
    viewParam?: string;
  } = {},
) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  const navigation: NavigationAdapter = {
    push: vi.fn(),
    replace: vi.fn(),
    back: vi.fn(),
    pathname: "/acme/agents/agent-1",
    searchParams: new URLSearchParams({
      ...(options.initialView ? { view: options.initialView } : {}),
      ...options.search,
    }),
    getShareableUrl: (path) => path,
  };
  const tree = () => (
    <I18nProvider locale="en" resources={TEST_RESOURCES}>
      <NavigationProvider value={navigation}>
        <QueryClientProvider client={queryClient}>
          <AgentOverviewPane
            agent={
              options.agent ?? { ...baseAgent, ...options.agentOverrides }
            }
            runtime={runtimes[0] ?? null}
            owner={null}
            source={options.source}
            runtimes={runtimes}
            members={[]}
            onUpdate={vi.fn().mockResolvedValue(undefined)}
            canEdit={options.canEdit ?? true}
            canOperateDingTalkBinding
            dingTalkBindingPermissionLoading={false}
            currentUserId={options.currentUserId}
            tagRole={options.tagRole}
            viewParam={options.viewParam}
            renderTenantConfig={() => <div>tenant-config</div>}
          />
        </QueryClientProvider>
      </NavigationProvider>
    </I18nProvider>
  );
  const result = render(tree());
  return {
    ...result,
    navigation,
    rerenderPane: () => result.rerender(tree()),
  };
}

function openConfiguration() {
  fireEvent.click(screen.getByRole("tab", { name: /^Configuration$/i }));
}

beforeEach(() => {
  larkListingRef.current = { installations: [], configured: false };
  slackListingRef.current = { installations: [], configured: false };
  dingtalkListingRef.current = { installations: [], configured: false };
  wecomListingRef.current = { installations: [], configured: false };
});

describe("AgentOverviewPane primary navigation", () => {
  it("shows four plain-language destinations", () => {
    renderPane([makeRuntime("claude")]);
    expect(screen.getAllByRole("tab").map((tab) => tab.textContent)).toEqual([
      "Overview",
      "Work",
      "Scenes",
      "Configuration",
    ]);
  });

  it("opens scenes for someone who can manage the agent", () => {
    const { navigation } = renderPane([makeRuntime("claude")], { initialView: "scenes" });
    expect(screen.getByText("scenes-tab")).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: "Scenes" })).toHaveAttribute("aria-selected", "true");
    expect(navigation.replace).not.toHaveBeenCalled();
  });

  it("keeps inbound history readable for viewers without edit permission", () => {
    renderPane([makeRuntime("claude")], { canEdit: false, initialView: "scenes" });
    expect(screen.getByText("coordinator-sessions-tab")).toBeInTheDocument();
    expect(screen.queryByText("scenes-tab")).not.toBeInTheDocument();
  });

  it.each(["inbound", "memory"])("moves old ?view=%s links into scenes", (view) => {
    const { navigation } = renderPane([makeRuntime("claude")], { initialView: view });
    expect(screen.getByText("scenes-tab")).toBeInTheDocument();
    expect(navigation.replace).toHaveBeenCalledWith("/acme/agents/agent-1?view=scenes");
  });

  it("drops the selected scene when leaving the scenes section", () => {
    const { navigation } = renderPane([makeRuntime("claude")], {
      initialView: "scenes",
      search: { scene: "cid-1", scene_tab: "config" },
    });
    fireEvent.click(screen.getByRole("tab", { name: "Work" }));
    expect(navigation.replace).toHaveBeenCalledWith("/acme/agents/agent-1?view=work");
  });

  it("drops the open app page when leaving the connector tab", () => {
    const { navigation } = renderPane([makeRuntime("claude")], {
      initialView: "mcp_config",
      search: { app: "github" },
    });
    fireEvent.click(screen.getByRole("tab", { name: "Work" }));
    expect(navigation.replace).toHaveBeenCalledWith("/acme/agents/agent-1?view=work");
  });

  it("never carries a scene's app dialog into the connector tab", () => {
    const { navigation } = renderPane([makeRuntime("claude")], {
      initialView: "scenes",
      search: { scene: "cid-1", scene_tab: "config", app: "notion" },
    });
    openConfiguration();
    fireEvent.click(screen.getByRole("tab", { name: /^Connectors$/i }));
    const urls = vi.mocked(navigation.replace).mock.calls.map(([url]) => url);
    expect(urls.at(-1)).toBe("/acme/agents/agent-1?view=mcp_config");
    expect(urls.some((url) => url.includes("app="))).toBe(false);
  });
});

describe("AgentOverviewPane MCP tab visibility", () => {
  it.each([
    ["Claude", "claude"],
    ["Codex", "codex"],
    ["Cursor", "cursor"],
    ["Hermes", "hermes"],
    ["Kimi", "kimi"],
    ["Kiro", "kiro"],
    ["OpenCode", "opencode"],
    ["OpenClaw", "openclaw"],
  ])(
    "renders the MCP tab when the agent runs on the %s runtime",
    (_label, provider) => {
      renderPane([makeRuntime(provider)]);
      openConfiguration();
      expect(
        screen.getByRole("tab", { name: /^Connectors$/i }),
      ).toBeInTheDocument();
    },
  );

  it("keeps Connectors for runtimes that do not read mcp_config but hides their own MCP servers", () => {
    // Official apps, Aone FaaS grants and offers go through the server relay
    // and work on every runtime. Only the agent's own mcp_config servers
    // would be a silent no-op on e.g. Gemini, so only that block is hidden.
    renderPane([makeRuntime("gemini")]);
    openConfiguration();
    fireEvent.click(screen.getByRole("tab", { name: /^Connectors$/i }));
    expect(screen.getByText("connectors-tab own-mcp:false")).toBeInTheDocument();
  });

  it("shows the agent's own MCP servers only for Pi runtimes whose template declares the capability", () => {
    const { unmount } = renderPane([makeRuntime("pi", ["pi", "mcp"])]);
    openConfiguration();
    fireEvent.click(screen.getByRole("tab", { name: /^Connectors$/i }));
    expect(screen.getByText("connectors-tab own-mcp:true")).toBeInTheDocument();
    unmount();

    renderPane([makeRuntime("pi", ["pi", "dws"])]);
    openConfiguration();
    fireEvent.click(screen.getByRole("tab", { name: /^Connectors$/i }));
    expect(screen.getByText("connectors-tab own-mcp:false")).toBeInTheDocument();
  });

  it("keeps the MCP tab visible when the runtime row hasn't loaded yet", () => {
    // Empty runtimes[] mimics the brief window between the page mounting and
    // the runtimes query resolving. Hiding the tab would flicker it off and
    // then back on, which reads as a bug.
    renderPane([]);
    openConfiguration();
    expect(
      screen.getByRole("tab", { name: /^Connectors$/i }),
    ).toBeInTheDocument();
  });
});

describe("AgentOverviewPane connection visibility", () => {
  it("separates owner-only MCP access from bot connections", () => {
    renderPane([makeRuntime("claude")], { currentUserId: "user-1" });

    openConfiguration();

    expect(
      screen.queryByRole("tab", { name: /^Bot Connections$/i }),
    ).not.toBeInTheDocument();
    expect(
      screen.getByRole("tab", { name: /^MCP Access$/i }),
    ).toBeInTheDocument();
  });

  it("shows Bot Connections once the deployment has Lark configured", async () => {
    larkListingRef.current = { installations: [], configured: true };
    renderPane([makeRuntime("claude")]);
    openConfiguration();
    expect(
      await screen.findByRole("tab", { name: /^Bot Connections$/i }),
    ).toBeInTheDocument();
  });

  it("shows Bot Connections when only Slack is configured", async () => {
    // Regression: the tab gate must consider Slack too, not just Lark —
    // a Slack-only deployment was hiding the tab (and its bind entry).
    slackListingRef.current = { installations: [], configured: true };
    renderPane([makeRuntime("claude")]);
    openConfiguration();
    expect(
      await screen.findByRole("tab", { name: /^Bot Connections$/i }),
    ).toBeInTheDocument();
  });

  it("keeps Digital Employee separate when no bot connection exists", () => {
    renderPane([makeRuntime("claude")]);
    openConfiguration();
    expect(
      screen.queryByRole("tab", { name: /^Bot Connections$/i }),
    ).not.toBeInTheDocument();
    expect(
      screen.getByRole("heading", { name: /^Identity & Goals/i }),
    ).toBeInTheDocument();
  });

  it("hides Bot Connections when no bot platform is configured", () => {
    // Default refs are configured:false; the tab must not appear on
    // deployments without either integration, the common case.
    renderPane([makeRuntime("claude")]);
    openConfiguration();
    expect(
      screen.queryByRole("tab", { name: /^Bot Connections$/i }),
    ).not.toBeInTheDocument();
  });
});

describe("AgentOverviewPane Digital Employee tab", () => {
  it("opens Digital Employee as the default configuration page", () => {
    renderPane([makeRuntime("claude")]);

    openConfiguration();

    expect(
      screen.queryByRole("tab", { name: /^Identity$/i }),
    ).not.toBeInTheDocument();
    expect(
      screen.getByRole("tab", { name: /^Digital Employee$/i }),
    ).toHaveAttribute("aria-selected", "true");
  });

  it("migrates legacy identity links to Digital Employee", () => {
    const { navigation } = renderPane([makeRuntime("claude")], {
      initialView: "identity",
    });

    expect(screen.getByText("digital-employee-tab")).toBeInTheDocument();
    expect(navigation.replace).toHaveBeenCalledWith(
      "/acme/agents/agent-1?view=digital_employee",
    );
  });

  it("returns to the last configuration page during the same visit", () => {
    renderPane([makeRuntime("claude")]);
    openConfiguration();
    fireEvent.click(screen.getByRole("tab", { name: "Instructions" }));
    fireEvent.click(screen.getByRole("tab", { name: "Work" }));
    openConfiguration();

    expect(
      screen.getByRole("tab", { name: "Instructions" }),
    ).toHaveAttribute("aria-selected", "true");
  });

  it("protects unsaved configuration when the page closes", () => {
    renderPane([makeRuntime("claude")]);
    openConfiguration();
    fireEvent.click(screen.getByRole("tab", { name: "Instructions" }));
    fireEvent.click(
      screen.getByRole("button", { name: "Mark instructions dirty" }),
    );
    const event = new Event("beforeunload", { cancelable: true });

    window.dispatchEvent(event);

    expect(event.defaultPrevented).toBe(true);
  });

  it("protects unsaved configuration from browser history changes", () => {
    const { navigation, rerenderPane } = renderPane([makeRuntime("claude")]);
    openConfiguration();
    fireEvent.click(screen.getByRole("tab", { name: "Instructions" }));
    fireEvent.click(
      screen.getByRole("button", { name: "Mark instructions dirty" }),
    );

    navigation.searchParams.set("view", "work");
    rerenderPane();

    expect(
      screen.getByText("Discard unsaved changes?"),
    ).toBeInTheDocument();
    expect(navigation.replace).toHaveBeenCalledWith(
      "/acme/agents/agent-1?view=instructions",
    );
  });
});

describe("AgentOverviewPane Connectors tab", () => {
  it("replaces the group and personal capabilities tab", () => {
    renderPane([makeRuntime("claude")]);
    openConfiguration();
    expect(screen.getByRole("tab", { name: /^Connectors$/i })).toBeInTheDocument();
    expect(
      screen.queryByRole("tab", { name: /^Context capabilities$/i }),
    ).not.toBeInTheDocument();
  });

  it("sends old context capability links to the Connectors tab", () => {
    const { navigation } = renderPane([makeRuntime("claude")], {
      initialView: "context_capabilities",
    });
    expect(screen.getByText(/^connectors-tab/)).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: /^Connectors$/i })).toHaveAttribute(
      "aria-selected",
      "true",
    );
    expect(navigation.replace).toHaveBeenCalledWith("/acme/agents/agent-1?view=mcp_config");
  });
});

describe("AgentOverviewPane Settings navigation", () => {
  it("gives Access its own settings tab", () => {
    renderPane([makeRuntime("claude")]);
    openConfiguration();
    expect(screen.getByRole("tab", { name: /^Access$/i })).toBeInTheDocument();
  });

  it("shows LLM Trace only for cloud agents", () => {
    const { unmount } = renderPane([makeRuntime("hermes")]);
    openConfiguration();
    expect(
      screen.queryByRole("tab", { name: /^LLM Trace$/i }),
    ).not.toBeInTheDocument();
    unmount();

    renderPane([makeRuntime("hermes")], {
      agentOverrides: { runtime_mode: "cloud" },
    });
    openConfiguration();
    expect(
      screen.getByRole("tab", { name: /^LLM Trace$/i }),
    ).toBeInTheDocument();
  });
});

describe("AgentOverviewPane Environment tab visibility", () => {
  it("keeps My Computer as its own execution tab", () => {
    renderPane([makeRuntime("claude")]);
    openConfiguration();

    fireEvent.click(screen.getByRole("tab", { name: /^My Computer$/i }));
    expect(screen.getByText("Execution machine")).toBeInTheDocument();
  });

  it("shows the Environment tab to someone who can manage the agent", () => {
    renderPane([makeRuntime("claude")]);
    openConfiguration();
    expect(
      screen.getByRole("tab", { name: /^Environment$/i }),
    ).toBeInTheDocument();
  });

  it("hides the Environment tab from users who cannot manage the agent", () => {
    // The env endpoints admit the agent owner or a workspace owner/admin
    // (MUL-5438) — the rule `canEdit` already encodes. Anyone else who opens
    // the tab hits a guaranteed 403 on "Reveal & edit".
    renderPane([makeRuntime("claude")], { canEdit: false });
    openConfiguration();
    expect(
      screen.queryByRole("tab", { name: /^Environment$/i }),
    ).not.toBeInTheDocument();
  });

  it("shows A2A only to the agent owner", () => {
    const { unmount } = renderPane([makeRuntime("claude")], {
      currentUserId: "user-1",
    });
    openConfiguration();
    expect(screen.getByRole("tab", { name: /^A2A$/i })).toBeInTheDocument();
    unmount();

    renderPane([makeRuntime("claude")], { currentUserId: "user-2" });
    openConfiguration();
    expect(
      screen.queryByRole("tab", { name: /^A2A$/i }),
    ).not.toBeInTheDocument();
  });
});

describe("AgentOverviewPane Plugins tab visibility", () => {
  it("shows DSH configuration for a DSH agent running in the cloud", () => {
    renderPane([makeRuntime("dsh")], { agentOverrides: { runtime_mode: "cloud" } });
    openConfiguration();
    expect(
      screen.getByRole("tab", { name: /^DSH configuration$/i }),
    ).toBeInTheDocument();
  });

  it("still shows DSH configuration for a DSH agent on a local daemon", () => {
    // A local daemon does not load plugins, but hiding the tab would strand
    // whatever is already bound: invisible, unremovable, and live again the
    // moment the agent moves back to a cloud runtime. The tab stays and says
    // so instead — see the notice test in dsh-plugins-tab.test.tsx.
    renderPane([makeRuntime("dsh")], { agentOverrides: { runtime_mode: "local" } });
    openConfiguration();
    expect(
      screen.getByRole("tab", { name: /^DSH configuration$/i }),
    ).toBeInTheDocument();
  });

  it("hides DSH configuration for a cloud agent on any other provider", () => {
    renderPane([makeRuntime("claude")], { agentOverrides: { runtime_mode: "cloud" } });
    openConfiguration();
    expect(
      screen.queryByRole("tab", { name: /^DSH configuration$/i }),
    ).not.toBeInTheDocument();
  });
});

it("shows publishing in Configuration for a Git-created Agent", () => {
  renderPane([], { source: {
    agent_id: "agent-1", source_type: "git", connection_id: "installation",
    repository: "acme/agent", ref: "main", manifest_path: "dingtalk-agent.json",
    synced_commit_sha: "a".repeat(40), sync_status: "ready", last_sync_error: null,
    last_sync_attempt_at: null, last_synced_at: "", connected: true,
  } });
  openConfiguration();
  fireEvent.click(screen.getByRole("tab", { name: "Publish" }));
  expect(screen.getByText("publish-tab")).toBeDefined();
  const importedBindings = screen.getByText(enAgents.package_bindings.title, { selector: "summary" }).closest("details");
  expect(importedBindings).toBeInTheDocument();
  expect(importedBindings).not.toHaveAttribute("open");
});

it("shows separate export and publish sections on a manually created Agent", () => {
  renderPane([]);
  openConfiguration();
  expect(screen.getByRole("tab", { name: "Publish" })).toBeDefined();
  fireEvent.click(screen.getByRole("tab", { name: "Export" }));
  expect(screen.getByText("export-tab")).toBeDefined();
});


it.each([
  ["dsh", "cloud", "fc-e2b", true, true],
  ["dsh", "cloud", "asb", true, false],
  ["dsh", "local", "fc-e2b", true, false],
  ["hermes", "cloud", "fc-e2b", true, false],
  ["dsh", "cloud", "fc-e2b", false, false],
])("gates DSH Home for %s/%s/%s manage=%s", (provider, mode, kind, canEdit, visible) => {
  const runtime = { ...makeRuntime(provider), runtime_mode: mode as "local" | "cloud", metadata: { kind } };
  renderPane([runtime], { canEdit, agentOverrides: { runtime_mode: mode as "local" | "cloud" } });
  openConfiguration();
  expect(screen.queryByRole("tab", { name: "DSH Home" })).not.toBeInTheDocument();
  const dsh = screen.queryByRole("tab", { name: "DSH configuration" });
  if (provider === "dsh") {
    expect(dsh).toBeInTheDocument();
    fireEvent.click(dsh!);
    expect(screen.getByText("dsh-plugins-tab")).toBeInTheDocument();
  } else {
    expect(dsh).not.toBeInTheDocument();
  }
  expect(screen.queryByText("dsh-home-tab") != null).toBe(visible);
});

it("groups Home and plugins for the migrated FC metadata returned by preproduction", () => {
  const runtime = { ...makeRuntime("dsh"), runtime_mode: "cloud" as const, metadata: {
    kind: "cloud-sandbox", sandbox_backend: "aliyun_fc", provider: "dsh",
    template_id: "lk2nt02azvizzdjzjjxq", template: "lk2nt02azvizzdjzjjxq", template_channel: "stable",
  } };
  renderPane([runtime], { canEdit: true, agentOverrides: { runtime_mode: "cloud" } });
  openConfiguration();
  expect(screen.getAllByRole("tab", { name: "DSH configuration" })).toHaveLength(1);
  fireEvent.click(screen.getByRole("tab", { name: "DSH configuration" }));
  expect(screen.getByText("dsh-plugins-tab")).toBeInTheDocument();
  expect(screen.getByText("dsh-home-tab")).toBeDefined();
});

describe("AgentOverviewPane in the Tag", () => {
  const cloud = { ...makeRuntime("claude"), runtime_mode: "cloud" } as AgentRuntime;

  it("shows the template as its shared configuration only", () => {
    renderPane([cloud], { tagRole: "template", agentOverrides: { runtime_mode: "cloud" } });
    // No overview, work or scenes: the Tag is managed and configured.
    expect(screen.queryByRole("tablist", { name: enAgents.tabs.page_navigation_aria })).toBeNull();
    expect(screen.getByRole("heading", { level: 2, name: enAgents.tabs.instructions })).toBeTruthy();
    expect(screen.getAllByText(enAgents.tabs.skills).length).toBeGreaterThan(0);
    expect(screen.getAllByText(enAgents.tabs.mcp_config).length).toBeGreaterThan(0);
    expect(screen.queryByText(enAgents.tabs.llm_trace)).toBeNull();
  });

  it("frames the template's skills as the default access bundle", () => {
    renderPane([cloud], { tagRole: "template", initialView: "skills", agentOverrides: { runtime_mode: "cloud" } });
    expect(screen.getByText(enAgents.tag_tenant.bundle_title)).toBeTruthy();
  });

  it("gives an employee its tenant configuration, scenes and recent work", () => {
    const { navigation } = renderPane([cloud], {
      tagRole: "employee",
      viewParam: "tview",
      agentOverrides: { runtime_mode: "cloud" },
    });
    expect(screen.getAllByRole("tab").map((tab) => tab.textContent)).toEqual([
      enAgents.tag_tenant.tab_tenant_config,
      enAgents.tabs.scenes,
      enAgents.tag_tenant.tab_recent_work,
    ]);
    expect(screen.getByText("tenant-config")).toBeTruthy();

    fireEvent.click(screen.getByRole("tab", { name: enAgents.tag_tenant.tab_recent_work }));
    expect(screen.getByText("activity-tab")).toBeTruthy();
    // The tenant pane keeps its own view param next to the shared pane's.
    expect(navigation.replace).toHaveBeenLastCalledWith("/acme/agents/agent-1?tview=overview");
  });
});
