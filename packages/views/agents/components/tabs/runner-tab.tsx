"use client";

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  AlertTriangle,
  Laptop,
  Loader2,
  Plus,
  Power,
  RefreshCw,
  Trash2,
  Wifi,
  WifiOff,
} from "lucide-react";
import { toast } from "sonner";
import type { Agent } from "@multica/core/types";
import { useWorkspaceId } from "@multica/core/hooks";
import {
  agentRunnerBindingsOptions,
  useCreateAgentRunnerReconnectCommand,
  useCreateAgentRunnerPairing,
  useDisconnectAgentRunnerBinding,
  useRevokeAgentRunnerBinding,
  type CreateRunnerPairingResponse,
  type CreateRunnerReconnectCommandResponse,
  type RunnerMachineBinding,
} from "@multica/core/runner";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@multica/ui/components/ui/alert-dialog";
import { Badge } from "@multica/ui/components/ui/badge";
import { Button } from "@multica/ui/components/ui/button";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../../../i18n";
import { RunnerCommandDialog } from "../../../runner/runner-command-dialog";

export function RunnerTab({
  agent,
  canBind,
}: {
  agent: Agent;
  canBind: boolean;
}) {
  const { t } = useT("agents");
  const workspaceId = useWorkspaceId();
  const bindings = useQuery(
    agentRunnerBindingsOptions(workspaceId, agent.id),
  );
  const createPairing = useCreateAgentRunnerPairing(agent.id);
  const createReconnectCommand = useCreateAgentRunnerReconnectCommand(agent.id);
  const disconnectBinding = useDisconnectAgentRunnerBinding(
    workspaceId,
    agent.id,
  );
  const revokeBinding = useRevokeAgentRunnerBinding(workspaceId, agent.id);
  const [pairing, setPairing] = useState<CreateRunnerPairingResponse | null>(
    null,
  );
  const [revokeTarget, setRevokeTarget] =
    useState<RunnerMachineBinding | null>(null);
  const [disconnectTarget, setDisconnectTarget] =
    useState<RunnerMachineBinding | null>(null);
  const [reconnectCommand, setReconnectCommand] =
    useState<CreateRunnerReconnectCommandResponse | null>(null);

  const handleCreatePairing = async () => {
    try {
      const created = await createPairing.mutateAsync();
      if (!created.installCommand) {
        throw new Error(t(($) => $.tab_body.runner.create_failed_toast));
      }
      setPairing(created);
    } catch (error) {
      toast.error(
        error instanceof Error
          ? error.message
          : t(($) => $.tab_body.runner.create_failed_toast),
      );
    }
  };

  const handleRevoke = async () => {
    if (!revokeTarget) return;
    try {
      await revokeBinding.mutateAsync(revokeTarget.bindingId);
      toast.success(t(($) => $.tab_body.runner.revoked_toast));
      setRevokeTarget(null);
    } catch (error) {
      toast.error(
        error instanceof Error
          ? error.message
          : t(($) => $.tab_body.runner.revoke_failed_toast),
      );
    }
  };

  const handleDisconnect = async () => {
    if (!disconnectTarget) return;
    try {
      await disconnectBinding.mutateAsync(disconnectTarget.bindingId);
      toast.success(t(($) => $.tab_body.runner.disconnected_toast));
      setDisconnectTarget(null);
    } catch (error) {
      toast.error(
        error instanceof Error
          ? error.message
          : t(($) => $.tab_body.runner.disconnect_failed_toast),
      );
    }
  };

  const handleCreateReconnectCommand = async (
    machine: RunnerMachineBinding,
  ) => {
    try {
      const created = await createReconnectCommand.mutateAsync(
        machine.bindingId,
      );
      setReconnectCommand(created);
    } catch (error) {
      toast.error(
        error instanceof Error
          ? error.message
          : t(($) => $.tab_body.runner.reconnect_failed_toast),
      );
    }
  };

  const machines = bindings.data?.machines ?? [];

  return (
    <div className="space-y-5">
      <div
        role="alert"
        className="flex items-start gap-2 rounded-md border border-amber-500/40 bg-amber-500/10 px-3 py-2.5 text-xs text-amber-700 dark:text-amber-400"
      >
        <AlertTriangle className="mt-0.5 h-3.5 w-3.5 shrink-0" aria-hidden />
        <span>{t(($) => $.tab_body.runner.security_warning)}</span>
      </div>

      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="space-y-1">
          <p className="text-sm font-medium">
            {t(($) => $.tab_body.runner.machines_title)}
          </p>
          <p className="max-w-xl text-xs text-muted-foreground">
            {t(($) => $.tab_body.runner.intro)}
          </p>
        </div>
        {canBind && (
          <Button
            size="sm"
            onClick={() => void handleCreatePairing()}
            disabled={createPairing.isPending}
          >
            {createPairing.isPending ? (
              <Loader2 className="h-3.5 w-3.5 animate-spin" aria-hidden />
            ) : (
              <Plus className="h-3.5 w-3.5" aria-hidden />
            )}
            {t(($) => $.tab_body.runner.add_machine)}
          </Button>
        )}
      </div>

      {bindings.isLoading ? (
        <div className="flex items-center gap-2 py-8 text-sm text-muted-foreground">
          <Loader2 className="h-4 w-4 animate-spin" aria-hidden />
          {t(($) => $.tab_body.runner.loading)}
        </div>
      ) : bindings.isError ? (
        <div className="space-y-3 rounded-lg border border-destructive/40 p-4">
          <p className="text-sm text-destructive">
            {t(($) => $.tab_body.runner.load_failed)}
          </p>
          <Button
            size="sm"
            variant="outline"
            onClick={() => void bindings.refetch()}
          >
            {t(($) => $.tab_body.runner.retry)}
          </Button>
        </div>
      ) : machines.length === 0 ? (
        <div className="rounded-lg border border-dashed p-8 text-center">
          <Laptop
            className="mx-auto mb-3 h-7 w-7 text-muted-foreground"
            aria-hidden
          />
          <p className="text-sm font-medium">
            {t(($) => $.tab_body.runner.empty_title)}
          </p>
          <p className="mt-1 text-xs text-muted-foreground">
            {canBind
              ? t(($) => $.tab_body.runner.empty_owner_hint)
              : t(($) => $.tab_body.runner.empty_admin_hint)}
          </p>
        </div>
      ) : (
        <ul className="divide-y rounded-lg border">
          {machines.map((machine) => (
            <li key={machine.bindingId} className="space-y-3 p-4">
              <div className="flex items-start justify-between gap-3">
                <div className="min-w-0">
                  <div className="flex flex-wrap items-center gap-2">
                    <Laptop className="h-4 w-4 text-muted-foreground" aria-hidden />
                    <span className="truncate text-sm font-medium">
                      {machine.name}
                    </span>
                    <Badge
                      variant="outline"
                      className={cn(
                        "gap-1 text-[10px]",
                        machine.online
                          ? "border-emerald-500/40 text-emerald-700 dark:text-emerald-400"
                          : "text-muted-foreground",
                      )}
                    >
                      {machine.online ? (
                        <Wifi className="h-2.5 w-2.5" aria-hidden />
                      ) : (
                        <WifiOff className="h-2.5 w-2.5" aria-hidden />
                      )}
                      {machine.disconnected
                        ? t(($) => $.tab_body.runner.disconnected)
                        : machine.online
                          ? t(($) => $.tab_body.runner.online)
                          : t(($) => $.tab_body.runner.offline)}
                    </Badge>
                    <Badge variant="secondary" className="text-[10px]">
                      {machine.os}/{machine.arch}
                    </Badge>
                  </div>
                  <p className="mt-1 font-mono text-[10px] text-muted-foreground">
                    {machine.machineId}
                  </p>
                </div>
                <div className="flex shrink-0 items-center gap-1">
                  {machine.disconnected ? (
                    <Button
                      size="icon-sm"
                      variant="ghost"
                      aria-label={t(($) => $.tab_body.runner.reconnect_aria, {
                        name: machine.name,
                      })}
                      disabled={createReconnectCommand.isPending}
                      onClick={() =>
                        void handleCreateReconnectCommand(machine)
                      }
                    >
                      {createReconnectCommand.isPending &&
                      createReconnectCommand.variables === machine.bindingId ? (
                        <Loader2
                          className="h-3.5 w-3.5 animate-spin"
                          aria-hidden
                        />
                      ) : (
                        <RefreshCw className="h-3.5 w-3.5 text-muted-foreground" />
                      )}
                    </Button>
                  ) : (
                    <Button
                      size="icon-sm"
                      variant="ghost"
                      aria-label={t(($) => $.tab_body.runner.disconnect_aria, {
                        name: machine.name,
                      })}
                      onClick={() => setDisconnectTarget(machine)}
                    >
                      <Power className="h-3.5 w-3.5 text-muted-foreground" />
                    </Button>
                  )}
                  <Button
                    size="icon-sm"
                    variant="ghost"
                    aria-label={t(($) => $.tab_body.runner.revoke_aria, {
                      name: machine.name,
                    })}
                    onClick={() => setRevokeTarget(machine)}
                  >
                    <Trash2 className="h-3.5 w-3.5 text-muted-foreground" />
                  </Button>
                </div>
              </div>
              <div>
                <p className="mb-1 text-[11px] font-medium text-muted-foreground">
                  {t(($) => $.tab_body.runner.file_roots)}
                </p>
                <div className="space-y-1">
                  {machine.roots.map((root) => (
                    <code
                      key={root}
                      className="block break-all rounded bg-muted px-2 py-1 font-mono text-[11px]"
                    >
                      {root}
                    </code>
                  ))}
                </div>
              </div>
            </li>
          ))}
        </ul>
      )}

      <RunnerCommandDialog
        command={pairing?.installCommand ?? null}
        title={t(($) => $.tab_body.runner.command_title)}
        description={t(($) => $.tab_body.runner.command_description)}
        expiry={t(($) => $.tab_body.runner.command_expiry)}
        copiedToast={t(($) => $.tab_body.runner.copied_toast)}
        copyAria={t(($) => $.tab_body.runner.copy_aria)}
        closeLabel={t(($) => $.tab_body.runner.close)}
        onClose={() => {
          setPairing(null);
          createPairing.reset();
        }}
      />

      <RunnerCommandDialog
        command={reconnectCommand?.reconnectCommand ?? null}
        title={t(($) => $.tab_body.runner.reconnect_command_title)}
        description={t(($) => $.tab_body.runner.reconnect_command_description)}
        expiry={t(($) => $.tab_body.runner.reconnect_command_expiry)}
        copiedToast={t(($) => $.tab_body.runner.reconnect_copied_toast)}
        copyAria={t(($) => $.tab_body.runner.copy_aria)}
        closeLabel={t(($) => $.tab_body.runner.close)}
        onClose={() => {
          setReconnectCommand(null);
          createReconnectCommand.reset();
        }}
      />

      <AlertDialog
        open={disconnectTarget !== null}
        onOpenChange={(open) => {
          if (!open && !disconnectBinding.isPending) setDisconnectTarget(null);
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t(($) => $.tab_body.runner.disconnect_title, {
                name: disconnectTarget?.name ?? "",
              })}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t(($) => $.tab_body.runner.disconnect_description)}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={disconnectBinding.isPending}>
              {t(($) => $.tab_body.runner.cancel)}
            </AlertDialogCancel>
            <AlertDialogAction
              onClick={(event) => {
                event.preventDefault();
                void handleDisconnect();
              }}
              disabled={disconnectBinding.isPending}
            >
              {disconnectBinding.isPending && (
                <Loader2 className="h-3.5 w-3.5 animate-spin" aria-hidden />
              )}
              {t(($) => $.tab_body.runner.disconnect_confirm)}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      <AlertDialog
        open={revokeTarget !== null}
        onOpenChange={(open) => {
          if (!open && !revokeBinding.isPending) setRevokeTarget(null);
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t(($) => $.tab_body.runner.revoke_title, {
                name: revokeTarget?.name ?? "",
              })}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t(($) => $.tab_body.runner.revoke_description)}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={revokeBinding.isPending}>
              {t(($) => $.tab_body.runner.cancel)}
            </AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              onClick={(event) => {
                event.preventDefault();
                void handleRevoke();
              }}
              disabled={revokeBinding.isPending}
            >
              {revokeBinding.isPending && (
                <Loader2 className="h-3.5 w-3.5 animate-spin" aria-hidden />
              )}
              {t(($) => $.tab_body.runner.revoke_confirm)}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}
