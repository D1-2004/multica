"use client";

import type { Agent } from "@multica/core/types";
import { DingTalkAccountBindingCard } from "../integrations/dingtalk-account-binding";

export function IdentityTab({ agent }: { agent: Agent }) {
  return (
    <DingTalkAccountBindingCard
      agentId={agent.id}
      agentName={agent.name}
      bindingMode="identity"
    />
  );
}
