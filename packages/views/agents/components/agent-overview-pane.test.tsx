// @vitest-environment jsdom

import { describe, it, expect, vi, beforeEach } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { Agent, AgentRuntime } from "@multica/core/types";
import { configStore } from "@multica/core/config";
import { AGENT_A2A_INBOUND_FLAG } from "@multica/core/feature-flags";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../locales/en/common.json";
import enAgents from "../../locales/en/agents.json";
import {
  NavigationProvider,
  type NavigationAdapter,
} from "../../navigation";

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
  InstructionsTab: () => <div>instructions-tab</div>,
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
vi.mock("./tabs/mcp-config-tab", () => ({
  McpConfigTab: () => <div>mcp-config-tab</div>,
}));
vi.mock("./tabs/integrations-tab", () => ({
  IntegrationsTab: () => <div>integrations-tab</div>,
}));
vi.mock("./tabs/identity-tab", () => ({
  IdentityTab: () => <div>identity-tab</div>,
}));
vi.mock("./tabs/llm-trace-tab", () => ({
  LLMTraceTab: () => <div>llm-trace-tab</div>,
}));
vi.mock("./tabs/a2a-tab", () => ({
  A2ATab: () => <div>a2a-tab</div>,
}));
vi.mock("../../common/actor-issues-panel", () => ({
  ActorIssuesPanel: () => <div>actor-issues-panel</div>,
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
const dingtalkAccountListingRef = vi.hoisted(() => ({
  current: { bindings: [] as unknown[], configured: false },
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
vi.mock("@multica/core/dingtalk-account-bindings", () => ({
  dingtalkAccountBindingsOptions: () => ({
    queryKey: ["dingtalk-account-bindings", "list"],
    queryFn: () => Promise.resolve(dingtalkAccountListingRef.current),
  }),
}));

import { AgentOverviewPane } from "./agent-overview-pane";

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
    searchParams: new URLSearchParams(),
    getShareableUrl: (path) => path,
  };
  return render(
    <I18nProvider locale="en" resources={TEST_RESOURCES}>
      <NavigationProvider value={navigation}>
        <QueryClientProvider client={queryClient}>
          <AgentOverviewPane
            agent={
              options.agent ?? { ...baseAgent, ...options.agentOverrides }
            }
            runtime={runtimes[0] ?? null}
            owner={null}
            runtimes={runtimes}
            members={[]}
            onUpdate={vi.fn().mockResolvedValue(undefined)}
            canEdit
            canOperateDingTalkBinding
            dingTalkBindingPermissionLoading={false}
            currentUserId={options.currentUserId}
          />
        </QueryClientProvider>
      </NavigationProvider>
    </I18nProvider>,
  );
}

function openCapabilities() {
  fireEvent.click(screen.getByRole("tab", { name: /^Capabilities$/i }));
}

function openSettings() {
  fireEvent.click(screen.getByRole("tab", { name: /^Settings$/i }));
}

beforeEach(() => {
  larkListingRef.current = { installations: [], configured: false };
  slackListingRef.current = { installations: [], configured: false };
  dingtalkListingRef.current = { installations: [], configured: false };
  dingtalkAccountListingRef.current = { bindings: [], configured: false };
  configStore.getState().setFeatureFlags({ [AGENT_A2A_INBOUND_FLAG]: false });
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
  ])("renders the MCP tab when the agent runs on the %s runtime", (_label, provider) => {
    renderPane([makeRuntime(provider)]);
    openCapabilities();
    expect(screen.getByRole("tab", { name: /^MCP tools$/i })).toBeInTheDocument();
  });

	it("hides the MCP tab for providers whose backend does not read mcp_config", () => {
    // Saving an MCP config on e.g. Gemini would be a silent no-op at run
    // time — that's the bug this hiding logic is meant to prevent.
    renderPane([makeRuntime("gemini")]);
    openCapabilities();
    expect(
      screen.queryByRole("tab", { name: /^MCP tools$/i }),
    ).not.toBeInTheDocument();
	});

	it("shows MCP only for Pi runtimes whose template declares the capability", () => {
		const { unmount } = renderPane([makeRuntime("pi", ["pi", "mcp"])]);
		openCapabilities();
		expect(screen.getByRole("tab", { name: /^MCP tools$/i })).toBeInTheDocument();
		unmount();

		renderPane([makeRuntime("pi", ["pi", "dws"])]);
		openCapabilities();
		expect(screen.queryByRole("tab", { name: /^MCP tools$/i })).not.toBeInTheDocument();
	});

  it("keeps the MCP tab visible when the runtime row hasn't loaded yet", () => {
    // Empty runtimes[] mimics the brief window between the page mounting and
    // the runtimes query resolving. Hiding the tab would flicker it off and
    // then back on, which reads as a bug.
    renderPane([]);
    openCapabilities();
    expect(screen.getByRole("tab", { name: /^MCP tools$/i })).toBeInTheDocument();
  });
});

describe("AgentOverviewPane Integrations tab visibility", () => {
  it("shows Integrations to the agent owner for MCP export even without channel integrations", () => {
    configStore.getState().setFeatureFlags({ [AGENT_A2A_INBOUND_FLAG]: true });
    renderPane([makeRuntime("claude")], { currentUserId: "user-1" });

    openCapabilities();

    expect(
      screen.getByRole("tab", { name: /^Integrations$/i }),
    ).toBeInTheDocument();
  });

  it("shows the Integrations tab once the deployment has Lark configured", async () => {
    larkListingRef.current = { installations: [], configured: true };
    renderPane([makeRuntime("claude")]);
    openCapabilities();
    expect(
      await screen.findByRole("tab", { name: /^Integrations$/i }),
    ).toBeInTheDocument();
  });

  it("shows the Integrations tab when only Slack is configured (Lark off)", async () => {
    // Regression: the tab gate must consider Slack too, not just Lark —
    // a Slack-only deployment was hiding the tab (and its bind entry).
    slackListingRef.current = { installations: [], configured: true };
    renderPane([makeRuntime("claude")]);
    openCapabilities();
    expect(
      await screen.findByRole("tab", { name: /^Integrations$/i }),
    ).toBeInTheDocument();
  });

  it("shows the Integrations tab when only enterprise digital employee binding is configured", async () => {
    dingtalkAccountListingRef.current = { bindings: [], configured: true };
    renderPane([makeRuntime("claude")]);
    openCapabilities();
    expect(
      await screen.findByRole("tab", { name: /^Integrations$/i }),
    ).toBeInTheDocument();
  });

  it("hides the Integrations tab when no integration is configured", () => {
    // Default refs are configured:false; the tab must not appear on
    // deployments without either integration, the common case.
    renderPane([makeRuntime("claude")]);
    openCapabilities();
    expect(
      screen.queryByRole("tab", { name: /^Integrations$/i }),
    ).not.toBeInTheDocument();
  });
});

describe("AgentOverviewPane Identity tab", () => {
  it("places Identity after Integrations and opens the identity-only page", async () => {
    dingtalkAccountListingRef.current = { bindings: [], configured: true };
    renderPane([makeRuntime("claude")]);

    openCapabilities();

    await screen.findByRole("tab", { name: /^Integrations$/i });
    const capabilityTabs = screen.getAllByRole("tab");
    expect(capabilityTabs.map((tab) => tab.textContent)).toEqual([
      "Overview",
      "Work",
      "Capabilities",
      "Settings",
      "Instructions",
      "Skills",
      "MCP tools",
      "Integrations",
      "Identity",
    ]);

    fireEvent.click(screen.getByRole("tab", { name: /^Identity$/i }));
    expect(screen.getByText("identity-tab")).toBeInTheDocument();
  });

  it("hides Identity when account binding is not configured", () => {
    renderPane([makeRuntime("claude")]);
    openCapabilities();

    expect(
      screen.queryByRole("tab", { name: /^Identity$/i }),
    ).not.toBeInTheDocument();
  });
});

describe("AgentOverviewPane Settings navigation", () => {
  it("gives Access its own settings tab", () => {
    renderPane([makeRuntime("claude")]);
    openSettings();
    expect(screen.getByRole("tab", { name: /^Access$/i })).toBeInTheDocument();
  });

  it("shows LLM Trace only for cloud agents", () => {
    const { unmount } = renderPane([makeRuntime("hermes")]);
    openSettings();
    expect(
      screen.queryByRole("tab", { name: /^LLM Trace$/i }),
    ).not.toBeInTheDocument();
    unmount();

    renderPane([makeRuntime("hermes")], {
      agentOverrides: { runtime_mode: "cloud" },
    });
    openSettings();
    expect(screen.getByRole("tab", { name: /^LLM Trace$/i })).toBeInTheDocument();
  });

  it("shows A2A only to the agent owner when the inbound flag is enabled", () => {
    configStore.getState().setFeatureFlags({ [AGENT_A2A_INBOUND_FLAG]: true });

    const { unmount } = renderPane([makeRuntime("claude")], {
      currentUserId: "user-1",
    });
    openSettings();
    expect(screen.getByRole("tab", { name: /^A2A$/i })).toBeInTheDocument();
    unmount();

    renderPane([makeRuntime("claude")], { currentUserId: "user-2" });
    openSettings();
    expect(
      screen.queryByRole("tab", { name: /^A2A$/i }),
    ).not.toBeInTheDocument();
  });

  it("hides A2A from the owner while the inbound flag is disabled", () => {
    renderPane([makeRuntime("claude")], { currentUserId: "user-1" });
    openSettings();
    expect(
      screen.queryByRole("tab", { name: /^A2A$/i }),
    ).not.toBeInTheDocument();
  });
});
