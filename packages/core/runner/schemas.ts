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
    lastSeenAt: machine.last_seen_at,
    boundAt: machine.bound_at,
  }));

export const RunnerMachineBindingListSchema = z
  .object({ machines: z.array(RunnerMachineBindingSchema) })
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
