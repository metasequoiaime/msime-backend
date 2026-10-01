# 插件社区

社区插件接口位于 `/v1/community/plugins`，分发客户端插件包：按键音（`sound`）、背景音乐（`music`）、命令表（`command_table`）和打字特效参数（`effect`），与客户端的 `PluginKind` 一致。插件包是一个 zip，内含 `plugin.toml`、被引用的音频和可选的说明文本（`effect` 包没有音频）。服务器只校验、存储和原样分发字节，从不解码音频、执行脚本或加载包内任何内容；客户端安装前仍按自己的规则再校验一次。

## 接口

- `GET /v1/community/plugins?q=&kind=&offset=0&scope=&fields=`：公开目录，按发布时间倒序每页 20 条，返回 `plugins` 和 `has_more`。`kind` 为空表示全部，否则只能是上述几种之一；`q` 按名称不区分大小写子串匹配，最长 128 字节；`offset` 为 0 到 100000。`scope=mine` 只列出自己的作品（含被审核员下架的）；`scope=saved` 只列出自己收藏的作品，按收藏时间倒序，分页方式相同，别人已下架的作品不再列出。两者都需要用户会话，否则 401 `user_session_required`；其他 scope 返回 400 `invalid_scope`。目录不含 zip 字节和清单。
- `GET /v1/community/plugins/{id}?fields=`：公开详情；登录时额外返回 `owned`（是否为作者）和 `my_rating`（自己的评分，未评为 0）。不存在返回 404 `plugin_not_found`。
- `POST /v1/community/plugins`：需要用户会话，提交 `{id,name,description,kind,plugin_id,version,archive}`，成功返回 201 和摘要。`archive` 为 zip 的标准 base64。`id` 为客户端生成的 UUID，用于网络失败后的安全重试：同一账号用完全相同的内容重试返回 200 和已存记录，任何字段不同或他人占用同一 id 返回 409 `plugin_id_conflict`。
- `POST /v1/community/plugins/{id}/download`：需要用户会话，返回 `{id,kind,plugin_id,version,size,sha256,archive}`，`archive` 为原样 zip 的标准 base64。客户端安装前应核对 `sha256`。下载人数按账号去重；重复下载不计人数，但计入下面的下载频率限制。
- `PUT /v1/community/plugins/{id}/rating`：需要用户会话，提交 `{stars:1..5}`，返回 `{stars}`。登录即可评分，不需要先下载；不能给自己的作品评分（403 `download_before_rating_or_own_plugin`，错误码沿用旧名，现在只表示「自己的作品」）；不存在或已下架的插件返回 404 `plugin_not_found`。重复提交更新同一条评分。
- `PUT /v1/community/plugins/{id}/save`：需要用户会话，提交 `{"saved":true|false}` 收藏或取消收藏，重复提交结果相同，返回 200 `{"saved":bool,"saves":int}`（saves 为收藏总数）。不存在或已下架（作者本人除外）的插件收藏时返回 404 `plugin_not_found`；取消收藏不看作品状态，总是删除并返回 200，作品下架后用户仍能把它移出收藏。
- `DELETE /v1/community/plugins/{id}`：仅作者可删除，返回 `{deleted:true}`；不是作者或不存在都返回 404。连带删除下载、评分和收藏记录，不影响其他设备已安装的本地副本。

摘要字段：`id`、`kind`、`plugin_id`、`name`、`description`、`author`、`version`、`license`、`size`、`sha256`、`downloads`、`rating_count`、`rating_average`、`owned`、`my_rating`、`created_at`，以及仅在 `fields=moderation` 时出现在自己作品上的 `moderation`、仅在 `fields=saved` 时出现的 `saved`（当前用户是否收藏，匿名为 false）和 `saves`（收藏总数）。`fields` 是逗号分隔的列表，可以组合，例如 `fields=moderation,saved`；不带 `saved` 时响应与加入收藏之前逐字节相同。

作者查看自己作品的审核状态：列表和详情带 `fields=moderation` 时，当前用户自己的作品多一个 `moderation` 字段（`approved`、`pending` 或 `removed`）；他人的作品和匿名访问永远不带，也不返回下架原因。不带这个参数时响应与以前逐字节相同，因为已发布的客户端按拒绝未知字段的方式解析。社区是事后审核，`pending` 的作品已经公开，客户端只需对 `removed` 显示「已下架」徽标。其他 `fields` 值返回 400 `invalid_fields`。`name`/`description` 是社区列表展示用的标题和说明；`plugin_id`、`version`、`license` 来自包内清单。发布后不允许原地替换包以继承旧版评分，新版本需以新 UUID 发布。

## 发布限制

请求体最多 11,300,000 字节（8 MiB zip 的 base64 加元数据），必须是 `application/json`，未知字段拒绝。标题 1 到 32 个 Unicode 字符、不含控制字符；说明最多 280 个，只允许换行和制表符两种控制字符。`plugin_id` 遵循客户端规则：1 到 64 个小写字母、数字、`.`、`-`、`_`，以字母或数字开头；`version` 1 到 32 字节。

每个账号最多 20 个插件，包体合计不超过 32 MiB；配额在锁定账号行后检查，并发发布不会越过上限。每账号每小时最多 10 次发布尝试，计数发生在解压校验之前，所以被拒绝的包也计入；安全重试在计数前就返回，不会因此变成 429。同一账号的发布逐个处理，每个副本最多同时处理 4 个发布，名额在读取请求体之前占用、直到响应返回才释放，所以并发的大请求体不会堆积在内存里；排队超过路由超时返回 503 `plugin_busy`。每个账号每小时最多 60 次下载尝试；每个副本最多同时发送 8 个包，名额在从数据库读取包体之前占用、直到响应写完才释放，排队超时同样返回 503 `plugin_busy`。下载响应的 `archive` 边编码边写出，不在内存里另存一份 base64。发布和下载的路由超时为 90 秒。

### zip 校验

- zip 不超过 8 MiB、最多 64 个成员，去掉 `__MACOSX` 和以 `.` 开头的隐藏成员后最多 16 个文件。
- 成员路径不能为空、绝对路径、含反斜杠、冒号、控制字符或 `.`/`..` 段；拒绝符号链接、其他特殊文件类型和加密成员。隐藏成员同样检查这些规则并计入成员数。
- 文件放在顶层或单个外层文件夹中（由第一个文件决定），不允许更深的子目录；文件名需满足客户端规则，忽略大小写后不能重名。
- 只接受 Store 和 Deflate。按声明大小限制：单个文件不超过 16 MiB，合计不超过 24 MiB，且合计不超过 1 MiB 加包体的 100 倍（防 zip 炸弹）。每个文件通过按声明大小截断的读取器解压一次，实际长度或 CRC 与声明不符即拒绝。
- 按扩展名和文件头拒绝嵌套压缩包（zip、gzip、bzip2、xz、7z、rar、zstd、cab、tar）。

### plugin.toml 校验

清单最大 256 KiB，必须是 UTF-8，用 go-toml 严格解析：重复键、类型不符（例如浮点写进整数）均拒绝；键名逐字比较，`Kind` 这类大小写不同的键视为未知键。

- 公共键：`schema_version`（必须为 1）、`kind`、`id`、`name`（≤ 80 字节）、`version`（≤ 32）、`license`（≤ 64，只含 SPDX 表达式字符）、可选 `author`（≤ 120）、`description`（≤ 500）、`permissions`（必须为空数组或省略）。必填文本不能为空白，任何文本不能含控制字符。
- `kind` 必须与请求的 `kind` 一致（否则 400 `plugin_kind_mismatch`），`id` 和 `version` 必须与请求的 `plugin_id`、`version` 一致（否则 400 `plugin_manifest_mismatch`）。`id` 不能占用客户端内置的同类插件 id。
- `sound`：`mode = "keys"`（默认）需要 `[sounds]` 且含 `default`，其余可选键为 `space`、`enter`、`backspace`、`commit`、`achievement`；`mode = "sequence"` 需要 `[sequence]`，`sample` 加 1 到 128 个 -24..24 的 `semitones`，可选 `advance = "key" | "commit"`。最多 8 个不同样本，单个不超过 512 KiB，合计不超过 4 MiB。
- `music`：`[music]` 的 `tracks` 为 1 到 8 个不重复文件，单个不超过 16 MiB。
- `command_table`：1 到 256 个 `[[commands]]`，每行 `trigger`（1 到 32 个小写字母，不重复）、`title`（≤ 48 字节）、`template`（≤ 199 个 UTF-16 单元）。模板的花括号必须成对且不嵌套，占位符只能是 `{date}`、`{time}`、`{weekday}`、`{date:FMT}`、`{time:FMT}`。FMT 按客户端所用 time crate 的 strftime 规则解析，不认识的说明符（如 `%Q`、`%E`、`%O`、`%Z`）以及需要时区偏移的 `%s`、`%z` 均拒绝。模板在 2026-09-30 和 2026-12-30 的 23:59:59 各展开一次（`{weekday}` 为“星期三”，月份和星期用英文名），展开结果不能含控制字符（`%n`、`%t` 会产生换行和制表符），也不能超过 199 个 UTF-16 单元。
- `effect`：只有 `[effect]` 表，键限 `style`（必填，`flash`、`sparks` 或 `power_mode`）、`intensity`（0 到 100 的整数）、`colors`（1 到 4 个 `#RRGGBB`，不接受缩写、透明度或颜色名）、`duration_ms`（60 到 1500 的整数）和 `particles`（0 到 64 的整数），后四项可省略。特效由各端内置绘制，包只选择样式并在这些范围内调参，所以不能带任何音频或其他文件，只能附 `.txt`/`.md` 说明。
- 引用的音频必须存在、非空、扩展名为 `.wav` 或 `.ogg` 且文件头分别为 `RIFF....WAVE` 或 `OggS`；`sound` 的采样只能是 `.wav`（各端播放前整段解码，只有 WAV 能事先核实时长，鸿蒙端会静音 Ogg 采样），`.ogg` 只用于 `music`；其余文件只能是 `plugin.toml` 或不超过 64 KiB 的 `.txt`/`.md` 说明。

zip 层面的错误返回 400 `invalid_plugin_archive`，清单与文件规则不符返回 400 `invalid_plugin_manifest`，任何大小上限返回 400 `plugin_too_large`。

## 错误码

| 状态 | code | 场景 |
| --- | --- | --- |
| 400 | `invalid_json` | 请求体超限、JSON 损坏或含未知字段 |
| 400 | `invalid_community_id` | id 不是 UUID |
| 400 | `invalid_plugin_metadata` | 标题、说明、`plugin_id` 或 `version` 不合规 |
| 400 | `invalid_kind` | `kind` 不在白名单（发布与列表） |
| 400 | `invalid_offset` / `invalid_search` | 列表参数不合规 |
| 400 | `plugin_too_large` / `invalid_plugin_archive` / `invalid_plugin_manifest` | 包校验失败 |
| 400 | `plugin_kind_mismatch` / `plugin_manifest_mismatch` | 清单与请求不一致 |
| 400 | `invalid_rating` | 评分不在 1..5 |
| 401 | `user_session_required` | 缺少用户会话 |
| 403 | `download_before_rating_or_own_plugin` | 给自己的作品评分 |
| 400 | `invalid_scope` / `invalid_fields` | 列表的 `scope` 或 `fields` 不合规 |
| 404 | `plugin_not_found` | 不存在、已下架（评分、收藏），或非作者删除 |
| 409 | `plugin_id_conflict` / `plugin_publish_limit` / `plugin_storage_limit` | UUID 冲突或配额已满 |
| 415 | `json_required` | Content-Type 不是 application/json |
| 422 | `blocked_content` | 标题、说明或清单命中拦截级敏感词，未保存 |
| 429 | `rate_limit_exceeded` | 超过每小时发布或下载次数，或每 IP 每分钟 120 次 |
| 503 | `plugin_busy` / `auth_unavailable` | 发布或下载排队超时，或数据库不可用 |
| 503 | `screening_unavailable` | 敏感词库暂时无法加载，未保存；带 `Retry-After`，稍后重试 |

## 存储与部署

四张表定义在 `internal/account/community_plugin_schema.sql`：`community_plugins`（zip 原样存为 bytea，`size` 和 `sha256` 是生成列，不会与下发字节不一致）、`community_plugin_downloads`、`community_plugin_ratings` 与 `community_plugin_saves`（主键均为 `(pack_id,user_id)`，保证多副本并发去重；收藏表的 `created_at` 是收藏时间，`scope=saved` 用索引 `community_plugin_saves_user (user_id, created_at DESC)` 按它倒序列出）。外键都对插件和账号级联删除，注销账号会移除其插件、下载、评分和收藏。

运行角色有 DDL 权限时新版本启动会自动建表。按最小权限部署时，上线前用迁移账号执行新版本的 `-migrate-users`，或在事务中执行上述 SQL（需要 PostgreSQL 12 及以上，用到生成列），并授予运行角色权限：

```sql
GRANT SELECT, INSERT, UPDATE, DELETE ON community_plugins, community_plugin_downloads, community_plugin_ratings, community_plugin_saves TO msime_backend;
```

先迁移再滚动更新，旧二进制不读这些表。加入收藏的版本只新增 `community_plugin_saves`，启动探测会检查它，缺表时自动执行迁移；已有部署只需额外授予这一张表。单个插件最多 8 MiB、每账号最多 32 MiB，数据库容量和备份需按预期发布量规划。

## 管理后台

`GET /api/plugins` 列出插件（id、kind、plugin_id、名称、版本、发布者、owner_id、大小、SHA-256、下载人数、发布时间），支持关键词搜索和分页。`GET /api/plugins/{id}` 返回元数据、清单文本、大小、SHA-256、下载与评分统计，不返回 zip 字节。`POST /api/actions` 的 `delete_plugin` 永久删除插件及其下载和评分并写入审计。总览新增 `plugins` 和 `plugin_downloads` 两项计数。管理后台网页暂未提供对应页面，需直接调用 API。
