/**
 * MCP servers of one configure-page scope (`{"mcpServers": {...}}`). The
 * configure page only adds remote URL servers (the server rejects local
 * commands), so a document holding anything else is listed read-only.
 */

export interface McpHeader {
  key: string;
  value: string;
}

export interface ScopeMcpServer {
  name: string;
  /** "" for a server without a URL (a local command). */
  url: string;
  /** Transport the server was stored with ("http", "sse",
   * "streamable-http"), "" when none was given. New servers are "http". */
  type: string;
  headers: McpHeader[];
  enabled: boolean;
  /** A remote URL server the configure page can save back. */
  remote: boolean;
}

export const MCP_SERVER_NAME_MAX_LENGTH = 64;
const SERVER_NAME = /^[A-Za-z0-9_-]+$/;
// Names the runtime keeps for its own servers.
const RESERVED_NAME = /^(multica|c[0-9a-f]{16})$/i;
const HEADER_NAME = /^[!#$%&'*+.^_`|~0-9A-Za-z-]+$/;
const REMOTE_KEYS = new Set(["url", "type", "headers", "disabled"]);
const REMOTE_TYPES = new Set(["http", "sse", "streamable-http"]);

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

/** An http(s) URL with a host. */
export function isRemoteMcpUrl(value: string): boolean {
  try {
    const url = new URL(value);
    return (url.protocol === "https:" || url.protocol === "http:") && url.hostname !== "";
  } catch {
    return false;
  }
}

function stringHeaders(value: unknown): McpHeader[] | null {
  if (value === undefined) return [];
  if (!isRecord(value)) return null;
  const pairs = Object.entries(value);
  if (pairs.some(([, item]) => typeof item !== "string")) return null;
  return pairs.map(([key, item]) => ({ key, value: item as string }));
}

function serverOf(name: string, entry: Record<string, unknown>): ScopeMcpServer {
  const url = typeof entry.url === "string" ? entry.url : "";
  const type = typeof entry.type === "string" ? entry.type : "";
  const headers = stringHeaders(entry.headers);
  const remote =
    Object.keys(entry).every((key) => REMOTE_KEYS.has(key)) &&
    isRemoteMcpUrl(url) &&
    (entry.type === undefined || REMOTE_TYPES.has(type)) &&
    (entry.disabled === undefined || typeof entry.disabled === "boolean") &&
    headers !== null;
  return {
    name,
    url,
    type,
    headers: headers ?? [],
    enabled: entry.disabled !== true && entry.enabled !== false,
    remote,
  };
}

export interface ScopeMcpDocument {
  servers: ScopeMcpServer[];
  /** Every server is a remote URL server in `mcpServers`, so the page may
   * rewrite the document. */
  editable: boolean;
}

/** Reads a scope's MCP document. A null document is an empty, editable one. */
export function readScopeMcpDocument(config: Record<string, unknown> | null): ScopeMcpDocument {
  if (config === null) return { servers: [], editable: true };
  const container = config.mcpServers;
  let editable =
    Object.keys(config).every((key) => key === "mcpServers") && (container === undefined || isRecord(container));
  const servers: ScopeMcpServer[] = [];
  if (isRecord(container)) {
    for (const [name, entry] of Object.entries(container)) {
      if (!isRecord(entry)) {
        editable = false;
        continue;
      }
      const server = serverOf(name, entry);
      if (!server.remote) editable = false;
      servers.push(server);
    }
  }
  return { servers: servers.sort((a, b) => a.name.localeCompare(b.name)), editable };
}

/** The `mcp_config` document of a list of remote servers. */
export function scopeMcpDocument(servers: ScopeMcpServer[]): Record<string, unknown> {
  const mcpServers: Record<string, unknown> = {};
  for (const server of servers) {
    const entry: Record<string, unknown> = server.type ? { type: server.type, url: server.url } : { url: server.url };
    if (server.headers.length > 0) {
      entry.headers = Object.fromEntries(server.headers.map((header) => [header.key, header.value]));
    }
    if (!server.enabled) entry.disabled = true;
    mcpServers[server.name] = entry;
  }
  return { mcpServers };
}

export type McpServerProblem =
  | "name_required"
  | "name_invalid"
  | "name_reserved"
  | "name_duplicate"
  | "url_required"
  | "url_invalid"
  | "header_invalid";

/** The first rule a server form breaks, or null. Values are trimmed. */
export function mcpServerProblem(
  server: Pick<ScopeMcpServer, "name" | "url" | "headers">,
  otherNames: ReadonlySet<string>,
): McpServerProblem | null {
  if (server.name === "") return "name_required";
  if (server.name.length > MCP_SERVER_NAME_MAX_LENGTH || !SERVER_NAME.test(server.name)) return "name_invalid";
  if (RESERVED_NAME.test(server.name)) return "name_reserved";
  if (otherNames.has(server.name)) return "name_duplicate";
  if (server.url === "") return "url_required";
  if (!isRemoteMcpUrl(server.url)) return "url_invalid";
  const keys = new Set<string>();
  for (const header of server.headers) {
    if (!HEADER_NAME.test(header.key) || /[\r\n\0]/.test(header.value)) return "header_invalid";
    const lower = header.key.toLowerCase();
    if (keys.has(lower)) return "header_invalid";
    keys.add(lower);
  }
  return null;
}
