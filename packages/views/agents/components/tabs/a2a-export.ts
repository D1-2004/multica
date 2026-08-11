export const A2A_TOKEN_ENV = "MULTICA_A2A_TOKEN" as const;
const A2A_TASK_ID_PLACEHOLDER = "<TASK_ID_FROM_SEND_MESSAGE>" as const;

type UUIDFactory = () => string;

interface A2AJSONRPCRequest {
  jsonrpc: "2.0";
  id: string;
  method: "SendMessage" | "GetTask";
  params: Record<string, unknown>;
}

function fallbackUUID(): string {
  const bytes = new Uint8Array(16);
  const cryptoApi = globalThis.crypto;

  if (typeof cryptoApi?.getRandomValues === "function") {
    cryptoApi.getRandomValues(bytes);
  } else {
    for (let index = 0; index < bytes.length; index += 1) {
      bytes[index] = Math.floor(Math.random() * 256);
    }
  }

  bytes[6] = (bytes[6]! & 0x0f) | 0x40;
  bytes[8] = (bytes[8]! & 0x3f) | 0x80;
  const hex = Array.from(bytes, (value) =>
    value.toString(16).padStart(2, "0"),
  ).join("");
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`;
}

function createUUID(): string {
  const cryptoApi = globalThis.crypto;
  return typeof cryptoApi?.randomUUID === "function"
    ? cryptoApi.randomUUID()
    : fallbackUUID();
}

function buildSendMessageRequest(
  text: string,
  uuidFactory: UUIDFactory,
): A2AJSONRPCRequest {
  return {
    jsonrpc: "2.0",
    id: uuidFactory(),
    method: "SendMessage",
    params: {
      configuration: {
        returnImmediately: true,
        acceptedOutputModes: ["text/plain"],
      },
      message: {
        messageId: uuidFactory(),
        role: "ROLE_USER",
        parts: [{ text }],
      },
    },
  };
}

function buildGetTaskRequest(uuidFactory: UUIDFactory): A2AJSONRPCRequest {
  return {
    jsonrpc: "2.0",
    id: uuidFactory(),
    method: "GetTask",
    params: { id: A2A_TASK_ID_PLACEHOLDER },
  };
}

export interface MulticaA2AExport {
  rpcUrl: string;
  agentCardUrl: string;
  protocolVersion: string;
  preferredBinding: string;
  tokenEnv: typeof A2A_TOKEN_ENV;
}

export function buildMulticaA2AExport({
  rpcUrl,
  agentCardUrl,
  protocolVersion,
  preferredBinding,
}: {
  rpcUrl: string;
  agentCardUrl: string;
  protocolVersion: string;
  preferredBinding: string;
}): MulticaA2AExport {
  return {
    rpcUrl,
    agentCardUrl,
    protocolVersion,
    preferredBinding,
    tokenEnv: A2A_TOKEN_ENV,
  };
}

export interface A2ALocalDebugConfig {
  schemaVersion: "multica.a2a.local/v1";
  rpcUrl: string;
  agentCardUrl: string;
  token: string;
  protocolVersion: string;
  preferredBinding: string;
  headers: {
    Authorization: string;
    "A2A-Version": string;
    "Content-Type": "application/json";
  };
  examples: {
    sendMessage: A2AJSONRPCRequest;
    getTask: A2AJSONRPCRequest;
  };
}

interface A2ALocalDebugInput {
  rpcUrl: string;
  agentCardUrl: string;
  token: string;
  protocolVersion: string;
  preferredBinding: string;
  uuidFactory?: UUIDFactory;
}

export interface A2ALocalDebugBundle {
  config: A2ALocalDebugConfig;
  curl: string;
}

export function buildA2ALocalDebugBundle({
  rpcUrl,
  agentCardUrl,
  token,
  protocolVersion,
  preferredBinding,
  uuidFactory = createUUID,
}: A2ALocalDebugInput): A2ALocalDebugBundle {
  const sendMessage = buildSendMessageRequest(
    "Hello from a local A2A client",
    uuidFactory,
  );
  const getTask = buildGetTaskRequest(uuidFactory);
  const config: A2ALocalDebugConfig = {
    schemaVersion: "multica.a2a.local/v1",
    rpcUrl,
    agentCardUrl,
    token,
    protocolVersion,
    preferredBinding,
    headers: {
      Authorization: `Bearer ${token}`,
      "A2A-Version": protocolVersion,
      "Content-Type": "application/json",
    },
    examples: { sendMessage, getTask },
  };

  return {
    config,
    curl: buildA2ALocalCurlFromRequests({
      rpcUrl,
      token,
      protocolVersion,
      sendMessage,
      getTask,
    }),
  };
}

export function buildA2ALocalDebugConfig(
  input: A2ALocalDebugInput,
): A2ALocalDebugConfig {
  return buildA2ALocalDebugBundle(input).config;
}

export function serializeJson(value: unknown): string {
  return `${JSON.stringify(value, null, 2)}\n`;
}

export function buildA2ACurlExample({
  rpcUrl,
  protocolVersion,
  uuidFactory = createUUID,
}: {
  rpcUrl: string;
  protocolVersion: string;
  uuidFactory?: UUIDFactory;
}): string {
  const body = JSON.stringify(
    buildSendMessageRequest("Hello from an A2A client", uuidFactory),
  );

  return [
    `curl --request POST '${rpcUrl}' \\`,
    `  --header 'A2A-Version: ${protocolVersion}' \\`,
    `  --header "Authorization: Bearer $${A2A_TOKEN_ENV}" \\`,
    "  --header 'Content-Type: application/json' \\",
    `  --data '${body}'`,
  ].join("\n");
}

function shellQuote(value: string): string {
  return `'${value.replaceAll("'", `'"'"'`)}'`;
}

export interface CodingAgentMCPBundle {
  mcpUrl: string;
  connectUrl: string;
  authorizationHeader: string;
  codexCommand: string;
  claudeCommand: string;
  openCodeCommand: string;
}

/**
 * Build copy-paste connection material from the canonical Agent MCP URL. The
 * connect URL is deliberately secret-bearing for clients that cannot persist
 * a literal header; revoking the underlying credential invalidates both forms.
 */
export function buildCodingAgentMCPBundle({
  mcpUrl,
  token,
  publicAgentId,
}: {
  mcpUrl: string;
  token: string;
  publicAgentId: string;
}): CodingAgentMCPBundle {
  const parsed = new URL(mcpUrl);
  const marker = "/api/mcp/agents/";
  const markerIndex = parsed.pathname.lastIndexOf(marker);
  if (markerIndex < 0) {
    throw new Error("invalid Multica Agent MCP URL");
  }
  parsed.pathname = `${parsed.pathname.slice(0, markerIndex)}/api/mcp/connect/${encodeURIComponent(token)}`;
  parsed.search = "";
  parsed.hash = "";

  const suffix = publicAgentId
    .toLowerCase()
    .replaceAll(/[^a-z0-9_-]/g, "-")
    .replaceAll(/^-+|-+$/g, "")
    .slice(0, 24);
  const serverName = `multica-${suffix || "agent"}`;
  const connectUrl = parsed.toString();
  return {
    mcpUrl,
    connectUrl,
    authorizationHeader: `Authorization: Bearer ${token}`,
    codexCommand: `codex mcp add ${serverName} --url ${shellQuote(connectUrl)}`,
    claudeCommand: `claude mcp add --transport http --scope user ${serverName} ${shellQuote(connectUrl)}`,
    openCodeCommand: `opencode mcp add ${serverName} --url ${shellQuote(mcpUrl)} --header ${shellQuote(`X-API-Key=${token}`)}`,
  };
}

export function buildA2ALocalCurlExample({
  rpcUrl,
  token,
  protocolVersion,
  uuidFactory = createUUID,
}: {
  rpcUrl: string;
  token: string;
  protocolVersion: string;
  uuidFactory?: UUIDFactory;
}): string {
  return buildA2ALocalDebugBundle({
    rpcUrl,
    agentCardUrl: "",
    token,
    protocolVersion,
    preferredBinding: "JSONRPC",
    uuidFactory,
  }).curl;
}

function buildA2ALocalCurlFromRequests({
  rpcUrl,
  token,
  protocolVersion,
  sendMessage,
  getTask,
}: {
  rpcUrl: string;
  token: string;
  protocolVersion: string;
  sendMessage: A2AJSONRPCRequest;
  getTask: A2AJSONRPCRequest;
}): string {
  const sendBody = JSON.stringify(sendMessage);
  const getBody = JSON.stringify(getTask);

  return [
    "# SendMessage",
    `curl --request POST ${shellQuote(rpcUrl)} \\`,
    `  --header ${shellQuote(`A2A-Version: ${protocolVersion}`)} \\`,
    `  --header ${shellQuote(`Authorization: Bearer ${token}`)} \\`,
    `  --header 'Content-Type: application/json' \\`,
    `  --data ${shellQuote(sendBody)}`,
    "",
    "# GetTask (A2A tasks/get): replace the placeholder with result.task.id from SendMessage",
    `curl --request POST ${shellQuote(rpcUrl)} \\`,
    `  --header ${shellQuote(`A2A-Version: ${protocolVersion}`)} \\`,
    `  --header ${shellQuote(`Authorization: Bearer ${token}`)} \\`,
    `  --header 'Content-Type: application/json' \\`,
    `  --data ${shellQuote(getBody)}`,
  ].join("\n");
}
