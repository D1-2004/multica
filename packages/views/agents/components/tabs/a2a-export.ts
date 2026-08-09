export const A2A_TOKEN_ENV = "MULTICA_A2A_TOKEN" as const;

export interface MulticaA2AExport {
  agentCardUrl: string;
  protocolVersion: string;
  preferredBinding: string;
  tokenEnv: typeof A2A_TOKEN_ENV;
}

export function buildMulticaA2AExport({
  agentCardUrl,
  protocolVersion,
  preferredBinding,
}: {
  agentCardUrl: string;
  protocolVersion: string;
  preferredBinding: string;
}): MulticaA2AExport {
  return {
    agentCardUrl,
    protocolVersion,
    preferredBinding,
    tokenEnv: A2A_TOKEN_ENV,
  };
}

export function serializeJson(value: unknown): string {
  return `${JSON.stringify(value, null, 2)}\n`;
}

export function buildA2ACurlExample({
  rpcUrl,
  protocolVersion,
}: {
  rpcUrl: string;
  protocolVersion: string;
}): string {
  const body = JSON.stringify({
    jsonrpc: "2.0",
    id: "request-1",
    method: "SendMessage",
    params: {
      configuration: {
        returnImmediately: true,
        acceptedOutputModes: ["text/plain"],
      },
      message: {
        messageId: "message-1",
        role: "ROLE_USER",
        parts: [{ text: "Hello from an A2A client" }],
      },
    },
  });

  return [
    `curl --request POST '${rpcUrl}' \\`,
    `  --header 'A2A-Version: ${protocolVersion}' \\`,
    `  --header "Authorization: Bearer $${A2A_TOKEN_ENV}" \\`,
    "  --header 'Content-Type: application/json' \\",
    `  --data '${body}'`,
  ].join("\n");
}
