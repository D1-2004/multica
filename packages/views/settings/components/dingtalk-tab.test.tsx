import { StrictMode, type ReactNode } from "react";
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../locales/en/common.json";
import enSettings from "../../locales/en/settings.json";

// ApiError is re-exported from @multica/core/api; we mock the api module
// itself but still need a real ApiError class so `e instanceof ApiError`
// in the polling catch behaves the way it does at runtime.
const ApiError = vi.hoisted(() => {
  class ApiError extends Error {
    readonly status: number;
    readonly statusText: string;
    readonly body?: unknown;
    constructor(message: string, status: number, statusText = "", body?: unknown) {
      super(message);
      this.name = "ApiError";
      this.status = status;
      this.statusText = statusText;
      this.body = body;
    }
  }
  return ApiError;
});

const mockBeginInstall = vi.hoisted(() => vi.fn());
const mockGetStatus = vi.hoisted(() => vi.fn());
const mockManualInstall = vi.hoisted(() => vi.fn());
const mockDeleteInstallation = vi.hoisted(() => vi.fn());
const mockRetryRouter = vi.hoisted(() => vi.fn());
const mockInvalidate = vi.hoisted(() => vi.fn());

type MemberRole = "owner" | "admin" | "member" | "guest";

const membersRef = vi.hoisted(() => ({
  current: [{ user_id: "user-1", role: "owner" as MemberRole }],
}));
const installationsRef = vi.hoisted(() => ({
  current: {
    installations: [] as unknown[],
    configured: true,
    install_supported: true,
  } as {
    installations: unknown[];
    configured: boolean;
    install_supported: boolean;
    capabilities?: {
      http_callback: { available: boolean; reason?: string };
    };
  },
}));

vi.mock("@tanstack/react-query", () => ({
  useQuery: (opts: { queryKey: unknown[]; enabled?: boolean }) => {
    if (opts.enabled === false) return { data: undefined, isLoading: false };
    const key = JSON.stringify(opts.queryKey);
    if (key.includes("members")) return { data: membersRef.current, isLoading: false };
    if (key.includes("installations")) {
      return { data: installationsRef.current, isLoading: false };
    }
    return { data: undefined, isLoading: false };
  },
  useQueryClient: () => ({
    invalidateQueries: mockInvalidate,
  }),
  queryOptions: <T,>(opts: T) => opts,
}));

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "workspace-1",
}));

vi.mock("@multica/core/workspace/queries", () => ({
  memberListOptions: () => ({ queryKey: ["members"], queryFn: vi.fn() }),
}));

const agentNameByIdRef = vi.hoisted(() => ({
  current: new Map<string, string>(),
}));
vi.mock("@multica/core/workspace/hooks", () => ({
  useActorName: () => ({
    getAgentName: (agentId: string) =>
      agentNameByIdRef.current.get(agentId) ?? "Unknown Agent",
    getMemberName: () => "Unknown",
    getSquadName: () => "Unknown Squad",
    getActorName: () => "Unknown",
    getActorInitials: () => "??",
    getActorAvatarUrl: () => null,
  }),
}));

vi.mock("../../common/actor-avatar", () => ({
  ActorAvatar: ({ actorType, actorId }: { actorType: string; actorId: string }) => (
    <span data-testid="actor-avatar" data-actor-type={actorType} data-actor-id={actorId} />
  ),
}));

vi.mock("@multica/core/dingtalk", () => ({
  dingtalkInstallationsOptions: () => ({
    queryKey: ["dingtalk", "installations"],
    queryFn: vi.fn(),
  }),
  dingtalkKeys: { installations: (wsId: string) => ["dingtalk", "installations", wsId] },
}));

vi.mock("@multica/core/api", () => ({
  api: {
    beginDingTalkInstall: mockBeginInstall,
    getDingTalkInstallStatus: mockGetStatus,
    manualInstallDingTalk: mockManualInstall,
    deleteDingTalkInstallation: mockDeleteInstallation,
    retryDingTalkRouterRegistration: mockRetryRouter,
  },
  ApiError,
}));

vi.mock("@multica/core/auth", () => {
  const useAuthStore = Object.assign(
    (sel?: (s: { user: { id: string } }) => unknown) =>
      sel ? sel({ user: { id: "user-1" } }) : { user: { id: "user-1" } },
    { getState: () => ({ user: { id: "user-1" } }) },
  );
  return { useAuthStore };
});

vi.mock("sonner", () => ({
  toast: {
    success: vi.fn(),
    error: vi.fn(),
    message: vi.fn(),
  },
}));

vi.mock("react-qr-code", () => {
  const QrStub = ({ value }: { value: string }) => (
    <span data-testid="qr-code" data-value={value} />
  );
  return { QRCode: QrStub, default: QrStub };
});

import { DingTalkAgentBindButton, DingTalkTab } from "./dingtalk-tab";
import { toast } from "sonner";

const TEST_RESOURCES = {
  en: { common: enCommon, settings: enSettings },
};

function I18nWrapper({ children }: { children: ReactNode }) {
  return (
    <I18nProvider locale="en" resources={TEST_RESOURCES}>
      {children}
    </I18nProvider>
  );
}

function StrictModeWrapper({ children }: { children: ReactNode }) {
  return (
    <StrictMode>
      <I18nProvider locale="en" resources={TEST_RESOURCES}>
        {children}
      </I18nProvider>
    </StrictMode>
  );
}

function resetFixtures() {
  vi.clearAllMocks();
  membersRef.current = [{ user_id: "user-1", role: "owner" }];
  installationsRef.current = {
    installations: [],
    configured: true,
    install_supported: true,
  };
  agentNameByIdRef.current = new Map();
}

const activeInstallation = {
  id: "inst-1",
  workspace_id: "workspace-1",
  agent_id: "agent-1",
  client_id: "dingabc",
  installer_user_id: "user-1",
  status: "active",
  installed_at: "2026-07-01T00:00:00Z",
  created_at: "2026-07-01T00:00:00Z",
  updated_at: "2026-07-01T00:00:00Z",
};

describe("DingTalkAgentBindButton (CTA gate)", () => {
  beforeEach(resetFixtures);

  it("shows the bind CTA for an owner", () => {
    render(<DingTalkAgentBindButton agentId="agent-1" agentName="Bot" />, {
      wrapper: I18nWrapper,
    });
    expect(screen.getByRole("button", { name: /Create enterprise bot/i })).toBeTruthy();
  });

  it("hides the bind CTA for a non-admin member (matches backend admin gate)", () => {
    membersRef.current = [{ user_id: "user-1", role: "member" }];
    const { container } = render(
      <DingTalkAgentBindButton agentId="agent-1" agentName="Bot" />,
      { wrapper: I18nWrapper },
    );
    expect(container.innerHTML).toBe("");
  });

  it("keeps the bind CTA when install_supported is false (manual fallback)", () => {
    // The scan-to-create device flow being unavailable must NOT hide the
    // CTA — the manual-credential path still works whenever configured.
    installationsRef.current = {
      installations: [],
      configured: true,
      install_supported: false,
    };
    render(<DingTalkAgentBindButton agentId="agent-1" agentName="Bot" />, {
      wrapper: I18nWrapper,
    });
    expect(screen.getByRole("button", { name: /Create enterprise bot/i })).toBeTruthy();
  });

  it("hides the bind CTA when the integration is not configured", () => {
    installationsRef.current = {
      installations: [],
      configured: false,
      install_supported: false,
    };
    const { container } = render(
      <DingTalkAgentBindButton agentId="agent-1" agentName="Bot" />,
      { wrapper: I18nWrapper },
    );
    expect(container.innerHTML).toBe("");
  });

  it("renders the connected badge for an already-bound agent even when install_supported is false", () => {
    installationsRef.current = {
      installations: [activeInstallation],
      configured: true,
      install_supported: false,
    };
    render(<DingTalkAgentBindButton agentId="agent-1" agentName="Bot" />, {
      wrapper: I18nWrapper,
    });
    expect(screen.getByTestId("dingtalk-agent-bot-connected")).toBeTruthy();
    expect(screen.getByText(/Enterprise bot connected/i)).toBeTruthy();
    expect(screen.queryByRole("button", { name: /Create enterprise bot/i })).toBeNull();
  });

  it("renders the compact status row when onShowConnectedDetails is provided", async () => {
    const user = userEvent.setup();
    installationsRef.current = {
      installations: [activeInstallation],
      configured: true,
      install_supported: true,
    };
    const onShow = vi.fn();
    render(
      <DingTalkAgentBindButton
        agentId="agent-1"
        agentName="Bot"
        onShowConnectedDetails={onShow}
      />,
      { wrapper: I18nWrapper },
    );
    const row = screen.getByTestId("dingtalk-agent-bot-status");
    await user.click(row);
    expect(onShow).toHaveBeenCalledTimes(1);
  });
});

describe("DingTalkInstallDialog (device flow)", () => {
  beforeEach(() => {
    resetFixtures();
    vi.useFakeTimers({ shouldAdvanceTime: true });
    mockBeginInstall.mockResolvedValue({
      session_id: "sess-1",
      qr_code_url: "https://open-dev.dingtalk.com/fe/app-registration?user_code=MUEU",
      expires_in_seconds: 300,
      poll_interval_seconds: 2,
    });
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  async function openDialog() {
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    render(<DingTalkAgentBindButton agentId="agent-1" agentName="Bot" />, {
      wrapper: I18nWrapper,
    });
    await user.click(screen.getByRole("button", { name: /Create enterprise bot/i }));
    await user.click(screen.getByTestId("dingtalk-install-start"));
    await waitFor(() => {
      expect(screen.getByTestId("qr-code")).toBeTruthy();
    });
  }

  it("chooses transport and organization access before starting registration", async () => {
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    installationsRef.current.capabilities = {
      http_callback: { available: true },
    };
    render(<DingTalkAgentBindButton agentId="agent-1" agentName="Bot" />, {
      wrapper: I18nWrapper,
    });
    await user.click(screen.getByRole("button", { name: /Create enterprise bot/i }));

    expect(mockBeginInstall).not.toHaveBeenCalled();
    expect(screen.getByRole("button", { name: /Stream mode/i })).toBeTruthy();
    expect(screen.getByRole("button", { name: /HTTP callback/i })).toBeTruthy();
    expect(
      screen.getByText(/Allow other organization members to use this bot/i),
    ).toBeTruthy();

    await user.click(screen.getByRole("button", { name: /HTTP callback/i }));
    await user.click(screen.getByRole("switch"));
    expect(mockBeginInstall).not.toHaveBeenCalled();
    await user.click(screen.getByTestId("dingtalk-install-start"));

    await waitFor(() => {
      expect(mockBeginInstall).toHaveBeenCalledTimes(1);
      expect(mockBeginInstall).toHaveBeenCalledWith(
        "workspace-1",
        "agent-1",
        true,
        "HTTP_CALLBACK",
      );
    });
  });

  it("renders the QR from the begin response and completes on a success poll", async () => {
    mockGetStatus.mockResolvedValue({ status: "success", installation_id: "inst-1" });

    await openDialog();
    expect(screen.getByTestId("qr-code").getAttribute("data-value")).toBe(
      "https://open-dev.dingtalk.com/fe/app-registration?user_code=MUEU",
    );
    // The transport is explicit even for the legacy/default Stream path.
    expect(mockBeginInstall).toHaveBeenCalledWith(
      "workspace-1",
      "agent-1",
      false,
      "STREAM",
    );
    expect(
      screen.getByText(/Allow other organization members to use this bot/i),
    ).toBeTruthy();
    expect(
      screen.getByText(/only the user who creates this enterprise bot can chat/i),
    ).toBeTruthy();
    expect(
      screen.getByText(/other members in the same DingTalk organization/i),
    ).toBeTruthy();
    expect(
      screen.getByText(/expand the bot's availability in the DingTalk developer console/i),
    ).toBeTruthy();

    await act(async () => {
      await vi.advanceTimersByTimeAsync(2100);
    });

    await waitFor(() => {
      expect(mockInvalidate).toHaveBeenCalled();
      expect(toast.success).toHaveBeenCalled();
    });
  });

  it("stops polling and warns that an approving bot cannot exchange messages yet", async () => {
    mockGetStatus.mockResolvedValue({
      status: "approving",
      installation_id: "inst-review",
    });

    await openDialog();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(2100);
    });

    expect(await screen.findByTestId("dingtalk-install-approving")).toBeTruthy();
    expect(
      screen.getByText(/cannot send or receive messages until DingTalk approves/i),
    ).toBeTruthy();
    expect(mockInvalidate).toHaveBeenCalled();
    expect(toast.message).toHaveBeenCalled();
    expect(toast.success).not.toHaveBeenCalled();

    await act(async () => {
      await vi.advanceTimersByTimeAsync(5000);
    });
    expect(mockGetStatus).toHaveBeenCalledTimes(1);
  });

  it("restarts registration with HTTP_CALLBACK when the capability is available", async () => {
    const user = userEvent.setup();
    installationsRef.current.capabilities = {
      http_callback: { available: true },
    };

    await openDialog();
    await user.click(screen.getByRole("button", { name: /HTTP callback/i }));

    await waitFor(() => {
      expect(mockBeginInstall).toHaveBeenLastCalledWith(
        "workspace-1",
        "agent-1",
        false,
        "HTTP_CALLBACK",
      );
    });
  });

  it("surfaces install_failed with the DingTalk fail reason as diagnostics", async () => {
    mockGetStatus.mockResolvedValue({
      status: "error",
      error_reason: "install_failed",
      error_message: "registration: fail: 用户拒绝授权",
    });

    await openDialog();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(2100);
    });

    await waitFor(() => {
      expect(
        screen.getByText(/DingTalk reported the authorization failed or was cancelled/i),
      ).toBeTruthy();
    });
    expect(screen.getByText(/用户拒绝授权/)).toBeTruthy();
    expect(screen.getByRole("button", { name: /Scan again/i })).toBeTruthy();
  });

  it("falls into a terminal session_lost error state when status polling 404s", async () => {
    mockGetStatus.mockRejectedValue(
      new ApiError("install session not found", 404, "Not Found"),
    );

    await openDialog();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(2100);
    });

    await waitFor(() => {
      expect(
        screen.getByText(
          /Install session expired or was lost\. Scan again to start over\./i,
        ),
      ).toBeTruthy();
    });
    // Terminal — no follow-up poll may be scheduled.
    const callsAfterTerminal = mockGetStatus.mock.calls.length;
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5000);
    });
    expect(mockGetStatus.mock.calls.length).toBe(callsAfterTerminal);
  });

  it("renders the QR after a React StrictMode double-mount (parity with the lark dialog regression)", async () => {
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    render(<DingTalkAgentBindButton agentId="agent-1" agentName="Bot" />, {
      wrapper: StrictModeWrapper,
    });
    await user.click(screen.getByRole("button", { name: /Create enterprise bot/i }));
    await user.click(screen.getByTestId("dingtalk-install-start"));

    await waitFor(
      () => {
        expect(screen.getByTestId("qr-code")).toBeTruthy();
      },
      { timeout: 2000 },
    );
    expect(screen.getByTestId("qr-code").getAttribute("data-value")).toBe(
      "https://open-dev.dingtalk.com/fe/app-registration?user_code=MUEU",
    );
  });
});

describe("DingTalkInstallDialog (manual credential flow)", () => {
  beforeEach(resetFixtures);

  it("opens straight into the manual form and creates the install from pasted credentials", async () => {
    const user = userEvent.setup();
    installationsRef.current = {
      installations: [],
      configured: true,
      install_supported: false,
    };
    mockManualInstall.mockResolvedValue({ id: "inst-9" });

    render(<DingTalkAgentBindButton agentId="agent-1" agentName="Bot" />, {
      wrapper: I18nWrapper,
    });
    await user.click(screen.getByRole("button", { name: /Create enterprise bot/i }));

    // Scan is unavailable, so the dialog is on the manual form and never
    // touches the begin endpoint.
    const form = await screen.findByTestId("dingtalk-install-manual-form");
    expect(form).toBeTruthy();
    expect(mockBeginInstall).not.toHaveBeenCalled();

    await user.type(screen.getByLabelText(/AppKey/i), "dingkey123");
    await user.type(screen.getByLabelText(/AppSecret/i), "secret456");
    await user.type(screen.getByLabelText(/Robot Code/i), "robot789");
    await user.click(screen.getByTestId("dingtalk-install-manual-submit"));

    await waitFor(() => {
      expect(mockManualInstall).toHaveBeenCalledWith("workspace-1", "agent-1", {
        clientId: "dingkey123",
        clientSecret: "secret456",
        robotCode: "robot789",
        allowUnbound: false,
      });
      expect(mockInvalidate).toHaveBeenCalled();
      expect(toast.success).toHaveBeenCalled();
    });
  });

  it("blocks submit and surfaces an error when a credential field is empty", async () => {
    const user = userEvent.setup();
    installationsRef.current = {
      installations: [],
      configured: true,
      install_supported: false,
    };

    render(<DingTalkAgentBindButton agentId="agent-1" agentName="Bot" />, {
      wrapper: I18nWrapper,
    });
    await user.click(screen.getByRole("button", { name: /Create enterprise bot/i }));
    await screen.findByTestId("dingtalk-install-manual-form");

    await user.type(screen.getByLabelText(/AppKey/i), "dingkey123");
    // AppSecret intentionally left blank.
    await user.click(screen.getByTestId("dingtalk-install-manual-submit"));

    expect(mockManualInstall).not.toHaveBeenCalled();
    expect(
      screen.getByText(/Enter both the AppKey and the AppSecret/i),
    ).toBeTruthy();
  });

  it("offers the manual form as a fallback from the scan view when the device flow is wired", async () => {
    const user = userEvent.setup();
    mockBeginInstall.mockResolvedValue({
      session_id: "sess-1",
      qr_code_url: "https://open-dev.dingtalk.com/fe/app-registration?user_code=MUEU",
      expires_in_seconds: 300,
      poll_interval_seconds: 2,
    });
    mockGetStatus.mockResolvedValue({ status: "pending" });

    render(<DingTalkAgentBindButton agentId="agent-1" agentName="Bot" />, {
      wrapper: I18nWrapper,
    });
    await user.click(screen.getByRole("button", { name: /Create enterprise bot/i }));

    // Scan configuration first, then generate the QR and switch to the manual form.
    await user.click(screen.getByTestId("dingtalk-install-start"));
    await waitFor(() => expect(screen.getByTestId("qr-code")).toBeTruthy());
    await user.click(screen.getByTestId("dingtalk-install-manual-link"));
    expect(screen.getByTestId("dingtalk-install-manual-form")).toBeTruthy();
  });
});

describe("DingTalkTab (settings panel)", () => {
  beforeEach(resetFixtures);

  it("renders the not-enabled notice when the at-rest key is missing", () => {
    installationsRef.current = {
      installations: [],
      configured: false,
      install_supported: false,
    };
    render(<DingTalkTab />, { wrapper: I18nWrapper });
    expect(screen.getByText(/Enterprise bot unavailable/i)).toBeTruthy();
    expect(screen.getByText(/MULTICA_DINGTALK_SECRET_KEY/)).toBeTruthy();
  });

  it("renders the connected-bots empty state (not coming-soon) when install is unsupported and nothing is installed", () => {
    // Manual install now works whenever configured, so the panel no longer
    // dead-ends on a "coming soon" notice — it points at the agent page.
    installationsRef.current = {
      installations: [],
      configured: true,
      install_supported: false,
    };
    render(<DingTalkTab />, { wrapper: I18nWrapper });
    expect(screen.queryByText(/DingTalk bot installation coming soon/i)).toBeNull();
    expect(screen.getByText(/No enterprise bots connected yet/i)).toBeTruthy();
  });

  it("lists installations by agent identity and disconnects via the API", async () => {
    const user = userEvent.setup();
    agentNameByIdRef.current = new Map([["agent-1", "Patcher"]]);
    installationsRef.current = {
      installations: [activeInstallation],
      configured: true,
      install_supported: true,
    };
    mockDeleteInstallation.mockResolvedValue(undefined);

    render(<DingTalkTab />, { wrapper: I18nWrapper });
    expect(screen.getByText("Patcher")).toBeTruthy();

    await user.click(screen.getByRole("button", { name: /Disconnect/i }));
    // Confirm dialog → the action button carries the same label.
    await user.click(
      screen.getAllByRole("button", { name: /^Disconnect$/i }).at(-1)!,
    );

    await waitFor(() => {
      expect(mockDeleteInstallation).toHaveBeenCalledWith("workspace-1", "inst-1");
      expect(mockInvalidate).toHaveBeenCalled();
      expect(toast.success).toHaveBeenCalled();
    });
  });

  it("retries Router registration without starting a new DingTalk scan", async () => {
    const user = userEvent.setup();
    installationsRef.current = {
      installations: [{
        ...activeInstallation,
        transport_mode: "HTTP_CALLBACK",
        router_status: "failed",
      }],
      configured: true,
      install_supported: true,
    };
    mockRetryRouter.mockResolvedValue({
      ...activeInstallation,
      transport_mode: "HTTP_CALLBACK",
      router_status: "registered",
    });

    render(<DingTalkTab />, { wrapper: I18nWrapper });
    await user.click(screen.getByRole("button", { name: /Retry Router/i }));

    await waitFor(() => {
      expect(mockRetryRouter).toHaveBeenCalledWith("workspace-1", "inst-1");
      expect(mockBeginInstall).not.toHaveBeenCalled();
      expect(mockInvalidate).toHaveBeenCalled();
    });
  });
});
