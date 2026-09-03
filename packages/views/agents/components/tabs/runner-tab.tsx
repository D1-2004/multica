"use client";

import { useQuery } from "@tanstack/react-query";
import { Laptop, Loader2, Wifi, WifiOff } from "lucide-react";
import { toast } from "sonner";
import type { Agent } from "@multica/core/types";
import { useWorkspaceId } from "@multica/core/hooks";
import { useAuthStore } from "@multica/core/auth";
import { accountRunnerBindingsOptions, agentRunnerBindingsOptions, useMountAgentRunnerMachine, useSetAgentRunnerMcpServerEnabled } from "@multica/core/runner";
import { Badge } from "@multica/ui/components/ui/badge";
import { Button } from "@multica/ui/components/ui/button";
import { Switch } from "@multica/ui/components/ui/switch";
import { useT } from "../../../i18n";

// Pairing and machine lifecycle are account concerns in General settings;
// this panel only selects the Agent execution mount and enabled capabilities.
export function RunnerTab({ agent, canBind, mode = "all" }: { agent: Agent; canBind: boolean; mode?: "all" | "execution" | "mcp" }) {
  const { t } = useT("agents");
  const workspaceId = useWorkspaceId();
  const userId = useAuthStore((state) => state.user?.id ?? "");
  const account = useQuery(accountRunnerBindingsOptions(userId));
  const mounts = useQuery(agentRunnerBindingsOptions(workspaceId, agent.id));
  const mountMachine = useMountAgentRunnerMachine(workspaceId, agent.id);
  const setServer = useSetAgentRunnerMcpServerEnabled(workspaceId, agent.id);
  const mounted = mounts.data?.machines[0];

  const selectMachine = async (machineId: string) => {
    try {
      await mountMachine.mutateAsync(machineId);
      toast.success(t(($) => $.tab_body.runner.mounted_toast));
    } catch (error) {
      toast.error(error instanceof Error ? error.message : t(($) => $.tab_body.runner.create_failed_toast));
    }
  };

  const toggleServer = async (serverName: string, fingerprint: string, enabled: boolean) => {
    if (!mounted) return;
    try {
      await setServer.mutateAsync({ bindingId: mounted.bindingId, serverName, fingerprint, enabled });
    } catch (error) {
      toast.error(error instanceof Error ? error.message : t(($) => $.tab_body.runner.create_failed_toast));
    }
  };

  return (
    <div className="space-y-6">
	  {mode !== "mcp" && <section className="space-y-3">
        <div><p className="text-sm font-medium">{t(($) => $.tab_body.runner.execution_title)}</p><p className="text-xs text-muted-foreground">{t(($) => $.tab_body.runner.execution_hint)}</p></div>
        {account.isLoading || mounts.isLoading ? (
          <div className="flex items-center gap-2 text-sm text-muted-foreground"><Loader2 className="size-4 animate-spin" />{t(($) => $.tab_body.runner.loading)}</div>
        ) : (account.data?.machines.length ?? 0) === 0 ? (
          <p className="rounded-lg border border-dashed p-4 text-sm text-muted-foreground">{t(($) => $.tab_body.runner.account_empty)}</p>
        ) : (
          <div className="grid gap-2">{account.data?.machines.map((machine) => {
            const selected = mounted?.machineId === machine.machineId;
            return <button key={machine.machineId} type="button" disabled={!canBind || mountMachine.isPending} onClick={() => void selectMachine(machine.machineId)} className="flex items-center justify-between rounded-lg border px-3 py-3 text-left disabled:opacity-60">
              <span className="flex min-w-0 items-center gap-2"><Laptop className="size-4" /><span className="truncate text-sm font-medium">{machine.name}</span></span>
              <span className="flex items-center gap-2"><Badge variant="outline">{machine.online ? <Wifi className="mr-1 size-3" /> : <WifiOff className="mr-1 size-3" />}{machine.online ? "Online" : "Offline"}</Badge>{selected && <Badge>{t(($) => $.tab_body.runner.mounted)}</Badge>}</span>
            </button>;
          })}</div>
        )}
	  </section>}
	  {mode !== "execution" && <section className="space-y-3">
        <div><p className="text-sm font-medium">{t(($) => $.tab_body.runner.local_mcp_title)}</p><p className="text-xs text-muted-foreground">{t(($) => $.tab_body.runner.local_mcp_hint)}</p></div>
        {!mounted ? <p className="text-sm text-muted-foreground">{t(($) => $.tab_body.runner.mount_first)}</p> : mounted.mcpServers.length === 0 ? <p className="text-sm text-muted-foreground">{mounted.online ? t(($) => $.tab_body.runner.mcp_empty) : t(($) => $.tab_body.runner.mcp_offline)}</p> : (
          <ul className="divide-y rounded-lg border">{mounted.mcpServers.map((server) => {
            const enabled = mounted.enabledMcpServers[server.name] === server.fingerprint;
            return <li key={server.name} className="flex items-center justify-between px-3 py-3"><div><p className="text-sm font-medium">{server.name}</p><p className="text-xs text-muted-foreground">{server.transport}</p></div><Switch checked={enabled} disabled={!canBind || !mounted.online || server.availability !== "available" || setServer.isPending} onCheckedChange={(checked) => void toggleServer(server.name, server.fingerprint, checked)} /></li>;
          })}</ul>
        )}
	  </section>}
      {!canBind && <Button variant="outline" disabled>{t(($) => $.tab_body.runner.read_only)}</Button>}
    </div>
  );
}
