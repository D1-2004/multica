"use client";

import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowLeft, Link2, MessageSquare, Pencil, Users } from "lucide-react";
import { toast } from "sonner";
import { useDefaultLayout } from "react-resizable-panels";
import { api } from "@multica/core/api";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@multica/ui/components/ui/alert-dialog";
import { Badge } from "@multica/ui/components/ui/badge";
import { Button } from "@multica/ui/components/ui/button";
import { Switch } from "@multica/ui/components/ui/switch";
import { Textarea } from "@multica/ui/components/ui/textarea";
import {
  ResizableHandle,
  ResizablePanel,
  ResizablePanelGroup,
} from "@multica/ui/components/ui/resizable";
import { useIsCompact } from "@multica/ui/hooks/use-mobile";
import { cn } from "@multica/ui/lib/utils";
import {
  agentSceneMemoryKeys,
  agentSceneRelationKeys,
  agentSceneMemoryOptions,
  agentSceneRelationOptions,
} from "@multica/core/agents";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import type { Agent, AgentSceneMemory } from "@multica/core/types";
import { AppLink } from "../../../navigation";
import { useT, useTimeAgo } from "../../../i18n";
import {
  SettingsCard,
  SettingsRow,
  SettingsSection,
} from "../../../settings/components/settings-layout";
import {
  displayMatterTitle,
  isEmptyMemoryBody,
  memoryStatusKey,
  partitionSceneMemories,
  sceneDisplayTitle,
  scenePreview,
  visibleMemorySections,
} from "./scene-memory-view";

export function SceneMemoryTab({
  agent,
  canEdit = false,
  onUpdate,
}: {
  agent: Agent;
  canEdit?: boolean;
  onUpdate: (id: string, data: Record<string, unknown>) => Promise<void>;
}) {
  const { t } = useT("agents");
  const wsId = useWorkspaceId();
  const isCompact = useIsCompact();
  const showList = canEdit && agent.scene_memory_ui_enabled === true;
  const {
    data: memories = [],
    isLoading,
    isError,
    refetch,
  } = useQuery(agentSceneMemoryOptions(wsId, agent.id, showList));
  // The selected scene's scene_id.
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const { defaultLayout, onLayoutChanged } = useDefaultLayout({
    id: "multica_agent_memory_layout",
  });

  useEffect(() => {
    if (selectedId && !memories.some((memory) => memory.scene_id === selectedId)) {
      setSelectedId(null);
    }
  }, [selectedId, memories]);

  useEffect(() => {
    const firstMemory = memories[0];
    if (!showList || selectedId || !firstMemory) {
      return;
    }
    setSelectedId(firstMemory.scene_id);
  }, [showList, selectedId, memories]);

  const selected =
    memories.find((memory) => memory.scene_id === selectedId) ?? null;

  if (!showList) {
    return (
      <div className="min-h-0 flex-1 overflow-y-auto">
        <div className="mx-auto w-full max-w-3xl p-4 sm:p-6 md:p-8">
          <MemoryFlagCard agent={agent} canEdit={canEdit} onUpdate={onUpdate} />
          <p className="mt-6 text-body text-muted-foreground">
            {t(($) => $.tab_body.memory.ui_off)}
          </p>
        </div>
      </div>
    );
  }

  const list = (
    <SceneMemoryList
      memories={memories}
      selectedId={selectedId}
      isLoading={isLoading}
      isError={isError}
      onRetry={() => void refetch()}
      onSelect={(memory) => setSelectedId(memory.scene_id)}
    />
  );

  const emptyDetail = isLoading
    ? t(($) => $.tab_body.inbound.memory_loading)
    : isError
      ? t(($) => $.tab_body.inbound.memory_load_failed)
      : memories.length === 0
        ? t(($) => $.tab_body.inbound.memory_empty)
        : t(($) => $.tab_body.inbound.memory_select_prompt);

  const detail = selected ? (
    <SceneMemoryDetail agent={agent} memory={selected} canEdit={canEdit} />
  ) : (
    <div className="flex h-full flex-col items-center justify-center gap-2 px-6 text-center text-muted-foreground">
      <Users className="h-8 w-8 text-faint-foreground" />
      <p className="text-body">{emptyDetail}</p>
    </div>
  );

  if (isCompact) {
    if (selected) {
      return (
        <div className="flex min-h-0 flex-1 flex-col">
          <div className="flex h-12 shrink-0 items-center border-b px-2">
            <Button
              variant="ghost"
              size="sm"
              onClick={() => setSelectedId(null)}
              className="gap-1.5 text-muted-foreground"
            >
              <ArrowLeft className="h-4 w-4" />
              {t(($) => $.tabs.memory)}
            </Button>
          </div>
          {detail}
        </div>
      );
    }
    return (
      <div className="flex min-h-0 flex-1 flex-col">
        <MemoryFlagBar agent={agent} canEdit={canEdit} onUpdate={onUpdate} />
        <div className="min-h-0 flex-1 overflow-y-auto">{list}</div>
      </div>
    );
  }

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <MemoryFlagBar agent={agent} canEdit={canEdit} onUpdate={onUpdate} />
      <ResizablePanelGroup
        orientation="horizontal"
        className="min-h-0 flex-1"
        defaultLayout={defaultLayout}
        onLayoutChanged={onLayoutChanged}
      >
        <ResizablePanel
          id="list"
          defaultSize={300}
          minSize={220}
          maxSize={420}
          groupResizeBehavior="preserve-pixel-size"
        >
          <div className="flex h-full flex-col border-r">
            <div className="min-h-0 flex-1 overflow-y-auto">{list}</div>
          </div>
        </ResizablePanel>
        <ResizableHandle />
        <ResizablePanel id="detail" minSize="45%">
          <div className="flex h-full min-h-0 flex-col">{detail}</div>
        </ResizablePanel>
      </ResizablePanelGroup>
    </div>
  );
}

type MemoryFlag = {
  key:
    | "scene_memory_write_enabled"
    | "scene_memory_recall_enabled"
    | "scene_memory_bootstrap_enabled"
    | "scene_memory_ui_enabled";
  enabled: boolean;
  label: string;
  fullLabel: string;
  hint: string;
};

function useMemoryFlags(agent: Agent): readonly MemoryFlag[] {
  const { t } = useT("agents");
  return [
    {
      key: "scene_memory_write_enabled",
      enabled: agent.scene_memory_write_enabled === true,
      label: t(($) => $.tab_body.memory.flag_write),
      fullLabel: t(($) => $.inspector.prop_scene_memory_write),
      hint: t(($) => $.inspector.prop_scene_memory_write_hint),
    },
    {
      key: "scene_memory_recall_enabled",
      enabled: agent.scene_memory_recall_enabled === true,
      label: t(($) => $.tab_body.memory.flag_recall),
      fullLabel: t(($) => $.inspector.prop_scene_memory_recall),
      hint: t(($) => $.inspector.prop_scene_memory_recall_hint),
    },
    {
      key: "scene_memory_bootstrap_enabled",
      enabled: agent.scene_memory_bootstrap_enabled === true,
      label: t(($) => $.tab_body.memory.flag_bootstrap),
      fullLabel: t(($) => $.inspector.prop_scene_memory_bootstrap),
      hint: t(($) => $.inspector.prop_scene_memory_bootstrap_hint),
    },
    {
      key: "scene_memory_ui_enabled",
      enabled: agent.scene_memory_ui_enabled === true,
      label: t(($) => $.tab_body.memory.flag_ui),
      fullLabel: t(($) => $.inspector.prop_scene_memory_ui),
      hint: t(($) => $.inspector.prop_scene_memory_ui_hint),
    },
  ] as const;
}

function MemoryFlagCard({
  agent,
  canEdit,
  onUpdate,
}: {
  agent: Agent;
  canEdit: boolean;
  onUpdate: (id: string, data: Record<string, unknown>) => Promise<void>;
}) {
  const { t } = useT("agents");
  const flags = useMemoryFlags(agent);
  return (
    <SettingsSection description={t(($) => $.tab_body.memory.flags_hint)}>
      <SettingsCard>
        {flags.map((flag) => (
          <SettingsRow
            key={flag.key}
            label={flag.fullLabel}
            description={flag.hint}
            align="start"
          >
            <MemoryFlagSwitch
              agentId={agent.id}
              enabled={flag.enabled}
              canEdit={canEdit}
              label={flag.label}
              onSave={(next) => onUpdate(agent.id, { [flag.key]: next })}
            />
          </SettingsRow>
        ))}
      </SettingsCard>
    </SettingsSection>
  );
}

export function MemoryFlagBar({
  agent,
  canEdit,
  onUpdate,
}: {
  agent: Agent;
  canEdit: boolean;
  onUpdate: (id: string, data: Record<string, unknown>) => Promise<void>;
}) {
  const { t } = useT("agents");
  const flags = useMemoryFlags(agent);
  return (
    <div className="flex shrink-0 flex-wrap items-center gap-x-5 gap-y-2 border-b px-4 py-2.5">
      <p className="mr-auto text-caption text-muted-foreground">
        {t(($) => $.tab_body.memory.flags_hint)}
      </p>
      {flags.map((flag) => (
        <div
          key={flag.key}
          className="flex items-center gap-2 text-caption"
          title={flag.hint}
        >
          <MemoryFlagSwitch
            agentId={agent.id}
            enabled={flag.enabled}
            canEdit={canEdit}
            label={flag.label}
            onSave={(next) => onUpdate(agent.id, { [flag.key]: next })}
          />
          <span
            className={
              flag.enabled ? "text-foreground" : "text-muted-foreground"
            }
            aria-hidden="true"
          >
            {flag.label}
          </span>
        </div>
      ))}
    </div>
  );
}

function MemoryFlagSwitch({
  agentId,
  enabled,
  canEdit,
  label,
  onSave,
}: {
  agentId: string;
  enabled: boolean;
  canEdit: boolean;
  label: string;
  onSave: (next: boolean) => Promise<void>;
}) {
  const [draft, setDraft] = useState(enabled);
  const [saving, setSaving] = useState(false);
  useEffect(() => {
    setDraft(enabled);
  }, [agentId, enabled]);
  return (
    <Switch
      size="sm"
      checked={draft}
      disabled={!canEdit || saving}
      onCheckedChange={(checked) => {
        setDraft(checked);
        setSaving(true);
        void onSave(checked)
          .catch(() => setDraft(!checked))
          .finally(() => setSaving(false));
      }}
      aria-label={label}
    />
  );
}

function SceneMemoryList({
  memories,
  selectedId,
  isLoading,
  isError,
  onRetry,
  onSelect,
}: {
  memories: AgentSceneMemory[];
  selectedId: string | null;
  isLoading: boolean;
  isError: boolean;
  onRetry: () => void;
  onSelect: (memory: AgentSceneMemory) => void;
}) {
  const { t } = useT("agents");
  const { dms, groups } = partitionSceneMemories(memories);
  if (isLoading) {
    return (
      <p className="px-4 py-6 text-caption text-muted-foreground">
        {t(($) => $.tab_body.inbound.memory_loading)}
      </p>
    );
  }
  if (isError) {
    return (
      <div className="space-y-2 px-4 py-6">
        <p className="text-caption text-muted-foreground">
          {t(($) => $.tab_body.inbound.memory_load_failed)}
        </p>
        <Button type="button" variant="outline" size="sm" onClick={onRetry}>
          {t(($) => $.tab_body.inbound.retry)}
        </Button>
      </div>
    );
  }
  if (memories.length === 0) {
    return (
      <p className="px-4 py-6 text-caption text-muted-foreground">
        {t(($) => $.tab_body.inbound.memory_empty)}
      </p>
    );
  }
  return (
    <div className="px-2 py-2">
      {dms.length > 0 ? (
        <section className="pb-2">
          <p className="px-2 pb-1 pt-2 text-caption font-medium text-muted-foreground">
            {t(($) => $.tab_body.inbound.memory_kind_dm)}
          </p>
          <ul>
            {dms.map((memory) => (
              <SceneMemoryRow
                key={memory.scene_id}
                memory={memory}
                selected={memory.scene_id === selectedId}
                onSelect={onSelect}
              />
            ))}
          </ul>
        </section>
      ) : null}
      {groups.length > 0 ? (
        <section className="pb-2">
          <p className="px-2 pb-1 pt-2 text-caption font-medium text-muted-foreground">
            {t(($) => $.tab_body.inbound.memory_kind_group)}
          </p>
          <ul>
            {groups.map((memory) => (
              <SceneMemoryRow
                key={memory.scene_id}
                memory={memory}
                selected={memory.scene_id === selectedId}
                onSelect={onSelect}
              />
            ))}
          </ul>
        </section>
      ) : null}
    </div>
  );
}

function SceneMemoryRow({
  memory,
  selected,
  onSelect,
}: {
  memory: AgentSceneMemory;
  selected: boolean;
  onSelect: (memory: AgentSceneMemory) => void;
}) {
  const { t } = useT("agents");
  const untitled =
    memory.scene_kind === "group"
      ? t(($) => $.tab_body.inbound.memory_untitled_group)
      : t(($) => $.tab_body.inbound.memory_untitled_dm);
  const title = sceneDisplayTitle(memory, untitled);
  const preview = scenePreview(memory);
  const status = memoryStatusKey(memory.status);
  const kind =
    memory.scene_kind === "group"
      ? t(($) => $.tab_body.inbound.memory_kind_group)
      : t(($) => $.tab_body.inbound.memory_kind_dm);
  return (
    <li>
      <button
        type="button"
        data-active={selected ? "true" : undefined}
        aria-current={selected ? "true" : undefined}
        aria-label={`${kind} ${title}`}
        className="flex w-full min-w-0 items-start gap-2.5 rounded-lg px-2 py-2 text-left transition-colors hover:bg-muted data-active:bg-muted data-active:font-medium data-active:text-foreground data-active:hover:bg-muted"
        onClick={() => onSelect(memory)}
      >
        <span
          aria-hidden="true"
          className="mt-0.5 flex size-8 shrink-0 items-center justify-center rounded-full bg-muted text-muted-foreground"
        >
          {memory.scene_kind === "group" ? (
            <Users className="size-3.5" />
          ) : (
            <MessageSquare className="size-3.5" />
          )}
        </span>
        <span className="min-w-0 flex-1">
          <span className="flex items-center gap-1.5">
            <span className="truncate text-body">{title}</span>
            {status !== "clean" ? (
              <Badge
                variant={
                  status === "blocked" || status === "retrying"
                    ? "destructive"
                    : "secondary"
                }
                className="shrink-0"
              >
                {t(($) => $.tab_body.inbound[`status_${status}`])}
              </Badge>
            ) : null}
          </span>
          {preview && preview !== title ? (
            <span className="mt-0.5 block truncate text-caption text-muted-foreground">
              {preview}
            </span>
          ) : null}
        </span>
      </button>
    </li>
  );
}

export function SceneMemoryDetail({
  agent,
  memory,
  canEdit,
  showTitle = true,
}: {
  agent: Agent;
  memory: AgentSceneMemory;
  canEdit: boolean;
  /** false inside a scene detail, whose own header already names the scene. */
  showTitle?: boolean;
}) {
  const { t } = useT("agents");
  const timeAgo = useTimeAgo();
  const wsId = useWorkspaceId();
  const paths = useWorkspacePaths();
  const queryClient = useQueryClient();
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState(memory.memory_text);
  const [confirm, setConfirm] = useState<"reset" | "clear" | null>(null);
  useEffect(() => {
    setDraft(memory.memory_text);
    setEditing(false);
  }, [memory.scene_id, memory.memory_revision, memory.memory_text]);
  const { data: relations = [], isLoading: relationsLoading } = useQuery(
    agentSceneRelationOptions(wsId, agent.id, memory.scene_id, true),
  );
  const save = useMutation({
    mutationFn: () =>
      api.updateAgentSceneMemory(agent.id, memory.scene_id, {
        memory_text: draft,
        expected_revision: memory.memory_revision,
      }),
    onSuccess: async () => {
      await queryClient.invalidateQueries({
        queryKey: agentSceneMemoryKeys.list(wsId, agent.id),
      });
      setEditing(false);
      toast.success(t(($) => $.tab_body.inbound.memory_saved));
    },
    onError: () => {
      toast.error(t(($) => $.tab_body.inbound.memory_save_failed));
    },
  });
  const reset = useMutation({
    mutationFn: () => api.resetAgentSceneMemory(agent.id, memory.scene_id),
    onSuccess: async () => {
      setConfirm(null);
      await Promise.all([
        queryClient.invalidateQueries({
          queryKey: agentSceneMemoryKeys.list(wsId, agent.id),
        }),
        queryClient.invalidateQueries({
          queryKey: agentSceneRelationKeys.list(wsId, agent.id, memory.scene_id),
        }),
      ]);
      toast.success(t(($) => $.tab_body.inbound.memory_reset_done));
    },
    onError: () => {
      toast.error(t(($) => $.tab_body.inbound.memory_reset_failed));
    },
  });
  const clearRelations = useMutation({
    mutationFn: () => api.clearAgentSceneRelations(agent.id, memory.scene_id),
    onSuccess: async () => {
      setConfirm(null);
      await queryClient.invalidateQueries({
        queryKey: agentSceneRelationKeys.list(wsId, agent.id, memory.scene_id),
      });
      toast.success(t(($) => $.tab_body.inbound.relations_cleared));
    },
    onError: () => {
      toast.error(t(($) => $.tab_body.inbound.relations_clear_failed));
    },
  });
  const untitled =
    memory.scene_kind === "group"
      ? t(($) => $.tab_body.inbound.memory_untitled_group)
      : t(($) => $.tab_body.inbound.memory_untitled_dm);
  const title = sceneDisplayTitle(memory, untitled);
  const kind =
    memory.scene_kind === "group"
      ? t(($) => $.tab_body.inbound.memory_kind_group)
      : t(($) => $.tab_body.inbound.memory_kind_dm);
  const status = memoryStatusKey(memory.status);
  const statusLabel =
    status === "pending"
      ? t(($) => $.tab_body.inbound.status_pending)
      : status === "running"
        ? t(($) => $.tab_body.inbound.status_running)
        : status === "retrying"
          ? t(($) => $.tab_body.inbound.status_retrying)
          : status === "blocked"
            ? t(($) => $.tab_body.inbound.status_blocked)
            : t(($) => $.tab_body.inbound.status_clean);
  const dirty = draft !== memory.memory_text;
  const sections = visibleMemorySections(
    memory.memory_text,
    memory.scene_kind,
  );
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div
        className={cn(
          "flex shrink-0 items-start justify-between gap-3 border-b px-6",
          showTitle ? "py-5" : "py-3",
        )}
      >
        <div className="min-w-0">
          {showTitle ? (
            <h1 className="text-title font-semibold text-pretty">{title}</h1>
          ) : null}
          <p
            className={cn(
              "flex flex-wrap items-center gap-2 text-caption text-muted-foreground",
              showTitle && "mt-1.5",
            )}
          >
            {showTitle ? <Badge variant="outline">{kind}</Badge> : null}
            <span>{statusLabel}</span>
            {memory.updated_at ? <span>{timeAgo(memory.updated_at)}</span> : null}
          </p>
          {memory.last_error ? (
            <p className="mt-2 text-caption text-destructive">{memory.last_error}</p>
          ) : null}
        </div>
        {canEdit && !editing ? (
          <div className="flex shrink-0 gap-2">
            <Button
              type="button"
              size="sm"
              variant="outline"
              className="gap-1.5"
              onClick={() => setEditing(true)}
            >
              <Pencil className="size-3.5" aria-hidden="true" />
              {t(($) => $.tab_body.inbound.memory_edit)}
            </Button>
            <Button
              type="button"
              size="sm"
              variant="ghost"
              disabled={reset.isPending}
              onClick={() => setConfirm("reset")}
            >
              {t(($) => $.tab_body.inbound.memory_reset)}
            </Button>
          </div>
        ) : null}
      </div>
      <div className="min-h-0 flex-1 overflow-y-auto">
        <section className="px-6 py-6">
          {editing ? (
            <div className="space-y-3">
              <label
                className="text-caption font-medium text-muted-foreground"
                htmlFor="scene-memory-draft"
              >
                {t(($) => $.tab_body.inbound.memory_editor_label)}
              </label>
              <Textarea
                id="scene-memory-draft"
                value={draft}
                onChange={(event) => setDraft(event.target.value)}
                rows={14}
                spellCheck={false}
                className="min-h-56 text-body leading-7"
              />
              <div className="flex flex-wrap gap-2">
                <Button
                  type="button"
                  size="sm"
                  disabled={!dirty || save.isPending}
                  onClick={() => save.mutate()}
                >
                  {save.isPending
                    ? t(($) => $.tab_body.inbound.memory_saving)
                    : t(($) => $.tab_body.inbound.memory_save)}
                </Button>
                <Button
                  type="button"
                  size="sm"
                  variant="ghost"
                  disabled={save.isPending}
                  onClick={() => {
                    setDraft(memory.memory_text);
                    setEditing(false);
                  }}
                >
                  {t(($) => $.tab_body.inbound.memory_cancel)}
                </Button>
              </div>
            </div>
          ) : sections.length === 0 ? (
            <p className="text-body text-muted-foreground">
              {t(($) => $.tab_body.inbound.memory_empty_section)}
            </p>
          ) : (
            <div className="space-y-7">
              {sections.map((section) => (
                <article key={section.heading || "body"} className="space-y-2">
                  {section.heading ? (
                    <h2 className="text-caption font-medium tracking-wide text-muted-foreground">
                      {section.heading}
                    </h2>
                  ) : null}
                  {isEmptyMemoryBody(section.body) || !section.body ? (
                    <p className="text-body text-muted-foreground">
                      {t(($) => $.tab_body.inbound.memory_empty_section)}
                    </p>
                  ) : (
                    <p className="whitespace-pre-wrap text-body leading-7 text-pretty">
                      {section.body}
                    </p>
                  )}
                </article>
              ))}
            </div>
          )}
        </section>
        <section className="border-t px-6 py-6">
          <div className="mb-4 flex items-center justify-between gap-3">
            <h2 className="flex items-center gap-1.5 text-caption font-medium text-muted-foreground">
              <Link2 className="size-3.5" aria-hidden="true" />
              {t(($) => $.tab_body.inbound.relations_title)}
            </h2>
            {canEdit && relations.length > 0 ? (
              <Button
                type="button"
                size="sm"
                variant="ghost"
                disabled={clearRelations.isPending}
                onClick={() => setConfirm("clear")}
              >
                {t(($) => $.tab_body.inbound.relations_clear)}
              </Button>
            ) : null}
          </div>
          {relationsLoading ? (
            <p className="text-caption text-muted-foreground">
              {t(($) => $.tab_body.inbound.relations_loading)}
            </p>
          ) : relations.length === 0 ? (
            <p className="text-body text-muted-foreground">
              {t(($) => $.tab_body.inbound.relations_empty)}
            </p>
          ) : (
            <ul className="space-y-1">
              {relations.map((item) => {
                const issueId = item.issue_id || item.issue;
                return (
                  <li key={issueId || item.purpose} className="px-1 py-2">
                    {issueId ? (
                      <AppLink
                        href={paths.issueDetail(issueId)}
                        className="text-body font-medium text-brand hover:underline"
                      >
                        {displayMatterTitle(item.purpose, issueId)}
                      </AppLink>
                    ) : (
                      <p className="text-body font-medium">
                        {displayMatterTitle(
                          item.purpose,
                          t(($) => $.tab_body.inbound.relations_untitled),
                        )}
                      </p>
                    )}
                    <p className="mt-0.5 text-caption text-muted-foreground">
                      {item.status}
                      {item.on_this_scene
                        ? ` · ${t(($) => $.tab_body.inbound.relations_on_scene)}`
                        : ""}
                    </p>
                  </li>
                );
              })}
            </ul>
          )}
        </section>
      </div>
      <AlertDialog
        open={confirm !== null}
        onOpenChange={(open) => {
          if (!open) setConfirm(null);
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {confirm === "clear"
                ? t(($) => $.tab_body.inbound.relations_clear)
                : t(($) => $.tab_body.inbound.memory_reset)}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {confirm === "clear"
                ? t(($) => $.tab_body.inbound.relations_clear_confirm)
                : t(($) => $.tab_body.inbound.memory_reset_confirm)}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>
              {t(($) => $.tab_body.inbound.memory_cancel)}
            </AlertDialogCancel>
            <AlertDialogAction
              onClick={(event) => {
                event.preventDefault();
                if (confirm === "clear") {
                  clearRelations.mutate();
                } else {
                  reset.mutate();
                }
              }}
            >
              {confirm === "clear"
                ? t(($) => $.tab_body.inbound.relations_clear)
                : t(($) => $.tab_body.inbound.memory_reset)}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}
