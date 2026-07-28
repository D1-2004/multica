// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../locales/en/common.json";
import enRuntimes from "../../locales/en/runtimes.json";

const TEST_RESOURCES = {
  en: { common: enCommon, runtimes: enRuntimes },
};

const mockCreateRelease = vi.hoisted(() => vi.fn());
const mockChannelQuery = vi.hoisted(() => ({
  data: {
    current: null as null | {
      template_id: string;
      template_build_id: string;
      template_alias: string;
      release_id: string;
    },
    active_release: null,
    can_publish: true,
  },
}));
const mockTemplatesQuery = vi.hoisted(() => ({
  data: [] as Array<{
    id: string;
    build_id: string;
    name: string;
    template: string;
    status: string;
    updated_at?: string;
    providers: string[];
    capabilities: string[];
  }>,
  isLoading: false,
  isError: false,
  error: null as Error | null,
}));

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "workspace-1",
}));

vi.mock("@multica/core/runtimes", () => ({
  isReadyFCE2BTemplate: (template: { id?: string; build_id?: string; status?: string }) =>
    Boolean(
      template.id?.trim() &&
        template.build_id?.trim() &&
        template.status?.toLowerCase() === "ready",
    ),
  useFCE2BStableChannel: () => mockChannelQuery,
  useFCE2BTemplates: () => mockTemplatesQuery,
  useCreateFCE2BStableRelease: () => ({
    mutateAsync: (...args: unknown[]) => mockCreateRelease(...args),
    isPending: false,
  }),
  useMutateFCE2BStableRelease: () => ({
    mutateAsync: vi.fn(),
    isPending: false,
  }),
}));

vi.mock("sonner", () => ({
  toast: { error: vi.fn(), success: vi.fn() },
}));

import { StableFCE2BReleaseDialog } from "./stable-fc-e2b-release-dialog";

function renderDialog() {
  return render(
    <I18nProvider locale="en" resources={TEST_RESOURCES}>
      <StableFCE2BReleaseDialog onClose={vi.fn()} />
    </I18nProvider>,
  );
}

function template(
  id: string,
  buildId: string,
  name: string,
  updatedAt: string,
) {
  return {
    id,
    build_id: buildId,
    name,
    template: name,
    status: "READY",
    updated_at: updatedAt,
    providers: ["hermes", "opencode", "pi"],
    capabilities: ["dws", "dws.im_event", "mcp"],
  };
}

describe("StableFCE2BReleaseDialog", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockCreateRelease.mockResolvedValue(undefined);
    mockChannelQuery.data.current = null;
    mockTemplatesQuery.data = [
      template("template-current", "build-current", "Current image", "2026-07-28T04:30:00Z"),
    ];
    mockTemplatesQuery.isLoading = false;
    mockTemplatesQuery.isError = false;
    mockTemplatesQuery.error = null;
  });

  it("initializes with template evidence and an optional note", async () => {
    renderDialog();

    expect(screen.queryByText("Runtime Git commit")).not.toBeInTheDocument();
    expect(screen.queryByText("ACR image digest")).not.toBeInTheDocument();
    expect(screen.getByText("Release note (optional)")).toBeInTheDocument();
    expect(screen.getByText(/Updated/)).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: /Current image/ }));
    fireEvent.click(
      screen.getByRole("button", {
        name: "Verify and initialize stable channel",
      }),
    );

    await waitFor(() =>
      expect(mockCreateRelease).toHaveBeenCalledWith({
        idempotencyKey: expect.any(String),
        data: {
          template_id: "template-current",
          expected_build_id: "build-current",
          note: "",
        },
      }),
    );
  });

  it("shows the current stable build as disabled and offers another build", () => {
    mockChannelQuery.data.current = {
      template_id: "template-current",
      template_build_id: "build-current",
      template_alias: "Current image",
      release_id: "release-current",
    };
    mockTemplatesQuery.data = [
      template("template-current", "build-current", "Current image", "2026-07-28T04:30:00Z"),
      template("template-next", "build-next", "Next image", "2026-07-28T05:30:00Z"),
    ];

    renderDialog();

    const currentTemplate = screen.getByRole("button", { name: /Current image/ });
    const nextTemplate = screen.getByRole("button", { name: /Next image/ });
    const publish = screen.getByRole("button", {
      name: "Verify and start rollout",
    });

    expect(currentTemplate).toBeDisabled();
    expect(screen.getByText("Current stable version")).toBeInTheDocument();
    expect(nextTemplate).toBeEnabled();
    expect(publish).toBeDisabled();

    fireEvent.click(nextTemplate);
    expect(publish).toBeEnabled();
  });
});
