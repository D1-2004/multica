// @vitest-environment jsdom

import { describe, it, expect, vi, beforeEach } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  render,
  screen,
  fireEvent,
  waitFor,
  within,
} from "@testing-library/react";
import type { AgentRuntime, RuntimeProfile } from "@multica/core/types";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../locales/en/common.json";
import enRuntimes from "../../locales/en/runtimes.json";
import enAgents from "../../locales/en/agents.json";

const TEST_RESOURCES = {
  en: { common: enCommon, runtimes: enRuntimes, agents: enAgents },
};

const mockUpdateRuntime = vi.hoisted(() => vi.fn());
const mockUpdateFCE2BTemplate = vi.hoisted(() => vi.fn());
const mockUseFCE2BTemplates = vi.hoisted(() => vi.fn());
const mockDeleteRuntimeProfile = vi.hoisted(() => vi.fn());
const mockQueryData = vi.hoisted(() => ({
  members: [] as Array<Record<string, unknown>>,
  profiles: [] as RuntimeProfile[],
}));
const mockTemplateQuery = vi.hoisted(() => ({
  data: [] as Array<{
    id?: string;
    build_id?: string;
    name?: string;
    template: string;
    status?: string;
    updated_at?: string;
    providers: string[];
    capabilities: string[];
  }>,
  isLoading: false,
  isError: false,
  error: null as Error | null,
}));

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "ws-1",
}));

vi.mock("@multica/core/api", () => ({
  api: {
    updateRuntime: (...args: unknown[]) => mockUpdateRuntime(...args),
    deleteRuntime: vi.fn(),
    archiveAgentsAndDeleteRuntime: vi.fn(),
    deleteRuntimeProfile: (...args: unknown[]) =>
      mockDeleteRuntimeProfile(...args),
  },
  ApiError: class ApiError extends Error {},
}));

vi.mock("sonner", () => ({
  toast: { error: vi.fn(), success: vi.fn() },
}));

// Pull the bits we want to test directly from the detail file. They aren't
// exported, so we exercise them through RuntimeDetail's DiagnosticsCard.
// Easier path: import the inner components by re-exporting them from a
// shared module. They live in the same file as RuntimeDetail; rather than
// touching the prod file just to ease testing, we test by rendering
// `RuntimeDetail` with a runtime fixture and asserting on the visibility
// UI. To avoid pulling in the entire detail page (which would need
// presence maps, member lists, paths, agents queries, etc.) we stub the
// heavy queries below.
vi.mock("@tanstack/react-query", async () => {
  const actual =
    await vi.importActual<typeof import("@tanstack/react-query")>(
      "@tanstack/react-query",
    );
  return {
    ...actual,
    useQuery: vi.fn((options: { queryKey?: readonly unknown[] }) => {
      const key = options?.queryKey;
      if (key?.[0] === "runtime-profiles") {
        return { data: mockQueryData.profiles, isLoading: false };
      }
      if (key?.[0] === "workspaces" && key?.[2] === "members") {
        return { data: mockQueryData.members, isLoading: false };
      }
      return { data: [], isLoading: false };
    }),
  };
});

vi.mock("@multica/core/auth", () => ({
  useAuthStore: (sel: (s: { user: { id: string } }) => unknown) =>
    sel({ user: { id: "user-me" } }),
}));

vi.mock("@multica/core/runtimes", () => ({
  deriveRuntimeHealth: () => "online",
  runtimeDisplayName: (rt: { name: string; custom_name?: string | null }) =>
    rt.custom_name?.trim() || rt.name,
  runtimeProfileListOptions: (wsId: string) => ({
    queryKey: ["runtime-profiles", wsId],
  }),
  parseFCE2BRuntimeMetadata: (runtime: AgentRuntime) => {
    if (
      runtime.runtime_mode !== "cloud" ||
      runtime.metadata.kind !== "fc-e2b"
    ) {
      return null;
    }
    const stringValue = (key: string) =>
      typeof runtime.metadata[key] === "string"
        ? (runtime.metadata[key] as string)
        : null;
    return {
      kind: "fc-e2b",
      template: stringValue("template"),
      templateId: stringValue("template_id"),
      templateBuildId: stringValue("template_build_id"),
      templateName: stringValue("template_name"),
      templateStatus: stringValue("template_status"),
    };
  },
  isReadyFCE2BTemplate: (template: { id?: string; status?: string }) =>
    Boolean(
      template.id?.trim() && template.status?.trim().toLowerCase() === "ready",
    ),
  useFCE2BTemplates: () => {
    mockUseFCE2BTemplates();
    return mockTemplateQuery;
  },
  useUpdateFCE2BRuntimeTemplate: () => ({
    mutateAsync: (...args: unknown[]) => mockUpdateFCE2BTemplate(...args),
    isPending: false,
  }),
  parseRuntimeProfileBoundConflict: () => null,
  useDeleteRuntimeProfile: () => ({
    mutate: vi.fn(),
    isPending: false,
    mutateAsync: (...args: unknown[]) => mockDeleteRuntimeProfile(...args),
  }),
}));

vi.mock("@multica/core/agents", () => ({
  useWorkspacePresenceMap: () => ({ byAgent: new Map() }),
}));

vi.mock("@multica/core/paths", () => ({
  useWorkspacePaths: () => ({
    runtimes: () => "/runtimes",
    agentDetail: () => "/agents",
  }),
}));

vi.mock("@multica/core/runtimes/mutations", () => ({
  useUpdateRuntime: () => ({
    mutate: (
      args: { runtimeId: string; patch: Record<string, unknown> },
      opts?: { onSuccess?: () => void; onError?: () => void },
    ) => {
      mockUpdateRuntime(args.runtimeId, args.patch);
      opts?.onSuccess?.();
    },
    isPending: false,
  }),
  useDeleteRuntime: () => ({ mutate: vi.fn(), isPending: false, mutateAsync: vi.fn() }),
  useArchiveAgentsAndDeleteRuntime: () => ({
    mutate: vi.fn(),
    isPending: false,
    mutateAsync: vi.fn(),
  }),
}));

// Stubbing ProviderLogo / UsageSection / UpdateSection avoids dragging in
// chart libs and additional query keys we don't care about here.
vi.mock("./provider-logo", () => ({ ProviderLogo: () => null }));
vi.mock("./update-section", () => ({ UpdateSection: () => null }));
vi.mock("./usage-section", () => ({ UsageSection: () => null }));
vi.mock("./shared", () => ({ HealthBadge: () => null }));
vi.mock("../../agents/presence", () => ({
  availabilityConfig: { offline: { dotClass: "", textClass: "" } },
  workloadConfig: { idle: { icon: () => null, textClass: "" } },
}));
vi.mock("../../common/actor-avatar", () => ({ ActorAvatar: () => null }));
vi.mock("../../navigation", () => ({
  AppLink: () => null,
  useNavigation: () => ({ push: vi.fn(), replace: vi.fn() }),
}));

import { RuntimeDetail } from "./runtime-detail";

function makeRuntime(overrides: Partial<AgentRuntime>): AgentRuntime {
  return {
    id: "rt-1",
    workspace_id: "ws-1",
    daemon_id: null,
    name: "Local Runtime",
    runtime_mode: "local",
    provider: "claude",
    launch_header: "",
    status: "online",
    device_info: "host.local",
    metadata: {},
    owner_id: "user-me",
    visibility: "private",
    last_seen_at: "2026-04-27T11:59:50Z",
    created_at: "2026-04-01T00:00:00Z",
    updated_at: "2026-04-01T00:00:00Z",
    ...overrides,
  };
}

function makeProfile(overrides: Partial<RuntimeProfile> = {}): RuntimeProfile {
  return {
    id: "profile-1",
    workspace_id: "ws-1",
    display_name: "Custom Codex",
    protocol_family: "codex",
    command_name: "custom-codex",
    description: null,
    fixed_args: [],
    visibility: "workspace",
    created_by: "user-me",
    enabled: true,
    created_at: "2026-04-01T00:00:00Z",
    updated_at: "2026-04-01T00:00:00Z",
    ...overrides,
  };
}

function renderDetail(runtime: AgentRuntime) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <I18nProvider locale="en" resources={TEST_RESOURCES}>
      <QueryClientProvider client={qc}>
        <RuntimeDetail runtime={runtime} />
      </QueryClientProvider>
    </I18nProvider>,
  );
}

describe("RuntimeDetail visibility section", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockQueryData.members = [];
    mockQueryData.profiles = [];
    mockTemplateQuery.data = [];
    mockTemplateQuery.isLoading = false;
    mockTemplateQuery.isError = false;
    mockTemplateQuery.error = null;
    mockUpdateFCE2BTemplate.mockResolvedValue(undefined);
    mockDeleteRuntimeProfile.mockResolvedValue(undefined);
  });

  it("shows owner-editable visibility choices when the caller owns the runtime", () => {
    renderDetail(makeRuntime({ owner_id: "user-me" }));
    expect(screen.getByText("Visibility")).toBeInTheDocument();
    expect(screen.getByText("Private")).toBeInTheDocument();
    expect(screen.getByText("Public")).toBeInTheDocument();
  });

  it("flips visibility to public when the owner clicks the Public choice", async () => {
    renderDetail(makeRuntime({ owner_id: "user-me", visibility: "private" }));
    fireEvent.click(screen.getByText("Public"));
    await waitFor(() =>
      expect(mockUpdateRuntime).toHaveBeenCalledWith("rt-1", { visibility: "public" }),
    );
  });

  it("renders a read-only visibility chip when the caller cannot edit", () => {
    renderDetail(makeRuntime({ owner_id: "someone-else", visibility: "public" }));
    expect(screen.getByText("Public")).toBeInTheDocument();
    // The editor's "Private" choice button must not render in read-only mode.
    expect(screen.queryByText("Private")).not.toBeInTheDocument();
  });

  // MUL-3352: an owner viewing an online local (self-healing) runtime
  // used to see a disabled Delete button with only a hover tooltip
  // explaining why. The new contract: the button is always clickable
  // for owner/admin; the dialog now carries the self-heal warning.
  it("renders an enabled Delete runtime button for an owner on a self-healing local runtime", () => {
    renderDetail(
      makeRuntime({
        owner_id: "user-me",
        runtime_mode: "local",
        status: "online",
      }),
    );
    const btn = screen.getByRole("button", {
      name: /Delete runtime/i,
    }) as HTMLButtonElement;
    expect(btn.disabled).toBe(false);
  });

  it("hides the Delete runtime button entirely for callers who cannot edit", () => {
    renderDetail(
      makeRuntime({
        owner_id: "someone-else",
        runtime_mode: "local",
        status: "online",
      }),
    );
    expect(
      screen.queryByRole("button", { name: /Delete runtime/i }),
    ).not.toBeInTheDocument();
  });

  it("routes custom runtime deletion through the profile delete dialog for admins", async () => {
    const profile = makeProfile();
    mockQueryData.members = [
      { user_id: "user-me", role: "owner", name: "Me" },
    ];
    mockQueryData.profiles = [profile];

    renderDetail(
      makeRuntime({
        owner_id: "someone-else",
        profile_id: profile.id,
      }),
    );

    fireEvent.click(screen.getByRole("button", { name: /Delete runtime/i }));
    expect(
      screen.getByText("Delete custom runtime from workspace?"),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("alertdialog", {
        name: "Delete custom runtime from workspace?",
      }),
    ).toBeInTheDocument();

    fireEvent.click(
      screen.getByRole("button", { name: "Delete from workspace" }),
    );
    await waitFor(() =>
      expect(mockDeleteRuntimeProfile).toHaveBeenCalledWith(profile.id),
    );
  });

  it("hides custom runtime delete for non-admin runtime owners", () => {
    const profile = makeProfile();
    mockQueryData.profiles = [profile];

    renderDetail(
      makeRuntime({
        owner_id: "user-me",
        profile_id: profile.id,
      }),
    );

    expect(
      screen.queryByRole("button", { name: /Delete runtime/i }),
    ).not.toBeInTheDocument();
  });

  it("shows FC/E2B image details and template controls to workspace admins", async () => {
    mockQueryData.members = [
      { user_id: "user-me", role: "admin", name: "Me" },
    ];
    mockTemplateQuery.data = [
      {
        id: "tpl-current",
        build_id: "build-current",
        name: "Team v1",
        template: "multica-fc-team-v1",
        status: "ready",
        providers: ["hermes"],
        capabilities: [],
      },
      {
        id: "tpl-new",
        build_id: "build-new",
        name: "Team v2",
        template: "multica-fc-team-v2",
        status: "ready",
        providers: ["hermes", "pi"],
        capabilities: ["dws"],
      },
      {
        id: "tpl-rebuilt",
        build_id: "build-rebuilt",
        name: "Team v1 rebuilt",
        template: "multica-fc-team-v1",
        status: "ready",
        providers: ["hermes"],
        capabilities: [],
      },
      {
        id: "tpl-building",
        build_id: "build-building",
        name: "Team v3 building",
        template: "multica-fc-team-v3",
        status: "building",
        providers: ["hermes"],
        capabilities: [],
      },
      {
        build_id: "build-no-id",
        name: "Template without ID",
        template: "multica-fc-team-no-id",
        status: "ready",
        providers: ["hermes"],
        capabilities: [],
      },
    ];

    renderDetail(
      makeRuntime({
        owner_id: "someone-else",
        runtime_mode: "cloud",
        provider: "hermes",
        metadata: {
          kind: "fc-e2b",
          template: "multica-fc-team-v1",
          template_id: "tpl-current",
          template_build_id: "build-current",
          template_name: "Team v1",
          template_status: "ready",
        },
      }),
    );

    expect(screen.getByText("Cloud sandbox image")).toBeInTheDocument();
    expect(screen.getByText("Team v1")).toBeInTheDocument();
    expect(screen.getByText("Hermes")).toBeInTheDocument();
    expect(mockUseFCE2BTemplates).not.toHaveBeenCalled();

    fireEvent.click(
      screen.getByRole("button", { name: "Change template" }),
    );
    expect(mockUseFCE2BTemplates).toHaveBeenCalledTimes(1);
    const dialog = screen.getByRole("dialog", {
      name: "Change cloud sandbox image",
    });
    const current = within(dialog).getByRole("button", {
      name: /Team v1tpl-currentCurrent/,
    });
    const rebuilt = within(dialog).getByRole("button", {
      name: /Team v1 rebuilt/,
    });
    const building = within(dialog).getByRole("button", {
      name: /Team v3 building/,
    });
    const missingId = within(dialog).getByRole("button", {
      name: /Template without ID/,
    });
    expect(current).toBeDisabled();
    expect(rebuilt).toBeEnabled();
    expect(building).toBeDisabled();
    expect(missingId).toBeDisabled();

    fireEvent.click(
      within(dialog).getByRole("button", { name: /Team v2/ }),
    );
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Change template" }),
    );

    await waitFor(() =>
      expect(mockUpdateFCE2BTemplate).toHaveBeenCalledWith({
        runtimeId: "rt-1",
        data: { template_id: "tpl-new" },
      }),
    );
    await waitFor(() =>
      expect(
        screen.queryByRole("dialog", { name: "Change cloud sandbox image" }),
      ).not.toBeInTheDocument(),
    );
  });

  it("keeps FC/E2B image details read-only for a non-admin runtime owner", () => {
    renderDetail(
      makeRuntime({
        owner_id: "user-me",
        runtime_mode: "cloud",
        provider: "opencode",
        metadata: {
          kind: "fc-e2b",
          template: "multica-fc-team-v1",
          template_id: "tpl-current",
          template_name: "Team v1",
        },
      }),
    );

    expect(screen.getByText("Cloud sandbox image")).toBeInTheDocument();
    expect(screen.getByText("OpenCode")).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Change template" }),
    ).not.toBeInTheDocument();
    expect(mockUseFCE2BTemplates).not.toHaveBeenCalled();
  });

  it("does not show cloud image controls for non-FC/E2B runtimes", () => {
    mockQueryData.members = [
      { user_id: "user-me", role: "owner", name: "Me" },
    ];
    renderDetail(makeRuntime({ runtime_mode: "cloud", metadata: {} }));

    expect(screen.queryByText("Cloud sandbox image")).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Change template" }),
    ).not.toBeInTheDocument();
  });

  it("filters templates and reports an empty result", () => {
    mockQueryData.members = [
      { user_id: "user-me", role: "owner", name: "Me" },
    ];
    mockTemplateQuery.data = [
      {
        id: "tpl-new",
        build_id: "build-new",
        name: "Hermes Team v2",
        template: "multica-fc-team-v2",
        status: "ready",
        providers: ["hermes"],
        capabilities: [],
      },
    ];
    renderDetail(
      makeRuntime({
        runtime_mode: "cloud",
        provider: "hermes",
        metadata: { kind: "fc-e2b", template_id: "tpl-current" },
      }),
    );
    fireEvent.click(
      screen.getByRole("button", { name: "Change template" }),
    );
    const dialog = screen.getByRole("dialog", {
      name: "Change cloud sandbox image",
    });
    fireEvent.change(
      within(dialog).getByPlaceholderText("Search template name or ID..."),
      { target: { value: "OpenCode" } },
    );

    expect(within(dialog).queryByText("Hermes Team v2")).not.toBeInTheDocument();
    expect(within(dialog).getByText("No templates available")).toBeInTheDocument();
  });

  it("renders template catalog failures without closing the dialog", () => {
    mockQueryData.members = [
      { user_id: "user-me", role: "owner", name: "Me" },
    ];
    mockTemplateQuery.isError = true;
    mockTemplateQuery.error = new Error("catalog unavailable");
    renderDetail(
      makeRuntime({
        runtime_mode: "cloud",
        provider: "hermes",
        metadata: { kind: "fc-e2b", template_id: "tpl-current" },
      }),
    );
    fireEvent.click(
      screen.getByRole("button", { name: "Change template" }),
    );

    const dialog = screen.getByRole("dialog", {
      name: "Change cloud sandbox image",
    });
    expect(within(dialog).getByText("catalog unavailable")).toBeInTheDocument();
  });

  it("keeps the selected template and dialog open when update fails", async () => {
    mockQueryData.members = [
      { user_id: "user-me", role: "owner", name: "Me" },
    ];
    mockTemplateQuery.data = [
      {
        id: "tpl-new",
        build_id: "build-new",
        name: "Team v2",
        template: "multica-fc-team-v2",
        status: "ready",
        providers: ["hermes"],
        capabilities: [],
      },
    ];
    mockUpdateFCE2BTemplate.mockRejectedValueOnce(new Error("cutover failed"));
    renderDetail(
      makeRuntime({
        runtime_mode: "cloud",
        provider: "hermes",
        metadata: { kind: "fc-e2b", template_id: "tpl-current" },
      }),
    );
    fireEvent.click(
      screen.getByRole("button", { name: "Change template" }),
    );
    const dialog = screen.getByRole("dialog", {
      name: "Change cloud sandbox image",
    });
    fireEvent.click(within(dialog).getByRole("button", { name: /Team v2/ }));
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Change template" }),
    );

    await waitFor(() =>
      expect(mockUpdateFCE2BTemplate).toHaveBeenCalledTimes(1),
    );
    expect(
      screen.getByRole("dialog", { name: "Change cloud sandbox image" }),
    ).toBeInTheDocument();
    expect(
      within(dialog).getByRole("button", { name: "Change template" }),
    ).toBeEnabled();
  });
});
