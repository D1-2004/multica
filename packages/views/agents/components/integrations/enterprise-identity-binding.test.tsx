// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { I18nProvider } from "@multica/core/i18n/react";
import { setApiInstance } from "@multica/core/api";
import type { ApiClient } from "@multica/core/api/client";
import enCommon from "../../../locales/en/common.json";
import enAgents from "../../../locales/en/agents.json";
import { EnterpriseIdentityBindingCard } from "./enterprise-identity-binding";

const getStatus = vi.fn();
const beginBinding = vi.fn();

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "workspace-1",
}));

const resources = { en: { common: enCommon, agents: enAgents } };

function renderCard(canManage: boolean) {
  const queryClient = new QueryClient({
    defaultOptions: {
      queries: { retry: false },
      mutations: { retry: false },
    },
  });
  return render(
    <EnterpriseIdentityBindingCard
      agentId="agent-1"
      canManage={canManage}
    />,
    {
      wrapper: ({ children }: { children: ReactNode }) => (
        <I18nProvider locale="en" resources={resources}>
          <QueryClientProvider client={queryClient}>
            {children}
          </QueryClientProvider>
        </I18nProvider>
      ),
    },
  );
}

beforeEach(() => {
  vi.clearAllMocks();
  window.history.replaceState(null, "", "/acme/agents/agent-1");
  setApiInstance({
    getAgentEnterpriseIdentityStatus: getStatus,
    beginAgentEnterpriseIdentityBinding: beginBinding,
    revokeAgentEnterpriseIdentity: vi.fn(),
  } as unknown as ApiClient);
});

describe("EnterpriseIdentityBindingCard", () => {
  it("shows non-sensitive status to a non-owner member without actions", async () => {
    getStatus.mockResolvedValue({
      configured: true,
      canManage: false,
      identity: {
        employeeId: "",
        displayName: "",
        status: "needs_reauth",
        aipId: "",
        agentSpiffeId: "",
        bucStatus: "needs_reauth",
        agentIdentityStatus: "needs_reauth",
      },
    });

    renderCard(false);

    expect(
      await screen.findByText(/Needs reauthorization/),
    ).toBeInTheDocument();
    expect(getStatus).toHaveBeenCalledWith("workspace-1", "agent-1");
    expect(screen.queryByText("Zhang San")).not.toBeInTheDocument();
    expect(screen.queryByText(/aip-1/i)).not.toBeInTheDocument();
    expect(screen.queryByRole("button")).not.toBeInTheDocument();
  });

  it("shows only the server-masked employee ID and management actions to an owner", async () => {
    getStatus.mockResolvedValue({
      configured: true,
      canManage: true,
      identity: {
        employeeId: "*2345",
        displayName: "Zhang San",
        status: "active",
        aipId: "aip-1",
        agentSpiffeId:
          "spiffe://agents.example/ns/multica/agents/agent-1",
        bucStatus: "active",
        agentIdentityStatus: "active",
        refreshExpiresAt: 1799200000,
      },
    });

    renderCard(true);

    expect(await screen.findByText("Zhang San")).toBeInTheDocument();
    expect(screen.getByText(/Employee ID: \*2345/)).toBeInTheDocument();
    expect(screen.queryByText(/12345/)).not.toBeInTheDocument();
    expect(screen.getByText(/Agent Identity profile.*aip-1/i)).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Test" }),
    ).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Reauthorize" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Unbind" })).toBeInTheDocument();
    expect(screen.queryByRole("textbox")).not.toBeInTheDocument();
  });

  it("lets an owner start BUC binding without entering an employee ID", async () => {
    getStatus.mockResolvedValue({
      configured: true,
      canManage: true,
      identity: null,
    });

    renderCard(true);

    expect(
      await screen.findByRole("button", { name: "Bind identity" }),
    ).toBeInTheDocument();
    expect(
      screen.getByText(/Group Account Permission Assistant/),
    ).toBeInTheDocument();
    expect(screen.queryByRole("textbox")).not.toBeInTheDocument();
  });

  it("starts a fresh binding when opened from a task authorization link", async () => {
    getStatus.mockResolvedValue({
      configured: true,
      canManage: true,
      identity: null,
    });
    beginBinding.mockResolvedValue({
      authorizationUrl: "https://login.example.com/authorize",
      expiresAt: 1799200000,
    });
    window.history.replaceState(
      null,
      "",
      "/acme/agents/agent-1?view=digital_employee&enterprise_identity=authorize",
    );

    renderCard(true);

    await waitFor(() => {
      expect(beginBinding).toHaveBeenCalledWith(
        "workspace-1",
        "agent-1",
        "/acme/agents/agent-1?view=digital_employee",
      );
    });
    expect(window.location.search).toBe("?view=digital_employee");
  });

  it("does not start binding from a shared link for a user who cannot manage the agent", async () => {
    getStatus.mockResolvedValue({
      configured: true,
      canManage: false,
      identity: null,
    });
    window.history.replaceState(
      null,
      "",
      "/acme/agents/agent-1?view=digital_employee&enterprise_identity=authorize",
    );

    renderCard(false);

    expect(
      await screen.findByText(/Only the agent owner and workspace owners\/admins/),
    ).toBeInTheDocument();
    expect(beginBinding).not.toHaveBeenCalled();
    expect(window.location.search).toBe("?view=digital_employee");
  });
});
