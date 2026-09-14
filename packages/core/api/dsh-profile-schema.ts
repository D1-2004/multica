import { z } from "zod";

const revision = z.string().regex(/^[1-9][0-9]{0,18}$/);

export const DSHProfileSchema = z.object({
  state: z.enum(["unprepared", "waiting_for_builds", "build_failed", "pending_host", "applied", "configuration_changed"]),
  desired_revision: revision.optional(),
  applied_revision: revision.optional(),
  applied_generation: z.number().int().nonnegative(),
  applied_sandbox_id: z.string().optional(),
  current: z.boolean(),
  builds: z.array(z.object({
    id: z.string().uuid().optional(),
    can_retry: z.boolean().optional(),
    package_name: z.string().min(1).max(214),
    version: z.string().min(1),
    state: z.enum(["queued", "ready", "failed"]),
  })).max(128).optional(),
}).superRefine((value, ctx) => {
  if (value.current !== (value.state === "applied") ||
      (value.current && (!value.desired_revision || value.applied_revision !== value.desired_revision ||
        value.applied_generation < 1 || !value.applied_sandbox_id)) ||
      (value.state !== "unprepared" && !value.desired_revision) ||
      (value.current && value.builds?.some((build) => build.state !== "ready"))) {
    ctx.addIssue({ code: "custom", message: "Profile application requires matching revision and Host receipt" });
  }
}).transform((value) => ({
  state: value.state,
  desiredRevision: value.desired_revision ?? "",
  appliedRevision: value.applied_revision ?? "",
  appliedGeneration: value.applied_generation,
  appliedSandboxId: value.applied_sandbox_id ?? "",
  current: value.current,
  builds: (value.builds ?? []).map((build) => ({ id: build.id ?? "", canRetry: build.can_retry === true && build.state === "failed" && !!build.id, packageName: build.package_name, version: build.version, state: build.state })),
}));

export type DSHProfileStatus = z.infer<typeof DSHProfileSchema>;
