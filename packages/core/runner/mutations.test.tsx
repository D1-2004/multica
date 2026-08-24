// @vitest-environment jsdom

import type { ReactNode } from "react";
import { act, renderHook } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { AccountRunnerBindingTarget } from "./types";

const disconnectAccountRunnerBinding = vi.hoisted(() => vi.fn());
const createAccountRunnerReconnectCommand = vi.hoisted(() => vi.fn());
const revokeAccountRunnerBinding = vi.hoisted(() => vi.fn());
const disconnectAgentRunnerBinding = vi.hoisted(() => vi.fn());
const revokeAgentRunnerBinding = vi.hoisted(() => vi.fn());

vi.mock("../api", () => ({
  api: {
    disconnectAccountRunnerBinding,
    createAccountRunnerReconnectCommand,
    revokeAccountRunnerBinding,
    disconnectAgentRunnerBinding,
    revokeAgentRunnerBinding,
  },
}));

import {
  useCreateAccountRunnerReconnectCommand,
  useDisconnectAccountRunnerBinding,
  useRevokeAccountRunnerBinding,
  useDisconnectAgentRunnerBinding,
  useRevokeAgentRunnerBinding,
} from "./mutations";
import { runnerBindingKeys } from "./queries";

const target: AccountRunnerBindingTarget = {
  bindingId: "11111111-1111-4111-8111-111111111111",
  workspaceId: "22222222-2222-4222-8222-222222222222",
  agentId: "33333333-3333-4333-8333-333333333333",
};

function setup() {
  const queryClient = new QueryClient({
    defaultOptions: { mutations: { retry: false } },
  });
  const invalidate = vi
    .spyOn(queryClient, "invalidateQueries")
    .mockResolvedValue(undefined);
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
  );
  return { invalidate, wrapper };
}

beforeEach(() => {
  vi.clearAllMocks();
  disconnectAccountRunnerBinding.mockResolvedValue(undefined);
  createAccountRunnerReconnectCommand.mockResolvedValue({
    reconnectCommand: "command",
    expiresAt: "2026-08-24T08:10:00Z",
  });
  revokeAccountRunnerBinding.mockResolvedValue(undefined);
  disconnectAgentRunnerBinding.mockResolvedValue(undefined);
  revokeAgentRunnerBinding.mockResolvedValue(undefined);
});

describe("Agent Runner binding mutations", () => {
  it.each([
    {
      name: "disconnect",
      useHook: useDisconnectAgentRunnerBinding,
      apiCall: disconnectAgentRunnerBinding,
    },
    {
      name: "delete",
      useHook: useRevokeAgentRunnerBinding,
      apiCall: revokeAgentRunnerBinding,
    },
  ])(
    "also invalidates the account inventory after $name",
    async ({ useHook, apiCall }) => {
      const { invalidate, wrapper } = setup();
      const { result } = renderHook(
        () => useHook(target.workspaceId, target.agentId),
        { wrapper },
      );

      await act(async () => {
        await result.current.mutateAsync(target.bindingId);
      });

      expect(apiCall).toHaveBeenCalledWith(target.agentId, target.bindingId);
      expect(invalidate).toHaveBeenCalledWith({
        queryKey: runnerBindingKeys.agent(target.workspaceId, target.agentId),
      });
      expect(invalidate).toHaveBeenCalledWith({
        queryKey: runnerBindingKeys.accountAll(),
      });
    },
  );
});

describe("account Runner binding mutations", () => {
  it.each([
    {
      name: "disconnect",
      useHook: useDisconnectAccountRunnerBinding,
      apiCall: disconnectAccountRunnerBinding,
    },
    {
      name: "create reconnect command",
      useHook: useCreateAccountRunnerReconnectCommand,
      apiCall: createAccountRunnerReconnectCommand,
    },
    {
      name: "delete",
      useHook: useRevokeAccountRunnerBinding,
      apiCall: revokeAccountRunnerBinding,
    },
  ])(
    "invalidates both account and Agent caches after $name",
    async ({ useHook, apiCall }) => {
      const { invalidate, wrapper } = setup();
      const { result } = renderHook(() => useHook("user-1"), { wrapper });

      await act(async () => {
        await result.current.mutateAsync(target);
      });

      expect(apiCall).toHaveBeenCalledWith(target.bindingId);
      expect(invalidate).toHaveBeenCalledWith({
        queryKey: runnerBindingKeys.account("user-1"),
      });
      expect(invalidate).toHaveBeenCalledWith({
        queryKey: runnerBindingKeys.agent(target.workspaceId, target.agentId),
      });
    },
  );
});
