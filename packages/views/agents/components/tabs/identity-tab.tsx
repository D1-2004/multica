"use client";

import { useQuery } from "@tanstack/react-query";
import { useAuthStore } from "@multica/core/auth";
import { useWorkspaceId } from "@multica/core/hooks";
import { isASBRuntime } from "@multica/core/runtimes";
import type { Agent, AgentRuntime } from "@multica/core/types";
import { memberListOptions } from "@multica/core/workspace/queries";
import { DingTalkAccountBindingCard } from "../integrations/dingtalk-account-binding";
import { EnterpriseIdentityBindingCard } from "../integrations/enterprise-identity-binding";
import { GitHubIdentityBindingCard } from "../integrations/github-identity-binding";

export function IdentityTab({
  agent,
  runtime,
  canOperateDingTalkBinding,
  dingTalkBindingPermissionLoading,
}: {
  agent: Agent;
  runtime?: AgentRuntime | null;
  canOperateDingTalkBinding: boolean;
  dingTalkBindingPermissionLoading: boolean;
}) {
  const wsId = useWorkspaceId();
  const user = useAuthStore((s) => s.user);
  const { data: members = [] } = useQuery({
    ...memberListOptions(wsId),
    enabled: !!wsId,
  });
  const currentMember = members.find((m) => m.user_id === user?.id) ?? null;
  const isWorkspaceAdmin =
    currentMember?.role === "owner" || currentMember?.role === "admin";
  const isAgentOwner =
    !!user?.id && agent.owner_id != null && agent.owner_id === user.id;
  const canManageIdentity = isWorkspaceAdmin || isAgentOwner;

  return (
    <div className="space-y-6">
      <GitHubIdentityBindingCard
        agentId={agent.id}
        canManage={canManageIdentity}
      />
      {isASBRuntime(runtime) && (
        <EnterpriseIdentityBindingCard
          agentId={agent.id}
          canManage={canManageIdentity}
        />
      )}
      <DingTalkAccountBindingCard
        agentId={agent.id}
        agentName={agent.name}
        bindingMode="identity"
        canOperate={canOperateDingTalkBinding}
        permissionLoading={dingTalkBindingPermissionLoading}
      />
    </div>
  );
}
