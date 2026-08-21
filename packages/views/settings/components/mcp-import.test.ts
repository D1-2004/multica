import { describe, expect, it } from "vitest";
import {
  buildMulticaMCPEndpoint,
  buildMulticaMCPImport,
} from "./mcp-import";

const endpoint = "https://multica.example/api/mcp";
const token = "mul_test_secret";

describe("Multica MCP imports", () => {
  it("uses the API base when present and otherwise falls back to the app origin", () => {
    expect(
      buildMulticaMCPEndpoint("https://api.example/base/", "https://app.example"),
    ).toBe("https://api.example/base/api/mcp");
    expect(buildMulticaMCPEndpoint("", "https://app.example/")).toBe(
      "https://app.example/api/mcp",
    );
  });

  it("builds Codex TOML with the dedicated token in its HTTP headers", () => {
    expect(buildMulticaMCPImport("codex", endpoint, token)).toBe(
      `[mcp_servers.multica]\nurl = "https://multica.example/api/mcp"\nhttp_headers = { Authorization = "Bearer mul_test_secret" }\nenabled = true`,
    );
  });

  it("builds a user-scoped Claude command", () => {
    expect(buildMulticaMCPImport("claude", endpoint, token)).toBe(
      `claude mcp add --transport http --scope user \\\n  --header 'Authorization: Bearer mul_test_secret' \\\n  multica 'https://multica.example/api/mcp'`,
    );
  });

  it("keeps Qoder and QoderWork transport names distinct", () => {
    const qoder = JSON.parse(buildMulticaMCPImport("qoder", endpoint, token));
    const qoderWork = JSON.parse(
      buildMulticaMCPImport("qoderwork", endpoint, token),
    );

    expect(qoder.mcpServers.multica.type).toBe("http");
    expect(qoderWork.mcpServers.multica.type).toBe("streamable-http");
    expect(qoderWork.mcpServers.multica.headers.Authorization).toBe(
      "Bearer mul_test_secret",
    );
  });
});
