// @vitest-environment jsdom

import type { ReactNode } from "react";
import { act, renderHook } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, describe, expect, it, vi } from "vitest";
import { setApiInstance } from "../api";
import type { ApiClient } from "../api/client";
import type {
  AgentContextCapabilities,
  ContextConfigSceneDetail,
  ContextNodeDetail,
  ContextNodeRef,
} from "../types/context-capability";
import {
  useAddConnectedApp,
  useCreateAgentTenant,
  useDeleteAgentTenant,
  useDeleteContextConnectorCredential,
  useDeleteContextNodeCredential,
  useRemoveAgentConnector,
  useRevokeContextNodeGrants,
  useSetAgentOffer,
  useSetContextCapabilityBinding,
  useSetContextConfigMcpConfig,
  useSetContextConfigPrompts,
  useSetContextConnectorCredential,
  useSetContextNodeBinding,
  useSetContextNodeCredential,
  useSetContextNodeMcpConfig,
  useSetContextNodePrompts,
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
  orgs: [],
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

  it("refreshes the agent details, which hold the enterprise level, after an enterprise write", async () => {
    const setContextConnectorCredential = vi.fn().mockResolvedValue(undefined);
    setApiInstance({ setContextConnectorCredential } as unknown as ApiClient);
    const queryClient = new QueryClient({ defaultOptions: { mutations: { retry: false } } });
    const invalidate = vi.spyOn(queryClient, "invalidateQueries");
    const { result } = renderHook(() => useSetContextConnectorCredential("agent-1"), {
      wrapper: createWrapper(queryClient),
    });

    await act(async () => {
      await result.current.mutateAsync({
        scopeType: "org",
        scopeKey: "dingA",
        orgId: "dingA",
        connectorId: "c1",
        bearer: "secret",
      });
    });

    expect(setContextConnectorCredential).toHaveBeenCalledWith("agent-1", {
      scopeType: "org",
      scopeKey: "dingA",
      orgId: "dingA",
      connectorId: "c1",
      bearer: "secret",
    });
    expect(invalidate).toHaveBeenCalledWith({ queryKey: contextConfigKeys.details("agent-1") });
    // Scene details stay cached.
    expect(invalidate).not.toHaveBeenCalledWith({ queryKey: contextConfigKeys.agent("agent-1") });
  });

  it("refreshes only a 1:1 chat scene after a write there: it is its own scope", async () => {
    const dmSceneId = "8d0c2f3e-5b1a-4c7d-9e2f-1a2b3c4d5e6f";
    const setContextCapabilityBinding = vi.fn().mockResolvedValue(undefined);
    setApiInstance({ setContextCapabilityBinding } as unknown as ApiClient);
    const queryClient = new QueryClient({ defaultOptions: { mutations: { retry: false } } });
    queryClient.setQueryData(contextConfigKeys.scene("agent-1", dmSceneId), {
      scene: { scopeKey: dmSceneId, scopeTitle: "Ada", source: "manager", expiresAt: "", kind: "dm", orgId: "" },
      bindings: [],
      credentials: [],
      scope: { type: "scene", key: dmSceneId, title: "Ada" },
      canConnect: true,
      rights: null,
      prompts: [],
      mcpConfig: null,
      mcpConfigRedacted: false,
    } satisfies ContextConfigSceneDetail);
    const invalidate = vi.spyOn(queryClient, "invalidateQueries");
    const { result } = renderHook(() => useSetContextCapabilityBinding("agent-1"), {
      wrapper: createWrapper(queryClient),
    });

    await act(async () => {
      await result.current.mutateAsync({
        scopeType: "scene",
        scopeKey: dmSceneId,
        resourceType: "skill",
        resourceId: "s1",
        enabled: true,
      });
    });

    expect(invalidate).toHaveBeenCalledTimes(1);
    expect(invalidate).toHaveBeenCalledWith({ queryKey: contextConfigKeys.scene("agent-1", dmSceneId) });
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

  it("saves a scope's prompts and MCP servers and refreshes what the scope write changed", async () => {
    const setContextConfigPrompts = vi.fn().mockResolvedValue([]);
    const setContextConfigMcpConfig = vi.fn().mockRejectedValue(new Error("400"));
    setApiInstance({ setContextConfigPrompts, setContextConfigMcpConfig } as unknown as ApiClient);
    const queryClient = new QueryClient({ defaultOptions: { mutations: { retry: false } } });
    const invalidate = vi.spyOn(queryClient, "invalidateQueries");
    const { result } = renderHook(
      () => ({ prompts: useSetContextConfigPrompts("agent-1"), mcp: useSetContextConfigMcpConfig("agent-1") }),
      { wrapper: createWrapper(queryClient) },
    );
    const config = { mcpServers: { docs: { url: "https://mcp.example/docs" } } };

    await act(async () => {
      await result.current.prompts.mutateAsync({
        scopeType: "scene",
        scopeKey: "cid1",
        orgId: "dingA",
        prompts: [{ name: "Tone", order: 1, text: "Be brief.", enabled: false }],
      });
      // A rejected save still refreshes the scope.
      await result.current.mcp
        .mutateAsync({ scopeType: "org", scopeKey: "dingA", orgId: "dingA", mcpConfig: config })
        .catch(() => undefined);
    });

    expect(setContextConfigPrompts).toHaveBeenCalledWith(
      "agent-1",
      { scopeType: "scene", scopeKey: "cid1", orgId: "dingA" },
      [{ name: "Tone", order: 1, text: "Be brief.", enabled: false }],
    );
    expect(setContextConfigMcpConfig).toHaveBeenCalledWith(
      "agent-1",
      { scopeType: "org", scopeKey: "dingA", orgId: "dingA" },
      config,
    );
    expect(invalidate).toHaveBeenNthCalledWith(1, { queryKey: contextConfigKeys.scene("agent-1", "cid1") });
    expect(invalidate).toHaveBeenNthCalledWith(2, { queryKey: contextConfigKeys.details("agent-1") });
  });

  it("does not keep MCP server headers in the mutation cache", async () => {
    const setContextConfigMcpConfig = vi.fn().mockResolvedValue(null);
    setApiInstance({ setContextConfigMcpConfig } as unknown as ApiClient);
    const queryClient = new QueryClient({ defaultOptions: { mutations: { retry: false } } });
    const { result } = renderHook(() => useSetContextConfigMcpConfig("agent-1"), {
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
        mcpConfig: { mcpServers: { docs: { url: "https://mcp.example", headers: { Authorization: "top-secret" } } } },
      });
    });
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

describe("tenant mutations", () => {
  afterEach(() => vi.restoreAllMocks());

  it("creates a tenant and refreshes only the tenant list", async () => {
    const tenant = { orgId: "ding1", name: "Acme", source: "created", groupCount: 0, personCount: 0 };
    const createAgentTenant = vi.fn().mockResolvedValue(tenant);
    setApiInstance({ createAgentTenant } as unknown as ApiClient);
    const queryClient = new QueryClient({ defaultOptions: { mutations: { retry: false } } });
    const invalidate = vi.spyOn(queryClient, "invalidateQueries");
    const { result } = renderHook(() => useCreateAgentTenant("ws-1", "agent-1"), {
      wrapper: createWrapper(queryClient),
    });

    await act(async () => {
      expect(await result.current.mutateAsync({ orgId: "ding1", name: "Acme" })).toEqual(tenant);
    });

    expect(createAgentTenant).toHaveBeenCalledWith("ws-1", "agent-1", { orgId: "ding1", name: "Acme" });
    expect(invalidate).toHaveBeenCalledTimes(1);
    expect(invalidate).toHaveBeenCalledWith({
      queryKey: contextCapabilityKeys.tenants("ws-1", "agent-1"),
      exact: true,
    });
  });

  it("refreshes the tree, every node and the app usage after a tenant delete, even a failed one", async () => {
    const deleteAgentTenant = vi.fn().mockRejectedValue(new Error("409"));
    setApiInstance({ deleteAgentTenant } as unknown as ApiClient);
    const queryClient = new QueryClient({ defaultOptions: { mutations: { retry: false } } });
    const invalidate = vi.spyOn(queryClient, "invalidateQueries");
    const { result } = renderHook(() => useDeleteAgentTenant("ws-1", "agent-1"), {
      wrapper: createWrapper(queryClient),
    });

    await act(async () => {
      await result.current.mutateAsync("ding1").catch(() => undefined);
    });

    expect(invalidate).toHaveBeenCalledWith({ queryKey: contextCapabilityKeys.tenants("ws-1", "agent-1") });
    expect(invalidate).toHaveBeenCalledWith({ queryKey: contextCapabilityKeys.contextNodes("ws-1", "agent-1") });
    expect(invalidate).toHaveBeenCalledWith({ queryKey: contextCapabilityKeys.connectedApps("ws-1", "agent-1") });
  });
});

describe("Context Builder node writes", () => {
  afterEach(() => vi.restoreAllMocks());

  const orgNode: ContextNodeRef = { orgId: "ding1", scopeType: "org", scopeKey: "ding1" };
  const personNode: ContextNodeRef = { orgId: "ding1", scopeType: "person", scopeKey: "staff-1" };
  const detail = {
    scope: { type: "org", orgId: "ding1", key: "ding1", title: "Acme" },
    scene: null,
    prompts: [],
    connectors: [],
    skills: [],
    mcpConfig: null,
    mcpConfigRedacted: false,
    canConnect: true,
    rights: null,
    effective: { prompts: [], connectors: [], skills: [], mcpServers: [] },
  } satisfies ContextNodeDetail;

  it("caches saved prompt components and refreshes every node's effective preview", async () => {
    const stored = [{ id: "p1", name: "Tone", order: 1, text: "Be brief.", updatedByName: "Ada", updatedAt: "t" }];
    const setContextNodePrompts = vi.fn().mockResolvedValue(stored);
    setApiInstance({ setContextNodePrompts } as unknown as ApiClient);
    const queryClient = new QueryClient({ defaultOptions: { mutations: { retry: false } } });
    const nodeKey = contextCapabilityKeys.contextNode("ws-1", "agent-1", orgNode);
    queryClient.setQueryData(nodeKey, detail);
    const invalidate = vi.spyOn(queryClient, "invalidateQueries");
    const { result } = renderHook(() => useSetContextNodePrompts("ws-1", "agent-1"), {
      wrapper: createWrapper(queryClient),
    });

    await act(async () => {
      await result.current.mutateAsync({ node: orgNode, prompts: [{ name: "Tone", order: 1, text: "Be brief." }] });
    });

    expect(setContextNodePrompts).toHaveBeenCalledWith("ws-1", "agent-1", orgNode, [
      { name: "Tone", order: 1, text: "Be brief." },
    ]);
    expect(queryClient.getQueryData<ContextNodeDetail>(nodeKey)?.prompts).toEqual(stored);
    // A level feeds the levels below it: every node refetches.
    expect(invalidate).toHaveBeenCalledWith({ queryKey: contextCapabilityKeys.contextNodes("ws-1", "agent-1") });
    // Prompts do not move connector or offer usage.
    expect(invalidate).not.toHaveBeenCalledWith({ queryKey: contextCapabilityKeys.connectedApps("ws-1", "agent-1") });
    expect(invalidate).not.toHaveBeenCalledWith({
      queryKey: contextCapabilityKeys.agent("ws-1", "agent-1"),
      exact: true,
    });
  });

  it("keeps the cached prompts when the echo is malformed", async () => {
    const setContextNodePrompts = vi.fn().mockResolvedValue(null);
    setApiInstance({ setContextNodePrompts } as unknown as ApiClient);
    const queryClient = new QueryClient({ defaultOptions: { mutations: { retry: false } } });
    const nodeKey = contextCapabilityKeys.contextNode("ws-1", "agent-1", orgNode);
    queryClient.setQueryData(nodeKey, detail);
    const { result } = renderHook(() => useSetContextNodePrompts("ws-1", "agent-1"), {
      wrapper: createWrapper(queryClient),
    });

    await act(async () => {
      await result.current.mutateAsync({ node: orgNode, prompts: [] });
    });
    expect(queryClient.getQueryData<ContextNodeDetail>(nodeKey)?.prompts).toEqual([]);
  });

  it("saves custom MCP servers into the node", async () => {
    const stored = { mcpServers: { docs: { url: "https://mcp.example/docs" } } };
    const setContextNodeMcpConfig = vi.fn().mockResolvedValue(stored);
    setApiInstance({ setContextNodeMcpConfig } as unknown as ApiClient);
    const queryClient = new QueryClient({ defaultOptions: { mutations: { retry: false } } });
    const nodeKey = contextCapabilityKeys.contextNode("ws-1", "agent-1", orgNode);
    queryClient.setQueryData(nodeKey, detail);
    const { result } = renderHook(() => useSetContextNodeMcpConfig("ws-1", "agent-1"), {
      wrapper: createWrapper(queryClient),
    });

    await act(async () => {
      await result.current.mutateAsync({ node: orgNode, mcpConfig: stored });
    });
    expect(setContextNodeMcpConfig).toHaveBeenCalledWith("ws-1", "agent-1", orgNode, stored);
    expect(queryClient.getQueryData<ContextNodeDetail>(nodeKey)?.mcpConfig).toEqual(stored);
  });

  it("refreshes nodes, app and offer usage and the tenant's people after a person switch, even a failed one", async () => {
    const setContextNodeBinding = vi.fn().mockRejectedValue(new Error("403"));
    setApiInstance({ setContextNodeBinding } as unknown as ApiClient);
    const queryClient = new QueryClient({ defaultOptions: { mutations: { retry: false } } });
    const invalidate = vi.spyOn(queryClient, "invalidateQueries");
    const { result } = renderHook(() => useSetContextNodeBinding("ws-1", "agent-1"), {
      wrapper: createWrapper(queryClient),
    });

    await act(async () => {
      await result.current
        .mutateAsync({ node: personNode, resourceType: "skill", resourceId: "s1", enabled: true })
        .catch(() => undefined);
    });

    expect(setContextNodeBinding).toHaveBeenCalledWith("ws-1", "agent-1", personNode, {
      resourceType: "skill",
      resourceId: "s1",
      enabled: true,
    });
    expect(invalidate).toHaveBeenCalledWith({ queryKey: contextCapabilityKeys.contextNodes("ws-1", "agent-1") });
    expect(invalidate).toHaveBeenCalledWith({ queryKey: contextCapabilityKeys.connectedApps("ws-1", "agent-1") });
    // The offer switches count usage from the agent's capabilities, which
    // never go stale on their own.
    expect(invalidate).toHaveBeenCalledWith({
      queryKey: contextCapabilityKeys.agent("ws-1", "agent-1"),
      exact: true,
    });
    expect(invalidate).toHaveBeenCalledWith({
      queryKey: contextCapabilityKeys.tenantPersons("ws-1", "agent-1", "ding1"),
    });
  });

  it("stores and removes a level's token without keeping the secret in the mutation cache", async () => {
    const setContextNodeCredential = vi.fn().mockRejectedValue(new Error("403"));
    const deleteContextNodeCredential = vi.fn().mockResolvedValue(undefined);
    setApiInstance({ setContextNodeCredential, deleteContextNodeCredential } as unknown as ApiClient);
    const queryClient = new QueryClient({ defaultOptions: { mutations: { retry: false } } });
    const invalidate = vi.spyOn(queryClient, "invalidateQueries");
    const { result } = renderHook(
      () => ({
        save: useSetContextNodeCredential("ws-1", "agent-1"),
        remove: useDeleteContextNodeCredential("ws-1", "agent-1"),
      }),
      { wrapper: createWrapper(queryClient) },
    );

    await act(async () => {
      await result.current.save
        .mutateAsync({ node: orgNode, connectorId: "c1", bearer: "secret-token" })
        .catch(() => undefined);
      await result.current.remove.mutateAsync({ node: orgNode, connectorId: "c1" });
    });

    expect(setContextNodeCredential).toHaveBeenCalledWith("ws-1", "agent-1", orgNode, {
      connectorId: "c1",
      bearer: "secret-token",
    });
    expect(deleteContextNodeCredential).toHaveBeenCalledWith("ws-1", "agent-1", orgNode, "c1");
    // Admin keys (workspace-scoped), never the configure-page keys.
    for (const call of invalidate.mock.calls) {
      expect(call[0]?.queryKey?.[0]).toBe("workspaces");
    }
    expect(invalidate).toHaveBeenCalledWith({ queryKey: contextCapabilityKeys.connectedApps("ws-1", "agent-1") });

    vi.useFakeTimers();
    try {
      act(() => result.current.save.reset());
      await act(async () => {
        await vi.advanceTimersByTimeAsync(1);
      });
    } finally {
      vi.useRealTimers();
    }
    expect(
      JSON.stringify(queryClient.getMutationCache().getAll().map((mutation) => mutation.state.variables ?? null)),
    ).not.toContain("secret-token");
  });

  it("revokes a person's configure-page access and refreshes the nodes and the people list", async () => {
    const revokeContextNodeGrants = vi.fn().mockResolvedValue(2);
    setApiInstance({ revokeContextNodeGrants } as unknown as ApiClient);
    const queryClient = new QueryClient({ defaultOptions: { mutations: { retry: false } } });
    const invalidate = vi.spyOn(queryClient, "invalidateQueries");
    const { result } = renderHook(() => useRevokeContextNodeGrants("ws-1", "agent-1"), {
      wrapper: createWrapper(queryClient),
    });

    let revoked: number | null = null;
    await act(async () => {
      revoked = await result.current.mutateAsync(personNode);
    });

    expect(revoked).toBe(2);
    expect(revokeContextNodeGrants).toHaveBeenCalledWith("ws-1", "agent-1", personNode);
    expect(invalidate).toHaveBeenCalledWith({ queryKey: contextCapabilityKeys.contextNodes("ws-1", "agent-1") });
    expect(invalidate).toHaveBeenCalledWith({
      queryKey: contextCapabilityKeys.tenantPersons("ws-1", "agent-1", "ding1"),
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

  it("adds an official app: creates the workspace connector, then grants it as a common capability", async () => {
    const addCatalogConnector = vi.fn().mockResolvedValue(null);
    const listInternalConnectors = vi.fn().mockResolvedValue([
      {
        id: "c-notion",
        name: "Notion",
        upstreamUrl: "https://mcp.notion.com/mcp",
        allowedTools: [],
        agentIds: ["agent-2"],
        enabled: true,
        authMode: "oauth",
        catalogSlug: "notion",
        writeEnabled: false,
      },
    ]);
    const updateInternalConnector = vi.fn().mockResolvedValue(undefined);
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
    // Added means granted (通用能力), not published to scenes.
    expect(updateInternalConnector).toHaveBeenCalledWith(
      "ws-1",
      "c-notion",
      expect.objectContaining({ agent_ids: ["agent-2", "agent-1"] }),
    );
    expect(setAgentContextCapabilityOffers).not.toHaveBeenCalled();
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
