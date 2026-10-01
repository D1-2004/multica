"use client";

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import { Lock, Tag as TagIcon } from "lucide-react";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import { runtimeListOptions } from "@multica/core/runtimes";
import { agentListOptions } from "@multica/core/workspace/queries";
import { tagOptions, useCreateTag, useDeleteTag, useSetTagSidebarVisible } from "@multica/core/tag";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { NativeSelect, NativeSelectOption } from "@multica/ui/components/ui/native-select";
import { Switch } from "@multica/ui/components/ui/switch";
import { AppLink } from "../../navigation";
import { useT } from "../../i18n";
import { SettingsCard, SettingsRow, SettingsSection, SettingsTab } from "./settings-layout";

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

/**
 * Settings → Tag. Only platform operators see this tab (the settings page
 * gates it on `canOperate`); the server enforces the same rule.
 */
export function TagSettingsTab() {
  const { t } = useT("settings");
  const wsId = useWorkspaceId();
  const paths = useWorkspacePaths();
  const { data: state } = useQuery(tagOptions(wsId));
  const setVisible = useSetTagSidebarVisible(wsId);
  const removeTag = useDeleteTag(wsId);
  const tag = state?.tag ?? null;

  return (
    <SettingsTab title={t(($) => $.tag.title)} description={t(($) => $.tag.description)}>
      <p className="flex items-center gap-1.5 text-caption text-muted-foreground">
        <Lock className="h-3.5 w-3.5" aria-hidden="true" />
        {t(($) => $.tag.operator_only)}
      </p>
      {tag ? (
        <>
          <SettingsCard>
            <SettingsRow
              label={
                <span className="flex items-center gap-2">
                  <TagIcon className="h-4 w-4" aria-hidden="true" />
                  {t(($) => $.tag.fixed_name)}
                </span>
              }
              description={t(($) => $.tag.tenants_count, { count: state?.tenants.length ?? 0 })}
            >
              <Button size="sm" variant="outline" render={<AppLink href={paths.tag()} />} nativeButton={false}>
                {t(($) => $.tag.open)}
              </Button>
            </SettingsRow>
            <SettingsRow label={t(($) => $.tag.sidebar_visible)} description={t(($) => $.tag.sidebar_hint)}>
              <Switch
                checked={tag.sidebarVisible}
                disabled={setVisible.isPending}
                onCheckedChange={(checked) =>
                  setVisible.mutate(checked === true, {
                    onError: (error) => toast.error(t(($) => $.tag.failed, { message: errorMessage(error) })),
                  })
                }
              />
            </SettingsRow>
          </SettingsCard>
          <SettingsSection title={t(($) => $.tag.remove)} description={t(($) => $.tag.remove_hint)}>
            <div>
              <Button
                size="sm"
                variant="destructive"
                disabled={(state?.tenants.length ?? 0) > 0 || removeTag.isPending}
                onClick={() =>
                  removeTag.mutate(undefined, {
                    onSuccess: () => toast.success(t(($) => $.tag.removed)),
                    onError: (error) => toast.error(t(($) => $.tag.failed, { message: errorMessage(error) })),
                  })
                }
              >
                {t(($) => $.tag.remove)}
              </Button>
            </div>
          </SettingsSection>
        </>
      ) : (
        <CreateTagForm wsId={wsId} />
      )}
    </SettingsTab>
  );
}

function CreateTagForm({ wsId }: { wsId: string }) {
  const { t } = useT("settings");
  const { data: runtimes = [] } = useQuery(runtimeListOptions(wsId));
  const { data: agents = [] } = useQuery(agentListOptions(wsId));
  const create = useCreateTag(wsId);
  const cloudRuntimes = runtimes.filter((runtime) => runtime.runtime_mode === "cloud");
  const [description, setDescription] = useState("");
  const [runtimeId, setRuntimeId] = useState("");
  const [model, setModel] = useState("");
  const [copyFrom, setCopyFrom] = useState("");
  const effectiveRuntime = runtimeId || cloudRuntimes[0]?.id || "";
  const canSubmit = effectiveRuntime !== "" && !create.isPending;

  const submit = () => {
    if (!canSubmit) return;
    create.mutate(
      {
        description: description.trim(),
        runtimeId: effectiveRuntime,
        model: model.trim(),
        copyFromAgentId: copyFrom || undefined,
      },
      {
        onSuccess: () => toast.success(t(($) => $.tag.created)),
        onError: (error) => toast.error(t(($) => $.tag.failed, { message: errorMessage(error) })),
      },
    );
  };

  return (
    <SettingsSection title={t(($) => $.tag.create_heading)} description={t(($) => $.tag.empty)}>
      <SettingsCard>
        <SettingsRow label={t(($) => $.tag.name)} description={t(($) => $.tag.name_fixed_hint)}>
          <span className="flex items-center gap-2 text-body font-medium">
            <TagIcon className="h-4 w-4" aria-hidden="true" />
            {t(($) => $.tag.fixed_name)}
          </span>
        </SettingsRow>
        <SettingsRow label={t(($) => $.tag.description_label)} size="text">
          <Input value={description} onChange={(event) => setDescription(event.target.value)} />
        </SettingsRow>
        <SettingsRow
          label={t(($) => $.tag.runtime)}
          description={cloudRuntimes.length > 0 ? t(($) => $.tag.runtime_hint) : t(($) => $.tag.no_cloud_runtime)}
          size="select-wide"
        >
          <NativeSelect
            value={effectiveRuntime}
            disabled={cloudRuntimes.length === 0}
            onChange={(event) => setRuntimeId(event.target.value)}
          >
            {cloudRuntimes.map((runtime) => (
              <NativeSelectOption key={runtime.id} value={runtime.id}>
                {runtime.name}
              </NativeSelectOption>
            ))}
          </NativeSelect>
        </SettingsRow>
        <SettingsRow label={t(($) => $.tag.model)} size="text">
          <Input value={model} onChange={(event) => setModel(event.target.value)} />
        </SettingsRow>
        <SettingsRow label={t(($) => $.tag.copy_from)} size="select-wide">
          <NativeSelect value={copyFrom} onChange={(event) => setCopyFrom(event.target.value)}>
            <NativeSelectOption value="">{t(($) => $.tag.copy_none)}</NativeSelectOption>
            {agents
              .filter((agent) => !agent.archived_at)
              .map((agent) => (
                <NativeSelectOption key={agent.id} value={agent.id}>
                  {agent.name}
                </NativeSelectOption>
              ))}
          </NativeSelect>
        </SettingsRow>
      </SettingsCard>
      <div className="flex justify-end">
        <Button disabled={!canSubmit} onClick={submit}>
          {t(($) => $.tag.submit)}
        </Button>
      </div>
    </SettingsSection>
  );
}
