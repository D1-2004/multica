import { describe, expect, it } from "vitest";
import type { AgentRuntime } from "../types";
import { isFCE2BRuntime } from "./cloud-runtime";

function makeRuntime(overrides: Partial<AgentRuntime> = {}): AgentRuntime {
  return {
    id: "rt-1",
    workspace_id: "ws-1",
    daemon_id: "fc-e2b:ws-1:fc-hermes",
    name: "FC-Hermes",
    runtime_mode: "cloud",
    provider: "hermes",
    launch_header: "",
    status: "online",
    device_info: "FC/E2B one-shot sandbox",
    metadata: { kind: "fc-e2b" },
    owner_id: "user-1",
    visibility: "private",
    last_seen_at: null,
    created_at: "2026-07-08T00:00:00Z",
    updated_at: "2026-07-08T00:00:00Z",
    ...overrides,
  };
}

describe("isFCE2BRuntime", () => {
  it("matches only FC/E2B cloud runtimes", () => {
    expect(isFCE2BRuntime(makeRuntime())).toBe(true);
    expect(isFCE2BRuntime(makeRuntime({ runtime_mode: "local" }))).toBe(false);
    expect(isFCE2BRuntime(makeRuntime({ metadata: { kind: "other" } }))).toBe(false);
  });
});
