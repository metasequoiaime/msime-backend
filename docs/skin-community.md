# 用户皮肤社区

社区接口位于 `/v1/community/skins`，与既有 `/v1/skins` 桌面 CSS 目录分开。当前设计格式对应 Apple 自定义键盘 v1，描述颜色、键帽、纹理、渐变及可选 JPEG 壁纸，不接受代码、任意资源 URL 或 CSS。其他平台需实现此设计格式后才能使用。

## 接口

- `GET /v1/community/skins?q=&offset=0`：公开目录，每页 20 条，返回 `skins` 和 `has_more`。目录不含壁纸字节。
- `GET /v1/community/skins/{id}`：公开详情，含完整 design；登录时额外返回自己的评分与是否为作者。
- `POST /v1/community/skins`：需要用户会话，提交 `{id,name,description,design}`。id 为客户端生成的 UUID，用于网络失败后的安全重试。每个账号最多 50 款。
- `POST /v1/community/skins/{id}/download`：需要用户会话，返回 `{design}`，每个账号只计一次下载。
- `PUT /v1/community/skins/{id}/rating`：需要用户会话，提交 `{stars:1..5}`。必须已下载，作者不可自评；重复提交更新同一条评分。
- `DELETE /v1/community/skins/{id}`：仅作者可下架；不删除其他设备已下载的本地副本。

摘要字段：id、name、description、author、design、downloads、rating_count、rating_average、owned、my_rating。人数代表累计去重下载账号数，不代表实时活跃使用人数。发布之后不允许原地替换设计以继承旧版评分；修改设计需发布新作品。

标题最多 32 个 Unicode 字符，说明最多 280 个；设计颜色为 24-bit RGB、数值范围与 iOS 编辑器一致。JSON 请求最大 710,000 字节，壁纸最多 512,000 字节且长宽均不超过 1024。服务器解码后重编码 JPEG，移除原图元数据。未知字段或错误图片会被拒绝。

## 账号与 K8s

复用现有 PostgreSQL 用户体系。配置 `auth.apple.client_ids: ["app.msime.ios"]`；Apple Developer 中为相同 App ID 启用 Sign in with Apple，并重新生成包含该 entitlement 的签名配置。客户端不包含设备共享令牌或 Apple 私钥。Apple nonce 来自后端挑战，ID Token 校验继续使用现有 issuer/audience/signature/nonce 校验。

运行角色有 DDL 权限时不需要单独迁移：新版本启动发现缺表会自己补上。按最小权限部署（运行角色只有 DML）时仍照旧：上线前使用迁移账号执行新版本的 `-migrate-users`，或者由数据库管理员在事务中执行 `internal/account/community_schema.sql`，并给运行角色授予三张新表的 SELECT/INSERT/UPDATE/DELETE。这些表放在现有 PostgreSQL 中，无需 K8s 本地目录或 PVC。先迁移再滚动更新，旧二进制可兼容新增表。数据库需按现有方案备份。

下载及评分的唯一键保证多副本并发去重；发布锁定账号行保证配额。浏览和写入继续使用数据库限流。没有给下载用户数设置产品上限；实际吞吐需按部署容量测试，不能把配额或副本数解释为可承载人数。

Apple 客户端入口：皮肤 → 皮肤社区。用户显式确认公开素材后发布，浏览不会修改当前皮肤。账号注销级联删除作品、评分和下载记录。登录令牌保存到本机 Keychain，刷新串行执行。

## 初始精选皮肤

`assets/community-starter-skins.json` 包含 8 款项目自有的纯参数设计，供社区冷启动。`scripts/community_seed.py` 只生成 SQL，不连接数据库：

```sh
python3 scripts/community_seed.py > /tmp/community-starter.sql
psql -X -v ON_ERROR_STOP=1 "$MSIME_MIGRATION_DATABASE_URL" -f /tmp/community-starter.sql
```

SQL 在单个事务中切换到既有 DML 运行角色 `msime_backend`，使用固定 ID 和事务锁保证重复执行安全；已有 ID 的内容不一致时整个事务失败，不覆盖用户作品或已获评分的设计。执行前审核目录和 SQL，并确认目标数据库。

作者「水杉精选」是专门标记项目精选内容的非交互发布主体，不是虚构的普通用户。只创建作者记录，不创建登录身份、密码、令牌或会话；如其 ID 已绑定登录身份或会话则拒绝执行。脚本只写作者和皮肤，不创建下载或评分。后续改版应新增版本 ID，避免继承旧版评分。

### AI 插画抽卡任务

客户端使用 `POST /v1/skins/jobs` 提交内部生成的原创场景描述，收到 202 后每 5 秒调用 `GET /v1/skins/jobs/{job}`。`running` 表示仍在绘制，`succeeded` 的 `artwork` 包含与同步接口相同的有界 PNG/JPEG 数据，`failed` 表示此次生成失败。无需让一个公开 HTTP 请求等待完整生图时间。

每个认证主体最多保留 3 个任务，全局最多 `min(8, max_concurrent)` 个；领取、取消或失败后调用 `DELETE /v1/skins/jobs/{job}` 释放任务。任务绑定认证主体，令牌刷新不改变归属，其他主体查询和删除都返回 404。上游请求最多运行 180 秒，服务关闭会取消并等待任务。提交请求不应自动重试，以免重复生成。

这些是当前单实例服务中的临时草稿，10 分钟后失效，重启也会失效；不写入社区或用户皮肤库。客户端应提示重新抽取，只有用户保存后才成为持久化皮肤。扩展到多实例前需要将任务存储和执行迁移到共享任务服务。

## 候选框皮肤包入库

候选框皮肤包（msime-skins 格式，`skin.toml` 加图片等资源）直接存入 PostgreSQL，通过既有 `/v1/skins` 目录下发，客户端不必固定某个 msime-skins 提交。格式、校验和冲突规则见 `internal/skins/README.md`。

部署：运行角色有 DDL 权限时新版本启动会自动建表。按最小权限部署时，上线前用迁移账号执行新版本的 `-migrate-users`，或在事务中执行 `internal/account/candidate_skin_schema.sql`（需要 PostgreSQL 12 及以上，用到生成列），并给运行角色授予两张新表的权限：

```sql
GRANT SELECT, INSERT, UPDATE, DELETE ON candidate_skins, candidate_skin_resources TO msime_backend;
```

服务只需要 SELECT；INSERT 供下面的种子 SQL 以运行角色写入，UPDATE/DELETE 供运维下架（`UPDATE candidate_skins SET published = false, updated_at = now() WHERE id = '…'`）或删除（级联删除资源）。先迁移再滚动更新，旧二进制不读这两张表。

`scripts/candidate_skins_seed.py` 只生成 SQL，不连接数据库：

```sh
python3 scripts/candidate_skins_seed.py ~/src/msime-skins > /tmp/candidate-skins.sql
python3 scripts/candidate_skins_seed.py ~/src/msime-skins --only bigfish,qq-blue > /tmp/candidate-skins.sql
psql -X -v ON_ERROR_STOP=1 "$MSIME_MIGRATION_DATABASE_URL" -f /tmp/candidate-skins.sql
```

脚本按客户端规则校验每个包，客户端会拒绝的清单直接拒绝（不改写），并在 stderr 说明原因；任何一个包被拒绝时不输出 SQL。`license.assets` 含 `UNVERIFIED` 的包（如 `niya-demo`）默认跳过，用 `--only` 点名时拒绝。非皮肤资源（README 等）不入库，会在 stderr 列出；符号链接、超过 4 MiB 的文件、超过 16 MiB 或 512 个条目的包直接拒绝。SQL 注释记录来源仓库（去掉凭据）和提交，工作区有未提交改动时给出警告。

SQL 在单个事务中切换到运行角色 `msime_backend`（`--role` 可改），加事务锁，只插入缺失的行；同一 ID 已存在且清单或资源集合（路径与 SHA-256）不一致时整个事务失败，不覆盖已发布的包。内容相同时重复执行不改变任何行。改版应发布新 ID，或由运维先显式下架、删除旧包。执行前审核 SQL 并确认目标数据库。

msime-skins 清单写 `base = "fluent"`（msime-windows 只接受四个内置 ID）。客户端把清单里的 `fluent` 当作 `system` 的别名，脚本和服务端同样接受；数据库保存原始清单字节，`/v1/skins` 返回的 `base` 为 `system`。其余 Windows 内置 ID 作为 base 仍被拒绝。

## 候选窗皮肤包分享

用户可以把自己的候选窗皮肤包（msime-skins 格式，`skin.toml` 加 PNG/JPEG 图片）公开发布到社区，其他用户下载后安装到外部皮肤目录。接口位于 `/v1/community/candidate-skins`，数据存放在 `community_candidate_skins`、`community_candidate_skin_files`、`community_candidate_skin_downloads` 和 `community_candidate_skin_ratings` 四张独立的表中，与上面的精选 `candidate_skins` 表和 `/v1/skins` 目录互不影响：用户上传不会进入 `/v1/skins`。

### 接口

- `GET /v1/community/candidate-skins?q=&offset=0&scope=`：公开目录，按发布时间倒序每页 20 条，返回 `skins` 和 `has_more`，不含清单和图片字节。`scope=mine` 只列出自己的作品，需要用户会话；其他 scope 返回 400 `invalid_scope`。
- `GET /v1/community/candidate-skins/{id}`：公开详情；登录时额外返回自己的评分与是否为作者。
- `GET /v1/community/candidate-skins/{id}/preview`：公开返回预览图 `{path,content_type,data}`，data 为标准 base64，content_type 为 `image/png` 或 `image/jpeg`。
- `POST /v1/community/candidate-skins`：需要用户会话，提交 `{id,name,description,manifest,files}`。manifest 为原样的 skin.toml 文本，files 的键为包内相对路径、值为标准 base64。首次发布返回 201，同一请求重试返回 200。
- `POST /v1/community/candidate-skins/{id}/download`：需要用户会话，返回 `{id,package_id,manifest,files}`，每个账号只计一次下载。
- `PUT /v1/community/candidate-skins/{id}/rating`：需要用户会话，提交 `{stars:1..5}`。必须已下载，作者不可自评；重复提交更新同一条评分。
- `DELETE /v1/community/candidate-skins/{id}`：仅作者可下架，连带删除图片、下载和评分记录；不删除其他设备已安装的副本。

摘要字段：id、package_id、name、description、author、version、license（code、assets、source，缺省为空字符串）、size（重新编码后的图片总字节数）、file_count（图片数量，不含 skin.toml）、downloads、rating_count、rating_average、owned、my_rating、created_at。

### 限制与校验

- 只接受 `skin.toml` 加 PNG/JPEG 图片（扩展名 png、jpg、jpeg，不区分大小写），最多 3 个图片文件，每个文件都必须被清单引用：只能是 `preview`、`candidate_window.decoration.image` 和 `candidate_window.background.image`。不接受样式表（`toolbar_stylesheet`）、字体、SVG、GIF 或 WebP；Go 标准库不能重新编码 WebP，支持它需要新增依赖。
- 单个图片不超过 1 MiB、合计不超过 2 MiB，上传时和重新编码后都要满足；skin.toml 另计，最多 65,536 字节。每边 1 到 2048 像素，整包解码像素合计不超过 800 万，尺寸在完整解码前从文件头读取。每张 JPEG 最多 32 个扫描段（SOS）：Go 的解码器对每个扫描都要遍历整张图，且不限制扫描数，常见渐进式 JPEG 约 10 个扫描；超过的返回 `candidate_skin_image_invalid`。
- 必须用 `preview` 指定一张包内的 PNG/JPEG 作为预览图，重新编码后不超过 256 KiB。
- `skin.toml` 必须包含 `[license]` 且 `assets` 非空。客户端发布时还要求用户勾选确认拥有素材权利。清单的 `version` 和 `[license]` 的 `code`、`assets`、`source` 不能含控制字符（包括换行和制表符），否则返回 `invalid_candidate_skin_package`：客户端会拒绝含控制字符的列表项，一条这样的记录会让所在的整页列表失败。
- 路径规则与客户端 `safe_resource` 一致：相对路径，每段只含 `A-Za-z0-9._-`，不允许空段、`.`、`..` 或反斜杠；键不能是 `skin.toml`，转小写后不能重复。
- 清单用与客户端加载器一致的规则校验（`internal/skins/client.go` 的 `ParseStored`），包括 ID 格式、保留主题 ID 与四个内置 ID、schema_version、base、supports 和窗口参数。
- 服务器按扩展名选择解码器解码每张图片后重新编码（PNG 最高压缩、JPEG 质量 90），因此会去除 EXIF、XMP、ICC 和文本块，APNG 只保留第一帧；扩展名与内容不符的图片被拒绝。去掉 ICC 可能带来轻微色差，Go 的 PNG 编码器也可能让已优化的 PNG 变大，接近上限的包可能在重新编码后被拒绝。
- 标题最多 32 个 Unicode 字符，说明最多 280 个，均先去除首尾空白；与清单里的 `name` 无关。标题不能含控制字符，说明只允许换行和制表符两种控制字符。发布请求最多 3,200,000 字节。
- 每个账号最多 20 款，每小时最多发布 10 次（按账号计，数据库限流）；图片解码每个进程最多同时 2 个，繁忙时返回 503 `candidate_skin_busy`。

错误码：400 `invalid_json`、`invalid_skin_metadata`、`invalid_community_id`、`invalid_candidate_skin_package`、`candidate_skin_file_type`、`candidate_skin_file_path`、`candidate_skin_too_large`、`candidate_skin_image_invalid`、`candidate_skin_license_required`、`candidate_skin_preview_required`；409 `candidate_skin_id_conflict`、`candidate_skin_publish_limit`；429 `rate_limit_exceeded`；503 `candidate_skin_busy`、`auth_unavailable`。

### ID 与版本

`id` 是客户端生成的发布 UUID，用于网络失败后的安全重试：同一账号用相同内容重试返回原作品，内容不同或属于其他账号返回 409。已提交作品的重试在每小时发布限流之前就会返回，不计入次数，因此丢失响应后的重试不会变成 429。两个账号同时用同一 UUID 发布时，后提交的一方同样得到 409。`package_id` 是清单里的 `id`，也是客户端安装的目录名。服务端不改写 skin.toml。不同作者可以发布相同的 `package_id`，安装时会替换本机同名皮肤，客户端会先请求确认。作品发布后不能原地修改，更新需要用新的 UUID 发布新作品，评分不继承。

### 部署与审核

运行角色有 DDL 权限时新版本启动会自动建表。按最小权限部署时，上线前用迁移账号执行新版本的 `-migrate-users`，或在事务中执行 `internal/account/community_candidate_skin_schema.sql`（需要 PostgreSQL 12 及以上，用到生成列），并给运行角色授予四张新表的权限：

```sql
GRANT SELECT, INSERT, UPDATE, DELETE ON community_candidate_skins, community_candidate_skin_files, community_candidate_skin_downloads, community_candidate_skin_ratings TO msime_backend;
```

先迁移再滚动更新，旧二进制不读这四张表。每个账号满额时约占 40 MiB bytea，需计入数据库容量与备份。账号注销级联删除作品、图片、评分和下载记录。

发布即公开，不做事前审核。管理后台 API 提供 `GET /api/candidate-skins`、`GET /api/candidate-skins/{id}`（元数据、清单文本和每个文件的路径、大小、SHA-256，不含图片字节）和审计过的 `delete_candidate_skin` 操作用于事后下架；管理后台网页暂未提供对应页面。
