"use client";

import { Globe } from "lucide-react";
import type { Agent, AgentRuntime } from "@multica/core/types";
import { useWorkspaceId } from "@multica/core/hooks";
import { useCurrentMember } from "@multica/core/permissions";
import { useT } from "../../../i18n";
import { AoneConnectorsSection } from "./aone-connectors-section";
import { ConnectedAppsSection } from "./connected-apps-section";
import { ConnectorNotice, SectionHeading } from "./connectors-ui";
import { McpConfigTab } from "./mcp-config-tab";

/**
 * 配置 → 能力 → 连接器, the agent's global connector configuration in two
 * blocks:
 *
 * A. 「MCP（由 Multica 管理）」: the Aone FaaS connectors granted to the
 *    agent (with their per-connector offer switch), then the agent's own MCP
 *    servers, runtime-inherited servers and Runner servers — the latter only
 *    when the runtime reads mcp_config.
 * B. 「连接应用」: the official apps (GitHub, Notion, ...), each with its own
 *    configuration page (`?app=<slug>`).
 *
 * Aone FaaS connectors and apps go through the server relay and apply to
 * every runtime.
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
  const { role, isLoading: memberLoading } = useCurrentMember(wsId);
  const isAdmin = role === "owner" || role === "admin";

  return (
    <div className="space-y-10">
      <p className="flex items-center gap-2 text-caption text-muted-foreground">
        <Globe className="size-3.5 shrink-0" aria-hidden="true" />
        {t(($) => $.tab_body.connectors.note)}
      </p>

      <section className="space-y-6" aria-labelledby="managed-mcp-title">
        <SectionHeading
          id="managed-mcp-title"
          level={2}
          title={t(($) => $.tab_body.connectors.mcp_title)}
          hint={t(($) => $.tab_body.connectors.mcp_hint)}
        />
        {memberLoading ? (
          <ConnectorNotice loading>{t(($) => $.tab_body.connectors.loading)}</ConnectorNotice>
        ) : (
          <AoneConnectorsSection agent={agent} wsId={wsId} canEdit={canEdit} isAdmin={isAdmin} />
        )}
        {supportsOwnMcpConfig ? (
          <section className="space-y-3 border-t pt-6" aria-labelledby="custom-mcp-title">
            <SectionHeading
              id="custom-mcp-title"
              level={3}
              title={t(($) => $.tab_body.connectors.custom_title)}
            />
            <McpConfigTab
              agent={agent}
              runtime={runtime}
              onSave={onSave}
              onDirtyChange={onDirtyChange}
              canEdit={canEdit}
            />
          </section>
        ) : null}
      </section>

      <div className="border-t pt-8">
        <ConnectedAppsSection agent={agent} wsId={wsId} canEdit={canEdit} />
      </div>
    </div>
  );
}
