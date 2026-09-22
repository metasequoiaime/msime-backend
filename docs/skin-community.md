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
