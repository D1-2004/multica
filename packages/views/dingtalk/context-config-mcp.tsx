"use client";

import { useRef, useState } from "react";
import { Loader2, Lock, Plus, Trash2, X } from "lucide-react";
import { toast } from "sonner";
import {
  useSetContextConfigMcpConfig,
  type ContextConfigScopeInput,
} from "@multica/core/context-capabilities";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Dialog, DialogContent, DialogHeader, DialogTitle } from "@multica/ui/components/ui/dialog";
import { ConfirmDialog, StatusPill } from "../agents/components/tabs/connectors-ui";
import { ConnectorLogo } from "../common/connector-logo";
import { useT } from "../i18n";
import { ConfigList, ConfigRow, SlotHeading, ToggleControl } from "./context-config-ui";
import {
  MCP_SERVER_NAME_MAX_LENGTH,
  mcpServerProblem,
  readScopeMcpDocument,
  scopeMcpDocument,
  type McpServerProblem,
  type ScopeMcpServer,
} from "./scope-mcp";

/** What the MCP dialog shows: an existing server (by name) or a new one. */
type McpDialogState = { kind: "edit"; name: string } | { kind: "new" } | null;

/** The host of a server URL, for its one-line row. */
function mcpHost(url: string): string {
  try {
    return new URL(url).host;
  } catch {
    return url;
  }
}

/**
 * MCP 服务器: the scope's own remote servers, a section like 连接器和插件:
 * one line each, 配置 opens its settings (address, headers, on/off,
 * delete) and 添加 opens an empty one. Remote URL servers only; every
 * change saves the whole document. A document the page cannot save back
 * (withheld, or holding a local server an admin added) is listed read-only.
 */
export function ScopeMcpServers({
  agentId,
  scope,
  mcpConfig,
  redacted,
  canEdit,
  reportError,
}: {
  agentId: string;
  scope: ContextConfigScopeInput;
  mcpConfig: Record<string, unknown> | null;
  redacted: boolean;
  canEdit: boolean;
  reportError: (error: unknown) => boolean;
}) {
  const { t } = useT("agents");
  const save = useSetContextConfigMcpConfig(agentId);
  const stored = readScopeMcpDocument(redacted ? null : mcpConfig);
  const servers = stored.servers;
  const editable = canEdit && !redacted && stored.editable;
  const [dialog, setDialog] = useState<McpDialogState>(null);
  const [deleting, setDeleting] = useState<ScopeMcpServer | null>(null);
  const [busyName, setBusyName] = useState<string | null>(null);
  const idPrefix = `context-mcp-${scope.scopeType}-${scope.scopeKey}`;
  const current = dialog?.kind === "edit" ? (servers.find((server) => server.name === dialog.name) ?? null) : null;

  if (!canEdit && !redacted && servers.length === 0) return null;

  const write = async (next: ScopeMcpServer[], busy: string): Promise<boolean> => {
    setBusyName(busy);
    try {
      await save.mutateAsync({ ...scope, mcpConfig: scopeMcpDocument(next) });
      return true;
    } catch (error) {
      if (!reportError(error)) toast.error(t(($) => $.context_config.mcp_failed));
      return false;
    } finally {
      // Drop the submitted headers from the mutation state right away.
      save.reset();
      setBusyName(null);
    }
  };

  const submitForm = async (server: ScopeMcpServer) => {
    const editing = dialog?.kind === "edit" ? dialog.name : null;
    const next = editing === null ? [...servers, server] : servers.map((entry) => (entry.name === editing ? server : entry));
    if (await write(next, server.name)) setDialog(null);
  };

  const otherNames = (name: string | null) =>
    new Set(servers.filter((server) => server.name !== name).map((server) => server.name));

  // Why the list cannot be changed here.
  const note = redacted
    ? t(($) => $.context_config.mcp_redacted)
    : canEdit && !stored.editable
      ? t(($) => $.context_config.mcp_locked)
      : null;

  return (
    <section className="space-y-2" aria-label={t(($) => $.context_config.mcp_title)}>
      <SlotHeading
        label={t(($) => $.context_config.mcp_title)}
        action={
          editable ? (
            <Button size="sm" variant="ghost" disabled={save.isPending} onClick={() => setDialog({ kind: "new" })}>
              <Plus className="size-3.5" />
              {t(($) => $.context_config.mcp_add)}
            </Button>
          ) : null
        }
      />
      <ConfigList label={t(($) => $.context_config.mcp_title)} empty={note ? null : t(($) => $.context_config.none)}>
        {servers.map((server) => (
          <ConfigRow
            key={server.name}
            icon={<ConnectorLogo slug="" />}
            name={server.name}
            muted={!server.enabled}
            status={
              !server.enabled ? (
                <StatusPill tone="muted">{t(($) => $.context_config.mcp_off)}</StatusPill>
              ) : server.url ? (
                <span className="max-w-28 truncate text-caption text-muted-foreground">{mcpHost(server.url)}</span>
              ) : null
            }
            action={
              editable ? (
                <div className="flex items-center gap-1.5">
                  <Button
                    size="sm"
                    variant="outline"
                    aria-label={t(($) => $.context_config.mcp_edit, { name: server.name })}
                    disabled={save.isPending}
                    onClick={() => setDialog({ kind: "edit", name: server.name })}
                  >
                    {t(($) => $.context_config.app_configure)}
                  </Button>
                  <Button
                    size="icon-sm"
                    variant="ghost"
                    className="text-muted-foreground hover:text-destructive"
                    aria-label={t(($) => $.context_config.mcp_delete, { name: server.name })}
                    disabled={save.isPending}
                    onClick={() => setDeleting(server)}
                  >
                    <Trash2 className="size-3.5" />
                  </Button>
                </div>
              ) : null
            }
          />
        ))}
      </ConfigList>
      {note ? (
        <p className="flex items-center gap-1.5 text-caption text-muted-foreground">
          <Lock className="size-3.5 shrink-0" />
          {note}
        </p>
      ) : null}
      {editable ? (
        <Dialog
          open={dialog !== null && (dialog.kind === "new" || current !== null)}
          onOpenChange={(open) => {
            if (!open) setDialog(null);
          }}
        >
          <DialogContent className="flex max-h-[85vh] flex-col gap-0 overflow-hidden p-0 sm:max-w-md">
            <DialogHeader className="border-b p-4 pr-12 text-left">
              <DialogTitle className="truncate">
                {dialog?.kind === "edit" && current ? current.name : t(($) => $.context_config.mcp_add)}
              </DialogTitle>
            </DialogHeader>
            <div className="min-h-0 flex-1 space-y-4 overflow-y-auto p-4">
              {current ? (
                <div className="flex items-center justify-between gap-3">
                  <span className="text-body">{t(($) => $.context_config.mcp_enabled)}</span>
                  <ToggleControl
                    busy={busyName === current.name}
                    checked={current.enabled}
                    disabled={save.isPending}
                    label={t(($) => $.context_config.toggle_aria, { name: current.name })}
                    onToggle={(enabled) =>
                      void write(
                        servers.map((entry) => (entry.name === current.name ? { ...entry, enabled } : entry)),
                        current.name,
                      )
                    }
                  />
                </div>
              ) : null}
              {dialog ? (
                <McpServerForm
                  // A new dialog starts from the server it edits.
                  key={dialog.kind === "edit" ? dialog.name : "new"}
                  idPrefix={`${idPrefix}-${dialog.kind === "edit" ? dialog.name : "new"}`}
                  initial={current}
                  otherNames={otherNames(current?.name ?? null)}
                  pending={save.isPending}
                  onCancel={() => setDialog(null)}
                  onSubmit={(next) => void submitForm(next)}
                />
              ) : null}
              {current ? (
                <Button
                  variant="ghost"
                  size="sm"
                  className="text-muted-foreground hover:text-destructive"
                  disabled={save.isPending}
                  onClick={() => {
                    setDeleting(current);
                    setDialog(null);
                  }}
                >
                  {t(($) => $.context_config.mcp_delete, { name: current.name })}
                </Button>
              ) : null}
            </div>
          </DialogContent>
        </Dialog>
      ) : null}
      {editable ? (
        <ConfirmDialog
          open={deleting !== null}
          onOpenChange={(open) => {
            if (!open) setDeleting(null);
          }}
          title={t(($) => $.context_config.mcp_delete_title, { name: deleting?.name ?? "" })}
          description={t(($) => $.context_config.delete_description)}
          confirmLabel={t(($) => $.context_config.mcp_delete, { name: deleting?.name ?? "" })}
          pending={save.isPending}
          onConfirm={() => {
            const target = deleting;
            if (!target) return;
            void write(
              servers.filter((server) => server.name !== target.name),
              target.name,
            ).then((ok) => {
              if (ok) setDeleting(null);
            });
          }}
        />
      ) : null}
    </section>
  );
}

function useMcpProblemMessage(): (problem: McpServerProblem) => string {
  const { t } = useT("agents");
  return (problem) => {
    switch (problem) {
      case "name_required":
        return t(($) => $.context_config.mcp_name_required);
      case "name_invalid":
        return t(($) => $.context_config.mcp_name_invalid);
      case "name_reserved":
        return t(($) => $.context_config.mcp_name_reserved);
      case "name_duplicate":
        return t(($) => $.context_config.mcp_name_duplicate);
      case "url_required":
        return t(($) => $.context_config.mcp_url_required);
      case "url_invalid":
        return t(($) => $.context_config.mcp_url_invalid);
      case "header_invalid":
        return t(($) => $.context_config.mcp_header_invalid);
      default:
        return t(($) => $.context_config.mcp_url_invalid);
    }
  };
}

interface HeaderRow {
  /** Local identity of the row while the form is open. */
  id: number;
  key: string;
  value: string;
}

/** Name, URL and optional headers of one remote server. No command and no
 * environment: the configure page never adds a local server. */
function McpServerForm({
  idPrefix,
  initial,
  otherNames,
  pending,
  onCancel,
  onSubmit,
}: {
  idPrefix: string;
  /** The server being edited; null adds one. */
  initial: ScopeMcpServer | null;
  otherNames: ReadonlySet<string>;
  pending: boolean;
  onCancel: () => void;
  onSubmit: (server: ScopeMcpServer) => void;
}) {
  const { t } = useT("agents");
  const problemMessage = useMcpProblemMessage();
  const nextRowId = useRef(initial?.headers.length ?? 0);
  const [name, setName] = useState(initial?.name ?? "");
  const [url, setUrl] = useState(initial?.url ?? "");
  const [headers, setHeaders] = useState<HeaderRow[]>(
    () => initial?.headers.map((header, index) => ({ id: index, ...header })) ?? [],
  );
  const [touched, setTouched] = useState(false);
  // Rows left blank are ignored.
  const filled = headers
    .map((header) => ({ key: header.key.trim(), value: header.value }))
    .filter((header) => header.key !== "" || header.value.trim() !== "");
  const server: ScopeMcpServer = {
    name: name.trim(),
    url: url.trim(),
    type: initial?.type || "http",
    headers: filled,
    enabled: initial?.enabled ?? true,
    remote: true,
  };
  const problem = mcpServerProblem(server, otherNames);
  const updateHeader = (id: number, patch: Partial<Omit<HeaderRow, "id">>) =>
    setHeaders((current) => current.map((row) => (row.id === id ? { ...row, ...patch } : row)));

  return (
    <form
      className="space-y-2"
      onSubmit={(event) => {
        event.preventDefault();
        setTouched(true);
        if (problem) return;
        onSubmit(server);
      }}
    >
      <label htmlFor={`${idPrefix}-name`} className="block text-caption font-medium">
        {t(($) => $.context_config.mcp_name)}
      </label>
      <Input
        id={`${idPrefix}-name`}
        value={name}
        maxLength={MCP_SERVER_NAME_MAX_LENGTH}
        autoComplete="off"
        autoCapitalize="off"
        spellCheck={false}
        onChange={(event) => setName(event.target.value)}
        className="h-10"
      />
      <label htmlFor={`${idPrefix}-url`} className="block text-caption font-medium">
        {t(($) => $.context_config.mcp_url)}
      </label>
      <Input
        id={`${idPrefix}-url`}
        type="url"
        inputMode="url"
        value={url}
        autoComplete="off"
        autoCapitalize="off"
        spellCheck={false}
        placeholder="https://"
        onChange={(event) => setUrl(event.target.value)}
        className="h-10"
      />
      <p className="text-caption text-muted-foreground">{t(($) => $.context_config.mcp_remote_only)}</p>
      <div className="space-y-2">
        <p className="text-caption font-medium">{t(($) => $.context_config.mcp_headers)}</p>
        {headers.map((header) => (
          <div key={header.id} className="flex items-center gap-1.5">
            <Input
              aria-label={t(($) => $.context_config.mcp_header_name)}
              placeholder={t(($) => $.context_config.mcp_header_name)}
              value={header.key}
              autoComplete="off"
              autoCapitalize="off"
              spellCheck={false}
              onChange={(event) => updateHeader(header.id, { key: event.target.value })}
              className="h-9 min-w-0 flex-1"
            />
            <Input
              aria-label={t(($) => $.context_config.mcp_header_value)}
              placeholder={t(($) => $.context_config.mcp_header_value)}
              value={header.value}
              type="password"
              autoComplete="off"
              autoCapitalize="off"
              spellCheck={false}
              onChange={(event) => updateHeader(header.id, { value: event.target.value })}
              className="h-9 min-w-0 flex-1"
            />
            <Button
              type="button"
              variant="ghost"
              size="icon-sm"
              aria-label={t(($) => $.context_config.mcp_header_remove)}
              onClick={() => setHeaders((current) => current.filter((row) => row.id !== header.id))}
            >
              <X />
            </Button>
          </div>
        ))}
        <Button
          type="button"
          variant="ghost"
          size="sm"
          onClick={() => {
            nextRowId.current += 1;
            const id = nextRowId.current;
            setHeaders((current) => [...current, { id, key: "", value: "" }]);
          }}
        >
          <Plus className="size-3.5" />
          {t(($) => $.context_config.mcp_header_add)}
        </Button>
      </div>
      {touched && problem ? (
        <p role="alert" className="text-caption text-destructive">
          {problemMessage(problem)}
        </p>
      ) : null}
      <div className="flex gap-2">
        <Button type="button" variant="ghost" className="h-9 flex-1" disabled={pending} onClick={onCancel}>
          {t(($) => $.context_config.cancel)}
        </Button>
        <Button type="submit" className="h-9 flex-1" disabled={pending}>
          {pending && <Loader2 className="size-3.5 animate-spin motion-reduce:animate-none" />}
          {t(($) => $.context_config.save)}
        </Button>
      </div>
    </form>
  );
}
