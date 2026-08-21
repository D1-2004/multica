export type MulticaMCPClient = "codex" | "claude" | "qoder" | "qoderwork";

export interface MulticaMCPClientOption {
  key: MulticaMCPClient;
  name: string;
  logoProvider: "codex" | "claude" | "qoder";
}

export const MULTICA_MCP_CLIENTS: readonly MulticaMCPClientOption[] = [
  { key: "codex", name: "Codex", logoProvider: "codex" },
  { key: "claude", name: "Claude", logoProvider: "claude" },
  { key: "qoder", name: "Qoder", logoProvider: "qoder" },
  { key: "qoderwork", name: "QoderWork", logoProvider: "qoder" },
] as const;

export function buildMulticaMCPEndpoint(
  apiBaseUrl: string,
  currentOrigin: string,
): string {
  const base = apiBaseUrl.trim().replace(/\/+$/, "");
  const origin = currentOrigin.trim().replace(/\/+$/, "");
  return `${base || origin}/api/mcp`;
}

function shellQuote(value: string): string {
  return `'${value.replaceAll("'", `'"'"'`)}'`;
}

function buildJSONImport(
  endpoint: string,
  token: string,
  transport: "http" | "streamable-http",
): string {
  return JSON.stringify(
    {
      mcpServers: {
        multica: {
          type: transport,
          url: endpoint,
          headers: {
            Authorization: `Bearer ${token}`,
          },
        },
      },
    },
    null,
    2,
  );
}

export function buildMulticaMCPImport(
  client: MulticaMCPClient,
  endpoint: string,
  token: string,
): string {
  switch (client) {
    case "codex":
      return [
        "[mcp_servers.multica]",
        `url = ${JSON.stringify(endpoint)}`,
        `http_headers = { Authorization = ${JSON.stringify(`Bearer ${token}`)} }`,
        "enabled = true",
      ].join("\n");
    case "claude":
      return [
        "claude mcp add --transport http --scope user \\",
        `  --header ${shellQuote(`Authorization: Bearer ${token}`)} \\`,
        `  multica ${shellQuote(endpoint)}`,
      ].join("\n");
    case "qoder":
      return buildJSONImport(endpoint, token, "http");
    case "qoderwork":
      return buildJSONImport(endpoint, token, "streamable-http");
  }
}
