// @vitest-environment jsdom

import type { ReactNode } from "react";
import { act, renderHook } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { setApiInstance } from "../api";
import type { ApiClient } from "../api/client";
import {
  useBindDingTalkMessageRouteManually,
  useSetDingTalkNativeSubscription,
} from "./mutations";
import { dingtalkAccountBindingKeys } from "./queries";

const setDingTalkNativeSubscription = vi.fn();
const bindDingTalkMessageRouteManually = vi.fn();

function createWrapper(queryClient: QueryClient) {
  return function Wrapper({ children }: { children: ReactNode }) {
    return (
      <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
    );
  };
}

function createQueryClient() {
  const queryClient = new QueryClient({
    defaultOptions: { mutations: { retry: false } },
  });
  const invalidate = vi.spyOn(queryClient, "invalidateQueries");
  return { queryClient, invalidate };
}

beforeEach(() => {
  vi.clearAllMocks();
  setApiInstance({
    setDingTalkNativeSubscription,
    bindDingTalkMessageRouteManually,
  } as unknown as ApiClient);
});

describe("DingTalk native subscription mutations", () => {
  it("switches native subscription and refreshes the workspace bindings", async () => {
    setDingTalkNativeSubscription.mockResolvedValue({ nativeSubscription: true });
    const { queryClient, invalidate } = createQueryClient();
    const { result } = renderHook(() => useSetDingTalkNativeSubscription("ws-1"), {
      wrapper: createWrapper(queryClient),
    });

    await act(async () => {
      await result.current.mutateAsync({ agentId: "agent-1", enabled: true });
    });

    expect(setDingTalkNativeSubscription).toHaveBeenCalledWith("ws-1", "agent-1", true);
    expect(invalidate).toHaveBeenCalledWith({
      queryKey: dingtalkAccountBindingKeys.all("ws-1"),
    });
  });

  it("refreshes the bindings after a conflict so the stale projection heals", async () => {
    setDingTalkNativeSubscription.mockRejectedValue(new Error("conflict"));
    const { queryClient, invalidate } = createQueryClient();
    const { result } = renderHook(() => useSetDingTalkNativeSubscription("ws-1"), {
      wrapper: createWrapper(queryClient),
    });

    await act(async () => {
      await expect(
        result.current.mutateAsync({ agentId: "agent-1", enabled: true }),
      ).rejects.toThrow("conflict");
    });

    expect(invalidate).toHaveBeenCalledWith({
      queryKey: dingtalkAccountBindingKeys.all("ws-1"),
    });
  });

  it("binds the message route by id and refreshes the workspace bindings", async () => {
    bindDingTalkMessageRouteManually.mockResolvedValue(undefined);
    const { queryClient, invalidate } = createQueryClient();
    const { result } = renderHook(() => useBindDingTalkMessageRouteManually("ws-1"), {
      wrapper: createWrapper(queryClient),
    });

    await act(async () => {
      await result.current.mutateAsync({
        agentId: "agent-1",
        orgId: "123456",
        uid: "7890",
        messageScope: "all",
      });
    });

    expect(bindDingTalkMessageRouteManually).toHaveBeenCalledWith("ws-1", "agent-1", {
      orgId: "123456",
      uid: "7890",
      messageScope: "all",
    });
    expect(invalidate).toHaveBeenCalledWith({
      queryKey: dingtalkAccountBindingKeys.all("ws-1"),
    });
  });
});
