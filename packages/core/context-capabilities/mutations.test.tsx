// @vitest-environment jsdom

import type { ReactNode } from "react";
import { act, renderHook } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, describe, expect, it, vi } from "vitest";
import { setApiInstance } from "../api";
import type { ApiClient } from "../api/client";
import type { AgentContextCapabilities, AgentSceneDetail } from "../types/context-capability";
import {
  useSetAgentContextCapabilityOffers,
  useSetAgentSceneBinding,
  useSetAgentScenePrompt,
  useSetContextCapabilityBinding,
  useSetContextConnectorCredential,
} from "./mutations";
import { contextCapabilityKeys, contextConfigKeys } from "./queries";

function createWrapper(queryClient: QueryClient) {
  return function Wrapper({ children }: { children: ReactNode }) {
    return <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
  };
}

const tabBody: AgentContextCapabilities = {
  enabled: true,
  library: { connectors: [], skills: [] },
  offers: { connectorIds: ["c1"], skillIds: [] },
  scenes: [],
  persons: [],
  configureUrl: "",
};

describe("context capability mutations", () => {
  afterEach(() => vi.restoreAllMocks());

  it("invalidates the agent and its scenes after a failed binding write", async () => {
    const setContextCapabilityBinding = vi.fn().mockRejectedValue(new Error("403"));
    setApiInstance({ setContextCapabilityBinding } as unknown as ApiClient);
    const queryClient = new QueryClient({ defaultOptions: { mutations: { retry: false } } });
    const invalidate = vi.spyOn(queryClient, "invalidateQueries");
    const { result } = renderHook(() => useSetContextCapabilityBinding("agent-1"), {
      wrapper: createWrapper(queryClient),
    });

    await act(async () => {
      await result.current
        .mutateAsync({
          scopeType: "scene",
          scopeKey: "cid1",
          resourceType: "skill",
          resourceId: "s1",
          enabled: true,
        })
        .catch(() => undefined);
    });

    expect(invalidate).toHaveBeenCalledWith({ queryKey: contextConfigKeys.agent("agent-1") });
  });

  it("writes the returned catalog into the workspace-scoped cache", async () => {
    const setAgentContextCapabilityOffers = vi.fn().mockResolvedValue(tabBody);
    setApiInstance({ setAgentContextCapabilityOffers } as unknown as ApiClient);
    const queryClient = new QueryClient({ defaultOptions: { mutations: { retry: false } } });
    const { result } = renderHook(() => useSetAgentContextCapabilityOffers("ws-1", "agent-1"), {
      wrapper: createWrapper(queryClient),
    });

    await act(async () => {
      await result.current.mutateAsync({ connectorIds: ["c1"], skillIds: [] });
    });

    expect(setAgentContextCapabilityOffers).toHaveBeenCalledWith("ws-1", "agent-1", {
      connectorIds: ["c1"],
      skillIds: [],
    });
    expect(queryClient.getQueryData(contextCapabilityKeys.agent("ws-1", "agent-1"))).toEqual(tabBody);
  });

  it("drops a submitted Bearer from the mutation cache once the mutation is reset", async () => {
    const setContextConnectorCredential = vi
      .fn()
      .mockResolvedValue({ connectorId: "c1", hint: "••••cret", updatedAt: "" });
    setApiInstance({ setContextConnectorCredential } as unknown as ApiClient);
    const queryClient = new QueryClient({ defaultOptions: { mutations: { retry: false } } });
    const { result } = renderHook(() => useSetContextConnectorCredential("agent-1"), {
      wrapper: createWrapper(queryClient),
    });
    const leaked = () =>
      queryClient
        .getMutationCache()
        .getAll()
        .some((mutation) => JSON.stringify(mutation.state.variables ?? null).includes("top-secret"));

    await act(async () => {
      await result.current.mutateAsync({
        scopeType: "person",
        scopeKey: "staff-1",
        connectorId: "c1",
        bearer: "top-secret",
      });
    });
    expect(leaked()).toBe(true);

    vi.useFakeTimers();
    try {
      act(() => result.current.reset());
      await act(async () => {
        await vi.advanceTimersByTimeAsync(1);
      });
    } finally {
      vi.useRealTimers();
    }
    expect(leaked()).toBe(false);
  });
});

describe("admin scene mutations", () => {
  afterEach(() => vi.restoreAllMocks());

  it("writes a saved scene prompt into the scene detail and refreshes the scene list", async () => {
    const prompt = { text: "Be brief.", updatedAt: "t", updatedByName: "Ada" };
    const setAgentScenePrompt = vi.fn().mockResolvedValue(prompt);
    setApiInstance({ setAgentScenePrompt } as unknown as ApiClient);
    const queryClient = new QueryClient({ defaultOptions: { mutations: { retry: false } } });
    const sceneKey = contextCapabilityKeys.scene("ws-1", "agent-1", "cid1");
    const detail = {
      scene: { sceneKey: "cid1" },
      prompt: { text: "", updatedAt: "", updatedByName: "" },
      bindings: [],
      offers: { connectors: [], skills: [] },
    } as unknown as AgentSceneDetail;
    queryClient.setQueryData(sceneKey, detail);
    const invalidate = vi.spyOn(queryClient, "invalidateQueries");
    const { result } = renderHook(() => useSetAgentScenePrompt("ws-1", "agent-1"), {
      wrapper: createWrapper(queryClient),
    });

    await act(async () => {
      await result.current.mutateAsync({ sceneKey: "cid1", prompt: "Be brief." });
    });

    expect(setAgentScenePrompt).toHaveBeenCalledWith("ws-1", "agent-1", "cid1", "Be brief.");
    expect(queryClient.getQueryData<AgentSceneDetail>(sceneKey)?.prompt).toEqual(prompt);
    expect(invalidate).toHaveBeenCalledWith({
      queryKey: contextCapabilityKeys.scenes("ws-1", "agent-1"),
    });
  });

  it("refreshes the whole agent after an admin scene toggle, even when it fails", async () => {
    const setAgentSceneBinding = vi.fn().mockRejectedValue(new Error("403"));
    setApiInstance({ setAgentSceneBinding } as unknown as ApiClient);
    const queryClient = new QueryClient({ defaultOptions: { mutations: { retry: false } } });
    const invalidate = vi.spyOn(queryClient, "invalidateQueries");
    const { result } = renderHook(() => useSetAgentSceneBinding("ws-1", "agent-1"), {
      wrapper: createWrapper(queryClient),
    });

    await act(async () => {
      await result.current
        .mutateAsync({ sceneKey: "cid1", resourceType: "skill", resourceId: "s1", enabled: true })
        .catch(() => undefined);
    });

    expect(setAgentSceneBinding).toHaveBeenCalledWith("ws-1", "agent-1", "cid1", {
      resourceType: "skill",
      resourceId: "s1",
      enabled: true,
    });
    expect(invalidate).toHaveBeenCalledWith({
      queryKey: contextCapabilityKeys.agent("ws-1", "agent-1"),
    });
  });
});
