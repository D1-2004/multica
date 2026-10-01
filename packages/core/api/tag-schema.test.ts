import { describe, expect, it } from "vitest";
import { parseWithFallback } from "./schema";
import {
  EMPTY_TAG_APPLY,
  EMPTY_TAG_STATE,
  TagApplyResponseSchema,
  TagStateSchema,
  TagTenantMutationSchema,
} from "./tag-schema";

const tenant = {
  id: "t-1",
  name: "Think测试组织",
  employee_agent_id: "a-2",
  employee_name: "QwenTag · Think测试组织",
  employee_archived: false,
  bound: true,
  org_id: "177928186",
  organization_name: "Think测试组织",
  digital_employee_name: "QwenTag",
  applied_revision: 3,
  applied_at: "2026-10-01T10:00:00Z",
  created_at: "2026-10-01T09:00:00Z",
};

describe("TagStateSchema", () => {
  it("maps a full state", () => {
    const state = TagStateSchema.parse({
      tag: {
        agent_id: "a-1",
        name: "QwenTag",
        description: "",
        avatar_url: null,
        runtime_mode: "cloud",
        sidebar_visible: true,
        latest_revision: 4,
        has_unpublished_changes: true,
        created_at: "2026-10-01T08:00:00Z",
      },
      can_operate: true,
      can_manage: true,
      tenants: [tenant],
    });
    expect(state.tag?.latestRevision).toBe(4);
    expect(state.tag?.hasUnpublishedChanges).toBe(true);
    expect(state.canManage).toBe(true);
    expect(state.tenants).toHaveLength(1);
    expect(state.tenants[0]).toMatchObject({ bound: true, orgId: "177928186", appliedRevision: 3 });
  });

  it("survives malformed responses", () => {
    expect(parseWithFallback("nope", TagStateSchema, EMPTY_TAG_STATE, { endpoint: "test" })).toEqual(EMPTY_TAG_STATE);
    const state = TagStateSchema.parse({
      tag: { agent_id: "a-1", name: 7, sidebar_visible: "yes", latest_revision: -1 },
      can_operate: "true",
      can_manage: true,
      tenants: [tenant, { id: "", employee_agent_id: "x" }, null, { ...tenant, id: "t-2", bound: true, org_id: "" }],
    });
    expect(state.tag).toMatchObject({ agentId: "a-1", name: "", sidebarVisible: true, latestRevision: null });
    // String booleans are not trusted.
    expect(state.canOperate).toBe(false);
    // Malformed rows are dropped; "bound" without an org is not bound.
    expect(state.tenants.map((t) => t.id)).toEqual(["t-1", "t-2"]);
    expect(state.tenants[1]?.bound).toBe(false);
  });

  it("ignores manage rights and tenants without a tag", () => {
    const state = TagStateSchema.parse({ tag: null, can_operate: true, can_manage: true, tenants: [tenant] });
    expect(state).toEqual({ tag: null, canOperate: true, canManage: false, tenants: [] });
  });
});

describe("Tag mutation schemas", () => {
  it("parses apply results and tolerates garbage", () => {
    const parsed = TagApplyResponseSchema.parse({
      revision: 5,
      published: true,
      results: [
        { tenant_id: "t-1", applied: true, skipped_skill_ids: ["s-1", 2], skipped_connector_ids: null },
        { applied: true },
      ],
    });
    expect(parsed.revision).toBe(5);
    expect(parsed.results).toEqual([
      { tenantId: "t-1", applied: true, reason: "", skippedSkillIds: ["s-1"], skippedConnectorIds: [], skippedPluginIds: [] },
    ]);
    expect(parseWithFallback(undefined, TagApplyResponseSchema, EMPTY_TAG_APPLY, { endpoint: "test" })).toEqual(EMPTY_TAG_APPLY);
  });

  it("parses tenant mutations", () => {
    expect(TagTenantMutationSchema.parse({ tenant, apply: null }).tenant?.id).toBe("t-1");
    expect(TagTenantMutationSchema.parse({ tenant: { bogus: true } })).toEqual({ tenant: null, apply: null });
  });
});
