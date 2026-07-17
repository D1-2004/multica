import type { Workspace } from "./workspace";
import type { BeginDingTalkInstallResponse } from "./dingtalk";

export interface FDEOnboardingState {
  configured: boolean;
  create_only: boolean;
  workspaces: Workspace[];
}

export interface ProvisionFDEOnboardingRequest {
  workspace_name: string;
}

export interface ProvisionFDEOnboardingResponse {
  workspace: Workspace;
  runtime_id: string;
  agent_id: string;
  agent_created: boolean;
  install_complete: boolean;
  install?: BeginDingTalkInstallResponse;
}
