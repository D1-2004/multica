import { describe, expect, it } from "vitest";
import { mcpServerProblem, readScopeMcpDocument, scopeMcpDocument } from "./scope-mcp";

describe("scope MCP document", () => {
  it("reads remote servers and writes them back unchanged", () => {
    const config = {
      mcpServers: {
        wiki: { type: "sse", url: "https://wiki.example/sse", headers: { Authorization: "Bearer t" }, disabled: true },
        docs: { url: "https://mcp.example/docs" },
      },
    };
    const read = readScopeMcpDocument(config);
    expect(read.editable).toBe(true);
    expect(read.servers.map((server) => [server.name, server.enabled])).toEqual([
      ["docs", true],
      ["wiki", false],
    ]);
    expect(scopeMcpDocument(read.servers)).toEqual(config);
  });

  it("is read-only when the document holds what the configure page cannot save", () => {
    const local = readScopeMcpDocument({ mcpServers: { local: { command: "npx" }, docs: { url: "https://a.example" } } });
    expect(local.editable).toBe(false);
    expect(local.servers.map((server) => [server.name, server.remote])).toEqual([
      ["docs", true],
      ["local", false],
    ]);
    expect(readScopeMcpDocument({ mcp: {} }).editable).toBe(false);
    expect(readScopeMcpDocument({ mcpServers: { x: { url: "https://a.example", timeout: 3 } } }).editable).toBe(false);
    expect(readScopeMcpDocument({ mcpServers: { x: { url: "ftp://a.example" } } }).editable).toBe(false);
    expect(readScopeMcpDocument({ mcpServers: { x: "https://a.example" } }).editable).toBe(false);
    expect(readScopeMcpDocument(null)).toEqual({ servers: [], editable: true });
  });

  it("accepts only remote http(s) servers with valid names and headers", () => {
    const ok = { name: "docs", url: "https://mcp.example/docs", headers: [{ key: "X-Token", value: "t" }] };
    const none = new Set<string>();
    expect(mcpServerProblem(ok, none)).toBeNull();
    expect(mcpServerProblem({ ...ok, url: "http://10.0.0.1:8080/mcp" }, none)).toBeNull();
    expect(mcpServerProblem({ ...ok, name: "" }, none)).toBe("name_required");
    expect(mcpServerProblem({ ...ok, name: "my server" }, none)).toBe("name_invalid");
    expect(mcpServerProblem({ ...ok, name: "Multica" }, none)).toBe("name_reserved");
    expect(mcpServerProblem({ ...ok, name: "c0123456789abcdef" }, none)).toBe("name_reserved");
    expect(mcpServerProblem(ok, new Set(["docs"]))).toBe("name_duplicate");
    expect(mcpServerProblem({ ...ok, url: "" }, none)).toBe("url_required");
    expect(mcpServerProblem({ ...ok, url: "npx server" }, none)).toBe("url_invalid");
    expect(mcpServerProblem({ ...ok, url: "file:///bin/server" }, none)).toBe("url_invalid");
    expect(mcpServerProblem({ ...ok, headers: [{ key: "Bad Header", value: "x" }] }, none)).toBe("header_invalid");
    expect(
      mcpServerProblem(
        { ...ok, headers: [{ key: "A", value: "1" }, { key: "a", value: "2" }] },
        none,
      ),
    ).toBe("header_invalid");
    expect(mcpServerProblem({ ...ok, headers: [{ key: "A", value: "x\ny" }] }, none)).toBe("header_invalid");
  });
});
