"use client";

import { useEffect, useId, useState } from "react";
import type { Agent } from "@multica/core/types";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { Button } from "@multica/ui/components/ui/button";
import { RadioGroup, RadioGroupItem } from "@multica/ui/components/ui/radio-group";
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
        {agent.inbound_coordinator === true && (agent.coordination_mode ?? "coordinator") === "coordinator" ? (
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
  const mode = agent.coordination_mode ?? "coordinator";
  const knownMode = mode !== "unknown";
  const ready = mode !== "employee" || agent.employee_loop_ready === true;

  return (
    <SettingsSection
      title={t(($) => $.tab_body.digital_employee.inbound_title)}
      description={t(($) => $.tab_body.digital_employee.inbound_hint)}
    >
      <SettingsCard>
        <CoordinationModeSetting agent={agent} canEdit={canEdit} onUpdate={onUpdate} />
        <BooleanSetting
          agentId={agent.id}
          label={t(($) => $.inspector.prop_inbound_coordinator)}
          description={t(($) => $.inspector.prop_inbound_coordinator_hint)}
          enabled={agent.inbound_coordinator === true}
          canEdit={canEdit && knownMode && (ready || agent.inbound_coordinator === true)}
          onSave={(next) => onUpdate(next ? { inbound_coordinator: true } : { inbound_coordinator: false, inbound_coordinator_user_decision_mode: "off", event_trigger_enabled: false })}
        />
        {mode === "coordinator" ? <UserDecisionSetting agent={agent} canEdit={canEdit} onUpdate={onUpdate} /> : null}
        <BooleanSetting
          agentId={agent.id}
          label={t(($) => $.inspector.prop_event_trigger)}
          description={t(($) => $.inspector.prop_event_trigger_hint)}
          enabled={agent.event_trigger_enabled === true}
          canEdit={canEdit && knownMode && (ready || agent.event_trigger_enabled === true)}
          onSave={(next) => onUpdate(next ? { event_trigger_enabled: true, inbound_coordinator: true } : { event_trigger_enabled: false })}
        />
      </SettingsCard>
    </SettingsSection>
  );
}

function CoordinationModeSetting({ agent, canEdit, onUpdate }: {
  agent: Agent;
  canEdit: boolean;
  onUpdate: (data: Record<string, unknown>) => Promise<void>;
}) {
  const { t } = useT("agents");
  const [saving, setSaving] = useState(false);
  const mode = agent.coordination_mode ?? "coordinator";
  const hintId = useId();
  return (
    <SettingsRow label={t(($) => $.inspector.prop_coordination_mode)} description={t(($) => $.inspector.prop_coordination_mode_hint)} size="text" align="start">
      <div className="space-y-3">
        <RadioGroup
          aria-label={t(($) => $.inspector.prop_coordination_mode)}
          aria-describedby={hintId}
          value={mode}
          disabled={!canEdit || saving || mode === "unknown"}
          onValueChange={(next) => {
            if (!canEdit || saving || mode === "unknown" || next === mode || (next !== "coordinator" && next !== "employee") || (next === "employee" && agent.employee_loop_ready !== true)) return;
            setSaving(true);
            void onUpdate({ coordination_mode: next }).catch(() => undefined).finally(() => setSaving(false));
          }}
          className="gap-3"
        >
          <label className="flex items-center gap-3">
            <RadioGroupItem value="coordinator" aria-labelledby={`${hintId}-coordinator`} />
            <span id={`${hintId}-coordinator`} className="text-body">Coordinator</span>
          </label>
          <label className="flex items-center gap-3">
            <RadioGroupItem value="employee" aria-labelledby={`${hintId}-employee`} disabled={agent.employee_loop_ready !== true} />
            <span id={`${hintId}-employee`} className="text-body">EmployeeLoop</span>
          </label>
        </RadioGroup>
        <p id={hintId} className="text-caption leading-5 text-muted-foreground">
          {mode === "unknown" ? t(($) => $.inspector.prop_coordination_mode_unknown) : agent.employee_loop_ready !== true ? t(($) => $.inspector.prop_employee_loop_unavailable) : t(($) => $.inspector.prop_coordination_mode_confirmed)}
        </p>
      </div>
    </SettingsRow>
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

function UserDecisionSetting({ agent, canEdit, onUpdate }: {
  agent: Agent;
  canEdit: boolean;
  onUpdate: (data: Record<string, unknown>) => Promise<void>;
}) {
  const { t } = useT("agents");
  const namesHintId = useId();
  const savedNames = (agent.inbound_coordinator_user_decision_names ?? []).join("\n");
  const [draftNames, setDraftNames] = useState(savedNames);
  const [saving, setSaving] = useState(false);
  useEffect(() => { setDraftNames(savedNames); }, [agent.id, savedNames]);
  const enabled = canEdit && agent.inbound_coordinator === true;
  const mode = agent.inbound_coordinator_user_decision_mode
    ?? (agent.inbound_coordinator_user_decision === true ? "named" : "off");
  const options = [
    { value: "off", label: t(($) => $.inspector.prop_coordinator_user_decision_off), description: t(($) => $.inspector.prop_coordinator_user_decision_off_hint) },
    { value: "all", label: t(($) => $.inspector.prop_coordinator_user_decision_all), description: t(($) => $.inspector.prop_coordinator_user_decision_all_hint) },
    { value: "named", label: t(($) => $.inspector.prop_coordinator_user_decision_named), description: t(($) => $.inspector.prop_coordinator_user_decision_named_hint) },
  ] as const;
  return (
    <SettingsRow
      label={t(($) => $.inspector.prop_coordinator_user_decision)}
      description={t(($) => $.inspector.prop_coordinator_user_decision_hint)}
      size="text"
      align="start"
    >
      <div className="space-y-4">
        <RadioGroup
          aria-label={t(($) => $.inspector.prop_coordinator_user_decision)}
          value={mode}
          disabled={!enabled || saving}
          onValueChange={(next) => {
            if (!enabled || saving || next === mode || !options.some((option) => option.value === next)) return;
            setSaving(true);
            void onUpdate({ inbound_coordinator_user_decision_mode: next })
              .catch(() => undefined)
              .finally(() => setSaving(false));
          }}
          className="gap-3"
        >
          {options.map((option) => (
            <label key={option.value} className="flex items-start gap-3">
              <RadioGroupItem value={option.value} aria-labelledby={`${namesHintId}-${option.value}`} aria-describedby={`${namesHintId}-${option.value}-hint`} className="mt-0.5" />
              <span className="min-w-0">
                <span id={`${namesHintId}-${option.value}`} className="block text-body font-medium">{option.label}</span>
                <span id={`${namesHintId}-${option.value}-hint`} className="block text-caption leading-5 text-muted-foreground">{option.description}</span>
              </span>
            </label>
          ))}
        </RadioGroup>
        {mode === "named" ? (
          <div className="space-y-2">
            <Textarea
              aria-label={t(($) => $.inspector.prop_coordinator_user_decision_names)}
              aria-describedby={namesHintId}
              placeholder={t(($) => $.inspector.prop_coordinator_user_decision_names_placeholder)}
              value={draftNames}
              onChange={(event) => setDraftNames(event.target.value)}
              disabled={!enabled || saving}
              rows={3}
              className="resize-y"
            />
            <p id={namesHintId} className="text-caption leading-5 text-muted-foreground">
              {t(($) => $.inspector.prop_coordinator_user_decision_names_hint)}
            </p>
            <Button
              size="sm"
              variant="outline"
              disabled={!enabled || saving || draftNames === savedNames}
              onClick={() => {
                const names = [...new Set(draftNames.split(/\r?\n/).map((name) => name.trim()).filter(Boolean))];
                setSaving(true);
                void onUpdate({ inbound_coordinator_user_decision_mode: "named", inbound_coordinator_user_decision_names: names })
                  .catch(() => undefined)
                  .finally(() => setSaving(false));
              }}
            >
              {t(($) => $.inspector.prop_coordinator_user_decision_names_save)}
            </Button>
          </div>
        ) : null}
      </div>
    </SettingsRow>
  );
}
