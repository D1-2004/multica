import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiClient } from "./client";
import { parseWithFallback } from "./schema";
import {
  AgentResponseListSchema,
  AgentResponseSchema,
  EMPTY_AGENT_RESPONSE,
} from "./schemas";

afterEach(() => vi.unstubAllGlobals());

describe("sandbox connection reuse", () => {
  it("stays on when an older backend omits or malforms the switch", () => {
    expect(AgentResponseSchema.parse({ id: "agent-1" }).sandbox_connection_reuse).toBe(true);
    expect(AgentResponseSchema.parse({ id: "agent-1", sandbox_connection_reuse: "no" }).sandbox_connection_reuse).toBe(true);
  });

  it("preserves an explicit off and sends it", async () => {
    expect(AgentResponseSchema.parse({ id: "agent-1", sandbox_connection_reuse: false }).sandbox_connection_reuse).toBe(false);
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ id: "agent-1", sandbox_connection_reuse: false }), { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);
    const result = await new ApiClient("https://api.example.test").updateAgent("agent-1", { sandbox_connection_reuse: false });
    expect(result.sandbox_connection_reuse).toBe(false);
    expect(fetchMock).toHaveBeenCalledWith("https://api.example.test/api/agents/agent-1", expect.objectContaining({ body: JSON.stringify({ sandbox_connection_reuse: false }) }));
  });
});

describe("Agent response policy compatibility", () => {
  it("defaults old agents to Coordinator without enabling coordination", () => {
    expect(AgentResponseSchema.parse({ id: "agent-1", inbound_coordinator: false })).toMatchObject({ coordination_mode: "coordinator", employee_loop_ready: false, inbound_coordinator: false });
  });
  it.each(["future", "", null, true, {}])("marks unknown coordination mode %j as unsupported", (mode) => {
    expect(AgentResponseSchema.parse({ id: "agent-1", coordination_mode: mode, employee_loop_ready: true })).toMatchObject({ coordination_mode: "unknown", employee_loop_ready: false });
  });
  it("sends and parses Employee mode independently from the enabled switch", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ id: "agent-1", coordination_mode: "employee", employee_loop_ready: true, inbound_coordinator: false }), { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);
    const result = await new ApiClient("https://api.example.test").updateAgent("agent-1", { coordination_mode: "employee" });
    expect(result).toMatchObject({ coordination_mode: "employee", employee_loop_ready: true, inbound_coordinator: false });
    expect(fetchMock).toHaveBeenCalledWith("https://api.example.test/api/agents/agent-1", expect.objectContaining({ body: JSON.stringify({ coordination_mode: "employee" }) }));
  });
  it.each(["off", "all", "named"] as const)("preserves explicit user decision mode %s", (mode) => {
    const parsed = AgentResponseSchema.parse({ id: "agent-1", inbound_coordinator_user_decision_mode: mode, inbound_coordinator_user_decision: mode === "off" });
    expect(parsed.inbound_coordinator_user_decision_mode).toBe(mode);
  });
  it.each([undefined, false, true])("maps the legacy setting %j without widening the audience", (enabled) => {
    const parsed = AgentResponseSchema.parse({ id: "agent-1", inbound_coordinator_user_decision: enabled });
    expect(parsed.inbound_coordinator_user_decision_mode).toBe(enabled === true ? "named" : "off");
  });
  it.each(["future", "", null, true, 1, {}, []])("fails closed for malformed user decision mode %j", (mode) => {
    const parsed = AgentResponseSchema.parse({ id: "agent-1", inbound_coordinator_user_decision_mode: mode, inbound_coordinator_user_decision: true });
    expect(parsed.inbound_coordinator_user_decision_mode).toBe("off");
  });
  it("sends and reads the selected mode through the update API", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ id: "agent-1", inbound_coordinator_user_decision_mode: "all" }), { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);
    const result = await new ApiClient("https://api.example.test").updateAgent("agent-1", { inbound_coordinator_user_decision_mode: "all" });
    expect(result.inbound_coordinator_user_decision_mode).toBe("all");
    expect(fetchMock).toHaveBeenCalledWith("https://api.example.test/api/agents/agent-1", expect.objectContaining({ body: JSON.stringify({ inbound_coordinator_user_decision_mode: "all" }) }));
  });
  it.each([undefined, "Alice", 1, null, {}, ["Alice", 1]])("fails closed for malformed user decision names %j", (value) => {
    expect(AgentResponseSchema.parse({ id: "agent-1", inbound_coordinator_user_decision_names: value }).inbound_coordinator_user_decision_names).toEqual([]);
  });
  it("preserves and sends an explicit name allowlist", async () => {
    const names = ["冬翔", "Alice"];
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ id: "agent-1", inbound_coordinator_user_decision_names: names }), { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);
    const result = await new ApiClient("https://api.example.test").updateAgent("agent-1", { inbound_coordinator_user_decision_names: names });
    expect(result.inbound_coordinator_user_decision_names).toEqual(names);
    expect(fetchMock).toHaveBeenCalledWith("https://api.example.test/api/agents/agent-1", expect.objectContaining({ body: JSON.stringify({ inbound_coordinator_user_decision_names: names }) }));
  });
  it.each([undefined, "true", 1, null, {}, []])("keeps user decision disabled for missing or malformed %j", (value) => {
    expect(AgentResponseSchema.parse({ id: "agent-1", inbound_coordinator_user_decision: value }).inbound_coordinator_user_decision).toBe(false);
  });
  it.each([true, false])("preserves explicit user decision setting %j", (value) => {
    expect(AgentResponseSchema.parse({ id: "agent-1", inbound_coordinator_user_decision: value }).inbound_coordinator_user_decision).toBe(value);
  });
  it.each(["true", 1, null, {}, []])("keeps event triggers disabled for malformed %j", (value) => {
    expect(AgentResponseSchema.parse({ id: "agent-1", event_trigger_enabled: value }).event_trigger_enabled).toBe(false);
  });

  it.each([true, false])("updates only the event trigger toggle to %j", async (enabled) => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ id: "agent-1", event_trigger_enabled: enabled }), { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);
    const result = await new ApiClient("https://api.example.test").updateAgent("agent-1", { event_trigger_enabled: enabled });
    expect(result.event_trigger_enabled).toBe(enabled);
    expect(fetchMock).toHaveBeenCalledWith("https://api.example.test/api/agents/agent-1", expect.objectContaining({ body: JSON.stringify({ event_trigger_enabled: enabled }) }));
  });

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
      event_trigger_enabled: false,
      dingtalk_response_enabled: false,
      dingtalk_show_ai_tag: false,
      dingtalk_response_policy_revision: 1,
      future_setting: { enabled: true },
    });
  });

  it.each(["true", 1, null, {}, []])("does not enable AI labels for malformed %j", (value) => {
    const parsed = AgentResponseSchema.parse({ id: "agent-1", dingtalk_show_ai_tag: value });
    expect(parsed.dingtalk_show_ai_tag).toBe(false);
  });

  it.each(["true", 1, null, {}, []])("does not enable unified responses for malformed %j", (value) => {
    const parsed = AgentResponseSchema.parse({ id: "agent-1", dingtalk_response_enabled: value });
    expect(parsed.dingtalk_response_enabled).toBe(false);
  });

  it.each([true, false])("sends the unified response setting as %j independently", async (enabled) => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      id: "agent-1", dingtalk_response_enabled: enabled, dingtalk_show_ai_tag: !enabled,
    }), { status: 200, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);

    const agent = await new ApiClient("https://api.example.test").updateAgent("agent-1", { dingtalk_response_enabled: enabled });
    expect(agent).toMatchObject({ dingtalk_response_enabled: enabled, dingtalk_show_ai_tag: !enabled });
    expect(fetchMock).toHaveBeenCalledWith("https://api.example.test/api/agents/agent-1", expect.objectContaining({
      method: "PUT", body: JSON.stringify({ dingtalk_response_enabled: enabled }),
    }));
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
      { id: "agent-1", name: "Employee", dingtalk_response_enabled: "true", dingtalk_show_ai_tag: "true", dingtalk_response_policy_revision: -1 },
      { id: "agent-2", dingtalk_show_ai_tag: true, dingtalk_response_policy_revision: 9 },
    ]), { status: 200, headers: { "Content-Type": "application/json" } })));

    const agents = await new ApiClient("https://api.example.test").listAgents();
    expect(agents).toMatchObject([
      { id: "agent-1", name: "Employee", dingtalk_response_enabled: false, dingtalk_show_ai_tag: false, dingtalk_response_policy_revision: 1 },
      { id: "agent-2", dingtalk_show_ai_tag: true, dingtalk_response_policy_revision: 9 },
    ]);
  });

  it("normalizes older update responses without changing the submitted setting", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ id: "agent-1", name: "Employee" }), {
      status: 200, headers: { "Content-Type": "application/json" },
    }));
    vi.stubGlobal("fetch", fetchMock);

    const agent = await new ApiClient("https://api.example.test").updateAgent("agent-1", { dingtalk_show_ai_tag: true });
    expect(agent).toMatchObject({ id: "agent-1", dingtalk_response_enabled: false, dingtalk_show_ai_tag: false, dingtalk_response_policy_revision: 1 });
    expect(fetchMock).toHaveBeenCalledWith("https://api.example.test/api/agents/agent-1", expect.objectContaining({
      method: "PUT", body: JSON.stringify({ dingtalk_show_ai_tag: true }),
    }));
  });
});
