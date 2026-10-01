import { z } from "zod";
import { APIError, errorMessage } from "./client";

// Dictionary pull request review: GET /api/dict-prs, GET /api/dict-prs/{n}, POST /api/dict-prs/{n}/approve|reject|trim (internal/server/admin_dict_prs.go).

export const prStates = ["open", "merged", "closed"] as const;
export type PRState = (typeof prStates)[number];
export type PRFilter = PRState | "all";

export const entryFlags = ["new", "dup", "ad", "bad"] as const;
export type EntryFlag = (typeof entryFlags)[number];

export const prCountsSchema = z.object({ total: z.number().int(), new: z.number().int(), dup: z.number().int(), flagged: z.number().int() });

export const prSummarySchema = z.object({
  number: z.number().int().positive(),
  title: z.string(),
  author: z.string(),
  author_bot: z.boolean(),
  created_at: z.string(),
  updated_at: z.string(),
  state: z.enum(prStates),
  url: z.string(),
  note: z.string(),
  counts: prCountsSchema.nullable(),
});
export type PRSummary = z.infer<typeof prSummarySchema>;

export const prListSchema = z.object({
  repo: z.string(),
  items: z.array(prSummarySchema),
  counts: z.object({ open: z.number().int(), merged: z.number().int(), closed: z.number().int(), all: z.number().int() }),
});
export type PRList = z.infer<typeof prListSchema>;

export const entrySchema = z.object({
  index: z.number().int().nonnegative(),
  file: z.string(),
  kind: z.enum(["words", "english", "translations"]),
  word: z.string(),
  pinyin: z.string(),
  flag: z.enum(entryFlags),
  reason: z.string().optional(),
});
export type Entry = z.infer<typeof entrySchema>;

export const prDetailSchema = z.object({
  repo: z.string(),
  pull: prSummarySchema,
  head_sha: z.string(),
  mergeable: z.boolean().nullable(),
  entries: z.array(entrySchema),
  submissions: z.array(z.object({ kind: z.string(), note: z.string(), created_at: z.string() })),
});
export type PRDetail = z.infer<typeof prDetailSchema>;

export const approveSchema = z.object({ ok: z.literal(true), merged: z.literal(true), count: z.number().int(), removed: z.number().int() });
export const trimSchema = z.object({ ok: z.literal(true), count: z.number().int(), removed: z.number().int(), head_sha: z.string() });
export const rejectSchema = z.object({ ok: z.literal(true) });

export const stateLabels: Record<PRState, string> = { open: "待审核", merged: "已通过", closed: "已驳回" };
export const stateTones = { open: "warn", merged: "ok", closed: "bad" } as const;
export const flagLabels: Record<EntryFlag, string> = { new: "可收录", dup: "词库已有 · 将去重", ad: "疑似广告", bad: "拼音不规范" };
export const flagTones = { new: "ok", dup: "mute", ad: "bad", bad: "warn" } as const;
export const kindLabels: Record<Entry["kind"], string> = { words: "词语", english: "英文", translations: "翻译" };

// authorLabel shows website submissions, which the word-submission GitHub App opens, as 官网机器人.
export function authorLabel(pr: Pick<PRSummary, "author" | "author_bot">): string {
  return pr.author_bot ? "官网机器人" : `@${pr.author}`;
}

const reviewMessages: Record<string, string> = {
  pr_changed: "PR 有了新的提交，请刷新后重新核对词条。",
  not_open: "这个 PR 已经不是待审核状态，请刷新列表。",
  not_mergeable: "GitHub 暂时无法合并这个 PR（可能有冲突或检查未通过）。",
  github_uncertain: "无法确认 GitHub 上的操作结果，请先在 GitHub 上核对再重试。",
  github_unavailable: "暂时无法连接 GitHub，请稍后重试。",
  github_rejected: "GitHub App 凭据被拒绝，请检查 admin.github 配置。",
  github_error: "GitHub 拒绝了这次操作，请在 GitHub 上查看 PR 状态。",
  invalid_keep: "勾选的词条无效，请刷新后重试。",
  invalid_head_sha: "缺少 PR 版本信息，请刷新后重试。",
  invalid_reason: "驳回原因不能为空，且不超过 500 字。",
};

// reviewErrorMessage explains the review endpoints' own error codes, which the shared client only knows by HTTP status.
export function reviewErrorMessage(error: unknown): string {
  if (error instanceof APIError && reviewMessages[error.code]) return reviewMessages[error.code];
  return errorMessage(error);
}
