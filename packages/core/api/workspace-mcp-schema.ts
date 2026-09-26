import { z } from "zod";

export const WorkspaceMCPConnectionSchema = z
  .object({
    id: z.string(),
    name: z.string(),
    scopes: z.array(z.string()).catch([]),
    expires_at: z.string(),
    revoked_at: z.string().nullable().catch(null),
    last_used_at: z.string().nullable().catch(null),
  })
  .transform((v) => ({
    id: v.id,
    name: v.name,
    scopes: v.scopes,
    expiresAt: v.expires_at,
    revokedAt: v.revoked_at,
    lastUsedAt: v.last_used_at,
  }));
export type WorkspaceMCPConnection = z.infer<
  typeof WorkspaceMCPConnectionSchema
>;
export const WorkspaceMCPConnectionsSchema = z.array(
  WorkspaceMCPConnectionSchema,
);
export const WorkspaceMCPLinkSchema = z.object({
  id: z.string(),
  url: z.string().url(),
});
export interface CreateWorkspaceMCPConnection {
  name: string;
  scopes: string[];
  expires_at: string;
}
