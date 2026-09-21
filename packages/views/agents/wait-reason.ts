export const DSH_WAIT_REASON_KEYS = [
  "sandbox_unhealthy",
  "destroy_unconfirmed",
  "create_intent_stale",
  "native_grant_busy",
  "task_drain_busy",
  "dsh_host_waiting",
] as const;

export type DSHWaitReasonKey = (typeof DSH_WAIT_REASON_KEYS)[number];

export function isDSHWaitReason(value: string | undefined): value is DSHWaitReasonKey {
  return (
    value === "sandbox_unhealthy" ||
    value === "destroy_unconfirmed" ||
    value === "create_intent_stale" ||
    value === "native_grant_busy" ||
    value === "task_drain_busy" ||
    value === "dsh_host_waiting"
  );
}
