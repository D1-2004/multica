"use client";

import { useEffect, useId, useState } from "react";
import type { Agent } from "@multica/core/types";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { Button } from "@multica/ui/components/ui/button";
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
          label={t(($) => $.inspector.prop_dingtalk_response_enabled)}
          description={t(($) => $.inspector.prop_dingtalk_response_enabled_hint)}
          enabled={agent.dingtalk_response_enabled === true}
          canEdit={canEdit}
          onSave={(next) => onUpdate({ dingtalk_response_enabled: next })}
        />
        <BooleanSetting
          agentId={agent.id}
          label={t(($) => $.inspector.prop_dingtalk_show_ai_tag)}
          description={t(($) => $.inspector.prop_dingtalk_show_ai_tag_hint)}
          enabled={agent.dingtalk_show_ai_tag === true}
          canEdit={canEdit}
          onSave={(next) => onUpdate({ dingtalk_show_ai_tag: next })}
        />
        <BooleanSetting
          agentId={agent.id}
          label={t(($) => $.inspector.prop_chat_session_resume)}
          description={t(($) => $.inspector.prop_chat_session_resume_hint)}
          enabled={agent.chat_session_resume === true}
          canEdit={canEdit}
          onSave={(next) => onUpdate({ chat_session_resume: next })}
        />
        {agent.inbound_coordinator === true ? (
          <BooleanSetting
            agentId={agent.id}
            label={t(($) => $.inspector.prop_task_finished_loop)}
            description={t(($) => $.inspector.prop_task_finished_loop_hint)}
            enabled={agent.task_finished_loop_enabled === true}
            canEdit={canEdit}
            onSave={(next) => onUpdate({ task_finished_loop_enabled: next })}
          />
        ) : null}
      </SettingsCard>
    </SettingsSection>
  );
}

export function InboundCoordinatorSetting({
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
      title={t(($) => $.tab_body.digital_employee.inbound_title)}
      description={t(($) => $.tab_body.digital_employee.inbound_hint)}
    >
      <SettingsCard>
        <BooleanSetting
          agentId={agent.id}
          label={t(($) => $.inspector.prop_inbound_coordinator)}
          description={t(($) => $.inspector.prop_inbound_coordinator_hint)}
          enabled={agent.inbound_coordinator === true}
          canEdit={canEdit}
          onSave={(next) => onUpdate(next ? { inbound_coordinator: true } : { inbound_coordinator: false, inbound_coordinator_user_decision: false, event_trigger_enabled: false })}
        />
        <BooleanSetting
          agentId={agent.id}
          label={t(($) => $.inspector.prop_coordinator_user_decision)}
          description={t(($) => $.inspector.prop_coordinator_user_decision_hint)}
          enabled={agent.inbound_coordinator_user_decision === true}
          canEdit={canEdit && agent.inbound_coordinator === true}
          onSave={(next) => onUpdate({ inbound_coordinator_user_decision: next })}
        />
        <UserDecisionNamesSetting agent={agent} canEdit={canEdit} onUpdate={onUpdate} />
        <BooleanSetting
          agentId={agent.id}
          label={t(($) => $.inspector.prop_event_trigger)}
          description={t(($) => $.inspector.prop_event_trigger_hint)}
          enabled={agent.event_trigger_enabled === true}
          canEdit={canEdit}
          onSave={(next) => onUpdate(next ? { event_trigger_enabled: true, inbound_coordinator: true } : { event_trigger_enabled: false })}
        />
      </SettingsCard>
    </SettingsSection>
  );
}

export function BooleanSetting({
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

function UserDecisionNamesSetting({ agent, canEdit, onUpdate }: {
  agent: Agent;
  canEdit: boolean;
  onUpdate: (data: Record<string, unknown>) => Promise<void>;
}) {
  const { t } = useT("agents");
  const effectsId = useId();
  const saved = (agent.inbound_coordinator_user_decision_names ?? []).join("\n");
  const [draft, setDraft] = useState(saved);
  const [saving, setSaving] = useState(false);
  useEffect(() => { setDraft(saved); }, [agent.id, saved]);
  const enabled = canEdit && agent.inbound_coordinator === true;
  return (
    <SettingsRow
      label={t(($) => $.inspector.prop_coordinator_user_decision_names)}
      description={t(($) => $.inspector.prop_coordinator_user_decision_names_hint)}
      size="text"
      align="start"
    >
      <div className="space-y-2">
        <p id={effectsId} className="whitespace-pre-line text-caption leading-5 text-foreground">
          {t(($) => $.inspector.prop_coordinator_user_decision_names_effect)}
        </p>
        <Textarea
          aria-label={t(($) => $.inspector.prop_coordinator_user_decision_names)}
          aria-describedby={effectsId}
          placeholder={t(($) => $.inspector.prop_coordinator_user_decision_names_placeholder)}
          value={draft}
          onChange={(event) => setDraft(event.target.value)}
          disabled={!enabled || saving}
          rows={3}
          className="resize-y"
        />
        <Button
          size="sm"
          variant="outline"
          disabled={!enabled || saving || draft === saved}
          onClick={() => {
            const names = [...new Set(draft.split(/\r?\n/).map((name) => name.trim()).filter(Boolean))];
            setSaving(true);
            void onUpdate({ inbound_coordinator_user_decision_names: names })
              .then(() => setDraft(names.join("\n")))
              .catch(() => undefined)
              .finally(() => setSaving(false));
          }}
        >
          {t(($) => $.inspector.prop_coordinator_user_decision_names_save)}
        </Button>
      </div>
    </SettingsRow>
  );
}
