import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiClient } from "./client";
import { setSchemaLogger } from "./schema";

const value = { agent_id: "agent/id", plugin_id: "plugin/id", revision: 7, inherited: false, row_id: "row", config: { token: "fixture-secret" } };
afterEach(() => vi.unstubAllGlobals());

describe("employee plugin configuration", () => {
  it("preserves the exact revision, employee and null reset", async () => {
    const request = vi.fn().mockImplementation(() => Promise.resolve(new Response(JSON.stringify(value))));
    vi.stubGlobal("fetch", request);
    const client = new ApiClient("https://pre.example.test");
    expect(await client.getAgentDshPluginConfig("agent/id", "plugin/id")).toMatchObject({ agentId: "agent/id", revision: 7 });
    await client.updateAgentDshPluginConfig("agent/id", "plugin/id", { expectedRevision: 7, override: null });
    expect(request).toHaveBeenLastCalledWith("https://pre.example.test/api/agents/agent%2Fid/dsh-plugins/plugin%2Fid/config", expect.objectContaining({ method: "PUT", body: '{"expected_revision":7,"config_override":null}' }));
  });
  it.each([{ ...value, revision: "bad" }, { ...value, inherited: null }, { ...value, config: [] }, { ...value, agent_id: "different" }])("refuses malformed or cross-employee responses", async (body) => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify(body))));
    const warn = vi.fn();
    setSchemaLogger({ debug: vi.fn(), info: vi.fn(), warn, error: vi.fn() });
    expect(await new ApiClient("https://pre.example.test").getAgentDshPluginConfig("agent/id", "plugin/id")).toBeNull();
    expect(JSON.stringify(warn.mock.calls)).not.toContain("fixture-secret");
  });
  it("does not retry a conflicting write", async () => {
    const request = vi.fn().mockResolvedValue(new Response('{"error":"reload"}', { status: 409 }));
    vi.stubGlobal("fetch", request);
    await expect(new ApiClient("https://pre.example.test").updateAgentDshPluginConfig("a", "p", { expectedRevision: 7, override: { rowId: "row", config: {} } })).rejects.toMatchObject({ status: 409 });
    expect(request).toHaveBeenCalledTimes(1);
  });
});

it("submits the final plugin set and private settings atomically in one request", async () => {
  const request = vi.fn().mockResolvedValue(new Response(null,{status:204}));
  vi.stubGlobal("fetch",request);
  await new ApiClient("https://pre.example.test").setAgentDshPlugins("agent",[
    {id:"existing",enabled:false,configChange:{expectedRevision:7,override:null}},
    {id:"new",enabled:true,configChange:{expectedRevision:0,override:{rowId:"loader",config:{option:true}}}},
  ]);
  expect(request).toHaveBeenCalledTimes(1);
  expect(JSON.parse(request.mock.calls[0]![1].body)).toEqual({plugins:[
    {id:"existing",enabled:false,config_change:{expected_revision:7,config_override:null}},
    {id:"new",enabled:true,config_change:{expected_revision:0,config_override:{row_id:"loader",config:{option:true}}}},
  ]});
});
