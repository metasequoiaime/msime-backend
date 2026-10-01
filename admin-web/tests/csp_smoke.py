#!/usr/bin/env python3
"""CSP smoke test for the admin console build.

Serves admin-web/dist with the exact Content-Security-Policy that internal/server/admin.go sends, answers the shell's /api calls with fixed test fixtures, drives every route and the shell popovers in headless Chromium, and fails on any CSP violation, page error or missing shell element.

It also builds tests/harness (every shared ui component with test fixtures) into a temporary directory and exercises the data table, confirm dialog, toast, drawer and charts the same way.

Usage: python3 admin-web/tests/csp_smoke.py   (after `pnpm build`; needs pnpm and `pip install playwright && playwright install chromium`)
"""

import json
import re
import subprocess
import sys
import tempfile
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

from playwright.sync_api import expect, sync_playwright

ROOT = Path(__file__).resolve().parents[2]
DIST = ROOT / "admin-web" / "dist"
ADMIN_GO = ROOT / "internal" / "server" / "admin.go"

PAGES = {
    "/": "数据概览", "/dictpr": "词库审核", "/community": "社区审核", "/issues": "问题分诊", "/words": "敏感词库",
    "/users": "用户账号", "/downloads": "下载记录", "/notice": "公告推送", "/release": "发布管理", "/cloud": "云端监控",
    "/crash": "崩溃上报", "/status": "系统状态", "/perm": "权限日志", "/me": "个人中心",
}
REDIRECTS = {"/admins": "/perm", "/audit": "/perm", "/system": "/status", "/crashes": "/crash", "/skins": "/community", "/dictionaries": "/community", "/replies": "/community"}

# Test fixtures for the shell endpoints, shaped like gap.md section 1.
FIXTURES = {
    "/api/auth/session": {"version": "0.34.0", "authenticated": True, "email": "owner@example.com", "google_enabled": True, "token_enabled": False, "can_manage_admins": True},
    "/api/shell": {"version": "0.34.0", "environment": "测试环境", "me": {"email": "owner@example.com", "name": "Owner", "role": "maintainer", "permissions": ["review_dict_pr", "manage_permissions"]}, "pending": {"dict_prs": 2, "community": 0, "issues": 5}, "unread_notifications": 1, "status": "degraded"},
    "/api/notifications": {"items": [{"id": 1, "kind": "dict_pr", "title": "词库 PR #9 等待审核", "target_page": "dictpr", "target_id": "9", "created_at": "2026-10-01T00:00:00Z", "read": False}], "unread": 1},
    "/api/notifications/read": {"ok": True},
    "/api/search": {"items": [{"kind": "user", "id": "u1", "title": "smoke-user", "where": "用户账号", "target": "users"}]},
    "/api/auth/logout": {"ok": True},
}

# Issue triage (U3): one open issue on the list and its detail, shaped like internal/server/admin_issues.go.
SMOKE_ISSUE = {"repo": "metasequoiaime/msime", "number": 12, "title": "Shift 切换偶尔失效", "author": "smoke-author", "url": "https://github.com/metasequoiaime/msime/issues/12", "state": "new", "platform": "linux", "kind": "bug", "labels": ["bug", "linux", "快捷键"], "assignees": [], "comments": 1, "created_at": "2026-10-01T00:00:00Z", "updated_at": "2026-10-01T00:00:00Z", "closed_at": None}
FIXTURES.update({
    "/api/issues": {"items": [SMOKE_ISSUE], "page": 1, "page_size": 50, "total": 1, "has_more": False, "repos": ["metasequoiaime/msime"], "platforms": [{"id": "linux", "name": "Linux", "label": "linux", "assignee": "houko"}], "unavailable": [], "platform_counts": {"all": 1, "other": 0, "linux": 1}, "state_counts": {"new": 1, "triaged": 0, "done": 0, "dup": 0}, "stats": {"pending": 1, "triaged": 0, "new_this_week": 1, "first_response_hours": 2.5, "first_response_samples": 1}},
    "/api/issues/metasequoiaime/msime/12": {"issue": {**SMOKE_ISSUE, "body": "连按 Shift 偶尔没有反应。"}, "timeline": [{"kind": "created", "actor": "smoke-author", "text": "", "at": "2026-10-01T00:00:00Z"}, {"kind": "commented", "actor": "houko", "text": "需要日志", "at": "2026-10-01T01:00:00Z"}], "similar": [], "platform_assignee": "houko"},
})


def production_csp() -> str:
    match = re.search(r'Header\(\)\.Set\("Content-Security-Policy", "([^"]+)"\)', ADMIN_GO.read_text())
    if not match:
        raise SystemExit(f"could not find the admin CSP in {ADMIN_GO}")
    return match.group(1)


class Handler(BaseHTTPRequestHandler):
    csp = ""
    harness = Path()
    logged_out = False

    def log_message(self, *_args):
        pass

    def send(self, status: int, body: bytes, content_type: str):
        self.send_response(status)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Content-Security-Policy", self.csp)
        self.send_header("Cache-Control", "no-store")
        self.send_header("X-Content-Type-Options", "nosniff")
        self.end_headers()
        self.wfile.write(body)

    def api(self):
        path = self.path.split("?", 1)[0]
        if path == "/api/auth/logout":
            Handler.logged_out = True
        body = FIXTURES.get(path)
        if path == "/api/auth/session" and Handler.logged_out:
            body = {**FIXTURES[path], "authenticated": False, "email": ""}
        if body is None:
            return self.send(404, json.dumps({"error": {"code": "not_found", "message": "not_found"}}).encode(), "application/json")
        return self.send(200, json.dumps(body).encode(), "application/json")

    def do_POST(self):
        length = int(self.headers.get("Content-Length") or 0)
        self.rfile.read(length)
        if self.path.startswith("/api/"):
            return self.api()
        return self.send(405, b"", "text/plain")

    def do_GET(self):
        if self.path.startswith("/api/"):
            return self.api()
        path = self.path.split("?", 1)[0]
        if path == "/harness/":
            return self.send(200, (self.harness / "index.html").read_bytes(), "text/html; charset=utf-8")
        if path.startswith("/assets/") or path.startswith("/harness/assets/"):
            file = (self.harness / path.removeprefix("/harness/")) if path.startswith("/harness/") else DIST / path.lstrip("/")
            if not file.is_file():
                return self.send(404, b"", "text/plain")
            types = {".js": "text/javascript", ".css": "text/css", ".png": "image/png", ".woff2": "font/woff2", ".woff": "font/woff"}
            return self.send(200, file.read_bytes(), types.get(file.suffix, "application/octet-stream"))
        if path in PAGES or path in REDIRECTS:
            return self.send(200, (DIST / "index.html").read_bytes(), "text/html; charset=utf-8")
        return self.send(404, b"", "text/plain")


class Server(ThreadingHTTPServer):
    # The browser aborts superseded requests (prefetches, cancelled queries); a closed socket is not a test failure.
    def handle_error(self, request, client_address):
        if not isinstance(sys.exc_info()[1], (BrokenPipeError, ConnectionResetError)):
            super().handle_error(request, client_address)


def main() -> int:
    if not (DIST / "index.html").is_file():
        raise SystemExit("admin-web/dist is missing; run pnpm build first")
    Handler.csp = production_csp()
    harness_dir = tempfile.TemporaryDirectory(prefix="msime-admin-harness-")
    subprocess.run(["pnpm", "exec", "vite", "build", "--logLevel", "warn", "--config", "tests/harness/vite.config.ts", "--outDir", harness_dir.name], cwd=ROOT / "admin-web", check=True)
    Handler.harness = Path(harness_dir.name)
    server = Server(("127.0.0.1", 0), Handler)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    base = f"http://127.0.0.1:{server.server_address[1]}"
    problems: list[str] = []
    try:
        with sync_playwright() as playwright:
            browser = playwright.chromium.launch()
            try:
                context = browser.new_context(viewport={"width": 1280, "height": 860})
                context.add_init_script("window.__csp = []; document.addEventListener('securitypolicyviolation', e => window.__csp.push(e.violatedDirective + ' ' + (e.blockedURI || 'inline') + ' ' + (e.sourceFile || '') + ':' + e.lineNumber));")
                page = context.new_page()
                page.on("pageerror", lambda error: problems.append(f"page error: {error}"))
                page.on("console", lambda message: problems.append(f"console {message.type}: {message.text}") if message.type == "error" and "Failed to load resource" not in message.text else None)

                def violations(label: str):
                    found = page.evaluate("window.__csp.splice(0)")
                    problems.extend(f"CSP violation on {label}: {item}" for item in found)

                for path, title in PAGES.items():
                    page.goto(base + path)
                    expect(page.get_by_role("complementary", name="后台导航")).to_be_visible()
                    expect(page.locator("header h1")).to_have_text(title)
                    violations(path)

                for old, new in REDIRECTS.items():
                    page.goto(base + old)
                    page.wait_for_url(base + new)
                    violations(old)

                page.goto(base + "/")
                expect(page.locator("header")).to_contain_text("测试环境")
                expect(page.get_by_role("link", name=re.compile("词库审核"))).to_contain_text("2")
                expect(page.get_by_text("部分服务降级")).to_be_visible()
                # The fixture role lacks view_cloud_usage, so 云端监控 is hidden from the sidebar.
                expect(page.get_by_role("complementary", name="后台导航").get_by_role("link", name="云端监控")).to_have_count(0)

                page.get_by_role("button", name=re.compile("^外观")).click()
                page.get_by_role("radio", name="深色").click()
                expect(page.locator("html")).to_have_attribute("data-theme", "dark")
                page.get_by_role("radio", name="冬").click()
                expect(page.locator("html")).to_have_attribute("data-season", "winter")
                page.keyboard.press("Escape")
                page.reload()
                expect(page.locator("html")).to_have_attribute("data-theme", "dark")
                expect(page.locator("html")).to_have_attribute("data-season", "winter")
                violations("appearance")

                page.get_by_role("button", name=re.compile("^通知")).click()
                expect(page.get_by_text("词库 PR #9 等待审核")).to_be_visible()
                page.get_by_role("button", name="全部已读").click()
                expect(page.get_by_text("已全部标记为已读", exact=True)).to_be_visible()
                page.keyboard.press("Escape")
                violations("notifications")

                page.keyboard.press("/")
                expect(page.get_by_placeholder("搜索页面、PR、Issue、用户…")).to_be_focused()
                page.keyboard.type("smoke")
                expect(page.get_by_role("option", name=re.compile("smoke-user"))).to_be_visible()
                page.keyboard.press("Enter")
                page.wait_for_url(re.compile(r"/users\?focus=u1$"))
                violations("search")

                page.get_by_role("button", name="收起侧栏").click()
                expect(page.get_by_role("button", name="展开侧栏")).to_be_visible()
                page.reload()
                expect(page.get_by_role("button", name="展开侧栏")).to_be_visible()
                page.get_by_role("button", name="展开侧栏").click()

                page.set_viewport_size({"width": 390, "height": 800})
                expect(page.locator("#admin-navigation")).to_have_attribute("inert", "")
                page.get_by_role("button", name="打开导航").click()
                page.get_by_role("link", name=re.compile("系统状态")).first.click()
                page.wait_for_url(base + "/status")
                page.set_viewport_size({"width": 1280, "height": 860})
                violations("mobile nav")

                page.goto(base + "/issues")
                expect(page.get_by_role("table", name="Issue 列表")).to_contain_text("Shift 切换偶尔失效")
                expect(page.get_by_text("2.5 小时")).to_be_visible()
                page.get_by_text("Shift 切换偶尔失效").click()
                drawer = page.get_by_role("dialog", name=re.compile("#12 Shift 切换偶尔失效"))
                expect(drawer).to_contain_text("@houko 回复：需要日志")
                drawer.get_by_role("button", name="需要日志").click()
                expect(drawer.get_by_placeholder("回复提交者…")).to_have_value(re.compile("导出诊断日志"))
                page.keyboard.press("Escape")
                expect(drawer).to_be_hidden()
                page.goto(base + "/issues?focus=metasequoiaime%2Fmsime%2312")
                expect(page.get_by_role("dialog", name=re.compile("#12"))).to_be_visible()
                page.keyboard.press("Escape")
                violations("issues")

                page.goto(base + "/me")
                page.get_by_role("button", name="退出登录").click()
                dialog = page.get_by_role("dialog", name="退出登录？")
                expect(dialog).to_be_visible()
                page.keyboard.press("Escape")
                expect(dialog).to_be_hidden()
                page.get_by_role("button", name="退出登录").click()
                dialog.get_by_role("button", name="退出").click()
                expect(page.get_by_role("heading", name="水杉管理后台")).to_be_visible()
                violations("confirm + logout")

                page.goto(base + "/harness/")
                expect(page.get_by_role("table", name="测试表格")).to_be_visible()
                expect(page.locator(".recharts-surface").first).to_be_visible()
                page.get_by_role("checkbox", name="全选").check()
                expect(page.get_by_text("已选 3 项")).to_be_visible()
                page.get_by_role("button", name="批量驳回").click()
                dialog = page.get_by_role("dialog", name="驳回 3 项？")
                dialog.get_by_role("radio", name="原因乙").click()
                dialog.get_by_placeholder("补充说明（可选）").fill("备注")
                dialog.get_by_role("button", name="驳回").click()
                expect(page.get_by_text("已驳回：原因乙：备注", exact=True)).to_be_visible()
                expect(page.get_by_text("已选 3 项")).to_be_hidden()
                page.get_by_role("button", name="撤销").click()
                expect(page.get_by_text("已撤销", exact=True)).to_be_visible()
                page.get_by_role("checkbox", name="全选").check()
                page.get_by_role("button", name="批量失败").click()
                expect(page.get_by_text("操作失败：测试失败", exact=True)).to_be_visible()
                expect(page.get_by_text("已选 3 项")).to_be_visible()
                page.get_by_role("button", name="取消选择").click()
                page.get_by_role("button", name="延迟提交").click()
                page.get_by_role("button", name="撤销").click()
                expect(page.get_by_test_id("committed")).to_have_text("undone")
                page.get_by_role("button", name="延迟提交").click()
                expect(page.get_by_test_id("committed")).to_have_text("yes", timeout=6000)
                page.get_by_role("radio", name=re.compile("待处理")).click()
                expect(page.get_by_text("第三行")).to_be_hidden()
                page.get_by_text("第一行").click()
                drawer = page.get_by_role("dialog", name="详情")
                expect(drawer).to_be_visible()
                expect(drawer.get_by_role("img", name=re.compile("26 键"))).to_be_visible()
                page.keyboard.press("Escape")
                expect(drawer).to_be_hidden()
                violations("ui harness")
            finally:
                browser.close()
    finally:
        server.shutdown()
        harness_dir.cleanup()

    if problems:
        print("\n".join(problems), file=sys.stderr)
        return 1
    print(f"CSP smoke passed: {len(PAGES)} pages, {len(REDIRECTS)} redirects, shell popovers, ui harness; CSP {Handler.csp!r}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
