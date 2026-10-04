import { useEffect } from "react";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { TagState } from "@multica/core/tag";
import { renderWithI18n } from "../test/i18n";
import { TagPage } from "./tag-page";

type DetailProps = { agentId: string; tagView?: { role: string; embedded?: boolean; tab?: string; onDirtyChange?: (dirty: boolean) => void } };

const { detailProps, navigation, tagQuery, renameMutate, deleteMutate, dirtyOnMount } = vi.hoisted(() => ({
  renameMutate: vi.fn(),
  deleteMutate: vi.fn(),
  dirtyOnMount: { current: false },
  detailProps: { current: [] as DetailProps[] },
  navigation: {
    current: { pathname: "/acme/tag", searchParams: new URLSearchParams(), replace: (() => undefined) as (url: string) => void },
  },
  tagQuery: { current: { data: undefined as TagState | undefined, isLoading: false } },
}));

vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws-1" }));
vi.mock("@multica/core/paths", () => ({
  useWorkspacePaths: () => ({ tag: () => "/acme/tag", settings: () => "/acme/settings" }),
}));
vi.mock("@multica/core/workspace/queries", () => ({
  agentListOptions: () => ({ queryKey: ["agents"], queryFn: () => [{ id: "template-1", runtime_id: "rt-1" }] }),
}));
vi.mock("@multica/core/runtimes", () => ({
  runtimeListOptions: () => ({ queryKey: ["runtimes"], queryFn: () => [{ id: "rt-1", provider: "pi" }] }),
}));
vi.mock("@multica/core/tag", () => ({
  useWorkspaceTag: () => tagQuery.current,
  useAdoptTagTenant: () => ({ mutate: vi.fn(), isPending: false }),
  useApplyTag: () => ({ mutate: vi.fn(), isPending: false }),
  useCreateTagTenant: () => ({ mutate: vi.fn(), isPending: false }),
  useDeleteTagTenant: () => ({ mutate: deleteMutate, isPending: false }),
  useRenameTagTenant: () => ({ mutate: renameMutate, isPending: false }),
}));
vi.mock("../navigation", () => ({
  AppLink: ({ children, href }: { children: React.ReactNode; href: string }) => <a href={href}>{children}</a>,
  useNavigation: () => ({
    pathname: navigation.current.pathname,
    searchParams: navigation.current.searchParams,
    replace: navigation.current.replace,
    push: vi.fn(),
  }),
}));
vi.mock("../agents/components/agent-detail-page", () => ({
  AgentDetailPage: (props: DetailProps) => {
    detailProps.current.push(props);
    const onDirty = props.tagView?.onDirtyChange;
    useEffect(() => {
      if (dirtyOnMount.current) onDirty?.(true);
    }, [onDirty]);
    return <div data-testid="agent-detail" data-agent={props.agentId} data-role={props.tagView?.role} data-tab={props.tagView?.tab} />;
  },
}));
vi.mock("./tenant-config", () => ({ TagTenantConfig: () => null }));

const tenant = {
  id: "t-1",
  name: "Think测试组织",
  employeeAgentId: "employee-1",
  employeeName: "Tag · Think测试组织",
  employeeArchived: false,
  bound: true,
  orgId: "177928186",
  organizationName: "Think测试组织",
  digitalEmployeeName: "QwenTag",
  appliedRevision: 2,
  appliedAt: null,
  createdAt: "2026-10-01T00:00:00Z",
};

function stateWith(overrides: Partial<TagState> = {}): TagState {
  return {
    tag: {
      agentId: "template-1",
      name: "Tag",
      description: "",
      avatarUrl: null,
      runtimeMode: "cloud",
      sidebarVisible: true,
      latestRevision: 2,
      hasUnpublishedChanges: false,
      createdAt: "2026-10-01T00:00:00Z",
    },
    canOperate: true,
    canManage: true,
    tenants: [tenant],
    ...overrides,
  };
}

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return renderWithI18n(
    <QueryClientProvider client={client}>
      <TagPage />
    </QueryClientProvider>,
  );
}

function setUrl(search: string, replace: (url: string) => void = () => undefined) {
  navigation.current = { pathname: "/acme/tag", searchParams: new URLSearchParams(search), replace };
}

describe("TagPage", () => {
  beforeEach(() => {
    renameMutate.mockReset();
    deleteMutate.mockReset();
    dirtyOnMount.current = false;
    detailProps.current = [];
    setUrl("");
  });

  it("tells members without operator rights that only operators create a Tag", () => {
    tagQuery.current = { data: { tag: null, canOperate: false, canManage: false, tenants: [] }, isLoading: false };
    renderPage();
    expect(screen.getByText("This workspace has no Tag yet")).toBeTruthy();
    expect(screen.getByText("Only platform operators can create a Tag.")).toBeTruthy();
    expect(screen.queryByText("Open settings")).toBeNull();
  });

  it("points operators at Settings → Tag", () => {
    tagQuery.current = { data: { tag: null, canOperate: true, canManage: false, tenants: [] }, isLoading: false };
    renderPage();
    expect(screen.getByText("Open settings").closest("a")?.getAttribute("href")).toBe("/acme/settings?tab=tag");
  });

  it("splits one tab bar: shared configuration on the left, the tenant on the right", () => {
    tagQuery.current = { data: stateWith(), isLoading: false };
    renderPage();
    const bar = screen.getByRole("tablist", { name: "Tag" });
    expect(within(bar).getAllByRole("tab").map((tab) => tab.textContent)).toEqual([
      "Instructions",
      "Skills",
      "Connectors",
      "Runtime",
      "Tenant configuration",
      "Scenes",
      "Recent work",
    ]);
    // Opens on the shared instructions, full width.
    const detail = screen.getByTestId("agent-detail");
    expect(detail.getAttribute("data-agent")).toBe("template-1");
    expect(detail.getAttribute("data-tab")).toBe("instructions");
    expect(within(bar).getByRole("tab", { name: "Instructions" })).toHaveAttribute("aria-selected", "true");
  });

  it("lists tenants on a tenant tab until one is picked", () => {
    tagQuery.current = { data: stateWith(), isLoading: false };
    setUrl("tab=scenes");
    renderPage();
    expect(screen.queryByTestId("agent-detail")).toBeNull();
    expect(screen.getByRole("button", { name: /Think测试组织/ })).toBeTruthy();
  });

  it("shows the selected tenant's employee with its Agent ID", () => {
    tagQuery.current = { data: stateWith(), isLoading: false };
    setUrl("tag_tenant=t-1");
    renderPage();
    const detail = screen.getByTestId("agent-detail");
    expect(detail.getAttribute("data-agent")).toBe("employee-1");
    expect(detail.getAttribute("data-role")).toBe("employee");
    expect(detail.getAttribute("data-tab")).toBe("digital_employee");
    expect(screen.getByText("employee-1")).toBeTruthy();
    // The OrgId shows in the tenant switcher and in the tenant strip.
    expect(screen.getAllByText("177928186").length).toBeGreaterThanOrEqual(2);
  });

  it("switches tabs through the URL and drops a scene selection when leaving scenes", async () => {
    const user = userEvent.setup();
    const replace = vi.fn();
    tagQuery.current = { data: stateWith(), isLoading: false };
    setUrl("tag_tenant=t-1&tab=scenes&tenant=dingA&node=scene%3Acid", replace);
    renderPage();
    await user.click(screen.getByRole("tab", { name: "Skills" }));
    expect(replace).toHaveBeenLastCalledWith("/acme/tag?tag_tenant=t-1&tab=skills");
  });

  it("guards unsaved edits when switching tabs", async () => {
    const user = userEvent.setup();
    const replace = vi.fn();
    dirtyOnMount.current = true;
    tagQuery.current = { data: stateWith(), isLoading: false };
    setUrl("tab=instructions", replace);
    renderPage();
    await user.click(screen.getByRole("tab", { name: "Skills" }));
    expect(replace).not.toHaveBeenCalled();
    expect(await screen.findByRole("alertdialog")).toBeTruthy();
  });

  it("asks to apply when the template has unpublished changes", () => {
    const base = stateWith();
    tagQuery.current = {
      data: { ...base, tag: base.tag ? { ...base.tag, hasUnpublishedChanges: true } : null },
      isLoading: false,
    };
    renderPage();
    expect(
      screen.getByText("The shared configuration has changes that are not applied to any tenant yet."),
    ).toBeTruthy();
  });

  it("manages tenants: renames one and deletes it with its employee archived", async () => {
    const user = userEvent.setup();
    tagQuery.current = { data: stateWith(), isLoading: false };
    renderPage();

    await user.click(screen.getByRole("button", { name: "Manage tenants…" }));
    const manage = await screen.findByRole("dialog", { name: "Manage tenants" });

    const name = within(manage).getByLabelText("Enterprise name");
    await user.clear(name);
    await user.type(name, "Think");
    await user.click(within(manage).getByRole("button", { name: "Save" }));
    expect(renameMutate).toHaveBeenCalledWith({ tenantId: "t-1", name: "Think" }, expect.anything());

    await user.click(within(manage).getByRole("button", { name: "Delete tenant Think测试组织" }));
    const confirm = await screen.findByRole("dialog", { name: "Delete tenant" });
    expect(within(confirm).getByRole("checkbox")).toBeChecked();
    await user.click(within(confirm).getByRole("button", { name: "Delete tenant" }));
    expect(deleteMutate).toHaveBeenCalledWith(
      { tenantId: "t-1", employeeAgentId: "employee-1", archiveEmployee: true },
      expect.anything(),
    );
  });
});
