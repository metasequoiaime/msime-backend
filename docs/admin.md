# 管理后台

`admin-web/` 与官网 MSIME-Web 使用相同核心技术栈：React 19、TypeScript、Vite 8、Sass、TanStack Router / Query、Zod、pnpm 10.15 和 Biome。Router 负责页面路由，Query 负责请求缓存与变更刷新，Zod 校验 API 响应。Vite 生成 `dist/`，`embed.go` 将产物嵌入 Go 二进制；Docker 在 Node 构建阶段重新构建前端，再编译进 Go 镜像。与现有 HTTP 服务共用端口，不需要单独启动 Node、前端容器或静态文件服务器。

## 启用与部署

1. 配置现有 PostgreSQL 用户体系（`auth.enabled: true`），设置 `MSIME_DATABASE_URL` 与 `MSIME_AUTH_PEPPER`。
2. 在 Google Cloud 项目中创建 Web OAuth 客户端，授权重定向 URI 设为 `https://admin.msime.app/api/auth/google/callback`；只使用 `openid email` 登录范围。将 Client Secret 保存到部署环境的 `MSIME_ADMIN_GOOGLE_SECRET`，将管理员邮箱白名单保存到 `MSIME_ADMIN_GOOGLE_EMAILS`（逗号分隔）。
3. 在原有配置中加入：

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
     }
   }
   ```

4. Google 登录模式不需要设置 `MSIME_ADMIN_TOKEN`。如果保留它，界面会额外提供管理员密钥登录作为兼容入口；不配置 Google 时仍需要至少 32 字节的独立随机管理员密钥。不要将任何密钥放进前端源码、安装包或版本库。
5. 使用新二进制先执行迁移：`./msime-server -config /config/config.json -migrate-users`。镜像中可在正常入口后追加 `-migrate-users`。迁移是幂等的；后台启用但缺少表时服务会拒绝启动。
6. 正常启动镜像；容器中的 `listen` 应为 `0.0.0.0:8080`。配置 `admin.msime.app` 的 DNS 指向入口，并在入口终止 HTTPS，将该域名的请求转发到相同的 Go 端口，保留原始 Host。Go 不信任 `X-Forwarded-Host`。

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

浏览器访问 `https://admin.msime.app`，点击“使用 Google 账号登录”。Google 验证完成后，后端校验 ID Token 的签名、issuer、audience、有效期、nonce，以及 `email_verified` 和邮箱白名单。普通 Google 用户不会因此成为管理员，也不会自动创建普通输入法用户账户。支持配置 `allowed_emails` 数组；配置 `allowed_emails_env` 时以环境变量为准。

授权码流程使用 PKCE S256 和一次性 state；state 与浏览器 HttpOnly Cookie 绑定，并在数据库中保留 10 分钟。管理员会话在 PostgreSQL 中仅存令牌哈希，有效期固定为 8 小时；浏览器使用 Secure、HttpOnly、SameSite=Lax、无 Domain 的 `__Host-` Cookie，页面刷新可恢复登录。每个请求重新校验当前权限：部署白名单指定超级管理员，数据库中启用的 `admin_members` 指定普通管理员。删除部署白名单账号并重启所有副本后，其旧会话不再具有超级管理员权限；若同一邮箱仍有普通管理员记录，则按该记录判定访问权限。退出会删除服务端会话；Cookie 管理写操作还要求 Origin 与配置的回调来源完全相同。过期登录流程和会话按现有每小时清理任务回收。

审计的 `actor` 记录 Google subject 与邮箱，旧数据和管理员密钥操作记为 `legacy-token`。Google Client Secret 和授权令牌不会返回给前端。此处遵循 [Google OpenID Connect 服务端流程](https://developers.google.com/identity/openid-connect/openid-connect)。

所有后台 `/api/*` 均校验管理员权限（仅登录元数据、开始登录、回调与退出接口有各自认证流程），普通用户/设备令牌无权访问。后台域名不承载 `/v1/*` 客户端 API。默认 `admin.enabled: false`，不会改变已有域名路由。

登录端点：

| 端点 | 用途 |
| --- | --- |
| `GET /api/auth/session` | 返回登录状态、管理员邮箱和启用的登录方式 |
| `GET /api/auth/google/start` | 创建 state/nonce/PKCE，跳转到 Google |
| `GET /api/auth/google/callback` | 校验回调并创建管理员会话 |
| `POST /api/auth/logout` | 删除服务端会话并清除 Cookie |

OAuth 客户端只允许配置固定回调。生产反向代理必须保持 Host，不应改写回调路径。Google Cloud 如果仍处于测试发布状态，需要将管理员加入该项目的测试用户；只有基础登录范围通常不涉及敏感 API 访问。真实 Google 回调验证需要新服务已在上述 HTTPS 域名上线。

本地私密配置可放在 `config.admin.local.json` 和 `.env.admin.local`（均被 Git 忽略）。使用前将现有部署的数据库、验证码密钥和客户端认证配置合并进去；Go 不自动加载 `.env`，可由部署工具注入或在本机先 `set -a; . ./.env.admin.local; set +a`。不要把本地凭据文件提交到仓库。

## 功能与统计口径

- 数据总览：累计注册账户、近 30 天注册数、持有有效会话的用户、安装包下载上报、崩溃及待处理数、社区皮肤、共享词库、回复模板和资源收藏。有效会话用户不是 DAU。
- 30 天趋势：UTC 自然日，每天的注册、下载上报和崩溃上报；图表展示下载，展开明细可看全部指标。
- 用户：搜索、分页、撤销全部会话（包括刷新令牌）；不展示邮箱/手机号、私人词典或剪贴板。
- 社区皮肤、词库与模板：查看列表与统计，删除公开内容。删除是永久操作，浏览器会要求确认，并连带删除对应下载/收藏/评分，所以这些社区统计反映当前留存记录。内置文件皮肤和引擎词库仍通过现有构建/配置管理。
- 崩溃：版本、平台、错误及堆栈详情，标记已处理或重新打开。
- 审计：管理变更与审计写入在同一数据库事务中，失败不部分生效。
- 列表：每页 50 条、搜索、匹配总数、前后分页及重置筛选；最大 10000 页。处理或删除后当前页为空会自动回退至有效页。

总览接口支持按需缩短趋势查询范围：`GET /api/overview?days=7` 或 `GET /api/overview?days=30`，只接受这两个值；响应的 `range_days` 与 `daily` 数组反映实际范围。省略参数时默认为 30 天。

`GET /api/system` 为管理员提供只读运行状态，展示后端版本、账号服务、输入法引擎与资源、云候选、对话、翻译及语音能力是否已配置。接口不会返回上游 URL、模型名、环境变量名或密钥；Admin 的「系统状态」页面直接使用该接口。
- 下载与崩溃列表支持 `platform`（最长 32 字节）、`version`（最长 64 字节）精确匹配；崩溃另支持 `status=open|resolved`，留空表示全部。筛选可与关键词 `q` 组合，非法参数或不适用的筛选返回 400。列表响应新增 `total`，与当页内容使用同一数据库快照计算。
- 操作日志支持 `action` 精确筛选和 `actor` 模糊筛选，便于按管理员或操作类型追查变更；这两个筛选只适用于 `audit` 列表。

安装包下载目前以新上报事件为数据来源，不会自动从 CDN、GitHub Release 或应用商店回填历史数据。皮肤下载沿用原有 `(skin_id,user_id)` 去重，资源收藏沿用 `(resource_id,user_id)`；不能与安装包下载混为一个总量。

## 客户端数据接入

向现有 API 域名发送 `POST /v1/telemetry/events`，携带现有设备或用户 Bearer 令牌：

```json
{
  "id": "8ff0faf8-5c26-4a15-bff5-e11c92bac154",
  "kind": "download",
  "platform": "windows",
  "version": "1.0.0"
}
```

崩溃示例：

```json
{
  "id": "f0c84d7e-7ca2-48cd-bcb1-941e07ba9dc5",
  "kind": "crash",
  "platform": "windows",
  "version": "1.0.0",
  "message": "Unhandled exception in keyboard initialization",
  "stack": "Keyboard::Initialize\nApplication::Start"
}
```

- 每个事件生成一个全局唯一 ID（16–128 字符），重试必须复用；同一 ID 只记录第一次，重复也返回 `202 {"accepted":true}`。推荐 UUID，不含用户身份。
- `platform` 为 1–32 字符，`version` 为 1–64 字符。崩溃 `message` 必填，最多 1000 字符，`stack` 最多 16000 字符；总请求体最多 32 KiB。下载事件不接受错误与堆栈字段的非空值。
- 使用服务端接收时间，离线上报算在接收日。客户端须在用户允许采集后发送，先清理输入文本、密码、令牌及个人信息；后台不额外存 IP 或用户身份。
- 接口沿用现有身份认证、速率限制与并发限制。数据保存在 PostgreSQL，多副本共享；当前不自动清理事件，需按实际规模设置归档/保留策略。
- 必须执行新迁移；采集接口不要求开启后台域名，但要求启用用户数据库。完整接口见生成的 OpenAPI。

## 本地开发与验证

```sh
pnpm --dir admin-web install --frozen-lockfile
pnpm --dir admin-web lint
pnpm --dir admin-web build
go test ./...
go build -o /tmp/msime-server ./cmd/msime-server
```

修改 `admin-web/src/` 后执行 `pnpm --dir admin-web build`，再重新编译 Go。生成的 `dist/` 随源码提交，保证直接 `go build` 也可用；CI 会重新构建并核对产物。运行镜像不需要 Node.js。

开发时将 Go 的 `admin.host` 配为 `admin.localhost`、`listen` 配为 `127.0.0.1:18089`，运行 `pnpm --dir admin-web dev`；Vite 将 `/api` 请求代理到该 Go 服务并设置开发 Host/Origin。也可直接访问 `http://admin.localhost:18089` 验证实际嵌入产物。生产必须通过 HTTPS 入口访问。

PostgreSQL 集成测试需设置 `MSIME_TEST_DATABASE_URL`，数据库名称必须含 `msime_auth_test`，仅可使用一次性测试库。测试会清空测试表。

## 管理员账号管理

`/admins` 页面和 `GET/POST /api/admins` 仅允许通过 Google 登录的部署白名单账号访问。白名单中的账号是超级管理员，网页不能添加、停用或撤销这些账号；运维修改配置保留恢复入口。静态管理员密钥和普通管理员不能访问该接口。

超级管理员可以添加 Google 邮箱（统一小写）、停用、重新启用普通管理员或撤销其会话。新增管理员可以执行已有运营和内容管理操作，不能管理管理员。最多保留 100 个普通管理员记录；停用不删除记录，重新启用需重新登录。状态更新与会话撤销、审计写入在同一事务中完成；会话创建锁定管理员行，防止停用与登录同时发生时产生遗漏的有效会话。

接口请求体为 `{"email":"admin@example.com","action":"add|enable|disable|revoke"}`，其中 action 必须是四个值之一。重复添加返回 409；无效邮箱/动作返回 400，非超级管理员或修改受保护账号返回 403。普通管理员记录不创建输入法用户账户，也不发送邀请邮件；被添加者直接使用其 Google 账号登录。

上线前需执行更新后的 `internal/account/admin_schema.sql`，新增 `admin_members` 表，归既有迁移所有者所有，并授予运行角色该表 SELECT/INSERT/UPDATE/DELETE；缺少迁移时后台拒绝启动。无需把 Google 密钥或超级管理员邮箱写入前端。

### 用户详情与单个会话管理

用户列表的「详情」展示注册时间、登录渠道类型、发布内容数量和最近 50 条保留的登录会话（创建时间、到期时间、有效/过期/撤销状态），同时显示有效及总会话数量。清理任务删除的历史会话不计入统计。接口不返回登录标识、访问令牌、刷新令牌或其哈希。

`GET /api/users/{id}` 返回上述详情。`POST /api/actions` 的 `revoke_session` 操作要求同时提供会话 `id` 和所属 `user_id`，只撤销匹配该用户的会话，并在同一事务记录操作者和会话 ID；其他会话不受影响。上述接口沿用后台身份校验、同源限制和限流，不需要新增数据库迁移。

### 社区内容详情

皮肤、词库和回复模板列表均提供「详情」。详情展示名称、描述、发布者、时间、下载或收藏用户数及评分；词库额外展示修订版本和可搜索的完整词条表，回复模板展示完整文本，皮肤优先展示键盘外观预览，设计 JSON 可折叠查看。不执行社区内容中的 HTML 或脚本。

只读接口为 `GET /api/skins/{id}`、`GET /api/dictionaries/{id}` 和 `GET /api/replies/{id}`，沿用后台认证、同源校验与限流。接口只查询社区公开内容，不涉及私人词库或发布者的登录标识。不存在或类型不匹配返回 404。详情页可确认后调用既有删除操作，关联记录级联删除并保留管理员审计。无需新增数据库迁移。

### 工作台与批量操作

- 总览支持近 7 / 30 天的新增用户、下载上报和崩溃上报趋势切换；明细和 CSV 导出遵循当前时间范围，日期采用 UTC。统计卡片可直接进入对应管理列表。
- 所有管理列表可切换紧凑显示、查看更新时间、导出当前页 CSV。导出仅包含当前筛选结果的本页及列表字段，不代表全部记录；CSV 保留 UTF-8 中文、引号与换行，并转义可能被表格软件识别为公式的文本。
- 崩溃列表支持选择当前页记录并批量标记已处理或重新打开。确认后逐条调用已有管理接口，每条操作独立写入审计；部分失败会显示成功/失败数量并保留失败项供重试。切换分页或筛选条件会清空选择，执行期间禁用筛选、翻页和重复操作。
- 导航按数据与用户、社区内容、系统管理分组；手机端通过「导航」展开，选择页面后自动收起。表格独立横向滚动、固定表头，详情窗口可滚动并保留顶部操作区。左上角继续显示实际后端版本。

### 皮肤外观预览

社区皮肤详情顶部支持 26 键和九键示意预览，使用经过类型和范围校验的设计参数渲染 SVG，支持 RGB 配色、渐变、圆角、边框、透明度、阴影、纹理、材质、等宽字体和内嵌 JPEG 背景。图标使用 Lucide，不加载皮肤提供的外部 URL、CSS 或脚本；不需要放宽后台 CSP。未知或非法设计格式显示提示，原始 JSON 仍可展开检查。此预览用于外观检查，字体、纹理和材质细节可能与原生客户端略有差异。
