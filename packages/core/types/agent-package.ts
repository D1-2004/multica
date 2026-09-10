import type { CoordinatorContract } from "./agent";
import type { GitHubAgentSkillPreview } from "./github";

export interface AgentPackageRequirements {
  secrets: string[];
  deferred_bindings: string[];
  runtime_provider: string;
}

export interface AgentPackagePreview {
  definition?: Record<string, unknown>;
  preview_id: string;
  expires_at: string;
  package_hash: string;
  manifest_version: string;
  name: string;
  description: string;
  instructions: string;
  coordinator_contract?: CoordinatorContract | null;
  skills: GitHubAgentSkillPreview[];
  manifest_fields: string[];
  configuration_fields: string[];
  warnings: string[];
  requirements: AgentPackageRequirements;
}

export interface CreateAgentPackageRequest {
  preview_id: string;
  runtime_id: string;
  name?: string;
  description?: string;
  secrets?: Record<string, string>;
  deferred_bindings?: string[];
}
