"use client";

import { createContext, useContext, useState } from "react";
import { ExternalLink, KeyRound, Link2, Loader2 } from "lucide-react";
import { toast } from "sonner";
import { errorCode } from "@multica/core/api";
import {
  useDeleteContextConnectorCredential,
  useSetContextConnectorCredential,
  useStartContextConnectorConnection,
  type ContextCapabilityBinding,
  type ContextConfigAgentDetail,
  type ContextConnectorCredential,
  type ContextOfferedConnector,
  type ContextSceneKind,
  type ContextWriteScopeType,
} from "@multica/core/context-capabilities";
import { Button } from "@multica/ui/components/ui/button";
import { Dialog, DialogContent, DialogTitle } from "@multica/ui/components/ui/dialog";
import { Input } from "@multica/ui/components/ui/input";
import { Switch } from "@multica/ui/components/ui/switch";
import { cn } from "@multica/ui/lib/utils";
import { AppTile, AppTileGrid, StatusPill, type StatusTone } from "../agents/components/tabs/connectors-ui";
import { ConnectorLogo, ConnectorMark } from "../common/connector-logo";
import { MAX_BEARER_LENGTH, isValidBearer, useResetOnBackForwardRestore } from "../common/connector-credential";
import { useT } from "../i18n";
import type { ContextConfigConnectTarget } from "./context-config-page";

export type OpenAuthorizeUrl = (url: string, target: ContextConfigConnectTarget) => void;

export interface ConnectPlumbing {
  open?: OpenAuthorizeUrl;
  /** Where the provider sign-in returns; undefined → the server's default. */
  returnTo?: string;
}

// Platform plumbing the connect buttons use. Kept in context so the scope
// editors do not drill it through every level.
export const ConnectPlumbingContext = createContext<ConnectPlumbing>({});

/** Catalog apps shown first, in this order, whatever their state. */
export const FEATURED_APP_SLUGS: readonly string[] = ["github", "slack", "notion"];

/**
 * One tile of 连接器和插件: a connector of the agent (an official app or a
 * custom connector), or a catalog app nobody has opened for the agent yet
 * (`id` null). Which bundle a connector comes from (the agent's own, or
 * offered to scenes) stays behind the page: it only decides whether the
 * connector is on by default or has to be added here.
 */
export interface ConnectorEntry {
  key: string;
  /** Connector id; null for a catalog app not opened for the agent. */
  id: string | null;
  /** Catalog slug; "" for a custom connector. */
  slug: string;
  name: string;
  /** The agent's own connector: on in every conversation. */
  defaultOn: boolean;
  /** Present when this level can switch it on or hold its account. */
  offered: ContextOfferedConnector | null;
}

/**
 * The tiles of 连接器和插件, in display order: GitHub, Slack and Notion first,
 * then the other official apps in catalog order, then custom connectors, then
 * the catalog apps not opened for the agent.
 */
export function connectorEntries(detail: ContextConfigAgentDetail): ConnectorEntry[] {
  const defaultIds = new Set(detail.global.connectors.map((connector) => connector.id));
  const entries: ConnectorEntry[] = [];
  const listed = new Set<string>();
  for (const connector of detail.offers.connectors) {
    listed.add(connector.id);
    entries.push({
      key: connector.id,
      id: connector.id,
      slug: connector.catalogSlug,
      name: connector.name,
      defaultOn: defaultIds.has(connector.id),
      offered: connector,
    });
  }
  for (const connector of detail.global.connectors) {
    if (listed.has(connector.id)) continue;
    listed.add(connector.id);
    entries.push({
      key: connector.id,
      id: connector.id,
      slug: connector.catalogSlug,
      name: connector.name,
      defaultOn: true,
      offered: null,
    });
  }
  const opened = new Set(entries.map((entry) => entry.slug).filter(Boolean));
  const apps = detail.apps ?? [];
  for (const app of apps) {
    if (opened.has(app.slug)) continue;
    entries.push({ key: `app:${app.slug}`, id: null, slug: app.slug, name: app.name, defaultOn: false, offered: null });
  }
  const catalogOrder = new Map(apps.map((app, index) => [app.slug, index]));
  const rank = (entry: ConnectorEntry): [number, number] => {
    const featured = FEATURED_APP_SLUGS.indexOf(entry.slug);
    if (entry.slug && featured >= 0) return [0, featured];
    const order = catalogOrder.get(entry.slug) ?? Number.MAX_SAFE_INTEGER;
    if (entry.id === null) return [3, order];
    return entry.slug ? [1, order] : [2, 0];
  };
  // Array.prototype.sort is stable: equal ranks keep the server's order.
  return entries.sort((a, b) => {
    const [groupA, orderA] = rank(a);
    const [groupB, orderB] = rank(b);
    return groupA - groupB || orderA - orderB;
  });
}

/** Whether a connector in effect here can be used: its own account, the
 * enterprise's, the workspace's shared one, or none needed. */
type AuthState = "none" | "authorized" | "shared" | "unauthorized";

interface EntryState {
  /** Switched on at this level. */
  added: boolean;
  /** Switched on at the enterprise level (a scene or person level). */
  byOrg: boolean;
  /** Applies at this level: on by default, added here or by the enterprise. */
  inEffect: boolean;
  auth: AuthState;
  /** "@account" of this level's own OAuth connection, else "". */
  account: string;
}

function entryState(
  entry: ConnectorEntry,
  enabledIds: ReadonlySet<string>,
  orgEnabledIds: ReadonlySet<string>,
  credential: ContextConnectorCredential | null,
  orgCredential: boolean,
): EntryState {
  const added = entry.id !== null && enabledIds.has(entry.id);
  const byOrg = entry.id !== null && orgEnabledIds.has(entry.id);
  const offered = entry.offered;
  let auth: AuthState = "none";
  if (offered && (offered.authMode === "oauth" || offered.acceptsCredential)) {
    if (credential || orgCredential) auth = "authorized";
    else auth = offered.credentialRequired ? "unauthorized" : "shared";
  }
  const account = credential?.hint.startsWith("@") ? credential.hint : "";
  return { added, byOrg, inEffect: entry.defaultOn || added || byOrg, auth, account };
}

/** What the connectors slot of one level needs from its editor. */
export interface ConnectorsSlotProps {
  agentId: string;
  scopeType: ContextWriteScopeType;
  scopeKey: string;
  orgId: string;
  sceneKind: ContextSceneKind;
  detail: ContextConfigAgentDetail;
  bindings: ContextCapabilityBinding[];
  credentials: ContextConnectorCredential[];
  /** May switch connectors on or off here. */
  canToggle: boolean;
  /** Accounts are connected by the scope's owner only (shown as a note). */
  ownerOnly: boolean;
  /** Accounts cannot be changed here (no note). */
  credentialReadOnly: boolean;
  isBusy: (key: string) => boolean;
  onToggle: (connectorId: string, enabled: boolean) => void;
  onToggleShare: (connectorId: string, shareInGroups: boolean) => void;
  reportError: (error: unknown) => boolean;
}

/**
 * 连接器和插件: every app and connector as a tile with its state at this
 * level. A tile opens a dialog with the current configuration: first 添加
 * (switch it on here), then 授权 (connect an account or store a token).
 */
export function ConnectorsTiles(props: ConnectorsSlotProps) {
  const { t } = useT("agents");
  const { detail, scopeType, bindings, credentials } = props;
  // The open tile, and the last one opened: the dialog keeps showing it
  // while it animates closed.
  const [openKey, setOpenKey] = useState("");
  const [shownKey, setShownKey] = useState("");
  const entries = connectorEntries(detail);
  const enabledIds = new Set(
    bindings
      .filter((binding) => binding.resourceType === "connector" && binding.enabled === true)
      .map((binding) => binding.resourceId),
  );
  // What the enterprise level turns on and holds accounts for applies at
  // the levels below it.
  const orgEnabledIds = new Set(scopeType === "org" ? [] : (detail.orgEffect?.connectorIds ?? []));
  const credentialByConnector = new Map(credentials.map((credential) => [credential.connectorId, credential]));
  const orgCredentialIds = new Set(scopeType === "org" ? [] : (detail.orgEffect?.credentialConnectorIds ?? []));
  const stateOf = (entry: ConnectorEntry) =>
    entryState(
      entry,
      enabledIds,
      orgEnabledIds,
      entry.id ? (credentialByConnector.get(entry.id) ?? null) : null,
      entry.id !== null && orgCredentialIds.has(entry.id),
    );
  // The tile's accessible name carries its state, which its content would
  // otherwise lose.
  const tileLabel = (entry: ConnectorEntry, state: EntryState) =>
    [t(($) => $.context_config.app_open_aria, { name: entry.name }), ...entryStatuses(t, entry, state).map((status) => status.text)].join(" · ");
  const shown = entries.find((entry) => entry.key === shownKey) ?? null;
  // GitHub, Slack and Notion keep a full tile even before anyone opens them;
  // the other apps not opened for the agent fold into one compact row.
  const tiles = entries.filter((entry) => entry.id !== null || FEATURED_APP_SLUGS.includes(entry.slug));
  const more = entries.filter((entry) => entry.id === null && !FEATURED_APP_SLUGS.includes(entry.slug));
  const open = (key: string) => {
    setOpenKey(key);
    setShownKey(key);
  };

  if (entries.length === 0) {
    return <p className="text-caption text-muted-foreground">{t(($) => $.context_config.none)}</p>;
  }
  return (
    <>
      {tiles.length > 0 ? (
        <AppTileGrid label={t(($) => $.context_config.slot_connectors)}>
          {tiles.map((entry) => (
            <AppTile
              key={entry.key}
              slug={entry.slug}
              name={entry.name}
              ariaLabel={tileLabel(entry, stateOf(entry))}
              onOpen={() => open(entry.key)}
            >
              <EntryPills entry={entry} state={stateOf(entry)} />
            </AppTile>
          ))}
        </AppTileGrid>
      ) : null}
      {more.length > 0 ? (
        <div className="space-y-1.5">
          <p className="text-caption text-muted-foreground">{t(($) => $.context_config.more_apps)}</p>
          <ul aria-label={t(($) => $.context_config.more_apps)} className="flex flex-wrap gap-1.5">
            {more.map((entry) => (
              <li key={entry.key}>
                <button
                  type="button"
                  onClick={() => open(entry.key)}
                  aria-label={tileLabel(entry, stateOf(entry))}
                  className="inline-flex items-center gap-1.5 rounded-full border bg-card px-2.5 py-1 text-caption text-muted-foreground transition-colors hover:bg-accent/50 hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                >
                  <ConnectorMark slug={entry.slug} className="size-3.5" />
                  {entry.name}
                </button>
              </li>
            ))}
          </ul>
        </div>
      ) : null}
      <ConnectorDialog
        {...props}
        open={openKey !== "" && shown !== null}
        entry={shown}
        state={shown ? stateOf(shown) : null}
        credential={shown?.id ? (credentialByConnector.get(shown.id) ?? null) : null}
        orgAccount={shown?.id ? orgCredentialIds.has(shown.id) : false}
        shared={
          shown?.id
            ? bindings.some(
                (binding) =>
                  binding.resourceType === "connector" &&
                  binding.resourceId === shown.id &&
                  binding.enabled === true &&
                  binding.shareInGroups === true,
              )
            : false
        }
        onClose={() => setOpenKey("")}
      />
    </>
  );
}

type AgentsT = ReturnType<typeof useT<"agents">>["t"];

/** The state of a tile in words: whether it applies here, then its account. */
function entryStatuses(t: AgentsT, entry: ConnectorEntry, state: EntryState): { tone: StatusTone; text: string }[] {
  if (entry.id === null) return [{ tone: "muted", text: t(($) => $.context_config.status_unavailable) }];
  const statuses: { tone: StatusTone; text: string }[] = [
    entry.defaultOn
      ? { tone: "success", text: t(($) => $.context_config.always_on) }
      : state.added
        ? { tone: "success", text: t(($) => $.context_config.status_added) }
        : state.byOrg
          ? { tone: "success", text: t(($) => $.context_config.on_for_org) }
          : { tone: "muted", text: t(($) => $.context_config.status_not_added) },
  ];
  if (state.inEffect && state.auth === "unauthorized") {
    statuses.push({ tone: "warning", text: t(($) => $.context_config.status_unauthorized) });
  }
  if (state.inEffect && state.auth === "authorized") {
    statuses.push({
      tone: "success",
      text: state.account
        ? t(($) => $.context_config.connected_as, { account: state.account })
        : t(($) => $.context_config.connected),
    });
  }
  return statuses;
}

function EntryPills({ entry, state }: { entry: ConnectorEntry; state: EntryState }) {
  const { t } = useT("agents");
  return (
    <>
      {entryStatuses(t, entry, state).map((status) => (
        <StatusPill key={status.text} tone={status.tone}>
          {status.text}
        </StatusPill>
      ))}
    </>
  );
}

/** Text of 「已添加到…」 for the level. */
function useAddedLabel(scopeType: ContextWriteScopeType, sceneKind: ContextSceneKind): string {
  const { t } = useT("agents");
  switch (scopeType) {
    case "org":
      return t(($) => $.context_config.added_org);
    case "person":
      return t(($) => $.context_config.added_person);
    default:
      return sceneKind === "dm" ? t(($) => $.context_config.added_dm) : t(($) => $.context_config.added_scene);
  }
}

function StepHeading({ step, title }: { step: number; title: string }) {
  return (
    <h3 className="flex items-center gap-2 text-caption font-medium text-muted-foreground">
      <span
        aria-hidden="true"
        className="flex size-5 shrink-0 items-center justify-center rounded-full bg-muted text-micro font-semibold text-foreground"
      >
        {step}
      </span>
      {title}
    </h3>
  );
}

function ConnectorDialog({
  open,
  entry,
  state,
  credential,
  orgAccount,
  shared,
  onClose,
  ...props
}: ConnectorsSlotProps & {
  open: boolean;
  /** The tile shown (kept while the dialog closes); null before any. */
  entry: ConnectorEntry | null;
  state: EntryState | null;
  credential: ContextConnectorCredential | null;
  /** The enterprise level holds an account for it. */
  orgAccount: boolean;
  shared: boolean;
  onClose: () => void;
}) {
  const { t } = useT("agents");
  const { scopeType, sceneKind, canToggle, isBusy, onToggle, onToggleShare } = props;
  const addedLabel = useAddedLabel(scopeType, sceneKind);
  const current = entry && state ? { entry, state } : null;

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        if (!next) onClose();
      }}
    >
      <DialogContent className="flex max-h-[85vh] flex-col gap-0 overflow-hidden p-0 sm:max-w-md">
        {current ? (
          <>
            <div className="flex items-center gap-3 border-b p-4 pr-12">
              <ConnectorLogo slug={current.entry.slug} size="md" />
              <div className="min-w-0 flex-1 space-y-1">
                <DialogTitle className="truncate text-title font-semibold">{current.entry.name}</DialogTitle>
                <div className="flex flex-wrap gap-1">
                  <EntryPills entry={current.entry} state={current.state} />
                </div>
              </div>
            </div>
            <div className="min-h-0 flex-1 space-y-5 overflow-y-auto p-4">
              {current.entry.id === null ? (
                <p className="text-caption text-muted-foreground text-pretty">
                  {t(($) => $.context_config.app_unavailable_note, { name: current.entry.name })}
                </p>
              ) : (
                <>
                  <section className="space-y-2">
                    <StepHeading step={1} title={t(($) => $.context_config.step_add)} />
                    {current.entry.defaultOn ? (
                      <p className="text-caption text-muted-foreground">{t(($) => $.context_config.default_on_note)}</p>
                    ) : current.state.added ? (
                      <div className="flex items-center justify-between gap-3">
                        <p className="min-w-0 text-body">{addedLabel}</p>
                        {canToggle ? (
                          <Button
                            variant="ghost"
                            size="sm"
                            className="text-muted-foreground hover:text-destructive"
                            disabled={isBusy(`connector:${current.entry.id}`)}
                            onClick={() => onToggle(current.entry.id ?? "", false)}
                          >
                            {isBusy(`connector:${current.entry.id}`) && (
                              <Loader2 className="size-3.5 animate-spin motion-reduce:animate-none" />
                            )}
                            {t(($) => $.context_config.app_remove)}
                          </Button>
                        ) : null}
                      </div>
                    ) : current.state.byOrg ? (
                      <p className="text-caption text-muted-foreground">{t(($) => $.context_config.org_on_note)}</p>
                    ) : current.entry.offered ? (
                      <div className="space-y-2">
                        <Button
                          className="h-9 w-full"
                          disabled={!canToggle || isBusy(`connector:${current.entry.id}`)}
                          onClick={() => onToggle(current.entry.id ?? "", true)}
                        >
                          {isBusy(`connector:${current.entry.id}`) && (
                            <Loader2 className="size-3.5 animate-spin motion-reduce:animate-none" />
                          )}
                          {t(($) => $.context_config.app_add)}
                        </Button>
                        {!canToggle ? (
                          <p className="text-caption text-muted-foreground">{t(($) => $.context_config.app_add_read_only)}</p>
                        ) : null}
                      </div>
                    ) : null}
                    {scopeType === "person" && canToggle && current.state.added && !current.entry.defaultOn ? (
                      <ShareInGroupsSwitch
                        connectorId={current.entry.id}
                        checked={shared}
                        busy={isBusy(`share:${current.entry.id}`)}
                        onToggle={(next) => onToggleShare(current.entry.id ?? "", next)}
                      />
                    ) : null}
                  </section>
                  {current.entry.offered && current.state.auth !== "none" ? (
                    <section className="space-y-2">
                      <StepHeading step={2} title={t(($) => $.context_config.step_auth)} />
                      {/* A stored account stays manageable even when the
                          connector no longer applies here: it still serves
                          runs where another level switches it on. */}
                      {current.state.inEffect || credential ? (
                        <ConnectorAccount
                          {...props}
                          connector={current.entry.offered}
                          credential={credential}
                          orgAccount={orgAccount}
                        />
                      ) : (
                        <p className="text-caption text-muted-foreground">{t(($) => $.context_config.auth_after_add)}</p>
                      )}
                    </section>
                  ) : null}
                  {current.entry.offered && current.entry.offered.tools.length > 0 ? (
                    <section className="space-y-2">
                      <h3 className="text-caption font-medium text-muted-foreground">
                        {t(($) => $.context_config.tools_label)}
                      </h3>
                      <div className="flex flex-wrap gap-1">
                        {current.entry.offered.tools.map((tool) => (
                          <span
                            key={tool}
                            className="max-w-full truncate rounded bg-muted px-1.5 py-0.5 font-mono text-micro text-muted-foreground"
                          >
                            {tool}
                          </span>
                        ))}
                      </div>
                    </section>
                  ) : null}
                </>
              )}
            </div>
          </>
        ) : null}
      </DialogContent>
    </Dialog>
  );
}

/** Whether a personal connector may also serve group chats the person
 * triggers. Off by default. The runtime does not read it yet: personal
 * connectors still apply in every run the person triggers, group runs
 * included (docs/context-capabilities.md §1.2), so the switch says it is
 * only saved. */
function ShareInGroupsSwitch({
  connectorId,
  checked,
  busy,
  onToggle,
}: {
  connectorId: string;
  checked: boolean;
  busy: boolean;
  onToggle: (next: boolean) => void;
}) {
  const { t } = useT("agents");
  const switchId = `context-share-${connectorId}`;
  return (
    <div className="flex items-start gap-3 rounded-md bg-muted/40 px-3 py-2.5">
      <div className="min-w-0 flex-1 space-y-0.5">
        <label htmlFor={switchId} className="block text-caption font-medium">
          {t(($) => $.context_config.share_in_groups)}
        </label>
        <p id={`${switchId}-hint`} className="text-caption text-muted-foreground">
          {t(($) => $.context_config.share_in_groups_hint)}
        </p>
        <p id={`${switchId}-pending`} className="text-caption text-muted-foreground">
          {t(($) => $.context_config.share_in_groups_pending_note)}
        </p>
      </div>
      <span className="flex h-6 w-10 shrink-0 items-center justify-end">
        {busy ? (
          <Loader2 className="size-4 animate-spin text-muted-foreground motion-reduce:animate-none" />
        ) : (
          <Switch
            id={switchId}
            size="sm"
            checked={checked}
            onCheckedChange={(next) => onToggle(next)}
            aria-describedby={`${switchId}-hint ${switchId}-pending`}
          />
        )}
      </span>
    </div>
  );
}

type AccountProps = ConnectorsSlotProps & {
  connector: ContextOfferedConnector;
  credential: ContextConnectorCredential | null;
  /** The enterprise level holds an account that serves here. */
  orgAccount: boolean;
};

/** The level's own account of a connector in effect here. */
function ConnectorAccount(props: AccountProps) {
  return props.connector.authMode === "oauth" ? <OAuthConnectionControl {...props} /> : <CredentialControl {...props} />;
}

/**
 * Official app connected through the provider's own sign-in: 授权 starts the
 * OAuth flow for this level and leaves the page; on return the stored
 * connection shows its account hint and can be revoked. GitHub also accepts a
 * Personal Access Token as a secondary option.
 */
function OAuthConnectionControl({
  agentId,
  scopeType,
  scopeKey,
  orgId,
  sceneKind,
  connector,
  credential,
  orgAccount,
  ownerOnly,
  credentialReadOnly: readOnly,
  reportError,
}: AccountProps) {
  const { t } = useT("agents");
  const credentialNote = useCredentialNote(scopeType, sceneKind);
  const { open: openAuthorizeUrl, returnTo } = useContext(ConnectPlumbingContext);
  const start = useStartContextConnectorConnection(agentId);
  const deleteCredential = useDeleteContextConnectorCredential(agentId);
  const [confirmingRemove, setConfirmingRemove] = useState(false);
  const [patOpen, setPatOpen] = useState(false);
  // Stays true after handing the page to the provider, so the button cannot
  // start a second flow while the WebView navigates away.
  const [redirecting, setRedirecting] = useState(false);
  const connecting = start.isPending || redirecting;
  useResetOnBackForwardRestore(redirecting, () => setRedirecting(false));

  // The server says whether it can run this app's sign-in (GitHub needs the
  // GitHub App client credentials); without it a Personal Access Token is
  // the only way to connect.
  const oauthReady = Boolean(openAuthorizeUrl) && connector.oauthAvailable;

  const connect = async () => {
    if (!openAuthorizeUrl || connecting) return;
    try {
      const url = await start.mutateAsync({
        scopeType,
        scopeKey,
        ...orgField(orgId),
        connectorId: connector.id,
        ...(returnTo ? { returnTo } : {}),
      });
      if (!url) {
        toast.error(t(($) => $.context_config.connect_failed));
        return;
      }
      setRedirecting(true);
      // The platform reopens this level (and its tenant) when the provider
      // returns.
      openAuthorizeUrl(url, { agentId, scopeType, scopeKey, ...orgField(orgId) });
    } catch (error) {
      if (reportError(error)) return;
      // Retrying cannot fix these two, so they are not reported as "try again".
      switch (errorCode(error)) {
        case "oauth_unavailable":
          toast.error(
            connector.acceptsPat
              ? t(($) => $.context_config.connect_unavailable_pat, { name: connector.name })
              : t(($) => $.context_config.connect_unavailable, { name: connector.name }),
          );
          break;
        case "forbidden":
          toast.error(t(($) => $.context_config.connect_forbidden));
          break;
        default:
          toast.error(t(($) => $.context_config.connect_failed));
      }
    }
  };

  const disconnect = async () => {
    try {
      await deleteCredential.mutateAsync({ scopeType, scopeKey, ...orgField(orgId), connectorId: connector.id });
      setConfirmingRemove(false);
      toast.success(t(($) => $.context_config.disconnected));
    } catch (error) {
      if (!reportError(error)) toast.error(t(($) => $.context_config.disconnect_failed));
    }
  };

  // OAuth hints are "@<account>" when the provider reported one, otherwise a
  // generic "OAuth"; a Personal Access Token keeps its masked "••••" hint.
  const status = credential
    ? credential.kind === "oauth" || credential.kind === "unknown"
      ? credential.hint.startsWith("@")
        ? t(($) => $.context_config.connected_as, { account: credential.hint })
        : t(($) => $.context_config.connected)
      : t(($) => $.context_config.pat_set, { hint: credential.hint || "••••" })
    : orgAccount
      ? t(($) => $.context_config.account_from_org)
      : connector.credentialRequired
        ? t(($) => $.context_config.connect_required)
        : t(($) => $.context_config.connect_optional);
  const missing = !credential && !orgAccount && connector.credentialRequired;

  return (
    <div className="space-y-2 rounded-md bg-muted/40 px-3 py-2.5">
      <div className="flex items-start gap-2">
        {credential ? (
          <Link2 className="mt-0.5 size-3.5 shrink-0 text-muted-foreground" />
        ) : (
          <KeyRound
            className={cn("mt-0.5 size-3.5 shrink-0", missing ? "text-warning" : "text-muted-foreground")}
          />
        )}
        <p
          className={cn(
            "min-w-0 flex-1 break-words text-caption",
            missing ? "text-warning" : credential ? "text-foreground" : "text-muted-foreground",
          )}
        >
          {status}
        </p>
      </div>

      {readOnly ? null : ownerOnly ? (
        <p className="text-caption text-muted-foreground">{t(($) => $.context_config.owner_connects)}</p>
      ) : patOpen ? (
        <BearerForm
          agentId={agentId}
          scopeType={scopeType}
          scopeKey={scopeKey}
          orgId={orgId}
          connectorId={connector.id}
          inputId={`context-pat-${scopeType}-${connector.id}`}
          inputLabel={t(($) => $.context_config.pat_input_label, { name: connector.name })}
          placeholder={t(($) => $.context_config.pat_placeholder)}
          note={credentialNote}
          onClose={() => setPatOpen(false)}
          reportError={reportError}
        />
      ) : confirmingRemove ? (
        <div className="flex gap-2">
          <Button
            variant="ghost"
            size="sm"
            className="flex-1"
            onClick={() => setConfirmingRemove(false)}
            disabled={deleteCredential.isPending}
          >
            {t(($) => $.context_config.cancel)}
          </Button>
          <Button
            variant="destructive"
            size="sm"
            className="flex-1"
            onClick={() => void disconnect()}
            disabled={deleteCredential.isPending}
          >
            {deleteCredential.isPending && <Loader2 className="size-3.5 animate-spin motion-reduce:animate-none" />}
            {t(($) => $.context_config.confirm_disconnect)}
          </Button>
        </div>
      ) : credential ? (
        <div className="flex flex-wrap gap-2">
          <Button
            variant="ghost"
            size="sm"
            className="text-muted-foreground hover:text-destructive"
            onClick={() => setConfirmingRemove(true)}
          >
            {t(($) => $.context_config.disconnect)}
          </Button>
        </div>
      ) : (
        <div className="space-y-2">
          <div className="flex flex-wrap items-center gap-2">
            {oauthReady && (
              <Button size="sm" onClick={() => void connect()} disabled={connecting}>
                {connecting && <Loader2 className="size-3.5 animate-spin motion-reduce:animate-none" />}
                {connecting ? t(($) => $.context_config.connecting) : t(($) => $.context_config.connect)}
              </Button>
            )}
            {connector.acceptsPat && (
              <Button
                variant={oauthReady ? "ghost" : "default"}
                size="sm"
                onClick={() => setPatOpen(true)}
                disabled={connecting}
              >
                {oauthReady ? t(($) => $.context_config.use_pat) : t(($) => $.context_config.pat_connect)}
              </Button>
            )}
          </div>
          {!connector.oauthAvailable && (
            <p className="text-caption text-muted-foreground">
              {connector.acceptsPat
                ? t(($) => $.context_config.connect_unavailable_pat, { name: connector.name })
                : t(($) => $.context_config.connect_unavailable, { name: connector.name })}
            </p>
          )}
          <p className="text-caption text-muted-foreground">
            {scopeType === "org"
              ? t(($) => $.context_config.connect_org_note)
              : scopeType === "scene"
                ? sceneKind === "dm"
                  ? t(($) => $.context_config.connect_dm_note, { name: connector.name })
                  : t(($) => $.context_config.connect_scene_note, { name: connector.name })
                : t(($) => $.context_config.connect_person_note)}
          </p>
        </div>
      )}

      {connector.catalogSlug === "github" && !readOnly && (
        <p className="text-caption text-muted-foreground">
          {t(($) => $.context_config.install_hint)}
          {connector.installUrl && (
            <>
              {" "}
              <a
                href={connector.installUrl}
                target="_blank"
                rel="noopener noreferrer"
                className="inline-flex items-center gap-1 font-medium text-foreground underline-offset-4 hover:underline"
              >
                {t(($) => $.context_config.install_link)}
                <ExternalLink className="size-3" />
              </a>
            </>
          )}
        </p>
      )}
    </div>
  );
}

function CredentialControl({
  agentId,
  scopeType,
  scopeKey,
  orgId,
  sceneKind,
  connector,
  credential,
  orgAccount,
  ownerOnly,
  credentialReadOnly: readOnly,
  reportError,
}: AccountProps) {
  const { t } = useT("agents");
  const credentialNote = useCredentialNote(scopeType, sceneKind);
  const deleteCredential = useDeleteContextConnectorCredential(agentId);
  const [editing, setEditing] = useState(false);
  const [confirmingRemove, setConfirmingRemove] = useState(false);

  const remove = async () => {
    try {
      await deleteCredential.mutateAsync({ scopeType, scopeKey, ...orgField(orgId), connectorId: connector.id });
      setConfirmingRemove(false);
      toast.success(t(($) => $.context_config.credential_removed));
    } catch (error) {
      if (!reportError(error)) toast.error(t(($) => $.context_config.credential_remove_failed));
    }
  };

  const missing = !credential && !orgAccount && connector.credentialRequired;
  const status = credential
    ? t(($) => $.context_config.credential_set, { hint: credential.hint || "••••" })
    : orgAccount
      ? t(($) => $.context_config.account_from_org)
      : connector.credentialRequired
        ? t(($) => $.context_config.credential_required)
        : t(($) => $.context_config.credential_optional);

  return (
    <div className="space-y-2 rounded-md bg-muted/40 px-3 py-2.5">
      <div className="flex items-start gap-2">
        <KeyRound className={cn("mt-0.5 size-3.5 shrink-0", missing ? "text-warning" : "text-muted-foreground")} />
        <p className={cn("min-w-0 flex-1 text-caption", missing ? "text-warning" : "text-muted-foreground")}>
          {status}
        </p>
      </div>

      {readOnly ? null : ownerOnly ? (
        <p className="text-caption text-muted-foreground">{t(($) => $.context_config.owner_connects)}</p>
      ) : editing ? (
        <BearerForm
          agentId={agentId}
          scopeType={scopeType}
          scopeKey={scopeKey}
          orgId={orgId}
          connectorId={connector.id}
          inputId={`context-credential-${scopeType}-${connector.id}`}
          inputLabel={t(($) => $.context_config.credential_input_label, { name: connector.name })}
          placeholder={t(($) => $.context_config.credential_placeholder)}
          note={credentialNote}
          onClose={() => setEditing(false)}
          reportError={reportError}
        />
      ) : confirmingRemove ? (
        <div className="flex gap-2">
          <Button
            variant="ghost"
            size="sm"
            className="flex-1"
            onClick={() => setConfirmingRemove(false)}
            disabled={deleteCredential.isPending}
          >
            {t(($) => $.context_config.cancel)}
          </Button>
          <Button
            variant="destructive"
            size="sm"
            className="flex-1"
            onClick={() => void remove()}
            disabled={deleteCredential.isPending}
          >
            {deleteCredential.isPending && <Loader2 className="size-3.5 animate-spin motion-reduce:animate-none" />}
            {t(($) => $.context_config.confirm_remove)}
          </Button>
        </div>
      ) : (
        <div className="flex flex-wrap gap-2">
          <Button variant="outline" size="sm" onClick={() => setEditing(true)}>
            {credential ? t(($) => $.context_config.replace_credential) : t(($) => $.context_config.set_credential)}
          </Button>
          {credential && (
            <Button
              variant="ghost"
              size="sm"
              className="text-muted-foreground hover:text-destructive"
              onClick={() => setConfirmingRemove(true)}
            >
              {t(($) => $.context_config.remove_credential)}
            </Button>
          )}
        </div>
      )}
    </div>
  );
}

/** Note under a token input: who the stored token serves. */
function useCredentialNote(scopeType: ContextWriteScopeType, sceneKind: ContextSceneKind): string {
  const { t } = useT("agents");
  switch (scopeType) {
    case "org":
      return t(($) => $.context_config.credential_org_note);
    case "scene":
      return sceneKind === "dm"
        ? t(($) => $.context_config.credential_dm_note)
        : t(($) => $.context_config.credential_scene_note);
    default:
      return t(($) => $.context_config.credential_person_note);
  }
}

/** `org_id` of a scope request: only for a tenant other than the agent's
 * own org, which is the server's default. */
export function orgField(orgId: string): { orgId?: string } {
  return orgId ? { orgId } : {};
}

/** Write-only token input for an enterprise, scene or personal credential
 * (Bearer, or a Personal Access Token for an OAuth connector that accepts
 * one). */
function BearerForm({
  agentId,
  scopeType,
  scopeKey,
  orgId,
  connectorId,
  inputId,
  inputLabel,
  placeholder,
  note,
  onClose,
  reportError,
}: {
  agentId: string;
  scopeType: ContextWriteScopeType;
  scopeKey: string;
  orgId: string;
  connectorId: string;
  inputId: string;
  inputLabel: string;
  placeholder: string;
  note: string;
  onClose: () => void;
  reportError: (error: unknown) => boolean;
}) {
  const { t } = useT("agents");
  const setCredential = useSetContextConnectorCredential(agentId);
  const [bearer, setBearer] = useState("");
  const [invalid, setInvalid] = useState(false);

  const save = async () => {
    const value = bearer.trim();
    if (!isValidBearer(value)) {
      setInvalid(true);
      return;
    }
    try {
      await setCredential.mutateAsync({ scopeType, scopeKey, ...orgField(orgId), connectorId, bearer: value });
      setBearer("");
      onClose();
      toast.success(t(($) => $.context_config.credential_saved));
    } catch (error) {
      if (!reportError(error)) toast.error(t(($) => $.context_config.credential_failed));
    } finally {
      // Drop the submitted secret from the mutation state right away.
      setCredential.reset();
    }
  };

  return (
    <div className="space-y-2">
      <label htmlFor={inputId} className="sr-only">
        {inputLabel}
      </label>
      <Input
        id={inputId}
        type="password"
        autoComplete="off"
        autoCapitalize="off"
        spellCheck={false}
        value={bearer}
        maxLength={MAX_BEARER_LENGTH}
        aria-invalid={invalid || undefined}
        placeholder={placeholder}
        onChange={(event) => {
          setBearer(event.target.value);
          setInvalid(false);
        }}
        className="h-10"
      />
      <p className={cn("text-caption", invalid ? "text-destructive" : "text-muted-foreground")}>
        {invalid ? t(($) => $.context_config.credential_invalid) : note}
      </p>
      <div className="flex gap-2">
        <Button variant="ghost" className="h-9 flex-1" onClick={onClose} disabled={setCredential.isPending}>
          {t(($) => $.context_config.cancel)}
        </Button>
        <Button className="h-9 flex-1" onClick={() => void save()} disabled={setCredential.isPending || !bearer.trim()}>
          {setCredential.isPending && <Loader2 className="size-3.5 animate-spin motion-reduce:animate-none" />}
          {t(($) => $.context_config.save)}
        </Button>
      </div>
    </div>
  );
}
