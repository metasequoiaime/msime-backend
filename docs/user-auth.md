# 用户体系

用户体系使用 PostgreSQL，支持 Apple、Google、微信网站扫码登录，以及阿里云短信、Lark SMTP 邮箱验证码登录。未配置的提供方保持关闭，`GET /v1/auth/providers` 返回实际状态。已有设备令牌可继续调用在线输入接口，但不能访问用户资料。

## 配置与迁移

参考 `config.example.json` 的 `auth` 配置，设置 `enabled: true`，通过环境变量提供数据库 URL 和至少 32 字节随机 `MSIME_AUTH_PEPPER`。密钥应存于 Vault，不提交到源码。

服务启动时会检查表是否齐全，缺表就自己补迁移，所以运行账号有 DDL 权限时不需要任何手工步骤，新增表的版本直接滚动更新即可。

生产上如果按下面的最小权限方案部署，运行账号没有 DDL 权限，自动迁移会失败并在启动日志里要求手工迁移。这种部署仍然按原来的方式做，用有 DDL 权限的账号先执行：

```sh
msime-server -config /config/config.json -migrate-users
```

迁移使用事务和 PostgreSQL advisory lock，可重复运行，多副本同时启动也会串行执行、后到的跑成空操作。生产可由运维迁移，再给运行账号授予本数据库的 CONNECT、public schema USAGE 和六张 `auth_*` 表的 SELECT/INSERT/UPDATE/DELETE；运行账号不需要超级用户、建库或建角色权限。连接生产 PostgreSQL 应启用 TLS；使用私有 CA 时挂载 CA 并设置 `sslmode=verify-full&sslrootcert=...`。

数据库保存用户、身份、验证码摘要、会话摘要和限流计数；不保存明文验证码或会话令牌，不按同名邮箱自动合并第三方身份。服务每小时清理过期挑战、会话和限流计数。数据库需要纳入备份；本服务不提供数据备份功能。

## 提供方

- Apple / Google：`client_ids` 配置本应用注册的 Client ID。客户端先请求挑战，将返回的 `nonce` 原样传入官方登录 SDK，再把 ID Token 提交给后端。后端校验签名、发行方、受众、有效期和 nonce。不得使用其他应用的 Client ID。
- 微信：配置本应用 `app_id`、密钥环境变量及已登记的 HTTPS `redirect_uri`。这是网站扫码登录，不是小程序或移动应用 SDK 登录。前端打开 `authorization_url`，在回调验证 `state == challenge_id`，再提交 code。回调页面由客户端项目提供。
- 阿里云短信：配置 region、AccessKey 环境变量、审核通过的签名及短信模板。模板参数为 `code`，六位数字、五分钟有效。手机号使用 `+8613800138000` 这样的 E.164 格式。国际短信还需相应发送资质与模板。
- Lark 邮箱：配置实际 SMTP 主机、邮箱账号、发件地址和应用密码。支持 465 隐式 TLS 或 587 STARTTLS，强制证书验证。示例主机需按邮箱所在区域确认；不会自动发送测试邮件。

## 登录与账号管理

1. `POST /v1/auth/challenges`：`{"provider":"email","target":"user@example.com","purpose":"login"}`。返回 `challenge_id`，不会返回验证码。第三方登录省略 target。
2. `POST /v1/auth/login`：`{"challenge_id":"...","credential":"..."}`。credential 为验证码、ID Token 或微信 code。首次有效登录自动创建账号，返回 access_token、refresh_token 和 user。
3. 使用 `Authorization: Bearer <access_token>` 调用在线输入接口及 `GET /v1/users/me`。访问令牌有效期 15 分钟，会话最长 30 天。
4. `POST /v1/auth/refresh`：提交 refresh_token，原访问令牌和刷新令牌立即失效。客户端必须串行刷新；重放旧刷新令牌会撤销该会话，需重新登录。令牌应存于操作系统安全存储，不放入 URL。
5. `POST /v1/auth/logout`：`{"all":false}` 退出当前会话，true 退出此用户所有会话。
6. `PATCH /v1/users/me`：`{"display_name":"昵称"}`，最长 64 字符。
7. 绑定其他身份：创建挑战时使用 `purpose: link`，创建和验证均携带同一用户的访问令牌。绑定与 `DELETE /v1/users/me` 注销操作均要求最近 10 分钟内重新登录。注销删除用户、身份、会话及关联挑战。

验证码最多尝试五次，每个目标每分钟一次、每小时五次、每天十次，全服务每天最多发送 500 次。邮箱地址统一转为小写。用户接口按 TCP 对端每分钟最多 120 次，不信任转发头；部署在反向代理后，同一代理的请求共享此额度。

本地设置 `docs_enabled: true` 后，Swagger `/swagger/` 包含所有用户接口。生产环境默认关闭文档。未完成生产提供方配置时，不应宣称相应登录已经可用。测试使用本地签名 JWT、模拟短信和 SMTP 服务以及真实 PostgreSQL，不替代生产供应商联调。
