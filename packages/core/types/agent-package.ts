import type { CoordinatorContract } from "./agent";
import type { GitAgentSkillPreview } from "./git-repo";

export interface AgentPackageRequirements {
  secrets: string[];
  deferred_bindings: string[];
  runtime_provider: string;
  binding_declarations?: { path: string; declaration: unknown }[];
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
  skills: GitAgentSkillPreview[];
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

export interface AgentPackageBinding {
  path: string;
  status: string;
  declaration: unknown;
  current: unknown;
  current_fingerprint: string;
  config_tab: string;
  message: string;
}

export interface AgentPackageBindingReport {
  revision: string;
  bindings: AgentPackageBinding[];
  resources: { ref: string; kind: string; label: string }[];
}

export interface ConfirmAgentPackageBindingRequest {
  path: string;
  revision: string;
  current_fingerprint: string;
  mappings: Record<string, string>;
}
