import { describe, expect, it } from "vitest";
import { focusManager, QueryObserver, type QueryClient } from "@tanstack/react-query";
import { createQueryClient } from "../query-client";
import {
  agentConnectedAppOptions,
  agentConnectedAppsOptions,
  agentSceneOptions,
  agentScenesOptions,
  contextCapabilityKeys,
  contextConfigKeys,
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

  it("nests the admin scene list and scene detail under the agent", () => {
    const agentKey = contextCapabilityKeys.agent("ws-1", "agent-1");
    const scenesKey = contextCapabilityKeys.scenes("ws-1", "agent-1");
    const sceneKey = contextCapabilityKeys.scene("ws-1", "agent-1", "cid1");
    expect(scenesKey.slice(0, agentKey.length)).toEqual([...agentKey]);
    expect(sceneKey.slice(0, scenesKey.length)).toEqual([...scenesKey]);
    expect(contextCapabilityKeys.scenes("ws-2", "agent-1")).not.toEqual(scenesKey);
  });
});

describe("admin scene list paging", () => {
  const page = (count: number, hasMore: boolean) => ({
    scenes: Array.from({ length: count }, () => ({}) as never),
    hasMore,
  });

  it("continues after the loaded rows and stops without more rows", () => {
    const { getNextPageParam } = agentScenesOptions("ws-1", "agent-1");
    expect(getNextPageParam(page(50, true), [], 0, [])).toBe(50);
    expect(getNextPageParam(page(50, false), [], 50, [])).toBeUndefined();
    // A server that claims more but sends nothing must not loop.
    expect(getNextPageParam(page(0, true), [], 50, [])).toBeUndefined();
  });
});

describe("connected apps and scene detail refetch on focus", () => {
  it("refetches when the window regains focus under the shared Infinity staleTime", async () => {
    // A connect finished in the system browser (desktop) or on a phone must
    // show when the admin comes back, although nothing invalidates the keys.
    for (const makeOptions of [
      () => agentConnectedAppsOptions("ws-1", "agent-1"),
      () => agentConnectedAppOptions("ws-1", "agent-1", "github"),
      () => agentSceneOptions("ws-1", "agent-1", "cid1"),
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
