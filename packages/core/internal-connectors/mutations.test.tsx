// @vitest-environment jsdom

import type { ReactNode } from "react";
import { act, renderHook } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, describe, expect, it, vi } from "vitest";
import { setApiInstance } from "../api";
import type { ApiClient } from "../api/client";
import type { InternalConnector } from "../api/internal-connector-schema";
import { contextCapabilityKeys } from "../context-capabilities/queries";
import {
  useDeleteInternalConnectorCredential,
  usePatchInternalConnector,
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

function newClient() {
  return new QueryClient({ defaultOptions: { mutations: { retry: false } } });
}

describe("library connector writes by id", () => {
  afterEach(() => vi.restoreAllMocks());

  it("grants a connector to an agent from a fresh read with the full connector body", async () => {
    const listInternalConnectors = vi.fn().mockResolvedValue([github]);
    const updateInternalConnector = vi.fn().mockResolvedValue(undefined);
    setApiInstance({ listInternalConnectors, updateInternalConnector } as unknown as ApiClient);
    const queryClient = newClient();
    const invalidate = vi.spyOn(queryClient, "invalidateQueries");
    const { result } = renderHook(() => usePatchInternalConnector("ws-1"), {
      wrapper: createWrapper(queryClient),
    });

    await act(async () => {
      await result.current.mutateAsync({
        connectorId: github.id,
        grant: { agentId: "agent-1", granted: true },
      });
    });

    expect(listInternalConnectors).toHaveBeenCalledWith("ws-1");
    expect(updateInternalConnector).toHaveBeenCalledWith("ws-1", github.id, {
      name: "GitHub",
      upstream_url: github.upstreamUrl,
      allowed_tools: ["get_me"],
      agent_ids: ["agent-other", "agent-1"],
      enabled: true,
      auth_mode: "oauth",
      write_enabled: false,
    });
    // Library changes also refresh the agent pages derived from it.
    expect(invalidate).toHaveBeenCalledWith({ queryKey: internalConnectorKeys.all("ws-1") });
    expect(invalidate).toHaveBeenCalledWith({ queryKey: contextCapabilityKeys.all("ws-1") });
  });

  it("revokes only this agent's grant and keeps the other fields", async () => {
    const listInternalConnectors = vi
      .fn()
      .mockResolvedValue([{ ...github, agentIds: ["agent-other", "agent-1"], writeEnabled: true }]);
    const updateInternalConnector = vi.fn().mockResolvedValue(undefined);
    setApiInstance({ listInternalConnectors, updateInternalConnector } as unknown as ApiClient);
    const { result } = renderHook(() => usePatchInternalConnector("ws-1"), {
      wrapper: createWrapper(newClient()),
    });

    await act(async () => {
      await result.current.mutateAsync({
        connectorId: github.id,
        grant: { agentId: "agent-1", granted: false },
      });
    });

    expect(updateInternalConnector).toHaveBeenCalledWith(
      "ws-1",
      github.id,
      expect.objectContaining({ agent_ids: ["agent-other"], write_enabled: true }),
    );
  });

  it("toggles write tools and the workspace switch", async () => {
    const listInternalConnectors = vi.fn().mockResolvedValue([github]);
    const updateInternalConnector = vi.fn().mockResolvedValue(undefined);
    setApiInstance({ listInternalConnectors, updateInternalConnector } as unknown as ApiClient);
    const { result } = renderHook(() => usePatchInternalConnector("ws-1"), {
      wrapper: createWrapper(newClient()),
    });

    await act(async () => {
      await result.current.mutateAsync({ connectorId: github.id, writeEnabled: true, enabled: false });
    });

    expect(updateInternalConnector).toHaveBeenCalledWith(
      "ws-1",
      github.id,
      expect.objectContaining({ write_enabled: true, enabled: false, agent_ids: ["agent-other"] }),
    );
  });

  it("refuses to write a connector that no longer exists", async () => {
    const listInternalConnectors = vi.fn().mockResolvedValue([]);
    const updateInternalConnector = vi.fn();
    setApiInstance({ listInternalConnectors, updateInternalConnector } as unknown as ApiClient);
    const { result } = renderHook(() => usePatchInternalConnector("ws-1"), {
      wrapper: createWrapper(newClient()),
    });

    await act(async () => {
      await expect(
        result.current.mutateAsync({ connectorId: github.id, enabled: true }),
      ).rejects.toThrow();
    });
    expect(updateInternalConnector).not.toHaveBeenCalled();
  });

  it("disconnects the shared account and refreshes both view families", async () => {
    const deleteInternalConnectorCredential = vi.fn().mockResolvedValue(undefined);
    setApiInstance({ deleteInternalConnectorCredential } as unknown as ApiClient);
    const queryClient = newClient();
    const invalidate = vi.spyOn(queryClient, "invalidateQueries");
    const { result } = renderHook(() => useDeleteInternalConnectorCredential("ws-1"), {
      wrapper: createWrapper(queryClient),
    });

    await act(async () => {
      await result.current.mutateAsync(github.id);
    });

    expect(deleteInternalConnectorCredential).toHaveBeenCalledWith("ws-1", github.id);
    expect(invalidate).toHaveBeenCalledWith({ queryKey: contextCapabilityKeys.all("ws-1") });
  });
});
