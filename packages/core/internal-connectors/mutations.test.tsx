// @vitest-environment jsdom

import type { ReactNode } from "react";
import { act, renderHook } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, describe, expect, it, vi } from "vitest";
import { setApiInstance } from "../api";
import type { ApiClient } from "../api/client";
import type { InternalConnector } from "../api/internal-connector-schema";
import {
  useAddAgentCatalogConnector,
  useSetInternalConnectorAgentGrant,
} from "./mutations";
import { internalConnectorKeys } from "./queries";

function createWrapper(queryClient: QueryClient) {
  return function Wrapper({ children }: { children: ReactNode }) {
    return <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
  };
}

const github: InternalConnector = {
  id: "33333333-3333-4333-8333-333333333333",
  workspaceId: "ws-1",
  name: "GitHub",
  upstreamUrl: "https://api.githubcopilot.com/mcp/",
  credentialRef: "REF",
  credentialReady: false,
  authMode: "oauth",
  credentialSource: "none",
  credentialOptional: true,
  allowedTools: ["get_me"],
  agentIds: ["agent-other"],
  enabled: true,
  catalogSlug: "github",
  writeEnabled: false,
  discoveredToolCount: 1,
  credentialAccount: "",
};

describe("agent connector grants", () => {
  afterEach(() => vi.restoreAllMocks());

  it("adds an official app and grants it to the agent with the full connector body", async () => {
    const addCatalogConnector = vi.fn().mockResolvedValue(github);
    const updateInternalConnector = vi.fn().mockResolvedValue(undefined);
    setApiInstance({ addCatalogConnector, updateInternalConnector } as unknown as ApiClient);
    const queryClient = new QueryClient({ defaultOptions: { mutations: { retry: false } } });
    const invalidate = vi.spyOn(queryClient, "invalidateQueries");
    const { result } = renderHook(() => useAddAgentCatalogConnector("ws-1"), {
      wrapper: createWrapper(queryClient),
    });

    await act(async () => {
      await result.current.mutateAsync({ slug: "github", agentId: "agent-1" });
    });

    expect(addCatalogConnector).toHaveBeenCalledWith("ws-1", "github");
    expect(updateInternalConnector).toHaveBeenCalledWith("ws-1", github.id, {
      name: "GitHub",
      upstream_url: github.upstreamUrl,
      allowed_tools: ["get_me"],
      agent_ids: ["agent-other", "agent-1"],
      enabled: true,
      auth_mode: "oauth",
      write_enabled: false,
    });
    expect(invalidate).toHaveBeenCalledWith({ queryKey: internalConnectorKeys.all("ws-1") });
  });

  it("reads the connector back when the add echo is malformed and skips an existing grant", async () => {
    const addCatalogConnector = vi.fn().mockResolvedValue(null);
    const listInternalConnectors = vi
      .fn()
      .mockResolvedValue([{ ...github, agentIds: ["agent-1"] }]);
    const updateInternalConnector = vi.fn();
    setApiInstance({
      addCatalogConnector,
      listInternalConnectors,
      updateInternalConnector,
    } as unknown as ApiClient);
    const queryClient = new QueryClient({ defaultOptions: { mutations: { retry: false } } });
    const { result } = renderHook(() => useAddAgentCatalogConnector("ws-1"), {
      wrapper: createWrapper(queryClient),
    });

    await act(async () => {
      await result.current.mutateAsync({ slug: "github", agentId: "agent-1" });
    });

    expect(listInternalConnectors).toHaveBeenCalledWith("ws-1");
    expect(updateInternalConnector).not.toHaveBeenCalled();
  });

  it("revokes only this agent's grant", async () => {
    const updateInternalConnector = vi.fn().mockResolvedValue(undefined);
    setApiInstance({ updateInternalConnector } as unknown as ApiClient);
    const queryClient = new QueryClient({ defaultOptions: { mutations: { retry: false } } });
    const { result } = renderHook(() => useSetInternalConnectorAgentGrant("ws-1"), {
      wrapper: createWrapper(queryClient),
    });

    await act(async () => {
      await result.current.mutateAsync({
        connector: { ...github, agentIds: ["agent-other", "agent-1"] },
        agentId: "agent-1",
        granted: false,
      });
    });

    expect(updateInternalConnector).toHaveBeenCalledWith(
      "ws-1",
      github.id,
      expect.objectContaining({ agent_ids: ["agent-other"] }),
    );
  });
});
