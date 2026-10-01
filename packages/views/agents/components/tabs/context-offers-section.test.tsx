// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { AgentContextCapabilities } from "@multica/core/context-capabilities";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../../locales/en/common.json";
import enAgents from "../../../locales/en/agents.json";
import { ContextOffersSection, offerUsageByResource } from "./context-offers-section";

const mocks = vi.hoisted(() => ({
  get: vi.fn(),
  set: vi.fn(),
}));

vi.mock("@multica/core/api", () => ({
  api: {
    getAgentContextCapabilities: mocks.get,
    setAgentContextCapabilityOffers: mocks.set,
  },
}));
vi.mock("sonner", () => ({ toast: { error: vi.fn(), success: vi.fn() } }));

const copy = enAgents.tab_body.context_offers;

const body: AgentContextCapabilities = {
  enabled: true,
  library: {
    connectors: [
      { id: "conn-wiki", name: "Wiki", enabled: true, authMode: "bearer", catalogSlug: "" },
      { id: "conn-jira", name: "Jira", enabled: false, authMode: "oauth", catalogSlug: "github" },
    ],
    skills: [
      { id: "skill-report", name: "Weekly report", description: "Writes reports" },
      { id: "skill-triage", name: "Triage", description: "" },
    ],
  },
  offers: { connectorIds: ["conn-wiki"], skillIds: ["skill-report"] },
  orgs: [],
  scenes: [
    {
      scopeKey: "cid-1",
      scopeTitle: "Sales",
      bindings: [
        { resourceType: "connector", resourceId: "conn-wiki", enabled: true, shareInGroups: false },
        { resourceType: "skill", resourceId: "skill-report", enabled: true, shareInGroups: false },
      ],
      credentialCount: 0,
    },
    {
      scopeKey: "cid-2",
      scopeTitle: "Ops",
      bindings: [{ resourceType: "connector", resourceId: "conn-wiki", enabled: false, shareInGroups: false }],
      credentialCount: 0,
    },
  ],
  persons: [
    {
      scopeKey: "staff-1",
      scopeTitle: "Ada",
      bindings: [{ resourceType: "connector", resourceId: "conn-wiki", enabled: true, shareInGroups: true }],
      credentialCount: 1,
    },
  ],
  configureUrl: "https://app.example/dingtalk/configure?agent=agent-1",
};

function renderSection(props: Partial<React.ComponentProps<typeof ContextOffersSection>> = {}) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  render(
    <I18nProvider locale="en" resources={{ en: { common: enCommon, agents: enAgents } }}>
      <QueryClientProvider client={client}>
        <ContextOffersSection agentId="agent-1" wsId="ws-1" {...props} />
      </QueryClientProvider>
    </I18nProvider>,
  );
}

beforeEach(() => {
  vi.clearAllMocks();
  mocks.get.mockResolvedValue(body);
  mocks.set.mockImplementation(async (_ws: string, _agent: string, input: { connectorIds: string[]; skillIds: string[] }) => ({
    ...body,
    offers: input,
  }));
});

describe("offer usage", () => {
  it("counts scenes and people with an enabled binding per resource", () => {
    const usage = offerUsageByResource(body);
    expect(usage.get("connector:conn-wiki")).toEqual({ scenes: 1, people: 1 });
    expect(usage.get("skill:skill-report")).toEqual({ scenes: 1, people: 0 });
    expect(usage.get("connector:conn-jira")).toBeUndefined();
  });

  it("counts an enterprise level's switch as a scene", () => {
    const usage = offerUsageByResource({
      ...body,
      orgs: [
        {
          scopeKey: "ding1",
          scopeTitle: "",
          bindings: [{ resourceType: "skill", resourceId: "skill-triage", enabled: true, shareInGroups: false }],
          credentialCount: 0,
        },
      ],
    });
    expect(usage.get("skill:skill-triage")).toEqual({ scenes: 1, people: 0 });
    expect(usage.get("skill:skill-report")).toEqual({ scenes: 1, people: 0 });
  });
});

describe("ContextOffersSection", () => {
  it("lists the skill library with usage counts for offered skills, never connectors", async () => {
    renderSection();

    const report = await screen.findByRole("switch", { name: "Allow Weekly report in scenes and for people" });
    expect(report).toBeChecked();
    expect(screen.getByText("Scenes: 1 · People: 0")).toBeInTheDocument();
    expect(screen.getByRole("switch", { name: "Allow Triage in scenes and for people" })).not.toBeChecked();
    // Connector offers are switched per connector in the 连接器 tab.
    expect(screen.queryByText("Wiki")).not.toBeInTheDocument();
  });

  it("offers a skill at once and keeps the connector part unchanged", async () => {
    const user = userEvent.setup();
    renderSection();

    await user.click(await screen.findByRole("switch", { name: "Allow Triage in scenes and for people" }));

    await waitFor(() =>
      expect(mocks.set).toHaveBeenCalledWith("ws-1", "agent-1", {
        connectorIds: ["conn-wiki"],
        skillIds: ["skill-report", "skill-triage"],
      }),
    );
  });

  it("asks before taking away a skill that scenes or people use", async () => {
    const user = userEvent.setup();
    renderSection();

    await user.click(await screen.findByRole("switch", { name: "Allow Weekly report in scenes and for people" }));
    expect(mocks.set).not.toHaveBeenCalled();
    const dialog = await screen.findByRole("alertdialog");
    expect(within(dialog).getByText("It turns off at once wherever it is on (scenes: 1, people: 0).")).toBeInTheDocument();
    await user.click(within(dialog).getByRole("button", { name: copy.remove_confirm }));

    await waitFor(() =>
      expect(mocks.set).toHaveBeenCalledWith("ws-1", "agent-1", {
        connectorIds: ["conn-wiki"],
        skillIds: [],
      }),
    );
  });

  it("offers a retry when the catalog cannot load", async () => {
    mocks.get.mockRejectedValue(new Error("boom"));
    renderSection();

    expect(await screen.findByText(copy.load_failed)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: copy.retry })).toBeInTheDocument();
  });
});
