export interface RunnerMachineBinding {
  bindingId: string;
  machineId: string;
  name: string;
  os: string;
  arch: string;
  clientVersion: string;
  roots: string[];
  online: boolean;
  lastSeenAt: string | null;
  boundAt: string;
}

export interface RunnerMachineBindingList {
  machines: RunnerMachineBinding[];
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
