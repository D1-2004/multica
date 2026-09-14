"use client";

import { useState } from "react";
import { useAgentDshPluginConfig, type AgentDshPlugin } from "@multica/core/dsh-plugins";
import { ApiError } from "@multica/core/api";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@multica/ui/components/ui/dialog";
import { useT } from "../../../i18n";

export function DshPluginConfigDialog({ wsId, agentId, plugin, onClose }: {
  wsId: string; agentId: string; plugin: AgentDshPlugin; onClose: () => void;
}) {
  const { t } = useT("agents");
  const { load, save } = useAgentDshPluginConfig(wsId, agentId, plugin.id);
  const [revision, setRevision] = useState<number | null>(null);
  const [rowId, setRowId] = useState("");
  const [draft, setDraft] = useState("{}");
  const [inherited, setInherited] = useState(false);
  const [message, setMessage] = useState("");
  const busy = load.isPending || save.isPending;
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
    try {
      const value = await save.mutateAsync({ expectedRevision: revision, override: inherit ? null : { rowId, config } });
      if (!value) throw new Error("invalid response");
      setRevision(value.revision); setInherited(value.inherited); setRowId(value.rowId); setDraft(JSON.stringify(value.config, null, 2));
      setMessage(t(($) => $.tab_body.dsh_plugins.config_saved));
    } catch (error) { reportError(error); } finally { save.reset(); }
  };
  return (
    <Dialog open onOpenChange={(open) => { if (!open) onClose(); }}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t(($) => $.tab_body.dsh_plugins.config_title, { name: plugin.packageName })}</DialogTitle>
          <DialogDescription>{t(($) => $.tab_body.dsh_plugins.config_hint)}</DialogDescription>
        </DialogHeader>
        <Button variant="outline" onClick={() => void reveal()} disabled={busy}>{t(($) => $.tab_body.dsh_plugins.config_reveal)}</Button>
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
