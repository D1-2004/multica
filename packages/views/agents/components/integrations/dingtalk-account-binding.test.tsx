// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { I18nProvider } from "@multica/core/i18n/react";
import { setApiInstance } from "@multica/core/api";
import type { ApiClient } from "@multica/core/api/client";
import { dingtalkAccountBindingKeys } from "@multica/core/dingtalk-account-bindings";
import enCommon from "../../../locales/en/common.json";
import enAgents from "../../../locales/en/agents.json";
import { DingTalkAccountBindingCard } from "./dingtalk-account-binding";

const listBindings = vi.fn();
const beginBinding = vi.fn();
const deleteBinding = vi.fn();

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "workspace-1",
}));

vi.mock("react-qr-code", () => ({
  QRCode: ({ value }: { value: string }) => (
    <svg aria-label="DingTalk account QR code" data-value={value} />
  ),
}));

const resources = { en: { common: enCommon, agents: enAgents } };

function createWrapper(queryClient: QueryClient) {
  return function Wrapper({ children }: { children: ReactNode }) {
    return (
      <I18nProvider locale="en" resources={resources}>
        <QueryClientProvider client={queryClient}>
          {children}
        </QueryClientProvider>
      </I18nProvider>
    );
  };
}

function renderCard() {
  const queryClient = new QueryClient({
    defaultOptions: {
      queries: { retry: false },
      mutations: { retry: false },
    },
  });
  const result = render(
    <DingTalkAccountBindingCard agentId="agent-1" agentName="Planner" />,
    { wrapper: createWrapper(queryClient) },
  );
  return { ...result, queryClient };
}

const activeBinding = {
  id: "installation-1",
  workspaceId: "workspace-1",
  agentId: "agent-1",
  dwsIdentity: {
    status: "active",
    accountDisplayName: "Zhang San",
    accountAvatarUrl: "https://example.test/avatar.png",
    boundAt: "2026-07-14T09:30:00Z",
  },
  messageRoute: {
    status: "active",
    accountDisplayName: "Zhang San",
    accountAvatarUrl: "https://example.test/avatar.png",
    boundAt: "2026-07-14T09:30:00Z",
  },
};

beforeEach(() => {
  vi.clearAllMocks();
  setApiInstance({
    listDingTalkAccountBindings: listBindings,
    beginDingTalkAccountBinding: beginBinding,
    deleteDingTalkAccountBinding: deleteBinding,
  } as unknown as ApiClient);
  listBindings.mockResolvedValue({ bindings: [], configured: true });
  beginBinding.mockResolvedValue({
    installationId: "installation-1",
    qrCodeUrl:
      "https://dbase.example/#bindingToken=router-secret&callbackToken=callback-secret&identityCallbackToken=identity-secret",
    expiresAt: new Date(Date.now() + 5 * 60 * 1000).toISOString(),
  });
  deleteBinding.mockResolvedValue(undefined);
});

afterEach(() => {
  vi.useRealTimers();
});

describe("DingTalkAccountBindingCard", () => {
  it("shows the unconfigured state without a begin action", async () => {
    listBindings.mockResolvedValue({ bindings: [], configured: false });

    renderCard();

    expect(
      await screen.findByText(/DingTalk account association is not configured/i),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: /Associate DingTalk account/i }),
    ).not.toBeInTheDocument();
  });

  it("renders the complete backend QR URL", async () => {
    const user = userEvent.setup();

    renderCard();
    await user.click(
      await screen.findByRole("button", { name: /Associate DingTalk account/i }),
    );

    const qr = await screen.findByLabelText("DingTalk account QR code");
    expect(qr).toHaveAttribute(
      "data-value",
      "https://dbase.example/#bindingToken=router-secret&callbackToken=callback-secret&identityCallbackToken=identity-secret",
    );
    expect(beginBinding).toHaveBeenCalledWith("workspace-1", "agent-1");
  });

  it("shows an expired state for an already-expired begin response", async () => {
    beginBinding.mockResolvedValue({
      installationId: "installation-1",
      qrCodeUrl: "https://dbase.example/#bindingToken=expired",
      expiresAt: "2020-01-01T00:00:00Z",
    });
    const user = userEvent.setup();

    renderCard();
    await user.click(
      await screen.findByRole("button", { name: /Associate DingTalk account/i }),
    );

    expect(await screen.findByText(/This QR code has expired/i)).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: /Generate a new QR code/i }),
    ).toBeInTheDocument();
  });

  it("closes the QR dialog after realtime-driven list data becomes active", async () => {
    const user = userEvent.setup();
    const { queryClient } = renderCard();
    await user.click(
      await screen.findByRole("button", { name: /Associate DingTalk account/i }),
    );
    expect(await screen.findByLabelText("DingTalk account QR code")).toBeInTheDocument();

    listBindings.mockResolvedValue({
      bindings: [activeBinding],
      configured: true,
    });
    await act(async () => {
      await queryClient.invalidateQueries({
        queryKey: dingtalkAccountBindingKeys.list("workspace-1"),
      });
    });

    expect(await screen.findByText("Zhang San")).toBeInTheDocument();
    expect(screen.getByText("Z")).toBeInTheDocument();
    await waitFor(() => {
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    });
  });

  it("keeps the active account visible when unbinding fails", async () => {
    listBindings.mockResolvedValue({
      bindings: [activeBinding],
      configured: true,
    });
    deleteBinding.mockRejectedValue(new Error("Router unavailable"));
    const user = userEvent.setup();

    renderCard();
    expect(await screen.findByText("Zhang San")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /^Unbind$/i }));
    await user.click(
      screen.getAllByRole("button", { name: /^Unbind$/i }).at(-1)!,
    );

    expect(await screen.findByText("Router unavailable")).toBeInTheDocument();
    expect(screen.getByText("Zhang San")).toBeInTheDocument();
  });

  it("keeps DWS identity active when the message route is still pending", async () => {
    listBindings.mockResolvedValue({
      bindings: [
        {
          ...activeBinding,
          messageRoute: { status: "pending" },
        },
      ],
      configured: true,
    });

    renderCard();

    expect(await screen.findByText("Zhang San")).toBeInTheDocument();
    expect(screen.getByText(/DWS identity:\s*Active/i)).toBeInTheDocument();
    expect(screen.getByText(/Direct message route:\s*Pending/i)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /^Unbind$/i })).toBeInTheDocument();
  });

  it("lets a member restart a pending association after reload", async () => {
    listBindings.mockResolvedValue({
      bindings: [
        {
          ...activeBinding,
          dwsIdentity: { status: "unbound" },
          messageRoute: { status: "pending" },
        },
      ],
      configured: true,
    });

    renderCard();

    expect(
      await screen.findByText(/The previous association was not completed/i),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: /Generate a new QR code/i }),
    ).toBeInTheDocument();
  });
});
