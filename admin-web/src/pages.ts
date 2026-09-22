export const pages = {
  admins: ["管理员管理", "管理 Google 后台账号及访问权限。", "admins"],
  overview: ["数据总览", "产品的每一步成长，都有迹可循。", "overview"],
  users: ["用户管理", "注册用户与当前有效会话，可撤销用户的全部登录会话。", "users"],
  downloads: ["下载记录", "安装包下载上报事件，按事件 ID 去重；不代表独立用户或安装数。", "downloads"],
  crashes: ["崩溃信息", "查看客户端报告，跟进并标记处理状态。", "crashes"],
  skins: ["社区皮肤", "管理用户发布的皮肤；下载数按用户与皮肤去重。", "skins"],
  dictionaries: ["社区词库", "管理公开共享词库，不展示用户的私人词典。", "dictionaries"],
  replies: ["回复模板", "管理社区公开的快捷回复模板。", "replies"],
  audit: ["操作日志", "管理员操作记录，按操作时间倒序排列。", "audit"],
  system: ["系统状态", "查看当前版本、引擎和上游能力配置。", "system"],
} as const;
export type Page = keyof typeof pages;
export type ListPage = Exclude<Page, "overview" | "admins" | "system">;
export function isListPage(value: string): value is ListPage { return value !== "overview" && value !== "admins" && Object.hasOwn(pages, value); }
export const columns: Record<ListPage, readonly (readonly [string, string])[]> = {
  users: [["display_name", "用户"], ["id", "用户 ID"], ["created_at", "注册时间"], ["sessions", "有效会话"]],
  downloads: [["platform", "平台"], ["version", "版本"], ["id", "事件 ID"], ["created_at", "上报时间"]],
  crashes: [["message", "错误信息"], ["platform", "平台"], ["version", "版本"], ["resolved", "状态"], ["created_at", "上报时间"]],
  skins: [["name", "皮肤名称"], ["description", "描述"], ["owner_id", "发布者 ID"], ["downloads", "下载用户数"], ["created_at", "发布时间"]],
  dictionaries: [["name", "词库名称"], ["entries", "词条数"], ["revision", "修订版本"], ["saves", "收藏用户数"], ["created_at", "发布时间"], ["updated_at", "更新时间"]],
  replies: [["name", "模板名称"], ["prompt", "内容"], ["revision", "修订版本"], ["created_at", "发布时间"], ["updated_at", "更新时间"]],
  audit: [["actor", "管理员"], ["action", "操作"], ["target", "目标 ID"], ["created_at", "操作时间"]],
};
export const actionLabels: Record<string, string> = { admin_add: "添加管理员", admin_enable: "启用管理员", admin_disable: "停用管理员", admin_revoke: "撤销管理员会话", revoke_session: "撤销单个用户会话", revoke_sessions: "撤销会话", delete_skin: "删除皮肤", delete_dictionary: "删除词库", delete_reply: "删除模板", resolve_crash: "标记已处理", reopen_crash: "重新打开" };
