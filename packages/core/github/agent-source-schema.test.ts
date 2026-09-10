import { describe, expect, it } from "vitest";
import { parseWithFallback } from "../api/schema";
import {
  AgentSourceSchema,
  EMPTY_AGENT_SOURCE,
  EMPTY_GITHUB_AGENT_PREVIEW,
  GitHubAgentPreviewSchema,
  AgentSourceSyncPreviewSchema,
  EMPTY_AGENT_SOURCE_SYNC_PREVIEW,
  AgentSourceBranchesSchema,
  EMPTY_AGENT_SOURCE_BRANCHES,
} from "../api/schemas";

describe("GitHub agent source API schemas", () => {
  it("rejects a sync preview without a confirmation ID or an immutable commit", () => {
    for (const value of [
      { resolved_sha: "a".repeat(40), ref: "main" },
      { preview_id: "11111111-1111-4111-8111-111111111111", resolved_sha: "main" },
    ]) {
      expect(parseWithFallback(value, AgentSourceSyncPreviewSchema, EMPTY_AGENT_SOURCE_SYNC_PREVIEW, {
        endpoint: "POST /api/agents/:id/source/preview",
      })).toEqual(EMPTY_AGENT_SOURCE_SYNC_PREVIEW);
    }
  });

  it("rejects malformed branch responses", () => {
    expect(parseWithFallback({ branches: [{ name: 123 }] }, AgentSourceBranchesSchema, EMPTY_AGENT_SOURCE_BRANCHES, {
      endpoint: "GET /api/agents/:id/source/branches",
    })).toEqual(EMPTY_AGENT_SOURCE_BRANCHES);
  });

  it("preserves file deletion and absent binary text in a sync diff", () => {
    const parsed = AgentSourceSyncPreviewSchema.parse({
      preview_id: "11111111-1111-4111-8111-111111111111",
      expires_at: "2026-09-08T12:00:00Z",
      repository_url: "https://github.com/acme/agent",
      ref: "release/v2", base_sha: "a".repeat(40), resolved_sha: "b".repeat(40), changed: true,
      git_changes: [{ path: "removed.md", status: "deleted", before: "old", after: null }],
      configuration_changes: null, warnings: null,
    });
    expect(parsed.git_changes[0]?.after).toBeNull();
    expect(parsed.configuration_changes).toEqual([]);
  });
  it("falls back when preview loses its immutable SHA", () => {
    const parsed = parseWithFallback(
      {
        installation_id: "installation",
        repository: "acme/agent",
        ref: "main",
        name: "reviewer",
      },
      GitHubAgentPreviewSchema,
      EMPTY_GITHUB_AGENT_PREVIEW,
      { endpoint: "POST /api/workspaces/:id/github/agent-preview" },
    );
    expect(parsed).toEqual(EMPTY_GITHUB_AGENT_PREVIEW);
  });

  it("normalizes nullable collection fields from older preview responses", () => {
    const parsed = parseWithFallback(
      {
        installation_id: "installation",
        repository: "acme/agent",
        ref: "main",
        resolved_sha: "abc",
        name: "reviewer",
        description: "Reviews code",
        instructions: "Review safely",
        skills: null,
        compatible_providers: null,
        warnings: null,
        blockers: null,
      },
      GitHubAgentPreviewSchema,
      EMPTY_GITHUB_AGENT_PREVIEW,
      { endpoint: "POST /api/workspaces/:id/github/agent-preview" },
    );

    expect(parsed).toMatchObject({
      repository: "acme/agent",
      name: "reviewer",
      skills: [],
      compatible_providers: [],
      warnings: [],
      blockers: [],
    });
  });

  it("defaults additive source status fields from older responses", () => {
    const parsed = parseWithFallback(
      {
        agent_id: "agent",
        source_type: "github",
        repository: "acme/agent",
        ref: "main",
        synced_commit_sha: "abc",
        sync_status: "ready",
      },
      AgentSourceSchema,
      EMPTY_AGENT_SOURCE,
      { endpoint: "GET /api/agents/:id/source" },
    );
    expect(parsed.github_connected).toBe(false);
    expect(parsed.installation_id).toBeNull();
    expect(parsed.manifest_path).toBe("dingtalk-agent.json");
  });

  it("falls back when source identity is malformed", () => {
    const parsed = parseWithFallback(
      { agent_id: 123, repository: [] },
      AgentSourceSchema,
      EMPTY_AGENT_SOURCE,
      { endpoint: "GET /api/agents/:id/source" },
    );
    expect(parsed).toEqual(EMPTY_AGENT_SOURCE);
  });
});
