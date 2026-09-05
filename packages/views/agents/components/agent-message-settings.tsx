"use client";

import { useEffect, useState } from "react";
import type { Agent } from "@multica/core/types";
import { Switch } from "@multica/ui/components/ui/switch";
import {
  SettingsCard,
  SettingsRow,
  SettingsSection,
} from "../../settings/components/settings-layout";
import { useT } from "../../i18n";

export function AgentMessageSettings({
  agent,
  canEdit,
  onUpdate,
}: {
  agent: Agent;
  canEdit: boolean;
  onUpdate: (data: Record<string, unknown>) => Promise<void>;
}) {
  const { t } = useT("agents");

  return (
    <SettingsSection
      title={t(($) => $.tab_body.digital_employee.message_title)}
      description={t(($) => $.tab_body.digital_employee.message_hint)}
    >
      <SettingsCard>
        <BooleanSetting
          agentId={agent.id}
          label={t(($) => $.inspector.prop_chat_session_resume)}
          description={t(($) => $.inspector.prop_chat_session_resume_hint)}
          enabled={agent.chat_session_resume === true}
          canEdit={canEdit}
          onSave={(next) => onUpdate({ chat_session_resume: next })}
        />
        <BooleanSetting
          agentId={agent.id}
          label={t(($) => $.inspector.prop_inbound_coordinator)}
          description={t(($) => $.inspector.prop_inbound_coordinator_hint)}
          enabled={agent.inbound_coordinator === true}
          canEdit={canEdit}
          onSave={(next) => onUpdate({ inbound_coordinator: next })}
        />
        <BooleanSetting
          agentId={agent.id}
          label={t(($) => $.inspector.prop_task_finished_loop)}
          description={t(($) => $.inspector.prop_task_finished_loop_hint)}
          enabled={agent.task_finished_loop_enabled === true}
          canEdit={canEdit && agent.inbound_coordinator === true}
          onSave={(next) => onUpdate({ task_finished_loop_enabled: next })}
        />
      </SettingsCard>
    </SettingsSection>
  );
}

function BooleanSetting({
  agentId,
  label,
  description,
  enabled,
  canEdit,
  onSave,
}: {
  agentId: string;
  label: string;
  description: string;
  enabled: boolean;
  canEdit: boolean;
  onSave: (next: boolean) => Promise<void>;
}) {
  const [draft, setDraft] = useState(enabled);
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    setDraft(enabled);
  }, [agentId, enabled]);

  return (
    <SettingsRow label={label} description={description} align="start">
      <Switch
        checked={draft}
        disabled={!canEdit || saving}
        onCheckedChange={(checked) => {
          setDraft(checked);
          setSaving(true);
          void onSave(checked)
            .catch(() => setDraft(!checked))
            .finally(() => setSaving(false));
        }}
        aria-label={label}
      />
    </SettingsRow>
  );
}
