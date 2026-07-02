import { beforeEach, describe, expect, it, vi } from "vitest";

const { redirect } = vi.hoisted(() => ({
  redirect: vi.fn(),
}));

vi.mock("next/navigation", () => ({
  redirect,
}));

import RootPage from "./page";

describe("RootPage", () => {
  beforeEach(() => {
    redirect.mockClear();
  });

  it("sends visitors directly to the login page", () => {
    RootPage();

    expect(redirect).toHaveBeenCalledWith("/login");
  });
});
