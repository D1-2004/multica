"use client";

import type { Agent, AgentRuntime } from "@multica/core/types";
import { isFCE2BRuntime } from "@multica/core/runtimes";
import { useT } from "../../../i18n";
import { DshHomeTab } from "./dsh-home-tab";
import { DshPluginsTab } from "./dsh-plugins-tab";

export function DshConfigTab({ workspaceId, agent, runtime, canEdit }: {
  workspaceId: string;
  agent: Agent;
  runtime: AgentRuntime | null;
  canEdit: boolean;
}) {
  const { t } = useT("agents");
  const showHome = canEdit && agent.runtime_mode === "cloud" &&
    runtime?.provider === "dsh" && isFCE2BRuntime(runtime);

  return (
    <div className="space-y-10">
      {showHome && (
        <section className="space-y-4">
          <h3 className="text-body font-medium">
            {t(($) => $.tab_body.dsh_config.home_title)}
          </h3>
          <DshHomeTab workspaceId={workspaceId} agentId={agent.id} />
        </section>
      )}
      <DshPluginsTab agent={agent} runtime={runtime} canEdit={canEdit} />
    </div>
  );
}
