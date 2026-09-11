"use client";

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import type { AgentSource } from "@multica/core/types";
import { useWorkspaceId } from "@multica/core/hooks";
import { agentSourceBranchesOptions, usePreviewAgentSourceSync, useSyncAgentSource } from "@multica/core/agents";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Label } from "@multica/ui/components/ui/label";
import { PackageRequirementsForm } from "../../create/package-requirements-form";
import { useT } from "../../../i18n";
import { PackageError } from "../../create/package-error";
import { ZIPPublishTab } from "./zip-publish-tab";
import { ChangeList } from "./source-change-list";

export function PublishTab({ agentId, source, canEdit }: { agentId: string; source: AgentSource | null; canEdit: boolean }) {
  const { t } = useT("agents");
  const [mode, setMode] = useState(source?.source_type === "github" ? "git" : "zip");
  return <div className="space-y-5">
    {source?.source_type === "github" && canEdit && <div className="flex gap-2">
      <Button variant={mode === "git" ? "secondary" : "ghost"} onClick={() => setMode("git")}>{t(($) => $.creation_studio.package.git_publish)}</Button>
      <Button variant={mode === "zip" ? "secondary" : "ghost"} onClick={() => setMode("zip")}>{t(($) => $.creation_studio.package.zip_publish)}</Button>
    </div>}
    {mode === "git" && source?.source_type === "github" ? <GitPublishTab source={source} canEdit={canEdit} /> : <ZIPPublishTab agentId={agentId} canEdit={canEdit} hasGitSource={source?.source_type === "github"} />}
  </div>;
}

function GitPublishTab({ source, canEdit }: { source: AgentSource; canEdit: boolean }) {
  const { t } = useT("agents");
  const workspaceId = useWorkspaceId();
  const [ref, setRef] = useState(source.ref);
  const canSync = canEdit && source.github_connected === true && source.can_sync !== false;
  const branches = useQuery({ ...agentSourceBranchesOptions(workspaceId, source.agent_id), enabled: canSync });
  const previewMutation = usePreviewAgentSourceSync(source.agent_id);
  const syncMutation = useSyncAgentSource(workspaceId, source.agent_id);
  const preview = previewMutation.data;
  const [secrets, setSecrets] = useState<Record<string, string>>({});
  const [deferBindings, setDeferBindings] = useState(false);
  const requirementsReady = (preview?.requirements?.secrets ?? []).every((ref) => Object.hasOwn(secrets, ref)) && (!preview?.requirements?.deferred_bindings.length || deferBindings);
  const pending = previewMutation.isPending || syncMutation.isPending;

  const handlePreview = async () => {
    try {
      setSecrets({}); setDeferBindings(false); syncMutation.reset();
      await previewMutation.mutateAsync(ref.trim());
    } catch { /* The persistent error panel retains the complete response. */ }
  };

  const handleConfirm = async () => {
    if (!preview?.preview_id || pending || !canSync || !requirementsReady) return;
    try {
      const result = await syncMutation.mutateAsync({ previewId: preview.preview_id, secrets, deferredBindings: deferBindings ? preview.requirements?.deferred_bindings : [] });
      setSecrets({});
      toast.success(t(($) => $.detail.source_sync_succeeded));
      result.warnings?.forEach((warning) => toast.warning(warning));
      previewMutation.reset();
    } catch {
      previewMutation.reset();
    }
  };

  return (
    <div className="space-y-6">
      <p className="text-caption text-muted-foreground">{t(($) => $.tab_body.publish.description)}</p>
      <dl className="grid gap-3 text-caption">
        <div className="space-y-1">
          <dt className="text-muted-foreground">{t(($) => $.tab_body.publish.repository)}</dt>
          <dd className="break-all"><a className="underline underline-offset-4" href={`https://github.com/${source.repository}`} target="_blank" rel="noreferrer">{`https://github.com/${source.repository}`}</a></dd>
        </div>
        <div className="space-y-1">
          <dt className="text-muted-foreground">{t(($) => $.tab_body.publish.deployed)}</dt>
          <dd className="break-all font-mono">{source.ref} · {source.synced_commit_sha}</dd>
        </div>
      </dl>
      {source.last_sync_error && <p role="alert" className="text-caption text-destructive">{source.last_sync_error}</p>}
      {!canSync && <p className="text-caption text-muted-foreground">{t(($) => $.tab_body.publish.unavailable)}</p>}
      {canSync && (
        <div className="space-y-3">
          <Label htmlFor="agent-source-ref">{t(($) => $.tab_body.publish.branch)}</Label>
          <div className="flex flex-wrap items-center gap-2">
            <Input id="agent-source-ref" list="agent-source-branches" value={ref} disabled={pending} className="min-w-0 flex-1"
              onChange={(event) => { setRef(event.target.value); previewMutation.reset(); syncMutation.reset(); }} />
            <datalist id="agent-source-branches">
              {branches.data?.branches?.map((branch) => <option key={branch.name} value={branch.name} />)}
            </datalist>
            <Button variant="outline" disabled={pending || !ref.trim()} onClick={handlePreview}>
              {previewMutation.isPending ? t(($) => $.tab_body.publish.loading) : t(($) => $.tab_body.publish.preview)}
            </Button>
          </div>
          {branches.isError && <p className="text-caption text-muted-foreground">{t(($) => $.tab_body.publish.branches_failed)}</p>}
        </div>
      )}
      <PackageError error={previewMutation.error ?? syncMutation.error} />
      {preview && (
        <div className="space-y-4">
          <p className="break-all font-mono text-caption">{preview.base_sha.slice(0, 12)} → {preview.resolved_sha.slice(0, 12)} · {preview.ref}</p>
          {preview.warnings?.map((warning) => <p key={warning} className="text-caption text-muted-foreground">{warning}</p>)}
          <PackageRequirementsForm requirements={preview.requirements} secrets={secrets} onSecretsChange={setSecrets} deferBindings={deferBindings} onDeferChange={setDeferBindings} disabled={pending} />
          <ChangeList title={t(($) => $.tab_body.publish.git_changes)} changes={preview.git_changes ?? []} />
          <ChangeList title={t(($) => $.tab_body.publish.configuration_changes)} changes={preview.configuration_changes ?? []} />
          {preview.changed === true ? (
            <div className="space-y-3">
              <p className="text-caption text-muted-foreground">{t(($) => $.tab_body.publish.confirm_hint)}</p>
              <Button disabled={pending || !canSync || !requirementsReady} onClick={handleConfirm}>
                {syncMutation.isPending ? t(($) => $.tab_body.publish.publishing) : t(($) => $.tab_body.publish.confirm)}
              </Button>
            </div>
          ) : <p className="text-caption text-muted-foreground">{t(($) => $.detail.source_already_current)}</p>}
        </div>
      )}
    </div>
  );
}
