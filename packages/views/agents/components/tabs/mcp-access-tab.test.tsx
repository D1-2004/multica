// @vitest-environment jsdom

import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import type { Agent } from "@multica/core/types";

vi.mock("../integrations/mcp-link-card", () => ({
  AgentMCPLinkCard: ({ agent }: { agent: Agent }) => (
    <section aria-label="MCP access" data-agent-id={agent.id} />
  ),
}));

import { AgentMCPAccessTab } from "./mcp-access-tab";

describe("AgentMCPAccessTab", () => {
  it("contains the inbound MCP endpoint for this agent", () => {
    render(<AgentMCPAccessTab agent={{ id: "agent-1" } as Agent} />);
    expect(screen.getByRole("region", { name: "MCP access" })).toHaveAttribute(
      "data-agent-id",
      "agent-1",
    );
  });
});
