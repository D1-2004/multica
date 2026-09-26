"use client";

import { useCurrentWorkspace } from "@multica/core/paths";
import { useT } from "../../i18n";
import { MCPSetupCard } from "./mcp-setup-card";
import { SettingsTab } from "./settings-layout";

export function MCPConnectionsTab() {
  const { t } = useT("settings");
  const workspace = useCurrentWorkspace();
  return (
    <SettingsTab
      title={t(($) => $.mcp.title)}
      description={t(($) => $.mcp.workspace.description)}
    >
      {workspace && (
        <MCPSetupCard
          key={workspace.id}
          workspaceId={workspace.id}
          workspaceName={workspace.name}
        />
      )}
    </SettingsTab>
  );
}
