export interface RunnerMachineBinding {
  bindingId: string;
  machineId: string;
  name: string;
  os: string;
  arch: string;
  clientVersion: string;
  roots: string[];
  online: boolean;
  disconnected: boolean;
  lastSeenAt: string | null;
  boundAt: string;
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
  agentId: string;
  agentName: string;
  machineName: string;
  os: string;
  arch: string;
  roots: string[];
  expiresAt: string;
}

export interface RunnerDeviceAuthorizationResult {
  status: "approved" | "denied";
  machineId?: string;
}
