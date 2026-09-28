import { describe, expect, it } from "vitest";
import { parseWithFallback } from "./schema";
import { AvailableInternalConnectorListSchema, InternalConnectorListSchema, InternalConnectorTestSchema } from "./internal-connector-schema";

const opts = { endpoint: "GET /api/workspaces/:id/internal-connectors", includeReceived: false };

describe("internal connector API boundary", () => {
  it("does not accidentally present a malformed connector as enabled", () => {
    const raw = [{ id: "broken", name: "Private", enabled: "true", credential_ref: "secret" }];
    expect(parseWithFallback(raw, InternalConnectorListSchema, [], opts)).toEqual([]);
  });
  it("keeps upstream URLs and credential references out of member-visible data", () => {
    const raw = [{id:"11111111-1111-4111-8111-111111111111",name:"Knowledge",agent_id:"22222222-2222-4222-8222-222222222222",agent_name:"Reader",tools:["lookup"],upstream_url:"https://private.example/mcp",credential_ref:"SECRET"}];
    const result = parseWithFallback(raw, AvailableInternalConnectorListSchema, [], opts);
    expect(result).toEqual([{id:raw[0]!.id,name:"Knowledge",agentId:raw[0]!.agent_id,agentName:"Reader",tools:["lookup"]}]);
    expect(JSON.stringify(result)).not.toContain("private.example");
    expect(JSON.stringify(result)).not.toContain("SECRET");
  });
  it("treats a malformed connectivity response as unreachable", () => {
    const result = parseWithFallback({reachable:"true",tools:["read"]},InternalConnectorTestSchema,{reachable:false,message:"Invalid connection test response"},opts);
    expect(result.reachable).toBe(false);
  });
});
