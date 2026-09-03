"use client";

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  AlertTriangle,
  ArrowUpRight,
  Bot,
  Laptop,
  Loader2,
	Plus,
	Pencil,
  Power,
  RefreshCw,
  Trash2,
  Wifi,
  WifiOff,
} from "lucide-react";
import { toast } from "sonner";
import { useAuthStore } from "@multica/core/auth";
import { paths } from "@multica/core/paths";
import {
  accountRunnerBindingsOptions,
	useCreateAccountRunnerPairing,
	useRenameAccountRunnerMachine,
	useRevokeAccountRunnerMachine,
  useCreateAccountRunnerReconnectCommand,
  useDisconnectAccountRunnerBinding,
  useRevokeAccountRunnerBinding,
  type AccountRunnerBinding,
  type AccountRunnerBindingTarget,
  type AccountRunnerMachine,
  type CreateRunnerReconnectCommandResponse,
	type CreateRunnerPairingResponse,
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
import { AppLink } from "../../navigation";
import { RunnerCommandDialog } from "../../runner/runner-command-dialog";
import { useT, useTimeAgo } from "../../i18n";
import { SettingsSection, SettingsTab } from "./settings-layout";

interface BindingSelection {
  machine: AccountRunnerMachine;
  binding: AccountRunnerBinding;
}

function mutationTarget(binding: AccountRunnerBinding): AccountRunnerBindingTarget {
  return {
    bindingId: binding.bindingId,
    workspaceId: binding.workspaceId,
    agentId: binding.agentId,
  };
}

export function LocalRunnerTab() {
  const { t } = useT("settings");
  const userId = useAuthStore((state) => state.user?.id ?? "");
  const bindingsQuery = useQuery(accountRunnerBindingsOptions(userId));
	const createPairing = useCreateAccountRunnerPairing();
	const renameMachine = useRenameAccountRunnerMachine(userId);
	const revokeMachine = useRevokeAccountRunnerMachine(userId);
  const disconnectBinding = useDisconnectAccountRunnerBinding(userId);
  const createReconnectCommand =
    useCreateAccountRunnerReconnectCommand(userId);
  const revokeBinding = useRevokeAccountRunnerBinding(userId);
  const [disconnectTarget, setDisconnectTarget] =
    useState<BindingSelection | null>(null);
  const [revokeTarget, setRevokeTarget] = useState<BindingSelection | null>(
    null,
  );
  const [reconnectCommand, setReconnectCommand] =
    useState<CreateRunnerReconnectCommandResponse | null>(null);
	const [pairing, setPairing] = useState<CreateRunnerPairingResponse | null>(null);
	const [revokeMachineTarget, setRevokeMachineTarget] = useState<AccountRunnerMachine | null>(null);

	const handleCreatePairing = async () => {
		try {
			setPairing(await createPairing.mutateAsync());
		} catch (error) {
			toast.error(error instanceof Error ? error.message : t(($) => $.local_runner.load_failed));
		}
	};

	const handleRenameMachine = async (machine: AccountRunnerMachine) => {
		const name = window.prompt(t(($) => $.local_runner.rename_prompt), machine.name)?.trim();
		if (!name || name === machine.name) return;
		try { await renameMachine.mutateAsync({ machineId: machine.machineId, name }); }
		catch (error) { toast.error(error instanceof Error ? error.message : t(($) => $.local_runner.rename_failed)); }
	};

	const handleRevokeMachine = async () => {
		if (!revokeMachineTarget) return;
		try { await revokeMachine.mutateAsync(revokeMachineTarget.machineId); setRevokeMachineTarget(null); }
		catch (error) { toast.error(error instanceof Error ? error.message : t(($) => $.local_runner.machine_revoke_failed)); }
	};

  const handleDisconnect = async () => {
    if (!disconnectTarget) return;
    try {
      await disconnectBinding.mutateAsync(
        mutationTarget(disconnectTarget.binding),
      );
      toast.success(t(($) => $.local_runner.disconnected_toast));
      setDisconnectTarget(null);
    } catch (error) {
      toast.error(
        error instanceof Error
          ? error.message
          : t(($) => $.local_runner.disconnect_failed_toast),
      );
    }
  };

  const handleReconnect = async (selection: BindingSelection) => {
    try {
      const created = await createReconnectCommand.mutateAsync(
        mutationTarget(selection.binding),
      );
      if (!created?.reconnectCommand) {
        throw new Error(t(($) => $.local_runner.reconnect_failed_toast));
      }
      setReconnectCommand(created);
    } catch (error) {
      toast.error(
        error instanceof Error
          ? error.message
          : t(($) => $.local_runner.reconnect_failed_toast),
      );
    }
  };

  const handleRevoke = async () => {
    if (!revokeTarget) return;
    try {
      await revokeBinding.mutateAsync(mutationTarget(revokeTarget.binding));
      toast.success(t(($) => $.local_runner.revoked_toast));
      setRevokeTarget(null);
    } catch (error) {
      toast.error(
        error instanceof Error
          ? error.message
          : t(($) => $.local_runner.revoke_failed_toast),
      );
    }
  };

  const loadFailed = bindingsQuery.isError || bindingsQuery.data === null;
  const machines = bindingsQuery.data?.machines ?? [];

  return (
    <SettingsTab
      title={t(($) => $.local_runner.title)}
      description={t(($) => $.local_runner.description)}
    >
      <div
        role="alert"
        className="flex items-start gap-2 rounded-lg border border-warning/40 bg-warning/10 px-3.5 py-3 text-caption leading-5 text-warning"
      >
        <AlertTriangle className="mt-0.5 size-4 shrink-0" aria-hidden />
        <span>{t(($) => $.local_runner.security_warning)}</span>
      </div>

      <SettingsSection
        title={t(($) => $.local_runner.machines_title)}
        description={t(($) => $.local_runner.machines_description)}
      >
		<div className="flex justify-end">
			<Button type="button" size="sm" onClick={() => void handleCreatePairing()} disabled={createPairing.isPending}>
				{createPairing.isPending ? <Loader2 className="size-4 animate-spin" /> : <Plus className="size-4" />}
				{t(($) => $.local_runner.add_machine)}
			</Button>
		</div>
        {bindingsQuery.isLoading || !userId ? (
          <div className="flex items-center gap-2 rounded-lg border border-surface-border px-4 py-8 text-body text-muted-foreground">
            <Loader2 className="size-4 animate-spin" aria-hidden />
            {t(($) => $.local_runner.loading)}
          </div>
        ) : loadFailed ? (
          <div className="space-y-3 rounded-lg border border-destructive/40 p-4">
            <p className="text-body text-destructive">
              {t(($) => $.local_runner.load_failed)}
            </p>
            <Button
              type="button"
              size="sm"
              variant="outline"
              onClick={() => void bindingsQuery.refetch()}
            >
              {t(($) => $.local_runner.retry)}
            </Button>
          </div>
        ) : machines.length === 0 ? (
          <div className="rounded-lg border border-dashed border-surface-border p-8 text-center">
            <Laptop
              className="mx-auto mb-3 size-7 text-muted-foreground"
              aria-hidden
            />
            <p className="text-body font-medium">
              {t(($) => $.local_runner.empty_title)}
            </p>
            <p className="mt-1 text-caption text-muted-foreground">
              {t(($) => $.local_runner.empty_description)}
            </p>
          </div>
        ) : (
          <ul className="space-y-4">
            {machines.map((machine) => (
              <MachineCard
                key={machine.machineId}
                machine={machine}
                reconnectPendingId={
                  createReconnectCommand.isPending
                    ? createReconnectCommand.variables?.bindingId
                    : undefined
                }
                onDisconnect={(binding) =>
                  setDisconnectTarget({ machine, binding })
                }
                onReconnect={(binding) =>
                  void handleReconnect({ machine, binding })
                }
                onRevoke={(binding) => setRevokeTarget({ machine, binding })}
				onRenameMachine={() => void handleRenameMachine(machine)}
				onRevokeMachine={() => setRevokeMachineTarget(machine)}
              />
            ))}
          </ul>
        )}
      </SettingsSection>

      <RunnerCommandDialog
		command={pairing?.installCommand ?? null}
		title={t(($) => $.local_runner.install_command_title)}
		description={t(($) => $.local_runner.install_command_description)}
		expiry={t(($) => $.local_runner.reconnect_command_expiry)}
		copiedToast={t(($) => $.local_runner.reconnect_copied_toast)}
		copyAria={t(($) => $.local_runner.copy_aria)}
		closeLabel={t(($) => $.local_runner.close)}
		onClose={() => setPairing(null)}
	  />

	  <RunnerCommandDialog
        command={reconnectCommand?.reconnectCommand ?? null}
        title={t(($) => $.local_runner.reconnect_command_title)}
        description={t(($) => $.local_runner.reconnect_command_description)}
        expiry={t(($) => $.local_runner.reconnect_command_expiry)}
        copiedToast={t(($) => $.local_runner.reconnect_copied_toast)}
        copyAria={t(($) => $.local_runner.copy_aria)}
        closeLabel={t(($) => $.local_runner.close)}
        onClose={() => {
          setReconnectCommand(null);
          createReconnectCommand.reset();
        }}
      />

      <AlertDialog
		open={revokeMachineTarget !== null}
		onOpenChange={(open) => { if (!open && !revokeMachine.isPending) setRevokeMachineTarget(null); }}
	  >
		<AlertDialogContent><AlertDialogHeader><AlertDialogTitle>{t(($) => $.local_runner.machine_revoke_title, { machine: revokeMachineTarget?.name ?? "" })}</AlertDialogTitle><AlertDialogDescription>{t(($) => $.local_runner.machine_revoke_description)}</AlertDialogDescription></AlertDialogHeader><AlertDialogFooter><AlertDialogCancel disabled={revokeMachine.isPending}>{t(($) => $.local_runner.cancel)}</AlertDialogCancel><AlertDialogAction variant="destructive" disabled={revokeMachine.isPending} onClick={(event) => { event.preventDefault(); void handleRevokeMachine(); }}>{t(($) => $.local_runner.machine_revoke_confirm)}</AlertDialogAction></AlertDialogFooter></AlertDialogContent>
	  </AlertDialog>

	  <AlertDialog
        open={disconnectTarget !== null}
        onOpenChange={(open) => {
          if (!open && !disconnectBinding.isPending) setDisconnectTarget(null);
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t(($) => $.local_runner.disconnect_title, {
                agent: disconnectTarget?.binding.agentName ?? "",
                machine: disconnectTarget?.machine.name ?? "",
              })}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t(($) => $.local_runner.disconnect_description)}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={disconnectBinding.isPending}>
              {t(($) => $.local_runner.cancel)}
            </AlertDialogCancel>
            <AlertDialogAction
              onClick={(event) => {
                event.preventDefault();
                void handleDisconnect();
              }}
              disabled={disconnectBinding.isPending}
            >
              {disconnectBinding.isPending && (
                <Loader2 className="size-3.5 animate-spin" aria-hidden />
              )}
              {t(($) => $.local_runner.disconnect_confirm)}
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
              {t(($) => $.local_runner.revoke_title, {
                agent: revokeTarget?.binding.agentName ?? "",
                machine: revokeTarget?.machine.name ?? "",
              })}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t(($) => $.local_runner.revoke_description)}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={revokeBinding.isPending}>
              {t(($) => $.local_runner.cancel)}
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
                <Loader2 className="size-3.5 animate-spin" aria-hidden />
              )}
              {t(($) => $.local_runner.revoke_confirm)}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </SettingsTab>
  );
}

function MachineCard({
  machine,
  reconnectPendingId,
  onDisconnect,
  onReconnect,
  onRevoke,
	onRenameMachine,
	onRevokeMachine,
}: {
  machine: AccountRunnerMachine;
  reconnectPendingId?: string;
  onDisconnect: (binding: AccountRunnerBinding) => void;
  onReconnect: (binding: AccountRunnerBinding) => void;
  onRevoke: (binding: AccountRunnerBinding) => void;
	onRenameMachine: () => void;
	onRevokeMachine: () => void;
}) {
  const { t } = useT("settings");
  const timeAgo = useTimeAgo();
  const lastSeen =
    machine.lastSeenAt && Number.isFinite(Date.parse(machine.lastSeenAt))
      ? timeAgo(machine.lastSeenAt)
      : null;

  return (
    <li className="overflow-hidden rounded-lg border border-surface-border">
      <div className="flex flex-wrap items-start justify-between gap-3 bg-muted/20 px-4 py-3.5">
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-2">
            <Laptop className="size-4 text-muted-foreground" aria-hidden />
            <h3 className="truncate text-body font-semibold">{machine.name}</h3>
            <Badge
              variant="outline"
              className={cn(
                "gap-1 text-micro",
                machine.online
                  ? "border-success/40 text-success"
                  : "text-muted-foreground",
              )}
            >
              {machine.online ? (
                <Wifi className="size-2.5" aria-hidden />
              ) : (
                <WifiOff className="size-2.5" aria-hidden />
              )}
              {machine.online
                ? t(($) => $.local_runner.online)
                : t(($) => $.local_runner.offline)}
            </Badge>
            <Badge variant="secondary" className="text-micro">
              {machine.os}/{machine.arch}
            </Badge>
          </div>
          <p className="mt-1 break-all font-mono text-micro text-muted-foreground">
            {machine.machineId}
          </p>
          <div className="mt-1 flex flex-wrap gap-x-3 gap-y-1 text-micro text-muted-foreground">
            {machine.clientVersion ? (
              <span>
                {t(($) => $.local_runner.version, {
                  version: machine.clientVersion,
                })}
              </span>
            ) : null}
            {lastSeen ? (
              <span>
                {t(($) => $.local_runner.last_seen, {
                  time: lastSeen,
                })}
              </span>
            ) : null}
          </div>
        </div>
		<div className="flex items-center gap-1"><Badge variant="outline" className="text-micro">{t(($) => $.local_runner.binding_count, { count: machine.bindings.length })}</Badge><Button type="button" size="icon-sm" variant="ghost" onClick={onRenameMachine} aria-label={t(($) => $.local_runner.rename_machine)}><Pencil className="size-3.5" /></Button><Button type="button" size="icon-sm" variant="ghost" onClick={onRevokeMachine} aria-label={t(($) => $.local_runner.revoke_machine)}><Trash2 className="size-3.5" /></Button></div>
      </div>
	  {machine.mcpServers.length > 0 && <div className="flex flex-wrap gap-1 border-t px-4 py-2">{machine.mcpServers.map((server) => <Badge key={server.name} variant="secondary" className="text-micro">{server.name} · {server.transport}</Badge>)}</div>}

      <ul className="divide-y divide-surface-border">
        {machine.bindings.map((binding) => {
          const agentHref = `${paths
            .workspace(binding.workspaceSlug)
            .agentDetail(binding.agentId)}?view=runner`;
          const reconnectPending = reconnectPendingId === binding.bindingId;

          return (
            <li key={binding.bindingId} className="space-y-3 px-4 py-3.5">
              <div className="flex items-start justify-between gap-3">
                <div className="min-w-0">
                  <AppLink
                    href={agentHref}
                    newTabTitle={binding.agentName}
                    className="inline-flex min-w-0 items-center gap-1.5 text-body font-medium hover:underline"
                  >
                    <Bot
                      className="size-4 shrink-0 text-muted-foreground"
                      aria-hidden
                    />
                    <span className="truncate">{binding.agentName}</span>
                    <ArrowUpRight
                      className="size-3.5 shrink-0 text-muted-foreground"
                      aria-hidden
                    />
                  </AppLink>
                  <p className="mt-0.5 text-caption text-muted-foreground">
                    {binding.workspaceName}
                  </p>
                </div>
                <div className="flex shrink-0 items-center gap-1">
                  <Badge
                    variant="outline"
                    className={cn(
                      "mr-1 text-micro",
                      !binding.disconnected && machine.online
                        ? "border-success/40 text-success"
                        : "text-muted-foreground",
                    )}
                  >
                    {binding.disconnected
                      ? t(($) => $.local_runner.disconnected)
                      : machine.online
                        ? t(($) => $.local_runner.online)
                        : t(($) => $.local_runner.offline)}
                  </Badge>
                  {binding.disconnected ? (
                    <Button
                      type="button"
                      size="icon-sm"
                      variant="ghost"
                      aria-label={t(($) => $.local_runner.reconnect_aria, {
                        agent: binding.agentName,
                        machine: machine.name,
                      })}
                      disabled={reconnectPendingId !== undefined}
                      onClick={() => onReconnect(binding)}
                    >
                      {reconnectPending ? (
                        <Loader2 className="size-3.5 animate-spin" aria-hidden />
                      ) : (
                        <RefreshCw className="size-3.5 text-muted-foreground" />
                      )}
                    </Button>
                  ) : (
                    <Button
                      type="button"
                      size="icon-sm"
                      variant="ghost"
                      aria-label={t(($) => $.local_runner.disconnect_aria, {
                        agent: binding.agentName,
                        machine: machine.name,
                      })}
                      onClick={() => onDisconnect(binding)}
                    >
                      <Power className="size-3.5 text-muted-foreground" />
                    </Button>
                  )}
                  <Button
                    type="button"
                    size="icon-sm"
                    variant="ghost"
                    aria-label={t(($) => $.local_runner.revoke_aria, {
                      agent: binding.agentName,
                      machine: machine.name,
                    })}
                    onClick={() => onRevoke(binding)}
                  >
                    <Trash2 className="size-3.5 text-muted-foreground" />
                  </Button>
                </div>
              </div>

              <div>
                <p className="mb-1 text-micro font-medium text-muted-foreground">
                  {t(($) => $.local_runner.file_roots)}
                </p>
                <div className="space-y-1">
                  {binding.roots.map((root) => (
                    <code
                      key={root}
                      className="block break-all rounded bg-muted px-2 py-1 font-mono text-micro"
                    >
                      {root}
                    </code>
                  ))}
                </div>
              </div>
            </li>
          );
        })}
      </ul>
    </li>
  );
}
