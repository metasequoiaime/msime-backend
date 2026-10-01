# MSIME Admin Web

技术栈：React 19、TypeScript、Vite 8、Tailwind CSS v4、TanStack Router / Query / Table、Radix UI、Recharts、Zod、pnpm 和 Biome。主题变量在 `src/styles/tokens.css`，共享组件在 `src/ui/`，外壳在 `src/shell/`，每个页面一个懒加载目录 `src/pages/<page>/`。

```sh
pnpm install --frozen-lockfile
pnpm lint
pnpm build
pnpm dev
```

后台的 CSP 是 `style-src 'self'`、不允许 eval，改动依赖或共享组件后运行 CSP 冒烟测试（需要 Python 版 Playwright 与 Chromium）：

```sh
pnpm build
python3 tests/csp_smoke.py
```

`src/` 为前端源码，`dist/` 为提交到版本库的生成产物；每次前端变更都需重新 build。Go `embed.go` 嵌入 `dist/`，Docker 的 Node 构建阶段自动重建，然后编入 Go 二进制。运行容器不需要 Node。

开发代理默认连接 `127.0.0.1:18089`，Go 本地配置需设 `admin.host: admin.localhost`。详情见 [后台文档](../docs/admin.md)。

支持 Google OIDC 管理员登录（后端授权码流程、邮箱白名单、HttpOnly 会话 Cookie），也可选保留管理员密钥登录。详见后台文档的 Google Cloud 配置步骤。
