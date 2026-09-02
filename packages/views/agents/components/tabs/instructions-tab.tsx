"use client";

import { useEffect, useState } from "react";
import { Loader2, Save, Sparkles } from "lucide-react";
import { toast } from "sonner";
import { api } from "@multica/core/api";
import {
  AGENT_PERSONA_MAX_LENGTH,
  AGENT_REPLY_TONE_MAX_LENGTH,
} from "@multica/core/agents";
import type { Agent } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { CharCounter } from "../char-counter";
import { useT } from "../../../i18n";

const PERSONA_TEMPLATE_IDS = [
  "colleague",
  "advisor",
  "assistant",
  "coordinator",
] as const;
const TONE_TEMPLATE_IDS = ["direct", "warm", "casual", "formal"] as const;

export function InstructionsTab({
  agent,
  onSave,
  onDirtyChange,
  readOnly = false,
  instructionsLocked = false,
}: {
  agent: Agent;
  onSave: (patch: {
    instructions?: string;
    persona?: string;
    reply_tone?: string;
  }) => Promise<void>;
  onDirtyChange?: (dirty: boolean) => void;
  readOnly?: boolean;
  instructionsLocked?: boolean;
}) {
  const { t } = useT("agents");
  const [value, setValue] = useState(agent.instructions ?? "");
  const [persona, setPersona] = useState(agent.persona ?? "");
  const [replyTone, setReplyTone] = useState(agent.reply_tone ?? "");
  const [saving, setSaving] = useState(false);
  const [extracting, setExtracting] = useState(false);
  const [systemOpen, setSystemOpen] = useState(false);
  const savedInstructions = agent.instructions ?? "";
  const savedPersona = agent.persona ?? "";
  const savedTone = agent.reply_tone ?? "";
  const instructionsDirty = value !== savedInstructions;
  const voiceDirty = persona !== savedPersona || replyTone !== savedTone;
  const isDirty = voiceDirty || (!instructionsLocked && instructionsDirty);
  const voiceOverLimit =
    [...persona].length > AGENT_PERSONA_MAX_LENGTH ||
    [...replyTone].length > AGENT_REPLY_TONE_MAX_LENGTH;

  // A system agent's prompt has two halves: the product half ships with the
  // backend and updates on deploy, so it is shown read-only; the editable
  // field below holds only this workspace's own notes, which no release
  // overwrites. Ordinary agents have no system half and render unchanged.
  const systemInstructions = agent.system_instructions?.trim() ?? "";
  const hasSystemLayer = systemInstructions.length > 0;
  const promptLocked = readOnly || instructionsLocked;

  useEffect(() => {
    setValue(agent.instructions ?? "");
    setPersona(agent.persona ?? "");
    setReplyTone(agent.reply_tone ?? "");
  }, [agent.id, agent.instructions, agent.persona, agent.reply_tone]);

  useEffect(() => {
    onDirtyChange?.(isDirty);
  }, [isDirty, onDirtyChange]);

  const handleSave = async () => {
    setSaving(true);
    try {
      const patch: {
        instructions?: string;
        persona?: string;
        reply_tone?: string;
      } = { persona, reply_tone: replyTone };
      if (!instructionsLocked) {
        patch.instructions = value;
      }
      await onSave(patch);
    } catch {
      // toast handled by parent
    } finally {
      setSaving(false);
    }
  };

  const handleExtract = async () => {
    const source = value.trim();
    if (source === "") {
      toast.error(t(($) => $.tab_body.instructions.extract_empty));
      return;
    }
    setExtracting(true);
    try {
      const extracted = await api.extractAgentVoice(agent.id, source);
      const nextPersona = extracted.persona.trim();
      const nextTone = extracted.reply_tone.trim();
      if (nextPersona === "" && nextTone === "") {
        toast.error(t(($) => $.tab_body.instructions.extract_failed));
        return;
      }
      if (nextPersona !== "") {
        setPersona(extracted.persona);
      }
      if (nextTone !== "") {
        setReplyTone(extracted.reply_tone);
      }
    } catch {
      toast.error(t(($) => $.tab_body.instructions.extract_failed));
    } finally {
      setExtracting(false);
    }
  };

  return (
    <div className="space-y-5">
      <p className="max-w-2xl text-pretty text-body leading-6 text-muted-foreground">
        {hasSystemLayer
          ? t(($) => $.tab_body.instructions.workspace_notes_intro)
          : t(($) => $.tab_body.instructions.intro)}
      </p>

      <div className="space-y-4 rounded-lg border p-4">
        <div className="space-y-2">
          <label
            htmlFor={`agent-persona-${agent.id}`}
            className="text-body font-medium"
          >
            {t(($) => $.tab_body.instructions.persona_label)}
          </label>
          <p className="text-caption leading-5 text-muted-foreground">
            {t(($) => $.tab_body.instructions.persona_hint)}
          </p>
          {!readOnly && (
            <div className="flex flex-wrap items-center gap-2">
              <span className="text-caption text-muted-foreground">
                {t(($) => $.tab_body.instructions.templates_label)}
              </span>
              {PERSONA_TEMPLATE_IDS.map((id) => (
                <Button
                  key={id}
                  type="button"
                  size="sm"
                  variant="outline"
                  onClick={() =>
                    setPersona(
                      t(($) => $.tab_body.instructions.persona_templates[id].text),
                    )
                  }
                >
                  {t(($) => $.tab_body.instructions.persona_templates[id].label)}
                </Button>
              ))}
            </div>
          )}
          <Textarea
            id={`agent-persona-${agent.id}`}
            name="agent-persona"
            autoComplete="off"
            value={persona}
            onChange={(event) => setPersona(event.target.value)}
            placeholder={t(($) => $.tab_body.instructions.persona_placeholder)}
            rows={3}
            className="min-h-20 resize-y leading-6"
            disabled={readOnly}
          />
          <CharCounter length={[...persona].length} max={AGENT_PERSONA_MAX_LENGTH} />
        </div>

        <div className="space-y-2">
          <label
            htmlFor={`agent-reply-tone-${agent.id}`}
            className="text-body font-medium"
          >
            {t(($) => $.tab_body.instructions.tone_label)}
          </label>
          <p className="text-caption leading-5 text-muted-foreground">
            {t(($) => $.tab_body.instructions.tone_hint)}
          </p>
          {!readOnly && (
            <div className="flex flex-wrap items-center gap-2">
              <span className="text-caption text-muted-foreground">
                {t(($) => $.tab_body.instructions.templates_label)}
              </span>
              {TONE_TEMPLATE_IDS.map((id) => (
                <Button
                  key={id}
                  type="button"
                  size="sm"
                  variant="outline"
                  onClick={() =>
                    setReplyTone(
                      t(($) => $.tab_body.instructions.tone_templates[id].text),
                    )
                  }
                >
                  {t(($) => $.tab_body.instructions.tone_templates[id].label)}
                </Button>
              ))}
            </div>
          )}
          <Textarea
            id={`agent-reply-tone-${agent.id}`}
            name="agent-reply-tone"
            autoComplete="off"
            value={replyTone}
            onChange={(event) => setReplyTone(event.target.value)}
            placeholder={t(($) => $.tab_body.instructions.tone_placeholder)}
            rows={3}
            className="min-h-20 resize-y leading-6"
            disabled={readOnly}
          />
          <CharCounter
            length={[...replyTone].length}
            max={AGENT_REPLY_TONE_MAX_LENGTH}
          />
        </div>

        {!readOnly && (
          <div className="flex justify-end">
            <Button
              type="button"
              size="sm"
              variant="outline"
              onClick={() => void handleExtract()}
              disabled={extracting}
            >
              {extracting ? (
                <Loader2
                  className="h-3.5 w-3.5 animate-spin motion-reduce:animate-none"
                  aria-hidden="true"
                />
              ) : (
                <Sparkles className="h-3.5 w-3.5" aria-hidden="true" />
              )}
              {extracting
                ? t(($) => $.tab_body.instructions.extracting)
                : t(($) => $.tab_body.instructions.extract)}
            </Button>
          </div>
        )}
      </div>

      {hasSystemLayer && (
        <div className="rounded-lg border bg-muted/30">
          <div className="flex flex-wrap items-center gap-x-3 gap-y-1 px-3 py-2.5">
            <span className="text-body font-medium">
              {t(($) => $.tab_body.instructions.system_layer_label)}
            </span>
            <p className="min-w-0 flex-1 text-caption leading-snug text-muted-foreground">
              {t(($) => $.tab_body.instructions.system_layer_hint)}
            </p>
            <Button
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
          {systemOpen && (
            <pre className="max-h-80 overflow-auto border-t px-3 py-2.5 text-caption leading-6 whitespace-pre-wrap text-muted-foreground">
              {systemInstructions}
            </pre>
          )}
        </div>
      )}

      <div className="space-y-2">
        <label
          htmlFor={`agent-system-prompt-${agent.id}`}
          className="text-body font-medium"
        >
          {hasSystemLayer
            ? t(($) => $.tab_body.instructions.workspace_notes_label)
            : t(($) => $.tab_body.instructions.system_prompt_label)}
        </label>
        {instructionsLocked && (
          <p className="text-caption leading-5 text-muted-foreground">
            {t(($) => $.tab_body.instructions.github_managed)}
          </p>
        )}
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

      {!readOnly && (
        <div className="flex items-center justify-end gap-3">
          {isDirty && (
            <span className="text-caption text-muted-foreground">
              {t(($) => $.tab_body.common.unsaved_changes)}
            </span>
          )}
          <Button
            size="sm"
            onClick={() => void handleSave()}
            disabled={!isDirty || saving || voiceOverLimit}
          >
            {saving ? (
              <Loader2
                className="h-3.5 w-3.5 animate-spin motion-reduce:animate-none"
                aria-hidden="true"
              />
            ) : (
              <Save className="h-3.5 w-3.5" aria-hidden="true" />
            )}
            {t(($) => $.tab_body.common.save)}
          </Button>
        </div>
      )}
    </div>
  );
}
