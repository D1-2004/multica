import { z } from "zod";

const RunnerMachineBindingSchema = z
  .object({
    binding_id: z.string().uuid(),
    machine_id: z.string().uuid(),
    name: z.string().min(1),
    os: z.string().min(1),
    arch: z.string().min(1),
    client_version: z.string(),
    roots: z.array(z.string()),
    online: z.boolean(),
    disconnected: z.boolean().optional().default(false),
    last_seen_at: z.string().nullable(),
    bound_at: z.string(),
  })
  .loose()
  .transform((machine) => ({
    bindingId: machine.binding_id,
    machineId: machine.machine_id,
    name: machine.name,
    os: machine.os,
    arch: machine.arch,
    clientVersion: machine.client_version,
    roots: machine.roots,
    online: machine.online,
    disconnected: machine.disconnected,
    lastSeenAt: machine.last_seen_at,
    boundAt: machine.bound_at,
  }));

export const RunnerMachineBindingListSchema = z
  .object({ machines: z.array(RunnerMachineBindingSchema) })
  .loose();

const AccountRunnerBindingSchema = z
  .object({
    binding_id: z.string().uuid(),
    workspace_id: z.string().uuid(),
    workspace_name: z.string().min(1),
    workspace_slug: z.string().min(1),
    agent_id: z.string().uuid(),
    agent_name: z.string().min(1),
    roots: z.array(z.string()),
    disconnected: z.boolean(),
    bound_at: z.string(),
  })
  .loose()
  .transform((binding) => ({
    bindingId: binding.binding_id,
    workspaceId: binding.workspace_id,
    workspaceName: binding.workspace_name,
    workspaceSlug: binding.workspace_slug,
    agentId: binding.agent_id,
    agentName: binding.agent_name,
    roots: binding.roots,
    disconnected: binding.disconnected,
    boundAt: binding.bound_at,
  }));

const AccountRunnerMachineSchema = z
  .object({
    machine_id: z.string().uuid(),
    name: z.string().min(1),
    os: z.string().min(1),
    arch: z.string().min(1),
    client_version: z.string(),
    online: z.boolean(),
    last_seen_at: z.string().nullable(),
    bindings: z.array(AccountRunnerBindingSchema),
  })
  .loose()
  .transform((machine) => ({
    machineId: machine.machine_id,
    name: machine.name,
    os: machine.os,
    arch: machine.arch,
    clientVersion: machine.client_version,
    online: machine.online,
    lastSeenAt: machine.last_seen_at,
    bindings: machine.bindings,
  }));

export const AccountRunnerBindingListSchema = z
  .object({ machines: z.array(AccountRunnerMachineSchema) })
  .loose();

export const CreateRunnerPairingResponseSchema = z
  .object({
    id: z.string().uuid(),
    install_command: z.string().min(1),
    expires_at: z.string(),
  })
  .loose()
  .transform((pairing) => ({
    id: pairing.id,
    installCommand: pairing.install_command,
    expiresAt: pairing.expires_at,
  }));

export const CreateRunnerReconnectCommandResponseSchema = z
  .object({
    reconnect_command: z.string().min(1),
    expires_at: z.string(),
  })
  .loose()
  .transform((response) => ({
    reconnectCommand: response.reconnect_command,
    expiresAt: response.expires_at,
  }));

export const RunnerDeviceAuthorizationSchema = z
  .object({
    user_code: z.string().min(1),
    state: z.enum(["device_pending", "approved", "denied", "consumed"]),
    agent_id: z.string().uuid(),
    agent_name: z.string().min(1),
    machine_name: z.string().min(1),
    os: z.string().min(1),
    arch: z.string().min(1),
    roots: z.array(z.string()),
    expires_at: z.string(),
  })
  .loose()
  .transform((authorization) => ({
    userCode: authorization.user_code,
    state: authorization.state,
    agentId: authorization.agent_id,
    agentName: authorization.agent_name,
    machineName: authorization.machine_name,
    os: authorization.os,
    arch: authorization.arch,
    roots: authorization.roots,
    expiresAt: authorization.expires_at,
  }));

export const RunnerDeviceAuthorizationResultSchema = z
  .object({
    status: z.enum(["approved", "denied"]),
    machine_id: z.string().uuid().optional(),
  })
  .loose()
  .transform((result) => ({
    status: result.status,
    ...(result.machine_id ? { machineId: result.machine_id } : {}),
  }));
