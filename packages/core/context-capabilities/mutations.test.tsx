// @vitest-environment jsdom

import type { ReactNode } from "react";
import { act, renderHook } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, describe, expect, it, vi } from "vitest";
import { setApiInstance } from "../api";
import type { ApiClient } from "../api/client";
import type { AgentContextCapabilities, AgentSceneDetail } from "../types/context-capability";
import {
  useAddConnectedApp,
  useDeleteContextConnectorCredential,
  useRemoveAgentConnector,
  useSetAgentOffer,
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

  it("refreshes only the scene after a scene write, even a failed one", async () => {
    // The agent detail lists every scene for a manager; a scene toggle must
    // not refetch it.
    const setContextCapabilityBinding = vi.fn().mockRejectedValue(new Error("403"));
    const deleteContextConnectorCredential = vi.fn().mockResolvedValue(undefined);
    setApiInstance({ setContextCapabilityBinding, deleteContextConnectorCredential } as unknown as ApiClient);
    const queryClient = new QueryClient({ defaultOptions: { mutations: { retry: false } } });
    const invalidate = vi.spyOn(queryClient, "invalidateQueries");
    const { result } = renderHook(
      () => ({
        bind: useSetContextCapabilityBinding("agent-1"),
        disconnect: useDeleteContextConnectorCredential("agent-1"),
      }),
      { wrapper: createWrapper(queryClient) },
    );

    await act(async () => {
      await result.current.bind
        .mutateAsync({
          scopeType: "scene",
          scopeKey: "cid1",
          resourceType: "skill",
          resourceId: "s1",
          enabled: true,
        })
        .catch(() => undefined);
      await result.current.disconnect.mutateAsync({ scopeType: "scene", scopeKey: "cid1", connectorId: "c1" });
    });

    expect(invalidate).toHaveBeenCalledTimes(2);
    expect(invalidate).toHaveBeenNthCalledWith(1, { queryKey: contextConfigKeys.scene("agent-1", "cid1") });
    expect(invalidate).toHaveBeenNthCalledWith(2, { queryKey: contextConfigKeys.scene("agent-1", "cid1") });
  });

  it("refreshes the agent detail, which holds the personal scope, after a person write", async () => {
    const setContextCapabilityBinding = vi.fn().mockResolvedValue(undefined);
    setApiInstance({ setContextCapabilityBinding } as unknown as ApiClient);
    const queryClient = new QueryClient({ defaultOptions: { mutations: { retry: false } } });
    const invalidate = vi.spyOn(queryClient, "invalidateQueries");
    const { result } = renderHook(() => useSetContextCapabilityBinding("agent-1"), {
      wrapper: createWrapper(queryClient),
    });

    await act(async () => {
      await result.current.mutateAsync({
        scopeType: "person",
        scopeKey: "staff-1",
        resourceType: "connector",
        resourceId: "c1",
        enabled: true,
        shareInGroups: true,
      });
    });

    expect(invalidate).toHaveBeenCalledWith({ queryKey: contextConfigKeys.agent("agent-1") });
  });

  it("offers a skill from a fresh read, keeps the connector offers and caches the saved catalog", async () => {
    // The page loaded before another tab offered c2; the switch must keep it.
    const fresh = { ...tabBody, offers: { connectorIds: ["c1", "c2"], skillIds: [] } };
    const saved = { ...tabBody, offers: { connectorIds: ["c1", "c2"], skillIds: ["s1"] } };
    const getAgentContextCapabilities = vi.fn().mockResolvedValue(fresh);
    const setAgentContextCapabilityOffers = vi.fn().mockResolvedValue(saved);
    setApiInstance({ getAgentContextCapabilities, setAgentContextCapabilityOffers } as unknown as ApiClient);
    const queryClient = new QueryClient({ defaultOptions: { mutations: { retry: false } } });
    queryClient.setQueryData(contextCapabilityKeys.agent("ws-1", "agent-1"), tabBody);
    const { result } = renderHook(() => useSetAgentOffer("ws-1", "agent-1"), {
      wrapper: createWrapper(queryClient),
    });

    await act(async () => {
      await result.current.mutateAsync({ resourceType: "skill", resourceId: "s1", offered: true });
    });

    expect(setAgentContextCapabilityOffers).toHaveBeenCalledWith("ws-1", "agent-1", {
      connectorIds: ["c1", "c2"],
      skillIds: ["s1"],
    });
    expect(queryClient.getQueryData(contextCapabilityKeys.agent("ws-1", "agent-1"))).toEqual(saved);
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

describe("connector offer and removal", () => {
  afterEach(() => vi.restoreAllMocks());

  const withOffers = (connectorIds: string[], skillIds: string[] = ["s1"]): AgentContextCapabilities => ({
    ...tabBody,
    offers: { connectorIds, skillIds },
  });
  const newClient = () => new QueryClient({ defaultOptions: { mutations: { retry: false } } });

  it("offers a connector from a fresh read and keeps every other offer", async () => {
    const getAgentContextCapabilities = vi.fn().mockResolvedValue(withOffers(["c1"]));
    const saved = withOffers(["c1", "c2"]);
    const setAgentContextCapabilityOffers = vi.fn().mockResolvedValue(saved);
    setApiInstance({ getAgentContextCapabilities, setAgentContextCapabilityOffers } as unknown as ApiClient);
    const queryClient = newClient();
    const agentKey = contextCapabilityKeys.agent("ws-1", "agent-1");
    const appsKey = contextCapabilityKeys.connectedApps("ws-1", "agent-1");
    queryClient.setQueryData(agentKey, withOffers(["c1"]));
    queryClient.setQueryData(appsKey, { apps: [], canAdmin: true });
    const { result } = renderHook(() => useSetAgentOffer("ws-1", "agent-1"), {
      wrapper: createWrapper(queryClient),
    });

    await act(async () => {
      await result.current.mutateAsync({ resourceType: "connector", resourceId: "c2", offered: true });
    });

    expect(setAgentContextCapabilityOffers).toHaveBeenCalledWith("ws-1", "agent-1", {
      connectorIds: ["c1", "c2"],
      skillIds: ["s1"],
    });
    // The saved body goes straight into the cache and is not refetched; the
    // connected apps nested under the agent key are.
    expect(queryClient.getQueryData(agentKey)).toEqual(saved);
    expect(queryClient.getQueryState(agentKey)?.isInvalidated).toBe(false);
    expect(queryClient.getQueryState(appsKey)?.isInvalidated).toBe(true);
  });

  it("skips the write when the offer is already in the requested state", async () => {
    const getAgentContextCapabilities = vi.fn().mockResolvedValue(withOffers(["c1"]));
    const setAgentContextCapabilityOffers = vi.fn();
    setApiInstance({ getAgentContextCapabilities, setAgentContextCapabilityOffers } as unknown as ApiClient);
    const { result } = renderHook(() => useSetAgentOffer("ws-1", "agent-1"), {
      wrapper: createWrapper(newClient()),
    });

    await act(async () => {
      await result.current.mutateAsync({ resourceType: "connector", resourceId: "c2", offered: false });
    });

    expect(setAgentContextCapabilityOffers).not.toHaveBeenCalled();
  });

  it("fails instead of overwriting the catalog when the fresh read is malformed", async () => {
    const getAgentContextCapabilities = vi.fn().mockResolvedValue(null);
    const setAgentContextCapabilityOffers = vi.fn();
    setApiInstance({ getAgentContextCapabilities, setAgentContextCapabilityOffers } as unknown as ApiClient);
    const { result } = renderHook(() => useSetAgentOffer("ws-1", "agent-1"), {
      wrapper: createWrapper(newClient()),
    });

    await act(async () => {
      await expect(result.current.mutateAsync({ resourceType: "connector", resourceId: "c1", offered: false })).rejects.toThrow();
    });
    expect(setAgentContextCapabilityOffers).not.toHaveBeenCalled();
  });

  it("adds an official app: creates the workspace connector, then offers it without a grant", async () => {
    const addCatalogConnector = vi.fn().mockResolvedValue(null);
    const listInternalConnectors = vi
      .fn()
      .mockResolvedValue([{ id: "c-notion", catalogSlug: "notion", agentIds: [] }]);
    const updateInternalConnector = vi.fn();
    const getAgentContextCapabilities = vi.fn().mockResolvedValue(withOffers(["c1"]));
    const setAgentContextCapabilityOffers = vi.fn().mockResolvedValue(withOffers(["c1", "c-notion"]));
    setApiInstance({
      addCatalogConnector,
      listInternalConnectors,
      updateInternalConnector,
      getAgentContextCapabilities,
      setAgentContextCapabilityOffers,
    } as unknown as ApiClient);
    const queryClient = newClient();
    const invalidate = vi.spyOn(queryClient, "invalidateQueries");
    const { result } = renderHook(() => useAddConnectedApp("ws-1", "agent-1"), {
      wrapper: createWrapper(queryClient),
    });

    let id = "";
    await act(async () => {
      id = await result.current.mutateAsync("notion");
    });

    expect(id).toBe("c-notion");
    expect(addCatalogConnector).toHaveBeenCalledWith("ws-1", "notion");
    // A malformed echo is read back from the library.
    expect(listInternalConnectors).toHaveBeenCalledWith("ws-1");
    expect(setAgentContextCapabilityOffers).toHaveBeenCalledWith("ws-1", "agent-1", {
      connectorIds: ["c1", "c-notion"],
      skillIds: ["s1"],
    });
    expect(updateInternalConnector).not.toHaveBeenCalled();
    expect(invalidate).toHaveBeenCalledWith({ queryKey: contextCapabilityKeys.all("ws-1") });
  });

  it("removes a connector from the agent: grant and offer, not the library connector", async () => {
    const connector = {
      id: "c1",
      name: "GitHub",
      upstreamUrl: "https://api.githubcopilot.com/mcp/",
      allowedTools: ["get_me"],
      agentIds: ["agent-1", "agent-2"],
      enabled: true,
      authMode: "oauth",
      catalogSlug: "github",
      writeEnabled: false,
    };
    const listInternalConnectors = vi.fn().mockResolvedValue([connector]);
    const updateInternalConnector = vi.fn().mockResolvedValue(undefined);
    const getAgentContextCapabilities = vi.fn().mockResolvedValue(withOffers(["c1", "c9"]));
    const setAgentContextCapabilityOffers = vi.fn().mockResolvedValue(withOffers(["c9"]));
    setApiInstance({
      listInternalConnectors,
      updateInternalConnector,
      getAgentContextCapabilities,
      setAgentContextCapabilityOffers,
    } as unknown as ApiClient);
    const queryClient = newClient();
    const invalidate = vi.spyOn(queryClient, "invalidateQueries");
    const { result } = renderHook(() => useRemoveAgentConnector("ws-1", "agent-1"), {
      wrapper: createWrapper(queryClient),
    });

    await act(async () => {
      await result.current.mutateAsync("c1");
    });

    expect(updateInternalConnector).toHaveBeenCalledWith(
      "ws-1",
      "c1",
      expect.objectContaining({ agent_ids: ["agent-2"], enabled: true }),
    );
    expect(setAgentContextCapabilityOffers).toHaveBeenCalledWith("ws-1", "agent-1", {
      connectorIds: ["c9"],
      skillIds: ["s1"],
    });
    expect(invalidate).toHaveBeenCalledWith({ queryKey: contextCapabilityKeys.all("ws-1") });
  });

  it("removes an offer-only connector without writing the library connector", async () => {
    const listInternalConnectors = vi.fn().mockResolvedValue([
      {
        id: "c1",
        name: "Wiki",
        upstreamUrl: "https://faas.example/mcp",
        allowedTools: [],
        agentIds: [],
        enabled: true,
        authMode: "bearer",
        catalogSlug: "",
      },
    ]);
    const updateInternalConnector = vi.fn();
    const getAgentContextCapabilities = vi.fn().mockResolvedValue(withOffers(["c1"]));
    const setAgentContextCapabilityOffers = vi.fn().mockResolvedValue(withOffers([]));
    setApiInstance({
      listInternalConnectors,
      updateInternalConnector,
      getAgentContextCapabilities,
      setAgentContextCapabilityOffers,
    } as unknown as ApiClient);
    const { result } = renderHook(() => useRemoveAgentConnector("ws-1", "agent-1"), {
      wrapper: createWrapper(newClient()),
    });

    await act(async () => {
      await result.current.mutateAsync("c1");
    });

    expect(updateInternalConnector).not.toHaveBeenCalled();
    expect(setAgentContextCapabilityOffers).toHaveBeenCalledWith("ws-1", "agent-1", {
      connectorIds: [],
      skillIds: ["s1"],
    });
  });
});
