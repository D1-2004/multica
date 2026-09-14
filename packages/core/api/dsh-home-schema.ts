import { z } from "zod";

export const DSHHomeSchema = z.object({
  provisioned: z.boolean(),
  state: z.enum(["unprovisioned", "planned", "creating", "complete", "offline", "running", "retiring"]),
  step: z.number().int().min(0).max(6),
  generation: z.number().int().nonnegative(),
  sandbox_id: z.string().optional(),
}).superRefine((value, ctx) => {
  if (value.provisioned && (value.step !== 6 || !["offline", "creating", "running", "retiring"].includes(value.state))) {
    ctx.addIssue({ code: "custom", message: "Provisioned Home requires a complete resource chain" });
  }
}).transform((value) => ({
  provisioned: value.provisioned,
  state: value.state,
  step: value.step,
  generation: value.generation,
  sandboxId: value.sandbox_id ?? "",
}));

export type DSHHomeStatus = z.infer<typeof DSHHomeSchema>;
