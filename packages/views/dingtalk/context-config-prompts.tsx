"use client";

import { useState } from "react";
import { Loader2, Pencil, Plus, Trash2 } from "lucide-react";
import { toast } from "sonner";
import {
  useSetContextConfigPrompts,
  type ContextConfigScopeInput,
  type ContextPromptComponent,
} from "@multica/core/context-capabilities";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { cn } from "@multica/ui/lib/utils";
import { ConfirmDialog } from "../agents/components/tabs/connectors-ui";
import {
  PROMPT_COMPONENT_MAX,
  PROMPT_NAME_MAX_LENGTH,
  promptProblem,
  usePromptProblemMessage,
} from "../common/context-prompt-rules";
import { useT } from "../i18n";
import { ItemGroup, ToggleControl } from "./context-config-ui";

interface PromptRow {
  key: string;
  name: string;
  text: string;
  enabled: boolean;
}

function rowsOf(prompts: ContextPromptComponent[]): PromptRow[] {
  return prompts.map((prompt, index) => ({
    key: prompt.id || `${prompt.name}#${index}`,
    name: prompt.name,
    text: prompt.text,
    enabled: prompt.enabled !== false,
  }));
}

/**
 * The scope's own prompt components: add, edit, delete and switch. Every
 * change saves the whole list at once (the server replaces it), so one write
 * runs at a time. Read-only without `canEdit`.
 */
export function ScopePrompts({
  agentId,
  scope,
  prompts,
  canEdit,
  reportError,
}: {
  agentId: string;
  scope: ContextConfigScopeInput;
  prompts: ContextPromptComponent[];
  canEdit: boolean;
  reportError: (error: unknown) => boolean;
}) {
  const { t } = useT("agents");
  const save = useSetContextConfigPrompts(agentId);
  const rows = rowsOf(prompts);
  // The row being edited, "new" while adding.
  const [editing, setEditing] = useState<string | null>(null);
  const [deleting, setDeleting] = useState<PromptRow | null>(null);
  const [busyKey, setBusyKey] = useState<string | null>(null);
  const idPrefix = `context-prompt-${scope.scopeType}-${scope.scopeKey}`;

  if (!canEdit && rows.length === 0) return null;

  const write = async (next: PromptRow[], busy: string): Promise<boolean> => {
    setBusyKey(busy);
    try {
      await save.mutateAsync({
        ...scope,
        prompts: next.map((row, index) => ({ name: row.name, order: index + 1, text: row.text, enabled: row.enabled })),
      });
      return true;
    } catch (error) {
      if (!reportError(error)) toast.error(t(($) => $.context_config.prompts_failed));
      return false;
    } finally {
      setBusyKey(null);
    }
  };

  const submitForm = async (name: string, text: string) => {
    const next =
      editing === "new"
        ? [...rows, { key: "new", name, text, enabled: true }]
        : rows.map((row) => (row.key === editing ? { ...row, name, text } : row));
    if (await write(next, editing ?? "new")) setEditing(null);
  };

  const otherNames = (key: string | null) =>
    new Set(rows.filter((row) => row.key !== key).map((row) => row.name));

  return (
    <>
      <ItemGroup
        label={t(($) => $.context_config.prompts_title)}
        action={
          canEdit && editing === null && rows.length < PROMPT_COMPONENT_MAX ? (
            <Button size="sm" variant="ghost" disabled={save.isPending} onClick={() => setEditing("new")}>
              <Plus className="size-3.5" />
              {t(($) => $.context_config.prompt_add)}
            </Button>
          ) : null
        }
        empty={editing === "new" ? null : t(($) => $.context_config.none)}
      >
        {rows.map((row) =>
          editing === row.key ? (
            <li key={row.key} className="p-3">
              <PromptForm
                idPrefix={`${idPrefix}-${row.key}`}
                initial={row}
                otherNames={otherNames(row.key)}
                pending={save.isPending}
                onCancel={() => setEditing(null)}
                onSubmit={(name, text) => void submitForm(name, text)}
              />
            </li>
          ) : (
            <li key={row.key} className="flex items-start gap-2 p-3" aria-label={row.name}>
              <div className={cn("min-w-0 flex-1", !row.enabled && "opacity-60")}>
                <p className="truncate text-body font-medium">{row.name}</p>
                <p className="line-clamp-2 whitespace-pre-wrap break-words text-caption text-muted-foreground">
                  {row.text}
                </p>
              </div>
              {canEdit ? (
                <div className="flex shrink-0 items-center">
                  <Button
                    variant="ghost"
                    size="icon-sm"
                    disabled={save.isPending || editing !== null}
                    aria-label={t(($) => $.context_config.prompt_edit, { name: row.name })}
                    onClick={() => setEditing(row.key)}
                  >
                    <Pencil />
                  </Button>
                  <Button
                    variant="ghost"
                    size="icon-sm"
                    disabled={save.isPending || editing !== null}
                    aria-label={t(($) => $.context_config.prompt_delete, { name: row.name })}
                    onClick={() => setDeleting(row)}
                  >
                    <Trash2 />
                  </Button>
                </div>
              ) : null}
              <ToggleControl
                busy={busyKey === row.key}
                checked={row.enabled}
                disabled={!canEdit || save.isPending}
                label={t(($) => $.context_config.toggle_aria, { name: row.name })}
                onToggle={(enabled) =>
                  void write(
                    rows.map((entry) => (entry.key === row.key ? { ...entry, enabled } : entry)),
                    row.key,
                  )
                }
              />
            </li>
          ),
        )}
        {editing === "new" ? (
          <li key="new" className="p-3">
            <PromptForm
              idPrefix={`${idPrefix}-new`}
              initial={null}
              otherNames={otherNames(null)}
              pending={save.isPending}
              onCancel={() => setEditing(null)}
              onSubmit={(name, text) => void submitForm(name, text)}
            />
          </li>
        ) : null}
      </ItemGroup>
      {canEdit ? (
        <ConfirmDialog
          open={deleting !== null}
          onOpenChange={(open) => {
            if (!open) setDeleting(null);
          }}
          title={t(($) => $.context_config.prompt_delete_title, { name: deleting?.name ?? "" })}
          description={t(($) => $.context_config.delete_description)}
          confirmLabel={t(($) => $.context_config.prompt_delete, { name: deleting?.name ?? "" })}
          pending={save.isPending}
          onConfirm={() => {
            const target = deleting;
            if (!target) return;
            void write(
              rows.filter((row) => row.key !== target.key),
              target.key,
            ).then((ok) => {
              if (ok) setDeleting(null);
            });
          }}
        />
      ) : null}
    </>
  );
}

function PromptForm({
  idPrefix,
  initial,
  otherNames,
  pending,
  onCancel,
  onSubmit,
}: {
  idPrefix: string;
  /** The component being edited; null adds one. */
  initial: PromptRow | null;
  otherNames: ReadonlySet<string>;
  pending: boolean;
  onCancel: () => void;
  onSubmit: (name: string, text: string) => void;
}) {
  const { t } = useT("agents");
  const problemMessage = usePromptProblemMessage();
  const [name, setName] = useState(initial?.name ?? "");
  const [text, setText] = useState(initial?.text ?? "");
  const [touched, setTouched] = useState(false);
  const problem = promptProblem(name.trim(), text.trim(), otherNames);

  return (
    <form
      className="space-y-2"
      onSubmit={(event) => {
        event.preventDefault();
        setTouched(true);
        if (problem) return;
        onSubmit(name.trim(), text.trim());
      }}
    >
      <label htmlFor={`${idPrefix}-name`} className="block text-caption font-medium">
        {t(($) => $.context_config.prompt_name)}
      </label>
      <Input
        id={`${idPrefix}-name`}
        value={name}
        maxLength={PROMPT_NAME_MAX_LENGTH}
        autoComplete="off"
        onChange={(event) => setName(event.target.value)}
        className="h-10"
      />
      <label htmlFor={`${idPrefix}-text`} className="block text-caption font-medium">
        {t(($) => $.context_config.prompt_text)}
      </label>
      <Textarea
        id={`${idPrefix}-text`}
        value={text}
        rows={5}
        onChange={(event) => setText(event.target.value)}
        className="min-h-28 text-body"
      />
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
