// @vitest-environment jsdom

import type { ReactNode } from "react";
import { act, renderHook } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { runtimeKeys } from "./queries";

const mockUpdateFCE2BRuntimeTemplate = vi.hoisted(() => vi.fn());

vi.mock("../api", () => ({
  api: {
    updateFCE2BRuntimeTemplate: (...args: unknown[]) =>
      mockUpdateFCE2BRuntimeTemplate(...args),
  },
}));

import { useUpdateFCE2BRuntimeTemplate } from "./cloud-runtime";

function createWrapper(queryClient: QueryClient) {
  return function Wrapper({ children }: { children: ReactNode }) {
    return (
      <QueryClientProvider client={queryClient}>
        {children}
      </QueryClientProvider>
    );
  };
}

describe("useUpdateFCE2BRuntimeTemplate", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("sends only the template ID and refreshes runtime queries after success", async () => {
    const queryClient = new QueryClient({
      defaultOptions: { mutations: { retry: false } },
    });
    const invalidateSpy = vi.spyOn(queryClient, "invalidateQueries");
    mockUpdateFCE2BRuntimeTemplate.mockResolvedValue({ id: "rt-1" });
    const { result } = renderHook(
      () => useUpdateFCE2BRuntimeTemplate("ws-1"),
      { wrapper: createWrapper(queryClient) },
    );

    await act(async () => {
      await result.current.mutateAsync({
        runtimeId: "rt-1",
        data: { template_id: "tpl-v2" },
      });
    });

    expect(mockUpdateFCE2BRuntimeTemplate).toHaveBeenCalledWith("rt-1", {
      template_id: "tpl-v2",
    });
    expect(invalidateSpy).toHaveBeenCalledWith({
      queryKey: runtimeKeys.all("ws-1"),
    });
  });

  it("does not refresh runtime queries when the update fails", async () => {
    const queryClient = new QueryClient({
      defaultOptions: { mutations: { retry: false } },
    });
    const invalidateSpy = vi.spyOn(queryClient, "invalidateQueries");
    mockUpdateFCE2BRuntimeTemplate.mockRejectedValue(new Error("cutover failed"));
    const { result } = renderHook(
      () => useUpdateFCE2BRuntimeTemplate("ws-1"),
      { wrapper: createWrapper(queryClient) },
    );

    await act(async () => {
      await expect(
        result.current.mutateAsync({
          runtimeId: "rt-1",
          data: { template_id: "tpl-v2" },
        }),
      ).rejects.toThrow("cutover failed");
    });

    expect(invalidateSpy).not.toHaveBeenCalled();
  });
});
