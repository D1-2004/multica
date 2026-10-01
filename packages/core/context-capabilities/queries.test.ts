import { describe, expect, it } from "vitest";
import { focusManager, QueryObserver, type QueryClient } from "@tanstack/react-query";
import { createQueryClient } from "../query-client";
import {
  agentConnectedAppOptions,
  agentConnectedAppsOptions,
  agentTenantGroupsOptions,
  contextCapabilityKeys,
  contextConfigKeys,
  contextNodeOptions,
} from "./queries";

describe("context capability query keys", () => {
  it("scopes admin keys by workspace and agent", () => {
    expect(contextCapabilityKeys.agent("ws-1", "agent-1")).toEqual([
      "workspaces",
      "ws-1",
      "context-capabilities",
      "agent-1",
    ]);
    expect(contextCapabilityKeys.agent("ws-2", "agent-1")).not.toEqual(
      contextCapabilityKeys.agent("ws-1", "agent-1"),
    );
  });

  it("nests scene detail under the agent so one invalidation covers both", () => {
    const agentKey = contextConfigKeys.agent("agent-1");
    const sceneKey = contextConfigKeys.scene("agent-1", "cid1");
    expect(sceneKey.slice(0, agentKey.length)).toEqual([...agentKey]);
    expect(contextConfigKeys.agent("agent-2")).not.toEqual(agentKey);
  });

  it("nests tenants, their groups and people, and every Context Builder node under the agent", () => {
    const agentKey = contextCapabilityKeys.agent("ws-1", "agent-1");
    const tenantsKey = contextCapabilityKeys.tenants("ws-1", "agent-1");
    const groupsKey = contextCapabilityKeys.tenantGroups("ws-1", "agent-1", "ding1");
    const personsKey = contextCapabilityKeys.tenantPersons("ws-1", "agent-1", "ding1");
    const nodesKey = contextCapabilityKeys.contextNodes("ws-1", "agent-1");
    const nodeKey = contextCapabilityKeys.contextNode("ws-1", "agent-1", {
      orgId: "ding1",
      scopeType: "scene",
      scopeKey: "cid1",
    });
    expect(tenantsKey.slice(0, agentKey.length)).toEqual([...agentKey]);
    expect(groupsKey.slice(0, tenantsKey.length)).toEqual([...tenantsKey]);
    expect(personsKey.slice(0, tenantsKey.length)).toEqual([...tenantsKey]);
    expect(nodeKey.slice(0, nodesKey.length)).toEqual([...nodesKey]);
    expect(nodesKey.slice(0, agentKey.length)).toEqual([...agentKey]);
    expect(contextCapabilityKeys.tenants("ws-2", "agent-1")).not.toEqual(tenantsKey);
    // The same key under two tenants, or at two levels, is two nodes.
    expect(
      contextCapabilityKeys.contextNode("ws-1", "agent-1", { orgId: "ding2", scopeType: "scene", scopeKey: "cid1" }),
    ).not.toEqual(nodeKey);
    expect(
      contextCapabilityKeys.contextNode("ws-1", "agent-1", { orgId: "ding1", scopeType: "person", scopeKey: "cid1" }),
    ).not.toEqual(nodeKey);
  });
});

describe("tenant group paging", () => {
  const page = (count: number, hasMore: boolean) => ({
    scenes: Array.from({ length: count }, () => ({}) as never),
    hasMore,
  });

  it("continues after the loaded rows and stops without more rows", () => {
    const { getNextPageParam } = agentTenantGroupsOptions("ws-1", "agent-1", "ding1");
    expect(getNextPageParam(page(50, true), [], 0, [])).toBe(50);
    expect(getNextPageParam(page(50, false), [], 50, [])).toBeUndefined();
    // A server that claims more but sends nothing must not loop.
    expect(getNextPageParam(page(0, true), [], 50, [])).toBeUndefined();
  });
});

describe("connected apps and Context Builder nodes refetch on focus", () => {
  it("refetches when the window regains focus under the shared Infinity staleTime", async () => {
    // A connect finished in the system browser (desktop) or on a phone must
    // show when the admin comes back, although nothing invalidates the keys.
    for (const makeOptions of [
      () => agentConnectedAppsOptions("ws-1", "agent-1"),
      () => agentConnectedAppOptions("ws-1", "agent-1", "github"),
      () => contextNodeOptions("ws-1", "agent-1", { orgId: "ding1", scopeType: "org", scopeKey: "ding1" }),
    ]) {
      const client: QueryClient = createQueryClient();
      client.mount();
      let calls = 0;
      const options = { ...makeOptions(), queryFn: async () => ({ call: ++calls }) } as never;
      const observer = new QueryObserver(client, options);
      const unsubscribe = observer.subscribe(() => undefined);
      await client.fetchQuery(options);
      const before = calls;

      focusManager.setFocused(false);
      focusManager.setFocused(true);
      await new Promise((resolve) => setTimeout(resolve, 0));

      expect(calls).toBeGreaterThan(before);
      unsubscribe();
      focusManager.setFocused(undefined);
      client.unmount();
      client.clear();
    }
  });
});
