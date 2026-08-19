// @vitest-environment jsdom

import type { ReactNode } from "react";
import { act, renderHook } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, describe, expect, it, vi } from "vitest";
import { setApiInstance } from "../api";
import type { ApiClient } from "../api/client";
import type { AgentA2ACredential } from "../types";
import { useCreateAgentA2ACredential } from "./mutations";

const credential: AgentA2ACredential = {
  id: "credential-1",
  keyId: "key-1",
  tokenPrefix: "mca2a_key-1",
  status: "active",
  expiresAt: null,
  lastUsedAt: null,
  revokedAt: null,
  createdAt: "2026-08-09T00:00:00Z",
  updatedAt: "2026-08-09T00:00:00Z",
};

function createWrapper(queryClient: QueryClient) {
  return function Wrapper({ children }: { children: ReactNode }) {
    return (
      <QueryClientProvider client={queryClient}>
        {children}
      </QueryClientProvider>
    );
  };
}

describe("useCreateAgentA2ACredential", () => {
  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("delivers the raw token once without retaining it as mutation data", async () => {
    const rawToken = "mca2a_key-1_one-time-secret";
    const createAgentA2ACredential = vi.fn().mockResolvedValue({
      credential,
      token: rawToken,
    });
    setApiInstance({ createAgentA2ACredential } as unknown as ApiClient);
    const queryClient = new QueryClient({
      defaultOptions: { mutations: { retry: false } },
    });
    const onToken = vi.fn();
    const { result } = renderHook(
      () => useCreateAgentA2ACredential("ws-1", "agent-1"),
      { wrapper: createWrapper(queryClient) },
    );

    let mutationData: AgentA2ACredential | undefined;
    await act(async () => {
      mutationData = await result.current.mutateAsync({
        clientId: "client-1",
        data: { expiresAt: null },
        onToken,
      });
    });

    expect(createAgentA2ACredential).toHaveBeenCalledWith(
      "agent-1",
      "client-1",
      { expiresAt: null },
    );
    expect(onToken).toHaveBeenCalledOnce();
    expect(onToken).toHaveBeenCalledWith(rawToken);
    expect(mutationData).toEqual(credential);
    expect(mutationData).not.toHaveProperty("token");
    expect(JSON.stringify(queryClient.getMutationCache().getAll()[0]?.state.data))
      .not.toContain(rawToken);
  });

  it("rejects a drifted response instead of showing an empty secret", async () => {
    const createAgentA2ACredential = vi.fn().mockResolvedValue({
      credential,
      token: "",
    });
    setApiInstance({ createAgentA2ACredential } as unknown as ApiClient);
    const queryClient = new QueryClient({
      defaultOptions: { mutations: { retry: false } },
    });
    const onToken = vi.fn();
    const { result } = renderHook(
      () => useCreateAgentA2ACredential("ws-1", "agent-1"),
      { wrapper: createWrapper(queryClient) },
    );

    await act(async () => {
      await expect(result.current.mutateAsync({
        clientId: "client-1",
        data: {},
        onToken,
      })).rejects.toThrow("Invalid A2A credential response");
    });
    expect(onToken).not.toHaveBeenCalled();
  });
});
