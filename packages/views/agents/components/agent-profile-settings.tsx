"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import { AGENT_DESCRIPTION_MAX_LENGTH } from "@multica/core/agents";
import type { Agent } from "@multica/core/types";
import { Input } from "@multica/ui/components/ui/input";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { AvatarUploadControl } from "../../common/avatar-upload-control";
import {
  SettingsCard,
  SettingsRow,
  SettingsSaveState,
  SettingsSection,
} from "../../settings/components/settings-layout";
import { useAutoSave } from "../../settings/components/use-auto-save";
import { useT } from "../../i18n";
import { CharCounter } from "./char-counter";

interface ProfileDraft {
  name: string;
  description: string;
}

function profileDraftsEqual(left: ProfileDraft, right: ProfileDraft) {
  return left.name === right.name && left.description === right.description;
}

export function AgentProfileSettings({
  agent,
  canEdit,
  onUpdate,
}: {
  agent: Agent;
  canEdit: boolean;
  onUpdate: (data: Record<string, unknown>) => Promise<void>;
}) {
  const { t } = useT("agents");
  const { t: ts } = useT("settings");
  const [name, setName] = useState(agent.name);
  const [description, setDescription] = useState(agent.description ?? "");

  useEffect(() => {
    setName(agent.name);
    setDescription(agent.description ?? "");
    // Only reset when moving to another agent; cache refreshes must not erase
    // a newer local draft while autosave is in flight.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [agent.id]);

  const profileDraft = useMemo(
    () => ({ name: name.trim(), description }),
    [description, name],
  );
  const savedProfile = useMemo(
    () => ({ name: agent.name, description: agent.description ?? "" }),
    [agent.description, agent.name],
  );
  const saveProfile = useCallback(
    async (next: ProfileDraft) => {
      await onUpdate({ name: next.name, description: next.description });
    },
    [onUpdate],
  );
  const profileAutoSave = useAutoSave({
    value: profileDraft,
    savedValue: savedProfile,
    onSave: saveProfile,
    enabled:
      canEdit &&
      profileDraft.name.length > 0 &&
      profileDraft.description.length <= AGENT_DESCRIPTION_MAX_LENGTH,
    isEqual: profileDraftsEqual,
  });
  const nameInvalid = name.trim().length === 0;

  return (
    <SettingsSection
      title={t(($) => $.inspector.section_profile)}
      description={t(($) => $.inspector.section_profile_hint)}
      action={
        <SettingsSaveState
          status={profileAutoSave.status}
          savingLabel={ts(($) => $.auto_save.saving)}
          savedLabel={ts(($) => $.auto_save.saved)}
          errorLabel={ts(($) => $.auto_save.failed)}
        />
      }
    >
      <SettingsCard>
        <SettingsRow
          label={t(($) => $.inspector.avatar_label)}
          description={t(($) => $.inspector.avatar_hint)}
          size="none"
        >
          <div className="flex justify-start sm:justify-end">
            <AvatarUploadControl
              variant="agent"
              value={agent.avatar_url ?? null}
              name={agent.name}
              size={56}
              disabled={!canEdit}
              onUploaded={(url) => onUpdate({ avatar_url: url })}
              onEmojiSelected={(value) => onUpdate({ avatar_url: value })}
            />
          </div>
        </SettingsRow>

        <SettingsRow label={t(($) => $.inspector.name_label)} size="text">
          <div>
            <Input
              type="text"
              name="agent-name"
              autoComplete="off"
              aria-label={t(($) => $.inspector.name_label)}
              value={name}
              onChange={(event) => setName(event.target.value)}
              onBlur={profileAutoSave.flush}
              disabled={!canEdit}
              aria-invalid={nameInvalid || undefined}
            />
            {nameInvalid ? (
              <p className="mt-1 text-caption text-destructive">
                {t(($) => $.inspector.rename_required)}
              </p>
            ) : null}
          </div>
        </SettingsRow>

        <SettingsRow
          label={t(($) => $.inspector.description_label)}
          size="text"
          align="start"
        >
          <div>
            <Textarea
              name="agent-description"
              autoComplete="off"
              aria-label={t(($) => $.inspector.description_label)}
              value={description}
              onChange={(event) => setDescription(event.target.value)}
              onBlur={profileAutoSave.flush}
              disabled={!canEdit}
              rows={5}
              maxLength={AGENT_DESCRIPTION_MAX_LENGTH}
              className="resize-y"
              placeholder={t(($) => $.inspector.description_placeholder)}
            />
            <CharCounter
              length={[...description].length}
              max={AGENT_DESCRIPTION_MAX_LENGTH}
            />
          </div>
        </SettingsRow>
      </SettingsCard>
    </SettingsSection>
  );
}
