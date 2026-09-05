"use client";

import { useCallback } from "react";
import { isASBRuntime } from "@multica/core/runtimes";
import type { Agent, AgentRuntime, MemberWithUser } from "@multica/core/types";
import { SettingsSection } from "../../../settings/components/settings-layout";
import { useT } from "../../../i18n";
import { AgentMessageSettings } from "../agent-message-settings";
import { AgentProfileSettings } from "../agent-profile-settings";
import { AgentVoiceSettings } from "../agent-voice-settings";
import { DingTalkAccountBindingCard } from "../integrations/dingtalk-account-binding";
import { EnterpriseIdentityBindingCard } from "../integrations/enterprise-identity-binding";

export function DigitalEmployeeTab({
  agent,
  runtime,
  members,
  currentUserId,
  canEdit,
  canOperateDingTalkBinding,
  dingTalkBindingPermissionLoading,
  onUpdate,
  onDirtyChange,
}: {
  agent: Agent;
  runtime: AgentRuntime | null;
  members: MemberWithUser[];
  currentUserId: string | null;
  canEdit: boolean;
  canOperateDingTalkBinding: boolean;
  dingTalkBindingPermissionLoading: boolean;
  onUpdate: (id: string, data: Record<string, unknown>) => Promise<void>;
  onDirtyChange?: (dirty: boolean) => void;
}) {
  const { t } = useT("agents");
  const update = useCallback(
    (data: Record<string, unknown>) => onUpdate(agent.id, data),
    [agent.id, onUpdate],
  );
  const currentMember = members.find(
    (member) => member.user_id === currentUserId,
  );
  const canManageIdentity =
    canEdit ||
    currentMember?.role === "owner" ||
    currentMember?.role === "admin" ||
    (!!currentUserId && agent.owner_id === currentUserId);

  return (
    <div className="space-y-8">
      <AgentProfileSettings
        agent={agent}
        canEdit={canEdit}
        onUpdate={update}
      />

      <SettingsSection
        title={t(($) => $.tab_body.digital_employee.identity_title)}
        description={t(($) => $.tab_body.digital_employee.identity_hint)}
      >
        <div className="space-y-4">
          <DingTalkAccountBindingCard
            agentId={agent.id}
            agentName={agent.name}
            bindingMode="message"
            canOperate={canOperateDingTalkBinding}
            permissionLoading={dingTalkBindingPermissionLoading}
          />
          <DingTalkAccountBindingCard
            agentId={agent.id}
            agentName={agent.name}
            bindingMode="identity"
            canOperate={canOperateDingTalkBinding}
            permissionLoading={dingTalkBindingPermissionLoading}
          />
          {isASBRuntime(runtime) ? (
            <EnterpriseIdentityBindingCard
              agentId={agent.id}
              canManage={canManageIdentity}
            />
          ) : null}
        </div>
      </SettingsSection>

      <AgentVoiceSettings
        agent={agent}
        canEdit={canEdit}
        onSave={update}
        onDirtyChange={onDirtyChange}
      />

      <AgentMessageSettings
        agent={agent}
        canEdit={canEdit}
        onUpdate={update}
      />
    </div>
  );
}
