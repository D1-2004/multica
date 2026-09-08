import { z } from "zod";
import type { ASBNetworkPolicySettings } from "../runtimes/network-policy";

export const ASBNetworkPolicySchema = z.object({
  default_action: z.literal("deny"),
  default_targets: z.array(z.string()),
  custom_targets: z.array(z.string()),
  effective_targets: z.array(z.string()),
}).transform((data): ASBNetworkPolicySettings => ({
  defaultAction: data.default_action,
  defaultTargets: data.default_targets,
  customTargets: data.custom_targets,
  effectiveTargets: data.effective_targets,
  available: true,
}));

export const EMPTY_ASB_NETWORK_POLICY: ASBNetworkPolicySettings = {
  defaultAction: "deny", defaultTargets: [], customTargets: [], effectiveTargets: [], available: false,
};
