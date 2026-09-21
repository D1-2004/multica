import { z } from "zod";

export const FilesystemRootSchema = z.object({
  kind: z.enum(["shared", "agent"]),
  id: z.string().optional(),
  provisioned: z.boolean(),
  access: z.string().optional(),
});

export const FilesystemRootsSchema = z.object({
  roots: z.array(FilesystemRootSchema),
});

export const FilesystemEntrySchema = z.object({
  name: z.string(),
  path: z.string(),
  is_dir: z.boolean(),
  size_bytes: z.number().optional(),
  modified_at: z.string().optional(),
});

export const FilesystemEntriesSchema = z.object({
  root: z.string(),
  path: z.string(),
  offset: z.number(),
  limit: z.number(),
  entries: z.array(FilesystemEntrySchema),
  count: z.number(),
  truncated: z.boolean(),
  next_offset: z.number().nullable().optional(),
});

export const FilesystemGrantSchema = z.object({
  workspace_id: z.string(),
  agent_id: z.string(),
  access: z.string(),
  generation: z.number().optional(),
  task_role_arn: z.string().optional(),
});

export const FilesystemGrantsSchema = z.object({
  grants: z.array(FilesystemGrantSchema),
});

export type FilesystemRoots = z.infer<typeof FilesystemRootsSchema>;
export type FilesystemRoot = z.infer<typeof FilesystemRootSchema>;
export type FilesystemEntry = z.infer<typeof FilesystemEntrySchema>;
export type FilesystemEntries = z.infer<typeof FilesystemEntriesSchema>;
export type FilesystemGrant = z.infer<typeof FilesystemGrantSchema>;
export type FilesystemGrants = z.infer<typeof FilesystemGrantsSchema>;
