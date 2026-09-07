import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiClient } from "./client";
import { parseWithFallback } from "./schema";
import {
  AgentResponseListSchema,
  AgentResponseSchema,
  EMPTY_AGENT_RESPONSE,
} from "./schemas";

afterEach(() => vi.unstubAllGlobals());

describe("Agent response policy compatibility", () => {
  it("defaults policy fields from older backends while preserving unrelated data", () => {
    const parsed = parseWithFallback(
      { id: "agent-1", name: "Employee", future_setting: { enabled: true } },
      AgentResponseSchema,
      EMPTY_AGENT_RESPONSE,
      { endpoint: "GET /api/agents/:id" },
    );
    expect(parsed).toMatchObject({
      id: "agent-1",
      name: "Employee",
      dingtalk_show_ai_tag: false,
      dingtalk_response_policy_revision: 1,
      future_setting: { enabled: true },
    });
  });

  it.each(["true", 1, null, {}, []])("does not enable AI labels for malformed %j", (value) => {
    const parsed = AgentResponseSchema.parse({ id: "agent-1", dingtalk_show_ai_tag: value });
    expect(parsed.dingtalk_show_ai_tag).toBe(false);
  });

  it.each(["2", 0, -1, 1.5, null, Number.MAX_SAFE_INTEGER + 1])("defaults invalid revision %j", (value) => {
    const parsed = AgentResponseSchema.parse({ id: "agent-1", dingtalk_response_policy_revision: value });
    expect(parsed.dingtalk_response_policy_revision).toBe(1);
  });

  it("preserves explicit values independently for each listed agent", () => {
    const parsed = AgentResponseListSchema.parse([
      { id: "agent-1", dingtalk_show_ai_tag: true, dingtalk_response_policy_revision: 7 },
      { id: "agent-2", dingtalk_show_ai_tag: false, dingtalk_response_policy_revision: 8 },
    ]);
    expect(parsed[0]).toMatchObject({ dingtalk_show_ai_tag: true, dingtalk_response_policy_revision: 7 });
    expect(parsed[1]).toMatchObject({ dingtalk_show_ai_tag: false, dingtalk_response_policy_revision: 8 });
  });

  it("falls back safely for a malformed resource", () => {
    const parsed = parseWithFallback(null, AgentResponseSchema, EMPTY_AGENT_RESPONSE, {
      endpoint: "PUT /api/agents/:id",
    });
    expect(parsed).toBe(EMPTY_AGENT_RESPONSE);
  });

  it("normalizes malformed policy fields through the actual list API", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify([
      { id: "agent-1", name: "Employee", dingtalk_show_ai_tag: "true", dingtalk_response_policy_revision: -1 },
      { id: "agent-2", dingtalk_show_ai_tag: true, dingtalk_response_policy_revision: 9 },
    ]), { status: 200, headers: { "Content-Type": "application/json" } })));

    const agents = await new ApiClient("https://api.example.test").listAgents();
    expect(agents).toMatchObject([
      { id: "agent-1", name: "Employee", dingtalk_show_ai_tag: false, dingtalk_response_policy_revision: 1 },
      { id: "agent-2", dingtalk_show_ai_tag: true, dingtalk_response_policy_revision: 9 },
    ]);
  });

  it("normalizes older update responses without changing the submitted setting", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ id: "agent-1", name: "Employee" }), {
      status: 200, headers: { "Content-Type": "application/json" },
    }));
    vi.stubGlobal("fetch", fetchMock);

    const agent = await new ApiClient("https://api.example.test").updateAgent("agent-1", { dingtalk_show_ai_tag: true });
    expect(agent).toMatchObject({ id: "agent-1", dingtalk_show_ai_tag: false, dingtalk_response_policy_revision: 1 });
    expect(fetchMock).toHaveBeenCalledWith("https://api.example.test/api/agents/agent-1", expect.objectContaining({
      method: "PUT", body: JSON.stringify({ dingtalk_show_ai_tag: true }),
    }));
  });
});
