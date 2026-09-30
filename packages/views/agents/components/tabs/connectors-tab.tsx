"use client";

import { Globe } from "lucide-react";
import type { Agent, AgentRuntime } from "@multica/core/types";
import { useWorkspaceId } from "@multica/core/hooks";
import { useCurrentMember } from "@multica/core/permissions";
import { useT } from "../../../i18n";
import { AgentConnectorsSection } from "./agent-connectors-section";
import { ContextOffersSection } from "./context-offers-section";
import { McpConfigTab } from "./mcp-config-tab";

/**
 * 配置 → 能力 → 连接器: the agent's global connector configuration. Top to
 * bottom: (a) connectors enabled for this agent (official apps and Aone
 * FaaS grants, with the shared-account controls and 添加连接器), (b) the
 * connector part of the offer catalog (允许在场域 / 个人中开启), (c) the
 * agent's own MCP servers, runtime-inherited servers and Runner servers —
 * shown only when the runtime reads mcp_config; (a) and (b) go through the
 * server relay and apply to every runtime.
 */
export function ConnectorsTab({
  agent,
  runtime,
  canEdit,
  supportsOwnMcpConfig = true,
  onSave,
  onDirtyChange,
}: {
  agent: Agent;
  runtime: AgentRuntime | null;
  canEdit: boolean;
  supportsOwnMcpConfig?: boolean;
  onSave: (updates: { mcp_config: unknown | null }) => Promise<void>;
  onDirtyChange?: (dirty: boolean) => void;
}) {
  const { t } = useT("agents");
  const wsId = useWorkspaceId();
  const { role } = useCurrentMember(wsId);
  const isWorkspaceAdmin = role === "owner" || role === "admin";

  return (
    <div className="space-y-10">
      <p className="flex items-center gap-2 text-caption text-muted-foreground">
        <Globe className="size-3.5 shrink-0" aria-hidden="true" />
        {t(($) => $.tab_body.connectors.note)}
      </p>

      <AgentConnectorsSection agent={agent} />

      {canEdit ? (
        <ContextOffersSection
          agentId={agent.id}
          wsId={wsId}
          resourceType="connector"
          isWorkspaceAdmin={isWorkspaceAdmin}
          showConfigureLink
        />
      ) : null}

      {supportsOwnMcpConfig ? (
        <div className="border-t pt-8">
          <McpConfigTab
            agent={agent}
            runtime={runtime}
            onSave={onSave}
            onDirtyChange={onDirtyChange}
            canEdit={canEdit}
          />
        </div>
      ) : null}
    </div>
  );
}
