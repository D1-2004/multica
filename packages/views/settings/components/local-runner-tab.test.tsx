// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { I18nProvider } from "@multica/core/i18n/react";
import {
  NavigationProvider,
  type NavigationAdapter,
} from "../../navigation";
import enCommon from "../../locales/en/common.json";
import enSettings from "../../locales/en/settings.json";

const queryState = vi.hoisted(() => ({
  current: {
    data: undefined as unknown,
    isLoading: false,
    isError: false,
    refetch: vi.fn(),
  },
}));
const disconnectMutation = vi.hoisted(() => ({
  mutateAsync: vi.fn(),
  isPending: false,
  variables: undefined as { bindingId: string } | undefined,
}));
const reconnectMutation = vi.hoisted(() => ({
  mutateAsync: vi.fn(),
  isPending: false,
  variables: undefined as { bindingId: string } | undefined,
  reset: vi.fn(),
}));
const revokeMutation = vi.hoisted(() => ({
  mutateAsync: vi.fn(),
  isPending: false,
  variables: undefined as { bindingId: string } | undefined,
}));
const simpleMutation = vi.hoisted(() => ({ mutateAsync: vi.fn(), isPending: false }));
const runnerOptions = vi.hoisted(() => vi.fn());
const disconnectHook = vi.hoisted(() => vi.fn());
const reconnectHook = vi.hoisted(() => vi.fn());
const revokeHook = vi.hoisted(() => vi.fn());
const toastSuccess = vi.hoisted(() => vi.fn());
const toastError = vi.hoisted(() => vi.fn());

vi.mock("@tanstack/react-query", () => ({
  useQuery: () => queryState.current,
  queryOptions: <T,>(options: T) => options,
}));

vi.mock("@multica/core/auth", () => ({
  useAuthStore: (selector: (state: { user: { id: string } }) => unknown) =>
    selector({ user: { id: "user-1" } }),
}));

vi.mock("@multica/core/runner", () => ({
  accountRunnerBindingsOptions: (userId: string) => {
    runnerOptions(userId);
    return { queryKey: ["runner-bindings", "account", userId] };
  },
  useDisconnectAccountRunnerBinding: (userId: string) => {
    disconnectHook(userId);
    return disconnectMutation;
  },
  useCreateAccountRunnerReconnectCommand: (userId: string) => {
    reconnectHook(userId);
    return reconnectMutation;
  },
  useRevokeAccountRunnerBinding: (userId: string) => {
    revokeHook(userId);
    return revokeMutation;
  },
	useCreateAccountRunnerPairing: () => simpleMutation,
	useRenameAccountRunnerMachine: () => simpleMutation,
	useRevokeAccountRunnerMachine: () => simpleMutation,
}));

vi.mock("sonner", () => ({
  toast: { success: toastSuccess, error: toastError },
}));

import { LocalRunnerTab } from "./local-runner-tab";

const bindingId = "11111111-1111-4111-8111-111111111111";
const disconnectedBindingId = "22222222-2222-4222-8222-222222222222";
const machineId = "33333333-3333-4333-8333-333333333333";
const workspaceId = "44444444-4444-4444-8444-444444444444";
const secondWorkspaceId = "55555555-5555-4555-8555-555555555555";
const agentId = "66666666-6666-4666-8666-666666666666";
const secondAgentId = "77777777-7777-4777-8777-777777777777";

const machine = {
  machineId,
  name: "studio-mac",
  os: "darwin",
  arch: "arm64",
  clientVersion: "0.2.0",
  online: true,
  lastSeenAt: "2026-08-24T08:00:00Z",
	mcpServers: [],
	inventoryRevision: "",
  bindings: [
    {
      bindingId,
      workspaceId,
      workspaceName: "Platform",
      workspaceSlug: "platform",
      agentId,
      agentName: "Coder",
      roots: ["/Users/dev/code"],
      disconnected: false,
      boundAt: "2026-08-23T08:00:00Z",
    },
    {
      bindingId: disconnectedBindingId,
      workspaceId: secondWorkspaceId,
      workspaceName: "Review",
      workspaceSlug: "review",
      agentId: secondAgentId,
      agentName: "Reviewer",
      roots: ["/Users/dev/review"],
      disconnected: true,
      boundAt: "2026-08-23T09:00:00Z",
    },
  ],
};

const push = vi.fn();

function renderTab() {
  const navigation: NavigationAdapter = {
    push,
    replace: vi.fn(),
    back: vi.fn(),
    pathname: "/current/settings",
    searchParams: new URLSearchParams("tab=local_runner"),
    getShareableUrl: (path) => path,
  };
  return render(
    <I18nProvider
      locale="en"
      resources={{ en: { common: enCommon, settings: enSettings } }}
    >
      <NavigationProvider value={navigation}>
        <LocalRunnerTab />
      </NavigationProvider>
    </I18nProvider>,
  );
}

beforeEach(() => {
  vi.clearAllMocks();
  queryState.current = {
    data: { machines: [machine] },
    isLoading: false,
    isError: false,
    refetch: vi.fn(),
  };
  disconnectMutation.isPending = false;
  disconnectMutation.variables = undefined;
  disconnectMutation.mutateAsync.mockResolvedValue(undefined);
  reconnectMutation.isPending = false;
  reconnectMutation.variables = undefined;
  reconnectMutation.mutateAsync.mockResolvedValue({
    reconnectCommand: "curl example.test | sh -- --reconnect-token secret",
    expiresAt: "2026-08-24T08:10:00Z",
  });
  revokeMutation.isPending = false;
  revokeMutation.variables = undefined;
  revokeMutation.mutateAsync.mockResolvedValue(undefined);
});

describe("LocalRunnerTab", () => {
  it("keeps machine online status separate from each binding connection state", () => {
    renderTab();

    expect(screen.getAllByText("Online")).toHaveLength(2);
    expect(
      screen.getAllByRole("heading", { name: "studio-mac" }),
    ).toHaveLength(1);
    expect(screen.getByText("Disconnected")).toBeInTheDocument();
    expect(screen.getByText("2 agent bindings")).toBeInTheDocument();
    expect(screen.getByText("/Users/dev/code")).toBeInTheDocument();
    expect(screen.getByText("/Users/dev/review")).toBeInTheDocument();
    expect(screen.getByText("Connect and use your computer")).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Pair machine" }),
    ).toBeInTheDocument();
  });

  it("explains installation, MCP configuration, lifecycle commands, and local file access", () => {
    renderTab();

    expect(
      screen.getByText(/starts Runner automatically after browser approval/i),
    ).toBeInTheDocument();
    expect(screen.getByText("Configure MCP services")).toBeInTheDocument();
    expect(screen.getByText("~/.multica/runner/mcp.json")).toBeInTheDocument();
    expect(screen.getAllByText(/local_machine/).length).toBeGreaterThan(0);
    expect(
      screen.getByText("~/.multica/runner/bin/multica runner start"),
    ).toBeInTheDocument();
    expect(
      screen.getByText("~/.multica/runner/bin/multica runner stop"),
    ).toBeInTheDocument();
    expect(
      screen.getByText("~/.multica/runner/bin/multica runner status"),
    ).toBeInTheDocument();
    expect(
      screen.getAllByText("Local file access directories").length,
    ).toBeGreaterThan(0);
    expect(
      screen.getByText(
        "~/.multica/runner/bin/multica runner configure --directory /absolute/path",
      ),
    ).toBeInTheDocument();
    expect(screen.queryByText("Exposed file roots")).not.toBeInTheDocument();
  });

  it("shows an enabled binding as offline when its machine is offline", () => {
    queryState.current = {
      data: {
        machines: [
          {
            ...machine,
            online: false,
            bindings: [machine.bindings[0]],
          },
        ],
      },
      isLoading: false,
      isError: false,
      refetch: vi.fn(),
    };

    renderTab();

    const offlineBadges = screen.getAllByText("Offline");
    expect(offlineBadges).toHaveLength(2);
    expect(
      offlineBadges.every(
        (badge) => !badge.className.includes("text-success"),
      ),
    ).toBe(true);
    expect(screen.queryByText("Disconnected")).not.toBeInTheDocument();
  });

  it("shows protocol details for MCP servers exposed by a machine", async () => {
    const user = userEvent.setup();
    queryState.current = {
      data: {
        machines: [{
          ...machine,
          mcpServers: [{
            name: "llm-wiki",
            title: "LLM Wiki Desktop",
            description: "Search and read the local knowledge base.",
            version: "1.2.0",
            transport: "stdio",
            availability: "available",
            detailStatus: "available",
            fingerprint: "sha256:wiki",
            tools: [{ name: "search_wiki", title: "Search wiki", description: "Search pages in LLM Wiki." }],
          }],
        }],
      },
      isLoading: false,
      isError: false,
      refetch: vi.fn(),
    };

    renderTab();
    await user.click(screen.getByRole("button", { name: /llm-wiki/i }));

    expect(screen.getByText("Search and read the local knowledge base.")).toBeInTheDocument();
    expect(screen.getByText("Search pages in LLM Wiki.")).toBeInTheDocument();
    expect(screen.getByText("v1.2.0")).toBeInTheDocument();
  });

  it("does not render an invalid last-seen timestamp", () => {
    queryState.current = {
      data: {
        machines: [{ ...machine, lastSeenAt: "not-a-date" }],
      },
      isLoading: false,
      isError: false,
      refetch: vi.fn(),
    };

    renderTab();

    expect(screen.queryByText(/Last seen/)).not.toBeInTheDocument();
    expect(screen.queryByText(/Invalid Date/)).not.toBeInTheDocument();
  });

  it("builds a cross-workspace deep link to the Agent Runner settings", async () => {
    const user = userEvent.setup();
    renderTab();

    const link = screen.getByRole("link", { name: /Reviewer/i });
    expect(link).toHaveAttribute(
      "href",
      `/review/agents/${secondAgentId}?view=runner`,
    );

    await user.click(link);
    expect(push).toHaveBeenCalledWith(
      `/review/agents/${secondAgentId}?view=runner`,
    );
  });

  it("disconnects exactly one Agent binding after confirmation", async () => {
    const user = userEvent.setup();
    renderTab();

    await user.click(
      screen.getByRole("button", {
        name: "Disconnect Coder from studio-mac",
      }),
    );
    const dialog = screen.getByRole("alertdialog");
    expect(
      within(dialog).getByText(/Disconnect "Coder" from "studio-mac"/),
    ).toBeInTheDocument();
    await user.click(
      within(dialog).getByRole("button", { name: "Disconnect" }),
    );

    await waitFor(() =>
      expect(disconnectMutation.mutateAsync).toHaveBeenCalledWith({
        bindingId,
        workspaceId,
        agentId,
      }),
    );
  });

  it("creates a reconnect command only for the disconnected binding", async () => {
    const user = userEvent.setup();
    renderTab();

    await user.click(
      screen.getByRole("button", {
        name: "Reconnect Reviewer to studio-mac",
      }),
    );

    await waitFor(() =>
      expect(reconnectMutation.mutateAsync).toHaveBeenCalledWith({
        bindingId: disconnectedBindingId,
        workspaceId: secondWorkspaceId,
        agentId: secondAgentId,
      }),
    );
    expect(
      await screen.findByText(
        "curl example.test | sh -- --reconnect-token secret",
      ),
    ).toBeInTheDocument();
  });

  it("reports a malformed reconnect response instead of opening an empty dialog", async () => {
    reconnectMutation.mutateAsync.mockResolvedValue(null);
    const user = userEvent.setup();
    renderTab();

    await user.click(
      screen.getByRole("button", {
        name: "Reconnect Reviewer to studio-mac",
      }),
    );

    await waitFor(() => expect(toastError).toHaveBeenCalledTimes(1));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  it("permanently deletes exactly one binding after confirmation", async () => {
    const user = userEvent.setup();
    renderTab();

    await user.click(
      screen.getByRole("button", {
        name: "Delete the Coder binding from studio-mac",
      }),
    );
    const dialog = screen.getByRole("alertdialog");
    await user.click(
      within(dialog).getByRole("button", { name: "Delete binding" }),
    );

    await waitFor(() =>
      expect(revokeMutation.mutateAsync).toHaveBeenCalledWith({
        bindingId,
        workspaceId,
        agentId,
      }),
    );
  });

  it("distinguishes a verified empty inventory from malformed data", async () => {
    queryState.current = {
      data: { machines: [] },
      isLoading: false,
      isError: false,
      refetch: vi.fn(),
    };
    const { unmount } = renderTab();
    expect(
      screen.getByText("No computers connected"),
    ).toBeInTheDocument();
    unmount();

    const refetch = vi.fn();
    queryState.current = {
      data: null,
      isLoading: false,
      isError: false,
      refetch,
    };
    const user = userEvent.setup();
    renderTab();
    expect(
      screen.getByText(/Couldn't load your connected computers/),
    ).toBeInTheDocument();
    expect(
      screen.queryByText("No computers connected"),
    ).not.toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Try again" }));
    expect(refetch).toHaveBeenCalledTimes(1);
  });

  it("keeps the last successful machine list visible during a refresh error", () => {
    queryState.current = {
      data: { machines: [machine] },
      isLoading: false,
      isError: true,
      refetch: vi.fn(),
    };

    renderTab();

    expect(
      screen.getByRole("heading", { name: "studio-mac" }),
    ).toBeInTheDocument();
    expect(
      screen.queryByText(/Couldn't load your connected computers/),
    ).not.toBeInTheDocument();
  });

  it("scopes account queries and mutations to the signed-in user", () => {
    renderTab();

    expect(runnerOptions).toHaveBeenCalledWith("user-1");
    expect(disconnectHook).toHaveBeenCalledWith("user-1");
    expect(reconnectHook).toHaveBeenCalledWith("user-1");
    expect(revokeHook).toHaveBeenCalledWith("user-1");
  });
});
