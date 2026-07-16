import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";

const { mockGetFDEOnboarding, mockProvisionFDEOnboarding, mockSetUser } = vi.hoisted(() => ({
  mockGetFDEOnboarding: vi.fn(),
  mockProvisionFDEOnboarding: vi.fn(),
  mockSetUser: vi.fn(),
}));

vi.mock("next/navigation", () => ({
  useSearchParams: () => new URLSearchParams(),
}));

vi.mock("@multica/core/auth", () => ({
  useAuthStore: (selector: (state: { setUser: typeof mockSetUser }) => unknown) =>
    selector({ setUser: mockSetUser }),
}));

vi.mock("@multica/core/api", () => ({
  api: {
    getFDEOnboarding: mockGetFDEOnboarding,
    provisionFDEOnboarding: mockProvisionFDEOnboarding,
  },
}));

import FDEStartPage from "./page";

const workspace = (id: string, name: string, slug: string) => ({
  id,
  name,
  slug,
  description: null,
  context: null,
  settings: {},
  repos: [],
  issue_prefix: "",
  avatar_url: null,
  created_at: "2026-07-16T00:00:00Z",
  updated_at: "2026-07-16T00:00:00Z",
});

describe("FDEStartPage workspace selection", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    sessionStorage.clear();
    sessionStorage.setItem("multica_fde_dingtalk_authenticated", "1");
    mockGetFDEOnboarding.mockResolvedValue({
      configured: true,
      workspaces: [
        workspace("workspace-1", "研发空间", "engineering"),
        workspace("workspace-2", "产品空间", "product"),
      ],
    });
  });

  it("shows a clear selected state after the user chooses a workspace", async () => {
    const user = userEvent.setup();
    render(<FDEStartPage />);

    const engineering = await screen.findByRole("radio", { name: /研发空间/ });
    const product = screen.getByRole("radio", { name: /产品空间/ });
    const continueButton = screen.getByRole("button", { name: "继续" });

    expect(engineering).toHaveAttribute("aria-checked", "false");
    expect(product).toHaveAttribute("aria-checked", "false");
    expect(continueButton).toBeDisabled();

    await user.click(product);

    await waitFor(() => expect(product).toHaveAttribute("aria-checked", "true"));
    expect(engineering).toHaveAttribute("aria-checked", "false");
    expect(within(product).getByText("已选择")).toBeVisible();
    expect(continueButton).toBeEnabled();
  });
});
