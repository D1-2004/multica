"use client";

import { useRef, useState } from "react";
import { Loader2, Lock, Pencil, Plus, Trash2, X } from "lucide-react";
import { toast } from "sonner";
import {
  useSetContextConfigMcpConfig,
  type ContextConfigScopeInput,
} from "@multica/core/context-capabilities";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { cn } from "@multica/ui/lib/utils";
import { ConfirmDialog } from "../agents/components/tabs/connectors-ui";
import { useT } from "../i18n";
import { ItemGroup, ToggleControl } from "./context-config-ui";
import {
  MCP_SERVER_NAME_MAX_LENGTH,
  mcpServerProblem,
  readScopeMcpDocument,
  scopeMcpDocument,
  type McpServerProblem,
  type ScopeMcpServer,
} from "./scope-mcp";

/**
 * The scope's own MCP servers, remote URL servers only: add, edit, delete
 * and switch. Every change saves the whole document. A document the page
 * cannot save back (withheld, or holding a local server an admin added) is
 * listed read-only.
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
  // The server being edited (by name), "" while adding.
  const [editing, setEditing] = useState<string | null>(null);
  const [deleting, setDeleting] = useState<ScopeMcpServer | null>(null);
  const [busyName, setBusyName] = useState<string | null>(null);
  const idPrefix = `context-mcp-${scope.scopeType}-${scope.scopeKey}`;

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
    const next =
      editing === "" ? [...servers, server] : servers.map((entry) => (entry.name === editing ? server : entry));
    if (await write(next, server.name)) setEditing(null);
  };

  const otherNames = (name: string | null) =>
    new Set(servers.filter((server) => server.name !== name).map((server) => server.name));

  // Why a manager cannot change the list here.
  const note = redacted
    ? t(($) => $.context_config.mcp_redacted)
    : canEdit && !stored.editable
      ? t(($) => $.context_config.mcp_locked)
      : null;

  return (
    <div className="space-y-1.5">
      <ItemGroup
        label={t(($) => $.context_config.mcp_title)}
        action={
          editable && editing === null ? (
            <Button size="sm" variant="ghost" disabled={save.isPending} onClick={() => setEditing("")}>
              <Plus className="size-3.5" />
              {t(($) => $.context_config.mcp_add)}
            </Button>
          ) : null
        }
        empty={note || editing === "" ? null : t(($) => $.context_config.none)}
      >
        {servers.map((server) =>
          editing === server.name ? (
            <li key={server.name} className="p-3">
              <McpServerForm
                idPrefix={`${idPrefix}-${server.name}`}
                initial={server}
                otherNames={otherNames(server.name)}
                pending={save.isPending}
                onCancel={() => setEditing(null)}
                onSubmit={(next) => void submitForm(next)}
              />
            </li>
          ) : (
            <li key={server.name} className="flex items-start gap-2 p-3" aria-label={server.name}>
              <div className={cn("min-w-0 flex-1", !server.enabled && "opacity-60")}>
                <p className="truncate text-body font-medium">{server.name}</p>
                {server.url ? (
                  <p className="truncate font-mono text-caption text-muted-foreground">{server.url}</p>
                ) : null}
              </div>
              {editable ? (
                <div className="flex shrink-0 items-center">
                  <Button
                    variant="ghost"
                    size="icon-sm"
                    disabled={save.isPending || editing !== null}
                    aria-label={t(($) => $.context_config.mcp_edit, { name: server.name })}
                    onClick={() => setEditing(server.name)}
                  >
                    <Pencil />
                  </Button>
                  <Button
                    variant="ghost"
                    size="icon-sm"
                    disabled={save.isPending || editing !== null}
                    aria-label={t(($) => $.context_config.mcp_delete, { name: server.name })}
                    onClick={() => setDeleting(server)}
                  >
                    <Trash2 />
                  </Button>
                </div>
              ) : null}
              <ToggleControl
                busy={busyName === server.name}
                checked={server.enabled}
                disabled={!editable || save.isPending}
                label={t(($) => $.context_config.toggle_aria, { name: server.name })}
                onToggle={(enabled) =>
                  void write(
                    servers.map((entry) => (entry.name === server.name ? { ...entry, enabled } : entry)),
                    server.name,
                  )
                }
              />
            </li>
          ),
        )}
        {editing === "" ? (
          <li key="new" className="p-3">
            <McpServerForm
              idPrefix={`${idPrefix}-new`}
              initial={null}
              otherNames={otherNames(null)}
              pending={save.isPending}
              onCancel={() => setEditing(null)}
              onSubmit={(next) => void submitForm(next)}
            />
          </li>
        ) : null}
      </ItemGroup>
      {note ? (
        <p className="flex items-center gap-1.5 text-caption text-muted-foreground">
          <Lock className="size-3.5 shrink-0" />
          {note}
        </p>
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
    </div>
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
