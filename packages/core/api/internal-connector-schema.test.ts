import { describe, expect, it } from "vitest";
import { parseWithFallback } from "./schema";
import {
  AddedCatalogConnectorSchema,
  AvailableInternalConnectorListSchema,
  ConnectorAuthorizeUrlSchema,
  InternalConnectorListSchema,
  InternalConnectorTestSchema,
  InternalConnectorToolsRefreshSchema,
  internalConnectorUpdateInput,
  safeExternalUrl,
  type InternalConnector,
  type InternalConnectorToolsRefresh,
} from "./internal-connector-schema";

const opts = { endpoint: "GET /api/workspaces/:id/internal-connectors", includeReceived: false };

const base = {
  id: "11111111-1111-4111-8111-111111111111",
  workspace_id: "22222222-2222-4222-8222-222222222222",
  name: "Knowledge",
  upstream_url: "https://approved.example/mcp",
  credential_ref: "REF",
  credential_ready: false,
  allowed_tools: ["read"],
  agent_ids: [],
  enabled: false,
};

function parseList(raw: unknown): InternalConnector[] {
  return parseWithFallback<InternalConnector[]>(raw, InternalConnectorListSchema, [], opts);
}

describe("internal connector API boundary", () => {
  it("does not accidentally present a malformed connector as enabled", () => {
    const raw = [{ id: "broken", name: "Private", enabled: "true", credential_ref: "secret" }];
    expect(parseList(raw)).toEqual([]);
  });
  it("treats a missing or malformed credential_optional as a required workspace credential", () => {
    expect(parseList([base])[0]?.credentialOptional).toBe(false);
    expect(parseList([{...base,credential_optional:true}])[0]?.credentialOptional).toBe(true);
    expect(parseList([{...base,credential_optional:"true"}])).toEqual([]);
  });
  it("keeps upstream URLs and credential references out of member-visible data", () => {
    const raw = [{id:"11111111-1111-4111-8111-111111111111",name:"Knowledge",agent_id:"22222222-2222-4222-8222-222222222222",agent_name:"Reader",tools:["lookup"],upstream_url:"https://private.example/mcp",credential_ref:"SECRET"}];
    const result = parseWithFallback(raw, AvailableInternalConnectorListSchema, [], opts);
    expect(result).toEqual([{id:raw[0]!.id,name:"Knowledge",serverName:`internal-${raw[0]!.id}`,agentId:raw[0]!.agent_id,agentName:"Reader",tools:["lookup"],catalogSlug:""}]);
    expect(JSON.stringify(result)).not.toContain("private.example");
    expect(JSON.stringify(result)).not.toContain("SECRET");
  });
  it("treats a malformed connectivity response as unreachable", () => {
    const result = parseWithFallback({reachable:"true",tools:["read"]},InternalConnectorTestSchema,{reachable:false,ready:false,missing_tools:[],message:"Invalid connection test response"},opts);
    expect(result.reachable).toBe(false);
  });
  it("does not mark an empty upstream tool list ready", () => {
    const result = InternalConnectorTestSchema.parse({reachable:true,tools:[],missing_tools:["read"]});
    expect(result.ready).toBe(false);
    expect(result.missing_tools).toEqual(["read"]);
  });
  it("uses the server-provided compact name for chat guidance", () => {
    const result = AvailableInternalConnectorListSchema.parse([{id:"a3fc1b87-7e59-452f-951d-7a317e110709",name:"Semantica",server_name:"ca3fc1b877e59452f",agent_id:"22222222-2222-4222-8222-222222222222",agent_name:"Reader",tools:["get_knowledge_graph_schema"]}]);
    expect(result[0]?.serverName).toBe("ca3fc1b877e59452f");
  });
  it("tells official apps from Aone FaaS connectors in the member-visible list", () => {
    const item = {id:"11111111-1111-4111-8111-111111111111",name:"GitHub",agent_id:"22222222-2222-4222-8222-222222222222",agent_name:"Reader",tools:[]};
    const result = AvailableInternalConnectorListSchema.parse([{...item,catalog_slug:"github"},{...item,catalog_slug:null}]);
    expect(result.map((connector) => connector.catalogSlug)).toEqual(["github", ""]);
  });
});

describe("internal connector catalog fields", () => {
  it("defaults the catalog fields of an older backend to a custom, read-only connector", () => {
    const [connector] = parseList([base]);
    expect(connector).toMatchObject({
      authMode: "bearer",
      credentialSource: "none",
      catalogSlug: "",
      writeEnabled: false,
      discoveredToolCount: 0,
      credentialAccount: "",
    });
  });

  it("keeps a connector listed when a newer backend sends an unknown auth mode or credential source", () => {
    const result = parseList([
      { ...base, auth_mode: "oauth", credential_source: "workspace" },
      { ...base, id: "33333333-3333-4333-8333-333333333333", auth_mode: "mtls", credential_source: "vault" },
    ]);
    expect(result.map((c) => c.authMode)).toEqual(["oauth", "unknown"]);
    expect(result.map((c) => c.credentialSource)).toEqual(["workspace", "unknown"]);
  });

  it("maps catalog fields and only enables writes on a literal true", () => {
    const [connector] = parseList([
      {
        ...base,
        auth_mode: "oauth",
        catalog_slug: "github",
        write_enabled: "true",
        discovered_tool_count: 48,
        credential_account: "@octocat",
      },
    ]);
    expect(connector).toMatchObject({
      catalogSlug: "github",
      writeEnabled: false,
      discoveredToolCount: 48,
      credentialAccount: "@octocat",
    });
  });

  it("tolerates null lists and a malformed tool count from Go handlers", () => {
    const [connector] = parseList([{ ...base, allowed_tools: null, agent_ids: null, discovered_tool_count: "many" }]);
    expect(connector?.allowedTools).toEqual([]);
    expect(connector?.agentIds).toEqual([]);
    expect(connector?.discoveredToolCount).toBe(0);
  });
});

describe("internalConnectorUpdateInput", () => {
  const custom = parseList([base])[0]!;
  const github = parseList([
    { ...base, auth_mode: "oauth", catalog_slug: "github", write_enabled: true, allowed_tools: ["get_me"] },
  ])[0]!;

  it("never sends write_enabled for a custom connector", () => {
    expect(internalConnectorUpdateInput(custom, { enabled: true })).toEqual({
      name: "Knowledge",
      upstream_url: "https://approved.example/mcp",
      allowed_tools: ["read"],
      agent_ids: [],
      enabled: true,
      auth_mode: "bearer",
    });
  });

  it("always carries write_enabled for a catalog connector so a save never drops write access", () => {
    expect(internalConnectorUpdateInput(github, { agent_ids: ["a"] })).toMatchObject({
      auth_mode: "oauth",
      agent_ids: ["a"],
      write_enabled: true,
    });
    expect(internalConnectorUpdateInput(github, { write_enabled: false }).write_enabled).toBe(false);
  });

  it("leaves an unknown auth mode for the server to keep", () => {
    const unknown = parseList([{ ...base, auth_mode: "mtls" }])[0]!;
    expect(internalConnectorUpdateInput(unknown).auth_mode).toBe("");
  });
});

describe("catalog connector add echo", () => {
  const catalogOpts = { endpoint: "GET /api/workspaces/:id/connector-catalog" };

  it("returns null for a malformed add echo so the caller refetches", () => {
    expect(parseWithFallback<InternalConnector | null>({ connector: { id: "x" } }, AddedCatalogConnectorSchema, null, catalogOpts)).toBeNull();
    const added = parseWithFallback<InternalConnector | null>(
      { connector: { ...base, auth_mode: "oauth", catalog_slug: "github", enabled: true, allowed_tools: [] } },
      AddedCatalogConnectorSchema,
      null,
      catalogOpts,
    );
    expect(added).toMatchObject({ catalogSlug: "github", authMode: "oauth", allowedTools: [] });
  });

  it("only hands an absolute https authorization URL to navigation", () => {
    const parse = (raw: unknown) => parseWithFallback<string>(raw, ConnectorAuthorizeUrlSchema, "", catalogOpts);
    expect(parse({ authorize_url: "https://github.com/login/oauth/authorize?client_id=x&state=mcpc.abc" })).toBe(
      "https://github.com/login/oauth/authorize?client_id=x&state=mcpc.abc",
    );
    expect(parse({ authorize_url: "javascript:alert(1)" })).toBe("");
    expect(parse({ authorize_url: "/relative/path" })).toBe("");
    expect(parse({ authorize_url: "http://evil.example/authorize" })).toBe("");
    expect(parse({ url: "https://github.com" })).toBe("");
    expect(parse(null)).toBe("");
  });

  it("allows http only on loopback for local development", () => {
    expect(safeExternalUrl("http://localhost:8080/authorize")).toBe("http://localhost:8080/authorize");
    expect(safeExternalUrl("http://127.0.0.1/authorize")).toBe("http://127.0.0.1/authorize");
    expect(safeExternalUrl("data:text/html,hi")).toBe("");
    expect(safeExternalUrl(42)).toBe("");
  });

  it("maps a tools refresh and treats a malformed one as unknown", () => {
    const parse = (raw: unknown) =>
      parseWithFallback<InternalConnectorToolsRefresh | null>(raw, InternalConnectorToolsRefreshSchema, null, catalogOpts);
    expect(parse({ discovered: 48, allowed_tools: ["get_me", "search_code"] })).toEqual({
      discovered: 48,
      allowedTools: ["get_me", "search_code"],
    });
    expect(parse({ discovered: 0, allowed_tools: null })).toEqual({ discovered: 0, allowedTools: [] });
    expect(parse({ discovered: "48", allowed_tools: [] })).toBeNull();
  });
});
