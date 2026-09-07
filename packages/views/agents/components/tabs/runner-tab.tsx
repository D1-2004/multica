"use client";

import { useQuery } from "@tanstack/react-query";
import { Loader2, Server } from "lucide-react";
import { toast } from "sonner";
import type { Agent } from "@multica/core/types";
import { useWorkspaceId } from "@multica/core/hooks";
import { useAuthStore } from "@multica/core/auth";
import { accountRunnerBindingsOptions, agentRunnerBindingsOptions, useMountAgentRunnerMachine, useRevokeAgentRunnerBinding } from "@multica/core/runner";
import { Button } from "@multica/ui/components/ui/button";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@multica/ui/components/ui/select";
import { useT } from "../../../i18n";
import { McpServerDetailsList } from "../../../runner/mcp-server-details-list";

// Pairing and machine lifecycle are account concerns in General settings;
// this panel selects the Agent execution mount and shows its dynamic inventory.
export function RunnerTab({ agent, canBind, mode = "all" }: { agent: Agent; canBind: boolean; mode?: "all" | "execution" | "mcp" }) {
  const { t } = useT("agents");
  const workspaceId = useWorkspaceId();
  const userId = useAuthStore((state) => state.user?.id ?? "");
  const account = useQuery(accountRunnerBindingsOptions(userId));
  const mounts = useQuery(agentRunnerBindingsOptions(workspaceId, agent.id));
  const mountMachine = useMountAgentRunnerMachine(workspaceId, agent.id);
  const revokeMount = useRevokeAgentRunnerBinding(workspaceId, agent.id);
  const mounted = mounts.data?.machines[0];
  const accountMachines = account.data?.machines ?? [];
  const selectableMachines =
    mounted &&
    !accountMachines.some((machine) => machine.machineId === mounted.machineId)
      ? [mounted, ...accountMachines]
      : accountMachines;
  const runnerItems = [
    { value: "__none__", label: t(($) => $.tab_body.runner.none) },
    ...selectableMachines.map((machine) => ({
      value: machine.machineId,
      label: `${machine.name} · ${machine.online
        ? t(($) => $.tab_body.runner.online)
        : t(($) => $.tab_body.runner.offline)}`,
    })),
  ];

  const selectMachine = async (machineId: string) => {
    try {
      if (machineId === "__none__") {
        if (!mounted) return;
        await revokeMount.mutateAsync(mounted.bindingId);
      } else {
        await mountMachine.mutateAsync(machineId);
      }
      toast.success(t(($) => $.tab_body.runner.mounted_toast));
    } catch (error) {
      toast.error(error instanceof Error ? error.message : t(($) => $.tab_body.runner.create_failed_toast));
    }
  };

  return (
    <div className="space-y-6">
      {mode !== "mcp" ? (
        <section className="space-y-3">
          <div>
            <h3 className="text-body font-medium text-pretty">
              {t(($) => $.tab_body.runner.execution_title)}
            </h3>
            <p className="mt-1 text-caption leading-5 text-muted-foreground text-pretty">
              {t(($) => $.tab_body.runner.execution_hint)}
            </p>
          </div>
          <Select
            items={runnerItems}
            value={mounted?.machineId ?? "__none__"}
            onValueChange={(machineId) => {
              if (machineId) void selectMachine(machineId);
            }}
            disabled={
              !canBind ||
              account.isLoading ||
              mounts.isLoading ||
              mountMachine.isPending ||
              revokeMount.isPending
            }
          >
            <SelectTrigger
              className="w-full"
              aria-label={t(($) => $.tab_body.runner.execution_title)}
            >
              {account.isLoading || mounts.isLoading ? (
                <span className="flex items-center gap-2 text-muted-foreground">
                  <Loader2
                    className="size-4 animate-spin motion-reduce:animate-none"
                    aria-hidden
                  />
                  {t(($) => $.tab_body.runner.loading)}
                </span>
              ) : mounted ? (
                <RunnerMachineStatus
                  name={mounted.name}
                  online={mounted.online}
                />
              ) : (
                <SelectValue />
              )}
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="__none__">
                {t(($) => $.tab_body.runner.none)}
              </SelectItem>
              {selectableMachines.map((machine) => (
                <SelectItem key={machine.machineId} value={machine.machineId}>
                  <RunnerMachineStatus
                    name={machine.name}
                    online={machine.online}
                  />
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </section>
      ) : null}
	  {mode !== "execution" && <section className="space-y-3">
        <div><p className="text-body font-medium">{t(($) => $.tab_body.runner.local_mcp_title)}</p><p className="text-caption text-muted-foreground">{t(($) => $.tab_body.runner.local_mcp_hint)}</p></div>
        {!mounted ? <RunnerMcpNotice text={t(($) => $.tab_body.runner.mount_first)} /> : mounted.mcpServers.length === 0 ? <RunnerMcpNotice text={mounted.online ? t(($) => $.tab_body.runner.mcp_empty) : t(($) => $.tab_body.runner.mcp_offline)} /> : (
          <McpServerDetailsList servers={mounted.mcpServers} online={mounted.online} />
        )}
	  </section>}
      {!canBind && mode !== "execution" && <Button variant="outline" disabled>{t(($) => $.tab_body.runner.read_only)}</Button>}
    </div>
  );
}

function RunnerMachineStatus({
  name,
  online,
}: {
  name: string;
  online: boolean;
}) {
  const { t } = useT("agents");
  const status = online
    ? t(($) => $.tab_body.runner.online)
    : t(($) => $.tab_body.runner.offline);

  return (
    <span className="flex min-w-0 flex-1 items-center gap-2">
      <span className="min-w-0 flex-1 truncate">{name}</span>
      <span
        className={`h-1.5 w-1.5 shrink-0 rounded-full ${
          online ? "bg-success" : "bg-muted-foreground/40"
        }`}
        aria-label={status}
        title={status}
      />
    </span>
  );
}

function RunnerMcpNotice({ text }: { text: string }) {
  return (
    <div className="flex items-center gap-2 rounded-lg border border-dashed px-4 py-6 text-caption text-muted-foreground">
      <Server className="h-4 w-4 shrink-0" aria-hidden="true" />
      <span>{text}</span>
    </div>
  );
}
