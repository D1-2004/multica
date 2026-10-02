"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import { ArrowDown, ArrowUp, Loader2, Lock, Pencil, Plus, Trash2 } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import { errorCode } from "@multica/core/api";
import {
  contextNodeOptions,
  useDeleteContextNodeCredential,
  useRevokeContextNodeGrants,
  useSetContextNodeBinding,
  useSetContextNodeCredential,
  useSetContextNodeMcpConfig,
  useSetContextNodePrompts,
  useStartContextNodeConnection,
  type ContextEffectiveLayer,
  type ContextLayer,
  type ContextNodeConnector,
  type ContextNodeDetail,
  type ContextNodeRef,
  type ContextNodeScopeType,
  type ContextPromptComponent,
  type ContextResourceType,
  type ContextSceneKind,
  type ContextScopeRights,
} from "@multica/core/context-capabilities";
import { Badge } from "@multica/ui/components/ui/badge";
import { Button } from "@multica/ui/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import { Input } from "@multica/ui/components/ui/input";
import { Switch } from "@multica/ui/components/ui/switch";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { cn } from "@multica/ui/lib/utils";
import { AtlassianDomainNote } from "../../../common/atlassian-domain-note";
import { ConnectorLogo } from "../../../common/connector-logo";
import { useResetOnBackForwardRestore } from "../../../common/connector-credential";
import {
  PROMPT_COMPONENT_MAX,
  PROMPT_NAME_MAX_LENGTH,
  promptProblem,
  usePromptProblemMessage,
} from "../../../common/context-prompt-rules";
import { SkillIcon } from "../../../skills/lib/skill-icon";
import { useT } from "../../../i18n";
import {
  AppTile,
  AppTileGrid,
  ConfirmDialog,
  ConnectorNotice,
  DialogSection,
  InstallLink,
  SectionHeading,
  StatusPill,
  SwitchRow,
  TokenForm,
  errorMessage,
} from "./connectors-ui";
import { McpServerList } from "./mcp-config-tab";
import {
  listManagedMcpServers,
  removeManagedMcpServer,
  setManagedMcpServerEnabled,
  upsertManagedMcpServer,
  type ManagedMcpServer,
} from "./mcp-config-model";
import { McpServerDialog } from "./mcp-server-dialog";

export {
  PROMPT_COMPONENT_MAX,
  PROMPT_NAME_MAX_LENGTH,
  PROMPT_TEXT_MAX_LENGTH,
} from "../../../common/context-prompt-rules";

/** How an account connect leaves the page and comes back. Platform plumbing
 * injected by the host page (agent detail on web and desktop, or the
 * configuration page). */
export interface ContextBuilderConnect {
  /** Path the provider sign-in returns to for the app `slug`. */
  returnPath: (slug: string) => string;
  /** Leaves the page for the provider's authorization URL. */
  navigate: (url: string) => void;
  /** Runs the connect elsewhere instead (desktop opens the web page in the
   * system browser); true when it took over. */
  handOff?: (returnPath: string) => boolean;
}

/** Localized layer names: 全局 / 企业 / 群聊 (单聊 for a 1:1 chat scene) /
 * 个人; a level this build does not know shows its raw name. */
export function useLayerLabel(sceneKind: ContextSceneKind = "group"): (layer: ContextEffectiveLayer) => string {
  const { t } = useT("agents");
  return (layer) => {
    switch (layer) {
      case "global":
        return t(($) => $.tab_body.context_builder.layer_global);
      case "org":
        return t(($) => $.tab_body.context_builder.layer_org);
      case "scene":
        return sceneKind === "dm"
          ? t(($) => $.context_config.kind_dm)
          : t(($) => $.tab_body.context_builder.layer_scene);
      case "person":
        return t(($) => $.tab_body.context_builder.layer_person);
      default:
        return String(layer);
    }
  };
}

/** Kind of a scene node's chat. A 1:1 chat is a scene of its own (keyed by
 * its scene_id), named so only on the node's own word; a person node's chat
 * never makes the node a 1:1 chat. */
function nodeSceneKind(node: ContextNodeRef, detail: ContextNodeDetail): ContextSceneKind {
  return node.scopeType === "scene" && detail.scene?.kind === "dm" ? "dm" : "group";
}

/** What the caller may change at a level. The server's rights decide; a
 * server that does not send them leaves a person level to its person (the
 * one who may connect accounts there) and the others to the agent's
 * managers. */
export function contextLevelRights(detail: ContextNodeDetail, canEdit: boolean): ContextScopeRights {
  if (detail.rights) return detail.rights;
  const canConnect = detail.canConnect === true;
  const allowed = canEdit && (detail.scope?.type !== "person" || canConnect);
  // A server without rights has no routine routes either.
  return { toggle: allowed, connect: canConnect, editPrompts: allowed, editMcp: allowed, editRoutines: false };
}

/**
 * Context Builder of one level (企业, a scene — 群聊 or 单聊 — or 个人): its
 * prompt components, its MCP (offered Aone FaaS connectors and its own
 * custom MCP servers), its 连接应用 (offered official apps, each in a
 * dialog) and its skills, then the 生效预览 of what a run there gets. A
 * lower level's prompt or custom MCP server replaces an upper one with the
 * same name; connectors and skills add up. Platform-free: the host injects
 * the connect plumbing and where the open app dialog lives.
 */
export function ContextBuilderPanel({
  wsId,
  agentId,
  node,
  canEdit,
  connect,
  openApp,
  onOpenAppChange,
  onDirtyChange,
  footer,
  tagFraming = false,
}: {
  wsId: string;
  agentId: string;
  node: ContextNodeRef;
  canEdit: boolean;
  /** A Tag tenant's scene: show where this level sits under the Tag's default
   * access bundle, and frame its skills, connectors and credentials as the
   * access bundle of this level. */
  tagFraming?: boolean;
  connect: ContextBuilderConnect;
  /** Catalog slug of the open app dialog, "" when closed. */
  openApp: string;
  onOpenAppChange: (slug: string) => void;
  onDirtyChange?: (dirty: boolean) => void;
  footer?: React.ReactNode;
}) {
  const { t } = useT("agents");
  const query = useQuery(contextNodeOptions(wsId, agentId, node));
  const detail = query.data ?? null;

  if (query.isLoading) {
    return (
      <ConnectorNotice loading>{t(($) => $.tab_body.context_builder.loading)}</ConnectorNotice>
    );
  }
  if (!detail) {
    return (
      <ConnectorNotice>
        <span>{t(($) => $.tab_body.context_builder.load_failed)}</span>
        <Button size="sm" variant="outline" onClick={() => void query.refetch()}>
          {t(($) => $.tab_body.scenes.retry)}
        </Button>
      </ConnectorNotice>
    );
  }
  const rights = contextLevelRights(detail, canEdit);
  const sceneKind = nodeSceneKind(node, detail);
  const capabilities = (
    <NodeCapabilities
      wsId={wsId}
      agentId={agentId}
      node={node}
      detail={detail}
      rights={rights}
      connect={connect}
      openApp={openApp}
      onOpenAppChange={onOpenAppChange}
    />
  );
  return (
    <div className="space-y-8">
      {tagFraming ? <TagLevelPath level={detail.scope?.type ?? node.scopeType} sceneKind={sceneKind} /> : null}
      <PromptSection
        // A new level starts with a fresh draft.
        key={`${node.orgId}/${node.scopeType}/${node.scopeKey}`}
        wsId={wsId}
        agentId={agentId}
        node={node}
        detail={detail}
        canEdit={rights.editPrompts}
        onDirtyChange={onDirtyChange}
      />
      {tagFraming ? (
        <section className="space-y-4 rounded-xl border px-4 py-4">
          <header>
            <h3 className="text-body font-semibold">{t(($) => $.tag_tenant.level_bundle_title)}</h3>
            <p className="text-caption text-muted-foreground">{t(($) => $.tag_tenant.level_bundle_hint)}</p>
          </header>
          {capabilities}
        </section>
      ) : (
        capabilities
      )}
      <EffectiveSection detail={detail} sceneKind={sceneKind} />
      {canEdit ? <AccessSection wsId={wsId} agentId={agentId} node={node} detail={detail} /> : null}
      {footer}
    </div>
  );
}

/** Tag 默认能力包 → 企业 → 群聊 / 单聊 / 个人, with this level marked: each
 * level inherits the one before and adds its own prompt and access bundle. */
function TagLevelPath({ level, sceneKind }: { level: string; sceneKind: ContextSceneKind }) {
  const { t } = useT("agents");
  const leaf =
    level === "person"
      ? t(($) => $.tag_tenant.level_person)
      : sceneKind === "dm"
        ? t(($) => $.context_config.kind_dm)
        : t(($) => $.tag_tenant.level_scene);
  const steps: { id: string; label: string }[] = [
    { id: "tag", label: t(($) => $.tag_tenant.level_tag) },
    { id: "org", label: t(($) => $.tag_tenant.level_org) },
    { id: "leaf", label: leaf },
  ];
  const current = level === "org" ? "org" : "leaf";
  const visible = current === "org" ? steps.slice(0, 2) : steps;
  return (
    <nav aria-label={t(($) => $.tag_tenant.level_aria)} className="flex flex-wrap items-center gap-1.5 text-caption">
      {visible.map((step, index) => (
        <span key={step.id} className="flex items-center gap-1.5">
          {index > 0 ? <span aria-hidden="true" className="text-muted-foreground">→</span> : null}
          <span
            aria-current={step.id === current ? "step" : undefined}
            className={cn(
              "rounded-full border px-2 py-0.5",
              step.id === current ? "border-foreground font-medium text-foreground" : "text-muted-foreground",
            )}
          >
            {step.label}
          </span>
        </span>
      ))}
    </nav>
  );
}

// ---------------------------------------------------------------------------
// Configure-page access
// ---------------------------------------------------------------------------

/** Who may configure a scene or person level from the DingTalk configure
 * page (configuration links, the group picker), and the manager's revoke:
 * a person whose forwarded link handed their scope to someone else gets it
 * back by asking for a new personal link. */
function AccessSection({
  wsId,
  agentId,
  node,
  detail,
}: {
  wsId: string;
  agentId: string;
  node: ContextNodeRef;
  detail: ContextNodeDetail;
}) {
  const { t } = useT("agents");
  const revoke = useRevokeContextNodeGrants(wsId, agentId);
  const [confirming, setConfirming] = useState(false);
  const level = detail.scope?.type ?? node.scopeType;
  if (level !== "scene" && level !== "person") return null;
  const titleId = `context-access-${node.scopeType}-${node.scopeKey}`;
  const doRevoke = async () => {
    try {
      const revoked = await revoke.mutateAsync(node);
      setConfirming(false);
      toast.success(t(($) => $.tab_body.context_builder.access_revoked, { count: revoked ?? 0 }));
    } catch (error) {
      toast.error(errorMessage(error, t(($) => $.tab_body.context_builder.access_revoke_failed)));
    }
  };
  return (
    <section className="space-y-3" aria-labelledby={titleId}>
      <SectionHeading
        id={titleId}
        level={3}
        title={t(($) => $.tab_body.context_builder.access_title)}
        action={
          <Button size="sm" variant="outline" onClick={() => setConfirming(true)}>
            {t(($) => $.tab_body.context_builder.access_revoke)}
          </Button>
        }
      />
      <p className="text-caption text-muted-foreground text-pretty">
        {t(($) => $.tab_body.context_builder.access_description)}
      </p>
      <ConfirmDialog
        open={confirming}
        onOpenChange={setConfirming}
        title={t(($) => $.tab_body.context_builder.access_revoke_title)}
        description={
          level === "person"
            ? t(($) => $.tab_body.context_builder.access_revoke_person)
            : nodeSceneKind(node, detail) === "dm"
              ? t(($) => $.tab_body.context_builder.access_revoke_dm)
              : t(($) => $.tab_body.context_builder.access_revoke_scene)
        }
        confirmLabel={t(($) => $.tab_body.context_builder.access_revoke)}
        pending={revoke.isPending}
        onConfirm={() => void doRevoke()}
      />
    </section>
  );
}

// ---------------------------------------------------------------------------
// Prompt components
// ---------------------------------------------------------------------------

interface PromptDraft {
  /** Local identity, stable across reorders. */
  key: string;
  name: string;
  text: string;
  /** Off: the component takes no part in the merge. */
  enabled: boolean;
}

function draftsOf(prompts: ContextPromptComponent[]): PromptDraft[] {
  return prompts.map((prompt, index) => ({
    key: prompt.id || `${prompt.name}#${index}`,
    name: prompt.name,
    text: prompt.text,
    enabled: prompt.enabled !== false,
  }));
}

function sameDrafts(a: PromptDraft[], b: PromptDraft[]): boolean {
  return (
    a.length === b.length &&
    a.every(
      (item, index) =>
        item.name === b[index]?.name && item.text === b[index]?.text && item.enabled === b[index]?.enabled,
    )
  );
}

function PromptSection({
  wsId,
  agentId,
  node,
  detail,
  canEdit,
  onDirtyChange,
}: {
  wsId: string;
  agentId: string;
  node: ContextNodeRef;
  detail: ContextNodeDetail;
  canEdit: boolean;
  onDirtyChange?: (dirty: boolean) => void;
}) {
  const { t } = useT("agents");
  const save = useSetContextNodePrompts(wsId, agentId);
  const stored = useMemo(() => draftsOf(detail.prompts), [detail.prompts]);
  const [draft, setDraft] = useState<PromptDraft[]>(stored);
  // The stored list the draft started from. When a save (this one or another
  // admin's, via a refetch) changes it, a draft without edits follows it; a
  // draft with unsaved edits is kept.
  const [baseline, setBaseline] = useState(stored);
  if (!sameDrafts(stored, baseline)) {
    setBaseline(stored);
    if (sameDrafts(draft, baseline)) setDraft(stored);
  }
  const dirty = !sameDrafts(draft, stored);
  const [editing, setEditing] = useState<PromptDraft | "new" | null>(null);
  const addedCount = useRef(0);
  const titleId = `context-prompts-${node.scopeType}-${node.scopeKey}`;
  const level: ContextLayer = detail.scope?.type ?? node.scopeType;
  const editingKey = editing && editing !== "new" ? editing.key : null;
  // Names of upper-layer prompts: reusing one replaces it at this level.
  const upperNames = useMemo(
    () => [
      ...new Set(
        detail.effective.prompts.filter((prompt) => prompt.layer !== level).map((prompt) => prompt.name),
      ),
    ],
    [detail.effective.prompts, level],
  );

  useEffect(() => {
    onDirtyChange?.(dirty);
  }, [dirty, onDirtyChange]);
  useEffect(() => () => onDirtyChange?.(false), [onDirtyChange]);

  const move = (index: number, delta: number) =>
    setDraft((current) => {
      const next = [...current];
      const target = index + delta;
      if (target < 0 || target >= next.length) return current;
      const [item] = next.splice(index, 1);
      if (item) next.splice(target, 0, item);
      return next;
    });

  const submit = async () => {
    if (!dirty) return;
    const submitted = draft;
    try {
      await save.mutateAsync({
        node,
        prompts: submitted.map((item, index) => ({
          name: item.name,
          order: index + 1,
          text: item.text,
          enabled: item.enabled,
        })),
      });
      // The saved list is the new baseline: the next stored list (the echo
      // or a refetch) replaces the draft unless it was edited meanwhile.
      setBaseline(submitted);
      toast.success(t(($) => $.tab_body.context_builder.prompts_saved));
    } catch (error) {
      toast.error(errorMessage(error, t(($) => $.tab_body.context_builder.prompts_save_failed)));
    }
  };

  const commitDialog = (name: string, text: string) => {
    if (editing === "new") {
      addedCount.current += 1;
      const key = `new-${addedCount.current}`;
      setDraft((current) => [...current, { key, name, text, enabled: true }]);
    } else if (editingKey) {
      setDraft((current) => current.map((item) => (item.key === editingKey ? { ...item, name, text } : item)));
    }
    setEditing(null);
  };

  return (
    <section className="space-y-3" aria-labelledby={titleId}>
      <SectionHeading
        id={titleId}
        level={3}
        title={t(($) => $.tab_body.context_builder.prompts_title)}
        action={
          canEdit && draft.length < PROMPT_COMPONENT_MAX ? (
            <Button size="sm" variant="ghost" onClick={() => setEditing("new")}>
              <Plus aria-hidden="true" />
              {t(($) => $.tab_body.context_builder.prompt_add)}
            </Button>
          ) : null
        }
      />
      {draft.length === 0 ? (
        <p className="text-caption text-muted-foreground">{t(($) => $.tab_body.context_builder.none)}</p>
      ) : (
        <ol className="divide-y rounded-lg border bg-card">
          {draft.map((item, index) => (
            <li key={item.key} className="flex items-start gap-3 px-3 py-2.5" aria-label={item.name}>
              <span className="mt-0.5 w-5 shrink-0 text-right text-caption tabular-nums text-muted-foreground">
                {index + 1}
              </span>
              <div className={cn("min-w-0 flex-1", !item.enabled && "opacity-60")}>
                <p className="truncate text-body font-medium">{item.name}</p>
                <p className="line-clamp-2 whitespace-pre-wrap break-words text-caption text-muted-foreground">
                  {item.text}
                </p>
              </div>
              <Switch
                size="sm"
                className="mt-1"
                checked={item.enabled}
                disabled={!canEdit}
                aria-label={t(($) => $.tab_body.context_builder.toggle_aria, { name: item.name })}
                onCheckedChange={(enabled) =>
                  setDraft((current) => current.map((entry) => (entry.key === item.key ? { ...entry, enabled } : entry)))
                }
              />
              {canEdit ? (
                <div className="flex shrink-0 items-center">
                  <Button
                    type="button"
                    variant="ghost"
                    size="icon-sm"
                    disabled={index === 0}
                    aria-label={t(($) => $.tab_body.context_builder.prompt_move_up, { name: item.name })}
                    onClick={() => move(index, -1)}
                  >
                    <ArrowUp aria-hidden="true" />
                  </Button>
                  <Button
                    type="button"
                    variant="ghost"
                    size="icon-sm"
                    disabled={index === draft.length - 1}
                    aria-label={t(($) => $.tab_body.context_builder.prompt_move_down, { name: item.name })}
                    onClick={() => move(index, 1)}
                  >
                    <ArrowDown aria-hidden="true" />
                  </Button>
                  <Button
                    type="button"
                    variant="ghost"
                    size="icon-sm"
                    aria-label={t(($) => $.tab_body.context_builder.prompt_edit, { name: item.name })}
                    onClick={() => setEditing(item)}
                  >
                    <Pencil aria-hidden="true" />
                  </Button>
                  <Button
                    type="button"
                    variant="ghost"
                    size="icon-sm"
                    aria-label={t(($) => $.tab_body.context_builder.prompt_delete, { name: item.name })}
                    onClick={() => setDraft((current) => current.filter((entry) => entry.key !== item.key))}
                  >
                    <Trash2 aria-hidden="true" />
                  </Button>
                </div>
              ) : null}
            </li>
          ))}
        </ol>
      )}
      {canEdit && dirty ? (
        <div className="flex justify-end gap-2">
          <Button size="sm" variant="ghost" disabled={save.isPending} onClick={() => setDraft(stored)}>
            {t(($) => $.tab_body.context_builder.discard)}
          </Button>
          <Button size="sm" disabled={save.isPending} onClick={() => void submit()}>
            {save.isPending && (
              <Loader2 className="size-3.5 animate-spin motion-reduce:animate-none" aria-hidden="true" />
            )}
            {t(($) => $.tab_body.context_builder.save)}
          </Button>
        </div>
      ) : null}
      {canEdit ? (
        <PromptDialog
          open={editing !== null}
          prompt={editing === "new" ? null : editing}
          otherNames={new Set(draft.filter((item) => item.key !== editingKey).map((item) => item.name))}
          suggestions={upperNames}
          onOpenChange={(open) => {
            if (!open) setEditing(null);
          }}
          onSubmit={commitDialog}
        />
      ) : null}
    </section>
  );
}

function PromptDialog({
  open,
  prompt,
  otherNames,
  suggestions,
  onOpenChange,
  onSubmit,
}: {
  open: boolean;
  /** The component being edited; null adds one. */
  prompt: PromptDraft | null;
  /** Names already used by the level's other components. */
  otherNames: ReadonlySet<string>;
  /** Upper-layer names; reusing one replaces it here. */
  suggestions: string[];
  onOpenChange: (open: boolean) => void;
  onSubmit: (name: string, text: string) => void;
}) {
  const { t } = useT("agents");
  const problemMessage = usePromptProblemMessage();
  const [name, setName] = useState("");
  const [text, setText] = useState("");
  const [touched, setTouched] = useState(false);

  useEffect(() => {
    if (!open) return;
    setName(prompt?.name ?? "");
    setText(prompt?.text ?? "");
    setTouched(false);
  }, [open, prompt]);

  const trimmedName = name.trim();
  const trimmedText = text.trim();
  // The server's rules: lengths in characters (code points), no control
  // characters in a name, no NUL in the content.
  const problem = promptProblem(trimmedName, trimmedText, otherNames);
  const error = problem ? problemMessage(problem) : "";
  const listId = "context-prompt-name-suggestions";

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>
            {prompt
              ? t(($) => $.tab_body.context_builder.prompt_dialog_edit)
              : t(($) => $.tab_body.context_builder.prompt_dialog_add)}
          </DialogTitle>
        </DialogHeader>
        <form
          className="space-y-3"
          onSubmit={(event) => {
            event.preventDefault();
            setTouched(true);
            if (error) return;
            onSubmit(trimmedName, trimmedText);
          }}
        >
          <div className="space-y-1.5">
            <label htmlFor="context-prompt-name" className="text-label font-medium">
              {t(($) => $.tab_body.context_builder.prompt_name)}
            </label>
            <Input
              id="context-prompt-name"
              value={name}
              maxLength={PROMPT_NAME_MAX_LENGTH}
              list={suggestions.length > 0 ? listId : undefined}
              autoComplete="off"
              onChange={(event) => setName(event.target.value)}
            />
            {suggestions.length > 0 ? (
              <datalist id={listId}>
                {suggestions.map((suggestion) => (
                  <option key={suggestion} value={suggestion} />
                ))}
              </datalist>
            ) : null}
          </div>
          <div className="space-y-1.5">
            <label htmlFor="context-prompt-text" className="text-label font-medium">
              {t(($) => $.tab_body.context_builder.prompt_text)}
            </label>
            <Textarea
              id="context-prompt-text"
              value={text}
              rows={8}
              onChange={(event) => setText(event.target.value)}
              className="min-h-40 text-body leading-6"
            />
          </div>
          {touched && error ? (
            <p role="alert" className="text-caption text-destructive">
              {error}
            </p>
          ) : null}
          <DialogFooter>
            <Button type="button" variant="ghost" onClick={() => onOpenChange(false)}>
              {t(($) => $.tab_body.connectors.cancel)}
            </Button>
            <Button type="submit">{t(($) => $.tab_body.context_builder.prompt_done)}</Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

// ---------------------------------------------------------------------------
// Switches shared by the MCP rows, the app dialog and the skills
// ---------------------------------------------------------------------------

interface NodeBindings {
  isEnabled: (resourceType: ContextResourceType, resourceId: string) => boolean;
  isBusy: (resourceType: ContextResourceType, resourceId: string) => boolean;
  toggle: (resourceType: ContextResourceType, resourceId: string, enabled: boolean) => void;
}

function bindingKey(resourceType: ContextResourceType, resourceId: string): string {
  return `${resourceType}:${resourceId}`;
}

/** The level's switches. Not optimistic: the server gates each write on the
 * offer catalog. One pending entry per row, so overlapping toggles of
 * different rows never clear each other's state. */
function useNodeBindings(wsId: string, agentId: string, node: ContextNodeRef, detail: ContextNodeDetail): NodeBindings {
  const { t } = useT("agents");
  const setBinding = useSetContextNodeBinding(wsId, agentId);
  const [busyKeys, setBusyKeys] = useState<ReadonlySet<string>>(() => new Set());
  const enabledKeys = useMemo(
    () =>
      new Set([
        ...detail.connectors
          .filter((connector) => connector.enabled === true)
          .map((connector) => bindingKey("connector", connector.id)),
        ...detail.skills.filter((skill) => skill.enabled === true).map((skill) => bindingKey("skill", skill.id)),
      ]),
    [detail.connectors, detail.skills],
  );

  const toggle = async (resourceType: ContextResourceType, resourceId: string, enabled: boolean) => {
    const key = bindingKey(resourceType, resourceId);
    if (busyKeys.has(key)) return;
    setBusyKeys((current) => new Set(current).add(key));
    try {
      await setBinding.mutateAsync({ node, resourceType, resourceId, enabled });
    } catch (error) {
      toast.error(errorMessage(error, t(($) => $.tab_body.context_builder.toggle_failed)));
    } finally {
      setBusyKeys((current) => {
        const next = new Set(current);
        next.delete(key);
        return next;
      });
    }
  };

  return {
    isEnabled: (resourceType, resourceId) => enabledKeys.has(bindingKey(resourceType, resourceId)),
    isBusy: (resourceType, resourceId) => busyKeys.has(bindingKey(resourceType, resourceId)),
    toggle: (resourceType, resourceId, enabled) => void toggle(resourceType, resourceId, enabled),
  };
}

function BindingSwitch({
  bindings,
  resourceType,
  resourceId,
  name,
  canEdit,
}: {
  bindings: NodeBindings;
  resourceType: ContextResourceType;
  resourceId: string;
  name: string;
  canEdit: boolean;
}) {
  const { t } = useT("agents");
  return (
    <span className="flex h-6 w-10 shrink-0 items-center justify-end">
      {bindings.isBusy(resourceType, resourceId) ? (
        <Loader2 className="size-4 animate-spin text-muted-foreground motion-reduce:animate-none" aria-hidden="true" />
      ) : (
        <Switch
          checked={bindings.isEnabled(resourceType, resourceId)}
          disabled={!canEdit}
          onCheckedChange={(next) => bindings.toggle(resourceType, resourceId, next)}
          aria-label={t(($) => $.tab_body.context_builder.toggle_aria, { name })}
        />
      )}
    </span>
  );
}

/** Label of the level's switch in an app dialog. */
function useEnableLabel(type: ContextNodeScopeType, sceneKind: ContextSceneKind): string {
  const { t } = useT("agents");
  switch (type) {
    case "org":
      return t(($) => $.tab_body.context_builder.enable_org);
    case "person":
      return t(($) => $.tab_body.context_builder.enable_person);
    default:
      return sceneKind === "dm"
        ? t(($) => $.tab_body.context_builder.enable_dm)
        : t(($) => $.tab_body.context_builder.enable_scene);
  }
}

/** Who connects accounts when this caller may not: a person connects their
 * own on the configuration page. */
function useConnectNote(detail: ContextNodeDetail): string {
  const { t } = useT("agents");
  return detail.scope?.type === "person"
    ? t(($) => $.tab_body.context_builder.owner_connects)
    : t(($) => $.tab_body.context_builder.configure_connects);
}

interface NodeContext {
  wsId: string;
  agentId: string;
  node: ContextNodeRef;
  detail: ContextNodeDetail;
  /** May switch connectors and skills here. */
  canEdit: boolean;
  /** May store, remove or connect accounts here. */
  canConnect: boolean;
  /** May change the level's own MCP servers. */
  canEditMcp: boolean;
  connect: ContextBuilderConnect;
  bindings: NodeBindings;
}

function NodeCapabilities({
  wsId,
  agentId,
  node,
  detail,
  rights,
  connect,
  openApp,
  onOpenAppChange,
}: {
  wsId: string;
  agentId: string;
  node: ContextNodeRef;
  detail: ContextNodeDetail;
  rights: ContextScopeRights;
  connect: ContextBuilderConnect;
  openApp: string;
  onOpenAppChange: (slug: string) => void;
}) {
  const bindings = useNodeBindings(wsId, agentId, node, detail);
  const context: NodeContext = {
    wsId,
    agentId,
    node,
    detail,
    canEdit: rights.toggle,
    canConnect: rights.connect,
    canEditMcp: rights.editMcp,
    connect,
    bindings,
  };
  return (
    <>
      <McpSection context={context} />
      <AppsSection context={context} openApp={openApp} onOpenAppChange={onOpenAppChange} />
      <SkillsSection context={context} />
    </>
  );
}

// ---------------------------------------------------------------------------
// MCP: offered Aone FaaS connectors and the level's own MCP servers
// ---------------------------------------------------------------------------

function McpSection({ context }: { context: NodeContext }) {
  const { t } = useT("agents");
  const { detail, node } = context;
  const connectors = detail.connectors.filter((connector) => connector.catalogSlug === "");
  const connectNote = useConnectNote(detail);
  const titleId = `context-mcp-${node.scopeType}-${node.scopeKey}`;
  return (
    <section className="space-y-3" aria-labelledby={titleId}>
      <SectionHeading id={titleId} level={3} title={t(($) => $.tab_body.context_builder.mcp_title)} />
      {connectors.length > 0 ? (
        <ul className="divide-y rounded-lg border bg-card">
          {connectors.map((connector) => (
            <ConnectorRow key={connector.id} context={context} connector={connector} connectNote={connectNote} />
          ))}
        </ul>
      ) : null}
      <CustomMcpServers context={context} />
    </section>
  );
}

function CredentialPills({ connector, showCredential }: { connector: ContextNodeConnector; showCredential: boolean }) {
  const { t } = useT("agents");
  const connected = connector.credential?.connected === true;
  const account = connector.credential?.account ?? "";
  const enabled = connector.enabled === true;
  return (
    <>
      {connector.global === true ? (
        <StatusPill tone="success">{t(($) => $.tab_body.connectors.status_enabled)}</StatusPill>
      ) : (
        <StatusPill tone={enabled ? "success" : "muted"}>
          {enabled
            ? t(($) => $.tab_body.connected_apps.usage_enabled)
            : t(($) => $.tab_body.connected_apps.usage_not_enabled)}
        </StatusPill>
      )}
      {showCredential ? (
        <StatusPill tone={connected ? "success" : "muted"}>
          {connected
            ? account
              ? t(($) => $.tab_body.connected_apps.shared_connected_as, { account })
              : t(($) => $.tab_body.connected_apps.shared_connected)
            : t(($) => $.tab_body.connected_apps.shared_none)}
        </StatusPill>
      ) : null}
    </>
  );
}

function ConnectorRow({
  context,
  connector,
  connectNote,
}: {
  context: NodeContext;
  connector: ContextNodeConnector;
  /** Shown instead of the token controls when the caller cannot connect. */
  connectNote: string;
}) {
  const { t } = useT("agents");
  const { wsId, agentId, node, canEdit, canConnect, bindings } = context;
  const saveToken = useSetContextNodeCredential(wsId, agentId);
  const removeToken = useDeleteContextNodeCredential(wsId, agentId);
  const [editing, setEditing] = useState(false);
  const [confirmRemove, setConfirmRemove] = useState(false);
  const takesToken = connector.acceptsCredential === true;
  const connected = connector.credential?.connected === true;

  const save = async (bearer: string) => {
    try {
      await saveToken.mutateAsync({ node, connectorId: connector.id, bearer });
      toast.success(t(($) => $.tab_body.context_builder.token_saved));
      setEditing(false);
    } finally {
      // Drop the submitted secret from the mutation state right away.
      saveToken.reset();
    }
  };

  const remove = async () => {
    try {
      await removeToken.mutateAsync({ node, connectorId: connector.id });
      setConfirmRemove(false);
      toast.success(t(($) => $.tab_body.context_builder.token_removed));
    } catch (error) {
      toast.error(errorMessage(error, t(($) => $.tab_body.context_builder.token_remove_failed)));
    }
  };

  return (
    <li className="space-y-2 px-3 py-2.5" aria-label={connector.name}>
      <div className="flex items-center gap-3">
        <ConnectorLogo slug="" />
        <div className="min-w-0 flex-1 space-y-1">
          <p className="truncate text-body font-medium">{connector.name}</p>
          <div className="flex flex-wrap items-center gap-1">
            <CredentialPills connector={connector} showCredential={takesToken} />
          </div>
        </div>
        {takesToken && !editing ? (
          canConnect ? (
            <Button
              size="sm"
              variant="ghost"
              className={connected ? "text-muted-foreground hover:text-destructive" : undefined}
              onClick={() => (connected ? setConfirmRemove(true) : setEditing(true))}
            >
              {connected
                ? t(($) => $.tab_body.context_builder.token_remove)
                : t(($) => $.tab_body.context_builder.token_set)}
            </Button>
          ) : (
            <span className="hidden text-caption text-muted-foreground sm:inline">{connectNote}</span>
          )
        ) : null}
        {/* A 通用能力 is on at every level; a level only gives it an account. */}
        {connector.global === true ? null : (
          <BindingSwitch
            bindings={bindings}
            resourceType="connector"
            resourceId={connector.id}
            name={connector.name}
            canEdit={canEdit}
          />
        )}
      </div>
      {takesToken && !canConnect ? (
        <p className="text-caption text-muted-foreground sm:hidden">{connectNote}</p>
      ) : null}
      {editing ? (
        <TokenForm
          inputId={`context-token-${connector.id}`}
          label={t(($) => $.tab_body.context_builder.token_label, { name: connector.name })}
          placeholder={t(($) => $.tab_body.context_builder.token_placeholder)}
          pending={saveToken.isPending}
          onSave={save}
          onCancel={() => setEditing(false)}
        />
      ) : null}
      <ConfirmDialog
        open={confirmRemove}
        onOpenChange={setConfirmRemove}
        title={t(($) => $.tab_body.context_builder.token_remove_title, { name: connector.name })}
        description={t(($) => $.tab_body.context_builder.token_remove_description)}
        confirmLabel={t(($) => $.tab_body.context_builder.token_remove)}
        pending={removeToken.isPending}
        onConfirm={() => void remove()}
      />
    </li>
  );
}

/** The level's own MCP servers (the agent `mcp_config` document shape). When
 * the workspace redacts secrets the stored document is withheld, so it is
 * shown as locked and never saved over. */
function CustomMcpServers({ context }: { context: NodeContext }) {
  const { t } = useT("agents");
  const { wsId, agentId, node, detail, canEditMcp } = context;
  const save = useSetContextNodeMcpConfig(wsId, agentId);
  const mcpConfig = detail.mcpConfig ?? null;
  const redacted = detail.mcpConfigRedacted === true;
  const editable = canEditMcp && !redacted;
  const servers = useMemo(() => listManagedMcpServers(mcpConfig), [mcpConfig]);
  const names = useMemo(() => new Set(servers.map((server) => server.name)), [servers]);
  const [editorOpen, setEditorOpen] = useState(false);
  const [editing, setEditing] = useState<ManagedMcpServer | null>(null);
  const [deleting, setDeleting] = useState<ManagedMcpServer | null>(null);
  const titleId = `context-custom-mcp-${node.scopeType}-${node.scopeKey}`;

  const saveServer = async (name: string, config: Record<string, unknown>) => {
    try {
      await save.mutateAsync({ node, mcpConfig: upsertManagedMcpServer(mcpConfig, editing, name, config) });
      toast.success(
        editing ? t(($) => $.tab_body.mcp_config.updated_toast) : t(($) => $.tab_body.mcp_config.added_toast),
      );
    } catch (error) {
      toast.error(errorMessage(error, t(($) => $.tab_body.mcp_config.save_failed_toast)));
      // Keeps the editor open with the entered values.
      throw error;
    }
  };

  const deleteServer = async (server: ManagedMcpServer) => {
    try {
      await save.mutateAsync({ node, mcpConfig: removeManagedMcpServer(mcpConfig, server) });
      setDeleting(null);
      toast.success(t(($) => $.tab_body.mcp_config.deleted_toast));
    } catch (error) {
      toast.error(errorMessage(error, t(($) => $.tab_body.mcp_config.delete_failed_toast)));
    }
  };

  // A switched-off server takes no part in the merge.
  const toggleServer = async (server: ManagedMcpServer, enabled: boolean) => {
    try {
      await save.mutateAsync({ node, mcpConfig: setManagedMcpServerEnabled(mcpConfig, server, enabled) });
    } catch (error) {
      toast.error(errorMessage(error, t(($) => $.tab_body.mcp_config.save_failed_toast)));
    }
  };

  return (
    <div className="space-y-2" role="group" aria-labelledby={titleId}>
      <div className="flex items-center justify-between gap-2">
        <h4 id={titleId} className="text-label font-medium text-muted-foreground">
          {t(($) => $.tab_body.connectors.custom_title)}
        </h4>
        {editable ? (
          <Button
            size="sm"
            variant="ghost"
            onClick={() => {
              setEditing(null);
              setEditorOpen(true);
            }}
          >
            <Plus aria-hidden="true" />
            {t(($) => $.tab_body.mcp_config.add_action)}
          </Button>
        ) : null}
      </div>
      {redacted ? (
        <p className="flex items-center gap-1.5 text-caption text-muted-foreground">
          <Lock className="size-3.5 shrink-0" aria-hidden="true" />
          {t(($) => $.tab_body.mcp_config.redacted_title)}
        </p>
      ) : servers.length > 0 ? (
        <McpServerList
          servers={servers}
          disabledLabel={t(($) => $.tab_body.mcp_config.agent_disabled_badge)}
          editLabel={t(($) => $.tab_body.mcp_config.edit_aria)}
          deleteLabel={t(($) => $.tab_body.mcp_config.delete_aria)}
          onEdit={
            editable
              ? (server) => {
                  setEditing(server);
                  setEditorOpen(true);
                }
              : undefined
          }
          onDelete={editable ? setDeleting : undefined}
          onToggle={editable ? (server, enabled) => void toggleServer(server, enabled) : undefined}
          toggleLabel={(name) => t(($) => $.tab_body.context_builder.toggle_aria, { name })}
          togglePending={save.isPending}
        />
      ) : (
        <p className="text-caption text-muted-foreground">{t(($) => $.tab_body.context_builder.none)}</p>
      )}
      {editable ? (
        <McpServerDialog
          open={editorOpen}
          server={editing}
          existingNames={names}
          onOpenChange={setEditorOpen}
          onSave={saveServer}
        />
      ) : null}
      <ConfirmDialog
        open={deleting !== null}
        onOpenChange={(open) => {
          if (!open) setDeleting(null);
        }}
        title={t(($) => $.tab_body.mcp_config.delete_dialog_title)}
        description={t(($) => $.tab_body.context_builder.mcp_delete_description, { name: deleting?.name ?? "" })}
        confirmLabel={t(($) => $.tab_body.mcp_config.delete_action)}
        pending={save.isPending}
        onConfirm={() => {
          if (deleting) void deleteServer(deleting);
        }}
      />
    </div>
  );
}

// ---------------------------------------------------------------------------
// 连接应用: offered official apps, switched on and connected at this level
// ---------------------------------------------------------------------------

function AppsSection({
  context,
  openApp,
  onOpenAppChange,
}: {
  context: NodeContext;
  openApp: string;
  onOpenAppChange: (slug: string) => void;
}) {
  const { t } = useT("agents");
  const { detail, node, bindings } = context;
  const apps = detail.connectors.filter((connector) => connector.catalogSlug !== "");
  const shownApp = apps.find((app) => app.catalogSlug === openApp) ?? null;
  const titleId = `context-apps-${node.scopeType}-${node.scopeKey}`;

  return (
    <section className="space-y-3" aria-labelledby={titleId}>
      <SectionHeading id={titleId} level={3} title={t(($) => $.tab_body.connected_apps.title)} />
      {apps.length === 0 ? (
        <p className="text-caption text-muted-foreground">{t(($) => $.tab_body.context_builder.none)}</p>
      ) : (
        <AppTileGrid label={t(($) => $.tab_body.connected_apps.title)}>
          {apps.map((app) => {
            const common = app.global === true;
            const enabled = bindings.isEnabled("connector", app.id);
            const connected = app.credential?.connected === true;
            return (
              <AppTile
                key={app.id}
                slug={app.catalogSlug}
                name={app.name}
                ariaLabel={t(($) => $.tab_body.connected_apps.card_aria, { name: app.name })}
                onOpen={() => onOpenAppChange(app.catalogSlug)}
              >
                {common ? (
                  <StatusPill tone="success">{t(($) => $.tab_body.connectors.status_enabled)}</StatusPill>
                ) : enabled ? (
                  <StatusPill tone="success">{t(($) => $.tab_body.connected_apps.usage_enabled)}</StatusPill>
                ) : null}
                {connected ? (
                  <StatusPill tone="success">
                    {app.credential.account
                      ? t(($) => $.tab_body.connected_apps.shared_connected_as, { account: app.credential.account })
                      : t(($) => $.tab_body.connected_apps.shared_connected)}
                  </StatusPill>
                ) : null}
                {!common && !enabled && !connected ? (
                  <StatusPill tone="muted">{t(($) => $.tab_body.connected_apps.usage_not_enabled)}</StatusPill>
                ) : null}
              </AppTile>
            );
          })}
        </AppTileGrid>
      )}
      <AppDialog context={context} app={shownApp} onClose={() => onOpenAppChange("")} />
    </section>
  );
}

function AppDialog({
  context,
  app,
  onClose,
}: {
  context: NodeContext;
  /** The open app; null when closed. */
  app: ContextNodeConnector | null;
  onClose: () => void;
}) {
  const { t } = useT("agents");
  const { detail, canEdit, bindings } = context;
  const enableLabel = useEnableLabel(
    detail.scope?.type ?? context.node.scopeType,
    nodeSceneKind(context.node, detail),
  );
  // Keep showing the last app while the dialog animates closed.
  const [shown, setShown] = useState(app);
  if (app && app !== shown) setShown(app);

  return (
    <Dialog
      open={app !== null}
      onOpenChange={(open) => {
        if (!open) onClose();
      }}
    >
      <DialogContent className="flex max-h-[85vh] flex-col gap-0 overflow-hidden p-0 sm:max-w-lg">
        {shown ? (
          <>
            <div className="flex items-center gap-3 border-b p-4 pr-12">
              <ConnectorLogo slug={shown.catalogSlug} size="md" />
              <DialogTitle className="min-w-0 truncate text-title font-semibold">{shown.name}</DialogTitle>
            </div>
            <div className="min-h-0 flex-1 space-y-4 overflow-y-auto p-4">
              {shown.global === true ? (
                <p className="text-caption text-muted-foreground">
                  {t(($) => $.tab_body.context_builder.common_note)}
                </p>
              ) : (
                <SwitchRow
                  id={`context-app-${shown.id}`}
                  label={enableLabel}
                  checked={bindings.isEnabled("connector", shown.id)}
                  disabled={!canEdit}
                  pending={bindings.isBusy("connector", shown.id)}
                  onCheckedChange={(next) => bindings.toggle("connector", shown.id, next)}
                />
              )}
              <AppAccount
                // A new app starts with a fresh form (never another app's
                // typed token).
                key={shown.id}
                context={context}
                app={shown}
              />
            </div>
          </>
        ) : null}
      </DialogContent>
    </Dialog>
  );
}

/** The level's own account of an app: OAuth connect, a Personal Access Token
 * or disconnect for a caller who may connect it; otherwise its status and
 * who connects it. */
function AppAccount({ context, app }: { context: NodeContext; app: ContextNodeConnector }) {
  const { t } = useT("agents");
  const { wsId, agentId, node, detail, connect, canConnect } = context;
  const start = useStartContextNodeConnection(wsId, agentId);
  const saveToken = useSetContextNodeCredential(wsId, agentId);
  const disconnect = useDeleteContextNodeCredential(wsId, agentId);
  const connectNote = useConnectNote(detail);
  const [patOpen, setPatOpen] = useState(false);
  const [confirmDisconnect, setConfirmDisconnect] = useState(false);
  const [redirecting, setRedirecting] = useState(false);
  useResetOnBackForwardRestore(redirecting, () => setRedirecting(false));
  const connected = app.credential?.connected === true;
  const account = app.credential?.account ?? "";
  const connecting = start.isPending || redirecting;

  const startConnect = async () => {
    const returnPath = connect.returnPath(app.catalogSlug);
    if (connect.handOff?.(returnPath)) return;
    const failed = t(($) => $.internal_mcp.catalog.connect_failed, { name: app.name });
    try {
      const url = await start.mutateAsync({ node, connectorId: app.id, returnTo: returnPath });
      if (!url) {
        toast.error(failed);
        return;
      }
      setRedirecting(true);
      connect.navigate(url);
    } catch (error) {
      toast.error(
        errorCode(error) === "oauth_unavailable"
          ? t(($) => $.tab_body.connected_apps.auth_unavailable_hint, { name: app.name })
          : errorMessage(error, failed),
      );
    }
  };

  const savePat = async (bearer: string) => {
    try {
      await saveToken.mutateAsync({ node, connectorId: app.id, bearer });
      toast.success(t(($) => $.internal_mcp.catalog.pat_saved));
      setPatOpen(false);
    } finally {
      // Drop the submitted secret from the mutation state right away.
      saveToken.reset();
    }
  };

  const doDisconnect = async () => {
    try {
      await disconnect.mutateAsync({ node, connectorId: app.id });
      setConfirmDisconnect(false);
      toast.success(t(($) => $.tab_body.context_builder.account_disconnected));
    } catch (error) {
      toast.error(errorMessage(error, t(($) => $.tab_body.connected_apps.disconnect_failed)));
    }
  };

  const status = connected
    ? account
      ? t(($) => $.tab_body.connected_apps.shared_connected_as, { account })
      : t(($) => $.tab_body.connected_apps.shared_connected)
    : t(($) => $.tab_body.connected_apps.shared_none);

  return (
    <DialogSection id={`context-app-account-${app.id}`} title={t(($) => $.tab_body.context_builder.account_title)}>
      {app.catalogSlug === "atlassian" ? (
        <AtlassianDomainNote
          body={t(($) => $.tab_body.connected_apps.atlassian_domain)}
          copyLabel={t(($) => $.tab_body.connected_apps.domain_copy)}
          copiedLabel={t(($) => $.tab_body.connected_apps.domain_copied)}
          docsLabel={t(($) => $.tab_body.connected_apps.atlassian_docs)}
        />
      ) : null}
      <div className="flex flex-wrap items-center gap-2">
        <span className={connected ? "text-body" : "text-body text-muted-foreground"}>{status}</span>
        {canConnect ? (
          <span className="ml-auto flex flex-wrap gap-1.5">
            {app.oauthAvailable ? (
              <Button
                size="sm"
                variant={connected ? "outline" : "default"}
                disabled={connecting}
                onClick={() => void startConnect()}
              >
                {connecting && <Loader2 className="size-3.5 animate-spin motion-reduce:animate-none" />}
                {connecting
                  ? t(($) => $.internal_mcp.catalog.connecting)
                  : connected
                    ? t(($) => $.tab_body.connected_apps.reconnect)
                    : t(($) => $.tab_body.connected_apps.connect)}
              </Button>
            ) : null}
            {app.acceptsPat && !patOpen ? (
              <Button
                size="sm"
                variant={app.oauthAvailable || connected ? "outline" : "default"}
                onClick={() => setPatOpen(true)}
              >
                {connected
                  ? t(($) => $.tab_body.connected_apps.replace_pat)
                  : t(($) => $.internal_mcp.catalog.use_pat)}
              </Button>
            ) : null}
            {connected ? (
              <Button
                size="sm"
                variant="ghost"
                className="text-muted-foreground hover:text-destructive"
                onClick={() => setConfirmDisconnect(true)}
              >
                {t(($) => $.tab_body.connected_apps.disconnect)}
              </Button>
            ) : null}
          </span>
        ) : null}
      </div>
      {canConnect ? (
        !app.oauthAvailable ? (
          <p className="text-caption text-muted-foreground">
            {app.acceptsPat
              ? t(($) => $.tab_body.connected_apps.auth_pat_only, { name: app.name })
              : t(($) => $.tab_body.connected_apps.auth_unavailable_hint, { name: app.name })}
          </p>
        ) : null
      ) : (
        <p className="text-caption text-muted-foreground">{connectNote}</p>
      )}
      {app.installUrl ? <InstallLink url={app.installUrl} /> : null}
      {patOpen ? (
        <TokenForm
          inputId={`context-pat-${app.id}`}
          label={t(($) => $.internal_mcp.catalog.pat_label, { name: app.name })}
          placeholder={t(($) => $.internal_mcp.catalog.pat_placeholder)}
          pending={saveToken.isPending}
          onSave={savePat}
          onCancel={() => setPatOpen(false)}
        />
      ) : null}
      <ConfirmDialog
        open={confirmDisconnect}
        onOpenChange={setConfirmDisconnect}
        title={t(($) => $.tab_body.context_builder.account_disconnect_title, { name: app.name })}
        description={t(($) => $.tab_body.context_builder.account_disconnect_description)}
        confirmLabel={t(($) => $.tab_body.connected_apps.disconnect)}
        pending={disconnect.isPending}
        onConfirm={() => void doDisconnect()}
      />
    </DialogSection>
  );
}

// ---------------------------------------------------------------------------
// Skills
// ---------------------------------------------------------------------------

function SkillsSection({ context }: { context: NodeContext }) {
  const { t } = useT("agents");
  const { detail, node, canEdit, bindings } = context;
  const titleId = `context-skills-${node.scopeType}-${node.scopeKey}`;
  return (
    <section className="space-y-3" aria-labelledby={titleId}>
      <SectionHeading id={titleId} level={3} title={t(($) => $.tab_body.context_builder.skills_title)} />
      {detail.skills.length === 0 ? (
        <p className="text-caption text-muted-foreground">{t(($) => $.tab_body.context_builder.none)}</p>
      ) : (
        <ul className="divide-y rounded-lg border bg-card">
          {detail.skills.map((skill) => (
            <li key={skill.id} className="flex items-center gap-3 px-3 py-2">
              <span className="flex size-8 shrink-0 items-center justify-center rounded-md bg-muted text-muted-foreground">
                <SkillIcon className="size-4" />
              </span>
              <div className="min-w-0 flex-1">
                <p className="truncate text-body font-medium">{skill.name}</p>
                {skill.description ? (
                  <p className="truncate text-caption text-muted-foreground">{skill.description}</p>
                ) : null}
              </div>
              <BindingSwitch
                bindings={bindings}
                resourceType="skill"
                resourceId={skill.id}
                name={skill.name}
                canEdit={canEdit}
              />
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

// ---------------------------------------------------------------------------
// 生效预览
// ---------------------------------------------------------------------------

function LayerBadge({ layer, sceneKind }: { layer: ContextEffectiveLayer; sceneKind: ContextSceneKind }) {
  const label = useLayerLabel(sceneKind);
  return (
    <Badge variant={layer === "global" ? "outline" : "secondary"} className="shrink-0">
      {label(layer)}
    </Badge>
  );
}

function EffectiveGroup({ title, children, empty }: { title: string; children: React.ReactNode; empty: boolean }) {
  const { t } = useT("agents");
  return (
    <div className="space-y-1.5">
      <h4 className="text-label font-medium text-muted-foreground">{title}</h4>
      {empty ? (
        <p className="text-caption text-muted-foreground">{t(($) => $.tab_body.context_builder.none)}</p>
      ) : (
        <ul className="divide-y rounded-lg border bg-card">{children}</ul>
      )}
    </div>
  );
}

/** What a run at this level gets, as the runtime builds it, with the layer
 * each component comes from and the ones a nearer layer replaces. */
function EffectiveSection({ detail, sceneKind }: { detail: ContextNodeDetail; sceneKind: ContextSceneKind }) {
  const { t } = useT("agents");
  const { prompts, mcpServers, connectors, skills } = detail.effective;
  const overriddenLabel = t(($) => $.tab_body.context_builder.overridden);
  const titleId = "context-effective";
  return (
    <section className="space-y-4" aria-labelledby={titleId}>
      <SectionHeading id={titleId} level={3} title={t(($) => $.tab_body.context_builder.effective_title)} />
      <EffectiveGroup title={t(($) => $.tab_body.context_builder.prompts_title)} empty={prompts.length === 0}>
        {prompts.map((prompt, index) => (
          <li
            key={`${prompt.layer}:${prompt.name}:${index}`}
            className={cn("flex items-start gap-2 px-3 py-2", prompt.overridden && "text-muted-foreground")}
          >
            <LayerBadge layer={prompt.layer} sceneKind={sceneKind} />
            <div className="min-w-0 flex-1">
              <p className={cn("truncate text-body font-medium", prompt.overridden && "line-through")}>
                {prompt.name}
              </p>
              {prompt.text ? (
                <p className="line-clamp-2 whitespace-pre-wrap break-words text-caption text-muted-foreground">
                  {prompt.text}
                </p>
              ) : null}
            </div>
            {prompt.overridden ? <span className="shrink-0 text-caption">{overriddenLabel}</span> : null}
          </li>
        ))}
      </EffectiveGroup>
      <EffectiveGroup title={t(($) => $.tab_body.connectors.custom_title)} empty={mcpServers.length === 0}>
        {mcpServers.map((server, index) => (
          <li
            key={`${server.layer}:${server.name}:${index}`}
            className={cn("flex items-center gap-2 px-3 py-2", server.overridden && "text-muted-foreground")}
          >
            <LayerBadge layer={server.layer} sceneKind={sceneKind} />
            <span className={cn("min-w-0 flex-1 truncate text-body", server.overridden && "line-through")}>
              {server.name}
            </span>
            {server.overridden ? <span className="shrink-0 text-caption">{overriddenLabel}</span> : null}
          </li>
        ))}
      </EffectiveGroup>
      <EffectiveGroup title={t(($) => $.tab_body.context_builder.connectors_title)} empty={connectors.length === 0}>
        {connectors.map((connector) => (
          <li key={`${connector.layer}:${connector.id}`} className="flex items-center gap-2 px-3 py-2">
            <LayerBadge layer={connector.layer} sceneKind={sceneKind} />
            <span className="min-w-0 flex-1 truncate text-body">{connector.name}</span>
          </li>
        ))}
      </EffectiveGroup>
      <EffectiveGroup title={t(($) => $.tab_body.context_builder.skills_title)} empty={skills.length === 0}>
        {skills.map((skill) => (
          <li key={`${skill.layer}:${skill.id}`} className="flex items-center gap-2 px-3 py-2">
            <LayerBadge layer={skill.layer} sceneKind={sceneKind} />
            <span className="min-w-0 flex-1 truncate text-body">{skill.name}</span>
          </li>
        ))}
      </EffectiveGroup>
    </section>
  );
}
