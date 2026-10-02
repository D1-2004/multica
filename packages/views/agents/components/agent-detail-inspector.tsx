"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import type { Agent, AgentRuntime, MemberWithUser } from "@multica/core/types";
import {
  AGENT_MAX_CONCURRENT_TASKS_MAX,
  AGENT_MAX_CONCURRENT_TASKS_MIN,
} from "@multica/core/agents";
import { isASBRuntime, isFCE2BRuntime, runtimeModelsOptions } from "@multica/core/runtimes";
import { isImeComposing } from "@multica/core/utils";
import { Input } from "@multica/ui/components/ui/input";
import { Switch } from "@multica/ui/components/ui/switch";
import {
  SettingsCard,
  SettingsRow,
  SettingsSection,
} from "../../settings/components/settings-layout";
import { useT } from "../../i18n";
import { ModelPicker } from "./inspector/model-picker";
import {
  buildModelChangeUpdate,
  type ModelCatalog,
} from "./inspector/model-change-cleanup";
import { RuntimePicker } from "./inspector/runtime-picker";
import { ASBRegionPicker } from "./inspector/asb-region-picker";
import { ThinkingSettingField } from "./inspector/thinking-prop-row";
import { ServiceTierSettingField } from "./inspector/service-tier-setting-field";
import { GitHubIdentityBindingCard } from "./integrations/github-identity-binding";

interface InspectorProps {
  agent: Agent;
  runtime: AgentRuntime | null;
  runtimes: AgentRuntime[];
  members: MemberWithUser[];
  currentUserId: string | null;
  canEdit: boolean;
  sourceManaged?: boolean;
  onUpdate: (id: string, data: Record<string, unknown>) => Promise<void>;
}

/**
 * Runtime settings form. Employee profile and communication behavior live in
 * Digital Employee; this component owns only execution choices and sandbox
 * identity.
 */
export function AgentDetailInspector({
  agent,
  runtime,
  runtimes,
  members,
  currentUserId,
  canEdit,
  onUpdate,
}: InspectorProps) {
  const { t } = useT("agents");
  const update = useCallback(
    (data: Record<string, unknown>) => onUpdate(agent.id, data),
    [agent.id, onUpdate],
  );

  const isOnline = runtime?.status === "online";

  // Same query the Thinking / Speed fields already use, so switching model
  // costs no extra request. `null` = not authoritative (offline runtime, still
  // loading, or discovery failed) and must not trigger any clearing.
  const modelsQuery = useQuery(
    runtimeModelsOptions(isOnline ? agent.runtime_id : null),
  );
  const modelCatalog = useMemo<ModelCatalog>(
    () =>
      modelsQuery.isSuccess
        ? modelsQuery.data.supported
          ? modelsQuery.data.models
          : []
        : null,
    [modelsQuery.data, modelsQuery.isSuccess],
  );
  const handleModelChange = useCallback(
    (model: string) =>
      update(
        buildModelChangeUpdate({
          provider: runtime?.provider ?? "",
          model,
          thinkingLevel: agent.thinking_level ?? "",
          serviceTier: agent.service_tier ?? "",
          catalog: modelCatalog,
        }),
      ),
    [
      agent.service_tier,
      agent.thinking_level,
      modelCatalog,
      runtime?.provider,
      update,
    ],
  );

  return (
    <div className="space-y-8">
      <SettingsSection
        title={t(($) => $.inspector.section_execution)}
        description={t(($) => $.inspector.section_execution_hint)}
      >
        <SettingsCard>
          <SettingsRow
            label={t(($) => $.inspector.prop_runtime)}
            size="select-wide"
          >
            <RuntimePicker
              variant="field"
              showLabel={false}
              value={agent.runtime_id}
              runtimes={runtimes}
              members={members}
              currentUserId={currentUserId}
              canEdit={canEdit}
              // Model, thinking level, and service tier are runtime/model
              // native. Clear them together so the new runtime resolves its
              // own defaults instead of inheriting incompatible tokens.
              onChange={(id) =>
                update({
                  runtime_id: id,
                  model: "",
                  thinking_level: "",
                  service_tier: "",
                })
              }
            />
          </SettingsRow>
          {isASBRuntime(runtime) ? (
            <SettingsRow
              label={t(($) => $.asb_regions.title)}
              size="select-wide"
            >
              <ASBRegionPicker
                key={`${agent.id}:${agent.runtime_id}`}
                agent={agent}
                canEdit={canEdit}
                onSave={update}
              />
            </SettingsRow>
          ) : null}
          <SettingsRow
            label={t(($) => $.inspector.prop_model)}
            size="select-wide"
          >
            <ModelPicker
              variant="field"
              showLabel={false}
              runtimeId={agent.runtime_id}
              runtime={runtime}
              runtimeOnline={!!isOnline}
              value={agent.model ?? ""}
              thinkingValue={agent.thinking_level ?? ""}
              canEdit={canEdit}
              onChange={handleModelChange}
              onThinkingChange={(thinkingLevel) =>
                update({ thinking_level: thinkingLevel })
              }
            />
          </SettingsRow>
          <ThinkingSettingField
            label={t(($) => $.inspector.prop_thinking)}
            runtimeId={agent.runtime_id}
            runtimeOnline={!!isOnline}
            provider={runtime?.provider ?? ""}
            model={agent.model ?? ""}
            value={agent.thinking_level ?? ""}
            canEdit={canEdit}
            onChange={(thinkingLevel) =>
              update({ thinking_level: thinkingLevel })
            }
          />
          <ServiceTierSettingField
            label={t(($) => $.inspector.prop_speed)}
            runtimeId={agent.runtime_id}
            runtimeOnline={!!isOnline}
            provider={runtime?.provider ?? ""}
            model={agent.model ?? ""}
            value={agent.service_tier ?? ""}
            canEdit={canEdit}
            onChange={(serviceTier) => update({ service_tier: serviceTier })}
          />
          <SettingsRow
            label={t(($) => $.inspector.prop_concurrency)}
            size="select-wide"
          >
            <ConcurrencyField
              value={agent.max_concurrent_tasks}
              canEdit={canEdit}
              onSave={(next) => update({ max_concurrent_tasks: next })}
            />
          </SettingsRow>
          {isFCE2BRuntime(runtime) ? (
            <SettingsRow
              label={t(($) => $.inspector.prop_sandbox_reuse)}
              description={t(($) => $.inspector.prop_sandbox_reuse_hint)}
              align="start"
            >
              <Switch
                checked={agent.sandbox_connection_reuse !== false}
                disabled={!canEdit}
                aria-label={t(($) => $.inspector.prop_sandbox_reuse)}
                onCheckedChange={(checked) => {
                  void update({ sandbox_connection_reuse: checked });
                }}
              />
            </SettingsRow>
          ) : null}
        </SettingsCard>
      </SettingsSection>
      <GitHubIdentityBindingCard agentId={agent.id} canManage={canEdit} />
    </div>
  );
}

function ConcurrencyField({
  value,
  canEdit,
  onSave,
}: {
  value: number;
  canEdit: boolean;
  onSave: (next: number) => Promise<void>;
}) {
  const { t } = useT("agents");
  const [draft, setDraft] = useState(String(value));

  useEffect(() => setDraft(String(value)), [value]);

  const commit = () => {
    const next = Number(draft);
    if (
      !Number.isInteger(next) ||
      next < AGENT_MAX_CONCURRENT_TASKS_MIN ||
      next > AGENT_MAX_CONCURRENT_TASKS_MAX
    ) {
      setDraft(String(value));
      return;
    }
    if (next !== value) void onSave(next);
  };

  return (
    <div>
      <Input
        id="agent-concurrency"
        type="number"
        name="agent-concurrency"
        autoComplete="off"
        inputMode="numeric"
        min={AGENT_MAX_CONCURRENT_TASKS_MIN}
        max={AGENT_MAX_CONCURRENT_TASKS_MAX}
        value={draft}
        onChange={(event) => setDraft(event.target.value)}
        onBlur={commit}
        onKeyDown={(event) => {
          if (isImeComposing(event)) return;
          if (event.key === "Enter") {
            event.preventDefault();
            commit();
          }
        }}
        disabled={!canEdit}
        aria-label={t(($) => $.inspector.prop_concurrency)}
        className="font-mono tabular-nums"
      />
      <p className="mt-1 text-caption text-muted-foreground">
        {t(($) => $.pickers.concurrency_range, {
          min: AGENT_MAX_CONCURRENT_TASKS_MIN,
          max: AGENT_MAX_CONCURRENT_TASKS_MAX,
        })}
      </p>
    </div>
  );
}
