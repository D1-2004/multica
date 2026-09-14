"use client";

import { useQuery } from "@tanstack/react-query";
import { dshPluginListOptions } from "@multica/core/dsh-plugins";
import type { AgentPackageRequirements } from "@multica/core/types";
import { Input } from "@multica/ui/components/ui/input";
import { Label } from "@multica/ui/components/ui/label";
import { useT } from "../../i18n";

export function PackageRequirementsForm({ workspaceId, pluginBindings, onPluginBindingsChange, requirements, secrets, onSecretsChange, deferBindings, onDeferChange, disabled }: {
  workspaceId: string;
  pluginBindings: Record<string,string>;
  onPluginBindingsChange: (values: Record<string,string>) => void;
  requirements?: AgentPackageRequirements;
  secrets: Record<string, string>;
  onSecretsChange: (values: Record<string, string>) => void;
  deferBindings: boolean;
  onDeferChange: (value: boolean) => void;
  disabled: boolean;
}) {
  const { t } = useT("agents");
  const plugins = requirements?.dshPlugins ?? [];
  const available = useQuery({ ...dshPluginListOptions(workspaceId), enabled: !!workspaceId && plugins.length > 0 });
  return <div className="space-y-4">
              {plugins.map((plugin) => {
                const matches = (available.data ?? []).filter((row) => !plugin.packageName || (row.packageName === plugin.packageName && row.resolvedVersion === plugin.version && row.integrity === plugin.integrity));
                return <div className="space-y-2" key={plugin.ref}>
                  <Label htmlFor={`package-plugin-${plugin.ref}`}>{t(($) => $.creation_studio.local.plugin_binding, { name: plugin.packageName || plugin.ref, version: plugin.version })}</Label>
                  <select id={`package-plugin-${plugin.ref}`} className="w-full rounded-md border bg-background px-3 py-2 text-body" value={pluginBindings[plugin.ref] ?? ""} disabled={disabled || available.isPending} onChange={(event) => onPluginBindingsChange({ ...pluginBindings, [plugin.ref]: event.target.value })}>
                    <option value="">{t(($) => $.creation_studio.local.plugin_select)}</option>
                    {matches.map((row) => <option key={row.id} value={row.id}>{row.packageName} @ {row.resolvedVersion}</option>)}
                  </select>
                  {available.isError ? <p role="alert" className="text-caption">{t(($) => $.creation_studio.local.plugin_load_failed)}</p> : !available.isPending && matches.length === 0 && <p className="text-caption text-muted-foreground">{t(($) => $.creation_studio.local.plugin_missing)}</p>}
                </div>;
              })}
              {requirements?.secrets.map((ref) => <div className="space-y-2" key={ref}>
                <Label htmlFor={`package-secret-${ref}`}>{t(($) => $.creation_studio.local.secret, { ref })}</Label>
                <Input id={`package-secret-${ref}`} type="password" autoComplete="new-password" value={secrets[ref] ?? ""} disabled={disabled} onChange={(event) => onSecretsChange({ ...secrets, [ref]: event.target.value })} />
              </div>)}
              {!!requirements?.deferred_bindings.length && <div className="space-y-2 rounded border p-3">
                <p className="text-body">{t(($) => $.creation_studio.local.bindings_required)}</p>
                <ul className="list-inside list-disc text-caption">{requirements.deferred_bindings.map((field) => <li key={field}>{field}</li>)}</ul>
                <label className="flex items-start gap-2 text-body"><input type="checkbox" checked={deferBindings} disabled={disabled} onChange={(event) => onDeferChange(event.target.checked)} />{t(($) => $.creation_studio.local.defer_bindings)}</label>
              </div>}
  </div>;
}
