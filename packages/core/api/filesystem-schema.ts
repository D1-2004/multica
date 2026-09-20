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

export type FilesystemRoots = z.infer<typeof FilesystemRootsSchema>;
export type FilesystemRoot = z.infer<typeof FilesystemRootSchema>;
