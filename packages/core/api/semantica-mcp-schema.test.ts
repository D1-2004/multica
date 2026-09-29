import { describe, expect, it } from "vitest";
import { parseWithFallback } from "./schema";
import { EMPTY_SEMANTICA_MCP_STATUS, SemanticaMCPStatusSchema } from "./semantica-mcp-schema";

const opts = { endpoint: "GET /api/workspaces/:id/semantica-mcp-relay", includeReceived: false };

describe("Semantica MCP status wire boundary", () => {
  it("keeps the page closed when a partial or malformed backend response arrives", () => {
    expect(parseWithFallback({ available: true, agent_id: "invalid" }, SemanticaMCPStatusSchema, EMPTY_SEMANTICA_MCP_STATUS, opts)).toEqual(EMPTY_SEMANTICA_MCP_STATUS);
    expect(parseWithFallback({ available: "true", agent_id: null, agent_name: null, tools: [] }, SemanticaMCPStatusSchema, EMPTY_SEMANTICA_MCP_STATUS, opts)).toEqual(EMPTY_SEMANTICA_MCP_STATUS);
  });

  it("translates the scoped agent and tools for the UI", () => {
    expect(parseWithFallback({ available: true, agent_id: "e2293e9e-1e79-4926-b0e6-da4cb693add0", agent_name: "Pre-release agent", tools: ["get_knowledge_graph_schema"] }, SemanticaMCPStatusSchema, EMPTY_SEMANTICA_MCP_STATUS, opts)).toEqual({ available: true, agentId: "e2293e9e-1e79-4926-b0e6-da4cb693add0", agentName: "Pre-release agent", tools: ["get_knowledge_graph_schema"] });
  });
});
