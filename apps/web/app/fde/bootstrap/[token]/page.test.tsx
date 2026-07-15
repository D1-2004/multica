import { render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  complete: vi.fn(),
}));

vi.mock("next/navigation", () => ({
  useParams: () => ({ token: "fdeb_test-token" }),
  useSearchParams: () => new URLSearchParams("authenticated=1"),
}));

vi.mock("@multica/core/api", () => ({
  api: {
    completeFDEBootstrapIntent: mocks.complete,
  },
}));

import FDEBootstrapPage from "./page";

describe("FDEBootstrapPage", () => {
  beforeEach(() => {
    mocks.complete.mockReset();
  });

  it("shows only the minimal DingTalk completion message after provisioning", async () => {
    mocks.complete.mockResolvedValue({ status: "ready" });
    render(<FDEBootstrapPage />);

    expect(await screen.findByText("创建完成，可以返回钉钉")).toBeInTheDocument();
    expect(screen.getByText("创建完成")).toBeInTheDocument();
    expect(screen.queryByText(/workspace id/i)).not.toBeInTheDocument();
    expect(screen.queryByRole("link")).not.toBeInTheDocument();
    await waitFor(() => {
      expect(mocks.complete).toHaveBeenCalledTimes(1);
      expect(mocks.complete).toHaveBeenCalledWith("fdeb_test-token");
    });
  });
});
