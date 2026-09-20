// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Agent } from "@multica/core/types";
import type { FilesystemRoot } from "@multica/core/filesystem";
import { renderWithI18n } from "../test/i18n";
import { NavigationProvider, type NavigationAdapter } from "../navigation";
import { FilesPage } from "./files-page";

const mocks = vi.hoisted(() => ({
  roots: { roots: [] as FilesystemRoot[] },
  rootsPending: false,
  rootsError: false,
  refetch: vi.fn(),
  agents: [] as Agent[],
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
      return { data: undefined, isPending: false, isError: false };
    },
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
  mocks.agents = [
    { ...BASE_AGENT, id: FEIDI_ID, name: "Feidi" },
    { ...BASE_AGENT, id: COACH_ID, name: "Coach" },
  ];
});

describe("FilesPage", () => {
  it("lists shared files and agent names instead of raw ids", () => {
    renderPage();

    expect(screen.getByRole("heading", { name: "Files", level: 1 })).toBeInTheDocument();
    expect(screen.getByText("Shared workspace files and private agent disks.")).toBeInTheDocument();
    expect(screen.getByRole("option", { name: /Shared files/ })).toBeInTheDocument();
    expect(screen.getByRole("option", { name: /Feidi/ })).toBeInTheDocument();
    expect(screen.getByRole("option", { name: /Coach/ })).toBeInTheDocument();
    expect(screen.queryByText(FEIDI_ID)).not.toBeInTheDocument();
    expect(screen.getByText("Nothing to show yet")).toBeInTheDocument();
  });

  it("shows an unready state for unprepared shared storage", () => {
    mocks.roots = {
      roots: [{ kind: "shared", provisioned: false, access: "write" }],
    };

    renderPage();

    expect(screen.getByText("Shared storage isn’t ready")).toBeInTheDocument();
    expect(
      screen.getByText("Members and agents will share files here once storage is prepared."),
    ).toBeInTheDocument();
  });

  it("selects an agent disk and links to the agent filesystem view", async () => {
    const user = userEvent.setup();
    renderPage();

    await user.click(screen.getByRole("option", { name: /Feidi/ }));

    expect(screen.getByRole("option", { name: /Feidi/ })).toHaveAttribute(
      "aria-selected",
      "true",
    );
    expect(screen.getByText("Private to Feidi")).toBeInTheDocument();
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
