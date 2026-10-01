# 管理后台

管理后台是挂在独立域名（默认 `admin.msime.app`）上的单页应用，源码在 `admin-web/`。技术栈：React 19、TypeScript、Vite 8、Tailwind CSS v4、TanStack Router / Query / Table、Radix UI、Recharts、Zod、react-hook-form、cmdk、date-fns、lucide-react、pnpm 10.15 和 Biome，不再使用 Sass。Vite 生成 `admin-web/dist/`，`admin-web/embed.go` 把产物嵌入 Go 二进制；Docker 在 Node 构建阶段重新构建前端，再编译进 Go 镜像。后台与现有 HTTP 服务共用端口，不需要单独启动 Node、前端容器或静态文件服务器。

后台页面受严格 CSP 约束：`default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'`。不允许 eval、内联样式表和外链图片，所以不显示 Google 头像，Zod 以 jitless 模式运行，也不使用会注入 `<style>` 的 Radix Overlay。

## 启用与部署

1. 启用现有 PostgreSQL 用户体系（`auth.enabled: true`），设置 `MSIME_DATABASE_URL` 与 `MSIME_AUTH_PEPPER`。数据库最低版本为 PostgreSQL 12，见下文「PostgreSQL 兼容性」。
2. 在 Google Cloud 项目中创建 Web OAuth 客户端，授权重定向 URI 设为 `https://admin.msime.app/api/auth/google/callback`。登录范围是 `openid email profile`，`profile` 只用来在外壳和个人中心显示管理员的 Google 名字。把 Client Secret 保存到 `MSIME_ADMIN_GOOGLE_SECRET`，把所有者邮箱白名单保存到 `MSIME_ADMIN_GOOGLE_EMAILS`（逗号分隔）。
3. 在配置中加入 `admin` 块。下面是包含全部配置项的示例，各块的含义见「配置」一节：

   ```json
   "admin": {
     "enabled": true,
     "host": "admin.msime.app",
     "token_env": "MSIME_ADMIN_TOKEN",
     "google": {
       "client_id": "YOUR_WEB_CLIENT_ID.apps.googleusercontent.com",
       "secret_env": "MSIME_ADMIN_GOOGLE_SECRET",
       "redirect_uri": "https://admin.msime.app/api/auth/google/callback",
       "allowed_emails_env": "MSIME_ADMIN_GOOGLE_EMAILS"
     },
     "environment": "生产环境",
     "github": {
       "app_id": 123456,
       "installation_id": 7890123,
       "private_key_env": "MSIME_ADMIN_GITHUB_APP_KEY",
       "dictionary_repo": "metasequoiaime/msime-dictionary",
       "issue_repos": ["metasequoiaime/msime", "metasequoiaime/msime-windows"],
       "platforms": [
         {"id": "windows", "name": "Windows", "repo": "metasequoiaime/msime-windows", "tag_prefix": "windows-v", "release_workflow": "release.yml", "assignee": "houko", "label": "windows"}
       ]
     },
     "services": [
       {"key": "translation", "name": "翻译", "provider": "DeepL", "quota": {"limit": 500000, "unit": "chars", "period": "month", "unit_price": 0}, "slow_ms": 3000}
     ],
     "telegram": {
       "bot_token_env": "MSIME_ADMIN_TELEGRAM_TOKEN",
       "chat_id": "@msime_news"
     }
   }
   ```

4. Google 登录模式不需要 `MSIME_ADMIN_TOKEN`。保留它时，登录页额外提供「管理员密钥登录」作为兼容入口；不配置 Google 时仍需要至少 32 字节的独立随机管理员密钥。管理员密钥不能以 `msime_pat_` 开头，这个前缀留给个人访问令牌，配置校验会拒绝。不要把任何密钥放进前端源码、安装包或版本库。
5. 运行账号有 DDL 权限时不需要单独迁移：启动时发现缺少后台表或列，会自动执行 `internal/account/admin_schema.sql` 和 `internal/account/admin_ops_schema.sql`，两者都是幂等的追加式迁移。运行账号按最小权限只有 DML 时，先用有 DDL 权限的账号执行 `./msime-server -config /config/config.json -migrate-users`，再给运行角色授予新表的 `SELECT, INSERT, UPDATE, DELETE` 以及 bigserial 序列的 `USAGE, SELECT`。新表包括 `admin_roles`、`admin_role_permissions`、`admin_tokens`、`admin_notifications`、`admin_notification_reads`、`admin_preferences`、`admin_notices`、`admin_crash_groups`、`admin_service_metrics`、`admin_service_daily`、`admin_incidents`、`admin_sensitive_words`、`admin_sensitive_hits`、`release_asset_snapshots`、`community_reports` 和 `word_submissions`；已有的 `admin_members`、`admin_sessions`、`admin_audit`、`admin_events` 和四张社区表新增了列。后台启用而表既不存在又补不上时，服务拒绝启动并在错误里说明原因。
6. 正常启动镜像，容器中的 `listen` 应为 `0.0.0.0:8080`。把 `admin.msime.app` 的 DNS 指向入口，在入口终止 HTTPS，把该域名的请求转发到相同的 Go 端口，并保留原始 Host。Go 不信任 `X-Forwarded-Host`。

示例 Nginx HTTPS 虚拟主机（证书路径、后端地址按部署调整）：

```nginx
server {
    listen 443 ssl;
    server_name admin.msime.app;
    ssl_certificate /etc/nginx/certs/admin.msime.app/fullchain.pem;
    ssl_certificate_key /etc/nginx/certs/admin.msime.app/privkey.pem;
    location / {
        proxy_set_header Host $host;
        proxy_pass http://msime-backend:8080;
    }
}
```

默认 `admin.enabled: false`，不会改变已有域名的路由。后台域名不承载 `/v1/*` 客户端 API。

本地私密配置可放在 `config.admin.local.json` 和 `.env.admin.local`（均被 Git 忽略）。Go 不自动加载 `.env`，可由部署工具注入，或在本机先 `set -a; . ./.env.admin.local; set +a`。

### PostgreSQL 兼容性

后台的全部迁移和查询都兼容 PostgreSQL 12 及以上版本：

- 不使用 PostgreSQL 13 才进入核心的 `gen_random_uuid()`。`admin_sessions.id` 这类短随机句柄由 `left(md5(random()::text||clock_timestamp()::text),16)` 生成。
- 社区皮肤相关表用到生成列，要求 12 及以上，这也是整个服务的最低版本。
- 在 `ADD COLUMN IF NOT EXISTS` 里写 `REFERENCES` 并非在所有版本上都受 `IF NOT EXISTS` 保护，所以外键（如 `admin_members.role → admin_roles`）放在 `DO` 块里，先查 `pg_constraint` 再单独添加；放宽 `admin_events.kind` 的 CHECK 约束也是同样的写法。
- 迁移可以重复执行。内置角色的默认权限只在创建角色行的那条语句里写入，之后在后台改过的权限矩阵不会被重跑的迁移覆盖。

## 登录、身份与令牌

后台有三种身份。每个 `/api/*` 请求都会重新解析身份和权限，所以改角色、停用成员、从白名单删除所有者都即时生效。

| 身份 | 认证方式 | 审计中的 actor | 角色与权限 |
| --- | --- | --- | --- |
| 所有者 | Google 登录，邮箱在 `google.allowed_emails` / `allowed_emails_env` 中 | `google:<sub>:<email>` | 恒为维护者，拥有全部 8 项权限，不受权限矩阵影响 |
| 成员 | Google 登录或个人访问令牌，邮箱在已启用的 `admin_members` 中 | `google:<sub>:<email>` 或 `pat:<email>` | `admin_members.role` 对应角色在权限矩阵中的权限 |
| 管理员密钥 | `Authorization: Bearer <MSIME_ADMIN_TOKEN>` | `legacy-token` | 维护者，拥有除 `manage_permissions` 以外的全部权限；没有个人中心 |

Google 登录时，后端校验 ID Token 的签名、issuer、audience、有效期、nonce、`email_verified`，以及邮箱是否在白名单或 `admin_members` 中。授权码流程使用 PKCE S256 和一次性 state；state 与浏览器 HttpOnly Cookie 绑定，在数据库中保留 10 分钟。管理员会话在 PostgreSQL 中只存令牌哈希，有效期固定 8 小时；浏览器使用 Secure、HttpOnly、SameSite=Lax、无 Domain 的 `__Host-` Cookie。会话另外记录创建时间、最近活动时间（每 5 分钟最多更新一次）、截断到 256 字符的 User-Agent 和 Google 名字，供个人中心显示和吊销。Cookie 会话发起的写请求要求 `Origin` 与回调地址的来源完全相同。普通 Google 用户不会因此成为管理员，也不会自动创建输入法用户账户。Google Cloud 项目处于测试发布状态时，需要把管理员加入测试用户。此处遵循 [Google OpenID Connect 服务端流程](https://developers.google.com/identity/openid-connect/openid-connect)。

| 端点 | 用途 |
| --- | --- |
| `GET /api/auth/session` | 登录状态、版本号、管理员邮箱和启用的登录方式 |
| `GET /api/auth/google/start` | 创建 state、nonce 和 PKCE，跳转到 Google |
| `GET /api/auth/google/callback` | 校验回调并创建管理员会话 |
| `POST /api/auth/logout` | 删除服务端会话并清除 Cookie |

### 个人访问令牌（PAT）

所有者和成员可以在个人中心生成个人访问令牌，用脚本调用后台 API：

- 格式为 `msime_pat_` 加 64 位十六进制，共 74 个字符。完整令牌只在生成时返回一次，数据库只存哈希和末 4 位。
- 有效期 30 天。重新生成会在同一事务里删除该邮箱的旧令牌，所以每人同时只有一个有效令牌。
- 用法：向后台域名发送 `Authorization: Bearer msime_pat_…`。令牌请求不需要 `Origin`，因为浏览器不会自动携带 Bearer 头，不存在跨站风险。
- 令牌没有独立的权限：每次请求都按该邮箱当前是否为所有者、以及 `admin_members` 记录重新判定，成员被停用后令牌立即失效。审计 actor 记为 `pat:<email>`，限流也按这个 actor 单独计。
- 管理员密钥身份没有个人中心，不能生成令牌。

## 角色与权限

权限共 8 项，与「权限日志」页的矩阵一一对应：

| 权限键 | 含义 |
| --- | --- |
| `review_dict_pr` | 词库 PR 的精简、通过和驳回 |
| `review_community` | 社区内容的通过、下架、恢复和删除；敏感词库的增删改 |
| `triage_issues` | Issue 分诊与回复；崩溃分组状态和为崩溃分组建 Issue；旧崩溃列表的处理；故障事件的开启、更新和恢复 |
| `ban_users` | 封禁、解封用户，吊销用户会话 |
| `publish_notices` | 发布、归档公告 |
| `trigger_release` | 触发发布流水线、编辑发布说明、撤回版本 |
| `view_cloud_usage` | 读取云端监控（`GET /api/cloud`），这是唯一受权限限制的读接口 |
| `manage_permissions` | 修改权限矩阵 |

内置 4 个角色，默认矩阵如下（✓ 为拥有）：

| 权限 | 维护者 `maintainer` | 审核志愿者 `reviewer` | 运营/客服 `operator` | 只读 `readonly` |
| --- | --- | --- | --- | --- |
| `review_dict_pr` | ✓ | ✓ | | |
| `review_community` | ✓ | ✓ | | |
| `triage_issues` | ✓ | ✓ | ✓ | |
| `ban_users` | ✓ | | ✓ | |
| `publish_notices` | ✓ | | ✓ | |
| `trigger_release` | ✓ | | | |
| `view_cloud_usage` | ✓ | | ✓ | ✓ |
| `manage_permissions` | ✓ | | | |

规则：

- 除 `GET /api/cloud` 外，所有 GET 请求对所有角色开放，「只读」角色就是靠这一点成立的。缺少权限的写操作返回 403 `permission_denied`，前端对应按钮置灰并提示所需权限，云端监控在侧栏和搜索中对没有 `view_cloud_usage` 的角色隐藏。
- 维护者的 `manage_permissions` 不能收回（409 `protected`），迁移也会在它缺失时补回。权限矩阵通过 `POST /api/permissions {action: grant|revoke, role, permission}` 修改。
- 所有者恒为维护者，不能在后台修改、停用或撤销（403 `protected_owner`），恢复入口始终在部署配置里。
- 成员的增删、启停和改角色走 `GET/POST /api/admins`，只有所有者能调用。请求体为 `{"email","action":"add|enable|disable|revoke|set_role","role"?}`：`add` 可带 `role`（默认维护者），`set_role` 必须带 `role`。最多 100 个成员（409 `admin_limit`），重复添加返回 409 `admin_exists`。停用不删除记录，重新启用后需重新登录。成员记录不创建输入法用户账户，也不发送邀请邮件，被添加者直接用 Google 账号登录。
- 引入角色之前已有的成员在迁移时成为维护者，权限不变。
- `save_notice_draft` 不需要权限，任何角色都能写草稿，发布才需要 `publish_notices`。个人中心的偏好、会话和令牌操作也不需要权限，只作用于本人。

## 限流与通用约定

- 限流按身份分桶：`admin:<actor>`，每分钟 300 次，超限返回 429 `rate_limit_exceeded` 并带 `Retry-After: 60`。以前是所有管理员共享一个每分钟 120 次的桶；外壳每 60 秒轮询一次，多人同时在线会互相挤占，所以改为按人计。同一个人的多个 Google 会话共享一个桶，PAT 另算一个桶。
- 登录相关端点（`/api/auth/*`）仍按来源 IP 计，每分钟 120 次。
- 每个后台请求有 15 秒的服务端超时。
- 数据库写操作统一经过 `POST /api/actions`，请求体为 `{"action","id"?,"user_id"?,"ids"?: [≤100],"reason"?: ≤500 字,"section"?,"value"?: ≤8 KiB JSON}`，拒绝未知字段。批量操作在单个事务内完成，审计与变更同事务写入，失败不部分生效。涉及 GitHub 的操作走各自的 REST 子路径，例如 `POST /api/dict-prs/{n}/approve`。
- 错误响应形如 `{"error":{"code","message"}}` 或 `{"error":"code"}`。前端的中文提示集中在 `admin-web/src/api/client.ts` 的 `codeMessages` 里，页面只在语境需要不同措辞时覆盖个别代码。
- 可逆操作（社区状态、Issue 标签、封禁、崩溃状态）完成后，提示条提供 4 秒「撤销」，调用服务端的反向操作。不可逆操作（合并词库 PR、为崩溃分组建 Issue）延迟 4 秒才真正发出，期间点「撤销」即取消；关闭页面时立即以 keepalive 请求发出。

## 外壳

外壳由 `GET /api/shell` 一次性提供，每 60 秒刷新：版本号、环境标签（`admin.environment`）、本人邮箱、名字、角色和权限、侧栏待处理角标（词库 PR、社区待复核、待分诊 Issue）、未读通知数和后端状态（`ok`、`degraded`、`down`；前端也接受 `unknown` 并显示「状态未知」）。

- 全局搜索（按 `/` 聚焦）：前端匹配页面名，`GET /api/search?q=` 最多返回 8 条，覆盖用户、社区内容、崩溃分组、敏感词、公告，以及内存缓存中的 GitHub PR、Issue 和 Release。结果通过 `?focus=<id>` 打开对应页面的详情。
- 通知：`GET /api/notifications?limit=20`，`POST /api/notifications/read {ids?|all:true}`。来源包括新举报（与举报同事务写入）、新词库 PR、崩溃分组 7 天环比上升超过 20%（每小时检查，同一分组 7 天内只提醒一次）、自动开启的故障事件、Release 状态变化和新 Issue。个人中心可以按类型关闭词库 PR、举报和崩溃提醒。新加入的管理员会看到全部历史通知为未读。
- 外观：浅色、深色或跟随系统；配色可选春、夏、秋、冬或「自动」。自动配色按本地月份切换（3–5 月春，6–8 月夏，9–11 月秋，12–2 月冬），页面一直开着跨过月份边界时也会自动切换。外观保存在浏览器本地。
- 旧路径重定向：`/admins`、`/audit` → `/perm`，`/system` → `/status`，`/crashes` → `/crash`，`/skins`、`/dictionaries`、`/replies` → `/community?tab=skins|dictionaries|replies`，原有查询参数保留。

## 页面

| 页面 | 路径 | 数据来源 | 写操作所需权限 |
| --- | --- | --- | --- |
| 数据概览 | `/` | `GET /api/overview` | — |
| 词库审核 | `/dictpr` | `GET /api/dict-prs`、`/api/dict-prs/{n}`（GitHub） | `review_dict_pr` |
| 社区审核 | `/community` | `GET /api/{skins,candidate-skins,plugins,dictionaries,replies}`、`/api/community/counts` | `review_community` |
| 问题分诊 | `/issues` | `GET /api/issues`、`/api/issues/{owner}/{repo}/{n}`（GitHub） | `triage_issues` |
| 敏感词库 | `/words` | `GET /api/sensitive-words` | `review_community` |
| 用户账号 | `/users` | `GET /api/users`、`/api/users/stats`、`/api/users/{id}` | `ban_users` |
| 下载记录 | `/downloads` | `GET /api/downloads/summary` | — |
| 公告推送 | `/notice` | `GET /api/notices` | 草稿无要求，发布与归档 `publish_notices` |
| 发布管理 | `/release` | `GET /api/releases`、`/api/releases/{platform}`（GitHub） | `trigger_release` |
| 云端监控 | `/cloud` | `GET /api/cloud` | 读取即需要 `view_cloud_usage` |
| 崩溃上报 | `/crash` | `GET /api/crash-groups`、`/api/crash-groups/{signature}` | `triage_issues` |
| 系统状态 | `/status` | `GET /api/status` | 故障事件需要 `triage_issues` |
| 权限日志 | `/perm` | `GET /api/permissions`、`/api/audit` | `manage_permissions`；成员管理仅限所有者 |
| 个人中心 | `/me` | `GET /api/me` | 只作用于本人 |

- **数据概览**：累计下载、注册用户、近 30 天新增、活跃设备（按天分 Windows、Mac/Linux、移动端）、近 7 天各平台活跃、无崩溃会话率及拖累最大的平台版本、最新出现的崩溃分组、待处理事项和服务状态。活跃与会话指标依赖客户端上报 `active`、`session`、`session_crash`，近 60 天没有任何上报时显示「客户端未上报」而不是 0。「累计下载」来自下载事件，不是安装数。
- **词库审核**：只列词库仓库自身 `community-words/` 分支上的 PR（忽略 fork），读最近 100 个。详情比较 PR 基准提交与头部提交的 `custom/{words,english,translations}.txt`，逐条标记：`ad` 命中敏感词，`bad` 不符合官网校验规则，`dup` 已在基准文件或内置词库中、或在 PR 内重复，`new` 可收录。可以只保留勾选项（逐个文件以 blob SHA 做比较交换后重写分支，并改写 PR 标题），可以通过（先精简，再以头部 SHA 为条件 squash 合并），也可以驳回（先评论「审核未通过：原因」再关闭）。页面显示每次投稿的补充说明和时间，来自 `word_submissions` 表。PR 作者是提交用的 GitHub App 时显示为「官网机器人」。
- **社区审核**：皮肤、候选皮肤、插件、词库、回复模板 5 个分类，按待复核、已通过、已下架筛选。卡片显示自动检查标记和被举报次数；详情抽屉显示举报记录、实时敏感词检查、作者的其他作品和皮肤键盘预览，候选皮肤的预览图由 `GET /api/candidate-skins/{id}/preview` 提供。操作为 `approve_content`、`remove_content`（需要原因）、`restore_content`（恢复下架前的状态）和永久删除。作者因封禁被下架的内容只能通过解封恢复（409 `owner_banned`）。审核规则见「社区事后审核」。
- **问题分诊**：遍历 `admin.github.issue_repos`，按平台 label 归类。状态映射：open 且无 `triaged` 标签为「新」，有 `triaged` 为「已分类」，closed 为「已关闭」，closed 且有 `duplicate` 标签为「重复」。可以分类（加 `triaged` 并指派给平台的 `assignee`）、标记重复、关闭、重新打开、取消分类和回复，每个操作都能撤销。每个仓库最多读 3 页共 300 个 open Issue，以及最近更新的 100 个 closed Issue。
- **敏感词库**：规则是普通词或 RE2 正则（不超过 200 个字符，不能匹配空文本），分类为广告导流、低俗、辱骂、违法、自定义，处理方式为「拦截」或「需复核」，并显示近 7 天命中次数。匹配器缓存在内存中，修改最多 30 秒后在所有副本生效；命中计数批量写回，进程退出时可能丢失最近 30 秒的计数。规则作用于词库投稿和社区上传，见下文。
- **用户账号**：搜索、按角色筛选（已验证邮箱是管理员成员时显示对应角色）、统计卡（总数、本周新增、开启设置同步的比例、已封禁数），详情抽屉显示脱敏的联系方式、登录设备（从 User-Agent 解析的平台，不显示地理位置）、作品和会话。`ban_user` 需要原因，在同一事务里封禁、吊销全部会话，并把该用户的社区内容以 `owner_banned` 下架；被封禁的账号登录、刷新令牌和会话鉴权都返回 403 `account_banned`。`unban_user` 只恢复因 `owner_banned` 下架的内容。
- **下载记录**：按平台、版本、安装包、渠道分组，显示今日和近 7 天下载量以及国内镜像占比，均按 UTC 自然日计。客户端和官网镜像的数据来自遥测下载事件；GitHub Release 渠道取每日资产下载量快照的差值，不依赖客户端上报，但每个资产的第一次快照计为 0，快照之前的下载不计入。
- **公告推送**：标题（不超过 200 字）、正文、投放平台（全部，或 windows、macos、linux、android、ios、harmony）和渠道（官网横幅 `site`、App 内通知 `app`、Telegram）。草稿可以反复编辑，切换到另一条草稿前会提示放弃未保存的修改。发布时勾选了 Telegram 的，先调用 Bot API `sendMessage`，失败则整条公告不发布。归档没有撤销。`/api/actions` 的 `value` 上限是 8 KiB，所以正文实际最多约 2700 个汉字，页面在发送前检查。「触达人数」需要客户端回执，显示「—」。
- **发布管理**：每个平台一张卡片，显示最新版本、状态和检查清单。「CI 全部通过」取自 tag 所在提交的 check run；「签名与公证」只有 workflow 里存在名为 `sign` 的 check run 时才显示；「更新日志已填写」看 release 说明是否为空；需要商店 API 的平台，商店一项显示「需手动」。历史版本从每个仓库最近 100 个 release 中按 `tag_prefix` 过滤：草稿为「待发布」，prerelease 为「公开测试」，正式版为「已发布」，带撤回标记的为「已撤回」。说明按 `### 新增 / 修复 / 改进 / 说明 / 待办` 分类显示。可以触发发布流水线、编辑说明和撤回版本；撤回会改为 prerelease、在说明开头加撤回标记，并把上一个正式版设为 latest。
- **云端监控**：每个上游服务的今日调用数、P95、错误率、24 小时曲线，以及本月（UTC）用量与 `admin.services` 中额度的对比；金额按用量乘以 `unit_price` 估算。只记录服务、耗时和状态类别，不记录请求内容。
- **崩溃上报**：按签名分组（规范化后的错误信息加第一个非系统栈帧，取 SHA-256 的前 16 位），显示 7 天次数、与前 7 天的环比、新出现标记和影响设备数（按 `install_id` 去重）。状态为未处理、已知问题、已修复；可以在平台对应的仓库建 Issue，状态随之改为已知问题并记录链接。已修复的分组再次崩溃时不会自动重新打开。
- **系统状态**：每 60 秒探测数据库，并汇总最近 5 分钟的上游指标，错误率或 P95 超过 `slow_ms` 判为降级；每日可用分钟数保留 60 天。降级时自动开启故障事件，恢复时自动关闭；也可以通过 `open_incident`、`update_incident`、`resolve_incident` 手动管理。只展示数据库和已配置的上游服务。
- **权限日志**：角色与权限矩阵、成员列表（所有者排在最前，显示会话数和最近活动），以及操作日志（`/api/audit`，可按 `action` 精确筛选、按 `actor` 模糊筛选，文案由 `action` 和 `detail` 生成）。
- **个人中心**：资料、本月处理量（词库 PR、社区审核、Issue）、社区审核的平均处理时长、通知偏好、最近 6 条本人操作、登录会话（可吊销其他会话）和个人访问令牌。「每周摘要」邮件尚未接入，开关置灰。

旧接口仍保留：`/api/overview?days=7|30`、`/api/users`、`/api/downloads`、`/api/crashes` 及 `resolve_crash` / `reopen_crash`、各社区列表与详情、`/api/audit`、`/api/admins`（仅所有者），以及只读的 `/api/system`（版本和各能力是否已配置，不返回上游 URL、模型名或密钥）。所有列表每页 50 条，支持 `q` 和 `page`，返回 `total` 和 `has_more`。

## 配置

### `admin.environment`

外壳顶部的环境标签，默认「生产环境」，最多 32 个字符，不能有首尾空白或换行。预发布或测试部署可以设为「测试环境」等，避免在错误的环境里操作。

### `admin.github`

后台以一个 GitHub App 的身份读写词库 PR、Issue、Release 和流水线。`app_id` 为 0 时整块只作文档用途，所有依赖 GitHub 的接口返回 404 `github_disabled`，对应页面显示「未配置」。

| 字段 | 说明 |
| --- | --- |
| `app_id`、`installation_id` | GitHub App 及其安装的 ID |
| `private_key_env` | 存放 App 私钥（PEM，PKCS#1 或 PKCS#8）的环境变量名 |
| `api_url` | 可选，默认 `https://api.github.com`，必须是 HTTPS |
| `dictionary_repo` | 词库审核的仓库，默认 `metasequoiaime/msime-dictionary` |
| `issue_repos` | 问题分诊的仓库，最多 20 个 |
| `platforms[]` | 发布平台，最多 16 个，按显示顺序排列 |

`platforms[]` 的字段：

- `id`：URL 中的稳定键，小写字母、数字和连字符，最多 32 位。
- `name`：显示名。
- `repo`：`owner/name`。
- `tag_prefix`：在仓库中选出本平台 release 的 tag 前缀，例如 `windows-v`。
- `release_workflow`：触发发布的 workflow 文件名，例如 `release.yml`；留空则不能在后台触发发布。
- `assignee`：分诊时指派的 GitHub 用户，可以留空。
- `label`：本平台 Issue 的标签。

GitHub App 需要安装到上面提到的每个仓库，并授予以下仓库权限：

| 权限 | 级别 | 用途 |
| --- | --- | --- |
| Contents | Read and write | 读写词库 PR 分支的文件；列出草稿 release，修改 release 说明和状态 |
| Pull requests | Read and write | 读取、改标题、合并、评论并关闭词库 PR |
| Issues | Read and write | Issue 分诊、指派、评论、开关；为崩溃分组建 Issue |
| Actions | Read and write | 以 `workflow_dispatch` 触发发布流水线 |
| Checks | Read-only | 发布检查清单中的 CI 和 `sign` 结果 |
| Metadata | Read-only | GitHub 对所有 App 的强制要求 |

每个平台的 `release_workflow` 必须声明 `workflow_dispatch` 触发器，并有一个名为 `version` 的输入。后台以仓库默认分支为 `ref`，发送 `{"inputs":{"version":"v0.5.5"}}`：

```yaml
on:
  workflow_dispatch:
    inputs:
      version:
        description: 要发布的版本号，例如 v0.5.5
        required: true
```

缺少触发器或输入时 GitHub 返回 422，后台报 409 `workflow_rejected`；文件不存在时报 409 `workflow_not_found`；未配置 `release_workflow` 时报 409 `no_workflow`。版本号必须匹配 `^v?[0-9][0-9A-Za-z.+-]{0,62}$`，否则返回 400 `invalid_version`。

GitHub 读请求在内存中缓存 60 秒，并使用 ETag 条件请求，以免耗尽每小时 5000 次的配额。几个平台共用一个仓库时，GitHub 的 latest 是整个仓库的：撤回某个平台的最新版后，该平台的上一个正式版会成为整个仓库的 latest，可能盖过另一个平台更新的版本。所以下载站应按 `tag_prefix` 自行挑选每个平台的版本并跳过 prerelease，不要直接读 `/releases/latest`。

`admin.github` 与官网词库投稿使用的 `word_submissions.github` 是两个独立配置，可以用同一个 App，也可以分开。

### `admin.services`

云端监控和系统状态页展示的上游服务，最多 32 个。不配置时，两个页面按配置文件中实际启用的上游服务和默认名称展示，但没有额度。

| 字段 | 说明 |
| --- | --- |
| `key` | 与指标记录一致的服务键：`cloud`、`chat`、`translation`、`transcription`、`streaming`、`images`、`niutrans_document`、`niutrans_image`、`niutrans_voice` |
| `name`、`provider` | 显示名（1–32 字）和服务商（不超过 64 字） |
| `quota.limit` | 月度额度，0 表示不限 |
| `quota.unit` | `calls`、`chars`、`hours` 或 `cny` |
| `quota.period` | 只支持 `month`（UTC 自然月） |
| `quota.unit_price` | 每个计量单位的估算人民币价格，0 表示不估算费用 |
| `slow_ms` | P95 超过此值判为降级，默认 3000，范围 1–120000 |

### `admin.telegram`

公告的 Telegram 渠道。`chat_id` 为空时渠道关闭，此时在后台勾选 Telegram 发布会返回 409 `telegram_disabled`。`chat_id` 是数字会话 ID 或 `@频道名`；`bot_token_env` 默认 `MSIME_ADMIN_TELEGRAM_TOKEN`，启动时校验令牌格式。Bot 需要能在目标频道或群组里发消息（频道需设为管理员）。

### 客户端跨域

`/v1/notices` 受 `/v1` 中间件的来源规则约束：浏览器从其他域名请求时，该域名必须在顶层 `allowed_origins` 中，否则返回 403。官网要显示公告横幅，需要先把官网域名加进去。

## 社区事后审核

社区内容采用事后审核（post-moderation）：

- 新上传的皮肤、候选皮肤、插件、词库和回复模板立即公开，状态为「待复核」（`pending`），由审核员随后复核。只有「已下架」（`removed`）的内容对作者以外的所有人隐藏：公开列表、详情、下载和评分接口都会排除它。已有内容在迁移时一律设为「已通过」。
- 上传时用敏感词库检查名称、描述等文本：命中「拦截」级规则返回 422 `blocked_content`，不保存；命中「需复核」级规则照常发布，并在待复核卡片上标出命中的词。
- 下架需要原因，可以撤销，撤销会恢复下架前的状态。
- 封禁作者会以 `owner_banned` 下架其全部内容，只有解封能恢复。
- 举报：登录用户通过 `POST /v1/community/reports` 举报，后台卡片显示被举报次数，详情列出举报原因，每条新举报生成一条通知。
- 目前没有事先审核（pre-moderation）模式，也没有向作者发送下架原因的机制；确认框里填写的原因只记录在审核记录和操作日志中。

## 公开客户端接口

以下接口在 API 域名上，不在后台域名上。

### `GET /v1/notices?platform=&channel=`

不需要认证，返回最近 20 条已发布的公告：`{"items":[{"id","title","body","targets":[…],"channels":[…],"published_at"}]}`。

- `platform` 为 `windows`、`macos`、`linux`、`android`、`ios`、`harmony` 之一，匹配投放到该平台或全部平台的公告。
- `channel` 为 `site`、`app` 或 `telegram`。
- 参数非法返回 400 `invalid_platform` 或 `invalid_channel`。
- 响应带 `Cache-Control: public, max-age=60`，发布和归档最多 60 秒后可见。该接口与登录共用每个 IP 每分钟 120 次的限额，客户端应遵守缓存头，不要频繁轮询。

### `POST /v1/community/reports`

需要已登录的用户会话。请求体：

```json
{"kind": "skins", "item_id": "…", "reason": "商标侵权", "detail": "可选补充说明"}
```

- `kind` 为 `skins`、`candidate-skins`、`plugins`、`dictionaries`、`replies` 之一；`reason` 1–64 字；`detail` 最多 1000 字。
- 只能举报自己能看到的内容（未下架；候选皮肤必须是公开的），否则返回 404 `item_not_found`。
- 同一用户重复举报同一内容只记一次：首次返回 201，重复返回 200，响应体都是 `{"reported":true}`。
- 每个账号每小时最多举报 30 次。

### `POST /v1/telemetry/events`

携带现有设备或用户 Bearer 令牌，请求体最多 32 KiB，成功返回 `202 {"accepted":true}`。

| 字段 | 规则 |
| --- | --- |
| `id` | 必填，16–128 字符的全局唯一事件 ID，推荐 UUID；重试必须复用，同一 ID 只记录第一次 |
| `kind` | 必填，见下表 |
| `platform` | 必填，1–32 字符，如 `windows`、`macos`、`android`、`ios`、`harmony` |
| `version` | 必填，1–64 字符 |
| `message` | 只有 `crash` 使用，且必填，最多 1000 字符 |
| `stack` | 只有 `crash` 使用，最多 16000 字符 |
| `artifact` | 可选，安装包名，1–64 字符，单行 |
| `channel` | 可选，分发渠道，匹配 `^[a-z0-9][a-z0-9_-]{0,31}$`；后台为 `cn-mirror`、`website`、`github`、`app-store`、`testflight`、`appgallery`、`google-play` 显示中文名 |
| `install_id` | 可选（`active` 必填），匿名安装 ID，16–64 位 `[A-Za-z0-9_-]`，不能含用户或硬件标识 |

| `kind` | 含义 | 用于 |
| --- | --- | --- |
| `download` | 一次安装包下载 | 下载记录、累计下载 |
| `crash` | 一次崩溃，带错误信息和堆栈 | 崩溃分组：入库时计算签名并更新分组 |
| `active` | 该安装当天活跃 | 活跃设备、各平台活跃、影响设备数 |
| `session` | 一次正常结束的会话 | 无崩溃会话率 |
| `session_crash` | 一次以崩溃结束的会话 | 无崩溃会话率 = session ÷ (session + session_crash)，按近 7 天计算 |

非 `crash` 事件不能带非空的 `message` 或 `stack`。后台统计时会归一平台名（`win` → windows，`mac`、`darwin` → macos，`ipados` → ios，`harmony`、`ohos` → HarmonyOS）。服务端以接收时间入库，离线上报算在接收日；后台不额外存 IP 或用户身份。客户端须在用户同意采集后发送，并先清理输入文本、密码、令牌和个人信息。数据保存在 PostgreSQL，多副本共享，当前不自动清理，需要按规模设置归档或保留策略。新增字段和类型都是可选的，旧客户端无需修改。

如果官网或客户端对 GitHub Release 的下载也上报 `channel=github` 的下载事件，这次下载会被遥测和 Release 快照各计一次。在确定统计口径之前，不要为 GitHub Release 下载上报遥测事件。

### 词库投稿的新错误码

官网的词库投稿接口现在可能返回：400 `blocked_word`（备注命中拦截级敏感词）、`invalid_entries.rejected` 中的 `blocked_word` 条目（词条命中拦截级敏感词），以及 503 `screening_unavailable`（敏感词检查暂时不可用）。官网表单应显示这些代码，至少回退显示 `message` 字段。

## 需要仓库外配合的改动

下列设计元素依赖客户端、官网或外部系统。后台已做好接收和展示，在对方完成之前显示空态或说明，不造假数据。

1. 活跃设备、各平台活跃、无崩溃会话率、影响设备数：各客户端需要上报 `active`、`session`、`session_crash` 事件和匿名 `install_id`。
2. 下载记录的安装包和渠道：官网镜像和客户端需要在下载事件里带上 `artifact` 和 `channel`。GitHub Release 渠道靠快照获取，不依赖这一条。
3. 社区举报：客户端需要增加「举报」按钮，调用 `POST /v1/community/reports`。
4. 公告的 App 内通知和官网横幅：App 和官网需要拉取 `GET /v1/notices`，官网域名还要加入 `allowed_origins`。在此之前公告只写入数据库，用户看不到。
5. 「下载后完成安装」比例无法测量，已去掉。
6. 公告只有 Telegram 一个推送渠道（QQ 群没有官方 Bot API）；触达人数需要客户端回执，显示「—」。
7. 登录方式是 Google OIDC，两步验证由 Google 账号控制，后台不提供 2FA、通行密钥或 GitHub 登录。
8. 登录设备不显示地理位置，这需要 GeoIP 库。
9. 没有站内信系统，用户详情不提供「发送站内信」；也没有 AI 额度模型。
10. 问题分诊不显示「官网匿名提交」和诊断附件，因为仓库里没有官网反馈端点；来源一律是 GitHub。
11. 「商店审核中」以及 iOS、HarmonyOS 的商店状态需要 App Store Connect 或 AppGallery API，检查清单显示「需手动」。
12. 发布检查清单的「签名与公证」只有 workflow 定义了名为 `sign` 的 check run 时才显示。
13. 词库仓库实际是 `metasequoiaime/msime-dictionary`。

## 审计

所有写操作都写入 `admin_audit`，包括 `actor`、`action`、`target` 和 `detail`（原因、条数、新旧值等 JSON）。数据库变更与审计在同一事务里，失败不部分生效。GitHub 和 Telegram 这类外部副作用无法与数据库放在同一事务里，做法是外部调用成功后再写审计，失败则不写；因此极少数情况下会出现外部动作已生效、而审计或数据库提交失败的情况，例如 Telegram 消息已发出但公告没有标记为已发布。标记通知已读只改本人的已读记录，不写审计。

## 本地开发与验证

```sh
pnpm --dir admin-web install --frozen-lockfile
pnpm --dir admin-web check
pnpm --dir admin-web lint
pnpm --dir admin-web build
python3 admin-web/tests/csp_smoke.py
go test ./...
go build -o /tmp/msime-server ./cmd/msime-server
```

修改 `admin-web/src/` 后执行 `pnpm --dir admin-web build`，再重新编译 Go。生成的 `dist/` 随源码提交，保证直接 `go build` 也可用；CI 会重新构建并核对产物。运行镜像不需要 Node.js。

`tests/csp_smoke.py` 用 Python 版 Playwright 和 Chromium，在模拟后端上逐页打开构建产物，检查 CSP 违规、旧路径重定向和外壳弹层，并单独构建 `tests/harness` 测试共享组件（表格、确认框、提示条、抽屉、图表）。改动依赖、共享组件或构建配置后都要运行。

开发时把 Go 的 `admin.host` 设为 `admin.localhost`、`listen` 设为 `127.0.0.1:18089`，然后运行 `pnpm --dir admin-web dev`；Vite 把 `/api` 请求代理到该 Go 服务，并设置开发用的 Host 和 Origin。也可以直接访问 `http://admin.localhost:18089` 验证实际嵌入的产物。生产必须通过 HTTPS 入口访问。

PostgreSQL 集成测试需要设置 `MSIME_TEST_DATABASE_URL`，数据库名必须含 `msime_auth_test`，只能使用一次性测试库，测试会清空测试表。多个包共用一个库时，用 `go test -p 1 ./...` 串行运行。

## 已知限制

- 多副本部署：敏感词、通知去重和 GitHub 缓存都在各副本的内存中，最多有 30–60 秒的不一致；系统状态的 5 分钟窗口按副本计算，自动故障事件可能来回开关。目前按单实例部署设计。
- 数据库本身不可用时，宕机分钟数和自动故障事件无法写入，`/api/status` 返回 503。
- 词库 PR 只读最近 100 个；条目比较以 PR 的 `base.sha` 为准，分支创建后主干上删除的行会显示为新增。驳回时如果评论成功而关闭失败，重试会再评论一次。
- 发布历史每个仓库只读最近 100 个 release，共用仓库的平台多时，较早的版本会从历史和每日快照中消失。并发撤回或编辑同一个 release 以最后一次为准。
- GitHub Release 的「今日」是当天快照与前一天快照的差值，快照任务在 UTC 清晨运行时主要反映前一天的下载；页面会显示「快照截至」日期。
- 社区内容下架后再恢复，会丢失上传时的自动检查标记；抽屉里的实时检查不受影响。
- 只在白名单中、没有 `admin_members` 记录的所有者，在用户列表里显示为普通用户。
- 在数据库里直接封禁（不经过后台）的用户，现有会话鉴权返回 401 而不是 403 `account_banned`；经后台封禁会同时吊销全部会话，不受影响。
- 后台列表搜索用 `to_jsonb(row)::text` 做模糊匹配，皮肤行包含设计 JSON，数字或字段名这类查询会匹配到几乎所有皮肤。
- 弹窗打开之前出现的提示条，其「撤销」仍可用鼠标点击，但弹窗打开期间辅助技术读不到它。弹窗打开之后出现的提示条不会吞掉 Escape，按 Escape 关闭的是弹窗。
