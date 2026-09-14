"use client";

import { DshPluginConfigDialog } from "./dsh-plugin-config-dialog";
import { useMemo, useState } from "react";
import { Blocks, Loader2, Plus, Trash2, TriangleAlert } from "lucide-react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import type { Agent, AgentRuntime } from "@multica/core/types";
import { api, ApiError } from "@multica/core/api";
import { useWorkspaceId } from "@multica/core/hooks";
import {
  agentDshPluginsOptions,
  dshPluginKeys,
  dshPluginListOptions,
  type AgentDshPlugin,
} from "@multica/core/dsh-plugins";
import { Badge } from "@multica/ui/components/ui/badge";
import { Button } from "@multica/ui/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import { Switch } from "@multica/ui/components/ui/switch";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../../../i18n";

/**
 * Which DSH plugins this agent boots with.
 *
 * The set is composed into the profile's bundle list at task start, so this
 * is the same lever `dsh plugin add` pulls — attaching a plugin here is what
 * puts its package in `dsh.profile.bundles` for that agent's sandbox.
 *
 * Only meaningful on a DeepSeek Harness runtime: every other provider ignores
 * the composed set entirely, which is why the tab is not offered for them.
 */
export function DshPluginsTab({
  agent,
  runtime,
  canEdit = true,
}: {
  agent: Agent;
  runtime: AgentRuntime | null;
  canEdit?: boolean;
}) {
  const { t } = useT("agents");
  const wsId = useWorkspaceId();
  const queryClient = useQueryClient();
  const [showAdd, setShowAdd] = useState(false);
  const [configPlugin, setConfigPlugin] = useState<AgentDshPlugin | null>(null);
  const [busyId, setBusyId] = useState<string | null>(null);

  const attached = useQuery(agentDshPluginsOptions(wsId, agent.id));
  const workspacePlugins = useQuery(dshPluginListOptions(wsId));

  const attachedRows = useMemo(() => attached.data ?? [], [attached.data]);
  const attachedIds = useMemo(
    () => new Set(attachedRows.map((row) => row.id)),
    [attachedRows],
  );
  const available = useMemo(
    () => (workspacePlugins.data ?? []).filter((row) => !attachedIds.has(row.id)),
    [workspacePlugins.data, attachedIds],
  );

  const invalidate = () => {
    void queryClient.invalidateQueries({ queryKey: dshPluginKeys.all(wsId) });
  };

  const save = useMutation({
    mutationFn: (plugins: { id: string; enabled?: boolean }[]) =>
      api.setAgentDshPlugins(agent.id, plugins),
    onSuccess: invalidate,
    onError: (error: unknown) => {
      toast.error(
        error instanceof ApiError && error.message
          ? error.message
          : t(($) => $.tab_body.dsh_plugins.save_failed_toast),
      );
    },
    onSettled: () => setBusyId(null),
  });

  // The endpoint replaces the whole set, so every mutation sends the full list
  // rather than a delta. That also makes an interrupted request harmless: it
  // either applies the new set or leaves the old one.
  const writeSet = (rows: { id: string; enabled: boolean }[]) =>
    save.mutate(rows.map((row) => ({ id: row.id, enabled: row.enabled })));

  const currentSet = () =>
    attachedRows.map((row) => ({ id: row.id, enabled: row.enabled }));

  const handleToggle = (pluginId: string, enabled: boolean) => {
    setBusyId(pluginId);
    writeSet(currentSet().map((row) => (row.id === pluginId ? { ...row, enabled } : row)));
  };

  const handleRemove = (pluginId: string) => {
    setBusyId(pluginId);
    writeSet(currentSet().filter((row) => row.id !== pluginId));
  };

  const handleAttach = (pluginId: string) => {
    setBusyId(pluginId);
    writeSet([...currentSet(), { id: pluginId, enabled: true }]);
    setShowAdd(false);
  };

  // Plugins are composed by the sandbox image's adapter. A local daemon runs
  // server/pkg/agent/dsh.go, which has no plugin awareness at all, so a binding
  // made here is stored, listed as enabled, and never loaded.
  //
  // Said rather than hidden. Hiding the tab would strand whatever is already
  // bound — invisible, unremovable, and live again the moment the agent moves
  // back to a cloud runtime. The controls stay usable so a binding made before
  // the move can be taken off.
  const runsPlugins = agent.runtime_mode === "cloud";

  return (
    <div className="space-y-8">
      <p className="text-body leading-6 text-muted-foreground">
        {t(($) => $.tab_body.dsh_plugins.intro)}
      </p>

      {runsPlugins ? null : (
        <div
          role="status"
          className="flex items-start gap-2 rounded-lg border border-dashed px-4 py-3 text-caption leading-5 text-muted-foreground"
        >
          <TriangleAlert className="mt-0.5 h-4 w-4 shrink-0" />
          <span>{t(($) => $.tab_body.dsh_plugins.local_runtime_notice)}</span>
        </div>
      )}

      <section className="space-y-3">
        <div className="flex items-start justify-between gap-4">
          <div>
            <h3 className="text-body font-medium">
              {t(($) => $.tab_body.dsh_plugins.attached_title)}
            </h3>
            <p className="mt-1 text-caption leading-5 text-muted-foreground">
              {t(($) => $.tab_body.dsh_plugins.attached_hint, {
                runtime: runtime?.name ?? "",
              })}
            </p>
          </div>
          {canEdit ? (
            <Button
              variant="outline"
              size="sm"
              onClick={() => setShowAdd(true)}
              disabled={available.length === 0}
            >
              <Plus className="h-3.5 w-3.5" />
              {t(($) => $.tab_body.dsh_plugins.add_action)}
            </Button>
          ) : null}
        </div>

        {attached.isPending ? (
          <div className="flex items-center gap-2 rounded-lg border border-dashed px-4 py-6 text-caption text-muted-foreground">
            <Loader2 className="h-4 w-4 animate-spin motion-reduce:animate-none" />
            {t(($) => $.tab_body.dsh_plugins.loading)}
          </div>
        ) : attachedRows.length === 0 ? (
          <div className="flex flex-col items-center justify-center rounded-lg border border-dashed py-10 text-muted-foreground">
            <span className="opacity-50">
              <Blocks className="h-6 w-6" />
            </span>
            <p className="mt-3 text-body">
              {t(($) => $.tab_body.dsh_plugins.empty_title)}
            </p>
            <p className="mt-1 max-w-sm text-center text-caption">
              {(workspacePlugins.data ?? []).length === 0
                ? t(($) => $.tab_body.dsh_plugins.empty_hint_none_imported)
                : t(($) => $.tab_body.dsh_plugins.empty_hint)}
            </p>
          </div>
        ) : (
          <ul className="divide-y rounded-lg border bg-surface-raised/40">
            {attachedRows.map((plugin) => (
              <AttachedRow
                key={plugin.id}
                plugin={plugin}
                canEdit={canEdit}
                busy={busyId === plugin.id}
                anyBusy={busyId !== null}
                onToggle={(enabled) => handleToggle(plugin.id, enabled)}
                onRemove={() => handleRemove(plugin.id)}
                onConfigure={() => setConfigPlugin(plugin)}
              />
            ))}
          </ul>
        )}
      </section>

      {configPlugin && canEdit && <DshPluginConfigDialog key={`${agent.id}:${configPlugin.id}`} wsId={wsId} agentId={agent.id} plugin={configPlugin} onClose={() => setConfigPlugin(null)} />}

      <Dialog open={showAdd} onOpenChange={setShowAdd}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t(($) => $.tab_body.dsh_plugins.add_title)}</DialogTitle>
            <DialogDescription>
              {t(($) => $.tab_body.dsh_plugins.add_description)}
            </DialogDescription>
          </DialogHeader>
          {available.length === 0 ? (
            <p className="text-caption text-muted-foreground">
              {t(($) => $.tab_body.dsh_plugins.add_none_available)}
            </p>
          ) : (
            <ul className="max-h-80 divide-y overflow-y-auto rounded-lg border">
              {available.map((plugin) => (
                <li key={plugin.id} className="flex items-center gap-3 p-3">
                  <span className="min-w-0 flex-1">
                    <span className="block truncate text-body font-medium">
                      {plugin.packageName}
                    </span>
                    <span className="block truncate text-caption text-muted-foreground">
                      {plugin.description ||
                        t(($) => $.tab_body.dsh_plugins.no_description)}
                    </span>
                  </span>
                  {plugin.resolvedVersion ? (
                    <Badge variant="secondary" className="font-mono tabular-nums">
                      {plugin.resolvedVersion}
                    </Badge>
                  ) : null}
                  <Button
                    size="sm"
                    variant="outline"
                    onClick={() => handleAttach(plugin.id)}
                    disabled={save.isPending}
                  >
                    {t(($) => $.tab_body.dsh_plugins.attach_action)}
                  </Button>
                </li>
              ))}
            </ul>
          )}
        </DialogContent>
      </Dialog>
    </div>
  );
}

function AttachedRow({
  plugin,
  canEdit,
  busy,
  anyBusy,
  onToggle,
  onRemove,
  onConfigure,
}: {
  plugin: AgentDshPlugin;
  canEdit: boolean;
  busy: boolean;
  anyBusy: boolean;
  onToggle: (enabled: boolean) => void;
  onRemove: () => void;
  onConfigure: () => void;
}) {
  const { t } = useT("agents");
  return (
    <li className="flex items-center gap-3 p-3">
      <span
        className={cn(
          "flex size-9 shrink-0 items-center justify-center rounded-md bg-muted text-muted-foreground",
          !plugin.enabled && "text-faint-foreground",
        )}
      >
        <Blocks className="h-4 w-4" />
      </span>
      <span className="min-w-0 flex-1">
        <span
          className={cn(
            "block truncate text-body font-medium",
            !plugin.enabled && "text-muted-foreground",
          )}
        >
          {plugin.packageName}
          {plugin.resolvedVersion ? (
            <Badge variant="secondary" className="ml-2 align-middle font-mono text-micro tabular-nums">
              {plugin.resolvedVersion}
            </Badge>
          ) : null}
        </span>
        <span className="block truncate text-caption text-muted-foreground">
          {plugin.description || t(($) => $.tab_body.dsh_plugins.no_description)}
        </span>
      </span>
      {canEdit && (
        <>
          <Button variant="outline" size="sm" onClick={onConfigure} disabled={anyBusy}>{t(($) => $.tab_body.dsh_plugins.config_action)}</Button>
          {busy ? (
            <Loader2 className="h-4 w-4 animate-spin text-muted-foreground motion-reduce:animate-none" />
          ) : (
            <Switch
              checked={plugin.enabled}
              onCheckedChange={onToggle}
              aria-label={t(($) => $.tab_body.dsh_plugins.toggle_aria, {
                name: plugin.packageName,
              })}
            />
          )}
          <Button
            variant="ghost"
            size="icon-sm"
            onClick={onRemove}
            disabled={anyBusy}
            aria-label={t(($) => $.tab_body.dsh_plugins.remove_aria, {
              name: plugin.packageName,
            })}
            className="text-muted-foreground hover:text-destructive"
          >
            <Trash2 className="h-3.5 w-3.5" />
          </Button>
        </>
      )}
    </li>
  );
}
