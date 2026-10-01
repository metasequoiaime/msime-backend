import { z } from "zod";

// Release status as the server derives it from GitHub: draft (待发布), prerelease (公开测试), published (已发布), or withdrawn by the console (已撤回).
export const releaseStatuses = ["draft", "beta", "released", "withdrawn"] as const;
export type ReleaseStatus = (typeof releaseStatuses)[number];

export const releaseNoteSchema = z.object({ kind: z.string(), text: z.string() });
export type ReleaseNote = z.infer<typeof releaseNoteSchema>;

export const releaseAssetSchema = z.object({ name: z.string(), size: z.number(), downloads: z.number(), url: z.string() });
export type ReleaseAsset = z.infer<typeof releaseAssetSchema>;

export const releaseSchema = z.object({
  id: z.number(),
  tag: z.string(),
  version: z.string(),
  name: z.string(),
  status: z.enum(releaseStatuses),
  created_at: z.string(),
  published_at: z.string().nullable(),
  author: z.string(),
  body: z.string(),
  notes: z.array(releaseNoteSchema),
  assets: z.array(releaseAssetSchema),
  downloads: z.number(),
  url: z.string(),
});
export type Release = z.infer<typeof releaseSchema>;

export const releasePlatformSchema = z.object({ id: z.string(), name: z.string(), repo: z.string(), tag_prefix: z.string(), workflow: z.string() });
export type ReleasePlatform = z.infer<typeof releasePlatformSchema>;

// Checklist state: manual means a person has to confirm it (a store review), unknown that GitHub has no check runs for the commit.
export const releaseCheckSchema = z.object({ key: z.string(), label: z.string(), state: z.enum(["passed", "failed", "pending", "unknown", "manual"]), note: z.string() });
export type ReleaseCheck = z.infer<typeof releaseCheckSchema>;

// GET /api/releases: one entry per admin.github.platforms entry, with its newest release that is not withdrawn. error is set when only this platform could not be read.
export const releaseSummarySchema = releasePlatformSchema.extend({
  latest: releaseSchema.nullable(),
  checklist: z.array(releaseCheckSchema),
  error: z.string().optional(),
});
export type ReleaseSummary = z.infer<typeof releaseSummarySchema>;
export const releasesSchema = z.object({ platforms: z.array(releaseSummarySchema) });

// GET /api/releases/{platform}: the platform's releases, newest first.
export const releaseHistorySchema = z.object({ platform: releasePlatformSchema, releases: z.array(releaseSchema) });
export type ReleaseHistory = z.infer<typeof releaseHistorySchema>;

// POST /api/releases/{platform}/trigger {version} and POST /api/releases/{platform}/{tag}/notes {body}.
export const releaseOkSchema = z.object({ ok: z.literal(true) });

// POST /api/releases/{platform}/{tag}/withdraw: previous is the platform's earlier published release, latest_restored whether it became the repository's latest again.
export const withdrawResultSchema = z.object({ ok: z.literal(true), previous: z.string().nullable(), latest_restored: z.boolean() });

export function releasePath(platform: string, tag?: string, action?: "trigger" | "notes" | "withdraw"): string {
  const parts = ["releases", encodeURIComponent(platform)];
  if (tag) parts.push(...tag.split("/").map(encodeURIComponent));
  if (action) parts.push(action);
  return parts.join("/");
}
