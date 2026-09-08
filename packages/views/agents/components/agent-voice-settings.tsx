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
import {
  SettingsCard,
  SettingsSection,
} from "../../settings/components/settings-layout";
import { useT } from "../../i18n";
import { CharCounter } from "./char-counter";

const PERSONA_TEMPLATE_IDS = [
  "colleague",
  "advisor",
  "assistant",
  "coordinator",
] as const;
const TONE_TEMPLATE_IDS = ["direct", "warm", "casual", "formal"] as const;

export function AgentVoiceSettings({
  agent,
  canEdit,
  onSave,
  onDirtyChange,
}: {
  agent: Agent;
  canEdit: boolean;
  onSave: (patch: { persona: string; reply_tone: string }) => Promise<void>;
  onDirtyChange?: (dirty: boolean) => void;
}) {
  const { t } = useT("agents");
  const [persona, setPersona] = useState(agent.persona ?? "");
  const [replyTone, setReplyTone] = useState(agent.reply_tone ?? "");
  const [saving, setSaving] = useState(false);
  const [extracting, setExtracting] = useState(false);
  const isDirty =
    persona !== (agent.persona ?? "") ||
    replyTone !== (agent.reply_tone ?? "");
  const overLimit =
    [...persona].length > AGENT_PERSONA_MAX_LENGTH ||
    [...replyTone].length > AGENT_REPLY_TONE_MAX_LENGTH;

  useEffect(() => {
    setPersona(agent.persona ?? "");
    setReplyTone(agent.reply_tone ?? "");
  }, [agent.id, agent.persona, agent.reply_tone]);

  useEffect(() => {
    onDirtyChange?.(isDirty);
  }, [isDirty, onDirtyChange]);

  const handleSave = async () => {
    setSaving(true);
    try {
      await onSave({ persona, reply_tone: replyTone });
    } catch {
      // The parent mutation owns the user-facing error toast.
    } finally {
      setSaving(false);
    }
  };

  const handleExtract = async () => {
    const source = (agent.instructions ?? "").trim();
    if (!source) {
      toast.error(t(($) => $.tab_body.instructions.extract_empty));
      return;
    }
    setExtracting(true);
    try {
      const extracted = await api.extractAgentVoice(agent.id, source);
      const nextPersona = extracted.persona.trim();
      const nextTone = extracted.reply_tone.trim();
      if (!nextPersona && !nextTone) {
        toast.error(t(($) => $.tab_body.instructions.extract_failed));
        return;
      }
      if (nextPersona) setPersona(extracted.persona);
      if (nextTone) setReplyTone(extracted.reply_tone);
    } catch {
      toast.error(t(($) => $.tab_body.instructions.extract_failed));
    } finally {
      setExtracting(false);
    }
  };

  return (
    <SettingsSection
      title={t(($) => $.tab_body.digital_employee.voice_title)}
      description={t(($) => $.tab_body.digital_employee.voice_hint)}
    >
      <SettingsCard>
        <div className="space-y-5 p-4">
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
            {canEdit ? (
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
            ) : null}
            <Textarea
              id={`agent-persona-${agent.id}`}
              name="agent-persona"
              autoComplete="off"
              value={persona}
              onChange={(event) => setPersona(event.target.value)}
              placeholder={t(($) => $.tab_body.instructions.persona_placeholder)}
              rows={3}
              className="min-h-20 resize-y leading-6"
              disabled={!canEdit}
            />
            <CharCounter
              length={[...persona].length}
              max={AGENT_PERSONA_MAX_LENGTH}
            />
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
            {canEdit ? (
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
            ) : null}
            <Textarea
              id={`agent-reply-tone-${agent.id}`}
              name="agent-reply-tone"
              autoComplete="off"
              value={replyTone}
              onChange={(event) => setReplyTone(event.target.value)}
              placeholder={t(($) => $.tab_body.instructions.tone_placeholder)}
              rows={3}
              className="min-h-20 resize-y leading-6"
              disabled={!canEdit}
            />
            <CharCounter
              length={[...replyTone].length}
              max={AGENT_REPLY_TONE_MAX_LENGTH}
            />
          </div>

          {canEdit ? (
            <div className="flex flex-wrap items-center justify-between gap-3">
              <Button
                type="button"
                size="sm"
                variant="outline"
                onClick={() => void handleExtract()}
                disabled={extracting}
              >
                {extracting ? (
                  <Loader2
                    className="size-3.5 animate-spin motion-reduce:animate-none"
                    aria-hidden="true"
                  />
                ) : (
                  <Sparkles className="size-3.5" aria-hidden="true" />
                )}
                {extracting
                  ? t(($) => $.tab_body.instructions.extracting)
                  : t(($) => $.tab_body.instructions.extract)}
              </Button>
              <div className="flex items-center gap-3">
                {isDirty ? (
                  <span className="text-caption text-muted-foreground">
                    {t(($) => $.tab_body.common.unsaved_changes)}
                  </span>
                ) : null}
                <Button
                  type="button"
                  size="sm"
                  onClick={() => void handleSave()}
                  disabled={!isDirty || saving || overLimit}
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
            </div>
          ) : null}
        </div>
      </SettingsCard>
    </SettingsSection>
  );
}
