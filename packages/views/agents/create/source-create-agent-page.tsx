"use client";

import { PackageError } from "./package-error";

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import { useCreateAgentPackage, usePreviewAgentPackage } from "@multica/core/agents";
import { gitRepositoryOptions, gitAgentBranchesOptions, usePreviewGitAgent } from "@multica/core/git-repo";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Label } from "@multica/ui/components/ui/label";
import { AppLink, useBackOrReplace, useNavigation } from "../../navigation";
import { useT } from "../../i18n";
import { PackageRequirementsForm } from "./package-requirements-form";
import { RuntimePicker } from "../components/runtime-picker";
import { GitRevisionSelect } from "../components/git-revision-select";
import { AgentCreateShell } from "./create-shell";
import { CreateAgentFooter } from "./create-agent-footer";
import { useCreateAgentForm } from "./use-create-agent-form";
import { withSquadParam } from "./squad-param";

export function SourceCreateAgentPage({ source }: { source: "git" | "local" }) {
  const local = source === "local";
  const { t } = useT("agents");
  const wsId = useWorkspaceId();
  const paths = useWorkspacePaths();
  const navigation = useNavigation();
  const back = useBackOrReplace();
  const squadId = navigation.searchParams.get("squad");
  const form = useCreateAgentForm();
  const [connectionId, setConnectionId] = useState("");
  const [repository, setRepository] = useState("");
  const [ref, setRef] = useState("");
  const [revisionReady, setRevisionReady] = useState(true);
  const [loadedRepository, setLoadedRepository] = useState("");
  const connections = useQuery({ ...gitRepositoryOptions(wsId, loadedRepository), enabled: !local && !!loadedRepository });
  const canManage = form.members.some((member) => member.user_id === form.currentUserId && (member.role === "owner" || member.role === "admin"));
  const branches = useQuery({ ...gitAgentBranchesOptions(wsId, connectionId, loadedRepository), enabled: !local && canManage && !!loadedRepository && !!connections.data && (!!connectionId || connections.data.connections.length < 2) });
  const previewMutation = usePreviewGitAgent(wsId);
  const uploadMutation = usePreviewAgentPackage(wsId);
  const createMutation = useCreateAgentPackage(wsId, squadId);
  const [file, setFile] = useState<File | null>(null);
  const [pluginBindings, setPluginBindings] = useState<Record<string,string>>({});
  const [secrets, setSecrets] = useState<Record<string, string>>({});
  const [deferBindings, setDeferBindings] = useState(false);
  const preview = local ? uploadMutation.data : previewMutation.data;
  const requirements = preview?.requirements;
  const requirementsReady = (requirements?.dshPlugins ?? []).every((plugin) => !!pluginBindings[plugin.ref]) && (requirements?.secrets ?? []).every((ref) => Object.hasOwn(secrets, ref)) && (!requirements?.deferred_bindings.length || deferBindings);
  const runtimeCompatible = !requirements?.runtime_provider || form.selectedRuntime?.provider === requirements.runtime_provider;
  const pending = previewMutation.isPending || uploadMutation.isPending || createMutation.isPending;
  const canCreate = canManage && !!preview?.preview_id && !previewMutation.data?.blockers?.length && requirementsReady && runtimeCompatible && form.draftReady && !!form.draft.name.trim() && !pending && !createMutation.isSuccess;
  const error = createMutation.error ?? uploadMutation.error ?? previewMutation.error ?? (!local ? connections.error : null);

  const invalidatePreview = () => { previewMutation.reset(); uploadMutation.reset(); createMutation.reset(); setSecrets({}); setPluginBindings({}); setDeferBindings(false); };
  const handlePreview = async () => {
    try {
      invalidatePreview();
      const result = local && file ? await uploadMutation.mutateAsync(file) : await previewMutation.mutateAsync({ connection_id: connectionId || branches.data?.connection_id || undefined, repository: repository.trim(), ref: ref.trim() || undefined });
      form.setDraft((current) => ({ ...current, name: result.name, description: result.description }));
    } catch { /* The form renders the mutation error. */ }
  };
  const handleCreate = async () => {
    if (!canCreate || !preview?.preview_id || !form.selectedRuntime) return;
    try {
      const result = await createMutation.mutateAsync({
        name: form.draft.name.trim(), runtime_id: form.selectedRuntime.id, secrets,
        deferred_bindings: deferBindings ? requirements?.deferred_bindings : [],
        preview_id: preview.preview_id, dsh_plugin_bindings: pluginBindings,
      });
      setSecrets({}); setPluginBindings({});
      result.warnings?.forEach((warning) => toast.warning(warning));
      navigation.push(`${paths.agentDetail(result.agent.id)}${requirements?.deferred_bindings.length ? "?view=publish" : ""}`);
    } catch { /* Keep the preview ID for an idempotent retry. */ }
  };

  return (
    <AgentCreateShell title={local ? t(($) => $.creation_studio.modes.local.title) : t(($) => $.creation_studio.modes.git.title)} step={local ? t(($) => $.creation_studio.local.step) : t(($) => $.creation_studio.git.step)} onBack={() => back(withSquadParam(paths.newAgent(), squadId))}>
      <div className="min-h-0 flex-1 overflow-y-auto">
        <div className="mx-auto w-full max-w-3xl space-y-6 px-5 py-8">
          <p className="text-body text-muted-foreground">{local ? t(($) => $.creation_studio.local.description) : t(($) => $.creation_studio.git.description)}</p>
          {!local && <p className="text-body text-muted-foreground"><AppLink className="underline" href={`${paths.settings()}?tab=repositories&section=connections`}>{t(($) => $.creation_studio.git.manage_connections)}</AppLink></p>}
          {local && <div className="space-y-3">
            <Label htmlFor="agent-package-file">{t(($) => $.creation_studio.local.file)}</Label>
            <Input id="agent-package-file" type="file" accept=".zip,application/zip" disabled={pending || createMutation.isSuccess} onChange={(event) => { invalidatePreview(); setFile(event.target.files?.[0] ?? null); }} />
            <Button variant="outline" disabled={!file || !canManage || pending || createMutation.isSuccess} onClick={() => void handlePreview()}>{t(($) => $.creation_studio.local.preview)}</Button>
            {!canManage && <p className="text-caption text-muted-foreground">{t(($) => $.creation_studio.local.admin_required)}</p>}
          </div>}
          {!local && <fieldset disabled={!canManage || pending || createMutation.isSuccess} className="space-y-4 disabled:opacity-60">
            <div className="space-y-2">
              <Label htmlFor="git-repository">{t(($) => $.tab_body.publish.repository)}</Label>
              <Input id="git-repository" placeholder="https://…/team/agent" value={repository} onChange={(event) => { setRepository(event.target.value); setLoadedRepository(""); setConnectionId(""); setRef(""); setRevisionReady(true); invalidatePreview(); }} />
              <Button variant="outline" disabled={!repository.trim()} onClick={() => setLoadedRepository(repository.trim())}>{t(($) => $.creation_studio.git.read_repository)}</Button>
              {branches.isError && <p role="alert" className="text-caption text-destructive">{branches.error.message}</p>}
            </div>
            {loadedRepository && (connections.data?.connections.length ?? 0) > 1 && <div className="space-y-2">
              <Label htmlFor="git-connection">{t(($) => $.creation_studio.git.connection)}</Label>
              <select id="git-connection" value={connectionId} className="h-9 w-full rounded-md border bg-background px-3 text-body" onChange={(event) => { setConnectionId(event.target.value); setRef(""); invalidatePreview(); }}>
                <option value="">{t(($) => $.creation_studio.git.auto_connection)}</option>
                {connections.data?.connections.map((item) => <option key={item.id} value={item.id}>{item.account_login} · {item.provider}</option>)}
              </select>
            </div>}
            <div className="space-y-2">
              <GitRevisionSelect key={`${connectionId}:${repository}`} id="git-ref" value={ref} revisions={branches.data} allowDefault onReadyChange={setRevisionReady} onChange={(value) => { setRef(value); invalidatePreview(); }} />
              {branches.isError && <p className="text-caption text-muted-foreground">{t(($) => $.tab_body.publish.branches_failed)}</p>}
            </div>
            <Button variant="outline" disabled={!loadedRepository || !branches.data || branches.isFetching || !revisionReady || pending} onClick={() => void handlePreview()}>{t(($) => $.creation_studio.git.preview)}</Button>
          </fieldset>}
          {preview && (
            <div className="space-y-4 rounded-lg border p-4">
              <p className="break-all font-mono text-caption">{local ? file?.name : `${previewMutation.data?.repository} · ${previewMutation.data?.ref}`} · {local ? uploadMutation.data?.package_hash : previewMutation.data?.resolved_sha}</p>
              {preview.warnings?.map((warning) => <p key={warning} className="text-caption text-muted-foreground">{warning}</p>)}
              {previewMutation.data?.blockers?.map((blocker) => <p key={blocker} role="alert" className="text-caption text-destructive">{blocker}</p>)}
              <div className="space-y-2">
                <Label htmlFor="git-agent-name">{t(($) => $.create_dialog.name_label)}</Label>
                <Input id="git-agent-name" value={form.draft.name} disabled={pending} onChange={(event) => form.setDraft((current) => ({ ...current, name: event.target.value }))} />
              </div>
              <RuntimePicker runtimes={form.runtimes} runtimesLoading={form.runtimesLoading} members={form.members} currentUserId={form.currentUserId} selectedRuntimeId={form.draft.runtimeId} disabled={pending} onSelect={(runtimeId) => form.setDraft((current) => ({ ...current, runtimeId }))} />
              {!runtimeCompatible && <p role="alert" className="text-caption text-destructive">{t(($) => $.creation_studio.local.runtime_required, { provider: requirements?.runtime_provider })}</p>}
              <PackageRequirementsForm workspaceId={wsId} pluginBindings={pluginBindings} onPluginBindingsChange={setPluginBindings} requirements={requirements} secrets={secrets} onSecretsChange={setSecrets} deferBindings={deferBindings} onDeferChange={setDeferBindings} disabled={pending} />
              <p className="text-caption text-muted-foreground">{t(($) => $.creation_studio.local.confirm_hint)}</p>
              {preview.definition && <details><summary className="cursor-pointer text-body">{t(($) => $.creation_studio.local.configuration)}</summary><pre className="mt-2 max-h-80 overflow-auto whitespace-pre-wrap break-words rounded bg-muted p-3 text-caption">{JSON.stringify(preview.definition, null, 2)}</pre></details>}
              <details><summary className="cursor-pointer text-body">{t(($) => $.tabs.instructions)}</summary><pre className="mt-2 max-h-80 overflow-auto whitespace-pre-wrap break-words rounded bg-muted p-3 text-caption">{preview.instructions}</pre></details>
              <div className="space-y-2"><h3 className="text-body font-medium">{t(($) => $.tabs.skills)} ({preview.skills?.length ?? 0})</h3>{preview.skills?.map((skill) => <div key={skill.source_path} className="break-words text-caption"><span className="font-medium">{skill.name}</span>{skill.enabled === false && <span> · {t(($) => $.creation_studio.git.disabled_skill)}</span>} · {skill.description} ({skill.file_count})</div>)}</div>
            </div>
          )}
        </div>
        <PackageError error={error} />
        <CreateAgentFooter canCreate={canCreate} creating={createMutation.isPending} squad={!!squadId} error={null} onCreate={() => void handleCreate()} />
      </div>
    </AgentCreateShell>
  );
}
