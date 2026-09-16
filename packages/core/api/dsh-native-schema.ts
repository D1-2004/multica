import { z } from "zod";

export const DSHNativeEntrySchema = z.object({
  access_id: z.string().uuid(),
  entry_url: z.string().max(2048).refine((value) => {
    try {
      const url = new URL(value);
      return url.protocol === "https:" && !url.username && !url.password && !url.port &&
        /^\/api\/dsh-native\/ui\/[0-9a-f-]{36}\/_multica\/open$/.test(url.pathname) && !url.search &&
        /^#entry=dnge_[A-Za-z0-9_-]{43}$/.test(url.hash);
    } catch { return false; }
  }, "Invalid native entry"),
  expires_at: z.string().datetime({ offset: true }),
}).refine((value) => { try { return new URL(value.entry_url).pathname === `/api/dsh-native/ui/${value.access_id}/_multica/open`; } catch { return false; } }, "Native entry scope mismatch").transform((value) => ({
  accessId: value.access_id,
  entryUrl: value.entry_url,
  expiresAt: value.expires_at,
}));

export type DSHNativeEntry = z.infer<typeof DSHNativeEntrySchema>;
