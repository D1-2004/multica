import { describe, expect, it } from "vitest";
import {
  A2A_TOKEN_ENV,
  buildA2ACurlExample,
  buildA2ALocalCurlExample,
  buildA2ALocalDebugConfig,
  buildCodingAgentMCPBundle,
  buildMulticaA2AExport,
  serializeJson,
} from "./a2a-export";

describe("A2A export helpers", () => {
  it("builds the non-standard Multica preset with only public connection metadata", () => {
    const preset = buildMulticaA2AExport({
      rpcUrl: "https://multica.example/api/a2a/agents/public-1/v1",
      agentCardUrl: "https://multica.example/api/a2a/agents/public-1/.well-known/agent-card.json",
      protocolVersion: "1.0",
      preferredBinding: "JSONRPC",
    });

    expect(preset).toEqual({
      rpcUrl: "https://multica.example/api/a2a/agents/public-1/v1",
      agentCardUrl: "https://multica.example/api/a2a/agents/public-1/.well-known/agent-card.json",
      protocolVersion: "1.0",
      preferredBinding: "JSONRPC",
      tokenEnv: A2A_TOKEN_ENV,
    });
    expect(Object.keys(preset)).toEqual([
      "rpcUrl",
      "agentCardUrl",
      "protocolVersion",
      "preferredBinding",
      "tokenEnv",
    ]);

    const serialized = serializeJson(preset);
    expect(serialized).not.toMatch(/"token"\s*:/i);
    expect(serialized).not.toMatch(/"secret"\s*:/i);
    expect(serialized).not.toContain("mca2a_example-secret");
  });

  it("uses only the documented environment variable in the call example", () => {
    const ids = [
      "00000000-0000-4000-8000-000000000001",
      "00000000-0000-4000-8000-000000000002",
    ];
    const example = buildA2ACurlExample({
      rpcUrl: "https://multica.example/api/a2a/agents/public-1/v1",
      protocolVersion: "1.0",
      uuidFactory: () => ids.shift() ?? "unexpected-id",
    });

    expect(example).toContain("Authorization: Bearer $MULTICA_A2A_TOKEN");
    expect(example).toContain("A2A-Version: 1.0");
    expect(example).toContain('"method":"SendMessage"');
    expect(example).toContain(
      '"configuration":{"returnImmediately":true,"acceptedOutputModes":["text/plain"]}',
    );
    expect(example).toContain('"role":"ROLE_USER"');
    expect(example).toContain('"parts":[{"text":"Hello from an A2A client"}]');
    expect(example).toContain(
      '"messageId":"00000000-0000-4000-8000-000000000002"',
    );
    expect(example).not.toContain("message/send");
    expect(example).not.toContain('"kind"');
    expect(example).not.toContain("mca2a_example-secret");
  });

  it("builds an explicit one-time local debug bundle with the raw credential", () => {
    const ids = [
      "00000000-0000-4000-8000-000000000011",
      "00000000-0000-4000-8000-000000000012",
      "00000000-0000-4000-8000-000000000013",
    ];
    const config = buildA2ALocalDebugConfig({
      rpcUrl: "https://multica.example/api/a2a/agents/public-1/v1",
      agentCardUrl: "https://multica.example/api/a2a/agents/public-1/.well-known/agent-card.json",
      token: "mca2a_local-secret",
      protocolVersion: "1.0",
      preferredBinding: "JSONRPC",
      uuidFactory: () => ids.shift() ?? "unexpected-id",
    });

    expect(config).toEqual({
      schemaVersion: "multica.a2a.local/v1",
      rpcUrl: "https://multica.example/api/a2a/agents/public-1/v1",
      agentCardUrl: "https://multica.example/api/a2a/agents/public-1/.well-known/agent-card.json",
      token: "mca2a_local-secret",
      protocolVersion: "1.0",
      preferredBinding: "JSONRPC",
      headers: {
        Authorization: "Bearer mca2a_local-secret",
        "A2A-Version": "1.0",
        "Content-Type": "application/json",
      },
      examples: {
        sendMessage: {
          jsonrpc: "2.0",
          id: "00000000-0000-4000-8000-000000000011",
          method: "SendMessage",
          params: {
            configuration: {
              returnImmediately: true,
              acceptedOutputModes: ["text/plain"],
            },
            message: {
              messageId: "00000000-0000-4000-8000-000000000012",
              role: "ROLE_USER",
              parts: [{ text: "Hello from a local A2A client" }],
            },
          },
        },
        getTask: {
          jsonrpc: "2.0",
          id: "00000000-0000-4000-8000-000000000013",
          method: "GetTask",
          params: { id: "<TASK_ID_FROM_SEND_MESSAGE>" },
        },
      },
    });
  });

  it("builds an immediately executable local curl command and shell-quotes secrets", () => {
    const ids = [
      "00000000-0000-4000-8000-000000000021",
      "00000000-0000-4000-8000-000000000022",
      "00000000-0000-4000-8000-000000000023",
    ];
    const example = buildA2ALocalCurlExample({
      rpcUrl: "https://multica.example/api/a2a/agents/public-1/v1",
      token: "mca2a_local-'secret",
      protocolVersion: "1.0",
      uuidFactory: () => ids.shift() ?? "unexpected-id",
    });

    expect(example).toContain(
      "'Authorization: Bearer mca2a_local-'\"'\"'secret'",
    );
    expect(example).toContain("'A2A-Version: 1.0'");
    expect(example).toContain('"method":"SendMessage"');
    expect(example).toContain(
      '"messageId":"00000000-0000-4000-8000-000000000022"',
    );
    expect(example).toContain(
      "# GetTask (A2A tasks/get): replace the placeholder with result.task.id from SendMessage",
    );
    expect(example).toContain('"method":"GetTask"');
    expect(example).toContain('"id":"<TASK_ID_FROM_SEND_MESSAGE>"');
    expect(example).not.toContain("$MULTICA_A2A_TOKEN");
  });

  it("creates a fresh UUID message id for every generated curl or local config", () => {
    const first = buildA2ACurlExample({
      rpcUrl: "https://multica.example/api/a2a/agents/public-1/v1",
      protocolVersion: "1.0",
    });
    const second = buildA2ACurlExample({
      rpcUrl: "https://multica.example/api/a2a/agents/public-1/v1",
      protocolVersion: "1.0",
    });
    const messageId = /"messageId":"([0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12})"/i;
    const firstId = first.match(messageId)?.[1];
    const secondId = second.match(messageId)?.[1];

    expect(firstId).toBeTruthy();
    expect(secondId).toBeTruthy();
    expect(firstId).not.toBe(secondId);

    const localInput = {
      rpcUrl: "https://multica.example/api/a2a/agents/public-1/v1",
      agentCardUrl:
        "https://multica.example/api/a2a/agents/public-1/.well-known/agent-card.json",
      token: "mca2a_local-secret",
      protocolVersion: "1.0",
      preferredBinding: "JSONRPC",
    };
    const firstConfig = serializeJson(buildA2ALocalDebugConfig(localInput));
    const secondConfig = serializeJson(buildA2ALocalDebugConfig(localInput));
    const configMessageId =
      /"messageId": "([0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12})"/i;

    expect(firstConfig.match(configMessageId)?.[1]).toBeTruthy();
    expect(secondConfig.match(configMessageId)?.[1]).toBeTruthy();
    expect(firstConfig.match(configMessageId)?.[1]).not.toBe(
      secondConfig.match(configMessageId)?.[1],
    );
  });

  it("builds one-command MCP installation URLs that preserve a deployment base path", () => {
    const bundle = buildCodingAgentMCPBundle({
      mcpUrl: "https://multica.example/base/api/mcp/agents/public-agent-1",
      token: "mca2a_0123456789abcdef0123456789abcdef01234567",
      publicAgentId: "Public-Agent-1",
    });

    expect(bundle.connectUrl).toBe(
      "https://multica.example/base/api/mcp/connect/mca2a_0123456789abcdef0123456789abcdef01234567",
    );
    expect(bundle.codexCommand).toBe(
      "codex mcp add multica-public-agent-1 --url 'https://multica.example/base/api/mcp/connect/mca2a_0123456789abcdef0123456789abcdef01234567'",
    );
    expect(bundle.claudeCommand).toContain(
      "claude mcp add --transport http --scope user multica-public-agent-1",
    );
    expect(bundle.openCodeCommand).toBe(
      "opencode mcp add multica-public-agent-1 --url 'https://multica.example/base/api/mcp/agents/public-agent-1' --header 'X-API-Key=mca2a_0123456789abcdef0123456789abcdef01234567'",
    );
    expect(bundle.authorizationHeader).toBe(
      "Authorization: Bearer mca2a_0123456789abcdef0123456789abcdef01234567",
    );
  });
});
