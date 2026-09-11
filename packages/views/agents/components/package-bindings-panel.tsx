"use client";

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { agentPackageBindingsOptions, useConfirmAgentPackageBinding } from "@multica/core/agents";
import type { AgentPackageBinding, AgentPackageBindingReport } from "@multica/core/types/agent-package";
import { useWorkspaceId } from "@multica/core/hooks";
import { Button } from "@multica/ui/components/ui/button";
import { useT } from "../../i18n";
import { PackageError } from "../create/package-error";

function references(value: unknown): string[] {
  if (Array.isArray(value)) return [...new Set(value.flatMap(references))];
  if (!value || typeof value !== "object") return [];
  const record = value as Record<string, unknown>;
  const ref = record.ref ?? record.runtime_ref;
  if (typeof ref === "string") return [ref];
  return [...new Set(Object.values(record).flatMap(references))];
}

export function PackageBindingsPanel({ agentId, expanded, onNavigate }: { agentId: string; expanded: boolean; onNavigate: (tab: string) => void }) {
  const { t } = useT("agents");
  const workspaceId = useWorkspaceId();
  const query = useQuery(agentPackageBindingsOptions(workspaceId, agentId));
  const pending = query.data?.bindings.filter((item) => item.status !== "ready").length ?? 0;
  if (query.isError) return <PackageError error={query.error} />;
  if (!query.data?.bindings.length) return null;
  if (!expanded) return pending > 0 ? <div className="mb-4 flex items-center justify-between gap-3 rounded-md border p-3 text-caption">
    <span>{t(($) => $.package_bindings.pending, { count: pending })}</span>
    <Button variant="outline" size="sm" onClick={() => onNavigate("publish")}>{t(($) => $.package_bindings.open)}</Button>
  </div> : null;
  return <div className="mb-6 space-y-3 rounded-md border p-4">
    <div className="flex items-center justify-between gap-2">
      <h3 className="text-title-sm font-medium">{t(($) => $.package_bindings.title)}</h3>
      <Button variant="outline" size="sm" disabled={query.isFetching} onClick={() => { void query.refetch(); }}>{t(($) => $.package_bindings.refresh)}</Button>
    </div>
    <p className="text-caption text-muted-foreground">{t(($) => $.package_bindings.description)}</p>
    {query.data.bindings.map((item) => <BindingRow key={`${item.path}:${query.data.revision}:${item.current_fingerprint}`} item={item} report={query.data} agentId={agentId} workspaceId={workspaceId} onNavigate={onNavigate} />)}
  </div>;
}

function BindingRow({ item, report, agentId, workspaceId, onNavigate }: { item: AgentPackageBinding; report: AgentPackageBindingReport; agentId: string; workspaceId: string; onNavigate: (tab: string) => void }) {
  const { t } = useT("agents");
  const [mappings, setMappings] = useState<Record<string, string>>({});
  const mutation = useConfirmAgentPackageBinding(workspaceId, agentId);
  const desired = references(item.declaration);
  const current = references(item.current);
  const canConfirm = item.status === "pending" && !!item.current_fingerprint && desired.every((ref) => !!mappings[ref]);
  return <div className="space-y-2 border-t pt-3 text-caption">
    <div className="flex flex-wrap items-center justify-between gap-2">
      <code>{item.path}</code>
      <span>{item.status === "ready" ? t(($) => $.package_bindings.ready) : item.status === "pending" ? t(($) => $.package_bindings.needs_setup) : t(($) => $.package_bindings.unavailable)}</span>
    </div>
    {item.status !== "ready" && <>
      <p className="text-muted-foreground">{desired.length ? desired.join(", ") : t(($) => $.package_bindings.clear)}</p>
      {item.message && <p role="alert">{item.message}</p>}
      <Button variant="outline" size="sm" onClick={() => onNavigate(item.config_tab)}>{t(($) => $.package_bindings.configure)}</Button>
      {desired.map((ref) => <label key={ref} className="flex items-center gap-3">
        <span className="min-w-0 break-all">{ref}</span>
        <select aria-label={ref} className="min-w-0 flex-1 rounded-md border bg-background p-2" value={mappings[ref] ?? ""} disabled={mutation.isPending || item.status === "unavailable"} onChange={(event) => setMappings((previous) => ({ ...previous, [ref]: event.target.value }))}>
          <option value="">{t(($) => $.package_bindings.select)}</option>
          {current.map((actual) => <option key={actual} value={actual}>{report.resources.find((resource) => resource.ref === actual)?.label || actual} · {actual}</option>)}
        </select>
      </label>)}
      <details><summary>{t(($) => $.package_bindings.current)}</summary><pre className="overflow-x-auto whitespace-pre-wrap break-all">{JSON.stringify(item.current, null, 2)}</pre></details>
      <Button size="sm" disabled={!canConfirm || mutation.isPending} onClick={async () => {
        try { await mutation.mutateAsync({ path: item.path, revision: report.revision, current_fingerprint: item.current_fingerprint, mappings }); }
        catch { /* Keep complete server diagnostics visible. */ }
      }}>{t(($) => $.package_bindings.confirm)}</Button>
      <PackageError error={mutation.error} />
    </>}
  </div>;
}
