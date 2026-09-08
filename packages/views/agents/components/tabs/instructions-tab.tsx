"use client";

import { useEffect, useState } from "react";
import { Loader2, Save } from "lucide-react";
import type { Agent } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { useT } from "../../../i18n";

export function InstructionsTab({
  agent,
  onSave,
  onDirtyChange,
  readOnly = false,
  instructionsLocked = false,
}: {
  agent: Agent;
  onSave: (patch: { instructions?: string }) => Promise<void>;
  onDirtyChange?: (dirty: boolean) => void;
  readOnly?: boolean;
  instructionsLocked?: boolean;
}) {
  const { t } = useT("agents");
  const [value, setValue] = useState(agent.instructions ?? "");
  const [saving, setSaving] = useState(false);
  const [systemOpen, setSystemOpen] = useState(false);
  const systemInstructions = agent.system_instructions?.trim() ?? "";
  const hasSystemLayer = systemInstructions.length > 0;
  const promptLocked = readOnly || instructionsLocked;
  const isDirty =
    !instructionsLocked && value !== (agent.instructions ?? "");

  useEffect(() => {
    setValue(agent.instructions ?? "");
  }, [agent.id, agent.instructions]);

  useEffect(() => {
    onDirtyChange?.(isDirty);
  }, [isDirty, onDirtyChange]);

  const handleSave = async () => {
    setSaving(true);
    try {
      await onSave({ instructions: value });
    } catch {
      // The parent mutation owns the user-facing error toast.
    } finally {
      setSaving(false);
    }
  };

  return (
    <div className="space-y-5">
      <p className="max-w-2xl text-pretty text-body leading-6 text-muted-foreground">
        {hasSystemLayer
          ? t(($) => $.tab_body.instructions.workspace_notes_intro)
          : t(($) => $.tab_body.instructions.intro)}
      </p>

      {hasSystemLayer ? (
        <div className="rounded-lg border bg-muted/30">
          <div className="flex flex-wrap items-center gap-x-3 gap-y-1 px-3 py-2.5">
            <span className="text-body font-medium">
              {t(($) => $.tab_body.instructions.system_layer_label)}
            </span>
            <p className="min-w-0 flex-1 text-caption leading-snug text-muted-foreground">
              {t(($) => $.tab_body.instructions.system_layer_hint)}
            </p>
            <Button
              type="button"
              size="sm"
              variant="ghost"
              className="shrink-0"
              aria-expanded={systemOpen}
              onClick={() => setSystemOpen((open) => !open)}
            >
              {systemOpen
                ? t(($) => $.tab_body.instructions.system_layer_hide)
                : t(($) => $.tab_body.instructions.system_layer_show)}
            </Button>
          </div>
          {systemOpen ? (
            <pre className="max-h-80 overflow-auto border-t px-3 py-2.5 text-caption leading-6 whitespace-pre-wrap text-muted-foreground">
              {systemInstructions}
            </pre>
          ) : null}
        </div>
      ) : null}

      <div className="space-y-2">
        <label
          htmlFor={`agent-system-prompt-${agent.id}`}
          className="text-body font-medium"
        >
          {hasSystemLayer
            ? t(($) => $.tab_body.instructions.workspace_notes_label)
            : t(($) => $.tab_body.instructions.system_prompt_label)}
        </label>
        {instructionsLocked ? (
          <p className="text-caption leading-5 text-muted-foreground">
            {t(($) => $.tab_body.instructions.github_managed)}
          </p>
        ) : null}
        <Textarea
          id={`agent-system-prompt-${agent.id}`}
          name="agent-system-prompt"
          autoComplete="off"
          value={value}
          onChange={(event) => setValue(event.target.value)}
          placeholder={
            hasSystemLayer
              ? t(($) => $.tab_body.instructions.workspace_notes_placeholder)
              : t(($) => $.tab_body.instructions.placeholder)
          }
          rows={18}
          className="min-h-96 resize-y leading-6"
          disabled={promptLocked}
        />
      </div>

      {!readOnly && !instructionsLocked ? (
        <div className="flex items-center justify-end gap-3">
          {isDirty ? (
            <span className="text-caption text-muted-foreground">
              {t(($) => $.tab_body.common.unsaved_changes)}
            </span>
          ) : null}
          <Button
            type="button"
            size="sm"
            onClick={() => void handleSave()}
            disabled={!isDirty || saving}
          >
            {saving ? (
              <Loader2
                className="size-3.5 animate-spin motion-reduce:animate-none"
                aria-hidden="true"
              />
            ) : (
              <Save className="size-3.5" aria-hidden="true" />
            )}
            {t(($) => $.tab_body.common.save)}
          </Button>
        </div>
      ) : null}
    </div>
  );
}
