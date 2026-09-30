"use client";

import { useEffect, useMemo, useState } from "react";
import { FileText, Loader2, Lock, Plus } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import type { Agent } from "@multica/core/types";
import { errorCode } from "@multica/core/api";
import {
  agentContextCapabilitiesOptions,
  useDeleteAgentSceneCredential,
  useSetAgentSceneBinding,
  useSetAgentSceneCredential,
  useSetAgentSceneMcpConfig,
  useSetAgentScenePrompt,
  useStartContextConnectorConnection,
  type AgentSceneDetail,
  type AgentSceneOfferedConnector,
  type ContextResourceType,
} from "@multica/core/context-capabilities";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import { Button } from "@multica/ui/components/ui/button";
import { Dialog, DialogContent, DialogTitle } from "@multica/ui/components/ui/dialog";
import { Switch } from "@multica/ui/components/ui/switch";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { cn } from "@multica/ui/lib/utils";
import { ConnectorLogo } from "../../../common/connector-logo";
import { useResetOnBackForwardRestore } from "../../../common/connector-credential";
import { useNavigation } from "../../../navigation";
import { SkillIcon } from "../../../skills/lib/skill-icon";
import { useT, useTimeAgo } from "../../../i18n";
import { APP_PARAM, useConnectReturnToast, useDesktopConnectHandoff, useReplaceSearch } from "./connect-flow";
import { ConfigureLink } from "./context-offers-section";
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
import { listManagedMcpServers, removeManagedMcpServer, upsertManagedMcpServer, type ManagedMcpServer } from "./mcp-config-model";
import { McpServerDialog } from "./mcp-server-dialog";

/** Server limit of a scene prompt, in characters. */
export const SCENE_PROMPT_MAX_LENGTH = 8000;

/**
 * 场域 → scene → 配置, parallel to the agent's 连接器 tab: the connector tab
 * is the shared (global) side, this page the scene's own. After the scene
 * prompt come 「MCP」 (the offered Aone FaaS connectors switched on for this
 * scene with the scene's own token, plus the scene's own custom MCP
 * servers), 「连接应用」 (offered official apps, switched on and connected
 * with the scene's own account in a dialog, `?app=<slug>`) and the offered
 * skills. A 1:1 chat's configuration is its person's configuration: the
 * server reads and writes that person's scope (`detail.scope`).
 */
export function SceneConfigPanel({
  agent,
  detail,
  canEdit,
  onDirtyChange,
}: {
  agent: Agent;
  detail: AgentSceneDetail;
  canEdit: boolean;
  onDirtyChange?: (dirty: boolean) => void;
}) {
  const { t } = useT("agents");
  const wsId = useWorkspaceId();
  const capabilities = useQuery({
    ...agentContextCapabilitiesOptions(wsId, agent.id),
    enabled: canEdit && Boolean(wsId),
  });
  const configureUrl = capabilities.data?.configureUrl ?? "";
  const scope = detail.scope;
  useConnectReturnToast();

  return (
    <div className="mx-auto w-full max-w-3xl space-y-8 p-4 sm:p-6">
      <ScenePromptEditor
        key={detail.scene.sceneKey}
        agentId={agent.id}
        sceneKey={detail.scene.sceneKey}
        prompt={detail.prompt}
        canEdit={canEdit}
        onDirtyChange={onDirtyChange}
      />
      {scope === null ? (
        <ConnectorNotice>
          {/* null is a 1:1 chat whose person is unknown; for a group it can
              only be a scope this build cannot read. */}
          {detail.scene.kind === "dm"
            ? t(($) => $.tab_body.scenes.scope_unknown)
            : t(($) => $.tab_body.scenes.scope_unavailable)}
        </ConnectorNotice>
      ) : (
        <>
          {scope?.type === "person" ? (
            <p className="rounded-md bg-muted/40 px-3 py-2 text-caption text-muted-foreground">
              {t(($) => $.tab_body.scenes.dm_bound, {
                title: scope.title || t(($) => $.tab_body.scenes.person_untitled),
              })}
            </p>
          ) : null}
          <SceneCapabilities agent={agent} wsId={wsId} detail={detail} canEdit={canEdit} />
        </>
      )}
      {configureUrl ? (
        <div className="space-y-1.5">
          <p className="text-caption font-medium text-muted-foreground">
            {t(($) => $.tab_body.context_offers.configure_title)}
          </p>
          <ConfigureLink url={configureUrl} />
        </div>
      ) : null}
    </div>
  );
}

function ScenePromptEditor({
  agentId,
  sceneKey,
  prompt,
  canEdit,
  onDirtyChange,
}: {
  agentId: string;
  sceneKey: string;
  prompt: AgentSceneDetail["prompt"];
  canEdit: boolean;
  onDirtyChange?: (dirty: boolean) => void;
}) {
  const { t } = useT("agents");
  const wsId = useWorkspaceId();
  const timeAgo = useTimeAgo();
  const save = useSetAgentScenePrompt(wsId, agentId);
  const [draft, setDraft] = useState(prompt.text);
  // The stored prompt the draft started from. When a save (this one or
  // another admin's, via a refetch) changes the stored prompt, a draft
  // without edits follows it; a draft with unsaved edits is kept.
  const [baseline, setBaseline] = useState(prompt.text);
  if (prompt.text !== baseline) {
    setBaseline(prompt.text);
    if (draft === baseline) setDraft(prompt.text);
  }
  const dirty = draft !== prompt.text;
  const tooLong = draft.length > SCENE_PROMPT_MAX_LENGTH;
  const inputId = `scene-prompt-${sceneKey}`;

  useEffect(() => {
    onDirtyChange?.(dirty);
  }, [dirty, onDirtyChange]);
  useEffect(() => () => onDirtyChange?.(false), [onDirtyChange]);

  const submit = async () => {
    if (!dirty || tooLong) return;
    const submitted = draft;
    try {
      const saved = await save.mutateAsync({ sceneKey, prompt: submitted });
      // The server trims the prompt; adopt the stored text unless the admin
      // kept typing while the save was in flight.
      setDraft((current) => (current === submitted ? saved.text : current));
      toast.success(t(($) => $.tab_body.scenes.prompt_saved));
    } catch (error) {
      toast.error(errorMessage(error, t(($) => $.tab_body.scenes.prompt_save_failed)));
    }
  };

  const updated = prompt.updatedAt
    ? prompt.updatedByName
      ? t(($) => $.tab_body.scenes.prompt_updated, {
          when: timeAgo(prompt.updatedAt),
          name: prompt.updatedByName,
        })
      : t(($) => $.tab_body.scenes.prompt_updated_no_name, { when: timeAgo(prompt.updatedAt) })
    : "";

  return (
    <section className="space-y-2" aria-labelledby={`${inputId}-title`}>
      <div className="flex flex-wrap items-baseline gap-x-2 gap-y-0.5">
        <h3 id={`${inputId}-title`} className="flex items-center gap-1.5 text-body font-medium">
          <FileText className="size-4 text-muted-foreground" aria-hidden="true" />
          {t(($) => $.tab_body.scenes.prompt_title)}
        </h3>
        <p className="text-caption text-muted-foreground">
          {canEdit ? t(($) => $.tab_body.scenes.prompt_hint) : t(($) => $.tab_body.scenes.prompt_read_only)}
        </p>
      </div>
      {canEdit ? (
        <>
          <label htmlFor={inputId} className="sr-only">
            {t(($) => $.tab_body.scenes.prompt_title)}
          </label>
          <Textarea
            id={inputId}
            value={draft}
            rows={4}
            placeholder={t(($) => $.tab_body.scenes.prompt_placeholder)}
            aria-invalid={tooLong || undefined}
            onChange={(event) => setDraft(event.target.value)}
            className="min-h-24 text-body leading-6"
          />
          <div className="flex flex-wrap items-center justify-between gap-2">
            <p className={cn("text-caption", tooLong ? "text-destructive" : "text-muted-foreground")}>
              {tooLong
                ? t(($) => $.tab_body.scenes.prompt_too_long, { max: SCENE_PROMPT_MAX_LENGTH })
                : updated}
            </p>
            <div className="flex gap-2">
              <Button
                size="sm"
                variant="ghost"
                disabled={!dirty || save.isPending}
                onClick={() => setDraft(prompt.text)}
              >
                {t(($) => $.tab_body.scenes.prompt_discard)}
              </Button>
              <Button size="sm" disabled={!dirty || tooLong || save.isPending} onClick={() => void submit()}>
                {save.isPending && (
                  <Loader2 className="size-3.5 animate-spin motion-reduce:animate-none" aria-hidden="true" />
                )}
                {save.isPending
                  ? t(($) => $.tab_body.scenes.prompt_saving)
                  : t(($) => $.tab_body.scenes.prompt_save)}
              </Button>
            </div>
          </div>
        </>
      ) : (
        <>
          <p className="whitespace-pre-wrap rounded-md border bg-muted/30 px-3 py-2 text-body">
            {prompt.text || t(($) => $.tab_body.scenes.prompt_empty)}
          </p>
          {updated ? <p className="text-caption text-muted-foreground">{updated}</p> : null}
        </>
      )}
    </section>
  );
}

// ---------------------------------------------------------------------------
// Bindings shared by the MCP rows, the app dialog and the skills
// ---------------------------------------------------------------------------

interface SceneBindings {
  isEnabled: (resourceType: ContextResourceType, resourceId: string) => boolean;
  isBusy: (resourceType: ContextResourceType, resourceId: string) => boolean;
  toggle: (resourceType: ContextResourceType, resourceId: string, enabled: boolean) => void;
}

function bindingKey(resourceType: ContextResourceType, resourceId: string): string {
  return `${resourceType}:${resourceId}`;
}

/** The scene's 在本场域启用 switches. Not optimistic: the server gates each
 * write on the offer catalog. One pending entry per row, so overlapping
 * toggles of different rows never clear each other's state. */
function useSceneBindings(wsId: string, agentId: string, detail: AgentSceneDetail): SceneBindings {
  const { t } = useT("agents");
  const setBinding = useSetAgentSceneBinding(wsId, agentId);
  const [busyKeys, setBusyKeys] = useState<ReadonlySet<string>>(() => new Set());
  const enabledKeys = useMemo(
    () =>
      new Set(
        detail.bindings
          .filter((binding) => binding.enabled === true)
          .map((binding) => bindingKey(binding.resourceType, binding.resourceId)),
      ),
    [detail.bindings],
  );
  const sceneKey = detail.scene.sceneKey;

  const toggle = async (resourceType: ContextResourceType, resourceId: string, enabled: boolean) => {
    const key = bindingKey(resourceType, resourceId);
    if (busyKeys.has(key)) return;
    setBusyKeys((current) => new Set(current).add(key));
    try {
      await setBinding.mutateAsync({ sceneKey, resourceType, resourceId, enabled });
    } catch (error) {
      toast.error(errorMessage(error, t(($) => $.tab_body.scenes.toggle_failed)));
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
  bindings: SceneBindings;
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
          aria-label={t(($) => $.tab_body.scenes.toggle_aria, { name })}
        />
      )}
    </span>
  );
}

/** Who connects accounts when this caller may not: a 1:1 chat's person, or
 * (a group) whoever connects on the configure page, which needs a DingTalk
 * sign-in. */
function useConnectNote(detail: AgentSceneDetail): string {
  const { t } = useT("agents");
  return detail.scope?.type === "person"
    ? t(($) => $.tab_body.scenes.owner_connects)
    : t(($) => $.tab_body.scenes.configure_connects);
}

function SceneCapabilities({
  agent,
  wsId,
  detail,
  canEdit,
}: {
  agent: Agent;
  wsId: string;
  detail: AgentSceneDetail;
  canEdit: boolean;
}) {
  const bindings = useSceneBindings(wsId, agent.id, detail);
  return (
    <>
      <SceneMcpSection agent={agent} wsId={wsId} detail={detail} canEdit={canEdit} bindings={bindings} />
      <SceneAppsSection agent={agent} wsId={wsId} detail={detail} canEdit={canEdit} bindings={bindings} />
      <SceneSkillsSection detail={detail} canEdit={canEdit} bindings={bindings} />
    </>
  );
}

// ---------------------------------------------------------------------------
// MCP: offered Aone FaaS connectors and the scene's own MCP servers
// ---------------------------------------------------------------------------

function SceneMcpSection({
  agent,
  wsId,
  detail,
  canEdit,
  bindings,
}: {
  agent: Agent;
  wsId: string;
  detail: AgentSceneDetail;
  canEdit: boolean;
  bindings: SceneBindings;
}) {
  const { t } = useT("agents");
  const connectors = detail.offers.connectors.filter((connector) => connector.catalogSlug === "");
  const connectNote = useConnectNote(detail);
  const titleId = `scene-mcp-${detail.scene.sceneKey}`;
  return (
    <section className="space-y-3" aria-labelledby={titleId}>
      <SectionHeading id={titleId} level={3} title={t(($) => $.tab_body.scenes.mcp_title)} />
      {connectors.length > 0 ? (
        <ul className="divide-y rounded-lg border bg-card">
          {connectors.map((connector) => (
            <SceneConnectorRow
              key={connector.id}
              agentId={agent.id}
              wsId={wsId}
              sceneKey={detail.scene.sceneKey}
              connector={connector}
              canEdit={canEdit}
              canConnect={detail.canConnect === true}
              connectNote={connectNote}
              bindings={bindings}
            />
          ))}
        </ul>
      ) : null}
      {detail.mcpConfigSupported === true ? (
        <SceneCustomMcpServers agentId={agent.id} wsId={wsId} detail={detail} canEdit={canEdit} />
      ) : connectors.length === 0 ? (
        <p className="text-caption text-muted-foreground">{t(($) => $.tab_body.scenes.none)}</p>
      ) : null}
    </section>
  );
}

/** 已开启 / 未开启, plus 已连接 / 未连接 for a connector that takes a
 * credential. */
function SceneStatusPills({
  enabled,
  connector,
  showCredential,
}: {
  enabled: boolean;
  connector: AgentSceneOfferedConnector;
  showCredential: boolean;
}) {
  const { t } = useT("agents");
  const connected = connector.credential?.connected === true;
  const account = connector.credential?.account ?? "";
  return (
    <>
      <StatusPill tone={enabled ? "success" : "muted"}>
        {enabled ? t(($) => $.tab_body.connected_apps.usage_enabled) : t(($) => $.tab_body.connected_apps.usage_not_enabled)}
      </StatusPill>
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

function SceneConnectorRow({
  agentId,
  wsId,
  sceneKey,
  connector,
  canEdit,
  canConnect,
  connectNote,
  bindings,
}: {
  agentId: string;
  wsId: string;
  sceneKey: string;
  connector: AgentSceneOfferedConnector;
  canEdit: boolean;
  canConnect: boolean;
  /** Shown instead of the token controls when the caller cannot connect. */
  connectNote: string;
  bindings: SceneBindings;
}) {
  const { t } = useT("agents");
  const saveToken = useSetAgentSceneCredential(wsId, agentId);
  const removeToken = useDeleteAgentSceneCredential(wsId, agentId);
  const [editing, setEditing] = useState(false);
  const [confirmRemove, setConfirmRemove] = useState(false);
  const takesToken = connector.acceptsCredential === true;
  const connected = connector.credential?.connected === true;

  const save = async (bearer: string) => {
    try {
      await saveToken.mutateAsync({ sceneKey, connectorId: connector.id, bearer });
      toast.success(t(($) => $.tab_body.scenes.token_saved));
      setEditing(false);
    } finally {
      // Drop the submitted secret from the mutation state right away.
      saveToken.reset();
    }
  };

  const remove = async () => {
    try {
      await removeToken.mutateAsync({ sceneKey, connectorId: connector.id });
      setConfirmRemove(false);
      toast.success(t(($) => $.tab_body.scenes.token_removed));
    } catch (error) {
      toast.error(errorMessage(error, t(($) => $.tab_body.scenes.token_remove_failed)));
    }
  };

  return (
    <li className="space-y-2 px-3 py-2.5" aria-label={connector.name}>
      <div className="flex items-center gap-3">
        <ConnectorLogo slug="" />
        <div className="min-w-0 flex-1 space-y-1">
          <p className="truncate text-body font-medium">{connector.name}</p>
          <div className="flex flex-wrap items-center gap-1">
            <SceneStatusPills
              enabled={bindings.isEnabled("connector", connector.id)}
              connector={connector}
              showCredential={takesToken}
            />
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
              {connected ? t(($) => $.tab_body.scenes.token_remove) : t(($) => $.tab_body.scenes.token_set)}
            </Button>
          ) : (
            <span className="hidden text-caption text-muted-foreground sm:inline">{connectNote}</span>
          )
        ) : null}
        <BindingSwitch
          bindings={bindings}
          resourceType="connector"
          resourceId={connector.id}
          name={connector.name}
          canEdit={canEdit}
        />
      </div>
      {takesToken && !canConnect ? (
        <p className="text-caption text-muted-foreground sm:hidden">{connectNote}</p>
      ) : null}
      {editing ? (
        <TokenForm
          inputId={`scene-token-${connector.id}`}
          label={t(($) => $.tab_body.scenes.token_label, { name: connector.name })}
          placeholder={t(($) => $.tab_body.scenes.token_placeholder)}
          pending={saveToken.isPending}
          onSave={save}
          onCancel={() => setEditing(false)}
        />
      ) : null}
      <ConfirmDialog
        open={confirmRemove}
        onOpenChange={setConfirmRemove}
        title={t(($) => $.tab_body.scenes.token_remove_title, { name: connector.name })}
        description={t(($) => $.tab_body.scenes.token_remove_description)}
        confirmLabel={t(($) => $.tab_body.scenes.token_remove)}
        pending={removeToken.isPending}
        onConfirm={() => void remove()}
      />
    </li>
  );
}

/** The scope's own MCP servers (the agent `mcp_config` document shape).
 * Stored only for now. When the workspace redacts secrets the stored
 * document is withheld, so it is shown as locked and never saved over. */
function SceneCustomMcpServers({
  agentId,
  wsId,
  detail,
  canEdit,
}: {
  agentId: string;
  wsId: string;
  detail: AgentSceneDetail;
  canEdit: boolean;
}) {
  const { t } = useT("agents");
  const save = useSetAgentSceneMcpConfig(wsId, agentId);
  const sceneKey = detail.scene.sceneKey;
  const mcpConfig = detail.mcpConfig ?? null;
  const redacted = detail.mcpConfigRedacted === true;
  const editable = canEdit && !redacted;
  const servers = useMemo(() => listManagedMcpServers(mcpConfig), [mcpConfig]);
  const names = useMemo(() => new Set(servers.map((server) => server.name)), [servers]);
  const [editorOpen, setEditorOpen] = useState(false);
  const [editing, setEditing] = useState<ManagedMcpServer | null>(null);
  const [deleting, setDeleting] = useState<ManagedMcpServer | null>(null);
  const titleId = `scene-custom-mcp-${sceneKey}`;

  const saveServer = async (name: string, config: Record<string, unknown>) => {
    try {
      await save.mutateAsync({
        sceneKey,
        mcpConfig: upsertManagedMcpServer(mcpConfig, editing, name, config),
      });
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
      await save.mutateAsync({ sceneKey, mcpConfig: removeManagedMcpServer(mcpConfig, server) });
      setDeleting(null);
      toast.success(t(($) => $.tab_body.mcp_config.deleted_toast));
    } catch (error) {
      toast.error(errorMessage(error, t(($) => $.tab_body.mcp_config.delete_failed_toast)));
    }
  };

  return (
    <div className="space-y-2" role="group" aria-labelledby={titleId}>
      <div className="flex items-center justify-between gap-2">
        <div className="flex min-w-0 flex-wrap items-baseline gap-x-2 gap-y-0.5">
          <h4 id={titleId} className="text-label font-medium text-muted-foreground">
            {t(($) => $.tab_body.connectors.custom_title)}
          </h4>
          {/* Stored only: the runtime does not apply a scope's servers yet. */}
          <p className="text-caption text-muted-foreground">{t(($) => $.tab_body.scenes.prompt_hint)}</p>
        </div>
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
        />
      ) : (
        <p className="text-caption text-muted-foreground">{t(($) => $.tab_body.scenes.none)}</p>
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
        description={t(($) => $.tab_body.scenes.mcp_delete_description, { name: deleting?.name ?? "" })}
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
// 连接应用: offered official apps, connected with the scene's own account
// ---------------------------------------------------------------------------

function SceneAppsSection({
  agent,
  wsId,
  detail,
  canEdit,
  bindings,
}: {
  agent: Agent;
  wsId: string;
  detail: AgentSceneDetail;
  canEdit: boolean;
  bindings: SceneBindings;
}) {
  const { t } = useT("agents");
  const navigation = useNavigation();
  const replaceSearch = useReplaceSearch();
  const apps = detail.offers.connectors.filter((connector) => connector.catalogSlug !== "");
  const openSlug = navigation.searchParams.get(APP_PARAM) ?? "";
  const openApp = apps.find((app) => app.catalogSlug === openSlug) ?? null;
  const connectNote = useConnectNote(detail);
  const titleId = `scene-apps-${detail.scene.sceneKey}`;

  const setOpenSlug = (slug: string) =>
    replaceSearch((params) => {
      if (slug) params.set(APP_PARAM, slug);
      else params.delete(APP_PARAM);
    });

  return (
    <section className="space-y-3" aria-labelledby={titleId}>
      <SectionHeading id={titleId} level={3} title={t(($) => $.tab_body.connected_apps.title)} />
      {apps.length === 0 ? (
        <p className="text-caption text-muted-foreground">{t(($) => $.tab_body.scenes.none)}</p>
      ) : (
        <AppTileGrid label={t(($) => $.tab_body.connected_apps.title)}>
          {apps.map((app) => {
            const enabled = bindings.isEnabled("connector", app.id);
            return (
              <AppTile
                key={app.id}
                slug={app.catalogSlug}
                name={app.name}
                ariaLabel={t(($) => $.tab_body.connected_apps.card_aria, { name: app.name })}
                onOpen={() => setOpenSlug(app.catalogSlug)}
              >
                {enabled ? (
                  <StatusPill tone="success">{t(($) => $.tab_body.connected_apps.usage_enabled)}</StatusPill>
                ) : null}
                {app.credential?.connected === true ? (
                  <StatusPill tone="success">
                    {app.credential.account
                      ? t(($) => $.tab_body.connected_apps.shared_connected_as, { account: app.credential.account })
                      : t(($) => $.tab_body.connected_apps.shared_connected)}
                  </StatusPill>
                ) : null}
                {!enabled && app.credential?.connected !== true ? (
                  <StatusPill tone="muted">{t(($) => $.tab_body.connected_apps.usage_not_enabled)}</StatusPill>
                ) : null}
              </AppTile>
            );
          })}
        </AppTileGrid>
      )}
      <SceneAppDialog
        agentId={agent.id}
        wsId={wsId}
        sceneKey={detail.scene.sceneKey}
        app={openApp}
        canEdit={canEdit}
        canConnect={detail.canConnect === true}
        connectNote={connectNote}
        bindings={bindings}
        onClose={() => setOpenSlug("")}
      />
    </section>
  );
}

function SceneAppDialog({
  agentId,
  wsId,
  sceneKey,
  app,
  canEdit,
  canConnect,
  connectNote,
  bindings,
  onClose,
}: {
  agentId: string;
  wsId: string;
  sceneKey: string;
  /** The open app; null when closed. */
  app: AgentSceneOfferedConnector | null;
  canEdit: boolean;
  canConnect: boolean;
  connectNote: string;
  bindings: SceneBindings;
  onClose: () => void;
}) {
  const { t } = useT("agents");
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
              <SwitchRow
                id={`scene-app-${shown.id}`}
                label={t(($) => $.tab_body.scenes.enable_label)}
                checked={bindings.isEnabled("connector", shown.id)}
                disabled={!canEdit}
                pending={bindings.isBusy("connector", shown.id)}
                onCheckedChange={(next) => bindings.toggle("connector", shown.id, next)}
              />
              <SceneAppAccount
                // A new app starts with a fresh form (never another app's
                // typed token).
                key={shown.id}
                agentId={agentId}
                wsId={wsId}
                sceneKey={sceneKey}
                app={shown}
                canConnect={canConnect}
                connectNote={connectNote}
              />
            </div>
          </>
        ) : null}
      </DialogContent>
    </Dialog>
  );
}

/** The scope's own account of an app: OAuth connect, a Personal Access Token
 * or disconnect, for a caller who may connect it; otherwise its status and
 * who connects it. */
function SceneAppAccount({
  agentId,
  wsId,
  sceneKey,
  app,
  canConnect,
  connectNote,
}: {
  agentId: string;
  wsId: string;
  sceneKey: string;
  app: AgentSceneOfferedConnector;
  canConnect: boolean;
  connectNote: string;
}) {
  const { t } = useT("agents");
  const paths = useWorkspacePaths();
  const start = useStartContextConnectorConnection(agentId);
  const saveToken = useSetAgentSceneCredential(wsId, agentId);
  const disconnect = useDeleteAgentSceneCredential(wsId, agentId);
  const handOff = useDesktopConnectHandoff();
  const [patOpen, setPatOpen] = useState(false);
  const [confirmDisconnect, setConfirmDisconnect] = useState(false);
  const [redirecting, setRedirecting] = useState(false);
  useResetOnBackForwardRestore(redirecting, () => setRedirecting(false));
  const connected = app.credential?.connected === true;
  const account = app.credential?.account ?? "";
  const connecting = start.isPending || redirecting;
  const returnPath =
    `${paths.agentDetail(agentId)}?view=scenes&scene=${encodeURIComponent(sceneKey)}` +
    `&scene_tab=config&${APP_PARAM}=${encodeURIComponent(app.catalogSlug)}`;

  const connect = async () => {
    if (handOff(returnPath)) return;
    const failed = t(($) => $.internal_mcp.catalog.connect_failed, { name: app.name });
    try {
      const url = await start.mutateAsync({
        scopeType: "scene",
        scopeKey: sceneKey,
        connectorId: app.id,
        returnTo: returnPath,
      });
      if (!url) {
        toast.error(failed);
        return;
      }
      setRedirecting(true);
      window.location.assign(url);
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
      await saveToken.mutateAsync({ sceneKey, connectorId: app.id, bearer });
      toast.success(t(($) => $.internal_mcp.catalog.pat_saved));
      setPatOpen(false);
    } finally {
      // Drop the submitted secret from the mutation state right away.
      saveToken.reset();
    }
  };

  const doDisconnect = async () => {
    try {
      await disconnect.mutateAsync({ sceneKey, connectorId: app.id });
      setConfirmDisconnect(false);
      toast.success(t(($) => $.tab_body.scenes.account_disconnected));
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
    <DialogSection id={`scene-app-account-${app.id}`} title={t(($) => $.tab_body.scenes.account_title)}>
      <div className="flex flex-wrap items-center gap-2">
        <span className={connected ? "text-body" : "text-body text-muted-foreground"}>{status}</span>
        {canConnect ? (
          <span className="ml-auto flex flex-wrap gap-1.5">
            {app.oauthAvailable ? (
              <Button
                size="sm"
                variant={connected ? "outline" : "default"}
                disabled={connecting}
                onClick={() => void connect()}
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
          inputId={`scene-pat-${app.id}`}
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
        title={t(($) => $.tab_body.scenes.account_disconnect_title, { name: app.name })}
        description={t(($) => $.tab_body.scenes.account_disconnect_description)}
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

function SceneSkillsSection({
  detail,
  canEdit,
  bindings,
}: {
  detail: AgentSceneDetail;
  canEdit: boolean;
  bindings: SceneBindings;
}) {
  const { t } = useT("agents");
  const skills = detail.offers.skills;
  const titleId = `scene-skills-${detail.scene.sceneKey}`;
  return (
    <section className="space-y-3" aria-labelledby={titleId}>
      <SectionHeading id={titleId} level={3} title={t(($) => $.tab_body.scenes.skills_label)} />
      {skills.length === 0 ? (
        <p className="text-caption text-muted-foreground">{t(($) => $.tab_body.scenes.none)}</p>
      ) : (
        <ul className="divide-y rounded-lg border bg-card">
          {skills.map((skill) => (
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
