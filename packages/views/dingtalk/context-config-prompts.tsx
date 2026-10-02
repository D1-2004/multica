"use client";

import { useState } from "react";
import { Loader2, Plus } from "lucide-react";
import { toast } from "sonner";
import {
  useSetContextConfigPrompts,
  type ContextConfigScopeInput,
  type ContextPromptComponent,
} from "@multica/core/context-capabilities";
import { Button } from "@multica/ui/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import { Input } from "@multica/ui/components/ui/input";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { ConfirmDialog } from "../agents/components/tabs/connectors-ui";
import {
  PROMPT_COMPONENT_MAX,
  PROMPT_NAME_MAX_LENGTH,
  promptProblem,
  usePromptProblemMessage,
} from "../common/context-prompt-rules";
import { useT } from "../i18n";
import { ConfigList, ConfigRow, SlotHeading, ToggleControl } from "./context-config-ui";

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

/** What the prompt dialog shows: one component, its editor, or a new one. */
type PromptDialogState = { kind: "view" | "edit"; key: string } | { kind: "new" } | null;

/**
 * 指令: the scope's own prompt components, the level's first capability
 * slot. One line each with its switch; a row opens the full text, where it
 * is edited or deleted; 添加 opens an empty editor. Every change saves the
 * whole list at once (the server replaces it), so one write runs at a time.
 * Read-only without `canEdit`.
 */
export function ScopePrompts({
  agentId,
  scope,
  prompts,
  canEdit,
  displayOnly = false,
  reportError,
}: {
  agentId: string;
  scope: ContextConfigScopeInput;
  prompts: ContextPromptComponent[];
  canEdit: boolean;
  /** Lists the components that apply, without their switches. */
  displayOnly?: boolean;
  reportError: (error: unknown) => boolean;
}) {
  const { t } = useT("agents");
  const save = useSetContextConfigPrompts(agentId);
  const rows = rowsOf(prompts);
  const [dialog, setDialog] = useState<PromptDialogState>(null);
  const [deleting, setDeleting] = useState<PromptRow | null>(null);
  const [busyKey, setBusyKey] = useState<string | null>(null);
  const idPrefix = `context-prompt-${scope.scopeType}-${scope.scopeKey}`;
  const current = dialog && dialog.kind !== "new" ? (rows.find((row) => row.key === dialog.key) ?? null) : null;

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
    const editingKey = dialog?.kind === "edit" ? dialog.key : null;
    const next =
      dialog?.kind === "new"
        ? [...rows, { key: "new", name, text, enabled: true }]
        : rows.map((row) => (row.key === editingKey ? { ...row, name, text } : row));
    if (await write(next, editingKey ?? "new")) setDialog(null);
  };

  const otherNames = (key: string | null) =>
    new Set(rows.filter((row) => row.key !== key).map((row) => row.name));

  return (
    <section className="space-y-2" aria-label={t(($) => $.context_config.prompts_title)}>
      <SlotHeading
        label={t(($) => $.context_config.prompts_title)}
        action={
          canEdit && rows.length < PROMPT_COMPONENT_MAX ? (
            <Button size="sm" variant="ghost" disabled={save.isPending} onClick={() => setDialog({ kind: "new" })}>
              <Plus className="size-3.5" />
              {t(($) => $.context_config.prompt_add)}
            </Button>
          ) : null
        }
      />
      <ConfigList label={t(($) => $.context_config.prompts_title)} empty={t(($) => $.context_config.none)}>
        {rows.map((row) => (
          <ConfigRow
            key={row.key}
            name={row.name}
            muted={!row.enabled}
            openLabel={row.name}
            onOpen={() => setDialog({ kind: "view", key: row.key })}
            action={
              displayOnly ? null : (
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
              )
            }
          />
        ))}
      </ConfigList>
      <Dialog
        open={dialog !== null && (dialog.kind === "new" || current !== null)}
        onOpenChange={(open) => {
          if (!open) setDialog(null);
        }}
      >
        <DialogContent className="sm:max-w-lg">
          {dialog?.kind === "view" && current ? (
            <>
              <DialogHeader>
                <DialogTitle className="break-words">{current.name}</DialogTitle>
                <DialogDescription>
                  {current.enabled
                    ? t(($) => $.context_config.prompt_on)
                    : t(($) => $.context_config.prompt_off)}
                </DialogDescription>
              </DialogHeader>
              <p className="max-h-[50vh] overflow-y-auto whitespace-pre-wrap break-words text-body">{current.text}</p>
              {canEdit ? (
                <DialogFooter className="flex-row justify-end gap-2">
                  <Button
                    variant="ghost"
                    className="text-muted-foreground hover:text-destructive"
                    onClick={() => {
                      setDeleting(current);
                      setDialog(null);
                    }}
                  >
                    {t(($) => $.context_config.action_delete)}
                  </Button>
                  <Button onClick={() => setDialog({ kind: "edit", key: current.key })}>
                    {t(($) => $.context_config.action_edit)}
                  </Button>
                </DialogFooter>
              ) : null}
            </>
          ) : dialog?.kind === "new" || (dialog?.kind === "edit" && current) ? (
            <>
              <DialogHeader>
                <DialogTitle>
                  {dialog.kind === "new"
                    ? t(($) => $.context_config.prompt_add)
                    : t(($) => $.context_config.prompt_edit, { name: current?.name ?? "" })}
                </DialogTitle>
              </DialogHeader>
              <PromptForm
                // A new dialog starts from the component it edits.
                key={dialog.kind === "new" ? "new" : dialog.key}
                idPrefix={`${idPrefix}-${dialog.kind === "new" ? "new" : dialog.key}`}
                initial={dialog.kind === "new" ? null : current}
                otherNames={otherNames(dialog.kind === "new" ? null : dialog.key)}
                pending={save.isPending}
                onCancel={() => setDialog(null)}
                onSubmit={(name, text) => void submitForm(name, text)}
              />
            </>
          ) : null}
        </DialogContent>
      </Dialog>
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
    </section>
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
        rows={6}
        onChange={(event) => setText(event.target.value)}
        className="min-h-32 text-body"
      />
      {touched && problem ? (
        <p role="alert" className="text-caption text-destructive">
          {problemMessage(problem)}
        </p>
      ) : null}
      <div className="flex gap-2 pt-1">
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
