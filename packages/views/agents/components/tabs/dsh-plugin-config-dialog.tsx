"use client";

import { useState } from "react";
import { useAgentDshPluginConfig, type AgentDshPlugin, type UpdateAgentDshPluginConfig } from "@multica/core/dsh-plugins";
import { ApiError } from "@multica/core/api";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@multica/ui/components/ui/dialog";
import { useT } from "../../../i18n";

export function DshPluginConfigDialog({ wsId, agentId, plugin, onClose, attached, initialChange, onStage }: {
  wsId: string; agentId: string; plugin: AgentDshPlugin; onClose: () => void;
  attached: boolean; initialChange?: UpdateAgentDshPluginConfig; onStage: (change: UpdateAgentDshPluginConfig) => void;
}) {
  const { t } = useT("agents");
  const { load } = useAgentDshPluginConfig(wsId, agentId, plugin.id);
  const [revision, setRevision] = useState<number | null>(initialChange?.expectedRevision ?? (attached ? null : 0));
  const [rowId, setRowId] = useState(initialChange?.override?.rowId ?? plugin.configRow);
  const [draft, setDraft] = useState(JSON.stringify(initialChange?.override?.config ?? (attached ? {} : plugin.config), null, 2));
  const [inherited, setInherited] = useState(initialChange ? initialChange.override === null : !attached);
  const [message, setMessage] = useState("");
  const busy = load.isPending;
  const reportError = (error: unknown) => setMessage(error instanceof ApiError ? error.message : t(($) => $.tab_body.dsh_plugins.config_failed));
  const reveal = async () => {
    setMessage("");
    try {
      const value = await load.mutateAsync();
      if (!value) throw new Error("invalid response");
      setRevision(value.revision); setRowId(value.rowId); setDraft(JSON.stringify(value.config, null, 2)); setInherited(value.inherited);
    } catch (error) { reportError(error); } finally { load.reset(); }
  };
  const submit = async (inherit: boolean) => {
    if (revision === null) return;
    setMessage("");
    let config: Record<string, unknown> = {};
    if (!inherit) {
      try {
        const parsed: unknown = JSON.parse(draft);
        if (parsed === null || Array.isArray(parsed) || typeof parsed !== "object") throw new Error("object required");
        config = parsed as Record<string, unknown>;
      } catch { setMessage(t(($) => $.tab_body.dsh_plugins.config_invalid)); return; }
    }
    onStage({expectedRevision: revision, override: inherit ? null : {rowId, config}});
  };
  return (
    <Dialog open onOpenChange={(open) => { if (!open) onClose(); }}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t(($) => $.tab_body.dsh_plugins.config_title, { name: plugin.packageName })}</DialogTitle>
          <DialogDescription>{t(($) => $.tab_body.dsh_plugins.config_hint)}</DialogDescription>
        </DialogHeader>
        {attached && <Button variant="outline" onClick={() => void reveal()} disabled={busy}>{t(($) => $.tab_body.dsh_plugins.config_reveal)}</Button>}
        {revision !== null && <>
          <p className="text-caption text-muted-foreground">{inherited ? t(($) => $.tab_body.dsh_plugins.config_inherited) : t(($) => $.tab_body.dsh_plugins.config_private)} · {t(($) => $.tab_body.dsh_plugins.config_revision, { revision })}</p>
          <label className="space-y-2 text-caption">
            <span>{t(($) => $.tab_body.dsh_plugins.config_row)}</span>
            <Input value={rowId} onChange={(event) => setRowId(event.target.value)} disabled={busy} autoComplete="off" />
          </label>
          <label className="space-y-2 text-caption">
            <span>{t(($) => $.tab_body.dsh_plugins.config_json)}</span>
            <Textarea className="min-h-48 font-mono" value={draft} onChange={(event) => setDraft(event.target.value)} disabled={busy} autoComplete="off" spellCheck={false} />
          </label>
          <div className="flex flex-wrap gap-2">
            <Button onClick={() => void submit(false)} disabled={busy}>{t(($) => $.tab_body.dsh_plugins.config_save)}</Button>
            <Button variant="outline" onClick={() => void submit(true)} disabled={busy || inherited}>{t(($) => $.tab_body.dsh_plugins.config_reset)}</Button>
          </div>
        </>}
        {message && <p role="status" className="text-caption">{message}</p>}
      </DialogContent>
    </Dialog>
  );
}
