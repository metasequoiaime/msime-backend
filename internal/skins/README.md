# 皮肤目录 API

接口需要设备或用户 access token，生产 Swagger 仍默认关闭。

- `GET /v1/skins?layout=horizontal&theme=dark`：查询兼容皮肤；两个筛选条件均可省略。
- `GET /v1/skins/{id}`：读取 `skin.toml` 对应元数据、资源路径、大小和 SHA-256。
- `GET /v1/skins/{id}/resources/{resource}`：下载资源，`resource` 可包含皮肤内子目录。
- `GET /v1/skins/source`：内置皮肤来源提交及文件摘要。
- `GET /v1/skins/license`：内置皮肤完整许可证。

四个内置 ID 是 `fluent`、`wechat`、`graphite`、`willow_green`。CSS 按 `source.json` 固定到 Windows 已合并提交，保持原始字节。自定义包不能覆盖这些 ID。

管理员可在后端配置设置绝对路径 `skins_root`，例如 `/data/skins`，并只读挂载此目录。每个子目录是独立皮肤包，目录名必须等于 `skin.toml` 的 `id`，遵循 Windows `schema_version = 1` 格式。

```toml
schema_version = 1
id = "example-skin"
name = "示例皮肤"
version = "1.0.0"
base = "fluent"
preview = "images/preview.png"
toolbar_stylesheet = "toolbar.css"

[supports]
layouts = ["horizontal", "vertical"]
themes = ["dark", "light"]

[candidate_window]
min_width_dip = 240

[candidate_window.decoration]
top_inset_dip = 0
width_dip = 0
```

声明的预览与工具栏文件必须存在。每个资源最多 4 MiB，包资源合计最多 16 MiB，扫描最多 512 个目录条目。只提供皮肤清单中的 CSS、图片、字体和 `skin.toml`，不提供任意文件下载；资源路径不能越出该包。无效包不进入列表，`invalid_packages` 给出数量，响应不包含服务器本机路径。

下载接口提供原始资源，不在服务端渲染页面。客户端保留 Windows 已有的 CSS URL 隔离和资源嵌入规则，并可按清单摘要验证下载内容。发布自定义包时宜采用完整目录切换，避免客户端下载期间文件发生变化。

## 数据库皮肤包

启用用户体系（`auth.enabled`）后，目录还会并入 PostgreSQL 中 `published = true` 的候选框皮肤包，格式与 [msime-skins](https://github.com/metasequoiaime/msime-skins) 相同：`candidate_skins` 保存原始 `skin.toml` 字节，`candidate_skin_resources` 保存其余文件字节，大小与 SHA-256 由数据库生成列计算。表结构见 `internal/account/candidate_skin_schema.sql`，随 `-migrate-users` 和启动自动迁移一起建立。写入只通过 `scripts/candidate_skins_seed.py` 生成的 SQL，接口本身只读。

两种方言分开校验，互不影响：

- 内置皮肤和 `skins_root` 仍按 Windows 方言（上文，`base` 为 `fluent` 等四个内置 ID）校验，规则和响应都不变。
- 数据库皮肤包按跨平台客户端的加载规则校验（`crates/client-core/src/skin/catalog.rs` 的 `load()`，移植在 `client.go`），每次读取都重新校验：`base` 必须是 `system`、`shuishan`、`light`、`paper`、`night`、`ink` 之一（客户端已废弃 `fluent`）；ID 不能是全局主题 ID（再加上 `custom`）或四个内置 ID；支持 `corner_radius_dip`、`decoration.image` / `align`、`[candidate_window.background]`、`candidate.*.translation`、`[toolbar]` 与 `[license]`；允许的资源类型与客户端一致（CSS、png/jpeg/gif/webp/svg/ico/bmp/avif、woff/woff2/ttf/otf）。`testdata/client_dialect.json` 同时约束 Go 校验和种子脚本，改规则时两边一起改。

响应结构与现有包相同，新增字段只出现在数据库皮肤包里：`candidate_window.corner_radius_dip`、`candidate_window.decoration.image` / `align`（默认 `right`，数据库包总会给出 `decoration`，无装饰时为 0/0）、`candidate_window.background`（补齐默认 `fit = cover`、`opacity = 1`）、`candidate.{dark,light}.translation`、`toolbar`、`license`。颜色按清单原样返回，由客户端规范化。资源列表首项是 `skin.toml`，其余按路径排序。

冲突与故障：数据库 ID 不能与内置 ID 相同（表约束和校验两道）。同一 ID 同时出现在 `skins_root` 与数据库时两边都不提供，`invalid_packages` 计 1，详情和资源返回 404，避免静默选中其中一份；需要切换来源时删掉其中一份。数据库暂时不可用时目录、非内置皮肤详情和资源返回 503，而不是悄悄只列出文件系统皮肤；内置皮肤不查询数据库。数据库包最多 256 个，与 `skins_root` 相同，单资源 4 MiB、单包 16 MiB、512 个条目（含 `skin.toml`）的限制也相同。比客户端更严的一点：`preview` 等引用必须指向已存储的文件，数据库不保存 README 这类非皮肤资源。
