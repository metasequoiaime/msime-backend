import { z } from "zod";
import { auditEntrySchema } from "./perm";

export const meSessionSchema = z.object({
  id: z.string(),
  created_at: z.string(),
  last_seen_at: z.string(),
  device: z.string(),
  current: z.boolean(),
});
export type MeSession = z.infer<typeof meSessionSchema>;

export const tokenInfoSchema = z.object({ last4: z.string(), created_at: z.string(), expires_at: z.string() });

// GET /api/me: the signed-in admin's profile, monthly stats, preferences, own audit entries, sessions and token. via is how this request authenticated: a Google session, a personal access token, or the legacy admin token (which has no personal data).
export const meSchema = z.object({
  email: z.string(),
  name: z.string(),
  role: z.string(),
  owner: z.boolean(),
  via: z.enum(["session", "token", "legacy"]),
  joined_at: z.string().nullable(),
  stats: z.object({
    dict_prs_month: z.number(),
    community_month: z.number(),
    issues_month: z.number(),
    avg_handle_hours: z.number().nullable(),
  }),
  prefs: z.record(z.string(), z.boolean()),
  recent: z.array(auditEntrySchema),
  sessions: z.array(meSessionSchema),
  token: tokenInfoSchema.nullable(),
});
export type Me = z.infer<typeof meSchema>;

// POST /api/me {action: set_pref|revoke_session|regenerate_token}.
export const meActionSchema = z.looseObject({ ok: z.literal(true) });
// regenerate_token returns the full token exactly once.
export const issuedTokenSchema = tokenInfoSchema.extend({ ok: z.literal(true), token: z.string() });

export type MeAction =
  | { action: "set_pref"; key: string; value: boolean }
  | { action: "revoke_session"; id: string }
  | { action: "regenerate_token" };

// Personal notification preferences, keyed like the notification kinds they filter. available is false where the server has nothing to send yet.
export const prefRows = [
  { key: "notify_dict_pr", name: "词库 PR 提醒", description: "有新的官网词库提交时通知我", available: true },
  { key: "notify_report", name: "社区举报提醒", description: "皮肤、词库、回复模板被举报时通知我", available: true },
  { key: "notify_crash_spike", name: "崩溃告警", description: "新增崩溃分组或崩溃率上升超过 20%", available: true },
  { key: "weekly_digest", name: "每周摘要邮件", description: "每周一早上发送上周数据汇总", available: false },
] as const;
