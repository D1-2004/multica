import { describe, expect, it } from "vitest";
import { AgentResponseSchema, AgentTemplateSchema, CoordinatorContractSchema, GitAgentPreviewSchema, StoredAgentDraftSchema } from "./schemas";

const contract = {
  version: 1 as const,
  scope: "产品问答",
  must_delegate: ["查证"],
  constraints: ["只起草"],
  clarify_when: [],
  source_instructions_sha256: "a".repeat(64),
};

describe("coordinator contract API boundaries", () => {
  it("preserves the exact source version in agent responses and stored drafts", () => {
    expect(AgentResponseSchema.parse({id:"a",coordinator_contract:contract,coordinator_contract_state:"stale"})).toMatchObject({coordinator_contract:contract,coordinator_contract_state:"stale"});
    expect(StoredAgentDraftSchema.parse({coordinator_contract:contract}).coordinator_contract).toEqual(contract);
  });
  it("rejects malformed, unknown-version, expanded-action and oversized contracts locally", () => {
    for (const value of [ {...contract,version:2}, {...contract,reply:"escape"}, {...contract,scope:4}, {...contract,scope:"长".repeat(1600)}, {...contract,source_instructions_sha256:"forged"} ]) {
      expect(CoordinatorContractSchema.safeParse(value).success).toBe(false);
      expect(AgentResponseSchema.parse({id:"a",coordinator_contract:value}).coordinator_contract).toBeNull();
    }
    expect(AgentResponseSchema.parse({id:"older"}).coordinator_contract_state).toBe("not_configured");
    expect(AgentResponseSchema.parse({id:"a",coordinator_contract_state:"future"}).coordinator_contract_state).toBe("unavailable");
  });
  it("keeps the field in template and Git preview payloads", () => {
    expect(AgentTemplateSchema.parse({slug:"x",name:"x",description:"",skills:[],instructions:"job",coordinator_contract:contract}).coordinator_contract).toEqual(contract);
    expect(GitAgentPreviewSchema.parse({connection_id:"1",repository:"a/b",ref:"main",resolved_sha:"sha",name:"x",coordinator_contract:contract}).coordinator_contract).toEqual(contract);
  });
});
