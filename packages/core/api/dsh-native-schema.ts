import { z } from "zod";

export const DSHNativeEntrySchema = z.object({
  access_id: z.string().uuid(),
  entry_url: z.string().max(2048).refine((value) => {
    try {
      const url = new URL(value);
      return url.protocol === "https:" && !url.username && !url.password && !url.port &&
        /^33124-[a-z0-9-]+\.[a-z0-9.-]+$/.test(url.hostname) &&
        url.pathname === "/_multica/open" && !url.search &&
        /^#entry=dnge_[A-Za-z0-9_-]{43}$/.test(url.hash);
    } catch { return false; }
  }, "Invalid native entry"),
  expires_at: z.string().datetime({ offset: true }),
}).transform((value) => ({
  accessId: value.access_id,
  entryUrl: value.entry_url,
  expiresAt: value.expires_at,
}));

export type DSHNativeEntry = z.infer<typeof DSHNativeEntrySchema>;
