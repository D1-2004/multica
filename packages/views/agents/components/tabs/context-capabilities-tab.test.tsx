// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Agent } from "@multica/core/types";
import type { AgentContextCapabilities } from "@multica/core/context-capabilities";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../../locales/en/common.json";
import enAgents from "../../../locales/en/agents.json";

const TEST_RESOURCES = { en: { common: enCommon, agents: enAgents } };

const mockGet = vi.hoisted(() => vi.fn());
const mockSetOffers = vi.hoisted(() => vi.fn());
const mockCopyText = vi.hoisted(() => vi.fn());

vi.mock("@multica/core/api", () => ({
  api: {
    getAgentContextCapabilities: (...args: unknown[]) => mockGet(...args),
    setAgentContextCapabilityOffers: (...args: unknown[]) => mockSetOffers(...args),
  },
}));

vi.mock("@multica/ui/lib/clipboard", () => ({
  copyText: (...args: unknown[]) => mockCopyText(...args),
}));

vi.mock("sonner", () => ({
  toast: { error: vi.fn(), success: vi.fn() },
}));

import { ContextCapabilitiesTab } from "./context-capabilities-tab";

const agent = { id: "agent-1", name: "Helper" } as Agent;

function body(overrides: Partial<AgentContextCapabilities> = {}): AgentContextCapabilities {
  return {
    enabled: true,
    library: {
      connectors: [
        { id: "conn-wiki", name: "Wiki", enabled: true, authMode: "bearer" },
        { id: "conn-crm", name: "CRM", enabled: false, authMode: "none" },
      ],
      skills: [{ id: "skill-report", name: "Weekly report", description: "Writes reports" }],
    },
    offers: { connectorIds: ["conn-wiki"], skillIds: [] },
    scenes: [
      {
        scopeKey: "cid-1",
        scopeTitle: "Sales team",
        bindings: [{ resourceType: "connector", resourceId: "conn-wiki", enabled: true }],
        credentialCount: 1,
      },
    ],
    persons: [],
    configureUrl: "https://app.example/dingtalk/configure?agent=agent-1",
    ...overrides,
  };
}

function renderTab(props: { canEdit?: boolean; onDirtyChange?: (dirty: boolean) => void } = {}) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return render(
    <I18nProvider locale="en" resources={TEST_RESOURCES}>
      <QueryClientProvider client={queryClient}>
        <ContextCapabilitiesTab
          agent={agent}
          wsId="ws-1"
          canEdit={props.canEdit ?? true}
          onDirtyChange={props.onDirtyChange}
        />
      </QueryClientProvider>
    </I18nProvider>,
  );
}

const copy = enAgents.tab_body.context_capabilities;

beforeEach(() => {
  vi.clearAllMocks();
  mockGet.mockResolvedValue(body());
});

describe("ContextCapabilitiesTab", () => {
  it("loads the catalog for the pinned workspace and marks offered items", async () => {
    renderTab();

    const wiki = await screen.findByRole("button", { name: /Wiki/ });
    expect(mockGet).toHaveBeenCalledWith("ws-1", "agent-1");
    expect(wiki).toHaveAttribute("aria-pressed", "true");
    expect(screen.getByRole("button", { name: /CRM/ })).toHaveAttribute("aria-pressed", "false");
    expect(screen.getByText(copy.connector_disabled)).toBeInTheDocument();
    expect(screen.getByText("Sales team")).toBeInTheDocument();
    expect(screen.getByText("1 credential")).toBeInTheDocument();
  });

  it("labels each connector with how it authenticates, including official apps", async () => {
    mockGet.mockResolvedValue(
      body({
        library: {
          connectors: [
            { id: "conn-wiki", name: "Wiki", enabled: true, authMode: "bearer" },
            { id: "conn-crm", name: "CRM", enabled: true, authMode: "none" },
            { id: "conn-github", name: "GitHub", enabled: true, authMode: "oauth" },
          ],
          skills: [],
        },
      }),
    );
    renderTab();

    expect(await screen.findByRole("button", { name: /GitHub/ })).toHaveTextContent(copy.auth_oauth);
    expect(screen.getByRole("button", { name: /Wiki/ })).toHaveTextContent(copy.auth_bearer);
    expect(screen.getByRole("button", { name: /CRM/ })).toHaveTextContent(copy.auth_none);
  });

  it("saves the whole catalog, warns before removing offers, and reports dirtiness", async () => {
    const user = userEvent.setup();
    const onDirtyChange = vi.fn();
    mockSetOffers.mockResolvedValue(
      body({ offers: { connectorIds: ["conn-crm"], skillIds: ["skill-report"] } }),
    );
    renderTab({ onDirtyChange });

    await user.click(await screen.findByRole("button", { name: /Wiki/ }));
    expect(screen.getByText(copy.removal_warning)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /CRM/ }));
    await user.click(screen.getByRole("button", { name: /Weekly report/ }));
    expect(onDirtyChange).toHaveBeenLastCalledWith(true);

    await user.click(screen.getByRole("button", { name: copy.save }));

    await waitFor(() =>
      expect(mockSetOffers).toHaveBeenCalledWith("ws-1", "agent-1", {
        connectorIds: ["conn-crm"],
        skillIds: ["skill-report"],
      }),
    );
    await waitFor(() => expect(onDirtyChange).toHaveBeenLastCalledWith(false));
  });

  it("keeps the catalog read-only for viewers", async () => {
    renderTab({ canEdit: false });

    const wiki = await screen.findByRole("button", { name: /Wiki/ });
    expect(wiki).toBeDisabled();
    expect(screen.queryByRole("button", { name: copy.save })).not.toBeInTheDocument();
  });

  it("copies the mobile configuration link", async () => {
    const user = userEvent.setup();
    mockCopyText.mockResolvedValue(true);
    renderTab();

    await user.click(await screen.findByRole("button", { name: copy.copy }));

    expect(mockCopyText).toHaveBeenCalledWith(
      "https://app.example/dingtalk/configure?agent=agent-1",
    );
  });

  it("explains when the server has the feature turned off", async () => {
    mockGet.mockResolvedValue(body({ enabled: false }));
    renderTab();

    expect(await screen.findByText(copy.disabled)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Wiki/ })).not.toBeInTheDocument();
  });

  it("offers a retry when the tab body is unreadable", async () => {
    mockGet.mockResolvedValueOnce(null).mockResolvedValueOnce(body());
    const user = userEvent.setup();
    renderTab();

    await user.click(await screen.findByRole("button", { name: copy.retry }));

    expect(await screen.findByRole("button", { name: /Wiki/ })).toBeInTheDocument();
  });
});
