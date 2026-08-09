import { describe, expect, it } from "vitest";
import {
  A2A_TOKEN_ENV,
  buildA2ACurlExample,
  buildMulticaA2AExport,
  serializeJson,
} from "./a2a-export";

describe("A2A export helpers", () => {
  it("builds the non-standard Multica preset with only public connection metadata", () => {
    const preset = buildMulticaA2AExport({
      agentCardUrl: "https://multica.example/api/a2a/agents/public-1/.well-known/agent-card.json",
      protocolVersion: "1.0",
      preferredBinding: "JSONRPC",
    });

    expect(preset).toEqual({
      agentCardUrl: "https://multica.example/api/a2a/agents/public-1/.well-known/agent-card.json",
      protocolVersion: "1.0",
      preferredBinding: "JSONRPC",
      tokenEnv: A2A_TOKEN_ENV,
    });
    expect(Object.keys(preset)).toEqual([
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
    const example = buildA2ACurlExample({
      rpcUrl: "https://multica.example/api/a2a/agents/public-1/v1",
      protocolVersion: "1.0",
    });

    expect(example).toContain("Authorization: Bearer $MULTICA_A2A_TOKEN");
    expect(example).toContain("A2A-Version: 1.0");
    expect(example).toContain('"method":"SendMessage"');
    expect(example).toContain(
      '"configuration":{"returnImmediately":true,"acceptedOutputModes":["text/plain"]}',
    );
    expect(example).toContain('"role":"ROLE_USER"');
    expect(example).toContain('"parts":[{"text":"Hello from an A2A client"}]');
    expect(example).not.toContain("message/send");
    expect(example).not.toContain('"kind"');
    expect(example).not.toContain("mca2a_example-secret");
  });
});
