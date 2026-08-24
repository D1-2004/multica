import { describe, expect, it, vi } from "vitest";

const listAccountRunnerBindings = vi.hoisted(() => vi.fn());

vi.mock("../api", () => ({
  api: { listAccountRunnerBindings },
}));

import { accountRunnerBindingsOptions, runnerBindingKeys } from "./queries";

describe("accountRunnerBindingsOptions", () => {
  it("keys the account inventory by signed-in user and polls machine state", async () => {
    listAccountRunnerBindings.mockResolvedValue({ machines: [] });

    const options = accountRunnerBindingsOptions("user-1");

    expect(options.queryKey).toEqual(
      runnerBindingKeys.account("user-1"),
    );
    expect(options.enabled).toBe(true);
    expect(options.refetchInterval).toBe(5_000);
    expect(options.refetchOnWindowFocus).toBe("always");
    await expect(options.queryFn?.({} as never)).resolves.toEqual({
      machines: [],
    });
    expect(listAccountRunnerBindings).toHaveBeenCalledTimes(1);
  });

  it("does not fetch before an authenticated user is known", () => {
    expect(accountRunnerBindingsOptions("").enabled).toBe(false);
  });
});
