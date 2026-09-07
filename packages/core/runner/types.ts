export interface RunnerMachineBinding {
  bindingId: string;
  machineId: string;
  name: string;
  os: string;
  arch: string;
  clientVersion: string;
  roots: string[];
	 enabledMcpServers: Record<string, string>;
	 mcpServers: RunnerMcpServer[];
	 inventoryRevision: string;
  online: boolean;
  disconnected: boolean;
  lastSeenAt: string | null;
  boundAt: string;
}

export interface RunnerMcpServer {
  name: string;
  title?: string;
  description?: string;
  version?: string;
  transport: "stdio" | "http";
  availability: string;
  detailStatus?: "available" | "unavailable";
  capabilities: string[];
  tools: RunnerMcpTool[];
  fingerprint: string;
}

export interface RunnerMcpTool {
  name: string;
  title?: string;
  description?: string;
}

export interface CreateRunnerReconnectCommandResponse {
  reconnectCommand: string;
  expiresAt: string;
}

export interface RunnerMachineBindingList {
  machines: RunnerMachineBinding[];
}

export interface AccountRunnerBinding {
  bindingId: string;
  workspaceId: string;
  workspaceName: string;
  workspaceSlug: string;
  agentId: string;
  agentName: string;
  roots: string[];
  disconnected: boolean;
  boundAt: string;
}

export interface AccountRunnerMachine {
  machineId: string;
  name: string;
  os: string;
  arch: string;
  clientVersion: string;
  online: boolean;
  lastSeenAt: string | null;
  bindings: AccountRunnerBinding[];
	 mcpServers: RunnerMcpServer[];
	 inventoryRevision: string;
}

export interface AccountRunnerBindingList {
  machines: AccountRunnerMachine[];
}

export interface AccountRunnerBindingTarget {
  bindingId: string;
  workspaceId: string;
  agentId: string;
}

export interface CreateRunnerPairingResponse {
  id: string;
  installCommand: string;
  expiresAt: string;
}

export interface RunnerDeviceAuthorization {
  userCode: string;
  state: "device_pending" | "approved" | "denied" | "consumed";
  machineName: string;
  os: string;
  arch: string;
  expiresAt: string;
}

export interface RunnerDeviceAuthorizationResult {
  status: "approved" | "denied";
  machineId?: string;
}
