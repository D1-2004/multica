"use client";

import { useState, type ReactNode } from "react";
import { toast } from "sonner";
import { useCreateAgentPackage, usePrepareAgentPackage, useDownloadPreparedAgentPackage } from "@multica/core/agents";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import { Button } from "@multica/ui/components/ui/button";
import { Label } from "@multica/ui/components/ui/label";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { useNavigation } from "../../navigation";
import { useT } from "../../i18n";
import { CreateAgentFooter } from "./create-agent-footer";
import { PackageRequirementsForm } from "./package-requirements-form";
import { PackageError } from "./package-error";

export function BuilderPackagePanel({ content, onChange, runtimeId, runtimeProvider, runtimeControl, squadId, pending, onCreated, onDiscard, discarding }: {
  content: string;
  onChange: (content: string) => void;
  runtimeId: string | null;
  runtimeProvider: string | null;
  runtimeControl: ReactNode;
  squadId: string | null;
  pending: boolean;
  onCreated: () => Promise<void>;
  onDiscard: () => void;
  discarding: boolean;
}) {
  const { t } = useT("agents");
  const workspaceId = useWorkspaceId();
  const paths = useWorkspacePaths();
  const navigation = useNavigation();
  const prepare = usePrepareAgentPackage(workspaceId);
  const create = useCreateAgentPackage(workspaceId, squadId);
  const download = useDownloadPreparedAgentPackage(workspaceId);
  const [secrets, setSecrets] = useState<Record<string, string>>({});
  const [deferBindings, setDeferBindings] = useState(false);
  const preview = prepare.variables === content ? prepare.data : undefined;
  const busy = pending || prepare.isPending || create.isPending || create.isSuccess || download.isPending;
  const requirements = preview?.requirements;
  const compatible = !requirements?.runtime_provider || requirements.runtime_provider === runtimeProvider;
  const ready = !!preview?.preview_id && !!runtimeId && compatible && !busy && (requirements?.secrets ?? []).every((ref) => Object.hasOwn(secrets, ref)) && (!requirements?.deferred_bindings.length || deferBindings);
  const confirm = async () => {
    if (!ready || !preview || !runtimeId) return;
    try {
      const result = await create.mutateAsync({ preview_id: preview.preview_id, runtime_id: runtimeId, secrets, deferred_bindings: deferBindings ? requirements?.deferred_bindings : [] });
      result.warnings?.forEach((warning) => toast.warning(warning));
      // Creation has committed; conversation cleanup must not make it retryable.
      try { await onCreated(); } catch { /* The created Agent remains authoritative. */ }
      navigation.push(squadId ? paths.squadDetail(squadId) : paths.agentDetail(result.agent.id));
    } catch { /* Complete server diagnostics are rendered below. */ }
  };
  const downloadZIP = async () => {
    if (!preview || busy) return;
    try {
      const blob = await download.mutateAsync(preview.preview_id);
      const url = URL.createObjectURL(blob);
      const link = document.createElement("a"); link.href = url; link.download = "agent.zip";
      document.body.append(link); link.click(); link.remove(); setTimeout(() => URL.revokeObjectURL(url), 1000);
    } catch { /* Complete server diagnostics are rendered below. */ }
  };
  return <div className="flex h-full min-h-0 flex-col border-l bg-muted/10">
    <div className="min-h-0 flex-1 space-y-5 overflow-y-auto px-5 py-6">
      <h2 className="text-title-sm font-semibold">{t(($) => $.creation_studio.package.builder_title)}</h2>
      <p className="text-caption text-muted-foreground">{t(($) => $.creation_studio.package.builder_hint)}</p>
      {runtimeControl}
      <div className="space-y-2">
        <Label htmlFor="builder-package">{t(($) => $.creation_studio.package.contents)}</Label>
        <Textarea id="builder-package" value={content} className="min-h-64 font-mono text-caption" disabled={busy} onChange={(event) => { onChange(event.target.value); prepare.reset(); create.reset(); download.reset(); }} />
      </div>
      <Button variant="outline" disabled={!content.trim() || busy} onClick={() => { setSecrets({}); setDeferBindings(false); create.reset(); download.reset(); void prepare.mutateAsync(content).catch(() => {}); }}>
        {t(($) => $.creation_studio.package.validate)}
      </Button>
      <PackageError error={prepare.error ?? create.error ?? download.error} />
      {preview && <div className="space-y-4">
        <p className="text-body font-medium">{preview.name}</p>
        <p className="break-all font-mono text-caption">{preview.package_hash}</p>
        <details><summary className="cursor-pointer text-body">{t(($) => $.creation_studio.local.configuration)}</summary><pre className="max-h-80 overflow-auto whitespace-pre-wrap break-words text-caption">{JSON.stringify(preview.definition, null, 2)}</pre></details>
        <details><summary className="cursor-pointer text-body">{t(($) => $.tabs.instructions)}</summary><pre className="max-h-80 overflow-auto whitespace-pre-wrap break-words text-caption">{preview.instructions}</pre></details>
        {preview.skills?.map((skill) => <p key={skill.source_path} className="break-words text-caption">{skill.source_path} · {skill.name} · {skill.file_count}</p>)}
        {preview.warnings?.map((warning) => <p key={warning} className="text-caption text-muted-foreground">{warning}</p>)}
        {!compatible && <p role="alert" className="text-caption text-destructive">{t(($) => $.creation_studio.local.runtime_required, { provider: requirements?.runtime_provider })}</p>}
        <PackageRequirementsForm requirements={requirements} secrets={secrets} onSecretsChange={setSecrets} deferBindings={deferBindings} onDeferChange={setDeferBindings} disabled={busy} />
        <Button variant="outline" disabled={busy} onClick={() => void downloadZIP()}>{t(($) => $.creation_studio.package.download)}</Button>
      </div>}
    </div>
    <CreateAgentFooter canCreate={ready} creating={create.isPending} squad={!!squadId} error={null} onCreate={() => void confirm()} onDiscard={onDiscard} discarding={discarding} />
  </div>;
}
