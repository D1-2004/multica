import { z } from "zod";

const revision = z.string().regex(/^[1-9][0-9]{0,18}$/);

export const DSHProfileSchema = z.object({
  state: z.enum(["unprepared", "waiting_for_builds", "pending_host", "applied", "configuration_changed"]),
  desired_revision: revision.optional(),
  applied_revision: revision.optional(),
  applied_generation: z.number().int().nonnegative(),
  applied_sandbox_id: z.string().optional(),
  current: z.boolean(),
}).superRefine((value, ctx) => {
  if (value.current !== (value.state === "applied") ||
      (value.current && (!value.desired_revision || value.applied_revision !== value.desired_revision ||
        value.applied_generation < 1 || !value.applied_sandbox_id)) ||
      (value.state !== "unprepared" && !value.desired_revision)) {
    ctx.addIssue({ code: "custom", message: "Profile application requires matching revision and Host receipt" });
  }
}).transform((value) => ({
  state: value.state,
  desiredRevision: value.desired_revision ?? "",
  appliedRevision: value.applied_revision ?? "",
  appliedGeneration: value.applied_generation,
  appliedSandboxId: value.applied_sandbox_id ?? "",
  current: value.current,
}));

export type DSHProfileStatus = z.infer<typeof DSHProfileSchema>;
