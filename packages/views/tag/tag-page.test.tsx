import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { TagState } from "@multica/core/tag";
import { renderWithI18n } from "../test/i18n";
import { TagPage } from "./tag-page";

const { detailProps, navigation, tagQuery, renameMutate, deleteMutate } = vi.hoisted(() => ({
  renameMutate: vi.fn(),
  deleteMutate: vi.fn(),
  detailProps: { current: null as null | { agentId: string; tagView?: { role: string; backHref?: string; backLabel?: string } } },
  navigation: { current: { pathname: "/acme/tag", searchParams: new URLSearchParams() } },
  tagQuery: { current: { data: undefined as TagState | undefined, isLoading: false } },
}));

vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws-1" }));
vi.mock("@multica/core/paths", () => ({
  useWorkspacePaths: () => ({ tag: () => "/acme/tag", settings: () => "/acme/settings" }),
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
    replace: vi.fn(),
    push: vi.fn(),
  }),
}));
vi.mock("../agents/components/agent-detail-page", () => ({
  AgentDetailPage: (props: { agentId: string; tagView?: { role: string; tabBarExtra?: React.ReactNode } }) => {
    detailProps.current = props;
    return (
      <div data-testid="agent-detail" data-agent={props.agentId} data-role={props.tagView?.role}>
        {props.tagView?.tabBarExtra}
      </div>
    );
  },
}));

const tenant = {
  id: "t-1",
  name: "Think测试组织",
  employeeAgentId: "employee-1",
  employeeName: "QwenTag · Think测试组织",
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
      name: "QwenTag",
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

describe("TagPage", () => {
  beforeEach(() => {
    renameMutate.mockReset();
    deleteMutate.mockReset();
    detailProps.current = null;
    navigation.current = { pathname: "/acme/tag", searchParams: new URLSearchParams() };
  });

  it("tells members without operator rights that only operators create a Tag", () => {
    tagQuery.current = { data: { tag: null, canOperate: false, canManage: false, tenants: [] }, isLoading: false };
    renderWithI18n(<TagPage />);
    expect(screen.getByText("This workspace has no Tag yet")).toBeTruthy();
    expect(screen.getByText("Only platform operators can create a Tag.")).toBeTruthy();
    expect(screen.queryByText("Open settings")).toBeNull();
  });

  it("points operators at Settings → Tag", () => {
    tagQuery.current = { data: { tag: null, canOperate: true, canManage: false, tenants: [] }, isLoading: false };
    renderWithI18n(<TagPage />);
    expect(screen.getByText("Open settings").closest("a")?.getAttribute("href")).toBe("/acme/settings?tab=tag");
  });

  it("shows the template for the shared configuration", () => {
    tagQuery.current = { data: stateWith(), isLoading: false };
    renderWithI18n(<TagPage />);
    const detail = screen.getByTestId("agent-detail");
    expect(detail.getAttribute("data-agent")).toBe("template-1");
    expect(detail.getAttribute("data-role")).toBe("template");
    // The tenant switcher sits in the tab bar and starts on the shared view.
    expect(screen.getByText("Shared")).toBeTruthy();
    // The breadcrumb leads back to the Tag page, not the agent list.
    expect(detailProps.current?.tagView?.backHref).toBe("/acme/tag");
    expect(detailProps.current?.tagView?.backLabel).toBe("Tag");
  });

  it("shows the selected tenant's employee", () => {
    tagQuery.current = { data: stateWith(), isLoading: false };
    navigation.current = { pathname: "/acme/tag", searchParams: new URLSearchParams("tag_tenant=t-1") };
    renderWithI18n(<TagPage />);
    const detail = screen.getByTestId("agent-detail");
    expect(detail.getAttribute("data-agent")).toBe("employee-1");
    expect(detail.getAttribute("data-role")).toBe("employee");
    expect(screen.getByText("177928186")).toBeTruthy();
  });

  it("asks to apply when the template has unpublished changes", () => {
    const base = stateWith();
    tagQuery.current = {
      data: { ...base, tag: base.tag ? { ...base.tag, hasUnpublishedChanges: true } : null },
      isLoading: false,
    };
    renderWithI18n(<TagPage />);
    expect(
      screen.getByText("The shared configuration has changes that are not applied to any tenant yet."),
    ).toBeTruthy();
  });

  it("manages tenants: renames one and deletes it with its employee archived", async () => {
    const user = userEvent.setup();
    tagQuery.current = { data: stateWith(), isLoading: false };
    renderWithI18n(<TagPage />);

    await user.click(screen.getByRole("button", { name: "Switch tenant" }));
    await user.click(await screen.findByRole("menuitem", { name: "Manage tenants…" }));
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
