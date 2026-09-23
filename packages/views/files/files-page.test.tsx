// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Agent, AgentRuntime } from "@multica/core/types";
import {
  SHARED_DISK_REQUIRED_RUNTIME_IMAGE,
  type FilesystemRoot,
} from "@multica/core/filesystem";
import { renderWithI18n } from "../test/i18n";
import { NavigationProvider, type NavigationAdapter } from "../navigation";
import { FilesPage } from "./files-page";

const mocks = vi.hoisted(() => ({
  roots: { roots: [] as FilesystemRoot[] },
  rootsPending: false,
  rootsError: false,
  refetch: vi.fn(),
  agents: [] as Agent[],
  runtimes: [] as AgentRuntime[],
  entries: {
    root: "shared",
    path: ".",
    offset: 0,
    limit: 200,
    entries: [] as Array<{ name: string; path: string; is_dir: boolean; size_bytes?: number }>,
    count: 0,
    truncated: false,
    next_offset: null as number | null,
  },
}));

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "ws-1",
}));

vi.mock("@multica/core/paths", () => ({
  useWorkspacePaths: () => ({
    agentDetail: (id: string) => `/acme/agents/${id}`,
    files: () => "/acme/files",
  }),
}));

vi.mock("@multica/core/workspace/avatar-url", () => ({
  resolvePublicFileUrl: (value: string | null | undefined) => value ?? null,
}));

vi.mock("@tanstack/react-query", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@tanstack/react-query")>();
  return {
    ...actual,
    useQuery: (options: { queryKey?: readonly unknown[] }) => {
      const key = options.queryKey ?? [];
      if (key[3] === "entries") {
        return {
          data: mocks.entries,
          isPending: false,
          isError: false,
        };
      }
      if (key[2] === "filesystem") {
        return {
          data: mocks.rootsPending || mocks.rootsError ? undefined : mocks.roots,
          isPending: mocks.rootsPending,
          isError: mocks.rootsError,
          refetch: mocks.refetch,
        };
      }
      if (key[2] === "agents") {
        return { data: mocks.agents, isPending: false, isError: false };
      }
      if (key[0] === "runtimes") {
        return { data: mocks.runtimes, isPending: false, isError: false };
      }
      return { data: undefined, isPending: false, isError: false };
    },
    useMutation: () => ({
      mutateAsync: vi.fn().mockResolvedValue(undefined),
      isPending: false,
    }),
    useQueryClient: () => ({ invalidateQueries: vi.fn() }),
  };
});

const BASE_AGENT: Agent = {
  id: "agent-base",
  workspace_id: "ws-1",
  runtime_id: "runtime-1",
  name: "Base Agent",
  description: "",
  instructions: "",
  avatar_url: null,
  runtime_mode: "cloud",
  runtime_config: {},
  custom_args: [],
  visibility: "workspace",
  permission_mode: "private",
  invocation_targets: [],
  status: "idle",
  max_concurrent_tasks: 1,
  model: "claude",
  owner_id: "user-1",
  skills: [],
  created_at: "2026-06-01T00:00:00Z",
  updated_at: "2026-06-01T00:00:00Z",
  archived_at: null,
  archived_by: null,
};

const FEIDI_ID = "11111111-1111-1111-1111-111111111111";
const COACH_ID = "22222222-2222-2222-2222-222222222222";

function makeAdapter(): NavigationAdapter {
  return {
    push: vi.fn(),
    replace: vi.fn(),
    back: vi.fn(),
    pathname: "/acme/files",
    searchParams: new URLSearchParams(),
    getShareableUrl: (path) => path,
  };
}

function renderPage() {
  return renderWithI18n(
    <NavigationProvider value={makeAdapter()}>
      <FilesPage />
    </NavigationProvider>,
  );
}

beforeEach(() => {
  mocks.roots = {
    roots: [
      { kind: "shared", provisioned: true, access: "write" },
      { kind: "agent", id: FEIDI_ID, provisioned: true, access: "write" },
      { kind: "agent", id: COACH_ID, provisioned: true, access: "write" },
    ],
  };
  mocks.rootsPending = false;
  mocks.rootsError = false;
  mocks.refetch.mockReset();
  mocks.entries = {
    root: "shared",
    path: ".",
    offset: 0,
    limit: 200,
    entries: [],
    count: 0,
    truncated: false,
    next_offset: null,
  };
  mocks.agents = [
    { ...BASE_AGENT, id: FEIDI_ID, name: "Feidi" },
    { ...BASE_AGENT, id: COACH_ID, name: "Coach" },
  ];
  mocks.runtimes = [];
});

function runtimeWithImage(templateName: string | null): AgentRuntime {
  return {
    id: "runtime-1",
    workspace_id: "ws-1",
    daemon_id: null,
    name: "PI",
    runtime_mode: "cloud",
    provider: "pi",
    launch_header: "",
    status: "online",
    device_info: "",
    metadata: templateName
      ? { kind: "fc-e2b", template_name: templateName, template_alias: templateName }
      : { kind: "fc-e2b" },
    owner_id: "user-1",
    visibility: "private",
    last_seen_at: null,
    created_at: "2026-06-01T00:00:00Z",
    updated_at: "2026-06-01T00:00:00Z",
  };
}

describe("FilesPage", () => {
  it("lists shared files and agent names instead of raw ids", () => {
    renderPage();

    expect(screen.getByRole("heading", { name: "Files", level: 1 })).toBeInTheDocument();
    expect(screen.getByText("Shared workspace files and private agent disks.")).toBeInTheDocument();
    expect(screen.getByRole("option", { name: /Shared files/ })).toBeInTheDocument();
    expect(screen.getByRole("option", { name: /Feidi/ })).toBeInTheDocument();
    expect(screen.getByRole("option", { name: /Coach/ })).toBeInTheDocument();
    expect(screen.queryByText(FEIDI_ID)).not.toBeInTheDocument();
    expect(screen.getByRole("option", { name: /Shared files/ })).toHaveAttribute(
      "aria-selected",
      "true",
    );
    expect(screen.getByText("This folder is empty")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "New folder" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Upload" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Upload folder" })).toBeInTheDocument();
    expect(screen.queryByText("No private disks yet")).not.toBeInTheDocument();
  });

  it("lists files in the shared folder and previews instead of downloading", async () => {
    mocks.entries = {
      ...mocks.entries,
      entries: [{ name: "notes.md", path: "notes.md", is_dir: false, size_bytes: 12 }],
      count: 1,
    };
    const user = userEvent.setup();
    renderPage();
    expect(screen.getByText("notes.md")).toBeInTheDocument();
    expect(screen.queryByText("This folder is empty")).not.toBeInTheDocument();
    await user.click(screen.getByText("notes.md"));
    expect(screen.getByRole("button", { name: "Download" })).toBeInTheDocument();
    expect(screen.getAllByText("12 B").length).toBeGreaterThan(0);
  });

  it("shows an unready state for unprepared shared storage", () => {
    mocks.roots = {
      roots: [{ kind: "shared", provisioned: false, access: "write" }],
    };

    renderPage();

    expect(screen.getByRole("option", { name: /Shared files/ })).toHaveAttribute(
      "aria-selected",
      "true",
    );
    expect(screen.getByText("This folder is empty")).toBeInTheDocument();
    expect(screen.queryByText("No private disks yet")).not.toBeInTheDocument();
  });

  it("prompts when the agent runtime image does not match the shared disk", async () => {
    mocks.runtimes = [
      runtimeWithImage("multica-m7-va2eb67817f146ef4-r1-6ccf66"),
    ];
    const user = userEvent.setup();
    renderPage();

    expect(screen.queryByRole("status")).not.toBeInTheDocument();
    await user.click(screen.getByRole("option", { name: /Feidi/ }));

    const notice = screen.getByRole("status");
    expect(notice).toHaveTextContent(SHARED_DISK_REQUIRED_RUNTIME_IMAGE);
    expect(notice).toHaveTextContent("multica-m7-va2eb67817f146ef4-r1-6ccf66");
    expect(notice).toHaveTextContent("do not match");
  });

  it("prompts when the bound runtime image is unknown", async () => {
    mocks.runtimes = [runtimeWithImage(null)];
    const user = userEvent.setup();
    renderPage();

    await user.click(screen.getByRole("option", { name: /Feidi/ }));

    const notice = screen.getByRole("status");
    expect(notice).toHaveTextContent(SHARED_DISK_REQUIRED_RUNTIME_IMAGE);
    expect(notice).toHaveTextContent("unknown");
    expect(notice).not.toHaveTextContent("do not match");
  });

  it("does not prompt when the runtime is not an FC image", async () => {
    mocks.runtimes = [
      {
        ...runtimeWithImage("local-runtime"),
        runtime_mode: "local",
        metadata: { template_name: "local-runtime" },
      },
    ];
    const user = userEvent.setup();
    renderPage();

    await user.click(screen.getByRole("option", { name: /Feidi/ }));

    expect(screen.queryByRole("status")).not.toBeInTheDocument();
  });

  it("does not prompt when the bound runtime image satisfies the requirement", async () => {
    mocks.runtimes = [runtimeWithImage(SHARED_DISK_REQUIRED_RUNTIME_IMAGE)];
    const user = userEvent.setup();
    renderPage();

    await user.click(screen.getByRole("option", { name: /Feidi/ }));

    expect(screen.queryByRole("status")).not.toBeInTheDocument();
    expect(screen.queryByText(SHARED_DISK_REQUIRED_RUNTIME_IMAGE)).not.toBeInTheDocument();
  });

  it("selects an agent disk and links to the agent filesystem view", async () => {
    const user = userEvent.setup();
    renderPage();

    await user.click(screen.getByRole("option", { name: /Feidi/ }));

    expect(screen.getByRole("option", { name: /Feidi/ })).toHaveAttribute(
      "aria-selected",
      "true",
    );
    expect(screen.getByText("This folder is empty")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Open agent" })).toHaveAttribute(
      "href",
      `/acme/agents/${FEIDI_ID}?view=filesystem`,
    );
  });

  it("filters agent disks by name", async () => {
    const user = userEvent.setup();
    renderPage();

    await user.type(screen.getByLabelText("Search disks"), "Coach");

    expect(screen.getByRole("option", { name: /Coach/ })).toBeInTheDocument();
    expect(screen.queryByRole("option", { name: /Feidi/ })).not.toBeInTheDocument();
  });

  it("retries after a load error", async () => {
    mocks.rootsError = true;
    const user = userEvent.setup();
    renderPage();

    expect(screen.getByRole("alert")).toHaveTextContent("Couldn’t load disks");
    await user.click(screen.getByRole("button", { name: "Try again" }));
    expect(mocks.refetch).toHaveBeenCalled();
  });
});
