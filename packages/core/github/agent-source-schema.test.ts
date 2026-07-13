import { describe, expect, it } from "vitest";
import { parseWithFallback } from "../api/schema";
import {
  AgentSourceSchema,
  EMPTY_AGENT_SOURCE,
  EMPTY_GITHUB_AGENT_PREVIEW,
  GitHubAgentPreviewSchema,
} from "../api/schemas";

describe("GitHub agent source API schemas", () => {
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
    expect(parsed.manifest_path).toBe("multica-agent.yaml");
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
