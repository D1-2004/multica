// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { I18nProvider } from "@multica/core/i18n/react";
import { ApiError, setApiInstance } from "@multica/core/api";
import type { ApiClient } from "@multica/core/api/client";
import { dingtalkAccountBindingKeys } from "@multica/core/dingtalk-account-bindings";
import enCommon from "../../../locales/en/common.json";
import enAgents from "../../../locales/en/agents.json";
import zhHansAgents from "../../../locales/zh-Hans/agents.json";
import { DingTalkAccountBindingCard } from "./dingtalk-account-binding";

const listBindings = vi.fn();
const beginBinding = vi.fn();
const listReusable = vi.fn();
const reuseIdentity = vi.fn();
const deleteBinding = vi.fn();
const updateBindingSurface = vi.fn();
const setNativeSubscription = vi.fn();
const getNativeSubscriptionStatus = vi.fn();
const bindManually = vi.fn();
const mid2Url = vi.hoisted(() => vi.fn());
const toastError = vi.hoisted(() => vi.fn());

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "workspace-1",
}));

vi.mock("react-qr-code", () => ({
  QRCode: ({ value }: { value: string }) => (
    <svg aria-label="Enterprise digital employee QR code" data-value={value} />
  ),
}));

vi.mock("@ali/ding-mediaid", () => ({ mid2Url }));

vi.mock("sonner", () => ({
  toast: { error: toastError },
}));

vi.mock("@multica/ui/components/ui/avatar", () => ({
  Avatar: ({ children }: { children: ReactNode }) => <span>{children}</span>,
  AvatarImage: ({
    src,
    alt,
    onLoadingStatusChange,
  }: {
    src?: string;
    alt?: string;
    onLoadingStatusChange?: (status: "error") => void;
  }) => (
    <img
      src={src}
      alt={alt}
      onError={() => onLoadingStatusChange?.("error")}
    />
  ),
  AvatarFallback: ({ children }: { children: ReactNode }) => (
    <span>{children}</span>
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

function renderCard(
  bindingMode: "message" | "identity" = "message",
  canOperate = true,
  permissionLoading = false,
) {
  const queryClient = new QueryClient({
    defaultOptions: {
      queries: { retry: false },
      mutations: { retry: false },
    },
  });
  const result = render(
    <DingTalkAccountBindingCard
      agentId="agent-1"
      agentName="Planner"
      bindingMode={bindingMode}
      canOperate={canOperate}
      permissionLoading={permissionLoading}
    />,
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
    source: "identity",
    organizationName: "Alibaba Group",
    accountDisplayName: "Zhang San",
    accountAvatarUrl: "https://example.test/avatar.png",
    boundAt: "2026-07-14T09:30:00Z",
  },
  messageRoute: {
    status: "active",
    accountDisplayName: "Zhang San",
    accountAvatarUrl: "https://example.test/avatar.png",
    boundAt: "2026-07-14T09:30:00Z",
    surfaceType: "issue",
    messageScope: "direct_only",
    enabledDomains: [],
    calendarStartEnabled: false,
    conversations: [],
    emojiConversations: [],
  },
};

const beginQRCodeURL =
  "https://dbase.example/#bindingMode=message&bindingToken=router-secret&callbackToken=callback-secret&callbackUrl=https%3A%2F%2Fmultica.example.com%2Fapi%2Fintegrations%2Fdingtalk%2Faccount-bindings%2Finstallation-1%2Fcallback&expiresAt=1784032200&agentId=agent-1&dispatchUrl=https%3A%2F%2Fmultica.example.com%2Fapi%2Fwebhooks%2Fagent-dispatch%2Fv1_endpoint";

beforeEach(() => {
  vi.clearAllMocks();
  mid2Url.mockImplementation(
    (mediaId: string) => `https://media.example.test/${mediaId.slice(1)}.png`,
  );
  setApiInstance({
    listDingTalkAccountBindings: listBindings,
    listReusableDingTalkIdentities: listReusable,
    reuseDingTalkIdentity: reuseIdentity,
    beginDingTalkAccountBinding: beginBinding,
    deleteDingTalkAccountBinding: deleteBinding,
    updateDingTalkAccountBindingSurface: updateBindingSurface,
    setDingTalkNativeSubscription: setNativeSubscription,
    getDingTalkNativeSubscriptionStatus: getNativeSubscriptionStatus,
    bindDingTalkMessageRouteManually: bindManually,
  } as unknown as ApiClient);
  listBindings.mockResolvedValue({ bindings: [], configured: true });
  listReusable.mockResolvedValue([]);
  reuseIdentity.mockResolvedValue(undefined);
  beginBinding.mockResolvedValue({
    bindingId: "agent-1",
    qrCodeUrl: beginQRCodeURL,
    expiresAt: new Date(Date.now() + 5 * 60 * 1000).toISOString(),
  });
  deleteBinding.mockResolvedValue(undefined);
  updateBindingSurface.mockResolvedValue(undefined);
  setNativeSubscription.mockResolvedValue({ nativeSubscription: true });
  getNativeSubscriptionStatus.mockResolvedValue(nativeStatus("connected"));
  bindManually.mockResolvedValue(undefined);
});

function nativeStatus(
  state: string,
  stream: Record<string, unknown> = {},
) {
  return {
    nativeSubscription: true,
    stream: { state, lastConnectedAt: null, lastEventAt: null, lastError: null, failures: 0, ...stream },
  };
}

afterEach(() => {
  vi.useRealTimers();
});

describe("DingTalkAccountBindingCard", () => {
  it("keeps the execution identity entry out of the Integrations card", async () => {
    renderCard();

    expect(
      await screen.findByRole("button", { name: /Bind digital employee/i }),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: /Bind execution identity/i }),
    ).not.toBeInTheDocument();
  });

  it.each([
    ["direct_only", "Listening to my direct messages"],
    ["all", "Listening to all messages"],
  ] as const)("shows the %s message scope summary", async (messageScope, summary) => {
    listBindings.mockResolvedValue({
      bindings: [
        {
          ...activeBinding,
          messageRoute: { ...activeBinding.messageRoute, messageScope },
        },
      ],
      configured: true,
    });

    renderCard();

    expect(await screen.findByText(summary)).toBeInTheDocument();
  });

  it.each<[string[], string[], string]>([
    [["*"], ["*"], "Listening to all messages"],
    [["*"], [], "Listening to all direct messages"],
    [[], ["*"], "Listening to @me messages in all groups"],
    [["cid-a"], [], "Listening to messages from 1 direct conversation"],
    [[], ["cid-g1", "cid-g2"], "Listening to @me messages in 2 groups"],
    [["*"], ["cid-g1"], "Listening to all direct messages and @me messages in 1 group"],
    [
      ["cid-a", "cid-b"],
      ["*"],
      "Listening to messages from 2 direct conversations and @me messages in all groups",
    ],
  ])(
    "shows the v2 subscription summary for direct=%j group=%j",
    async (directCids, groupCids, summary) => {
      listBindings.mockResolvedValue({
        bindings: [
          {
            ...activeBinding,
            messageRoute: {
              ...activeBinding.messageRoute,
              messageScope: "custom",
              messageScopeVersion: 2,
              subscription: { directCids, groupCids },
              conversations: [],
            },
          },
        ],
        configured: true,
      });

      renderCard();

      expect(await screen.findByText(summary)).toBeInTheDocument();
    },
  );

  it("keeps the legacy summary when the subscription has no v2 version marker", async () => {
    listBindings.mockResolvedValue({
      bindings: [
        {
          ...activeBinding,
          messageRoute: {
            ...activeBinding.messageRoute,
            messageScope: "custom",
            subscription: { directCids: [], groupCids: ["*"] },
            conversations: [],
          },
        },
      ],
      configured: true,
    });

    renderCard();

    expect(
      await screen.findByRole("button", {
        name: "Listening to messages from 0 conversations",
      }),
    ).toBeInTheDocument();
  });

  it("falls back to the legacy summary when a v2 record carries no subscription", async () => {
    listBindings.mockResolvedValue({
      bindings: [
        {
          ...activeBinding,
          messageRoute: {
            ...activeBinding.messageRoute,
            messageScope: "direct_only",
            messageScopeVersion: 2,
            subscription: null,
          },
        },
      ],
      configured: true,
    });

    renderCard();

    expect(
      await screen.findByText("Listening to my direct messages"),
    ).toBeInTheDocument();
  });

  it("shows calendar listening when the binding enables calendar starts", async () => {
    listBindings.mockResolvedValue({
      bindings: [
        {
          ...activeBinding,
          messageRoute: {
            ...activeBinding.messageRoute,
            calendarStartEnabled: true,
          },
        },
      ],
      configured: true,
    });

    renderCard();

    expect(await screen.findByText("Listening for calendar starts")).toBeInTheDocument();
  });

  it("shows approval listening when the binding enables approval events", async () => {
    listBindings.mockResolvedValue({
      bindings: [
        {
          ...activeBinding,
          messageRoute: {
            ...activeBinding.messageRoute,
            enabledDomains: ["channel", "approval"],
          },
        },
      ],
      configured: true,
    });

    renderCard();

    expect(await screen.findByText("Listening for approval events")).toBeInTheDocument();
  });

  it("shows the emoji reaction conversations when the binding subscribes to them", async () => {
    listBindings.mockResolvedValue({
      bindings: [
        {
          ...activeBinding,
          messageRoute: {
            ...activeBinding.messageRoute,
            emojiConversations: [
              { cid: "78288514993", name: "消息测试" },
              { cid: "2960443310:6261898177", name: "叶志毅" },
            ],
          },
        },
      ],
      configured: true,
    });

    renderCard();

    expect(
      await screen.findByText("Listening for emoji reactions in 2 conversations"),
    ).toBeInTheDocument();

    fireEvent.click(
      screen.getByRole("button", {
        name: "Listening for emoji reactions in 2 conversations",
      }),
    );
    expect(await screen.findByText("消息测试")).toBeInTheDocument();
    expect(screen.getByText("叶志毅")).toBeInTheDocument();
  });

  it("uses the exact Chinese listening summaries", () => {
    const integrations = zhHansAgents.tab_body.integrations;

    expect(integrations.dingtalk_account_scope_direct_only).toBe("已监听我聊消息");
    expect(integrations.dingtalk_account_scope_all).toBe("已监听全部消息");
    expect(integrations.dingtalk_account_scope_custom_other).toBe(
      "已监听 {{count}} 个对话的消息",
    );
    expect(integrations.dingtalk_account_scope_direct_all).toBe("已监听所有单聊消息");
    expect(integrations.dingtalk_account_scope_group_all).toBe(
      "已监听所有群聊@我的消息",
    );
    expect(integrations.dingtalk_account_scope_direct_custom_other).toBe(
      "已监听 {{count}} 个单聊对话的消息",
    );
    expect(integrations.dingtalk_account_scope_group_custom_other).toBe(
      "已监听 {{count}} 个群聊@我的消息",
    );
    expect(integrations.dingtalk_account_scope_direct_all_group_custom_other).toBe(
      "已监听所有单聊消息和 {{count}} 个群聊@我的消息",
    );
    expect(integrations.dingtalk_account_scope_direct_custom_group_all_other).toBe(
      "已监听 {{count}} 个单聊对话和所有群聊@我的消息",
    );
    expect(integrations.dingtalk_account_scope_direct_custom_group_custom_other).toBe(
      "已监听 {{directCount}} 个单聊对话和 {{groupCount}} 个群聊@我的消息",
    );
    expect(integrations.dingtalk_account_scope_approval).toBe("已监听审批事件");
    expect(integrations.dingtalk_account_binding_invalid_warning).toBe(
      "该数字员工已经绑定到其他智能体，消息订阅已失效",
    );
    expect(integrations.dingtalk_account_router_unavailable_warning).toBe(
      "暂时无法核验消息订阅状态",
    );
  });

  it.each([
    [
      "bound_to_other_agent",
      "This digital employee is now bound to another agent. Its message subscription is no longer active here.",
    ],
    [
      "inconsistent",
      "This digital employee is now bound to another agent. Its message subscription is no longer active here.",
    ],
    ["unbound", "This digital employee's message subscription is no longer active."],
    ["router_unavailable", "The message subscription status cannot be verified right now."],
  ] as const)("keeps the unbind action for a %s message relation", async (status, warning) => {
    listBindings.mockResolvedValue({
      bindings: [
        {
          ...activeBinding,
          dwsIdentity: { status: "unbound" },
          messageRoute: { ...activeBinding.messageRoute, status },
        },
      ],
      configured: true,
    });
    renderCard();

    expect(await screen.findByText(warning)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Bind digital employee/i })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: /^Unbind$/i })).toBeInTheDocument();
    expect(deleteBinding).not.toHaveBeenCalled();
  });

  it("removes a stale bound-to-other-agent projection after unbind succeeds and refreshes", async () => {
    listBindings
      .mockResolvedValueOnce({
        bindings: [
          {
            ...activeBinding,
            dwsIdentity: { status: "unbound" },
            messageRoute: {
              ...activeBinding.messageRoute,
              status: "bound_to_other_agent",
            },
          },
        ],
        configured: true,
      })
      .mockResolvedValue({ bindings: [], configured: true });
    const user = userEvent.setup();

    renderCard();

    expect(await screen.findByText(
      "This digital employee is now bound to another agent. Its message subscription is no longer active here.",
    )).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /^Unbind$/i }));
    await user.click(screen.getAllByRole("button", { name: /^Unbind$/i }).at(-1)!);

    expect(deleteBinding).toHaveBeenCalledWith("workspace-1", "agent-1", "message");
    expect(await screen.findByRole("button", { name: /Bind digital employee/i })).toBeInTheDocument();
    expect(listBindings).toHaveBeenCalledTimes(2);
  });

  it("keeps a router-unavailable projection and shows a retryable unbind error", async () => {
    listBindings.mockResolvedValue({
      bindings: [
        {
          ...activeBinding,
          dwsIdentity: { status: "unbound" },
          messageRoute: {
            ...activeBinding.messageRoute,
            status: "router_unavailable",
          },
        },
      ],
      configured: true,
    });
    deleteBinding.mockRejectedValue(new Error("Subscription verification failed. Try again."));
    const user = userEvent.setup();

    renderCard();

    expect(await screen.findByText(
      "The message subscription status cannot be verified right now.",
    )).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /^Unbind$/i }));
    await user.click(screen.getAllByRole("button", { name: /^Unbind$/i }).at(-1)!);

    expect(await screen.findByText("Subscription verification failed. Try again.")).toBeInTheDocument();
    expect(screen.getByText(
      "The message subscription status cannot be verified right now.",
    )).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Bind digital employee/i })).not.toBeInTheDocument();
    expect(listBindings).toHaveBeenCalledTimes(1);
  });

  it("expands every custom conversation with media-id, URL, and initial fallbacks", async () => {
    mid2Url.mockImplementation((mediaId: string) => {
      if (mediaId === "@broken-media") throw new Error("invalid media id");
      return `https://media.example.test/${mediaId.slice(1)}.png`;
    });
    listBindings.mockResolvedValue({
      bindings: [
        {
          ...activeBinding,
          messageRoute: {
            ...activeBinding.messageRoute,
            messageScope: "custom",
            conversations: [
              {
                cid: "cid-alpha",
                name: "Project Alpha",
                avatarMediaId: "@media-alpha",
                avatarUrl: "https://fallback.example.test/alpha.png",
              },
              {
                cid: "cid-beta",
                name: "Project Beta",
                avatarUrl: "https://fallback.example.test/beta.png",
              },
              {
                cid: "cid-gamma",
                name: "Project Gamma",
                avatarMediaId: "@broken-media",
                avatarUrl: "https://fallback.example.test/gamma.png",
              },
              { cid: "cid-delta", name: "Delta Team" },
            ],
          },
        },
      ],
      configured: true,
    });
    const user = userEvent.setup();

    renderCard();

    const summary = await screen.findByRole("button", {
      name: "Listening to messages from 4 conversations",
    });
    expect(summary).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByText("Project Alpha")).not.toBeInTheDocument();

    await user.click(summary);

    expect(summary).toHaveAttribute("aria-expanded", "true");
    for (const name of [
      "Project Alpha",
      "Project Beta",
      "Project Gamma",
      "Delta Team",
    ]) {
      expect(screen.getByText(name)).toBeInTheDocument();
    }
    expect(screen.getByRole("img", { name: "Project Alpha" })).toHaveAttribute(
      "src",
      "https://media.example.test/media-alpha.png",
    );
    fireEvent.error(screen.getByRole("img", { name: "Project Alpha" }));
    expect(screen.getByRole("img", { name: "Project Alpha" })).toHaveAttribute(
      "src",
      "https://fallback.example.test/alpha.png",
    );
    fireEvent.error(screen.getByRole("img", { name: "Project Alpha" }));
    expect(
      screen.queryByRole("img", { name: "Project Alpha" }),
    ).not.toBeInTheDocument();
    expect(screen.getByRole("img", { name: "Project Beta" })).toHaveAttribute(
      "src",
      "https://fallback.example.test/beta.png",
    );
    expect(screen.getByRole("img", { name: "Project Gamma" })).toHaveAttribute(
      "src",
      "https://fallback.example.test/gamma.png",
    );
    expect(screen.getByText("D")).toBeInTheDocument();
    expect(mid2Url).toHaveBeenCalledWith("@media-alpha", {
      imageSize: "thumb",
    });

    await user.click(summary);
    expect(summary).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByText("Project Alpha")).not.toBeInTheDocument();
  });

  it("shows the unconfigured state without a begin action", async () => {
    listBindings.mockResolvedValue({ bindings: [], configured: false });

    renderCard();

    expect(
      await screen.findByText(/Enterprise digital employee binding is not configured/i),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: /Bind digital employee/i }),
    ).not.toBeInTheDocument();
  });

  it("renders the complete backend QR URL", async () => {
    const user = userEvent.setup();

    renderCard();
    await user.click(
      await screen.findByRole("button", { name: /Bind digital employee/i }),
    );

    const qr = await screen.findByLabelText("Enterprise digital employee QR code");
    expect(qr).toHaveAttribute(
      "data-value",
      beginQRCodeURL,
    );
    expect(beginBinding).toHaveBeenCalledWith("workspace-1", "agent-1", "message");
  });

  it("starts the independent execution identity mode", async () => {
    const user = userEvent.setup();

    renderCard("identity");
    await user.click(
      await screen.findByRole("button", { name: /Bind execution identity/i }),
    );

    expect(beginBinding).toHaveBeenCalledWith("workspace-1", "agent-1", "identity");
    expect(await screen.findByRole("heading", { name: "Bind execution identity" })).toBeInTheDocument();
  });

  it.each([
    ["message", /Bind digital employee/i],
    ["identity", /Bind execution identity/i],
  ] as const)("keeps unauthorized %s binding read only", async (bindingMode, buttonName) => {
    const user = userEvent.setup();

    renderCard(bindingMode, false);
    await user.click(await screen.findByRole("button", { name: buttonName }));

    expect(toastError).toHaveBeenCalledWith(
      "You don't have permission to perform this action. Contact this agent's administrator.",
    );
    expect(beginBinding).not.toHaveBeenCalled();
  });

  it("shows an expired state for an already-expired begin response", async () => {
    beginBinding.mockResolvedValue({
      bindingId: "agent-1",
      qrCodeUrl: "https://dbase.example/#bindingToken=expired",
      expiresAt: "2020-01-01T00:00:00Z",
    });
    const user = userEvent.setup();

    renderCard();
    await user.click(
      await screen.findByRole("button", { name: /Bind digital employee/i }),
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
      await screen.findByRole("button", { name: /Bind digital employee/i }),
    );
    expect(await screen.findByLabelText("Enterprise digital employee QR code")).toBeInTheDocument();

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

  it("treats an active message route as connected without an execution identity", async () => {
    listBindings.mockResolvedValue({
      bindings: [
        {
          ...activeBinding,
          dwsIdentity: { status: "unbound" },
          messageRoute: {
            ...activeBinding.messageRoute,
            accountDisplayName: "Digital Worker Zhang",
          },
        },
      ],
      configured: true,
    });

    renderCard("message");

    expect(await screen.findByText("Digital Worker Zhang")).toBeInTheDocument();
    expect(screen.getByRole("img", { name: "Digital Worker Zhang" })).toHaveAttribute(
      "src",
      "https://example.test/avatar.png",
    );
    expect(screen.getByText("Listening to my direct messages")).toBeInTheDocument();
    const accountRow = screen.getByTestId("dingtalk-account-binding-active-row");
    expect(accountRow).toHaveClass("items-start");
    expect(screen.getByRole("button", { name: /^Unbind$/i })).toBeInTheDocument();
    const modeTrigger = screen.getByRole("button", {
      name: "Run mode: Task mode",
    });
    expect(modeTrigger).toHaveTextContent("Task mode");
    expect(
      screen.queryByRole("radio", { name: /Conversation mode/i }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: /Bind digital employee/i }),
    ).not.toBeInTheDocument();
  });

  it("switches an active digital employee from issue to chat without rebinding", async () => {
    listBindings.mockResolvedValue({ bindings: [activeBinding], configured: true });
    const user = userEvent.setup();

    renderCard("message");

    await user.click(await screen.findByRole("button", {
      name: "Run mode: Task mode",
    }));
    await user.click(screen.getByRole("radio", { name: /Conversation mode/i }));

    expect(updateBindingSurface).not.toHaveBeenCalled();

    await user.click(screen.getByRole("button", { name: "Confirm" }));

    expect(updateBindingSurface).toHaveBeenCalledWith(
      "workspace-1",
      "agent-1",
      "chat",
    );
    expect(beginBinding).not.toHaveBeenCalled();
    expect(deleteBinding).not.toHaveBeenCalled();
  });

  it("shows automatic mode for an active binding", async () => {
    listBindings.mockResolvedValue({
      bindings: [
        {
          ...activeBinding,
          messageRoute: {
            ...activeBinding.messageRoute,
            surfaceType: "auto",
          },
        },
      ],
      configured: true,
    });

    renderCard("message");

    const modeTrigger = await screen.findByRole("button", {
      name: "Run mode: Automatic mode",
    });
    expect(modeTrigger).toHaveTextContent("Automatic mode");
  });

  it("switches to automatic mode without rebinding", async () => {
    listBindings.mockResolvedValue({ bindings: [activeBinding], configured: true });
    const user = userEvent.setup();

    renderCard("message");

    const modeTrigger = await screen.findByRole("button", {
      name: "Run mode: Task mode",
    });
    await user.click(modeTrigger);
    expect(screen.getByText(
      "Start in chat, then let the agent answer directly or create an issue for background work.",
    )).toBeInTheDocument();
    await user.click(screen.getByRole("radio", { name: /Automatic mode/i }));
    await user.click(screen.getByRole("button", { name: "Confirm" }));

    expect(updateBindingSurface).toHaveBeenCalledWith(
      "workspace-1",
      "agent-1",
      "auto",
    );
    expect(beginBinding).not.toHaveBeenCalled();
    expect(deleteBinding).not.toHaveBeenCalled();
  });

  it("keeps the current run mode when the popover selection is cancelled", async () => {
    listBindings.mockResolvedValue({ bindings: [activeBinding], configured: true });
    const user = userEvent.setup();

    renderCard("message");

    const modeTrigger = await screen.findByRole("button", {
      name: "Run mode: Task mode",
    });
    await user.click(modeTrigger);
    await user.click(screen.getByRole("radio", { name: /Conversation mode/i }));
    await user.click(screen.getByRole("button", { name: "Cancel" }));

    expect(updateBindingSurface).not.toHaveBeenCalled();
    expect(modeTrigger).toHaveTextContent("Task mode");
    expect(
      screen.queryByRole("radio", { name: /Conversation mode/i }),
    ).not.toBeInTheDocument();
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

  it("blocks unauthorized unbind and run-mode changes without calling the API", async () => {
    listBindings.mockResolvedValue({
      bindings: [activeBinding],
      configured: true,
    });
    const user = userEvent.setup();

    renderCard("message", false);
    expect(await screen.findByText("Zhang San")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: /^Unbind$/i }));
    await user.click(screen.getByRole("button", { name: "Run mode: Task mode" }));

    expect(toastError).toHaveBeenCalledTimes(2);
    expect(deleteBinding).not.toHaveBeenCalled();
    expect(updateBindingSurface).not.toHaveBeenCalled();
    expect(screen.queryByRole("radio", { name: /Conversation mode/i })).not.toBeInTheDocument();
  });

  it("does not report a permission error while permissions are loading", async () => {
    renderCard("message", false, true);

    expect(
      await screen.findByRole("button", { name: /Bind digital employee/i }),
    ).toBeDisabled();
    expect(toastError).not.toHaveBeenCalled();
    expect(beginBinding).not.toHaveBeenCalled();
  });

  it("shows and can unbind the execution identity while the digital employee is active", async () => {
    listBindings.mockResolvedValue({ bindings: [activeBinding], configured: true });

    const user = userEvent.setup();

    renderCard("identity");

    expect(await screen.findByText("Zhang San")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Bind execution identity/i })).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /^Unbind$/i }));
    await user.click(screen.getAllByRole("button", { name: /^Unbind$/i }).at(-1)!);

    expect(deleteBinding).toHaveBeenCalledWith("workspace-1", "agent-1", "identity");
  });

  it("keeps DWS identity active when the message route is still pending", async () => {
    listBindings.mockResolvedValue({
      bindings: [
        {
          ...activeBinding,
          dwsIdentity: { ...activeBinding.dwsIdentity, source: "identity" },
          messageRoute: { status: "pending" },
        },
      ],
      configured: true,
    });

    renderCard("identity");

    expect(await screen.findByText("Zhang San")).toBeInTheDocument();
    expect(screen.getByText(/Organization:\s*Alibaba Group/i)).toBeInTheDocument();
    expect(screen.getByText(/Default execution identity bound/i)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /^Unbind$/i })).toBeInTheDocument();
  });

  it("shows the connect action for identity-only binding without a message projection", async () => {
    listBindings.mockResolvedValue({
      bindings: [
        {
          ...activeBinding,
          dwsIdentity: { ...activeBinding.dwsIdentity, source: "identity" },
          messageRoute: { status: "unbound", messageScope: "direct_only" },
        },
      ],
      configured: true,
    });

    const { unmount } = renderCard("identity");

    expect(await screen.findByText("Zhang San")).toBeInTheDocument();
    expect(screen.getByText(/Default execution identity bound/i)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /^Unbind$/i })).toBeInTheDocument();

    unmount();
    renderCard("message");
    expect(await screen.findByRole("button", { name: /Bind digital employee/i })).toBeInTheDocument();
    expect(screen.queryByText(enAgents.tab_body.integrations.dingtalk_account_unbound_warning)).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^Unbind$/i })).not.toBeInTheDocument();
    expect(deleteBinding).not.toHaveBeenCalled();
  });

  it("shows terminal task failures and allows a fresh binding attempt", async () => {
    listBindings.mockResolvedValue({
      bindings: [
        {
          ...activeBinding,
          dwsIdentity: { status: "unbound" },
          messageRoute: {
            status: "failed",
            error: {
              code: "source_already_bound",
              message: "Message source is already bound to another agent. Unbind it and try again.",
              retryable: false,
            },
          },
        },
      ],
      configured: true,
    });

    renderCard();

    expect(await screen.findByText(/Direct message route:\s*Failed/i)).toBeInTheDocument();
    expect(
      screen.getByText(
        "Message source is already bound to another agent. Unbind it and try again.",
      ),
    ).toBeInTheDocument();
    expect(screen.queryByText(/DWS identity/i)).not.toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: /Generate a new QR code/i }),
    ).toBeInTheDocument();
  });

  it("lets an authorized operator restart a pending association after reload", async () => {
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
      await screen.findByText(/The previous digital employee binding was not completed/i),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: /Generate a new QR code/i }),
    ).toBeInTheDocument();
  });

  it("keeps the exact Chinese permission message", () => {
    expect(
      zhHansAgents.tab_body.integrations.dingtalk_account_permission_denied,
    ).toBe("无权限操作，请联系此智能体管理员处理");
  });
});


describe("reusable execution identity", () => {
  const candidate = { sourceAgentId: "source-agent", sourceAgentName: "Existing agent", accountDisplayName: "Alice", organizationName: "Acme" };
  it("applies the chosen existing identity without creating a QR attempt", async () => {
    listReusable.mockResolvedValue([candidate]);
    reuseIdentity.mockImplementation(async () => {
      listBindings.mockResolvedValue({ configured: true, bindings: [{ ...activeBinding, dwsIdentity: { ...activeBinding.dwsIdentity, accountDisplayName: "Alice" } }] });
    });
    renderCard("identity");
    const select = await screen.findByRole("combobox", { name: "Use an existing identity" });
    expect(screen.getByRole("button", { name: "Use this identity" })).toBeDisabled();
    fireEvent.change(select, { target: { value: "source-agent" } });
    fireEvent.click(screen.getByRole("button", { name: "Use this identity" }));
    await waitFor(() => expect(reuseIdentity).toHaveBeenCalledWith("workspace-1", "agent-1", "source-agent"));
    expect(beginBinding).not.toHaveBeenCalled();
    expect(await screen.findByText("Alice")).toBeInTheDocument();
    expect(screen.queryByRole("combobox")).not.toBeInTheDocument();
  });
  it("keeps QR binding available with no reusable identities", async () => {
    renderCard("identity");
    expect(await screen.findByText("No reusable identities yet. Scan to bind a new account.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Bind execution identity" })).toBeEnabled();
  });
  it("shows a revoked-source error without reporting success", async () => {
    listReusable.mockResolvedValue([candidate]);
    reuseIdentity.mockRejectedValue(new Error("identity is no longer reusable"));
    renderCard("identity");
    fireEvent.change(await screen.findByRole("combobox"), { target: { value: "source-agent" } });
    fireEvent.click(screen.getByRole("button", { name: "Use this identity" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("identity is no longer reusable");
    expect(screen.queryByText("Default execution identity bound")).not.toBeInTheDocument();
  });
  it("never offers personal identity reuse on the message-binding card", async () => {
    renderCard("message");
    await screen.findByRole("button", { name: "Bind digital employee" });
    expect(listReusable).not.toHaveBeenCalled();
  });
});

function conflict(code: string): ApiError {
  return new ApiError("conflict", 409, "Conflict", { code, error: "conflict" });
}

// Identity bound, no digital-employee message projection.
const identityOnlyBinding = {
  ...activeBinding,
  dwsIdentity: { ...activeBinding.dwsIdentity, nativeSubscription: false },
  messageRoute: { status: "unbound", messageScope: "direct_only" },
};

const nativeOnBinding = {
  ...identityOnlyBinding,
  dwsIdentity: { ...identityOnlyBinding.dwsIdentity, nativeSubscription: true },
};

describe("DWS native subscription on the execution identity", () => {
  it("shows the switch on a bound identity and reflects the server state", async () => {
    listBindings.mockResolvedValue({ bindings: [nativeOnBinding], configured: true });

    renderCard("identity");

    const toggle = await screen.findByRole("switch", { name: "Native subscription" });
    expect(toggle).toHaveAttribute("aria-checked", "true");
    expect(toggle).not.toHaveAttribute("data-disabled");
    expect(screen.getByText(enAgents.tab_body.integrations.dingtalk_identity_native_subscription_hint))
      .toBeInTheDocument();
    // The switch sits on the bound row, next to Unbind.
    expect(screen.getByTestId("dingtalk-identity-binding-active-row")).toContainElement(toggle);
    expect(screen.getByRole("button", { name: /^Unbind$/i })).toBeInTheDocument();
  });

  it.each([
    ["connected", "Subscription connected"],
    ["connecting", "Subscription connecting"],
    ["disconnected", "Subscription disconnected"],
    ["unavailable", "Native subscription is not running in this environment"],
    ["unknown", "Subscription status unknown"],
  ])("shows the %s stream as a status light", async (state, label) => {
    listBindings.mockResolvedValue({ bindings: [nativeOnBinding], configured: true });
    getNativeSubscriptionStatus.mockResolvedValue(nativeStatus(state));

    renderCard("identity");

    const light = await screen.findByRole("img", { name: new RegExp(`^${label}`) });
    expect(light).toHaveAttribute("data-state", state);
    expect(getNativeSubscriptionStatus).toHaveBeenCalledWith("workspace-1", "agent-1");
    // The light sits next to the switch on the bound row.
    expect(screen.getByTestId("dingtalk-identity-binding-active-row")).toContainElement(light);
  });

  it("explains a disconnected stream under the switch", async () => {
    listBindings.mockResolvedValue({ bindings: [nativeOnBinding], configured: true });
    getNativeSubscriptionStatus.mockResolvedValue(
      nativeStatus("disconnected", { lastError: "dial: handshake refused", failures: 3 }),
    );

    renderCard("identity");

    expect(
      await screen.findByText(
        "Subscription disconnected (failed attempts: 3): dial: handshake refused",
      ),
    ).toBeInTheDocument();
  });

  it("names a connected stream that has not received a message yet", async () => {
    listBindings.mockResolvedValue({ bindings: [nativeOnBinding], configured: true });
    getNativeSubscriptionStatus.mockResolvedValue(
      nativeStatus("connected", { lastConnectedAt: new Date().toISOString() }),
    );

    renderCard("identity");

    expect(
      await screen.findByRole("img", {
        name: "Subscription connected · No message received since connecting",
      }),
    ).toBeInTheDocument();
  });

  it("does not watch the stream while native subscription is off", async () => {
    listBindings.mockResolvedValue({ bindings: [identityOnlyBinding], configured: true });

    renderCard("identity");

    await screen.findByRole("switch", { name: "Native subscription" });
    expect(screen.queryByTestId("dingtalk-native-stream")).not.toBeInTheDocument();
    expect(getNativeSubscriptionStatus).not.toHaveBeenCalled();
  });

  it("turns native subscription on and refreshes the bindings", async () => {
    listBindings.mockResolvedValue({ bindings: [identityOnlyBinding], configured: true });
    const user = userEvent.setup();

    renderCard("identity");

    const toggle = await screen.findByRole("switch", { name: "Native subscription" });
    expect(toggle).toHaveAttribute("aria-checked", "false");
    await user.click(toggle);

    await waitFor(() =>
      expect(setNativeSubscription).toHaveBeenCalledWith("workspace-1", "agent-1", true),
    );
    await waitFor(() => expect(listBindings).toHaveBeenCalledTimes(2));
  });

  it("turns native subscription off", async () => {
    listBindings.mockResolvedValue({ bindings: [nativeOnBinding], configured: true });
    const user = userEvent.setup();

    renderCard("identity");
    await user.click(await screen.findByRole("switch", { name: "Native subscription" }));

    await waitFor(() =>
      expect(setNativeSubscription).toHaveBeenCalledWith("workspace-1", "agent-1", false),
    );
  });

  it("disables the switch while the digital employee message binding is active", async () => {
    listBindings.mockResolvedValue({ bindings: [activeBinding], configured: true });
    const user = userEvent.setup();

    renderCard("identity");

    const toggle = await screen.findByRole("switch", { name: "Native subscription" });
    expect(toggle).toHaveAttribute("data-disabled");
    expect(screen.getByText(
      enAgents.tab_body.integrations.dingtalk_identity_native_subscription_blocked,
    )).toBeInTheDocument();
    await user.click(toggle);
    expect(setNativeSubscription).not.toHaveBeenCalled();
  });

  it("does not render the switch while the identity is unbound", async () => {
    listBindings.mockResolvedValue({
      bindings: [{ ...identityOnlyBinding, dwsIdentity: { status: "unbound" } }],
      configured: true,
    });

    renderCard("identity");

    expect(await screen.findByRole("button", { name: /Bind execution identity/i })).toBeInTheDocument();
    expect(screen.queryByRole("switch")).not.toBeInTheDocument();
  });

  it("keeps the switch off the message-binding card", async () => {
    listBindings.mockResolvedValue({ bindings: [activeBinding], configured: true });

    renderCard("message");

    expect(await screen.findByText("Zhang San")).toBeInTheDocument();
    expect(screen.queryByRole("switch")).not.toBeInTheDocument();
  });

  it("maps a server conflict code to a friendly message", async () => {
    listBindings.mockResolvedValue({ bindings: [identityOnlyBinding], configured: true });
    setNativeSubscription.mockRejectedValue(
      conflict("native_subscription_conflicts_with_message_binding"),
    );
    const user = userEvent.setup();

    renderCard("identity");
    await user.click(await screen.findByRole("switch", { name: "Native subscription" }));

    expect(await screen.findByRole("alert")).toHaveTextContent(
      enAgents.tab_body.integrations.dingtalk_identity_native_subscription_blocked,
    );
  });

  it.each([
    ["native_subscription_requires_managed_response", "dingtalk_identity_native_subscription_requires_managed_response"],
    ["native_subscription_account_in_use", "dingtalk_identity_native_subscription_account_in_use"],
    ["native_subscription_unavailable", "dingtalk_identity_native_subscription_unavailable"],
  ] as const)("maps %s to a friendly message", async (code, key) => {
    listBindings.mockResolvedValue({ bindings: [identityOnlyBinding], configured: true });
    setNativeSubscription.mockRejectedValue(conflict(code));
    const user = userEvent.setup();

    renderCard("identity");
    await user.click(await screen.findByRole("switch", { name: "Native subscription" }));

    expect(await screen.findByRole("alert")).toHaveTextContent(enAgents.tab_body.integrations[key]);
  });

  it("blocks unauthorized toggles without calling the API", async () => {
    listBindings.mockResolvedValue({ bindings: [identityOnlyBinding], configured: true });
    const user = userEvent.setup();

    renderCard("identity", false);
    await user.click(await screen.findByRole("switch", { name: "Native subscription" }));

    expect(toastError).toHaveBeenCalledWith(
      "You don't have permission to perform this action. Contact this agent's administrator.",
    );
    expect(setNativeSubscription).not.toHaveBeenCalled();
  });
});

describe("operator manual message binding", () => {
  it("is hidden unless the server allows manual binding", async () => {
    renderCard("message");

    expect(await screen.findByRole("button", { name: /Bind digital employee/i })).toBeInTheDocument();
    expect(screen.queryByTestId("dingtalk-manual-message-binding")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Bind directly" })).not.toBeInTheDocument();
  });

  it("never shows the manual form on the execution identity card", async () => {
    listBindings.mockResolvedValue({ bindings: [], configured: true, manualBindingAllowed: true });

    renderCard("identity");

    expect(await screen.findByRole("button", { name: /Bind execution identity/i })).toBeInTheDocument();
    expect(screen.queryByTestId("dingtalk-manual-message-binding")).not.toBeInTheDocument();
  });

  it("validates the corpId and user id and binds with the chosen scope", async () => {
    listBindings.mockResolvedValue({ bindings: [], configured: true, manualBindingAllowed: true });
    const user = userEvent.setup();

    renderCard("message");

    expect(await screen.findByTestId("dingtalk-manual-message-binding")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Bind digital employee/i })).toBeEnabled();
    const submit = screen.getByRole("button", { name: "Bind directly" });
    const corpId = screen.getByLabelText("CorpID");
    const uid = screen.getByLabelText("UID");
    const scope = screen.getByLabelText("Message scope");
    expect(scope).toHaveValue("all");
    expect(submit).toBeDisabled();

    // A numeric OrgID is not a corpId.
    await user.type(corpId, "439446171");
    await user.type(uid, "abc");
    expect(screen.getByText("Enter a DingTalk corpId (starts with ding).")).toBeInTheDocument();
    expect(screen.getByText("Enter a numeric DingTalk ID.")).toBeInTheDocument();
    expect(corpId).toHaveAttribute("aria-invalid", "true");
    expect(submit).toBeDisabled();

    await user.clear(corpId);
    await user.type(corpId, "ding8196cd9a2b2405da24f2f5cc6abecb85");
    await user.clear(uid);
    await user.type(uid, "7890");
    expect(screen.queryByText("Enter a DingTalk corpId (starts with ding).")).not.toBeInTheDocument();
    expect(screen.queryByText("Enter a numeric DingTalk ID.")).not.toBeInTheDocument();
    await user.selectOptions(scope, "direct_only");
    expect(submit).toBeEnabled();

    await user.click(submit);

    await waitFor(() =>
      expect(bindManually).toHaveBeenCalledWith("workspace-1", "agent-1", {
        corpId: "ding8196cd9a2b2405da24f2f5cc6abecb85",
        uid: "7890",
        messageScope: "direct_only",
      }),
    );
    await waitFor(() => expect(listBindings).toHaveBeenCalledTimes(2));
    expect(beginBinding).not.toHaveBeenCalled();
  });

  it("shows a friendly message for an operator-only rejection", async () => {
    listBindings.mockResolvedValue({ bindings: [], configured: true, manualBindingAllowed: true });
    bindManually.mockRejectedValue(
      new ApiError("forbidden", 403, "Forbidden", { code: "operator_only", error: "forbidden" }),
    );
    const user = userEvent.setup();

    renderCard("message");
    await user.type(await screen.findByLabelText("CorpID"), "ding8196cd9a2b2405da24f2f5cc6abecb85");
    await user.type(screen.getByLabelText("UID"), "7890");
    await user.click(screen.getByRole("button", { name: "Bind directly" }));

    expect(await screen.findByRole("alert")).toHaveTextContent(
      enAgents.tab_body.integrations.dingtalk_account_manual_operator_only,
    );
  });

  it("disables QR and manual binding while native subscription is on", async () => {
    listBindings.mockResolvedValue({
      bindings: [nativeOnBinding],
      configured: true,
      manualBindingAllowed: true,
    });

    renderCard("message");

    expect(await screen.findByText(
      enAgents.tab_body.integrations.dingtalk_account_native_subscription_blocked,
    )).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Bind digital employee/i })).toBeDisabled();
    expect(screen.getByLabelText("CorpID")).toBeDisabled();
    expect(screen.getByLabelText("UID")).toBeDisabled();
    expect(screen.getByLabelText("Message scope")).toBeDisabled();
    expect(screen.getByRole("button", { name: "Bind directly" })).toBeDisabled();
  });

  it("explains a QR begin rejected by native subscription", async () => {
    beginBinding.mockRejectedValue(conflict("message_binding_conflicts_with_native_subscription"));
    const user = userEvent.setup();

    renderCard("message");
    await user.click(await screen.findByRole("button", { name: /Bind digital employee/i }));

    expect(await screen.findByRole("alert")).toHaveTextContent(
      enAgents.tab_body.integrations.dingtalk_account_native_subscription_blocked,
    );
  });

  it("keeps the exact Chinese native subscription copy", () => {
    const integrations = zhHansAgents.tab_body.integrations;

    expect(integrations.dingtalk_identity_native_subscription).toBe("原生订阅");
    expect(integrations.dingtalk_identity_native_subscription_hint).toBe(
      "开启后，这个身份的单聊和群里 @ 它的消息通过线上 DWS 原生订阅进入智能体；与上方数字员工消息绑定二选一。",
    );
    expect(integrations.dingtalk_identity_native_subscription_blocked).toBe(
      "已绑定数字员工消息，需先解除才能开启原生订阅",
    );
    expect(integrations.dingtalk_account_native_subscription_blocked).toBe(
      "已开启原生订阅，需先关闭才能绑定数字员工消息",
    );
    expect(integrations.dingtalk_account_manual_submit).toBe("直接绑定");
    expect(integrations.dingtalk_account_manual_scope_all).toBe("全部消息");
    expect(integrations.dingtalk_account_manual_scope_direct_only).toBe("仅单聊");
  });
});
