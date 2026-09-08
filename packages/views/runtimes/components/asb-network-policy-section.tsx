"use client";

import { useState } from "react";
import { ShieldCheck } from "lucide-react";
import { toast } from "sonner";
import { useASBNetworkPolicy, useUpdateASBNetworkPolicy } from "@multica/core/runtimes";
import { useWorkspaceId } from "@multica/core/hooks";
import { Button } from "@multica/ui/components/ui/button";
import { Label } from "@multica/ui/components/ui/label";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { useT } from "../../i18n";

export function ASBNetworkPolicySection({ runtimeId }: { runtimeId: string }) {
  const { t } = useT("runtimes");
  const wsId = useWorkspaceId();
  const policy = useASBNetworkPolicy(wsId, runtimeId);
  const update = useUpdateASBNetworkPolicy(wsId, runtimeId);
  const [draft, setDraft] = useState<string | null>(null);
  const saved = policy.data?.customTargets?.join("\n") ?? "";
  const available = policy.data?.available === true;

  async function save() {
    try {
      const result = await update.mutateAsync((draft ?? saved).split(/[\n,]/).map((v) => v.trim()).filter(Boolean));
      if (result.available !== true) throw new Error(t(($) => $.detail.asb_network.load_failed));
      setDraft(null);
      toast.success(t(($) => $.detail.asb_network.saved));
    } catch (error) {
      toast.error(error instanceof Error ? error.message : t(($) => $.detail.asb_network.save_failed));
    }
  }

  return (
    <section className="rounded-lg border">
      <div className="flex items-center gap-1.5 border-b px-4 py-2.5">
        <ShieldCheck className="h-3.5 w-3.5 text-muted-foreground" />
        <span className="text-caption font-semibold">{t(($) => $.detail.asb_network.title)}</span>
      </div>
      <div className="space-y-3 p-4">
        <p className="text-caption text-muted-foreground">{t(($) => $.detail.asb_network.description)}</p>
        {policy.isPending ? <p className="text-caption">{t(($) => $.detail.asb_network.loading)}</p> : !available || policy.isError ? (
          <div className="space-y-2">
            <p className="text-caption text-destructive">{t(($) => $.detail.asb_network.load_failed)}</p>
            <Button size="sm" variant="outline" onClick={() => policy.refetch()}>{t(($) => $.detail.asb_network.retry)}</Button>
          </div>
        ) : (
          <>
            <details>
              <summary className="cursor-pointer text-caption font-medium">{t(($) => $.detail.asb_network.defaults, { count: policy.data.defaultTargets?.length ?? 0 })}</summary>
              <div className="mt-2 max-h-48 overflow-y-auto rounded-md bg-muted/30 p-3">
                {policy.data.defaultTargets?.map((target) => <code key={target} className="block break-all text-caption">{target}</code>)}
              </div>
            </details>
            <div className="space-y-2">
              <Label htmlFor={`asb-network-${runtimeId}`}>{t(($) => $.detail.asb_network.custom)}</Label>
              <Textarea id={`asb-network-${runtimeId}`} value={draft ?? saved} onChange={(event) => setDraft(event.target.value)} placeholder="api.example.com" rows={5} disabled={update.isPending} className="font-mono text-caption" />
              <p className="text-caption text-muted-foreground">{t(($) => $.detail.asb_network.hint)}</p>
              <p className="text-caption text-muted-foreground">{t(($) => $.detail.asb_network.applies)}</p>
            </div>
            <div className="flex gap-2">
              <Button size="sm" disabled={draft === null || draft === saved || update.isPending} onClick={save}>{t(($) => update.isPending ? $.detail.asb_network.saving : $.detail.asb_network.save)}</Button>
              <Button size="sm" variant="outline" disabled={draft === null || update.isPending} onClick={() => setDraft(null)}>{t(($) => $.detail.asb_network.cancel)}</Button>
            </div>
          </>
        )}
      </div>
    </section>
  );
}
