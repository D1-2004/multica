import { z } from "zod";

export const ASBRegionsSchema = z
  .object({
    regions: z.array(z.string().regex(/^[a-z0-9]+(?:-[a-z0-9]+)+$/)),
  })
  .transform(({ regions }) => ({
    available: true,
    regions: [...new Set(regions)].sort(),
  }));

export const EMPTY_ASB_REGIONS = { available: false, regions: [] as string[] };
