import { afterEach, expect, it, vi } from "vitest";
import { ApiClient } from "./client";
import { AgentSceneMemorySchema } from "./schemas";

afterEach(() => vi.unstubAllGlobals());
it("sends explicit loop and frozen scene/revision for Employee management", async () => {
  const fetchMock = vi.fn().mockImplementation(async () => new Response(JSON.stringify({ id: "scene", loop: "employee", memory_revision: 7 }), { status: 200 }));
  vi.stubGlobal("fetch", fetchMock);
  const client = new ApiClient("https://example.test");
  await client.getAgentSceneMemory("agent", "scene", "employee");
  expect(fetchMock.mock.calls[0]?.[0]).toBe("https://example.test/api/agents/agent/scene-memory/scene?loop=employee");
  const selection = { loop: "employee" as const, scene_id: "scene", org_id: "org", expected_revision: 7 };
  await client.resetAgentSceneMemory("agent", "scene", selection);
  expect(fetchMock.mock.calls[1]?.[0]).toBe("https://example.test/api/agents/agent/scene-memory/scene/reset?loop=employee");
  expect(JSON.parse(fetchMock.mock.calls[1]?.[1].body)).toEqual(selection);
});
it("does not treat an unknown response loop as either writable namespace", () => {
  expect(AgentSceneMemorySchema.parse({ id: "scene", loop: "future" }).loop).toBe("unknown");
});
