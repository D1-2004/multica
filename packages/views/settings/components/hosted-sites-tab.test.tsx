import { fireEvent, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { renderWithI18n } from "../../test/i18n";

const mocks = vi.hoisted(() => ({
  deleteSite: vi.fn(),
  copyText: vi.fn(),
  success: vi.fn(),
  error: vi.fn(),
}));

vi.mock("@tanstack/react-query", () => ({
  useQuery: () => ({
    data: [
      {
        siteId: "site-1",
        publicSiteId: "public-1",
        title: "Weekly Review",
        status: "active",
        activeRevisionId: "revision-1",
        latestRevisionId: "revision-1",
        latestStatus: "active",
        latestError: "",
        createdAt: "2026-08-29T10:00:00Z",
        updatedAt: "2026-08-29T11:00:00Z",
        siteUrl: "https://sites.example.test/sites/public-1/",
      },
    ],
    isLoading: false,
    isError: false,
  }),
}));

vi.mock("@multica/core/sitehosting", () => ({
  hostedSiteListOptions: () => ({}),
  hostedSiteDisplayTitle: (site: { title: string; publicSiteId: string }) =>
    site.title.trim() || site.publicSiteId,
  useDeleteHostedSite: () => ({
    mutateAsync: mocks.deleteSite,
    isPending: false,
  }),
}));

vi.mock("@multica/core", () => ({
  useWorkspaceId: () => "workspace-1",
}));

vi.mock("@multica/ui/lib/clipboard", () => ({
  copyText: mocks.copyText,
}));

vi.mock("sonner", () => ({
  toast: { success: mocks.success, error: mocks.error },
}));

import { HostedSitesTab } from "./hosted-sites-tab";

beforeEach(() => {
  vi.clearAllMocks();
  mocks.copyText.mockResolvedValue(true);
  mocks.deleteSite.mockResolvedValue(undefined);
});

describe("HostedSitesTab", () => {
  it("shows the entrypoint HTML title as the website label", () => {
    renderWithI18n(<HostedSitesTab />);

    expect(screen.getByText("Weekly Review")).toBeInTheDocument();
    expect(screen.getByText("public-1")).toBeInTheDocument();
  });

  it("shares the public URL", async () => {
    renderWithI18n(<HostedSitesTab />);

    fireEvent.click(screen.getByRole("button", { name: "Share" }));

    await waitFor(() =>
      expect(mocks.copyText).toHaveBeenCalledWith(
        "https://sites.example.test/sites/public-1/",
      ),
    );
    expect(mocks.success).toHaveBeenCalled();
  });

  it("confirms deletion before removing a site", async () => {
    renderWithI18n(<HostedSitesTab />);

    fireEvent.click(screen.getByRole("button", { name: "Delete" }));
    expect(mocks.deleteSite).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole("button", { name: "Delete website" }));

    await waitFor(() =>
      expect(mocks.deleteSite).toHaveBeenCalledWith("site-1"),
    );
    expect(mocks.success).toHaveBeenCalled();
  });
});
