import { z } from "zod";
import type {
  TagApplyResponse,
  TagApplyTenantResult,
  TagState,
  TagSummary,
  TagTenant,
  TagTenantMutationResult,
} from "../tag/types";

/** Drops malformed items instead of failing the whole list. */
function tolerantList<T extends z.ZodType>(item: T) {
  return z
    .array(z.unknown())
    .nullish()
    .catch(null)
    .transform((values): z.output<T>[] =>
      (values ?? []).flatMap((value) => {
        const parsed = item.safeParse(value);
        return parsed.success ? [parsed.data as z.output<T>] : [];
      }),
    );
}

const str = z.string().catch("");
const optStr = z.string().nullish().catch(null).transform((value) => value ?? "");
const bool = z.boolean().nullish().catch(null).transform((value) => value === true);
const revision = z
  .number()
  .int()
  .positive()
  .nullish()
  .catch(null)
  .transform((value) => value ?? null);
const idList = z
  .array(z.unknown())
  .nullish()
  .catch(null)
  .transform((values) => (values ?? []).filter((value): value is string => typeof value === "string"));

export const TagTenantWireSchema = z
  .object({
    id: z.string().min(1),
    name: str,
    employee_agent_id: z.string().min(1),
    employee_name: optStr,
    employee_archived: bool,
    bound: bool,
    org_id: optStr,
    organization_name: optStr,
    digital_employee_name: optStr,
    applied_revision: revision,
    applied_at: z.string().nullish().catch(null).transform((value) => value ?? null),
    created_at: optStr,
  })
  .transform(
    (value): TagTenant => ({
      id: value.id,
      name: value.name,
      employeeAgentId: value.employee_agent_id,
      employeeName: value.employee_name,
      employeeArchived: value.employee_archived,
      // A tenant counts as bound only with an org; never trust the flag alone.
      bound: value.bound && value.org_id !== "",
      orgId: value.org_id,
      organizationName: value.organization_name,
      digitalEmployeeName: value.digital_employee_name,
      appliedRevision: value.applied_revision,
      appliedAt: value.applied_at,
      createdAt: value.created_at,
    }),
  );

const TagSummaryWireSchema = z
  .object({
    agent_id: z.string().min(1),
    name: str,
    description: optStr,
    avatar_url: z.string().nullish().catch(null).transform((value) => value ?? null),
    runtime_mode: optStr,
    sidebar_visible: z.boolean().nullish().catch(null).transform((value) => value !== false),
    latest_revision: revision,
    has_unpublished_changes: bool,
    created_at: optStr,
  })
  .transform(
    (value): TagSummary => ({
      agentId: value.agent_id,
      name: value.name,
      description: value.description,
      avatarUrl: value.avatar_url,
      runtimeMode: value.runtime_mode,
      sidebarVisible: value.sidebar_visible,
      latestRevision: value.latest_revision,
      hasUnpublishedChanges: value.has_unpublished_changes,
      createdAt: value.created_at,
    }),
  );

export const EMPTY_TAG_STATE: TagState = { tag: null, canOperate: false, canManage: false, tenants: [] };

export const TagStateSchema = z
  .object({
    tag: TagSummaryWireSchema.nullish().catch(null),
    can_operate: bool,
    can_manage: bool,
    tenants: tolerantList(TagTenantWireSchema),
  })
  .transform(
    (value): TagState => ({
      tag: value.tag ?? null,
      canOperate: value.can_operate,
      // Managing tenants only makes sense once a Tag exists.
      canManage: value.can_manage && value.tag != null,
      tenants: value.tag ? value.tenants : [],
    }),
  );

const TagApplyTenantResultWireSchema = z
  .object({
    tenant_id: z.string().min(1),
    applied: bool,
    reason: optStr,
    skipped_skill_ids: idList,
    skipped_connector_ids: idList,
    skipped_plugin_ids: idList,
  })
  .transform(
    (value): TagApplyTenantResult => ({
      tenantId: value.tenant_id,
      applied: value.applied,
      reason: value.reason,
      skippedSkillIds: value.skipped_skill_ids,
      skippedConnectorIds: value.skipped_connector_ids,
      skippedPluginIds: value.skipped_plugin_ids,
    }),
  );

export const EMPTY_TAG_APPLY: TagApplyResponse = { revision: 0, published: false, results: [] };

export const TagApplyResponseSchema = z
  .object({
    revision: z.number().int().nonnegative().catch(0),
    published: bool,
    results: tolerantList(TagApplyTenantResultWireSchema),
  })
  .transform((value): TagApplyResponse => value);

export const EMPTY_TAG_TENANT_MUTATION: TagTenantMutationResult = { tenant: null, apply: null };

export const TagTenantMutationSchema = z
  .object({
    tenant: TagTenantWireSchema.nullish().catch(null),
    apply: TagApplyTenantResultWireSchema.nullish().catch(null),
  })
  .transform(
    (value): TagTenantMutationResult => ({ tenant: value.tenant ?? null, apply: value.apply ?? null }),
  );
