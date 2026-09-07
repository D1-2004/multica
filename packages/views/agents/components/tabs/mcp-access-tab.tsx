"use client";

import type { Agent } from "@multica/core/types";
import { AgentMCPLinkCard } from "../integrations/mcp-link-card";

export function AgentMCPAccessTab({ agent }: { agent: Agent }) {
  return <AgentMCPLinkCard agent={agent} />;
}
