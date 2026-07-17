import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";

const {
  mockDingTalkLogin,
  mockCloseDingTalkPage,
  mockGetConfig,
  mockGetDingTalkInstallStatus,
  mockGetFDEOnboarding,
  mockOpenDingTalkInstallPage,
  mockProvisionFDEOnboarding,
  mockReplaceCurrentPage,
  mockSearchParams,
  mockSetToken,
  mockSetUser,
} = vi.hoisted(() => ({
  mockDingTalkLogin: vi.fn(),
  mockCloseDingTalkPage: vi.fn(),
  mockGetConfig: vi.fn(),
  mockGetDingTalkInstallStatus: vi.fn(),
  mockGetFDEOnboarding: vi.fn(),
  mockOpenDingTalkInstallPage: vi.fn(),
  mockProvisionFDEOnboarding: vi.fn(),
  mockReplaceCurrentPage: vi.fn(),
  mockSearchParams: { current: new URLSearchParams() },
  mockSetToken: vi.fn(),
  mockSetUser: vi.fn(),
}));

vi.mock("next/navigation", () => ({
  useSearchParams: () => mockSearchParams.current,
}));

vi.mock("@multica/core/auth", () => ({
  useAuthStore: (selector: (state: { setUser: typeof mockSetUser }) => unknown) =>
    selector({ setUser: mockSetUser }),
}));

vi.mock("@multica/core/api", () => ({
  api: {
    fdeDingtalkLogin: mockDingTalkLogin,
    getConfig: mockGetConfig,
    getDingTalkInstallStatus: mockGetDingTalkInstallStatus,
    getFDEOnboarding: mockGetFDEOnboarding,
    provisionFDEOnboarding: mockProvisionFDEOnboarding,
    setToken: mockSetToken,
  },
}));

vi.mock("./navigation", () => ({
  closeDingTalkPage: mockCloseDingTalkPage,
  openDingTalkInstallPage: mockOpenDingTalkInstallPage,
  replaceCurrentPage: mockReplaceCurrentPage,
}));

import FDEStartPage from "./page";

const workspace = (id: string, name: string, slug: string) => ({
  id,
  name,
  slug,
  description: null,
  context: null,
  settings: {},
  repos: [],
  issue_prefix: "",
  avatar_url: null,
  created_at: "2026-07-16T00:00:00Z",
  updated_at: "2026-07-16T00:00:00Z",
});

describe("FDEStartPage DingTalk authentication", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    localStorage.clear();
    sessionStorage.clear();
    mockSearchParams.current = new URLSearchParams();
    mockGetConfig.mockResolvedValue({ dingtalk_client_id: "ding-client" });
    mockDingTalkLogin.mockResolvedValue({
      token: "fde-token",
      user: { id: "user-1", name: "FDE User" },
    });
    mockGetFDEOnboarding.mockResolvedValue({
      configured: true,
      dedicated: true,
      workspaces: [
        workspace("workspace-1", "研发空间", "engineering"),
        workspace("workspace-2", "产品空间", "product"),
      ],
    });
  });

  it("restarts OAuth instead of rejecting a quick-init callback without local state", async () => {
    mockSearchParams.current = new URLSearchParams(
      "authCode=external-code&code=external-code&state=external-state",
    );

    render(<FDEStartPage />);

    await waitFor(() => expect(mockGetConfig).toHaveBeenCalledOnce());
    expect(mockDingTalkLogin).not.toHaveBeenCalled();
    expect(mockReplaceCurrentPage).toHaveBeenCalledWith(
      expect.stringContaining("https://login.dingtalk.com/oauth2/auth?"),
    );
    expect(screen.queryByText("登录状态已失效，请重新打开开通链接")).not.toBeInTheDocument();
  });

  it("accepts a matching OAuth state restored from localStorage", async () => {
    mockSearchParams.current = new URLSearchParams(
      "authCode=valid-code&state=expected-state",
    );
    localStorage.setItem("multica_fde_oauth_state", "expected-state");

    render(<FDEStartPage />);

    await waitFor(() => expect(mockDingTalkLogin).toHaveBeenCalledWith("valid-code"));
    expect(mockSetToken).toHaveBeenCalledWith("fde-token");
    expect(mockSetUser).toHaveBeenCalledWith({ id: "user-1", name: "FDE User" });
    expect(localStorage.getItem("multica_fde_oauth_state")).toBeNull();
  });
});

describe("FDEStartPage DingTalk installation navigation", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    localStorage.clear();
    sessionStorage.clear();
    sessionStorage.setItem("multica_fde_dingtalk_authenticated", "1");
    mockSearchParams.current = new URLSearchParams();
    mockGetFDEOnboarding.mockResolvedValue({
      configured: true,
      dedicated: true,
      workspaces: [workspace("workspace-1", "研发空间", "engineering")],
    });
    mockProvisionFDEOnboarding.mockResolvedValue({
      workspace: workspace("workspace-1", "研发空间", "engineering"),
      runtime_id: "runtime-1",
      agent_id: "agent-1",
      agent_created: true,
      install_complete: false,
      install: {
        session_id: "session-1",
        qr_code_url: "https://open-dev.dingtalk.com/fe/app-registration?user_code=abc",
        expires_in_seconds: 300,
        poll_interval_seconds: 60,
      },
    });
    mockGetDingTalkInstallStatus.mockResolvedValue({ status: "pending" });
  });

  it("opens the registration URL through the mobile-safe navigation helper", async () => {
    render(<FDEStartPage />);

    await waitFor(() =>
      expect(mockOpenDingTalkInstallPage).toHaveBeenCalledWith(
        "https://open-dev.dingtalk.com/fe/app-registration?user_code=abc",
      ),
    );
  });
});

describe("FDEStartPage dedicated workspace onboarding", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    localStorage.clear();
    sessionStorage.clear();
    mockSearchParams.current = new URLSearchParams();
    sessionStorage.setItem("multica_fde_dingtalk_authenticated", "1");
    mockGetFDEOnboarding.mockResolvedValue({
      configured: true,
      dedicated: true,
      workspaces: [],
    });
    mockProvisionFDEOnboarding.mockResolvedValue({
      workspace: workspace("workspace-fde", "我的 FDE 工作区", "my-fde-workspace"),
      runtime_id: "runtime-1",
      agent_id: "agent-1",
      agent_created: true,
      install_complete: true,
    });
  });

  it("always asks for a new dedicated workspace without rendering existing choices", async () => {
    const user = userEvent.setup();
    render(<FDEStartPage />);

    expect(await screen.findByText("创建专属 FDE 开发者工作空间")).toBeVisible();
    expect(screen.queryByRole("radiogroup")).not.toBeInTheDocument();
    expect(screen.getByText(/不会修改你已有的工作区/)).toBeVisible();

    await user.type(screen.getByLabelText("工作区名称"), "我的 FDE 工作区");
    await user.click(screen.getByRole("button", { name: "创建并继续" }));

    await waitFor(() => expect(mockProvisionFDEOnboarding).toHaveBeenCalledWith({
      workspace_name: "我的 FDE 工作区",
    }));
  });

  it("resumes the recorded FDE workspace and only offers returning to DingTalk when complete", async () => {
    const user = userEvent.setup();
    const existing = workspace("workspace-fde", "之前创建的 FDE 工作区", "previous-fde");
    mockGetFDEOnboarding.mockResolvedValue({
      configured: true,
      dedicated: true,
      workspaces: [existing],
    });
    mockProvisionFDEOnboarding.mockResolvedValue({
      workspace: existing,
      runtime_id: "runtime-1",
      agent_id: "agent-1",
      agent_created: false,
      install_complete: true,
    });

    render(<FDEStartPage />);

    expect(await screen.findByText("初始化已完成")).toBeVisible();
    expect(screen.getByText("之前创建的 FDE 工作区")).toBeVisible();
    expect(screen.queryByText("进入 FDE 工作空间")).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "返回钉钉" }));

    expect(mockProvisionFDEOnboarding).toHaveBeenCalledWith({ workspace_id: "workspace-fde" });
    expect(mockCloseDingTalkPage).toHaveBeenCalledOnce();
  });
});
