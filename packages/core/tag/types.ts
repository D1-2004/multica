/**
 * The workspace Tag: one multi-tenant digital employee per workspace.
 *
 * The Tag's shared configuration lives on a template agent. Each tenant (one
 * enterprise) is embodied by its own employee agent, which owns that
 * enterprise's DingTalk digital employee, scenes and enterprise-level
 * context. Shared configuration reaches a tenant only when a revision is
 * applied to it.
 */

export interface TagTenant {
  id: string;
  name: string;
  employeeAgentId: string;
  employeeName: string;
  employeeArchived: boolean;
  /** The employee has a DingTalk digital employee, so the tenant receives
   * messages. */
  bound: boolean;
  /** The bound digital employee's DingTalk OrgId; empty until bound. */
  orgId: string;
  organizationName: string;
  digitalEmployeeName: string;
  /** Revision last applied to the employee; null until the first apply. */
  appliedRevision: number | null;
  appliedAt: string | null;
  createdAt: string;
}

export interface TagSummary {
  /** The template agent. */
  agentId: string;
  name: string;
  description: string;
  avatarUrl: string | null;
  runtimeMode: string;
  sidebarVisible: boolean;
  latestRevision: number | null;
  /** The template differs from its latest published revision. */
  hasUnpublishedChanges: boolean;
  createdAt: string;
}

export interface TagState {
  tag: TagSummary | null;
  /** Platform operator: may create, hide and remove the Tag. */
  canOperate: boolean;
  /** May manage tenants and apply configuration. */
  canManage: boolean;
  tenants: TagTenant[];
}

export interface TagApplyTenantResult {
  tenantId: string;
  applied: boolean;
  reason: string;
  skippedSkillIds: string[];
  skippedConnectorIds: string[];
  skippedPluginIds: string[];
  skippedOfferIds: string[];
}

export interface TagApplyResponse {
  revision: number;
  published: boolean;
  results: TagApplyTenantResult[];
}

export interface CreateTagInput {
  name: string;
  description?: string;
  runtimeId: string;
  model?: string;
  /** Seed the template's configuration from an existing agent. */
  copyFromAgentId?: string;
}

export interface TagTenantMutationResult {
  tenant: TagTenant | null;
  apply: TagApplyTenantResult | null;
}
