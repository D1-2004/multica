// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { I18nProvider } from "@multica/core/i18n/react";
import { NavigationProvider } from "../../navigation";
import enCommon from "../../locales/en/common.json";
import enRuntimes from "../../locales/en/runtimes.json";

const TEST_RESOURCES = {
  en: { common: enCommon, runtimes: enRuntimes },
};

const mockChannelQuery = vi.hoisted(() => ({
  data: {
    current: {
      template_id: "template-stable",
      template_build_id: "build-stable",
      template_alias: "stable-image",
      release_id: "release-stable",
    },
    active_release: {
      id: "release-next",
      template_id: "template-next",
      template_build_id: "build-next",
      template_alias: "next-image",
      source_revision: "abcdef",
      note: "",
      actor_user_id: "user-1",
      bootstrap: false,
      status: "awaiting_rollout",
      current_batch: 0,
      target_percentage: 0,
      previous_template_id: "template-stable",
      previous_template_build_id: "build-stable",
      previous_template_alias: "stable-image",
      total_targets: 3,
      updated_targets: 1,
      failed_targets: 0,
      developer_targets: 1,
      developer_updated_targets: 1,
      created_at: "2026-07-28T04:00:00Z",
      updated_at: "2026-07-28T05:00:00Z",
    },
    can_publish: true,
  },
  isLoading: false,
  isError: false,
  error: null as Error | null,
  isFetching: false,
  dataUpdatedAt: Date.parse("2026-07-28T05:00:00Z"),
  refetch: vi.fn(),
}));

const mockRuntimesQuery = vi.hoisted(() => ({
  data: [
    {
      runtime_id: "runtime-alpha",
      workspace_id: "workspace-a",
      workspace_name: "Workspace Alpha",
      runtime_name: "Runtime Alpha",
      provider: "hermes",
      status: "online",
      template_channel: "stable",
      template_alias: "stable-image",
      template_id: "template-stable",
      template_build_id: "build-stable",
      matches_current_stable: true,
      matches_active_release: false,
      active_release_target_status: "",
      updated_at: "2026-07-28T04:30:00Z",
    },
    {
      runtime_id: "runtime-beta",
      workspace_id: "workspace-b",
      workspace_name: "Workspace Beta",
      runtime_name: "Runtime Beta",
      provider: "opencode",
      status: "online",
      template_channel: "stable",
      template_alias: "next-image",
      template_id: "template-next",
      template_build_id: "build-next",
      matches_current_stable: false,
      matches_active_release: true,
      active_release_target_status: "updated",
      updated_at: "2026-07-28T04:40:00Z",
    },
    {
      runtime_id: "runtime-gamma",
      workspace_id: "workspace-b",
      workspace_name: "Workspace Beta",
      runtime_name: "Runtime Gamma",
      provider: "pi",
      status: "offline",
      template_channel: "stable",
      template_alias: "old-image",
      template_id: "template-old",
      template_build_id: "build-old",
      matches_current_stable: false,
      matches_active_release: false,
      active_release_target_status: "pending",
      updated_at: "2026-07-28T04:50:00Z",
    },
  ],
  isLoading: false,
  isError: false,
  error: null as Error | null,
  isFetching: false,
  dataUpdatedAt: Date.parse("2026-07-28T05:00:00Z"),
  refetch: vi.fn(),
}));

vi.mock("@multica/core/paths", () => ({
  useWorkspacePaths: () => ({
    runtimes: () => "/workspace/runtimes",
  }),
}));

vi.mock("@multica/core/runtimes", () => ({
  useFCE2BStableChannel: () => mockChannelQuery,
  useFCE2BStableRuntimes: () => mockRuntimesQuery,
}));

vi.mock("./stable-fc-e2b-release-dialog", () => ({
  StableFCE2BReleaseDialog: () => <div>Stable release dialog</div>,
}));

import { StableFCE2BRuntimeOverviewPage } from "./stable-fc-e2b-runtime-overview-page";

function renderPage() {
  return render(
    <I18nProvider locale="en" resources={TEST_RESOURCES}>
      <NavigationProvider
        value={{
          push: vi.fn(),
          replace: vi.fn(),
          back: vi.fn(),
          pathname: "/workspace/runtimes/stable",
          searchParams: new URLSearchParams(),
          getShareableUrl: (path) => path,
        }}
      >
        <StableFCE2BRuntimeOverviewPage />
      </NavigationProvider>
    </I18nProvider>,
  );
}

describe("StableFCE2BRuntimeOverviewPage", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockChannelQuery.refetch.mockResolvedValue(undefined);
    mockRuntimesQuery.refetch.mockResolvedValue(undefined);
  });

  it("shows stable convergence, release progress, and every Runtime", () => {
    renderPage();

    expect(
      screen.getByRole("heading", {
        name: "All-workspace Runtime overview",
      }),
    ).toBeInTheDocument();
    expect(screen.getAllByText("stable-image").length).toBeGreaterThan(0);
    expect(screen.getAllByText("next-image").length).toBeGreaterThan(0);
    expect(screen.getByText("Runtime Alpha")).toBeInTheDocument();
    expect(screen.getByText("Runtime Beta")).toBeInTheDocument();
    expect(screen.getByText("Runtime Gamma")).toBeInTheDocument();
    expect(screen.getByText("Awaiting developer approval")).toBeInTheDocument();
    expect(screen.getByText("Showing 3 of 3 runtimes")).toBeInTheDocument();
  });

  it("filters Runtime details by search", () => {
    renderPage();

    fireEvent.change(
      screen.getByPlaceholderText(
        "Search workspace, Runtime, template, or build ID...",
      ),
      { target: { value: "Runtime Beta" } },
    );

    expect(screen.queryByText("Runtime Alpha")).not.toBeInTheDocument();
    expect(screen.getByText("Runtime Beta")).toBeInTheDocument();
    expect(screen.queryByText("Runtime Gamma")).not.toBeInTheDocument();
    expect(screen.getByText("Showing 1 of 3 runtimes")).toBeInTheDocument();
  });
});
