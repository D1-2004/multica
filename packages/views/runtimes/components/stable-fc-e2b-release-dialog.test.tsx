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
const mockMutateRelease = vi.hoisted(() => ({
  pause: vi.fn(),
  resume: vi.fn(),
  "start-rollout": vi.fn(),
  "advance-rollout": vi.fn(),
  "complete-observation": vi.fn(),
  terminate: vi.fn(),
  rollback: vi.fn(),
}));
const mockChannelQuery = vi.hoisted(() => ({
  data: {
    current: null as null | {
      artifact_ref: string;
      artifact_build_id: string;
      artifact_alias: string;
      artifact_digest: string;
      template_id: string;
      template_build_id: string;
      template_alias: string;
      release_id: string;
    },
    active_release: null as null | Record<string, unknown>,
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
  useCloudSandboxStableChannel: () => mockChannelQuery,
  useFCE2BTemplates: () => mockTemplatesQuery,
  useCreateCloudSandboxStableRelease: () => ({
    mutateAsync: (...args: unknown[]) => mockCreateRelease(...args),
    isPending: false,
  }),
  useMutateCloudSandboxStableRelease: (
    _backend: string,
    action: keyof typeof mockMutateRelease,
  ) => ({
      mutateAsync: (...args: unknown[]) => mockMutateRelease[action](...args),
      isPending: false,
    }),
}));

vi.mock("sonner", () => ({
  toast: { error: vi.fn(), success: vi.fn() },
}));

import { StableFCE2BReleaseDialog } from "./stable-fc-e2b-release-dialog";

function renderDialog(sandboxBackend: "aliyun_fc" | "asb" = "aliyun_fc") {
  return render(
    <I18nProvider locale="en" resources={TEST_RESOURCES}>
      <StableFCE2BReleaseDialog
        sandboxBackend={sandboxBackend}
        onClose={vi.fn()}
      />
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
    Object.values(mockMutateRelease).forEach((mutation) =>
      mutation.mockResolvedValue(undefined),
    );
    mockChannelQuery.data.current = null;
    mockChannelQuery.data.active_release = null;
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
    expect(
      screen.queryByText("Runtime status across all workspaces"),
    ).not.toBeInTheDocument();
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
          sandbox_backend: "aliyun_fc",
          template_id: "template-current",
          expected_build_id: "build-current",
          note: "",
        },
      }),
    );
  });

  it("shows the current stable build separately and allows publishing it again", async () => {
    mockChannelQuery.data.current = {
      artifact_ref: "template-current",
      artifact_build_id: "build-current",
      artifact_alias: "Current image",
      artifact_digest: "",
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
      name: "Verify and update developers",
    });

    expect(currentTemplate).toBeEnabled();
    expect(screen.getByText("Currently published stable version")).toBeInTheDocument();
    expect(nextTemplate).toBeEnabled();
    expect(publish).toBeDisabled();

    fireEvent.click(currentTemplate);
    expect(publish).toBeEnabled();
    fireEvent.click(publish);

    await waitFor(() =>
      expect(mockCreateRelease).toHaveBeenCalledWith({
        idempotencyKey: expect.any(String),
        data: {
          sandbox_backend: "aliyun_fc",
          template_id: "template-current",
          expected_build_id: "build-current",
          note: "",
        },
      }),
    );
  });

  it("waits for developer approval before manually starting the 24-hour rollout", async () => {
    mockChannelQuery.data.current = {
      artifact_ref: "template-current",
      artifact_build_id: "build-current",
      artifact_alias: "Current image",
      artifact_digest: "",
      template_id: "template-current",
      template_build_id: "build-current",
      template_alias: "Current image",
      release_id: "release-current",
    };
    mockChannelQuery.data.active_release = {
      id: "release-next",
      artifact_alias: "Next image",
      template_alias: "Next image",
      status: "awaiting_rollout",
      bootstrap: false,
      developer_targets: 4,
      developer_updated_targets: 4,
      updated_targets: 4,
      total_targets: 60,
      target_percentage: 0,
      validation_error: "",
    };

    renderDialog();

    expect(screen.getByText("Awaiting developer approval")).toBeInTheDocument();
    expect(screen.getByText("Updated 4 / 4")).toBeInTheDocument();
    fireEvent.click(
      screen.getByRole("button", { name: "Start 24-hour rollout" }),
    );

    await waitFor(() =>
      expect(mockMutateRelease["start-rollout"]).toHaveBeenCalledWith(
        "release-next",
      ),
    );
  });

  it("shows the fixed rollout schedule and advances without changing its timestamps", async () => {
    mockChannelQuery.data.current = {
      artifact_ref: "template-current",
      artifact_build_id: "build-current",
      artifact_alias: "Current image",
      artifact_digest: "",
      template_id: "template-current",
      template_build_id: "build-current",
      template_alias: "Current image",
      release_id: "release-current",
    };
    mockChannelQuery.data.active_release = {
      id: "release-next",
      template_alias: "Next image",
      status: "rolling_out",
      bootstrap: false,
      current_batch: 1,
      target_percentage: 5,
      previous_template_alias: "Current image",
      updated_targets: 4,
      total_targets: 61,
      validation_error: "",
      rollout_schedule: [
        {
          batch: 1,
          percentage: 5,
          scheduled_at: "2026-07-29T02:00:00Z",
          kind: "rollout",
        },
        {
          batch: 2,
          percentage: 25,
          scheduled_at: "2026-07-29T04:00:00Z",
          kind: "rollout",
        },
        {
          batch: 3,
          percentage: 50,
          scheduled_at: "2026-07-29T10:00:00Z",
          kind: "rollout",
        },
        {
          batch: 4,
          percentage: 100,
          scheduled_at: "2026-07-29T22:00:00Z",
          kind: "rollout",
        },
        {
          batch: 5,
          percentage: 100,
          scheduled_at: "2026-07-30T02:00:00Z",
          kind: "complete",
        },
      ],
    };

    renderDialog();

    expect(screen.getByText("24-hour rollout schedule")).toBeInTheDocument();
    expect(screen.getByText("Roll out to 5%")).toBeInTheDocument();
    expect(screen.getByText("Roll out to 25%")).toBeInTheDocument();
    expect(screen.getByText("Roll out to 50%")).toBeInTheDocument();
    expect(screen.getByText("Roll out to 100%")).toBeInTheDocument();
    expect(screen.getByText("Complete final observation")).toBeInTheDocument();
    expect(
      screen.getByText(/Manual advance opens the next stage early/),
    ).toBeInTheDocument();
    expect(
      screen.getByText(/switches only the runtimes updated/),
    ).toBeInTheDocument();

    const scheduledTimes = Array.from(document.querySelectorAll("time")).map(
      (element) => element.getAttribute("datetime"),
    );
    expect(scheduledTimes).toEqual([
      "2026-07-29T02:00:00Z",
      "2026-07-29T04:00:00Z",
      "2026-07-29T10:00:00Z",
      "2026-07-29T22:00:00Z",
      "2026-07-30T02:00:00Z",
    ]);

    fireEvent.click(
      screen.getByRole("button", { name: "Advance now to 25%" }),
    );

    await waitFor(() =>
      expect(mockMutateRelease["advance-rollout"]).toHaveBeenCalledWith(
        "release-next",
      ),
    );
    expect(
      Array.from(document.querySelectorAll("time")).map((element) =>
        element.getAttribute("datetime"),
      ),
    ).toEqual(scheduledTimes);
  });

  it("completes final observation and exits the active release", async () => {
    mockChannelQuery.data.current = {
      artifact_ref: "template-current",
      artifact_build_id: "build-current",
      artifact_alias: "Current image",
      artifact_digest: "",
      template_id: "template-current",
      template_build_id: "build-current",
      template_alias: "Current image",
      release_id: "release-current",
    };
    mockChannelQuery.data.active_release = {
      id: "release-next",
      template_alias: "Next image",
      status: "observing",
      bootstrap: false,
      current_batch: 4,
      target_percentage: 100,
      previous_template_alias: "Current image",
      updated_targets: 3,
      total_targets: 3,
      validation_error: "",
    };

    renderDialog();

    expect(screen.getByText("Final observation")).toBeInTheDocument();
    expect(
      screen.getByText(/marks this template as the current stable version/),
    ).toBeInTheDocument();
    fireEvent.click(
      screen.getByRole("button", { name: "Complete observation" }),
    );

    await waitFor(() =>
      expect(mockMutateRelease["complete-observation"]).toHaveBeenCalledWith(
        "release-next",
      ),
    );
  });

  it("submits an immutable ASB image with Aone and source evidence", async () => {
    const digest = `sha256:${"a".repeat(64)}`;
    const commit = "b".repeat(40);
    renderDialog("asb");

    fireEvent.change(
      screen.getByLabelText("Immutable OCI image reference"),
      {
        target: {
          value: `hub.docker.alibaba-inc.com/aone-base-global/multica-asb-runtime@${digest}`,
        },
      },
    );
    fireEvent.change(screen.getByLabelText("Aone build ID"), {
      target: { value: "56309841" },
    });
    fireEvent.change(screen.getByLabelText("Image digest"), {
      target: { value: digest },
    });
    fireEvent.change(screen.getByLabelText("Source commit"), {
      target: { value: commit },
    });

    fireEvent.click(
      screen.getByRole("button", {
        name: "Verify and initialize stable channel",
      }),
    );

    await waitFor(() =>
      expect(mockCreateRelease).toHaveBeenCalledWith({
        idempotencyKey: expect.any(String),
        data: {
          sandbox_backend: "asb",
          artifact_ref: `hub.docker.alibaba-inc.com/aone-base-global/multica-asb-runtime@${digest}`,
          artifact_build_id: "56309841",
          artifact_digest: digest,
          git_commit: commit,
          note: "",
        },
      }),
    );
  });
});
