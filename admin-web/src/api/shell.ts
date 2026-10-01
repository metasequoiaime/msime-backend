import { z } from "zod";

export const permissionKeys = ["review_dict_pr", "review_community", "triage_issues", "ban_users", "publish_notices", "trigger_release", "view_cloud_usage", "manage_permissions"] as const;
export type Permission = (typeof permissionKeys)[number];

export const roleLabels: Record<string, string> = { maintainer: "维护者", reviewer: "审核志愿者", operator: "运营/客服", readonly: "只读" };
export function roleLabel(role: string): string {
  return roleLabels[role] ?? role;
}

// GET /api/shell: everything the shell needs in one request, polled every 60s.
export const shellSchema = z.object({
  version: z.string(),
  environment: z.string(),
  me: z.object({ email: z.string(), name: z.string().nullish(), role: z.string(), permissions: z.array(z.string()) }),
  pending: z.object({ dict_prs: z.number().int().nonnegative(), community: z.number().int().nonnegative(), issues: z.number().int().nonnegative() }),
  unread_notifications: z.number().int().nonnegative(),
  status: z.enum(["ok", "degraded", "down"]),
});
export type Shell = z.infer<typeof shellSchema>;

// GET /api/search?q=: at most 8 server-side matches; page names are matched client-side.
export const searchResultSchema = z.object({
  kind: z.string(),
  id: z.union([z.string(), z.number()]).transform(String),
  title: z.string(),
  where: z.string(),
  target: z.string(),
});
export type SearchResult = z.infer<typeof searchResultSchema>;
export const searchSchema = z.union([z.object({ items: z.array(searchResultSchema) }), z.array(searchResultSchema).transform(items => ({ items }))]);

// GET /api/notifications?limit=20 and POST /api/notifications/read {ids}|{all:true}.
export const notificationSchema = z.object({
  id: z.union([z.string(), z.number()]).transform(String),
  kind: z.string(),
  title: z.string(),
  target_page: z.string(),
  target_id: z.union([z.string(), z.number()]).nullish().transform(value => value == null ? "" : String(value)),
  created_at: z.string(),
  read: z.boolean(),
});
export type Notification = z.infer<typeof notificationSchema>;
export const notificationsSchema = z.object({ items: z.array(notificationSchema), unread: z.number().int().nonnegative() });
export const notificationsReadSchema = z.object({ ok: z.literal(true) });

export const notificationKindLabels: Record<string, string> = {
  dict_pr: "词库审核",
  report: "社区举报",
  community: "社区",
  crash_spike: "崩溃告警",
  incident: "系统事件",
  release: "发布",
  issue: "问题反馈",
};
