import { z } from "zod";

// Platforms a notice can target besides "all", in display order; the server validates the same set.
export const noticePlatforms = [
  { key: "windows", label: "Windows" },
  { key: "macos", label: "macOS" },
  { key: "linux", label: "Linux" },
  { key: "android", label: "Android" },
  { key: "ios", label: "iOS" },
  { key: "harmony", label: "HarmonyOS" },
] as const;

// Channels: site and app pull /v1/notices, telegram is pushed by the server when the notice is published.
export const noticeChannels = [
  { key: "site", label: "官网横幅" },
  { key: "app", label: "App 内通知" },
  { key: "telegram", label: "Telegram" },
] as const;

export type NoticeChannel = (typeof noticeChannels)[number]["key"];
export type NoticeStatus = "draft" | "live" | "archived";

export const noticeSchema = z.object({
  id: z.string(),
  title: z.string(),
  body: z.string(),
  targets: z.array(z.string()),
  channels: z.array(z.string()),
  status: z.enum(["draft", "live", "archived"]),
  created_by: z.string(),
  author: z.string(),
  created_at: z.string(),
  published_at: z.string().nullable(),
  updated_at: z.string(),
});

export const noticesSchema = z.object({
  items: z.array(noticeSchema),
  // telegram is false when admin.telegram is not configured; the console then shows the channel as 未配置.
  telegram: z.boolean(),
});

export type Notice = z.infer<typeof noticeSchema>;

// NoticeValue is the value of the save_notice_draft and publish_notice actions.
export type NoticeValue = { title: string; body: string; targets: string[]; channels: string[] };

// noticeValueMaxBytes is the server's limit for an action value; long bodies are rejected before they are sent.
export const noticeValueMaxBytes = 8192;

// noticeErrorMessages turns the notice actions' specific error codes into console copy.
export const noticeErrorMessages: Record<string, string> = {
  telegram_disabled: "服务端未配置 Telegram 渠道（admin.telegram），请取消勾选 Telegram。",
  telegram_failed: "Telegram 推送失败，公告没有发布，请稍后重试。",
  not_draft: "这条公告已经发布或归档，不能再编辑，请刷新列表。",
  already_archived: "这条公告已经归档。",
  invalid_title: "标题不能为空，最多 200 字，且不能包含控制字符。",
  invalid_body: "正文过长或包含不支持的字符。",
  invalid_targets: "请选择投放范围。",
  invalid_channels: "请至少选择一个渠道。",
  invalid_value: "公告内容过长，请缩短正文。",
};
