"use client";

import { useState } from "react";
import { toast } from "sonner";
import { usePreviewAgentPackagePublication, useSyncAgentSource } from "@multica/core/agents";
import { useWorkspaceId } from "@multica/core/hooks";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Label } from "@multica/ui/components/ui/label";
import { PackageRequirementsForm } from "../../create/package-requirements-form";
import { PackageError } from "../../create/package-error";
import { SourceChangesDialog } from "./source-change-list";
import { useT } from "../../../i18n";

export function ZIPPublishTab({ agentId, canEdit }: { agentId: string; canEdit: boolean }) {
  const { t } = useT("agents");
  const workspaceId = useWorkspaceId();
  const [file, setFile] = useState<File | null>(null);
  const [pluginBindings, setPluginBindings] = useState<Record<string,string>>({});
  const [secrets, setSecrets] = useState<Record<string, string>>({});
  const [deferBindings, setDeferBindings] = useState(false);
  const previewMutation = usePreviewAgentPackagePublication(agentId);
  const confirmMutation = useSyncAgentSource(workspaceId, agentId);
  const preview = previewMutation.data;
  const [previewOpen, setPreviewOpen] = useState(false);
  const pending = previewMutation.isPending || confirmMutation.isPending;
  const ready = (preview?.requirements?.dshPlugins ?? []).every((plugin) => !!pluginBindings[plugin.ref]) && !!preview?.preview_id && (preview.requirements?.secrets ?? []).every((ref) => Object.hasOwn(secrets, ref)) && (!preview.requirements?.deferred_bindings.length || deferBindings);
  const handlePreview = async () => {
    if (!file || pending) return;
    setSecrets({}); setPluginBindings({}); setDeferBindings(false); confirmMutation.reset();
    setPreviewOpen(false); previewMutation.reset();
    try {
      await previewMutation.mutateAsync(file);
      setPreviewOpen(true);
    } catch { /* The persistent error panel retains the complete response. */ }
  };
  const confirm = async () => {
    if (!ready || !preview || pending || !canEdit) return;
    try {
      const result = await confirmMutation.mutateAsync({ previewId: preview.preview_id, secrets, dshPluginBindings: pluginBindings, deferredBindings: deferBindings ? preview.requirements?.deferred_bindings : [] });
      result.warnings?.forEach((warning) => toast.warning(warning));
      toast.success(t(($) => $.detail.source_sync_succeeded));
      setSecrets({}); setPluginBindings({}); previewMutation.reset();
    } catch { /* The persistent error panel retains the complete response. */ }
  };
  return <div className="space-y-4">
    <p className="text-body text-muted-foreground">{t(($) => $.creation_studio.package.zip_hint)}</p>
    {canEdit && <div className="space-y-3">
      <Label htmlFor="agent-publish-zip">{t(($) => $.creation_studio.local.file)}</Label>
      <Input id="agent-publish-zip" type="file" accept=".zip,application/zip" disabled={pending} onChange={(event) => {
        setFile(event.target.files?.[0] ?? null); setSecrets({}); setPluginBindings({}); setDeferBindings(false); previewMutation.reset(); confirmMutation.reset();
      }} />
      <Button variant="outline" disabled={!file || pending} onClick={handlePreview}>
        {t(($) => $.tab_body.publish.preview)}
      </Button>
    </div>}
    <PackageError error={previewMutation.error ?? confirmMutation.error} />
    {preview && <div className="space-y-4">
      <p className="break-all font-mono text-caption">{preview.resolved_sha}</p>
      {preview.warnings?.map((warning) => <p key={warning} className="text-caption text-muted-foreground">{warning}</p>)}
      <SourceChangesDialog key={preview.preview_id} open={previewOpen} onOpenChange={setPreviewOpen} groups={[
        { id: "configuration", title: t(($) => $.tab_body.publish.configuration_changes), changes: preview.configuration_changes ?? [] },
      ]} />
      <PackageRequirementsForm workspaceId={workspaceId} pluginBindings={pluginBindings} onPluginBindingsChange={setPluginBindings} requirements={preview.requirements} secrets={secrets} onSecretsChange={setSecrets} deferBindings={deferBindings} onDeferChange={setDeferBindings} disabled={pending} />
      <p className="text-caption text-muted-foreground">{t(($) => $.tab_body.publish.confirm_hint)}</p>
      <Button disabled={!ready || pending || !canEdit} onClick={() => void confirm()}>{t(($) => $.tab_body.publish.confirm)}</Button>
    </div>}
  </div>;
}
