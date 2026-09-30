"use client";

import type { ConnectedApp } from "@multica/core/context-capabilities";
import { useT } from "../../../i18n";
import { StatusPill, type StatusTone } from "./connectors-ui";

/** Some account (the shared one, a group's or a person's) is connected, so
 * the first tool discovery has run: no tools then means it failed or found
 * nothing, and 刷新工具 is the way out rather than connecting an account. */
export function hasConnectedAccount(app: ConnectedApp): boolean {
  return app.sharedAccount.connected || app.usage.scenesConnected > 0 || app.usage.personsConnected > 0;
}

/** The app's state for this agent, in order of what matters most. Built
 * only from the connected-apps response. The runtime mounts an app only
 * with at least one allowed tool, so an app without one is not usable
 * however it is switched on. */
function useStatus(app: ConnectedApp): { tone: StatusTone; label: string } {
  const { t } = useT("agents");
  if (!app.added) return { tone: "muted", label: t(($) => $.tab_body.connected_apps.status_not_added) };
  if (app.connectorId !== null && !app.enabledInWorkspace) {
    return { tone: "muted", label: t(($) => $.tab_body.connectors.disabled_in_workspace) };
  }
  if (app.globalEnabled) {
    // Granted, but without a usable shared account nobody's run gets it.
    if (!app.sharedAccount.connected) {
      return { tone: "warning", label: t(($) => $.tab_body.connected_apps.status_everyone_no_account) };
    }
    return app.tools.allowed > 0
      ? { tone: "success", label: t(($) => $.tab_body.connected_apps.status_everyone) }
      : { tone: "warning", label: t(($) => $.tab_body.connected_apps.status_no_tools) };
  }
  if (app.offered) {
    // Before any account connects, the first connect discovers the tools;
    // after that, no tools means discovery needs a refresh.
    return app.tools.allowed === 0 && hasConnectedAccount(app)
      ? { tone: "warning", label: t(($) => $.tab_body.connected_apps.status_no_tools) }
      : { tone: "success", label: t(($) => $.tab_body.connected_apps.status_offered) };
  }
  return { tone: "muted", label: t(($) => $.tab_body.connected_apps.status_off) };
}

export function ConnectedAppStatusPill({ app }: { app: ConnectedApp }) {
  const status = useStatus(app);
  return <StatusPill tone={status.tone}>{status.label}</StatusPill>;
}
