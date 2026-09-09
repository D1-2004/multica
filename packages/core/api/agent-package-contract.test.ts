import { describe, expect, it } from "vitest";
import { AgentPackagePreviewSchema } from "./schemas";

const contract = {
  version: 1 as const,
  scope: "Product support",
  must_delegate: ["Product evidence checks"],
  constraints: ["Draft only"],
  clarify_when: [],
  source_instructions_sha256: "a".repeat(64),
};
const preview = {
  definition: { name: "Example", coordinator_contract: contract },
  preview_id: "preview-1",
  expires_at: "2026-09-09T12:00:00Z",
  package_hash: "package-hash",
  manifest_version: "multica.agent/v2",
  name: "Example",
  instructions: "\nDraft only.\n",
  requirements: { secrets: [], deferred_bindings: [], runtime_provider: "" },
};

describe("package preview coordinator contract", () => {
  it("preserves the source version and existing package preview metadata", () => {
    const result = AgentPackagePreviewSchema.parse({
      ...preview,
      coordinator_contract: contract,
    });
    expect(result.coordinator_contract).toEqual(contract);
    expect(result.instructions).toBe(preview.instructions);
    expect(result.definition).toEqual(preview.definition);
    expect(result.package_hash).toBe(preview.package_hash);
  });

  it("accepts older previews without a contract and explicit null", () => {
    expect(AgentPackagePreviewSchema.parse(preview).coordinator_contract).toBeUndefined();
    expect(AgentPackagePreviewSchema.parse({ ...preview, coordinator_contract: null }).coordinator_contract).toBeNull();
  });

  it("rejects malformed constraints rather than hiding them before confirmation", () => {
    for (const malformed of [
      { ...contract, version: 2 },
      { ...contract, reply: "an extra action" },
      { ...contract, scope: 4 },
      { ...contract, scope: "x".repeat(1600) },
      { ...contract, source_instructions_sha256: "invalid" },
    ]) {
      expect(AgentPackagePreviewSchema.safeParse({ ...preview, coordinator_contract: malformed }).success).toBe(false);
    }
  });
});
