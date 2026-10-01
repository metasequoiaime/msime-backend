import { format } from "date-fns";
import { errorMessage, isAPIError } from "../../api/client";
import type { Release, ReleaseStatus } from "../../api/release";
import type { Tone } from "../../ui/pill";

export const statusLabel: Record<ReleaseStatus, string> = { released: "已发布", beta: "公开测试", draft: "待发布", withdrawn: "已撤回" };
export const statusTone: Record<ReleaseStatus, Tone> = { released: "ok", beta: "info", draft: "mute", withdrawn: "bad" };

// Note kinds follow the "### 新增 / 修复 / 改进 / 说明 / 待办" headings of the release body.
export const noteKindClass: Record<string, string> = {
  新增: "bg-accent-soft text-accent-ink",
  修复: "bg-info-soft text-info",
  改进: "bg-panel-2 text-body",
  说明: "bg-warn-soft text-warn",
  待办: "bg-warn-soft text-warn",
};

const releaseErrors: Record<string, string> = {
  no_workflow: "该平台没有配置发布流水线（admin.github.platforms[].release_workflow）。",
  workflow_not_found: "GitHub 上找不到这个平台的发布流水线文件。",
  workflow_rejected: "发布流水线拒绝了这次触发：需要 workflow_dispatch 触发器和 version 输入。",
  invalid_version: "版本号格式不正确，例如 v0.5.5。",
  invalid_body: "发布说明过长或包含不支持的内容。",
  invalid_tag: "tag 与平台的前缀不匹配。",
  not_published: "草稿还没有发布，不能撤回。",
  already_withdrawn: "该版本已经撤回。",
  release_rejected: "GitHub 拒绝了这次修改。",
  github_unavailable: "GitHub 暂时无法访问，请稍后重试。",
  github_rejected: "GitHub App 的凭据或权限被拒绝，请检查安装权限。",
  github_repo_not_found: "GitHub App 无法访问该仓库，请检查仓库名和安装范围。",
};

export function releaseErrorText(error: unknown): string {
  for (const [code, text] of Object.entries(releaseErrors)) {
    if (isAPIError(error, code)) return text;
  }
  return errorMessage(error);
}

export function platformErrorText(code: string): string {
  return releaseErrors[code] ?? "读取失败，请稍后重试。";
}

export function releaseDate(release: Release): string {
  return release.published_at ? format(new Date(release.published_at), "yyyy-MM-dd") : "未发布";
}

export function repoShortName(repo: string): string {
  return repo.split("/").pop() ?? repo;
}

export function formatSize(bytes: number): string {
  if (bytes >= 1024 * 1024) return `${(bytes / 1024 / 1024).toFixed(1)} MB`;
  if (bytes >= 1024) return `${Math.round(bytes / 1024)} KB`;
  return `${bytes} B`;
}

export function formatCount(n: number): string {
  return n.toLocaleString("en-US");
}
